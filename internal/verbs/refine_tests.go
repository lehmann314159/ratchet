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
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
	"ratchet/internal/splice"
)

// refinementCycleCap is the maximum number of write-critique-judge cycles
// per bead before escalating to the user.
const refinementCycleCap = 5

// refinementWriteAttempts is the maximum number of chat rounds within a single
// REFINE_TESTS_WRITE call to fix compile errors before giving up.
const refinementWriteAttempts = 3

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

	// Collect current test file content.
	var testBuf strings.Builder
	for _, f := range bead.OutputFiles {
		if !strings.HasSuffix(f, "_test.go") {
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

	for turn := 1; turn <= refinementWriteAttempts; turn++ {
		msg, toolErr := oc.ChatWithTools(ctx, model, messages, []ollama.Tool{writeFunctionTool}, nil, nil)
		if toolErr != nil {
			return "", toolErr
		}
		if strings.TrimSpace(msg.Content) != "" {
			summary = strings.TrimSpace(msg.Content)
		}
		if len(msg.ToolCalls) == 0 {
			break
		}

		messages = append(messages, msg)
		for _, tc := range msg.ToolCalls {
			var result string
			if tc.Function.Name != "write_function" {
				result = fmt.Sprintf("error: unknown tool %q — only write_function is available", tc.Function.Name)
			} else {
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
			}
			messages = append(messages, ollama.Message{Role: "tool", Content: result})
		}

		// Assemble or splice, write to disk, compile check.
		if len(writtenFuncs) == 0 {
			continue
		}
		var fileContent string
		if cid == 1 {
			funcs := make([]string, 0, len(funcOrder))
			for _, name := range funcOrder {
				funcs = append(funcs, writtenFuncs[name])
			}
			fileContent, _ = splice.Assemble(splice.DetectPackage(folderPath), funcs)
		} else {
			fileContent = originalSrc
			for name, body := range writtenFuncs {
				fileContent, _ = splice.Replace(fileContent, name, body)
			}
		}
		if err := os.WriteFile(testPath, []byte(fileContent), 0o644); err != nil {
			return "", fmt.Errorf("write test file: %w", err)
		}

		ok, compileOut := runCompile(ctx, folderPath)
		if ok {
			slog.Info("REFINE_TESTS_WRITE: compile passed", "bead_id", beadID, "turn", turn)
			if summary == "" {
				summary = "Test functions written and compiling."
			}
			break
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

type RefineTestsCritique struct{}

func (h *RefineTestsCritique) Verb() string { return db.VerbRefineTestsCritique }

func (h *RefineTestsCritique) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	bead, project, _, implContext, testFilePaths, currentTestContent, err := loadRefineContext(ctx, d, job)
	if err != nil {
		return "", err
	}

	model, err := loadVerbModel(ctx, d, job.ProjectID, h.Verb())
	if err != nil {
		return "", err
	}

	userMsg := buildBaseUserMsg(bead, project.FolderPath, implContext, currentTestContent, testFilePaths)

	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: refineTestsCritiqueSystemPrompt},
		{Role: "user", Content: userMsg},
	}, nil)
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

type RefineTestsJudge struct{}

func (h *RefineTestsJudge) Verb() string { return db.VerbRefineTestsJudge }

func (h *RefineTestsJudge) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	_, _, _, _, _, currentTestContent, err := loadRefineContext(ctx, d, job)
	if err != nil {
		return "", err
	}

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
		// Fallback: use the summary stored in test_refinements.
		_ = d.QueryRowContext(ctx, `
			SELECT summary FROM test_refinements
			WHERE bead_id = ? AND verb = ? AND cycle_id = ?
			ORDER BY created_at DESC LIMIT 1`,
			job.BeadID.Int64, db.VerbRefineTestsCritique, cid,
		).Scan(&critiqueRaw)
	}

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
		_, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbExecuteBead, beadID, now, now)
		return err
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
