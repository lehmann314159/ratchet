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
)

// refinementCycleCap is the maximum number of write-critique-judge cycles
// per bead before escalating to the user.
const refinementCycleCap = 3

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

func writeTestFiles(folderPath string, files []RefineTestsFile) {
	for _, tf := range files {
		if !strings.HasSuffix(tf.Path, "_test.go") {
			slog.Warn("REFINE_TESTS: skipping non-test file in output", "path", tf.Path)
			continue
		}
		fullPath := filepath.Join(folderPath, tf.Path)
		if mkErr := os.MkdirAll(filepath.Dir(fullPath), 0o755); mkErr != nil {
			slog.Error("REFINE_TESTS: mkdir", "path", fullPath, "error", mkErr)
			continue
		}
		if wErr := os.WriteFile(fullPath, []byte(tf.Content), 0o644); wErr != nil {
			slog.Error("REFINE_TESTS: write file", "path", fullPath, "error", wErr)
		}
	}
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

func (h *RefineTestsWrite) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	bead, project, folderPath, implContext, testFilePaths, currentTestContent, err := loadRefineContext(ctx, d, job)
	if err != nil {
		return "", err
	}
	_ = project // folderPath used directly

	model, err := loadVerbModel(ctx, d, job.ProjectID, h.Verb())
	if err != nil {
		return "", err
	}

	cid := cycleID(job)

	// Fetch correction instructions from the most recent JUDGE turn (prior cycles).
	var instructions string
	if cid > 1 {
		_ = d.QueryRowContext(ctx, `
			SELECT summary FROM test_refinements
			WHERE bead_id = ? AND verb = ? AND decision = 'revise'
			ORDER BY cycle_id DESC LIMIT 1`,
			job.BeadID.Int64, db.VerbRefineTestsJudge,
		).Scan(&instructions)
	}

	userMsg := buildBaseUserMsg(bead, folderPath, implContext, currentTestContent, testFilePaths)
	if instructions != "" {
		userMsg += "\n\n## Correction Instructions (from prior review cycle — apply every item)\n\n" + instructions
	}

	messages := []ollama.Message{
		{Role: "system", Content: refineTestsWriteSystemPrompt},
		{Role: "user", Content: userMsg},
	}

	var raw string
	for attempt := 1; attempt <= refinementWriteAttempts; attempt++ {
		raw, err = oc.Chat(ctx, model, messages, nil)
		if err != nil {
			return "", err
		}

		var out RefineTestsWriteOutput
		if jsonErr := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); jsonErr != nil || len(out.TestFiles) == 0 {
			// Malformed response — let Validate() handle it on the final attempt.
			break
		}

		writeTestFiles(folderPath, out.TestFiles)

		ok, compileOut := runCompile(ctx, folderPath)
		if ok {
			slog.Info("REFINE_TESTS_WRITE: compile passed", "bead_id", job.BeadID.Int64, "attempt", attempt)
			break
		}
		slog.Error("REFINE_TESTS_WRITE: compile failed", "bead_id", job.BeadID.Int64, "attempt", attempt, "output", compileOut)

		if attempt < refinementWriteAttempts {
			messages = append(messages,
				ollama.Message{Role: "assistant", Content: raw},
				ollama.Message{Role: "user", Content: "The file you wrote failed to compile:\n```\n" + compileOut + "\n```\nFix every error and output the complete corrected file."},
			)
		}
	}

	return raw, nil
}

func (h *RefineTestsWrite) Validate(rawOutput string) (string, any) {
	var out RefineTestsWriteOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(rawOutput)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if strings.TrimSpace(out.Summary) == "" {
		return "malformed: summary is empty", nil
	}
	if len(out.TestFiles) == 0 {
		return "malformed: test_files is empty", nil
	}
	for _, tf := range out.TestFiles {
		if strings.TrimSpace(tf.Path) == "" {
			return "malformed: test_files entry has empty path", nil
		}
		if !strings.HasSuffix(tf.Path, "_test.go") {
			return fmt.Sprintf("malformed: test_files entry %q is not a _test.go file", tf.Path), nil
		}
		if strings.TrimSpace(tf.Content) == "" {
			return fmt.Sprintf("malformed: test_files entry %q has empty content", tf.Path), nil
		}
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
	if out.Decision == "revise" && strings.TrimSpace(out.Instructions) == "" {
		return "malformed: decision is 'revise' but instructions is empty", nil
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
		summary = out.Instructions
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
