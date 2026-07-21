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
	"regexp"
	"sort"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
	"ratchet/internal/splice"
)

// requiredTestFuncRe matches grep -q 'func TestXxx' patterns (single or double quotes)
// in exit criteria strings to extract required test function names.
var requiredTestFuncRe = regexp.MustCompile(`grep\s+-q\s+['"]func\s+(Test\w+)['"]`)

// extractRequiredTestFuncs returns the unique ordered list of Test* function names
// that the exit criteria require to be present in the test file.
func extractRequiredTestFuncs(exitCriteria []string) []string {
	seen := map[string]bool{}
	var names []string
	for _, c := range exitCriteria {
		for _, m := range requiredTestFuncRe.FindAllStringSubmatch(c, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				names = append(names, m[1])
			}
		}
	}
	return names
}

// missingRequiredFuncs returns which names from required are absent from testPath.
// If the file cannot be read or parsed, all names are returned as missing.
func missingRequiredFuncs(testPath string, required []string) []string {
	if len(required) == 0 {
		return nil
	}
	src, err := os.ReadFile(testPath)
	if err != nil {
		return required
	}
	present, err := splice.FuncMap(string(src))
	if err != nil {
		return required
	}
	var missing []string
	for _, name := range required {
		if _, ok := present[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

// refinementCycleCap is the maximum number of write-critique-judge cycles
// per bead before escalating to the user.
const refinementCycleCap = 5

// refinementWriteAttempts is the maximum number of chat rounds within a single
// REFINE_TESTS_WRITE call to fix compile errors before giving up. One more
// than the original 3-round compile-fix budget, to leave room for the
// mandatory run_go_snippet verification round (see its use in Run) without
// crowding out compile-error retries.
const refinementWriteAttempts = 4

// --- shared helpers ---

func loadRefineContext(ctx context.Context, d *db.DB, job *db.HandoffJob) (
	bead *beadState, project *db.Project, folderPath string,
	implContext string, testFilePaths []string, currentTestContent string, err error,
) {
	if !job.BeadID.Valid {
		return nil, nil, "", "", nil, "", fmt.Errorf("job %d has no bead_id", job.ID)
	}
	bead, err = loadBeadByID(ctx, d, job.BeadID.Int64)
	if err != nil {
		return
	}
	project, err = loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return
	}
	folderPath = project.FolderPath

	// Collect non-test .go files from prior beads for domain context.
	var implBuf strings.Builder
	if entries, rdErr := os.ReadDir(folderPath); rdErr == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if content, rerr := os.ReadFile(filepath.Join(folderPath, name)); rerr == nil {
				fmt.Fprintf(&implBuf, "### %s\n\n```go\n%s\n```\n\n", name, string(content))
			}
		}
	}
	implContext = implBuf.String()

	// Collect current test file content. apiCheckTestFilename is mechanically
	// owned by writeAPICheckTest (regenerated from the SURVEY_SPEC manifest,
	// see scaffold_go.go) — it holds compile-time `var _ = X` assertions only,
	// never hand-written behavioral tests, so REFINE_TESTS must not target it.
	var testBuf strings.Builder
	for _, f := range bead.OutputFiles {
		if !strings.HasSuffix(f, "_test.go") || filepath.Base(f) == apiCheckTestFilename {
			continue
		}
		testFilePaths = append(testFilePaths, f)
		content, rerr := os.ReadFile(filepath.Join(folderPath, f))
		if rerr != nil {
			continue
		}
		fmt.Fprintf(&testBuf, "### %s\n\n```go\n%s\n```\n\n", f, string(content))
	}
	currentTestContent = testBuf.String()
	return
}

func buildBaseUserMsg(bead *beadState, folderPath string, implContext string,
	currentTestContent string, testFilePaths []string) string {
	msg := "## Bead Specification\n\n" + bead.FullText

	if prescriptive, rerr := os.ReadFile(filepath.Join(folderPath, "design_doc_prescriptive.md")); rerr == nil {
		msg += "\n\n## Prescriptive Design Document\n\n" + string(prescriptive)
	}

	if implContext != "" {
		msg += "\n\n## Implementation Files (prior beads — types and conventions)\n\n" +
			strings.TrimSpace(implContext)
	}

	if currentTestContent != "" {
		msg += "\n\n## Current Test File\n\n" + strings.TrimSpace(currentTestContent)
	} else {
		msg += "\n\n## Current Test File\n\n(No test file exists yet — write from scratch.)"
	}

	if len(testFilePaths) > 0 {
		msg += "\n\n## Test Files to Produce\n\n" + strings.Join(testFilePaths, "\n")
	}
	return msg
}

func runCompile(ctx context.Context, folderPath string) (ok bool, output string) {
	cmd := exec.CommandContext(ctx, "go", "test", "-c", "-o", os.DevNull, ".")
	cmd.Dir = folderPath
	out, err := cmd.CombinedOutput()
	return err == nil, strings.TrimSpace(string(out))
}

// maxSnippetRuntime bounds a single run_go_snippet execution so a runaway or
// looping snippet can't stall REFINE_TESTS_CRITIQUE. A var, not a const, so
// tests can shrink it rather than waiting out the real timeout.
var maxSnippetRuntime = 10 * time.Second

// runGoSnippet compiles and runs a single self-contained Go source file
// (package main, stdlib imports only) in an isolated temp directory,
// returning its combined stdout+stderr (compile errors, panics, and normal
// output are all returned this way — they're the useful signal, not an
// exec-level failure). err is reserved for infrastructure problems (temp
// dir creation, writing the file) that have nothing to do with the
// snippet's own correctness.
//
// This exists so REFINE_TESTS_CRITIQUE, ADJUDICATE_NEXT_EXECUTION, and
// REFINE_TESTS_WRITE can each verify a specific claim about Go/stdlib
// runtime behavior by actually executing it, instead of only reasoning
// about it in text. Root cause of the 2026-07-20 exprvm-web-v1 incidents
// this closes: across separate real beads, both a CRITIQUE call and an
// ADJUDICATE call reasoned in prose about (a) whether html/template escapes
// '+' in rendered output, and (b) whether two fmt.Errorf-produced errors
// compare equal with ==, and were confidently wrong — an LLM predicting the
// shape of an answer is not the same as Go actually computing it.
func runGoSnippet(ctx context.Context, src string) (output string, err error) {
	dir, mkErr := os.MkdirTemp("", "ratchet-snippet-*")
	if mkErr != nil {
		return "", fmt.Errorf("create snippet dir: %w", mkErr)
	}
	defer os.RemoveAll(dir)

	// A minimal go.mod is required for `go run` to work at all outside an
	// existing module tree (the temp dir isn't nested under one) — stdlib-only
	// snippets need no dependencies, so this never touches the network.
	if werr := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module snippet\n\ngo 1.21\n"), 0o644); werr != nil {
		return "", fmt.Errorf("write snippet go.mod: %w", werr)
	}
	if werr := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); werr != nil {
		return "", fmt.Errorf("write snippet main.go: %w", werr)
	}

	runCtx, cancel := context.WithTimeout(ctx, maxSnippetRuntime)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "go", "run", "main.go")
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("snippet exceeded %s timeout", maxSnippetRuntime)
	}
	result := strings.TrimSpace(string(out))
	if runErr != nil && result == "" {
		result = runErr.Error()
	}
	return result, nil
}


func cycleID(job *db.HandoffJob) int64 {
	if job.RefinementCycleID.Valid {
		return job.RefinementCycleID.Int64
	}
	return 1
}

func insertRefinement(ctx context.Context, tx *sql.Tx, projectID, beadID, cycle int64,
	verb, summary, decision string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO test_refinements (project_id, bead_id, cycle_id, turn, verb, changed, summary, decision, created_at)
		VALUES (?, ?, ?, (SELECT COALESCE(MAX(turn),0)+1 FROM test_refinements WHERE bead_id=? AND cycle_id=?), ?, 0, ?, ?, ?)`,
		projectID, beadID, cycle, beadID, cycle, verb, summary, decision, now)
	return err
}

// --- REFINE_TESTS_WRITE ---

type RefineTestsWrite struct{}

func (h *RefineTestsWrite) Verb() string { return db.VerbRefineTestsWrite }

// writeFunctionTool is the sole tool available to REFINE_TESTS_WRITE.
// The model writes one test function per call; Ratchet assembles/splices.
var writeFunctionTool = ollama.Tool{
	Type: "function",
	Function: ollama.ToolFunction{
		Name:        "write_function",
		Description: "Submit a test function. Call once per function. Do not include package declarations or imports.",
		Parameters: ollama.ToolParameters{
			Type: "object",
			Properties: map[string]ollama.ToolProperty{
				"name": {Type: "string", Description: "Exact function name, must start with 'Test'"},
				"body": {Type: "string", Description: "Complete function from 'func TestXxx' through the closing '}'"},
			},
			Required: []string{"name", "body"},
		},
	},
}

func (h *RefineTestsWrite) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	bead, _, folderPath, implContext, testFilePaths, _, err := loadRefineContext(ctx, d, job)
	if err != nil {
		return "", err
	}
	if len(testFilePaths) == 0 {
		return "", fmt.Errorf("no test file paths for bead %d", job.BeadID.Int64)
	}

	model, err := loadVerbModel(ctx, d, job.ProjectID, h.Verb())
	if err != nil {
		return "", err
	}

	// Restore any missing implementation files as scaffold stubs so the compile
	// check succeeds. Handles the case where rewind-bead deleted impl files.
	if restoreErr := restoreMissingScaffolds(ctx, d, job.ProjectID, folderPath, bead.OutputFiles); restoreErr != nil {
		return "", fmt.Errorf("restore missing scaffolds: %w", restoreErr)
	}

	cid := cycleID(job)
	beadID := job.BeadID.Int64
	testPath := filepath.Join(folderPath, testFilePaths[0])
	requiredFuncs := extractRequiredTestFuncs(bead.ExitCriteria)

	// Save original file content before any writes (needed for verified-function lock).
	var originalSrc string
	if origBytes, rerr := os.ReadFile(testPath); rerr == nil {
		originalSrc = string(origBytes)
	}

	// Cycle 1: write all functions from scratch.
	// Cycle 2+: only rewrite functions flagged by the prior JUDGE.
	var allowedFuncs map[string]bool // nil = unrestricted (cycle 1)
	var userMsg string

	if cid == 1 {
		userMsg = buildFirstWriteMsg(bead, folderPath, implContext)
	} else {
		judgeOut, jErr := loadPriorJudgeOutput(ctx, d, beadID, cid-1)
		if jErr != nil {
			return "", fmt.Errorf("load judge output for cycle %d: %w", cid-1, jErr)
		}
		allowedFuncs = make(map[string]bool, len(judgeOut.FunctionsToRewrite))
		for _, name := range judgeOut.FunctionsToRewrite {
			allowedFuncs[name] = true
		}

		// Extract the current bodies of broken functions from disk.
		brokenBodies := make(map[string]string)
		if originalSrc != "" {
			if fm, fmErr := splice.FuncMap(originalSrc); fmErr == nil {
				for name := range allowedFuncs {
					brokenBodies[name] = fm[name]
				}
			}
		}
		userMsg = buildRevisionWriteMsg(bead, folderPath, implContext, brokenBodies, string(judgeOut.Instructions))
	}

	messages := []ollama.Message{
		{Role: "system", Content: refineTestsWriteSystemPrompt},
		{Role: "user", Content: userMsg},
	}

	// writtenFuncs collects accepted function bodies; funcOrder preserves
	// insertion order for cycle-1 assembly (Go maps are unordered).
	writtenFuncs := make(map[string]string)
	var funcOrder []string
	var summary string

	// usedSnippet tracks whether run_go_snippet has been called at least
	// once. Required before either exit path below (no-tool-calls, or a
	// clean compile) is allowed to finish — see its gate at each. This is
	// the same enforcement REFINE_TESTS_CRITIQUE and ADJUDICATE_NEXT_EXECUTION
	// use and for the same reason: a test-fixture choice that "feels obviously
	// fine" is exactly as likely to be wrong as one the model would flag as
	// uncertain (e.g. picking "1 + 1" as fixture text for an assertion
	// against html/template-rendered output, not realizing '+' gets escaped)
	// — catching that here, before the function is even submitted, is
	// earlier and cheaper than catching it in CRITIQUE.
	usedSnippet := false
	for turn := 1; turn <= refinementWriteAttempts; turn++ {
		msg, toolErr := oc.ChatWithTools(ctx, model, messages, []ollama.Tool{writeFunctionTool, runGoSnippetTool}, nil, nil)
		if toolErr != nil {
			return "", toolErr
		}
		if strings.TrimSpace(msg.Content) != "" {
			summary = strings.TrimSpace(msg.Content)
		}
		if len(msg.ToolCalls) == 0 {
			if usedSnippet || turn == refinementWriteAttempts {
				if !usedSnippet {
					slog.Warn("REFINE_TESTS_WRITE finalized without ever calling run_go_snippet",
						"bead_id", beadID, "turn", turn)
				}
				break
			}
			messages = append(messages, ollama.Message{Role: "user", Content: "Before finishing, call " +
				"run_go_snippet at least once to verify a specific runtime-behavior assumption behind one " +
				"of your assertions (e.g. how a rendering/formatting/escaping function actually treats a " +
				"fixture value you used) — even if you feel confident. Then continue."})
			continue
		}

		messages = append(messages, msg)
		for _, tc := range msg.ToolCalls {
			var result string
			switch tc.Function.Name {
			case "run_go_snippet":
				if src, _ := tc.Function.Arguments["source"].(string); strings.TrimSpace(src) == "" {
					result = "error: source is empty"
				} else if out, rerr := runGoSnippet(ctx, src); rerr != nil {
					result = fmt.Sprintf("error: %v", rerr)
				} else if out == "" {
					result = "(snippet ran with no output — add a print statement to see a result)"
				} else {
					result = out
				}
				usedSnippet = true
			case "write_function":
				name, _ := tc.Function.Arguments["name"].(string)
				body, _ := tc.Function.Arguments["body"].(string)
				switch {
				case !strings.HasPrefix(name, "Test"):
					result = fmt.Sprintf("error: name %q must start with 'Test'", name)
				case cid > 1 && !allowedFuncs[name]:
					allowed := make([]string, 0, len(allowedFuncs))
					for k := range allowedFuncs {
						allowed = append(allowed, k)
					}
					result = fmt.Sprintf("error: %q is not in the list of functions to rewrite; allowed: %s", name, strings.Join(allowed, ", "))
				case !strings.HasPrefix(strings.TrimSpace(body), "func "):
					result = "error: body must begin with 'func '"
				default:
					if _, exists := writtenFuncs[name]; !exists {
						funcOrder = append(funcOrder, name)
					}
					writtenFuncs[name] = body
					result = fmt.Sprintf("ok: accepted %s (%d bytes)", name, len(body))
					slog.Info("REFINE_TESTS_WRITE: function accepted", "name", name, "bytes", len(body))
				}
			default:
				result = fmt.Sprintf("error: unknown tool %q — only write_function and run_go_snippet are available", tc.Function.Name)
			}
			messages = append(messages, ollama.Message{Role: "tool", Content: result})
		}

		// Assemble or splice, write to disk, compile check.
		if len(writtenFuncs) == 0 {
			continue
		}
		var fileContent string
		if originalSrc == "" {
			// No prior content at this path (fresh file — the common cid==1
			// case) — assemble from scratch.
			funcs := make([]string, 0, len(funcOrder))
			for _, name := range funcOrder {
				funcs = append(funcs, writtenFuncs[name])
			}
			fileContent, _ = splice.Assemble(splice.DetectPackage(folderPath), funcs)
		} else {
			// The path already has content — whether from this bead's own
			// earlier cycle, or from a prior bead sharing this test file —
			// splice onto it rather than discarding it.
			fileContent = originalSrc
			for _, name := range funcOrder {
				fileContent, _ = splice.Replace(fileContent, name, writtenFuncs[name])
			}
		}
		if err := os.WriteFile(testPath, []byte(fileContent), 0o644); err != nil {
			return "", fmt.Errorf("write test file: %w", err)
		}

		ok, compileOut := runCompile(ctx, folderPath)
		if ok {
			// Completeness check: verify all required Test* functions are present.
			if missing := missingRequiredFuncs(testPath, requiredFuncs); len(missing) > 0 && turn < refinementWriteAttempts {
				slog.Warn("REFINE_TESTS_WRITE: compile passed but required functions missing",
					"bead_id", beadID, "turn", turn, "missing", missing)
				messages = append(messages, ollama.Message{
					Role: "user",
					Content: "Compile passed, but the following required test functions are still missing: " +
						strings.Join(missing, ", ") +
						". Call write_function once for each missing function.",
				})
				continue
			}
			slog.Info("REFINE_TESTS_WRITE: compile passed", "bead_id", beadID, "turn", turn)
			if summary == "" {
				summary = "Test functions written and compiling."
			}
			if usedSnippet || turn == refinementWriteAttempts {
				if !usedSnippet {
					slog.Warn("REFINE_TESTS_WRITE finalized without ever calling run_go_snippet",
						"bead_id", beadID, "turn", turn)
				}
				break
			}
			messages = append(messages, ollama.Message{Role: "user", Content: "Before finishing, call " +
				"run_go_snippet at least once to verify a specific runtime-behavior assumption behind one " +
				"of your assertions (e.g. how a rendering/formatting/escaping function actually treats a " +
				"fixture value you used) — even if you feel confident. Then continue."})
			continue
		}
		slog.Error("REFINE_TESTS_WRITE: compile failed", "bead_id", beadID, "turn", turn, "output", compileOut)
		if turn < refinementWriteAttempts {
			messages = append(messages, ollama.Message{
				Role:    "user",
				Content: "Compile failed:\n```\n" + compileOut + "\n```\nFix the errors in the affected function(s). Call write_function again with the corrected body.",
			})
		}
	}

	// Verified-function lock: restore any verified functions WRITE changed.
	// This is a safety net — the tool constraint should prevent writes to
	// verified functions, but belt-and-suspenders is cheap here.
	if cid > 1 && originalSrc != "" {
		verifiedSet, _ := loadVerifiedFunctionSet(ctx, d, beadID)
		// JUDGE's rewrite decision takes precedence: if JUDGE flagged a function
		// for rewriting, it is not truly verified regardless of what CRITIQUE said.
		for name := range allowedFuncs {
			delete(verifiedSet, name)
		}
		if len(verifiedSet) > 0 {
			currentBytes, _ := os.ReadFile(testPath)
			currentSrc := string(currentBytes)
			origFuncs, _ := splice.FuncMap(originalSrc)
			restoredAny := false
			for name := range verifiedSet {
				if origBody, ok := origFuncs[name]; ok {
					restored, rErr := splice.Replace(currentSrc, name, origBody)
					if rErr == nil && restored != currentSrc {
						currentSrc = restored
						restoredAny = true
						slog.Warn("REFINE_TESTS_WRITE: restored verified function", "name", name)
					}
				}
			}
			if restoredAny {
				_ = os.WriteFile(testPath, []byte(currentSrc), 0o644)
			}
		}
	}

	if summary == "" {
		summary = "Test function write attempted."
	}
	out, _ := json.Marshal(RefineTestsWriteOutput{Summary: summary})
	return string(out), nil
}

// buildFirstWriteMsg builds the user message for WRITE on cycle 1 (no existing file).
func buildFirstWriteMsg(bead *beadState, folderPath, implContext string) string {
	msg := "## Bead Specification\n\n" + bead.FullText
	if prescriptive, rerr := os.ReadFile(filepath.Join(folderPath, "design_doc_prescriptive.md")); rerr == nil {
		msg += "\n\n## Prescriptive Design Document\n\n" + string(prescriptive)
	}
	if implContext != "" {
		msg += "\n\n## Implementation Files (prior beads — types and conventions)\n\n" + strings.TrimSpace(implContext)
	}
	msg += "\n\n## Task\n\nWrite test functions covering every behavior required by the spec above. " +
		"Call write_function once per test function. " +
		"Do not write package declarations, import statements, or helper functions — only Test* functions."
	if required := extractRequiredTestFuncs(bead.ExitCriteria); len(required) > 0 {
		msg += "\n\nYou MUST write ALL of the following functions (one write_function call each):\n"
		for _, name := range required {
			msg += fmt.Sprintf("- %s\n", name)
		}
	}
	return msg
}

// buildRevisionWriteMsg builds the user message for WRITE on cycle 2+ (rewriting broken functions).
func buildRevisionWriteMsg(bead *beadState, folderPath, implContext string, brokenBodies map[string]string, instructions string) string {
	msg := "## Functions to Rewrite\n\n"
	for name, body := range brokenBodies {
		msg += fmt.Sprintf("### %s (current body)\n\n```go\n%s\n```\n\n", name, body)
	}
	msg += "## Fix Instructions\n\n" + instructions
	if prescriptive, rerr := os.ReadFile(filepath.Join(folderPath, "design_doc_prescriptive.md")); rerr == nil {
		msg += "\n\n## Reference: Prescriptive Design Document\n\n" + string(prescriptive)
	}
	if implContext != "" {
		msg += "\n\n## Reference: Implementation Files\n\n" + strings.TrimSpace(implContext)
	}
	msg += "\n\n## Reference: Bead Specification\n\n" + bead.FullText
	msg += "\n\n## Task\n\nRewrite only the functions listed above, applying every fix instruction. " +
		"Call write_function exactly once per function listed. Do not write any other function."
	return msg
}

// loadPriorJudgeOutput fetches the validated JUDGE raw output for the given
// bead and cycle from handoff_attempts.
func loadPriorJudgeOutput(ctx context.Context, d *db.DB, beadID, cid int64) (*RefineTestsJudgeOutput, error) {
	var raw string
	err := d.QueryRowContext(ctx, `
		SELECT ha.raw_output FROM handoff_attempts ha
		JOIN handoff_jobs hj ON hj.id = ha.job_id
		WHERE hj.bead_id = ? AND hj.verb = ? AND hj.refinement_cycle_id = ?
		  AND ha.validation_result = 'valid'
		ORDER BY ha.id DESC LIMIT 1`,
		beadID, db.VerbRefineTestsJudge, cid,
	).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("query judge output bead=%d cycle=%d: %w", beadID, cid, err)
	}
	var out RefineTestsJudgeOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return nil, fmt.Errorf("parse judge output: %w", err)
	}
	return &out, nil
}

// loadVerifiedFunctionSet unions all verified_functions reported by CRITIQUE
// across all cycles for beadID.
func loadVerifiedFunctionSet(ctx context.Context, d *db.DB, beadID int64) (map[string]bool, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT ha.raw_output FROM handoff_attempts ha
		JOIN handoff_jobs hj ON hj.id = ha.job_id
		WHERE hj.bead_id = ? AND hj.verb = ? AND ha.validation_result = 'valid'
		ORDER BY ha.id ASC`,
		beadID, db.VerbRefineTestsCritique)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		var out RefineTestsCritiqueOutput
		if json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out) != nil {
			continue
		}
		for _, name := range out.VerifiedFunctions {
			result[name] = true
		}
	}
	return result, rows.Err()
}

func (h *RefineTestsWrite) Validate(rawOutput string) (string, any) {
	var out RefineTestsWriteOutput
	if err := json.Unmarshal([]byte(rawOutput), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if strings.TrimSpace(out.Summary) == "" {
		return "malformed: summary is empty", nil
	}
	return "valid", out
}

func (h *RefineTestsWrite) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(RefineTestsWriteOutput)
	beadID := job.BeadID.Int64
	cid := cycleID(job)
	now := time.Now().UTC().Format(time.RFC3339)

	var folderPath string
	if err := tx.QueryRowContext(ctx, `SELECT folder_path FROM projects WHERE id = ?`, job.ProjectID).Scan(&folderPath); err != nil {
		return fmt.Errorf("load folder_path: %w", err)
	}

	slog.Info("REFINE_TESTS_WRITE complete", "bead_id", beadID, "cycle_id", cid, "summary", out.Summary)

	if err := insertRefinement(ctx, tx, job.ProjectID, beadID, cid, h.Verb(), out.Summary, ""); err != nil {
		return fmt.Errorf("insert test_refinement: %w", err)
	}

	// Check compile state of what's now on disk.
	ok, compileOut := runCompile(ctx, folderPath)
	if !ok {
		slog.Error("REFINE_TESTS_WRITE: compile still failing after all attempts — escalating",
			"bead_id", beadID, "cycle_id", cid, "output", compileOut)
		_, err := tx.ExecContext(ctx,
			`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`, now, job.ID)
		return err
	}

	// Enqueue CRITIQUE for this cycle.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		job.ProjectID, db.VerbRefineTestsCritique, beadID, cid, now, now)
	return err
}

// --- REFINE_TESTS_CRITIQUE ---

// runGoSnippetTool is available to REFINE_TESTS_CRITIQUE so it can verify a
// specific, narrow claim about Go/stdlib runtime behavior by actually
// executing it (via runGoSnippet) instead of only reasoning about it in
// text. Not a general sandbox — the model doesn't need project files or
// state to check "does html/template escape '+'" or "does comparing these
// two errors with == succeed"; it only needs a stdlib-only, self-contained
// program.
var runGoSnippetTool = ollama.Tool{
	Type: "function",
	Function: ollama.ToolFunction{
		Name: "run_go_snippet",
		Description: "Compile and run a small, self-contained Go program to verify a specific factual claim " +
			"about Go or standard-library runtime behavior (e.g. does a given string survive a particular " +
			"escaping/formatting function unchanged, does a comparison evaluate as expected). Use this before " +
			"citing any such claim in a finding — do not guess or rely on general impressions of how Go " +
			"behaves. The program must be complete (package main, func main, stdlib imports only) and print " +
			"whatever result answers your question.",
		Parameters: ollama.ToolParameters{
			Type: "object",
			Properties: map[string]ollama.ToolProperty{
				"source": {Type: "string", Description: "Complete Go source: 'package main' through the final '}', stdlib imports only."},
			},
			Required: []string{"source"},
		},
	},
}

// snippetVerificationTurns bounds how many run_go_snippet round-trips a
// single REFINE_TESTS_CRITIQUE or ADJUDICATE_NEXT_EXECUTION call may use
// before it must give its final verdict/decision — enough for a handful of
// independent verifications without letting the call run indefinitely.
const snippetVerificationTurns = 6

type RefineTestsCritique struct{}

func (h *RefineTestsCritique) Verb() string { return db.VerbRefineTestsCritique }

func (h *RefineTestsCritique) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	bead, _, _, implContext, _, currentTestContent, err := loadRefineContext(ctx, d, job)
	if err != nil {
		return "", err
	}

	model, err := loadVerbModel(ctx, d, job.ProjectID, h.Verb())
	if err != nil {
		return "", err
	}

	userMsg := "## Bead Specification\n\n" + bead.FullText
	if implContext != "" {
		userMsg += "\n\n## Implementation Files (prior beads — types and conventions)\n\n" +
			strings.TrimSpace(implContext)
	}
	userMsg += "\n\n## Current Test File\n\n" + strings.TrimSpace(currentTestContent)

	messages := []ollama.Message{
		{Role: "system", Content: refineTestsCritiqueSystemPrompt},
		{Role: "user", Content: userMsg},
	}

	// Tool loop: the model may call run_go_snippet to verify a runtime-
	// behavior claim before giving its final JSON verdict. Mirrors
	// RefineTestsWrite's turn loop below, with one addition: a final
	// (zero-tool-call) response is only accepted once at least one snippet
	// has actually been run.
	//
	// Why: "verify when you're not certain" (the original prompt wording)
	// only helps when the model perceives itself as uncertain. The 2026-07-20
	// exprvm-web-v1 bead 34 incident showed the opposite failure — CRITIQUE
	// stated, as flat unhedged fact, that html/template does not escape '+'
	// (reversing a correct fix ADJUDICATE had just made), with no sign the
	// tool was ever invoked. A model that's confidently wrong never judges
	// itself uncertain, so a conditional trigger never fires for exactly the
	// failure mode this tool exists to catch. Removing the model's
	// discretion over *whether* to verify — not just giving it the option —
	// is the fix; every real review has at least one runtime-behavior
	// assumption buried in it (satisfiability tracing per the system
	// prompt's clause (b) requires this anyway), so this isn't asking for
	// busywork on a genuinely tool-free review.
	var lastContent string
	usedTool := false
	for turn := 1; turn <= snippetVerificationTurns; turn++ {
		msg, toolErr := oc.ChatWithTools(ctx, model, messages, []ollama.Tool{runGoSnippetTool}, nil, nil)
		if toolErr != nil {
			return "", toolErr
		}
		if strings.TrimSpace(msg.Content) != "" {
			lastContent = msg.Content
		}
		if len(msg.ToolCalls) == 0 {
			if usedTool || turn == snippetVerificationTurns {
				// Accept: either it verified something, or the turn budget is
				// exhausted — fall back to whatever it gave rather than fail
				// the job outright. Logged so an unverified verdict is
				// visible, not silent.
				if !usedTool {
					slog.Warn("REFINE_TESTS_CRITIQUE finalized without ever calling run_go_snippet",
						"bead_id", job.BeadID.Int64, "turn", turn)
				}
				return msg.Content, nil
			}
			messages = append(messages, msg)
			messages = append(messages, ollama.Message{Role: "user", Content: "Before giving your final " +
				"verdict, call run_go_snippet at least once to verify a specific claim from your review " +
				"against actual Go behavior — even if you feel confident. Confidence is not evidence; that " +
				"gap is exactly what this tool exists to close. Then give your final JSON verdict."})
			continue
		}

		messages = append(messages, msg)
		for _, tc := range msg.ToolCalls {
			var result string
			if tc.Function.Name != "run_go_snippet" {
				result = fmt.Sprintf("error: unknown tool %q — only run_go_snippet is available", tc.Function.Name)
			} else if src, _ := tc.Function.Arguments["source"].(string); strings.TrimSpace(src) == "" {
				result = "error: source is empty"
			} else if out, rerr := runGoSnippet(ctx, src); rerr != nil {
				result = fmt.Sprintf("error: %v", rerr)
			} else if out == "" {
				result = "(snippet ran with no output — add a print statement to see a result)"
			} else {
				result = out
			}
			usedTool = true
			messages = append(messages, ollama.Message{Role: "tool", Content: result})
		}
	}
	// Turn cap reached without a final non-tool response — return whatever
	// content the model last produced (Validate will reject it as malformed
	// if it isn't valid JSON, triggering the normal retry path).
	return lastContent, nil
}

func (h *RefineTestsCritique) Validate(rawOutput string) (string, any) {
	var out RefineTestsCritiqueOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(rawOutput)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if strings.TrimSpace(out.Summary) == "" {
		return "malformed: summary is empty", nil
	}
	return "valid", out
}

func (h *RefineTestsCritique) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(RefineTestsCritiqueOutput)
	beadID := job.BeadID.Int64
	cid := cycleID(job)
	now := time.Now().UTC().Format(time.RFC3339)

	slog.Info("REFINE_TESTS_CRITIQUE complete", "bead_id", beadID, "cycle_id", cid,
		"all_correct", out.AllCorrect, "findings", len(out.Findings), "summary", out.Summary)

	if err := insertRefinement(ctx, tx, job.ProjectID, beadID, cid, h.Verb(), out.Summary, ""); err != nil {
		return fmt.Errorf("insert test_refinement: %w", err)
	}

	// Enqueue JUDGE for this cycle.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		job.ProjectID, db.VerbRefineTestsJudge, beadID, cid, now, now)
	return err
}

// --- REFINE_TESTS_JUDGE ---

type RefineTestsJudge struct {
	// fromAdjudicateReRefine/adjudicateGuidance/currentTestContent cache
	// state from Run for Validate to enforce a mechanical override: see
	// their use in Validate for why this exists (2026-07-20 exprvm-web-v1
	// bead 33 incident).
	fromAdjudicateReRefine bool
	adjudicateGuidance     string
	currentTestContent     string
}

func (h *RefineTestsJudge) Verb() string { return db.VerbRefineTestsJudge }

func (h *RefineTestsJudge) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	_, _, _, _, _, currentTestContent, err := loadRefineContext(ctx, d, job)
	if err != nil {
		return "", err
	}
	h.currentTestContent = currentTestContent

	model, err := loadVerbModel(ctx, d, job.ProjectID, h.Verb())
	if err != nil {
		return "", err
	}

	cid := cycleID(job)

	// Prefer the full structured JSON from the critique's raw output over the summary.
	var critiqueRaw string
	_ = d.QueryRowContext(ctx, `
		SELECT ha.raw_output FROM handoff_attempts ha
		JOIN handoff_jobs hj ON hj.id = ha.job_id
		WHERE hj.project_id = ? AND hj.verb = ? AND hj.bead_id = ? AND hj.refinement_cycle_id = ?
		  AND ha.validation_result = 'valid'
		ORDER BY ha.id DESC LIMIT 1`,
		job.ProjectID, db.VerbRefineTestsCritique, job.BeadID.Int64, cid,
	).Scan(&critiqueRaw)

	if critiqueRaw == "" {
		// Fallback: use the summary stored in test_refinements. Reaching this
		// branch means no real REFINE_TESTS_CRITIQUE job ran this cycle — the
		// only way that happens is ADJUDICATE_NEXT_EXECUTION's re_refine
		// decision injecting its own diagnosis directly (see its Commit,
		// "Inject ADJUDICATE's diagnosis as CRITIQUE findings"). That
		// diagnosis is not shaped like a real CRITIQUE's structured
		// findings/all_correct JSON — it's free-form, often hedged prose
		// ("if this is failing consistently... the expected value ... is
		// incorrect") — and JUDGE's prompt, written assuming CRITIQUE-shaped
		// input, is not reliably recognizing it as "genuine correctness
		// problems". Track this so Validate can enforce the already-made
		// upstream decision rather than let JUDGE silently rubber-stamp a
		// test ADJUDICATE has already diagnosed as broken.
		h.fromAdjudicateReRefine = true
		_ = d.QueryRowContext(ctx, `
			SELECT summary FROM test_refinements
			WHERE bead_id = ? AND verb = ? AND cycle_id = ?
			ORDER BY created_at DESC LIMIT 1`,
			job.BeadID.Int64, db.VerbRefineTestsCritique, cid,
		).Scan(&critiqueRaw)
	}
	h.adjudicateGuidance = critiqueRaw

	userMsg := "## Test File\n\n" + strings.TrimSpace(currentTestContent)
	userMsg += "\n\n## Critique Findings\n\n" + critiqueRaw

	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: refineTestsJudgeSystemPrompt},
		{Role: "user", Content: userMsg},
	}, nil)
}

func (h *RefineTestsJudge) Validate(rawOutput string) (string, any) {
	var out RefineTestsJudgeOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(rawOutput)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if strings.TrimSpace(out.Summary) == "" {
		return "malformed: summary is empty", nil
	}
	if out.Decision != "approved" && out.Decision != "revise" {
		return fmt.Sprintf("malformed: decision must be 'approved' or 'revise', got %q", out.Decision), nil
	}
	if out.Decision == "revise" && strings.TrimSpace(string(out.Instructions)) == "" {
		return "malformed: decision is 'revise' but instructions is empty", nil
	}
	if out.Decision == "revise" && len(out.FunctionsToRewrite) == 0 {
		return "malformed: decision is 'revise' but functions_to_rewrite is empty", nil
	}

	// Mechanical override: this cycle's "critique findings" came from
	// ADJUDICATE_NEXT_EXECUTION's re_refine diagnosis, not a real
	// REFINE_TESTS_CRITIQUE call — that decision has already been made
	// upstream (ADJUDICATE only reaches re_refine on strong mechanical
	// evidence: byte-identical failure across genuinely different
	// implementations, or an explicit forced override — see
	// adjudicate_next_execution.go). JUDGE re-approving here isn't a
	// legitimate second opinion, it's silently discarding that diagnosis: the
	// test stays unchanged, and the bead is guaranteed to fail EXECUTE_BEAD
	// identically again. Force revise, targeting every top-level Test* func
	// in the current file (a general fallback since the diagnosis names
	// subtests, not top-level function names) with the diagnosis itself as
	// instructions.
	if h.fromAdjudicateReRefine && out.Decision == "approved" {
		funcs, ferr := splice.FuncMap(h.currentTestContent)
		var names []string
		if ferr == nil {
			for name := range funcs {
				if strings.HasPrefix(name, "Test") {
					names = append(names, name)
				}
			}
			sort.Strings(names)
		}
		if len(names) > 0 && strings.TrimSpace(h.adjudicateGuidance) != "" {
			slog.Warn("REFINE_TESTS_JUDGE decision mechanically overridden to revise",
				"model_decision", out.Decision, "forced_functions", names)
			out.Decision = "revise"
			out.FunctionsToRewrite = names
			out.Instructions = flexString("[Forced by the orchestrator — ADJUDICATE_NEXT_EXECUTION already " +
				"diagnosed this test via re_refine; approving it here would silently discard that diagnosis " +
				"and guarantee an identical failure on the next attempt.]\n\n" + h.adjudicateGuidance)
			out.Summary = "forced revision — ADJUDICATE's re_refine diagnosis was not acted on"
		}
	}
	return "valid", out
}

func (h *RefineTestsJudge) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(RefineTestsJudgeOutput)
	beadID := job.BeadID.Int64
	cid := cycleID(job)
	now := time.Now().UTC().Format(time.RFC3339)

	slog.Info("REFINE_TESTS_JUDGE complete", "bead_id", beadID, "cycle_id", cid,
		"decision", out.Decision, "summary", out.Summary)

	// Store instructions in summary so WRITE can retrieve them next cycle.
	summary := out.Summary
	if out.Decision == "revise" {
		summary = string(out.Instructions)
	}

	if err := insertRefinement(ctx, tx, job.ProjectID, beadID, cid, h.Verb(), summary, out.Decision); err != nil {
		return fmt.Errorf("insert test_refinement: %w", err)
	}

	if out.Decision == "approved" {
		slog.Info("REFINE_TESTS_JUDGE: approved — enqueuing EXECUTE_BEAD", "bead_id", beadID)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbExecuteBead, beadID, now, now,
		); err != nil {
			return err
		}
		if pause, err := shouldPauseAfterVerb(ctx, tx, job.ProjectID, db.VerbRefineTestsJudge); err != nil {
			return err
		} else if pause {
			return pauseProject(ctx, tx, job.ProjectID, now)
		}
		return nil
	}

	// revise — check cycle cap.
	nextCycle := cid + 1
	if nextCycle > refinementCycleCap {
		slog.Error("ESCALATION — REFINE_TESTS: judge requested revision after cycle cap",
			"bead_id", beadID, "cycle_id", cid, "cap", refinementCycleCap)
		_, err := tx.ExecContext(ctx,
			`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`, now, job.ID)
		return err
	}

	slog.Info("REFINE_TESTS_JUDGE: requesting revision", "bead_id", beadID, "next_cycle", nextCycle)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		job.ProjectID, db.VerbRefineTestsWrite, beadID, nextCycle, now, now)
	return err
}
