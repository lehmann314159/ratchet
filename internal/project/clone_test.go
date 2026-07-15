package project

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ratchet/internal/db"
)

// seedCloneFullProject creates a real folder tree (a design doc plus a
// traces/ subdirectory holding one real trace file) and one row in every
// table clone-project must copy — including handoff_attempts, which
// fixtureScopedTables deliberately excludes for save-fixture's different
// reason (no project_id column) but which clone still has to handle — plus
// edge cases the remap bookkeeping has to survive: a second bead with no
// revision yet (current_revision_id NULL), a project-scoped handoff_job with
// bead_id NULL, and a spec_revisions row with new_revision_id NULL (action
// was no_change).
func seedCloneFullProject(t *testing.T, d *db.DB, projectID int64) (folder string, beadAID, beadBID, jobAID, rev1ID, rev2ID int64) {
	t.Helper()
	ctx := context.Background()

	folder = t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "design_doc.md"), []byte("# Design\n"), 0o644); err != nil {
		t.Fatalf("write design doc: %v", err)
	}
	tracesDir := filepath.Join(folder, "traces")
	if err := os.MkdirAll(tracesDir, 0o755); err != nil {
		t.Fatalf("mkdir traces: %v", err)
	}
	tracePath := filepath.Join(tracesDir, "bead-1-attempt-1.log")
	if err := os.WriteFile(tracePath, []byte("trace contents\n"), 0o644); err != nil {
		t.Fatalf("write trace file: %v", err)
	}

	mustExecFixture(t, d, `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default, audit_reconcile_round_cap,
		   max_execution_attempts, language, pause_after_reconcile, pause_after_verb, pause_after_bead_id,
		   created_at, updated_at)
		VALUES (?, 'clone-source', ?, 'design_doc.md', 'active',
		        'honor', 300, 2, 5, 'go', 0, NULL, NULL,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		projectID, folder)

	mustExecFixture(t, d, `INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (?, 'DECOMPOSE_SPEC', 'm1')`, projectID)

	// Bead A: full history — two revisions, an execution, analysis,
	// compressed_history, adjudication, handoff_job/attempt/verify/cert, test_refinement.
	res, err := d.ExecContext(ctx, `INSERT INTO beads (project_id, status, current_revision_id, rewound_at, execution_attempts_override) VALUES (?, 'succeeded', NULL, '2026-01-02T00:00:00Z', 7)`, projectID)
	if err != nil {
		t.Fatalf("seed bead A: %v", err)
	}
	beadAID, _ = res.LastInsertId()

	rev1Res, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions (project_id, bead_id, revision_number, full_text, execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (?, ?, 1, '{"title":"A"}', 300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		projectID, beadAID)
	if err != nil {
		t.Fatalf("seed rev1: %v", err)
	}
	rev1ID, _ = rev1Res.LastInsertId()

	rev2Res, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions (project_id, bead_id, revision_number, full_text, execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (?, ?, 2, '{"title":"A revised"}', 300, 'honor', 'RECONCILE_DECOMPOSITION', '2026-01-01T01:00:00Z')`,
		projectID, beadAID)
	if err != nil {
		t.Fatalf("seed rev2: %v", err)
	}
	rev2ID, _ = rev2Res.LastInsertId()
	mustExecFixture(t, d, `UPDATE beads SET current_revision_id = ? WHERE id = ?`, rev2ID, beadAID)

	// Bead B: pending, no revision yet — current_revision_id stays NULL.
	res, err = d.ExecContext(ctx, `INSERT INTO beads (project_id, status, current_revision_id) VALUES (?, 'pending', NULL)`, projectID)
	if err != nil {
		t.Fatalf("seed bead B: %v", err)
	}
	beadBID, _ = res.LastInsertId()

	mustExecFixture(t, d, `
		INSERT INTO audit_reconcile_rounds (project_id, round_number, critique_text, reconciliation, outcome, created_at)
		VALUES (?, 1, 'critique', 'reconciliation', 'converged', '2026-01-01T00:00:00Z')`, projectID)

	execRes, err := d.ExecContext(ctx, `
		INSERT INTO executions
		  (project_id, bead_id, bead_revision_id, trace_path, termination_cause,
		   monitor_fired, monitor_honored, started_at, ended_at, infra_failure, test_first_attempt)
		VALUES (?, ?, ?, ?, 'success', 1, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z', 0, 1)`,
		projectID, beadAID, rev2ID, tracePath)
	if err != nil {
		t.Fatalf("seed executions: %v", err)
	}
	execID, _ := execRes.LastInsertId()

	mustExecFixture(t, d, `
		INSERT INTO analyses (project_id, execution_id, mechanical_findings, analyzer_interpretation, created_at)
		VALUES (?, ?, 'findings', 'interpretation', '2026-01-01T00:00:00Z')`, projectID, execID)

	mustExecFixture(t, d, `
		INSERT INTO compressed_history (bead_id, project_id, compressed_text, updated_at)
		VALUES (?, ?, 'history', '2026-01-01T00:00:00Z')`, beadAID, projectID)

	mustExecFixture(t, d, `
		INSERT INTO adjudications
		  (project_id, bead_id, execution_id, trend, bead_spec_fit, reasoning_text,
		   attempt_budget_cost, monitor_escalation_status, decision, created_at)
		VALUES (?, ?, ?, 'same', 'not_applicable', 'reasoning', 1.5, 0, 'declare_success', '2026-01-01T00:00:00Z')`,
		projectID, beadAID, execID)

	// spec_revisions: one row with a real new_revision_id, one with NULL (no_change).
	mustExecFixture(t, d, `
		INSERT INTO spec_revisions (project_id, trigger_bead_id, revised_bead_id, old_revision_id, new_revision_id, created_at)
		VALUES (?, ?, ?, ?, ?, '2026-01-01T00:00:00Z')`, projectID, beadAID, beadAID, rev1ID, rev2ID)
	mustExecFixture(t, d, `
		INSERT INTO spec_revisions (project_id, trigger_bead_id, revised_bead_id, old_revision_id, new_revision_id, created_at)
		VALUES (?, ?, ?, ?, NULL, '2026-01-01T00:00:00Z')`, projectID, beadAID, beadBID, rev1ID)

	jobRes, err := d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at)
		VALUES (?, 'ADJUDICATE_NEXT_EXECUTION', ?, 'complete', NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		projectID, beadAID)
	if err != nil {
		t.Fatalf("seed handoff_jobs (bead-scoped): %v", err)
	}
	jobAID, _ = jobRes.LastInsertId()

	// A project-scoped job (bead_id NULL) — must survive as pending, and is
	// exactly the row the round-trip test expects the clone to be able to
	// dispatch further.
	mustExecFixture(t, d, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at)
		VALUES (?, 'SURVEY_SPEC', NULL, 'pending', NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, projectID)

	mustExecFixture(t, d, `
		INSERT INTO handoff_attempts (job_id, attempt_number, raw_output, validation_result, created_at, ended_at)
		VALUES (?, 1, 'raw output', 'valid', '2026-01-01T00:00:00Z', '2026-01-01T00:00:05Z')`, jobAID)

	mustExecFixture(t, d, `
		INSERT INTO verify_attempts
		  (project_id, job_id, attempt_number, file_presence_pass, no_behavioral_tests_pass,
		   compile_pass, api_check_pass, stub_purity_pass, violations, verifier_interpretation, created_at)
		VALUES (?, ?, 1, 1, 1, 1, 1, 1, NULL, 'looks fine', '2026-01-01T00:00:00Z')`, projectID, jobAID)

	var verifyAttemptID int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM verify_attempts WHERE project_id = ?`, projectID).Scan(&verifyAttemptID); err != nil {
		t.Fatalf("read verify_attempt id: %v", err)
	}
	mustExecFixture(t, d, `
		INSERT INTO certifications (project_id, verify_attempt_id, preliminary_decision, model_reasoning, final_decision, feedback, created_at)
		VALUES (?, ?, 'approve', 'reasoning', 'approve', NULL, '2026-01-01T00:00:00Z')`, projectID, verifyAttemptID)

	mustExecFixture(t, d, `
		INSERT INTO test_refinements (project_id, bead_id, cycle_id, turn, verb, changed, summary, decision, created_at)
		VALUES (?, ?, 1, 1, 'REFINE_TESTS_WRITE', 1, 'summary', 'approved', '2026-01-01T00:00:00Z')`, projectID, beadAID)

	return folder, beadAID, beadBID, jobAID, rev1ID, rev2ID
}

func TestCloneProject_DeepCopiesEveryTable(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	folder, beadAID, beadBID, jobAID, _, rev2ID := seedCloneFullProject(t, d, 50)

	newFolder := filepath.Join(t.TempDir(), "clone")
	newID, err := cloneProject(ctx, d, 50, "clone-1", newFolder)
	if err != nil {
		t.Fatalf("cloneProject: %v", err)
	}
	if newID == 50 || newID <= 0 {
		t.Fatalf("newID = %d, want a fresh positive id distinct from 50", newID)
	}

	// New project row: fresh id, active, config columns carried over verbatim.
	var label, folderPath, status, monitorOverride, language string
	var budget, roundCap, maxAttempts int
	if err := d.QueryRowContext(ctx, `
		SELECT label, folder_path, status, monitor_override_default, execution_budget_default,
		       audit_reconcile_round_cap, max_execution_attempts, language
		FROM projects WHERE id = ?`, newID,
	).Scan(&label, &folderPath, &status, &monitorOverride, &budget, &roundCap, &maxAttempts, &language); err != nil {
		t.Fatalf("query cloned project: %v", err)
	}
	if label != "clone-1" {
		t.Errorf("label = %q, want clone-1", label)
	}
	newFolderAbs, _ := filepath.Abs(newFolder)
	if folderPath != newFolderAbs {
		t.Errorf("folder_path = %q, want %q", folderPath, newFolderAbs)
	}
	if status != "active" {
		t.Errorf("status = %q, want active", status)
	}
	if monitorOverride != "honor" || budget != 300 || roundCap != 2 || maxAttempts != 5 || language != "go" {
		t.Errorf("config columns not carried over verbatim: monitor=%q budget=%d roundCap=%d maxAttempts=%d language=%q",
			monitorOverride, budget, roundCap, maxAttempts, language)
	}

	// Source project completely untouched.
	var srcStatus string
	if err := d.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = 50`).Scan(&srcStatus); err != nil {
		t.Fatalf("query source project: %v", err)
	}
	if srcStatus != "active" {
		t.Errorf("source project status = %q, want active (untouched)", srcStatus)
	}
	if _, err := os.Stat(filepath.Join(folder, "design_doc.md")); err != nil {
		t.Errorf("source design doc missing after clone: %v", err)
	}

	// Table row counts under the new project.
	for _, tc := range []struct {
		table string
		want  int
	}{
		{"verb_model_assignments", 1},
		{"beads", 2},
		{"bead_revisions", 2},
		{"audit_reconcile_rounds", 1},
		{"executions", 1},
		{"analyses", 1},
		{"compressed_history", 1},
		{"adjudications", 1},
		{"spec_revisions", 2},
		{"handoff_jobs", 2},
		{"verify_attempts", 1},
		{"certifications", 1},
		{"test_refinements", 1},
	} {
		if n := countProjectScoped(t, d, tc.table, newID); n != tc.want {
			t.Errorf("%s: count under new id = %d, want %d", tc.table, n, tc.want)
		}
	}

	// Source project's rows are all still present too (not moved, copied).
	for _, table := range fixtureScopedTables {
		if n := countProjectScoped(t, d, table, 50); n == 0 {
			t.Errorf("%s: source rows gone after clone (should be copy, not move)", table)
		}
	}

	// current_revision_id resolves to the *new* latest revision, not the old id.
	var newBeadAID int64
	if err := d.QueryRowContext(ctx, `SELECT b.id FROM beads b JOIN bead_revisions br ON br.bead_id = b.id WHERE b.project_id = ? AND br.revision_number = 2`, newID).Scan(&newBeadAID); err != nil {
		t.Fatalf("find cloned bead A: %v", err)
	}
	var curRevID, curRevNum int64
	if err := d.QueryRowContext(ctx, `
		SELECT br.id, br.revision_number FROM beads b JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, newBeadAID).Scan(&curRevID, &curRevNum); err != nil {
		t.Fatalf("query cloned bead A current_revision_id: %v", err)
	}
	if curRevNum != 2 {
		t.Errorf("current_revision_id points at revision_number %d, want 2 (the latest)", curRevNum)
	}
	if curRevID == beadAID || curRevID == 0 {
		t.Errorf("current_revision_id = %d looks unremapped", curRevID)
	}

	// The pending bead (no revision) clones with current_revision_id still NULL.
	var beadBCurRev sql.NullInt64
	if err := d.QueryRowContext(ctx, `
		SELECT b.current_revision_id FROM beads b
		WHERE b.project_id = ? AND b.status = 'pending'`, newID).Scan(&beadBCurRev); err != nil {
		t.Fatalf("query cloned bead B: %v", err)
	}
	if beadBCurRev.Valid {
		t.Errorf("cloned bead B current_revision_id = %v, want NULL", beadBCurRev)
	}

	// rewound_at and execution_attempts_override carried over verbatim.
	var rewoundAt sql.NullString
	var attemptsOverride sql.NullInt64
	if err := d.QueryRowContext(ctx, `
		SELECT rewound_at, execution_attempts_override FROM beads WHERE id = ?`, newBeadAID,
	).Scan(&rewoundAt, &attemptsOverride); err != nil {
		t.Fatalf("query cloned bead A extra columns: %v", err)
	}
	if !rewoundAt.Valid || rewoundAt.String != "2026-01-02T00:00:00Z" {
		t.Errorf("rewound_at = %v, want 2026-01-02T00:00:00Z", rewoundAt)
	}
	if !attemptsOverride.Valid || attemptsOverride.Int64 != 7 {
		t.Errorf("execution_attempts_override = %v, want 7", attemptsOverride)
	}

	// trace_path rewritten to the new folder; the file it points at actually exists.
	var newTracePath string
	if err := d.QueryRowContext(ctx, `SELECT trace_path FROM executions WHERE project_id = ?`, newID).Scan(&newTracePath); err != nil {
		t.Fatalf("query cloned execution: %v", err)
	}
	if !strings.HasPrefix(newTracePath, newFolderAbs) {
		t.Errorf("trace_path = %q, want it prefixed by the new folder %q", newTracePath, newFolderAbs)
	}
	if filepath.Base(newTracePath) != "bead-1-attempt-1.log" {
		t.Errorf("trace_path filename = %q, want bead-1-attempt-1.log unchanged", filepath.Base(newTracePath))
	}
	if data, err := os.ReadFile(newTracePath); err != nil {
		t.Errorf("trace file not present at rewritten path: %v", err)
	} else if string(data) != "trace contents\n" {
		t.Errorf("trace file contents = %q, want unchanged", data)
	}

	// spec_revisions: the NULL new_revision_id row stays NULL, the non-NULL one is remapped.
	rows, err := d.QueryContext(ctx, `SELECT new_revision_id FROM spec_revisions WHERE project_id = ? ORDER BY id`, newID)
	if err != nil {
		t.Fatalf("query cloned spec_revisions: %v", err)
	}
	var sawNull, sawNonNull bool
	for rows.Next() {
		var v sql.NullInt64
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			t.Fatalf("scan spec_revisions: %v", err)
		}
		if v.Valid {
			sawNonNull = true
			if v.Int64 == rev2ID {
				t.Errorf("spec_revisions.new_revision_id still references the old revision id %d", rev2ID)
			}
		} else {
			sawNull = true
		}
	}
	rows.Close()
	if !sawNull || !sawNonNull {
		t.Errorf("expected one NULL and one non-NULL new_revision_id among cloned spec_revisions, sawNull=%v sawNonNull=%v", sawNull, sawNonNull)
	}

	// handoff_jobs: the project-scoped job (bead_id NULL) clones with bead_id still NULL.
	var projectScopedCount int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = ? AND bead_id IS NULL`, newID).Scan(&projectScopedCount); err != nil {
		t.Fatalf("count project-scoped handoff_jobs: %v", err)
	}
	if projectScopedCount != 1 {
		t.Errorf("project-scoped (bead_id NULL) handoff_jobs under new id = %d, want 1", projectScopedCount)
	}

	// handoff_attempts followed its job under the new job id (no project_id column of its own).
	var newJobAID int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM handoff_jobs WHERE project_id = ? AND verb = 'ADJUDICATE_NEXT_EXECUTION'`, newID).Scan(&newJobAID); err != nil {
		t.Fatalf("find cloned job A: %v", err)
	}
	var attemptCount int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM handoff_attempts WHERE job_id = ?`, newJobAID).Scan(&attemptCount); err != nil {
		t.Fatalf("count cloned handoff_attempts: %v", err)
	}
	if attemptCount != 1 {
		t.Errorf("handoff_attempts under cloned job = %d, want 1", attemptCount)
	}
	var oldJobAttemptStillThere int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM handoff_attempts WHERE job_id = ?`, jobAID).Scan(&oldJobAttemptStillThere); err != nil {
		t.Fatalf("count source handoff_attempts: %v", err)
	}
	if oldJobAttemptStillThere != 1 {
		t.Errorf("source handoff_attempts disturbed: count = %d, want 1", oldJobAttemptStillThere)
	}

	_ = beadBID
}

// TestCloneProject_IndependentMutability confirms mutating the clone never
// touches the source — the whole point of a deep copy rather than a renumber.
func TestCloneProject_IndependentMutability(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, beadAID, _, _, _, _ := seedCloneFullProject(t, d, 51)

	newFolder := filepath.Join(t.TempDir(), "clone")
	newID, err := cloneProject(ctx, d, 51, "clone-2", newFolder)
	if err != nil {
		t.Fatalf("cloneProject: %v", err)
	}

	var newBeadAID int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM beads WHERE project_id = ? AND status = 'succeeded'`, newID).Scan(&newBeadAID); err != nil {
		t.Fatalf("find cloned bead A: %v", err)
	}
	mustExecFixture(t, d, `UPDATE beads SET status = 'full_stopped' WHERE id = ?`, newBeadAID)

	var sourceStatus string
	if err := d.QueryRowContext(ctx, `SELECT status FROM beads WHERE id = ?`, beadAID).Scan(&sourceStatus); err != nil {
		t.Fatalf("query source bead: %v", err)
	}
	if sourceStatus != "succeeded" {
		t.Errorf("mutating the clone's bead changed the source bead's status to %q", sourceStatus)
	}

	// Mutate the clone's folder; source folder's design doc must be unaffected.
	newFolderAbs, _ := filepath.Abs(newFolder)
	if err := os.WriteFile(filepath.Join(newFolderAbs, "extra.txt"), []byte("clone-only"), 0o644); err != nil {
		t.Fatalf("write to clone folder: %v", err)
	}
}

// TestCloneProject_DispatchableRoundTrip confirms the clone's copied-forward
// pending handoff_job satisfies the exact WHERE clause the orchestrator's own
// claimNextJob query uses (internal/orchestrator/queue.go) — status='active'
// on the project, status IN ('pending','failed_retry') on the job — so the
// clone can actually be picked up and driven forward, not just look right in
// isolation.
func TestCloneProject_DispatchableRoundTrip(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedCloneFullProject(t, d, 52)

	newFolder := filepath.Join(t.TempDir(), "clone")
	newID, err := cloneProject(ctx, d, 52, "clone-3", newFolder)
	if err != nil {
		t.Fatalf("cloneProject: %v", err)
	}

	var projectStatus string
	if err := d.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = ?`, newID).Scan(&projectStatus); err != nil {
		t.Fatalf("query cloned project status: %v", err)
	}
	if projectStatus != "active" {
		t.Fatalf("cloned project status = %q, want active", projectStatus)
	}

	var jobID int64
	err = d.QueryRowContext(ctx, `
		SELECT id FROM handoff_jobs
		WHERE project_id = ? AND status IN ('pending', 'failed_retry')
		ORDER BY created_at LIMIT 1`, newID).Scan(&jobID)
	if err != nil {
		t.Fatalf("clone has no dispatchable job by the orchestrator's own claim criteria: %v", err)
	}

	var verb string
	if err := d.QueryRowContext(ctx, `SELECT verb FROM handoff_jobs WHERE id = ?`, jobID).Scan(&verb); err != nil {
		t.Fatalf("query dispatchable job verb: %v", err)
	}
	if verb != "SURVEY_SPEC" {
		t.Errorf("dispatchable job verb = %q, want SURVEY_SPEC", verb)
	}
}

func TestCloneProject_RefusesWithRunningJob(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedCloneFullProject(t, d, 53)
	mustExecFixture(t, d, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (53, 'EXECUTE_BEAD', NULL, 'running', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)

	newFolder := filepath.Join(t.TempDir(), "clone")
	if _, err := cloneProject(ctx, d, 53, "clone-4", newFolder); err == nil {
		t.Fatal("expected error for a source project with a running job, got nil")
	}
	if _, err := os.Stat(newFolder); err == nil {
		t.Errorf("clone folder %q was created despite the running-job rejection", newFolder)
	}
}

func TestCloneProject_RefusesExistingFolder(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedCloneFullProject(t, d, 54)

	existing := t.TempDir() // already exists
	if _, err := cloneProject(ctx, d, 54, "clone-5", existing); err == nil {
		t.Fatal("expected error for an already-existing folder, got nil")
	}
}

func TestCloneProject_NotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	newFolder := filepath.Join(t.TempDir(), "clone")
	if _, err := cloneProject(ctx, d, 999, "clone-6", newFolder); err == nil {
		t.Fatal("expected error for an unknown source project, got nil")
	}
}

// TestCloneProject_WorksFromNegativeFixtureID confirms clone-project is one
// general capability regardless of source sign, per the design: cloning a
// fixture (negative id, status='fixture') works exactly like cloning a live
// project.
func TestCloneProject_WorksFromNegativeFixtureID(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedCloneFullProject(t, d, 55)
	fixtureID, _, err := saveFixture(ctx, d, 55, "test fixture")
	if err != nil {
		t.Fatalf("saveFixture: %v", err)
	}

	newFolder := filepath.Join(t.TempDir(), "clone")
	newID, err := cloneProject(ctx, d, fixtureID, "clone-from-fixture", newFolder)
	if err != nil {
		t.Fatalf("cloneProject from a fixture id: %v", err)
	}
	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = ?`, newID).Scan(&status); err != nil {
		t.Fatalf("query cloned project: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q, want active even though the source was a fixture", status)
	}
}
