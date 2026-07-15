package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
	"ratchet/internal/trace"
)

type AnalyzeExecution struct{}

func (h *AnalyzeExecution) Verb() string { return db.VerbAnalyzeExecution }

func (h *AnalyzeExecution) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	if !job.BeadID.Valid {
		return "", fmt.Errorf("%s job %d has no bead_id", db.VerbAnalyzeExecution, job.ID)
	}
	beadID := job.BeadID.Int64

	// Load the most recent completed execution for this bead, plus the bead
	// revision's full_text (for output_files) and the project folder path.
	var execID int64
	var tracePath, terminationCause, beadFullTextJSON, folderPath, designDocPath string
	var monitorFired, monitorHonored *bool
	if err := d.QueryRowContext(ctx, `
		SELECT e.id, e.trace_path, e.termination_cause, e.monitor_fired, e.monitor_honored,
		       br.full_text, p.folder_path, p.design_doc_path
		FROM executions e
		JOIN bead_revisions br ON br.id = e.bead_revision_id
		JOIN projects p ON p.id = e.project_id
		WHERE e.bead_id = ? AND e.termination_cause IS NOT NULL
		ORDER BY e.ended_at DESC
		LIMIT 1`, beadID,
	).Scan(&execID, &tracePath, &terminationCause, &monitorFired, &monitorHonored,
		&beadFullTextJSON, &folderPath, &designDocPath); err != nil {
		return "", fmt.Errorf("load execution for bead %d: %w", beadID, err)
	}

	traceData, err := os.ReadFile(tracePath)
	if err != nil {
		return "", fmt.Errorf("read trace %s: %w", tracePath, err)
	}

	outputFileStatus := checkOutputFiles(beadFullTextJSON, folderPath)

	var beadSpec struct {
		FullText     string   `json:"full_text"`
		OutputFiles  []string `json:"output_files"`
		ExitCriteria []string `json:"exit_criteria"`
	}
	json.Unmarshal([]byte(beadFullTextJSON), &beadSpec) //nolint:errcheck — malformed JSON handled by empty slices

	// Generate mechanical_findings from structured trace data — no model call,
	// no causal-language risk.
	pt := trace.Parse(traceData)
	mechanicalFindings := trace.GenerateMechanicalFindings(
		pt, terminationCause, monitorFired, monitorHonored,
		beadSpec.ExitCriteria, outputFileStatus,
	)

	// Detect files the execute model wrote outside its declared output_files.
	// This is structural fact, not interpretation — append directly to findings.
	// A transient DB failure here must skip the check entirely, not run it
	// against an empty bead list — checkUndeclaredFiles has no way to tell
	// "no beads exist" from "the query failed," and would otherwise flag
	// every file in the project folder as undeclared.
	if allBeads, err := loadCurrentBeads(ctx, d, job.ProjectID); err != nil {
		slog.Warn("checkUndeclaredFiles: load beads failed, skipping check", "project_id", job.ProjectID, "error", err)
	} else if undeclared := checkUndeclaredFiles(folderPath, designDocPath, allBeads); undeclared != "" {
		mechanicalFindings += "\n\n" + undeclared
	}

	if compileErr := checkGoTestCompilation(ctx, folderPath); compileErr != "" {
		mechanicalFindings += "\n\n" + compileErr
	} else if testOut := checkGoTestOutput(ctx, folderPath); testOut != "" {
		mechanicalFindings += "\n\n" + testOut
	}

	if pkgErr := checkPackageMain(folderPath, beadSpec.OutputFiles); pkgErr != "" {
		mechanicalFindings += "\n\n" + pkgErr
	}

	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbAnalyzeExecution)
	if err != nil {
		return "", err
	}

	// Independent test verification after a legacy test-first attempt.
	// Skipped when REFINE_TESTS ran for this bead — tests are pre-certified.
	if isPostTestFirstState(folderPath, beadSpec.OutputFiles) && !beadHasRefinements(ctx, d, beadID) {
		if verif := verifyTestExpectations(ctx, oc, model, folderPath, beadSpec.FullText, beadSpec.OutputFiles); verif != "" {
			mechanicalFindings += "\n\n" + verif
		}
	}

	lastFailure, err := loadLastValidationFailure(ctx, d, job.ID)
	if err != nil {
		return "", fmt.Errorf("load last validation failure: %w", err)
	}

	userMsg := buildAnalyzeUserMsg(mechanicalFindings, lastFailure)
	raw, err := oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: analyzeExecutionSystemPrompt},
		{Role: "user", Content: userMsg},
	}, nil)
	if err != nil {
		return "", err
	}

	// Parse the model's interpretation-only response and assemble the full output.
	var modelOut struct {
		AnalyzerInterpretation string `json:"analyzer_interpretation"`
	}
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &modelOut); err != nil {
		return "", fmt.Errorf("parse analyzer interpretation: %w", err)
	}
	out := AnalyzeExecutionOutput{
		MechanicalFindings:     mechanicalFindings,
		AnalyzerInterpretation: modelOut.AnalyzerInterpretation,
	}
	result, _ := json.Marshal(out)
	return string(result), nil
}

// checkOutputFiles stats each file listed in the bead's output_files and
// returns a human-readable status string for inclusion in the ANALYZE prompt.
// A missing file is an objective fact — no causal language needed here.
func checkOutputFiles(beadFullTextJSON, folderPath string) string {
	var spec struct {
		OutputFiles []string `json:"output_files"`
	}
	if err := json.Unmarshal([]byte(beadFullTextJSON), &spec); err != nil || len(spec.OutputFiles) == 0 {
		return "(output_files not available)"
	}
	var sb strings.Builder
	for _, rel := range spec.OutputFiles {
		fullPath := filepath.Join(folderPath, rel)
		info, err := os.Stat(fullPath)
		if err != nil {
			fmt.Fprintf(&sb, "%s: missing\n", rel)
			continue
		}
		if strings.HasSuffix(rel, "_test.go") {
			if content, rerr := os.ReadFile(fullPath); rerr == nil {
				n := strings.Count(string(content), "\nfunc Test")
				if n == 0 && strings.HasPrefix(string(content), "func Test") {
					n = 1 // file starts with a test function (no leading newline)
				}
				fmt.Fprintf(&sb, "%s: present (%d bytes, %d test function(s))\n", rel, info.Size(), n)
				continue
			}
		}
		fmt.Fprintf(&sb, "%s: present (%d bytes)\n", rel, info.Size())
	}
	return strings.TrimRight(sb.String(), "\n")
}

func buildAnalyzeUserMsg(mechanicalFindings, lastFailure string) string {
	msg := "## Mechanical Findings\n\n" + mechanicalFindings
	if lastFailure != "" {
		msg += "\n\n## Previous Attempt Rejected\n\nYour previous attempt was rejected: " + lastFailure
	}
	return msg
}

func (h *AnalyzeExecution) Validate(raw string) (string, any) {
	var out AnalyzeExecutionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if strings.TrimSpace(out.MechanicalFindings) == "" {
		return "malformed: mechanical_findings is empty", nil
	}
	return "valid", out
}

func (h *AnalyzeExecution) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(AnalyzeExecutionOutput)
	now := time.Now().UTC().Format(time.RFC3339)

	// Load the execution_id for the most recent completed execution of this bead.
	var execID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM executions
		WHERE bead_id = ? AND termination_cause IS NOT NULL
		ORDER BY ended_at DESC LIMIT 1`,
		job.BeadID.Int64,
	).Scan(&execID); err != nil {
		return fmt.Errorf("load execution_id: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO analyses (project_id, execution_id, mechanical_findings, analyzer_interpretation, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		job.ProjectID, execID,
		out.MechanicalFindings, nullableString(out.AnalyzerInterpretation),
		now,
	); err != nil {
		return fmt.Errorf("insert analysis: %w", err)
	}

	// Enqueue COMPRESS_ANALYSIS for this bead.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?)`,
		job.ProjectID, db.VerbCompressAnalysis, job.BeadID.Int64, now, now)
	return err
}

// nullableString returns sql.NullString for the INSERT; used inline as any.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// checkUndeclaredFiles walks the project folder and reports any file that is
// present on disk but absent from every bead's output_files. These are files
// the execute model created without declaring them — invisible to ADJUDICATE's
// mechanical checks until surfaced here.
//
// Excluded from the scan: traces/ and .git/ directories; auto-generated files
// (go.sum, go.work.sum); OS metadata (.DS_Store); the project design doc;
// and compiled executables (no file extension, executable bit set).
func checkUndeclaredFiles(folderPath, designDocPath string, allBeads []beadState) string {
	expected := make(map[string]bool)
	for _, b := range allBeads {
		for _, f := range b.OutputFiles {
			expected[filepath.ToSlash(filepath.Clean(f))] = true
		}
	}
	if designDocPath != "" {
		expected[filepath.ToSlash(filepath.Clean(designDocPath))] = true
	}

	skipDirs := map[string]bool{"traces": true, ".git": true}
	skipFiles := map[string]bool{"go.sum": true, "go.work.sum": true, ".DS_Store": true}

	var undeclared []string
	_ = filepath.WalkDir(folderPath, func(path string, de os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(folderPath, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		if de.IsDir() {
			top := strings.SplitN(rel, "/", 2)[0]
			if skipDirs[top] {
				return filepath.SkipDir
			}
			return nil
		}

		base := filepath.Base(rel)
		if skipFiles[base] {
			return nil
		}

		// Skip compiled executables: no file extension and executable bit set.
		if filepath.Ext(base) == "" {
			if info, infoErr := de.Info(); infoErr == nil && info.Mode()&0o111 != 0 {
				return nil
			}
		}

		if !expected[rel] {
			undeclared = append(undeclared, rel)
		}
		return nil
	})

	if len(undeclared) == 0 {
		return ""
	}
	sort.Strings(undeclared)
	return "undeclared files (present on disk but absent from all bead output_files): " +
		strings.Join(undeclared, ", ")
}

// checkPackageMain verifies that main.go (if in output_files) declares "package main".
// A project whose files all use a non-main package name will compile as a static library —
// go build ./... succeeds but go build -o binary . produces an ar archive, not an executable.
// Returns a finding string if the violation is detected; empty string if OK or not applicable.
func checkPackageMain(folderPath string, outputFiles []string) string {
	hasmain := false
	for _, f := range outputFiles {
		if filepath.Base(f) == "main.go" {
			hasmain = true
			break
		}
	}
	if !hasmain {
		return ""
	}
	mainPath := filepath.Join(folderPath, "main.go")
	content, err := os.ReadFile(mainPath)
	if err != nil {
		return "" // main.go not yet written; nothing to check
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == "package main" {
			return ""
		}
	}
	// Identify the actual package name for a more actionable message.
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			pkg := strings.Fields(trimmed)[1]
			return fmt.Sprintf(
				"package main missing: main.go declares \"package %s\" instead of \"package main\". "+
					"go build ./... will produce a static library (ar archive), not an executable. "+
					"Every .go source file in this project must declare \"package main\".", pkg)
		}
	}
	return "package main missing: main.go does not declare \"package main\". " +
		"go build ./... will produce a static library (ar archive), not an executable. " +
		"Every .go source file in this project must declare \"package main\"."
}

// checkGoTestCompilation runs "go test -c -o /dev/null ./..." to compile all test
// files without executing any tests and without producing "no tests to run" output.
// go build ./... silently skips *_test.go files, so syntax errors or missing imports
// in test files are invisible to the exit criterion build check and accumulate across
// attempts. This catches them immediately at analysis time.
// Returns the compiler output if compilation fails, empty string otherwise.
func checkGoTestCompilation(ctx context.Context, folderPath string) string {
	if _, err := os.Stat(filepath.Join(folderPath, "go.mod")); err != nil {
		return ""
	}
	tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(tctx, "go", "test", "-c", "-o", "/dev/null", "./...")
	cmd.Dir = folderPath
	out, err := cmd.CombinedOutput()
	if err == nil {
		return ""
	}
	return "go test -c -o /dev/null ./... (compile-only check) failed:\n" + strings.TrimSpace(string(out))
}

// checkGoTestOutput runs "go test ./..." after execution and returns the output
// as a structured finding. Only runs when test files are present and compilation
// has already been confirmed clean. Provides ADJUDICATE with the authoritative
// post-execution test result — exact failure messages, not trace-embedded output.
func checkGoTestOutput(ctx context.Context, folderPath string) string {
	if _, err := os.Stat(filepath.Join(folderPath, "go.mod")); err != nil {
		return ""
	}
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return ""
	}
	hasTests := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "_test.go") {
			hasTests = true
			break
		}
	}
	if !hasTests {
		return ""
	}
	tctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(tctx, "go", "test", "./...")
	cmd.Dir = folderPath
	out, runErr := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if runErr == nil {
		return "Post-execution test run (go test ./...): all tests passed"
	}
	const maxBytes = 4000
	if len(output) > maxBytes {
		output = output[:maxBytes] + "\n[truncated]"
	}
	return "Post-execution test run (go test ./...):\n" + output
}

// isPostTestFirstState returns true when the bead has *_test.go files that
// exist on disk but non-test .go implementation files are absent. This indicates
// the previous execution was a test-first attempt: tests written, implementation not yet.
func isPostTestFirstState(folderPath string, outputFiles []string) bool {
	testExists, implMissing := false, false
	for _, f := range outputFiles {
		_, err := os.Stat(filepath.Join(folderPath, f))
		if strings.HasSuffix(f, "_test.go") {
			if err == nil {
				testExists = true
			}
		} else if strings.HasSuffix(f, ".go") {
			if err != nil {
				implMissing = true
			} else if stubs := detectStubFuncs(filepath.Join(folderPath, f), f); len(stubs) > 0 {
				implMissing = true // scaffold stub present — treat as absent
			}
		}
	}
	return testExists && implMissing
}

// verifyTestExpectations calls the ANALYZE model to independently verify test
// assertions against the bead specification. Returns a findings string for
// inclusion in mechanicalFindings; returns "" if verification is not possible
// or produces no useful signal.
func verifyTestExpectations(ctx context.Context, oc *ollama.Client, model, folderPath, beadFullText string, outputFiles []string) string {
	var testContent strings.Builder
	for _, f := range outputFiles {
		if !strings.HasSuffix(f, "_test.go") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(folderPath, f))
		if err != nil {
			continue
		}
		fmt.Fprintf(&testContent, "### %s\n\n```go\n%s\n```\n\n", f, string(content))
	}
	if testContent.Len() == 0 {
		return ""
	}

	userMsg := "## Bead Specification\n\n" + beadFullText
	userMsg += "\n\n## Test File\n\n" + strings.TrimSpace(testContent.String())

	raw, err := oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: testVerificationSystemPrompt},
		{Role: "user", Content: userMsg},
	}, nil)
	if err != nil {
		return ""
	}

	var out struct {
		TestFunctionsFound []string `json:"test_functions_found"`
		Verifications      []struct {
			TestFunction string `json:"test_function"`
			Assertion    string `json:"assertion"`
			DerivedValue string `json:"derived_value"`
			TestValue    string `json:"test_value"`
			Result       string `json:"result"`
			SpecCitation string `json:"spec_citation"`
		} `json:"verifications"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil || len(out.Verifications) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[Test-first verification] Independent review of test expectations against spec:\n")
	if len(out.TestFunctionsFound) > 0 {
		fmt.Fprintf(&sb, "  Functions found: %s\n", strings.Join(out.TestFunctionsFound, ", "))
	}
	hasMismatch := false
	for _, v := range out.Verifications {
		if v.Result == "MISMATCH" {
			hasMismatch = true
			fmt.Fprintf(&sb, "  MISMATCH %s — assertion %q: test says %q, spec implies %q (spec: %q)\n",
				v.TestFunction, v.Assertion, v.TestValue, v.DerivedValue, v.SpecCitation)
		} else {
			fmt.Fprintf(&sb, "  MATCH %s — %q\n", v.TestFunction, v.Assertion)
		}
	}
	if !hasMismatch {
		sb.WriteString("  All assertions match the specification.")
	}
	sb.WriteString("\nSummary: " + out.Summary)
	return sb.String()
}
