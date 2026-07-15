package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ratchet/internal/db"
	"ratchet/internal/verbs"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// seedRewindProject inserts a project rooted at folder, plus a completed
// SURVEY_SPEC manifest declaring game.go (but never a test file — SURVEY
// never scaffolds test files), so WriteScaffoldStubs can find it.
func seedRewindProject(t *testing.T, d *db.DB, projectID int64, folder string) {
	t.Helper()
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   max_execution_attempts, created_at, updated_at)
		VALUES (?, 'rewind-test', ?, 'design.md', 'active', 'honor', 300, 5,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		projectID, folder); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	manifest := verbs.SurveySpecOutput{
		Module:  "example.com/m",
		Package: "main",
		Files: []verbs.SurveyManifestFile{
			{Path: "game.go", Declarations: "func NewGame() *Game { return &Game{} }\n\ntype Game struct{}\n"},
		},
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'complete', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		projectID, db.VerbSurveySpec)
	if err != nil {
		t.Fatalf("seed survey job: %v", err)
	}
	jobID, _ := res.LastInsertId()
	if _, err := d.ExecContext(ctx, `
		INSERT INTO handoff_attempts (job_id, attempt_number, raw_output, validation_result, created_at)
		VALUES (?, 1, ?, 'valid', '2026-01-01T00:00:00Z')`,
		jobID, mustMarshal(t, manifest)); err != nil {
		t.Fatalf("seed survey attempt: %v", err)
	}
}

// TestRewindBead_PreservesOutputFilesAddedAfterRevision1 reproduces the Stage
// 6 audit bug: revision 1 (DECOMPOSE_SPEC) declared only game.go, but
// RECONCILE_DECOMPOSITION's mechanical fix (goFixBeadSpec, mechanical_checks.go)
// later added the missing game_test.go to output_files/exit_criteria as
// revision 2 — a permanent structural correction, not prose drift. Before
// this fix, rewind-bead reverted the bead's whole spec (output_files
// included) straight to revision 1, silently discarding the added test file:
// the bead's post-rewind spec had no test file at all, so the re-enqueued
// REFINE_TESTS_WRITE would hard-error forever ("no test file paths for bead
// N") on every retry with no escalation (dispatch.go treated it as an infra
// error). Verifies the merged post-rewind spec keeps game_test.go, the prose
// still reverts to revision 1's, and the stale on-disk game_test.go is
// actually deleted rather than orphaned.
func TestRewindBead_PreservesOutputFilesAddedAfterRevision1(t *testing.T) {
	d := openTestDB(t)
	folder := t.TempDir()
	seedRewindProject(t, d, 1, folder)
	ctx := context.Background()

	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status) VALUES (1, 'pending')`)
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	beadID, _ := res.LastInsertId()

	rev1 := verbs.ParsedBead{
		Title: "game bead", FullText: "implement the game", ExecutionBudget: 300,
		MonitorOverride: "honor", OutputFiles: []string{"game.go"},
		ExitCriteria: []string{"go build ./..."},
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget,
		   monitor_override, created_by_verb, created_at)
		VALUES (1, ?, 1, ?, 300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		beadID, mustMarshal(t, rev1)); err != nil {
		t.Fatalf("seed revision 1: %v", err)
	}

	rev2 := rev1
	rev2.OutputFiles = []string{"game.go", "game_test.go"}
	rev2.ExitCriteria = []string{"grep -q 'func TestNewGame' game_test.go", "go test -run TestNewGame ./..."}
	res2, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget,
		   monitor_override, created_by_verb, created_at)
		VALUES (1, ?, 2, ?, 300, 'honor', 'RECONCILE_DECOMPOSITION', '2026-01-01T01:00:00Z')`,
		beadID, mustMarshal(t, rev2))
	if err != nil {
		t.Fatalf("seed revision 2: %v", err)
	}
	rev2ID, _ := res2.LastInsertId()

	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, rev2ID, beadID,
	); err != nil {
		t.Fatalf("point bead at revision 2: %v", err)
	}

	// On-disk state as if EXECUTE_BEAD had run against revision 2 and left
	// broken content behind.
	if err := os.WriteFile(filepath.Join(folder, "game.go"), []byte("package main\n\nfunc NewGame() *Game { panic(\"broken\") }\n"), 0644); err != nil {
		t.Fatalf("write game.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "game_test.go"), []byte("package main\n\nfunc TestNewGame(t *testing.T) { t.Fatal(\"broken\") }\n"), 0644); err != nil {
		t.Fatalf("write game_test.go: %v", err)
	}

	result, err := rewindBead(ctx, d, beadID)
	if err != nil {
		t.Fatalf("rewindBead: %v", err)
	}

	// The merged spec must still declare game_test.go — the whole point of
	// this fix. Losing it reproduces the "no test file paths" dead end.
	var newRevisionID int64
	var newFullText string
	if err := d.QueryRowContext(ctx, `
		SELECT br.id, br.full_text FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID,
	).Scan(&newRevisionID, &newFullText); err != nil {
		t.Fatalf("query post-rewind revision: %v", err)
	}
	if newRevisionID == rev2ID {
		t.Errorf("expected a fresh merged revision, current_revision_id still points at revision 2 (%d)", rev2ID)
	}
	var merged verbs.ParsedBead
	if err := json.Unmarshal([]byte(newFullText), &merged); err != nil {
		t.Fatalf("parse merged spec: %v", err)
	}
	if len(merged.OutputFiles) != 2 || merged.OutputFiles[0] != "game.go" || merged.OutputFiles[1] != "game_test.go" {
		t.Errorf("output_files = %v, want [game.go game_test.go] (revision 2's structural fix must survive rewind)", merged.OutputFiles)
	}
	if len(merged.ExitCriteria) != 2 {
		t.Errorf("exit_criteria = %v, want revision 2's 2 entries preserved", merged.ExitCriteria)
	}
	if merged.FullText != rev1.FullText {
		t.Errorf("full_text = %q, want revision 1's prose %q", merged.FullText, rev1.FullText)
	}

	// game_test.go must actually be deleted from disk, not orphaned.
	if _, statErr := os.Stat(filepath.Join(folder, "game_test.go")); !os.IsNotExist(statErr) {
		t.Errorf("expected game_test.go to be deleted, stat err = %v", statErr)
	}
	found := false
	for _, f := range result.DeletedTests {
		if f == "game_test.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("result.DeletedTests = %v, want game_test.go included", result.DeletedTests)
	}

	// game.go must be reset to its scaffold stub, not left with broken content.
	gameGoContent, err := os.ReadFile(filepath.Join(folder, "game.go"))
	if err != nil {
		t.Fatalf("read game.go: %v", err)
	}
	if string(gameGoContent) == "package main\n\nfunc NewGame() *Game { panic(\"broken\") }\n" {
		t.Errorf("game.go was not reset to its scaffold stub")
	}

	// Bead is pending again with a fresh budget and rewound_at set.
	var status string
	var rewoundAt *string
	if err := d.QueryRowContext(ctx,
		`SELECT status, rewound_at FROM beads WHERE id = ?`, beadID,
	).Scan(&status, &rewoundAt); err != nil {
		t.Fatalf("query bead: %v", err)
	}
	if status != "pending" {
		t.Errorf("status = %q, want pending", status)
	}
	if rewoundAt == nil || *rewoundAt == "" {
		t.Errorf("rewound_at not set")
	}

	// REFINE_TESTS_WRITE re-enqueued.
	var verb, jobStatus string
	if err := d.QueryRowContext(ctx,
		`SELECT verb, status FROM handoff_jobs WHERE bead_id = ? AND verb = 'REFINE_TESTS_WRITE'`, beadID,
	).Scan(&verb, &jobStatus); err != nil {
		t.Fatalf("query REFINE_TESTS_WRITE job: %v", err)
	}
	if jobStatus != "pending" {
		t.Errorf("REFINE_TESTS_WRITE job status = %q, want pending", jobStatus)
	}
}

// TestRewindBead_AlreadySucceededErrors verifies rewind refuses to touch a
// bead that already succeeded.
func TestRewindBead_AlreadySucceededErrors(t *testing.T) {
	d := openTestDB(t)
	folder := t.TempDir()
	seedRewindProject(t, d, 1, folder)
	ctx := context.Background()

	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status) VALUES (1, 'succeeded')`)
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	beadID, _ := res.LastInsertId()

	if _, err := rewindBead(ctx, d, beadID); err == nil {
		t.Error("expected error rewinding an already-succeeded bead")
	}
}
