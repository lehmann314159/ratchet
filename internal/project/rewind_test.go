package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

	result, err := rewindBead(ctx, d, beadID, RewindOptions{})
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

// TestRewindBead_UsesCurrentRevisionExecutionBudgetNotStaleJSONField
// reproduces the checkers-try-1 bead-684 incident: revision 1's full_text
// JSON blob carries a vestigial "execution_budget" field (DECOMPOSE_SPEC's
// own placeholder — the framework never reads it back; the real value lives
// in the execution_budget DB column, set correctly by whichever verb creates
// each revision). Before this fix, rewind sourced the new revision's budget
// from that stale JSON field, silently resetting a bead whose budget had been
// doubled after real timeouts back down to a placeholder value — 0 in this
// bead's case, which fired the budget timer almost instantly on the very
// next EXECUTE_BEAD attempt. Verifies the merged revision's real DB column
// (and the JSON blob's own field, kept in sync) come from the current
// revision, not revision 1.
func TestRewindBead_UsesCurrentRevisionExecutionBudgetNotStaleJSONField(t *testing.T) {
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
		Title: "layout", FullText: "create the layout", ExecutionBudget: 0,
		MonitorOverride: "honor", OutputFiles: []string{"game.go"},
		ExitCriteria: []string{"go build ./..."},
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget,
		   monitor_override, created_by_verb, created_at)
		VALUES (1, ?, 1, ?, 900, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		beadID, mustMarshal(t, rev1)); err != nil {
		t.Fatalf("seed revision 1: %v", err)
	}

	// ADJUDICATE doubled the budget after a real timeout — its DB column
	// (1800) is the authoritative value for this revision.
	rev2 := rev1
	rev2.ExecutionBudget = 1800
	rev2.MonitorOverride = "ignore"
	res2, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget,
		   monitor_override, created_by_verb, created_at)
		VALUES (1, ?, 2, ?, 1800, 'ignore', 'ADJUDICATE_NEXT_EXECUTION', '2026-01-01T01:00:00Z')`,
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

	if _, err := rewindBead(ctx, d, beadID, RewindOptions{}); err != nil {
		t.Fatalf("rewindBead: %v", err)
	}

	var newBudget int
	var newMonitorOverride, newFullText string
	if err := d.QueryRowContext(ctx, `
		SELECT br.execution_budget, br.monitor_override, br.full_text FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID,
	).Scan(&newBudget, &newMonitorOverride, &newFullText); err != nil {
		t.Fatalf("query post-rewind revision: %v", err)
	}
	if newBudget != 1800 {
		t.Errorf("execution_budget = %d, want 1800 (current revision's real value, not revision 1's stale JSON field of 0)", newBudget)
	}
	if newMonitorOverride != "ignore" {
		t.Errorf("monitor_override = %q, want %q (current revision's value)", newMonitorOverride, "ignore")
	}

	// The JSON blob's own execution_budget/monitor_override fields must agree
	// with the real DB columns above — otherwise the stored spec text lies
	// about its own effective values.
	var merged verbs.ParsedBead
	if err := json.Unmarshal([]byte(newFullText), &merged); err != nil {
		t.Fatalf("parse merged spec: %v", err)
	}
	if merged.ExecutionBudget != 1800 {
		t.Errorf("merged spec JSON execution_budget = %d, want 1800 (in sync with the DB column)", merged.ExecutionBudget)
	}
	if merged.MonitorOverride != "ignore" {
		t.Errorf("merged spec JSON monitor_override = %q, want %q (in sync with the DB column)", merged.MonitorOverride, "ignore")
	}
	// full_text prose still reverts to revision 1 — the one field rewind is
	// actually meant to undo.
	if merged.FullText != rev1.FullText {
		t.Errorf("full_text = %q, want revision 1's prose %q", merged.FullText, rev1.FullText)
	}
}

// TestRewindBead_SelfHealsInheritedBrokenExitCriteria reproduces the
// checkers-try-1 bead-684 incident from the other direction: the current
// revision's exit_criteria already carries a bug that a mechanical fix in
// mechanical_checks.go now knows how to repair (here, the othello-fixture
// unescaped-asterisk bug — a pointer-receiver grep pattern that can never
// match real Go source, see escapeStrayGrepAsterisks). Before wiring
// verbs.ApplyMechanicalBeadFixes into the merge, rewind carried the current
// revision's exit_criteria forward completely unchanged, so a bead escalating
// specifically *because* of a broken exit criterion would come back from
// rewind with the identical broken criterion — the only way out was a manual
// DB patch. Verifies the merged post-rewind spec has the pattern escaped.
func TestRewindBead_SelfHealsInheritedBrokenExitCriteria(t *testing.T) {
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
		Title: "game-flips", FullText: "implement FindFlips", ExecutionBudget: 300,
		MonitorOverride: "honor", OutputFiles: []string{"game.go", "game_test.go"},
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

	// The current (escalated) revision carries the unescaped-asterisk bug —
	// this criterion can never pass regardless of what the agent writes.
	rev2 := rev1
	rev2.ExitCriteria = []string{"grep -q 'func (g *Game) FindFlips' game.go && go test -run TestFindFlips ."}
	res2, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget,
		   monitor_override, created_by_verb, created_at)
		VALUES (1, ?, 2, ?, 300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T01:00:00Z')`,
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

	if _, err := rewindBead(ctx, d, beadID, RewindOptions{}); err != nil {
		t.Fatalf("rewindBead: %v", err)
	}

	var newFullText string
	if err := d.QueryRowContext(ctx, `
		SELECT br.full_text FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID,
	).Scan(&newFullText); err != nil {
		t.Fatalf("query post-rewind revision: %v", err)
	}
	var merged verbs.ParsedBead
	if err := json.Unmarshal([]byte(newFullText), &merged); err != nil {
		t.Fatalf("parse merged spec: %v", err)
	}
	want := "grep -q 'func (g \\*Game) FindFlips' game.go && go test -run TestFindFlips ."
	if len(merged.ExitCriteria) != 1 || merged.ExitCriteria[0] != want {
		t.Errorf("exit_criteria = %v, want [%q] (asterisk escaped by the mechanical fix, not carried forward broken)",
			merged.ExitCriteria, want)
	}
}

// TestRewindBead_PreservesRevisePendingProseButNotAdjudicatePatches verifies
// the fix made after the checkers bead-2 "BlockedPieces" incident: rewind
// used to always revert full_text to revision 1's prose unconditionally,
// which — since REVISE_PENDING's revisions are legitimate cross-bead learning
// applied once while a bead is still pending, not the kind of reactive
// drift rewind is meant to undo — would have silently discarded a genuinely
// useful REVISE_PENDING addition right alongside any later
// ADJUDICATE_NEXT_EXECUTION patches. Sets up DECOMPOSE_SPEC (rev1) ->
// REVISE_PENDING (rev2, different prose) -> ADJUDICATE_NEXT_EXECUTION (rev3,
// yet more different prose) and verifies the merged post-rewind spec carries
// rev2's prose, not rev1's, and definitely not rev3's.
func TestRewindBead_PreservesRevisePendingProseButNotAdjudicatePatches(t *testing.T) {
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
		Title: "move-generation", FullText: "implement ValidMoves and AllValidMoves",
		ExecutionBudget: 300, MonitorOverride: "honor",
		OutputFiles:  []string{"game.go", "game_test.go"},
		ExitCriteria: []string{"go test -run TestValidMoves ."},
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget,
		   monitor_override, created_by_verb, created_at)
		VALUES (1, ?, 1, ?, 300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		beadID, mustMarshal(t, rev1)); err != nil {
		t.Fatalf("seed revision 1: %v", err)
	}

	// REVISE_PENDING adds a genuinely useful cross-bead note learned from a
	// sibling bead's success — this is exactly the kind of content that must
	// survive rewind.
	rev2 := rev1
	rev2.FullText = rev1.FullText + " Note: game-state's ApplyMove expects Square{Row,Col} in that field order — match it here."
	res2, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget,
		   monitor_override, created_by_verb, created_at)
		VALUES (1, ?, 2, ?, 300, 'honor', 'REVISE_PENDING', '2026-01-01T00:30:00Z')`,
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

	// ADJUDICATE_NEXT_EXECUTION then reacts to a failed execution attempt
	// with a verbatim patch — this is the content rewind must discard.
	rev3 := rev2
	rev3.FullText = rev2.FullText + " RECURRING FAILURE FIX: you MUST implement the occupancy check verbatim as follows: ..."
	res3, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget,
		   monitor_override, created_by_verb, created_at)
		VALUES (1, ?, 3, ?, 300, 'honor', 'ADJUDICATE_NEXT_EXECUTION', '2026-01-01T02:00:00Z')`,
		beadID, mustMarshal(t, rev3))
	if err != nil {
		t.Fatalf("seed revision 3: %v", err)
	}
	rev3ID, _ := res3.LastInsertId()
	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, rev3ID, beadID,
	); err != nil {
		t.Fatalf("point bead at revision 3: %v", err)
	}

	if _, err := rewindBead(ctx, d, beadID, RewindOptions{}); err != nil {
		t.Fatalf("rewindBead: %v", err)
	}

	var newFullText string
	if err := d.QueryRowContext(ctx, `
		SELECT br.full_text FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID,
	).Scan(&newFullText); err != nil {
		t.Fatalf("query post-rewind revision: %v", err)
	}
	var merged verbs.ParsedBead
	if err := json.Unmarshal([]byte(newFullText), &merged); err != nil {
		t.Fatalf("parse merged spec: %v", err)
	}
	if merged.FullText != rev2.FullText {
		t.Errorf("full_text = %q, want revision 2's (REVISE_PENDING's) prose %q", merged.FullText, rev2.FullText)
	}
	if merged.FullText == rev1.FullText {
		t.Error("full_text reverted all the way to revision 1, discarding REVISE_PENDING's legitimate addition")
	}
	if strings.Contains(merged.FullText, "RECURRING FAILURE FIX") {
		t.Error("full_text still contains ADJUDICATE_NEXT_EXECUTION's patch — it should have been discarded")
	}
}

// TestRewindBead_SnapshotsPreRewindContentBeforeDestroying verifies rewind
// preserves the actual broken file content that led to escalation — not just
// the bead-report.md text snapshot already written at escalation time, but a
// real runnable/diffable copy — before test files are deleted and impl files
// are stubbed. Loop-mode forensics goal: a human reviewing this rewind should
// be able to see exactly what the failing attempt looked like on disk.
func TestRewindBead_SnapshotsPreRewindContentBeforeDestroying(t *testing.T) {
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
		MonitorOverride: "honor", OutputFiles: []string{"game.go", "game_test.go"},
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
	var revID int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM bead_revisions WHERE bead_id = ?`, beadID).Scan(&revID); err != nil {
		t.Fatalf("query revision id: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID,
	); err != nil {
		t.Fatalf("point bead at revision: %v", err)
	}

	brokenGame := "package main\n\nfunc NewGame() *Game { panic(\"broken\") }\n"
	brokenTest := "package main\n\nfunc TestNewGame(t *testing.T) { t.Fatal(\"broken\") }\n"
	if err := os.WriteFile(filepath.Join(folder, "game.go"), []byte(brokenGame), 0644); err != nil {
		t.Fatalf("write game.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "game_test.go"), []byte(brokenTest), 0644); err != nil {
		t.Fatalf("write game_test.go: %v", err)
	}

	result, err := rewindBead(ctx, d, beadID, RewindOptions{})
	if err != nil {
		t.Fatalf("rewindBead: %v", err)
	}

	if result.SnapshotDir == "" {
		t.Fatal("expected a non-empty SnapshotDir")
	}
	snapshotGame, err := os.ReadFile(filepath.Join(result.SnapshotDir, "game.go"))
	if err != nil {
		t.Fatalf("read snapshotted game.go: %v", err)
	}
	if string(snapshotGame) != brokenGame {
		t.Errorf("snapshotted game.go = %q, want the pre-rewind broken content %q", snapshotGame, brokenGame)
	}
	snapshotTest, err := os.ReadFile(filepath.Join(result.SnapshotDir, "game_test.go"))
	if err != nil {
		t.Fatalf("read snapshotted game_test.go: %v", err)
	}
	if string(snapshotTest) != brokenTest {
		t.Errorf("snapshotted game_test.go = %q, want the pre-rewind broken content %q", snapshotTest, brokenTest)
	}
	if _, err := os.Stat(filepath.Join(result.SnapshotDir, "README.md")); err != nil {
		t.Errorf("expected a README.md manifest in the snapshot dir: %v", err)
	}

	// The live files, meanwhile, must actually have been changed by rewind —
	// the snapshot must be a copy, not the only surviving reference.
	liveGame, err := os.ReadFile(filepath.Join(folder, "game.go"))
	if err != nil {
		t.Fatalf("read live game.go: %v", err)
	}
	if string(liveGame) == brokenGame {
		t.Error("live game.go still has the broken content — rewind should have reset it to a stub")
	}
	if _, statErr := os.Stat(filepath.Join(folder, "game_test.go")); !os.IsNotExist(statErr) {
		t.Error("live game_test.go should have been deleted by rewind")
	}
}

// TestRewindBead_SecondRewindGetsItsOwnSnapshot verifies rewinding the same
// bead twice doesn't overwrite the first rewind's snapshot — each attempt's
// pre-rewind state needs to stay independently inspectable.
func TestRewindBead_SecondRewindGetsItsOwnSnapshot(t *testing.T) {
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
	var revID int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM bead_revisions WHERE bead_id = ?`, beadID).Scan(&revID); err != nil {
		t.Fatalf("query revision id: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID,
	); err != nil {
		t.Fatalf("point bead at revision: %v", err)
	}

	if err := os.WriteFile(filepath.Join(folder, "game.go"), []byte("attempt one, broken"), 0644); err != nil {
		t.Fatalf("write game.go: %v", err)
	}
	result1, err := rewindBead(ctx, d, beadID, RewindOptions{})
	if err != nil {
		t.Fatalf("first rewindBead: %v", err)
	}

	// Simulate a second failed attempt after the first rewind, then rewind again.
	if _, err := d.ExecContext(ctx, `UPDATE beads SET status = 'pending' WHERE id = ?`, beadID); err != nil {
		t.Fatalf("reset bead status: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "game.go"), []byte("attempt two, also broken"), 0644); err != nil {
		t.Fatalf("write game.go: %v", err)
	}
	result2, err := rewindBead(ctx, d, beadID, RewindOptions{})
	if err != nil {
		t.Fatalf("second rewindBead: %v", err)
	}

	if result1.SnapshotDir == result2.SnapshotDir {
		t.Fatalf("expected distinct snapshot dirs, both were %q", result1.SnapshotDir)
	}
	first, err := os.ReadFile(filepath.Join(result1.SnapshotDir, "game.go"))
	if err != nil {
		t.Fatalf("read first snapshot: %v", err)
	}
	if string(first) != "attempt one, broken" {
		t.Errorf("first snapshot's game.go = %q, want %q", first, "attempt one, broken")
	}
	second, err := os.ReadFile(filepath.Join(result2.SnapshotDir, "game.go"))
	if err != nil {
		t.Fatalf("read second snapshot: %v", err)
	}
	if string(second) != "attempt two, also broken" {
		t.Errorf("second snapshot's game.go = %q, want %q", second, "attempt two, also broken")
	}
}

// TestRewindBead_AppendsGuidanceNoteAndRecordsItInManifest verifies a
// --note passed to rewindBead lands in the merged spec's Guidance Log (not
// patched into the base prose) and is recorded in the snapshot manifest.
func TestRewindBead_AppendsGuidanceNoteAndRecordsItInManifest(t *testing.T) {
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
	var revID int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM bead_revisions WHERE bead_id = ?`, beadID).Scan(&revID); err != nil {
		t.Fatalf("query revision id: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID,
	); err != nil {
		t.Fatalf("point bead at revision: %v", err)
	}

	result, err := rewindBead(ctx, d, beadID, RewindOptions{Note: "use Square{Row,Col} field order to match game-state"})
	if err != nil {
		t.Fatalf("rewindBead: %v", err)
	}
	if result.NewNoteNumber != 1 {
		t.Errorf("NewNoteNumber = %d, want 1", result.NewNoteNumber)
	}

	var newFullText string
	if err := d.QueryRowContext(ctx, `
		SELECT br.full_text FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID,
	).Scan(&newFullText); err != nil {
		t.Fatalf("query post-rewind revision: %v", err)
	}
	var merged verbs.ParsedBead
	if err := json.Unmarshal([]byte(newFullText), &merged); err != nil {
		t.Fatalf("parse merged spec: %v", err)
	}
	if !strings.HasPrefix(merged.FullText, rev1.FullText) {
		t.Errorf("expected base prose preserved at the start, got: %q", merged.FullText)
	}
	if !strings.Contains(merged.FullText, "use Square{Row,Col} field order to match game-state") {
		t.Errorf("expected the guidance note in the merged full_text, got: %q", merged.FullText)
	}

	manifest, err := os.ReadFile(filepath.Join(result.SnapshotDir, "README.md"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !strings.Contains(string(manifest), "use Square{Row,Col} field order to match game-state") {
		t.Errorf("expected the manifest to record the guidance note, got:\n%s", manifest)
	}
}

// TestRewindBead_GuidanceNoteSurvivesASubsequentRewind verifies a note added
// at rewind N is still present (not discarded as if it were an
// ADJUDICATE_NEXT_EXECUTION patch) after a second rewind N+1 with no note of
// its own — exercising the composition between applyGuidance and the
// existing "restore to last pre-ADJUDICATE_NEXT_EXECUTION revision" logic.
func TestRewindBead_GuidanceNoteSurvivesASubsequentRewind(t *testing.T) {
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
	var revID int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM bead_revisions WHERE bead_id = ?`, beadID).Scan(&revID); err != nil {
		t.Fatalf("query revision id: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID,
	); err != nil {
		t.Fatalf("point bead at revision: %v", err)
	}

	if _, err := rewindBead(ctx, d, beadID, RewindOptions{Note: "first attempt's guidance"}); err != nil {
		t.Fatalf("first rewindBead: %v", err)
	}

	// Simulate ADJUDICATE_NEXT_EXECUTION reacting to another failed attempt
	// with a reactive patch, on top of the rewound (Note-1-bearing) revision.
	var afterFirstRewindRevID int64
	var afterFirstRewindFullText string
	if err := d.QueryRowContext(ctx, `
		SELECT br.id, br.full_text FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID,
	).Scan(&afterFirstRewindRevID, &afterFirstRewindFullText); err != nil {
		t.Fatalf("query post-first-rewind revision: %v", err)
	}
	var afterFirstRewindSpec verbs.ParsedBead
	if err := json.Unmarshal([]byte(afterFirstRewindFullText), &afterFirstRewindSpec); err != nil {
		t.Fatalf("parse post-first-rewind spec: %v", err)
	}
	adjudicateSpec := afterFirstRewindSpec
	adjudicateSpec.FullText += " RECURRING FAILURE FIX: do X."
	var maxRevNum int
	if err := d.QueryRowContext(ctx, `SELECT MAX(revision_number) FROM bead_revisions WHERE bead_id = ?`, beadID).Scan(&maxRevNum); err != nil {
		t.Fatalf("max revision number: %v", err)
	}
	adjRes, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget,
		   monitor_override, created_by_verb, created_at)
		VALUES (1, ?, ?, ?, 300, 'honor', 'ADJUDICATE_NEXT_EXECUTION', '2026-01-01T02:00:00Z')`,
		beadID, maxRevNum+1, mustMarshal(t, adjudicateSpec))
	if err != nil {
		t.Fatalf("seed ADJUDICATE revision: %v", err)
	}
	adjRevID, _ := adjRes.LastInsertId()
	if _, err := d.ExecContext(ctx, `UPDATE beads SET current_revision_id = ?, status = 'pending' WHERE id = ?`, adjRevID, beadID); err != nil {
		t.Fatalf("point bead at ADJUDICATE revision: %v", err)
	}

	if _, err := rewindBead(ctx, d, beadID, RewindOptions{}); err != nil {
		t.Fatalf("second rewindBead: %v", err)
	}

	var finalFullText string
	if err := d.QueryRowContext(ctx, `
		SELECT br.full_text FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID,
	).Scan(&finalFullText); err != nil {
		t.Fatalf("query final revision: %v", err)
	}
	var finalSpec verbs.ParsedBead
	if err := json.Unmarshal([]byte(finalFullText), &finalSpec); err != nil {
		t.Fatalf("parse final spec: %v", err)
	}
	if !strings.Contains(finalSpec.FullText, "first attempt's guidance") {
		t.Errorf("expected Note 1 to survive a second rewind, got: %q", finalSpec.FullText)
	}
	if strings.Contains(finalSpec.FullText, "RECURRING FAILURE FIX") {
		t.Error("expected ADJUDICATE_NEXT_EXECUTION's reactive patch to be discarded, but it survived")
	}
}

// TestRewindBead_SupersedesUnknownNoteErrorsBeforeAnyDestruction verifies a
// bad --supersedes value fails the whole rewind before anything is touched —
// snapshot included — rather than partially applying.
func TestRewindBead_SupersedesUnknownNoteErrorsBeforeAnyDestruction(t *testing.T) {
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
	var revID int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM bead_revisions WHERE bead_id = ?`, beadID).Scan(&revID); err != nil {
		t.Fatalf("query revision id: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID,
	); err != nil {
		t.Fatalf("point bead at revision: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "game.go"), []byte("original content"), 0644); err != nil {
		t.Fatalf("write game.go: %v", err)
	}

	if _, err := rewindBead(ctx, d, beadID, RewindOptions{Supersedes: 5}); err == nil {
		t.Fatal("expected an error for --supersedes referencing a nonexistent note")
	}

	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM beads WHERE id = ?`, beadID).Scan(&status); err != nil {
		t.Fatalf("query bead status: %v", err)
	}
	if status != "pending" {
		t.Errorf("bead status = %q, want unchanged %q — a failed rewind must not partially apply", status, "pending")
	}
	content, err := os.ReadFile(filepath.Join(folder, "game.go"))
	if err != nil {
		t.Fatalf("read game.go: %v", err)
	}
	if string(content) != "original content" {
		t.Errorf("game.go was modified despite the rewind failing validation")
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

	if _, err := rewindBead(ctx, d, beadID, RewindOptions{}); err == nil {
		t.Error("expected error rewinding an already-succeeded bead")
	}
}
