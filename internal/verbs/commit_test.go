package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"ratchet/internal/db"
)

// --- DB helpers ---

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// seedProject inserts a minimal projects row with the given negative ID.
// label states what the fixture tests (convention from ratchet_testing_conventions.md).
func seedProject(t *testing.T, d *db.DB, id int64, label string) {
	t.Helper()
	_, err := d.ExecContext(context.Background(), `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (?, ?, '/tmp', 'design.md', 'active', 'honor', 300, 2,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, label)
	if err != nil {
		t.Fatalf("seedProject %d: %v", id, err)
	}
}

// seedBead inserts a bead + revision 1, returns (beadID, revisionID).
// full_text is stored as JSON so json_extract queries (RECONCILE applyFixes) work.
func seedBead(t *testing.T, d *db.DB, projectID int64, title string) (beadID, revID int64) {
	t.Helper()
	ctx := context.Background()
	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status, current_revision_id) VALUES (?, 'pending', NULL)`, projectID)
	if err != nil {
		t.Fatalf("seedBead insert: %v", err)
	}
	beadID, _ = res.LastInsertId()

	fullText, _ := json.Marshal(map[string]any{
		"title": title, "full_text": "spec for " + title,
		"execution_budget": 300, "monitor_override": "honor",
	})
	res, err = d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text,
		   execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (?, ?, 1, ?, 300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		projectID, beadID, string(fullText))
	if err != nil {
		t.Fatalf("seedBead revision: %v", err)
	}
	revID, _ = res.LastInsertId()

	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
		t.Fatalf("seedBead set current_revision_id: %v", err)
	}
	return beadID, revID
}

// seedExecution inserts an executions row with termination_cause set.
// monitorFired nil → NULL; otherwise 0 or 1.
func seedExecution(t *testing.T, d *db.DB, projectID, beadID, revID int64, terminationCause string, monitorFired *int) int64 {
	t.Helper()
	ctx := context.Background()
	var res sql.Result
	var err error
	if monitorFired != nil {
		res, err = d.ExecContext(ctx, `
			INSERT INTO executions
			  (project_id, bead_id, bead_revision_id, trace_path,
			   termination_cause, monitor_fired, monitor_honored,
			   started_at, ended_at)
			VALUES (?, ?, ?, '/tmp/trace.log', ?, ?, 0,
			        '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z')`,
			projectID, beadID, revID, terminationCause, *monitorFired)
	} else {
		res, err = d.ExecContext(ctx, `
			INSERT INTO executions
			  (project_id, bead_id, bead_revision_id, trace_path,
			   termination_cause, monitor_fired, monitor_honored,
			   started_at, ended_at)
			VALUES (?, ?, ?, '/tmp/trace.log', ?, NULL, 0,
			        '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z')`,
			projectID, beadID, revID, terminationCause)
	}
	if err != nil {
		t.Fatalf("seedExecution: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// seedJob inserts a handoff_jobs row in 'running' status and returns its ID.
func seedJob(t *testing.T, d *db.DB, projectID int64, verb string, beadID sql.NullInt64) *db.HandoffJob {
	t.Helper()
	res, err := d.ExecContext(context.Background(), `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, ?, 'running', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		projectID, verb, beadID)
	if err != nil {
		t.Fatalf("seedJob: %v", err)
	}
	id, _ := res.LastInsertId()
	return &db.HandoffJob{
		ID:        id,
		ProjectID: projectID,
		Verb:      verb,
		BeadID:    beadID,
		Status:    "running",
	}
}

// inTx runs fn inside a new transaction; commits on success, calls t.Fatal on error.
func inTx(t *testing.T, d *db.DB, fn func(*sql.Tx) error) {
	t.Helper()
	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Commit returned error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
}

// countRows runs a COUNT query and returns the result.
func countRows(t *testing.T, d *db.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("countRows(%q): %v", query, err)
	}
	return n
}

// --- DECOMPOSE_SPEC ---

func TestDecomposeSpecCommit(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: DECOMPOSE_SPEC commit")

	job := seedJob(t, d, -1, db.VerbDecomposeSpec, sql.NullInt64{})
	out := DecomposeSpecOutput{
		Beads: []ParsedBead{
			{Title: "B01", FullText: "build the widget", ExecutionBudget: 300, MonitorOverride: "honor"},
			{Title: "B02", FullText: "write integration tests", ExecutionBudget: 120, MonitorOverride: "ignore"},
		},
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&DecomposeSpec{}).Commit(ctx, tx, job, out)
	})

	if n := countRows(t, d, `SELECT COUNT(*) FROM beads WHERE project_id = -1`); n != 2 {
		t.Errorf("beads = %d, want 2", n)
	}
	// Both beads get revision 1.
	if n := countRows(t, d, `SELECT COUNT(*) FROM bead_revisions WHERE project_id = -1 AND revision_number = 1`); n != 2 {
		t.Errorf("bead_revisions rev1 = %d, want 2", n)
	}
	// current_revision_id must be set on every bead (not NULL).
	if n := countRows(t, d, `SELECT COUNT(*) FROM beads WHERE project_id = -1 AND current_revision_id IS NULL`); n != 0 {
		t.Errorf("beads with NULL current_revision_id = %d, want 0", n)
	}
	// AUDIT_DECOMPOSITION enqueued.
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ?`, db.VerbAuditDecomposition); n != 1 {
		t.Errorf("AUDIT_DECOMPOSITION jobs = %d, want 1", n)
	}
}

// --- AUDIT_DECOMPOSITION ---

func TestAuditDecompositionCommit(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: AUDIT_DECOMPOSITION commit")

	job := seedJob(t, d, -1, db.VerbAuditDecomposition, sql.NullInt64{})
	out := AuditDecompositionOutput{OverallVerdict: "no_issues"}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AuditDecomposition{}).Commit(ctx, tx, job, out)
	})

	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ?`, db.VerbReconcileDecomposition); n != 1 {
		t.Errorf("RECONCILE_DECOMPOSITION jobs = %d, want 1", n)
	}
}

// --- RECONCILE_DECOMPOSITION ---

func TestReconcileDecompositionCommitConverged(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: RECONCILE_DECOMPOSITION commit/converged")
	beadID, _ := seedBead(t, d, -1, "B01")

	job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h := &ReconcileDecomposition{
		lastCritique:    "the stride formula uses 3 bytes/pixel; NRGBA is always 4",
		lastRoundsSoFar: 0,
	}
	out := ReconcileDecompositionOutput{
		Responses: []ReconcileResponse{
			{
				BeadTitle: "B01", Action: "agree_and_fix", Reason: "correct finding",
				UpdatedBead: &ParsedBead{Title: "B01", FullText: "revised with stride=4", ExecutionBudget: 300, MonitorOverride: "honor"},
			},
		},
		UpdatedBeads: []ParsedBead{
			{Title: "B01", FullText: "revised with stride=4", ExecutionBudget: 300, MonitorOverride: "honor"},
		},
	}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	var outcome string
	if err := d.QueryRowContext(ctx,
		`SELECT outcome FROM audit_reconcile_rounds WHERE project_id = -1 AND round_number = 1`,
	).Scan(&outcome); err != nil {
		t.Fatalf("audit_reconcile_rounds row missing: %v", err)
	}
	if outcome != "converged" {
		t.Errorf("outcome = %q, want converged", outcome)
	}
	// Fix applied: bead B01 should now have revision 2.
	if n := countRows(t, d, `SELECT COUNT(*) FROM bead_revisions WHERE bead_id = ? AND revision_number = 2`, beadID); n != 1 {
		t.Errorf("bead_revisions rev2 = %d, want 1 (agree_and_fix must create a new revision)", n)
	}
	// EXECUTE_BEAD enqueued for the bead.
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ? AND bead_id = ?`, db.VerbExecuteBead, beadID); n != 1 {
		t.Errorf("EXECUTE_BEAD jobs = %d, want 1", n)
	}
}

func TestReconcileDecompositionCommitDisagreedContinuing(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: RECONCILE_DECOMPOSITION commit/disagreed-continuing")
	seedBead(t, d, -1, "B01")

	job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h := &ReconcileDecomposition{lastCritique: "critique text", lastRoundsSoFar: 0}
	out := ReconcileDecompositionOutput{
		Responses: []ReconcileResponse{
			{BeadTitle: "B01", Action: "disagree", Reason: "the audit finding is wrong — named approach per §2.3"},
		},
		UpdatedBeads: []ParsedBead{
			{Title: "B01", FullText: "original spec unchanged", ExecutionBudget: 300, MonitorOverride: "honor"},
		},
	}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	var outcome string
	if err := d.QueryRowContext(ctx,
		`SELECT outcome FROM audit_reconcile_rounds WHERE project_id = -1`,
	).Scan(&outcome); err != nil {
		t.Fatalf("audit_reconcile_rounds row missing: %v", err)
	}
	if outcome != "disagreed_continuing" {
		t.Errorf("outcome = %q, want disagreed_continuing", outcome)
	}
	// Another AUDIT_DECOMPOSITION job enqueued to continue the debate loop.
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ?`, db.VerbAuditDecomposition); n != 1 {
		t.Errorf("AUDIT_DECOMPOSITION jobs = %d, want 1 (re-enqueued for next round)", n)
	}
}

func TestReconcileDecompositionCommitEscalated(t *testing.T) {
	// Round cap is 2; lastRoundsSoFar=1 means this is round 2 — a disagree here escalates.
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: RECONCILE_DECOMPOSITION commit/escalated-at-cap")
	seedBead(t, d, -1, "B01")

	job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h := &ReconcileDecomposition{lastCritique: "critique text", lastRoundsSoFar: 1}
	out := ReconcileDecompositionOutput{
		Responses: []ReconcileResponse{
			{BeadTitle: "B01", Action: "disagree", Reason: "still disagree after round 2"},
		},
		UpdatedBeads: []ParsedBead{
			{Title: "B01", FullText: "spec", ExecutionBudget: 300, MonitorOverride: "honor"},
		},
	}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	var outcome string
	if err := d.QueryRowContext(ctx,
		`SELECT outcome FROM audit_reconcile_rounds WHERE project_id = -1`,
	).Scan(&outcome); err != nil {
		t.Fatalf("audit_reconcile_rounds row missing: %v", err)
	}
	if outcome != "escalated" {
		t.Errorf("outcome = %q, want escalated", outcome)
	}
	// The handoff_jobs row must be marked escalated.
	var status string
	if err := d.QueryRowContext(ctx,
		`SELECT status FROM handoff_jobs WHERE id = ?`, job.ID,
	).Scan(&status); err != nil {
		t.Fatalf("handoff_jobs row missing: %v", err)
	}
	if status != "escalated" {
		t.Errorf("job status = %q, want escalated", status)
	}
}

// --- ANALYZE_EXECUTION ---

func TestAnalyzeExecutionCommit(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ANALYZE_EXECUTION commit")
	beadID, revID := seedBead(t, d, -1, "B01")
	seedExecution(t, d, -1, beadID, revID, "success", nil)

	job := seedJob(t, d, -1, db.VerbAnalyzeExecution, sql.NullInt64{Int64: beadID, Valid: true})
	out := AnalyzeExecutionOutput{
		MechanicalFindings:     "TestReadBit FAIL exit 1 line 88",
		AnalyzerInterpretation: "suggests off-by-one in Pix index",
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AnalyzeExecution{}).Commit(ctx, tx, job, out)
	})

	if n := countRows(t, d, `SELECT COUNT(*) FROM analyses WHERE project_id = -1`); n != 1 {
		t.Errorf("analyses = %d, want 1", n)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ? AND bead_id = ?`, db.VerbCompressAnalysis, beadID); n != 1 {
		t.Errorf("COMPRESS_ANALYSIS jobs = %d, want 1", n)
	}
}

// --- COMPRESS_ANALYSIS ---

func TestCompressAnalysisCommit(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: COMPRESS_ANALYSIS commit")
	beadID, _ := seedBead(t, d, -1, "B01")

	job := seedJob(t, d, -1, db.VerbCompressAnalysis, sql.NullInt64{Int64: beadID, Valid: true})
	out := CompressAnalysisOutput{CompressedText: "attempt 1: TestReadBit FAIL nil ptr; same failure on attempt 2"}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&CompressAnalysis{}).Commit(ctx, tx, job, out)
	})

	var got string
	if err := d.QueryRowContext(ctx,
		`SELECT compressed_text FROM compressed_history WHERE bead_id = ?`, beadID,
	).Scan(&got); err != nil {
		t.Fatalf("compressed_history missing: %v", err)
	}
	if got != out.CompressedText {
		t.Errorf("compressed_text = %q, want %q", got, out.CompressedText)
	}
	// ADJUDICATE enqueued after first Commit.
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ? AND bead_id = ?`, db.VerbAdjudicateNextExecution, beadID); n != 1 {
		t.Errorf("ADJUDICATE jobs = %d, want 1", n)
	}

	// Second call must upsert compressed_history — not create a duplicate row.
	inTx(t, d, func(tx *sql.Tx) error {
		return (&CompressAnalysis{}).Commit(ctx, tx, job, CompressAnalysisOutput{CompressedText: "updated on attempt 3"})
	})
	if n := countRows(t, d, `SELECT COUNT(*) FROM compressed_history WHERE bead_id = ?`, beadID); n != 1 {
		t.Errorf("compressed_history rows = %d after upsert, want 1", n)
	}
}

// --- ADJUDICATE_NEXT_EXECUTION ---

// TestAdjudicateMonitorFiredReadDirectly is the targeted test for the
// monitor_escalation_status fix. monitor_fired=1 with termination_cause='success'
// means the monitor fired but was ignored (honor flag was 'ignore').
// Before the fix (reading termination_cause IN (...) instead of monitor_fired),
// this case produced monitor_escalation_status=0 — wrong.
func TestAdjudicateMonitorFiredReadDirectly(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE monitor_escalation_status — monitor fired but ignored")
	beadID, revID := seedBead(t, d, -1, "B01")

	one := 1
	seedExecution(t, d, -1, beadID, revID, "success", &one) // monitor_fired=1, cause='success'

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend: "same", BeadSpecFit: "bead_problem",
		Reasoning: "the bead spec is missing the stride-formula constraint",
		Decision:  "execute_as_is",
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{}).Commit(ctx, tx, job, out)
	})

	var status int
	if err := d.QueryRowContext(ctx,
		`SELECT monitor_escalation_status FROM adjudications WHERE bead_id = ?`, beadID,
	).Scan(&status); err != nil {
		t.Fatalf("adjudications row missing: %v", err)
	}
	if status != 1 {
		t.Errorf("monitor_escalation_status = %d, want 1 (monitor fired regardless of whether it caused termination)", status)
	}
}

// TestAdjudicateMonitorFiredNullTreatedAsFalse confirms COALESCE(monitor_fired, 0)
// — a NULL monitor_fired (subprocess hasn't written yet, or not applicable)
// must produce monitor_escalation_status=0, not a scan error.
func TestAdjudicateMonitorFiredNullTreatedAsFalse(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE monitor_escalation_status — NULL monitor_fired")
	beadID, revID := seedBead(t, d, -1, "B01")

	seedExecution(t, d, -1, beadID, revID, "success", nil) // monitor_fired=NULL

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend: "same", BeadSpecFit: "bead_problem",
		Reasoning: "the bead spec omits the required return type",
		Decision:  "execute_as_is",
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{}).Commit(ctx, tx, job, out)
	})

	var status int
	if err := d.QueryRowContext(ctx,
		`SELECT monitor_escalation_status FROM adjudications WHERE bead_id = ?`, beadID,
	).Scan(&status); err != nil {
		t.Fatalf("adjudications row missing: %v", err)
	}
	if status != 0 {
		t.Errorf("monitor_escalation_status = %d, want 0 (NULL monitor_fired → not escalated)", status)
	}
}

func TestAdjudicateNextExecutionCommitExecuteAsIs(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE commit/execute_as_is")
	beadID, revID := seedBead(t, d, -1, "B01")
	zero := 0
	seedExecution(t, d, -1, beadID, revID, "success", &zero)

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend: "same", BeadSpecFit: "bead_problem",
		Reasoning: "the bead spec is missing the stride-formula constraint",
		Decision:  "execute_as_is",
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{}).Commit(ctx, tx, job, out)
	})

	if n := countRows(t, d, `SELECT COUNT(*) FROM adjudications WHERE bead_id = ?`, beadID); n != 1 {
		t.Errorf("adjudications = %d, want 1", n)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE verb = ? AND bead_id = ?`, db.VerbExecuteBead, beadID); n != 1 {
		t.Errorf("EXECUTE_BEAD jobs = %d, want 1", n)
	}
}

func TestAdjudicateNextExecutionCommitExecuteRevised(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE commit/execute_revised")
	beadID, revID := seedBead(t, d, -1, "B01")
	zero := 0
	seedExecution(t, d, -1, beadID, revID, "success", &zero)

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend: "narrower", BeadSpecFit: "bead_problem",
		Reasoning: "the bead spec is missing the stride-formula constraint; revised to add it",
		Decision:  "execute_revised",
		RevisedBead: &ParsedBead{
			Title: "B01", FullText: "revised spec: stride must be 4 bytes/pixel",
			ExecutionBudget: 300, MonitorOverride: "honor",
		},
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{}).Commit(ctx, tx, job, out)
	})

	// New revision (revision 2) created.
	if n := countRows(t, d, `SELECT COUNT(*) FROM bead_revisions WHERE bead_id = ? AND revision_number = 2`, beadID); n != 1 {
		t.Errorf("bead_revisions rev2 = %d, want 1", n)
	}
	// current_revision_id updated to the new revision.
	var revNum int
	if err := d.QueryRowContext(ctx, `
		SELECT br.revision_number FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID,
	).Scan(&revNum); err != nil {
		t.Fatalf("current revision query: %v", err)
	}
	if revNum != 2 {
		t.Errorf("current revision = %d, want 2", revNum)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE verb = ? AND bead_id = ?`, db.VerbExecuteBead, beadID); n != 1 {
		t.Errorf("EXECUTE_BEAD jobs = %d, want 1", n)
	}
}

func TestAdjudicateNextExecutionCommitFullStop(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE commit/full_stop")
	beadID, revID := seedBead(t, d, -1, "B01")
	zero := 0
	seedExecution(t, d, -1, beadID, revID, "success", &zero)

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend: "same", BeadSpecFit: "bead_problem",
		Reasoning: "the bead spec is fundamentally ambiguous; repeated revisions have not resolved it",
		Decision:  "full_stop",
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{}).Commit(ctx, tx, job, out)
	})

	var beadStatus string
	if err := d.QueryRowContext(ctx, `SELECT status FROM beads WHERE id = ?`, beadID).Scan(&beadStatus); err != nil {
		t.Fatalf("bead row missing: %v", err)
	}
	if beadStatus != "full_stopped" {
		t.Errorf("bead status = %q, want full_stopped", beadStatus)
	}
	// Only bead in project → project also full_stopped.
	var projStatus string
	if err := d.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = -1`).Scan(&projStatus); err != nil {
		t.Fatalf("project row missing: %v", err)
	}
	if projStatus != "full_stopped" {
		t.Errorf("project status = %q, want full_stopped", projStatus)
	}
}

func TestAdjudicateNextExecutionCommitExecutionCap(t *testing.T) {
	// Cap=2, bead already has 2 executions — execute_as_is must escalate instead of retry.
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE execution cap")
	_, _ = d.ExecContext(ctx, `UPDATE projects SET max_execution_attempts = 2 WHERE id = -1`)
	beadID, revID := seedBead(t, d, -1, "B01")
	zero := 0
	seedExecution(t, d, -1, beadID, revID, "timeout", &zero)
	seedExecution(t, d, -1, beadID, revID, "timeout", &zero)

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend: "same", BeadSpecFit: "execution_capability_problem",
		Reasoning: "timed out twice, runner could not complete the implementation",
		Decision:  "execute_as_is",
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{}).Commit(ctx, tx, job, out)
	})

	// Job must be escalated, not enqueue another EXECUTE_BEAD.
	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, job.ID).Scan(&status); err != nil {
		t.Fatalf("job row missing: %v", err)
	}
	if status != "escalated" {
		t.Errorf("job status = %q, want escalated (cap reached)", status)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE verb = ? AND bead_id = ?`, db.VerbExecuteBead, beadID); n != 0 {
		t.Errorf("unexpected EXECUTE_BEAD job enqueued after cap reached")
	}
}

func TestAdjudicateNextExecutionCommitDeclareSuccess(t *testing.T) {
	// Single bead declared success → bead 'succeeded', project 'complete'.
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE commit/declare_success single bead")
	beadID, revID := seedBead(t, d, -1, "B01")
	zero := 0
	seedExecution(t, d, -1, beadID, revID, "success", &zero)

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend:       "not_applicable",
		BeadSpecFit: "not_applicable",
		Reasoning:   "All exit criteria confirmed met: TestDeterminism, TestBoundary, TestStateAdvancement all passed.",
		Decision:    "declare_success",
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{}).Commit(ctx, tx, job, out)
	})

	var beadStatus string
	if err := d.QueryRowContext(ctx, `SELECT status FROM beads WHERE id = ?`, beadID).Scan(&beadStatus); err != nil {
		t.Fatalf("bead row missing: %v", err)
	}
	if beadStatus != "succeeded" {
		t.Errorf("bead status = %q, want succeeded", beadStatus)
	}
	// Only bead in project → project also complete.
	var projStatus string
	if err := d.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = -1`).Scan(&projStatus); err != nil {
		t.Fatalf("project row missing: %v", err)
	}
	if projStatus != "complete" {
		t.Errorf("project status = %q, want complete", projStatus)
	}
	// adjudications row written.
	if n := countRows(t, d, `SELECT COUNT(*) FROM adjudications WHERE bead_id = ?`, beadID); n != 1 {
		t.Errorf("adjudications = %d, want 1", n)
	}
	// No next job enqueued for the bead (it is done).
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE bead_id = ? AND verb != ?`, beadID, db.VerbAdjudicateNextExecution); n != 0 {
		t.Errorf("unexpected next job after declare_success: %d", n)
	}
}

func TestAdjudicateNextExecutionCommitDeclareSuccessPartial(t *testing.T) {
	// Two beads: B01 declared success, B02 still pending. Project stays active.
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE commit/declare_success partial — project stays active")
	beadID1, revID1 := seedBead(t, d, -1, "B01")
	seedBead(t, d, -1, "B02")
	zero := 0
	seedExecution(t, d, -1, beadID1, revID1, "success", &zero)

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID1, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend:       "not_applicable",
		BeadSpecFit: "not_applicable",
		Reasoning:   "All exit criteria confirmed met for B01.",
		Decision:    "declare_success",
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{}).Commit(ctx, tx, job, out)
	})

	var projStatus string
	if err := d.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = -1`).Scan(&projStatus); err != nil {
		t.Fatalf("project row missing: %v", err)
	}
	if projStatus != "active" {
		t.Errorf("project status = %q, want active (B02 still pending)", projStatus)
	}
}

func TestAdjudicateNextExecutionCommitFullStopPartial(t *testing.T) {
	// Two beads: one full_stopped, one still pending. Project must stay active.
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE commit/full_stop partial — project stays active")
	beadID1, revID1 := seedBead(t, d, -1, "B01")
	seedBead(t, d, -1, "B02") // B02 stays pending
	zero := 0
	seedExecution(t, d, -1, beadID1, revID1, "success", &zero)

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID1, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend: "same", BeadSpecFit: "bead_problem",
		Reasoning: "bead spec is irresolvable",
		Decision:  "full_stop",
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{}).Commit(ctx, tx, job, out)
	})

	var projStatus string
	if err := d.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = -1`).Scan(&projStatus); err != nil {
		t.Fatalf("project row missing: %v", err)
	}
	if projStatus != "active" {
		t.Errorf("project status = %q, want active (B02 still pending)", projStatus)
	}
}
