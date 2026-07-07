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

// refinementTurnCap is the maximum number of model calls (A+B interleaved)
// per cycle before a stalemate is declared. Cap=4 allows 2 full A→B rounds.
const refinementTurnCap = 4

// RefineTests implements REFINE_TESTS_A and REFINE_TESTS_B: a symmetric peer-model
// loop that certifies test files before EXECUTE_BEAD runs.
//
// Each invocation is one "turn" of the loop. Commit decides whether to continue
// (enqueue the peer verb), declare consensus (enqueue EXECUTE_BEAD), or
// declare stalemate (escalate the job).
//
// Consensus rule: two consecutive turns in the same cycle both returned changed=false.
// Stalemate rule: refinementTurnCap turns elapsed in the current cycle without consensus.
// Grant & Requeue increments refinement_cycle_id on the job, starting a fresh cycle.
type RefineTests struct {
	verbName string // "REFINE_TESTS_A" or "REFINE_TESTS_B"
}

func (h *RefineTests) Verb() string { return h.verbName }

func (h *RefineTests) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	if !job.BeadID.Valid {
		return "", fmt.Errorf("%s job %d has no bead_id", h.verbName, job.ID)
	}
	beadID := job.BeadID.Int64

	bead, err := loadBeadByID(ctx, d, beadID)
	if err != nil {
		return "", err
	}
	project, err := loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	folderPath := project.FolderPath

	// Collect implementation files from prior beads for domain context.
	var implContext strings.Builder
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
				fmt.Fprintf(&implContext, "### %s\n\n```go\n%s\n```\n\n", name, string(content))
			}
		}
	}

	// Collect current test file content and run a compile check.
	var testContent strings.Builder
	var testFilePaths []string
	for _, f := range bead.OutputFiles {
		if !strings.HasSuffix(f, "_test.go") {
			continue
		}
		testFilePaths = append(testFilePaths, f)
		content, err := os.ReadFile(filepath.Join(folderPath, f))
		if err != nil {
			continue
		}
		fmt.Fprintf(&testContent, "### %s\n\n```go\n%s\n```\n\n", f, string(content))
	}

	// Run compile check on current state of test files (before this turn's edits).
	var compileStatus string
	if testContent.Len() > 0 {
		cmd := exec.CommandContext(ctx, "go", "test", "-c", "-o", os.DevNull, ".")
		cmd.Dir = folderPath
		compileOut, compileErr := cmd.CombinedOutput()
		if compileErr != nil {
			compileStatus = "FAILED:\n```\n" + strings.TrimSpace(string(compileOut)) + "\n```"
			slog.Error("REFINE_TESTS: compile check failed", "verb", h.verbName, "bead_id", beadID, "output", string(compileOut))
		} else {
			compileStatus = "PASS"
			slog.Info("REFINE_TESTS: compile check passed", "verb", h.verbName, "bead_id", beadID)
		}
	}

	userMsg := "## Bead Specification\n\n" + bead.FullText

	// Inject prescriptive design doc if present in the project folder.
	if prescriptive, rerr := os.ReadFile(filepath.Join(folderPath, "design_doc_prescriptive.md")); rerr == nil {
		userMsg += "\n\n## Prescriptive Design Document\n\n" + string(prescriptive)
	}

	if implContext.Len() > 0 {
		userMsg += "\n\n## Implementation Files (prior beads — use for type definitions and domain conventions)\n\n" +
			strings.TrimSpace(implContext.String())
	}
	if testContent.Len() > 0 {
		userMsg += "\n\n## Current Test Files\n\n" + strings.TrimSpace(testContent.String())
		userMsg += "\n\n## Compilation Status\n\n" + compileStatus
	} else {
		userMsg += "\n\n## Current Test Files\n\n(No test files exist yet. Write them from scratch.)"
	}
	if len(testFilePaths) > 0 {
		userMsg += "\n\n## Test Files to Produce\n\n" + strings.Join(testFilePaths, "\n")
	}

	model, err := loadVerbModel(ctx, d, job.ProjectID, h.verbName)
	if err != nil {
		return "", err
	}

	raw, err := oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: refineTestsSystemPrompt},
		{Role: "user", Content: userMsg},
	}, nil)
	if err != nil {
		return "", err
	}

	// Write corrected test files to disk immediately when the model reports changes.
	// Run() happens outside any transaction; Commit() will record the event.
	var out RefineTestsOutput
	if jsonErr := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); jsonErr == nil && out.Changed {
		for _, tf := range out.TestFiles {
			if !strings.HasSuffix(tf.Path, "_test.go") {
				slog.Warn("REFINE_TESTS: skipping non-test file in output", "verb", h.verbName, "path", tf.Path)
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
		// Log post-write compile result; the next turn will include it in context.
		postCmd := exec.CommandContext(ctx, "go", "test", "-c", "-o", os.DevNull, ".")
		postCmd.Dir = folderPath
		if postOut, postErr := postCmd.CombinedOutput(); postErr != nil {
			slog.Error("REFINE_TESTS: post-write compile failed", "verb", h.verbName, "bead_id", beadID, "output", string(postOut))
		} else {
			slog.Info("REFINE_TESTS: post-write compile passed", "verb", h.verbName, "bead_id", beadID)
		}
	}

	return raw, nil
}

func (h *RefineTests) Validate(rawOutput string) (string, any) {
	var out RefineTestsOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(rawOutput)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if strings.TrimSpace(out.Summary) == "" {
		return "malformed: summary is empty", nil
	}
	if out.Changed && len(out.TestFiles) == 0 {
		return "malformed: changed is true but test_files is empty", nil
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

func (h *RefineTests) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(RefineTestsOutput)
	beadID := job.BeadID.Int64
	now := time.Now().UTC().Format(time.RFC3339)

	// Resolve the current cycle ID (1 for legacy rows without the column).
	cycleID := int64(1)
	if job.RefinementCycleID.Valid {
		cycleID = job.RefinementCycleID.Int64
	}

	// Determine turn number within the current cycle only.
	var existingTurns int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM test_refinements WHERE bead_id = ? AND cycle_id = ?`, beadID, cycleID,
	).Scan(&existingTurns); err != nil {
		return fmt.Errorf("count test_refinements: %w", err)
	}
	turn := existingTurns + 1

	changed := 0
	if out.Changed {
		changed = 1
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO test_refinements (project_id, bead_id, cycle_id, turn, verb, changed, summary, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ProjectID, beadID, cycleID, turn, h.verbName, changed, out.Summary, now,
	); err != nil {
		return fmt.Errorf("insert test_refinement: %w", err)
	}

	slog.Info("REFINE_TESTS turn complete",
		"verb", h.verbName, "bead_id", beadID, "cycle_id", cycleID, "turn", turn, "changed", out.Changed,
		"summary", out.Summary)

	// Read the previous turn's changed flag within the same cycle for the consensus check.
	prevChanged := -1 // sentinel: no previous turn
	if turn >= 2 {
		_ = tx.QueryRowContext(ctx, `
			SELECT changed FROM test_refinements
			WHERE bead_id = ? AND cycle_id = ?
			ORDER BY turn DESC
			LIMIT 1 OFFSET 1`, beadID, cycleID,
		).Scan(&prevChanged)
	}

	// Consensus: this turn AND the previous turn both declared changed=false.
	if turn >= 2 && changed == 0 && prevChanged == 0 {
		slog.Info("REFINE_TESTS consensus reached — enqueuing EXECUTE_BEAD",
			"bead_id", beadID, "cycle_id", cycleID, "turns", turn)
		_, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbExecuteBead, beadID, now, now)
		return err
	}

	// Stalemate: hit the cap without consensus → escalate.
	if turn >= refinementTurnCap {
		slog.Error("ESCALATION — REFINE_TESTS stalemate: models disagree after cap",
			"bead_id", beadID, "cycle_id", cycleID, "turns", turn, "cap", refinementTurnCap)
		_, err := tx.ExecContext(ctx,
			`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`,
			now, job.ID)
		return err
	}

	// Continue loop: enqueue the peer verb with the same cycle_id.
	peerVerb := db.VerbRefineTestsB
	if h.verbName == db.VerbRefineTestsB {
		peerVerb = db.VerbRefineTestsA
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		job.ProjectID, peerVerb, beadID, cycleID, now, now)
	return err
}