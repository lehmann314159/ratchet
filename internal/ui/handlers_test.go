package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ratchet/internal/db"
)

func openTestServer(t *testing.T) (*server, *db.DB) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	s, err := newServer(d)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return s, d
}

// seedProject inserts a minimal project row, returning its ID (always 1).
func seedProject(t *testing.T, d *db.DB) int64 {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (1, 'p', '/tmp', 'design.md', 'active', 'honor', 300, 2,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return 1
}

// seedBead inserts a bead with one bead_revisions row (revision 1, the given
// budget), returning the bead ID.
func seedBead(t *testing.T, d *db.DB, projectID int64, budget int) int64 {
	t.Helper()
	ctx := context.Background()
	res, err := d.ExecContext(ctx, `
		INSERT INTO beads (project_id, status, current_revision_id) VALUES (?, 'pending', NULL)`, projectID)
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	beadID, _ := res.LastInsertId()
	full, _ := json.Marshal(map[string]any{"title": "t", "output_files": []string{}, "exit_criteria": []string{}})
	revRes, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (?, ?, 1, ?, ?, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		projectID, beadID, string(full), budget)
	if err != nil {
		t.Fatalf("seed bead_revisions: %v", err)
	}
	revID, _ := revRes.LastInsertId()
	if _, err := d.ExecContext(ctx, `UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
		t.Fatalf("set current_revision_id: %v", err)
	}
	return beadID
}

// seedJob inserts a handoff_jobs row with the given status and bead_id
// (0 = project-scoped, NULL bead_id), returning the job ID.
func seedJob(t *testing.T, d *db.DB, projectID, beadID int64, verb, status string) int64 {
	t.Helper()
	ctx := context.Background()
	var res sql.Result
	var err error
	if beadID == 0 {
		res, err = d.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, NULL, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, projectID, verb, status)
	} else {
		res, err = d.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, projectID, verb, beadID, status)
	}
	if err != nil {
		t.Fatalf("seed handoff_jobs: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func jobStatus(t *testing.T, d *db.DB, jobID int64) string {
	t.Helper()
	var status string
	if err := d.QueryRowContext(context.Background(),
		`SELECT status FROM handoff_jobs WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("query job status: %v", err)
	}
	return status
}

func doPost(t *testing.T, s *server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body strings.Reader
	if form != nil {
		body = *strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(http.MethodPost, path, &body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// --- handleRequeue ---

func TestHandleRequeue_EscalatedJobSucceeds(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	jobID := seedJob(t, d, pid, 0, "RECONCILE_DECOMPOSITION", "escalated")

	rec := doPost(t, s, "/escalations/"+strconv.FormatInt(jobID, 10)+"/requeue", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := jobStatus(t, d, jobID); got != "pending" {
		t.Errorf("expected status 'pending', got %q", got)
	}
}

// TestHandleRequeue_NonEscalatedJobConflicts reproduces the Stage 8 audit
// finding: a stale escalation-detail page (or a duplicate/retried request)
// must not be able to requeue a job that has already moved on — e.g. it's
// currently 'running' under the orchestrator. Before the fix, the UPDATE had
// no status guard at all and would silently reset any job regardless of its
// current state.
func TestHandleRequeue_NonEscalatedJobConflicts(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	jobID := seedJob(t, d, pid, 0, "RECONCILE_DECOMPOSITION", "running")

	rec := doPost(t, s, "/escalations/"+strconv.FormatInt(jobID, 10)+"/requeue", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := jobStatus(t, d, jobID); got != "running" {
		t.Errorf("expected status to remain 'running' (bug reproduced if changed), got %q", got)
	}
}

// --- handleClose ---

func TestHandleClose_EscalatedJobSucceeds(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	jobID := seedJob(t, d, pid, 0, "RECONCILE_DECOMPOSITION", "escalated")

	rec := doPost(t, s, "/escalations/"+strconv.FormatInt(jobID, 10)+"/close", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := jobStatus(t, d, jobID); got != "complete" {
		t.Errorf("expected status 'complete', got %q", got)
	}
}

func TestHandleClose_NonEscalatedJobConflicts(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	jobID := seedJob(t, d, pid, 0, "RECONCILE_DECOMPOSITION", "pending")

	rec := doPost(t, s, "/escalations/"+strconv.FormatInt(jobID, 10)+"/close", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := jobStatus(t, d, jobID); got != "pending" {
		t.Errorf("expected status to remain 'pending', got %q", got)
	}
}

// --- handleRequeuWithBudget ---

func TestHandleRequeuWithBudget_EscalatedJobSucceeds(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	beadID := seedBead(t, d, pid, 300)
	jobID := seedJob(t, d, pid, beadID, "EXECUTE_BEAD", "escalated")

	rec := doPost(t, s, "/escalations/"+strconv.FormatInt(jobID, 10)+"/requeue-with-budget",
		url.Values{"budget": {"900"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := jobStatus(t, d, jobID); got != "pending" {
		t.Errorf("expected status 'pending', got %q", got)
	}
	var revCount int
	var budget int
	if err := d.QueryRowContext(context.Background(),
		`SELECT COUNT(*), MAX(execution_budget) FROM bead_revisions WHERE bead_id = ?`, beadID,
	).Scan(&revCount, &budget); err != nil {
		t.Fatalf("query revisions: %v", err)
	}
	if revCount != 2 {
		t.Errorf("expected a new revision inserted (2 total), got %d", revCount)
	}
	if budget != 900 {
		t.Errorf("expected new revision budget 900, got %d", budget)
	}
}

// TestHandleRequeuWithBudget_NonEscalatedJobConflictsRollsBackRevision
// verifies the whole transaction — including the new bead_revisions row —
// rolls back when the job is no longer escalated, not just the final status
// write. A partial apply (new revision inserted, but job left in its old
// status) would silently orphan a revision no job is driving.
func TestHandleRequeuWithBudget_NonEscalatedJobConflictsRollsBackRevision(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	beadID := seedBead(t, d, pid, 300)
	jobID := seedJob(t, d, pid, beadID, "EXECUTE_BEAD", "complete")

	rec := doPost(t, s, "/escalations/"+strconv.FormatInt(jobID, 10)+"/requeue-with-budget",
		url.Values{"budget": {"900"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := jobStatus(t, d, jobID); got != "complete" {
		t.Errorf("expected status to remain 'complete', got %q", got)
	}
	var revCount int
	if err := d.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM bead_revisions WHERE bead_id = ?`, beadID,
	).Scan(&revCount); err != nil {
		t.Fatalf("query revisions: %v", err)
	}
	if revCount != 1 {
		t.Errorf("expected the new revision to be rolled back (still 1 total), got %d — bug reproduced: partial apply", revCount)
	}
}

// --- handleGrantAttempts ---

func TestHandleGrantAttempts_EscalatedJobSucceeds(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	beadID := seedBead(t, d, pid, 300)
	jobID := seedJob(t, d, pid, beadID, "ADJUDICATE_NEXT_EXECUTION", "escalated")

	rec := doPost(t, s, "/escalations/"+strconv.FormatInt(jobID, 10)+"/grant-attempts",
		url.Values{"attempts": {"3"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := jobStatus(t, d, jobID); got != "pending" {
		t.Errorf("expected status 'pending', got %q", got)
	}
	var override sql.NullInt64
	if err := d.QueryRowContext(context.Background(),
		`SELECT execution_attempts_override FROM beads WHERE id = ?`, beadID,
	).Scan(&override); err != nil {
		t.Fatalf("query override: %v", err)
	}
	// Project's max_execution_attempts defaults to 5 (schema default); granting
	// 3 more should seed the override from that default and add 3.
	if !override.Valid || override.Int64 != 8 {
		t.Errorf("expected execution_attempts_override = 8 (5 default + 3 granted), got %v", override)
	}
}

// TestHandleGrantAttempts_NonEscalatedJobConflictsRollsBackOverride verifies
// the attempts-override bump rolls back along with the status write when the
// job is no longer escalated.
func TestHandleGrantAttempts_NonEscalatedJobConflictsRollsBackOverride(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	beadID := seedBead(t, d, pid, 300)
	jobID := seedJob(t, d, pid, beadID, "ADJUDICATE_NEXT_EXECUTION", "failed_retry")

	rec := doPost(t, s, "/escalations/"+strconv.FormatInt(jobID, 10)+"/grant-attempts",
		url.Values{"attempts": {"3"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := jobStatus(t, d, jobID); got != "failed_retry" {
		t.Errorf("expected status to remain 'failed_retry', got %q", got)
	}
	var override sql.NullInt64
	if err := d.QueryRowContext(context.Background(),
		`SELECT execution_attempts_override FROM beads WHERE id = ?`, beadID,
	).Scan(&override); err != nil {
		t.Fatalf("query override: %v", err)
	}
	if override.Valid {
		t.Errorf("expected execution_attempts_override to remain unset (rolled back), got %v — bug reproduced: partial apply", override)
	}
}

// --- handleTrace ---

// TestHandleTrace_ServesKnownExecution confirms the execution-ID-based route
// still serves real trace content for a legitimate execution.
func TestHandleTrace_ServesKnownExecution(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	beadID := seedBead(t, d, pid, 300)

	dir := t.TempDir()
	tracePath := filepath.Join(dir, "bead-1-attempt-1.log")
	if err := os.WriteFile(tracePath, []byte("hello from trace"), 0o644); err != nil {
		t.Fatalf("write trace file: %v", err)
	}

	var revID int64
	if err := d.QueryRowContext(context.Background(),
		`SELECT current_revision_id FROM beads WHERE id = ?`, beadID).Scan(&revID); err != nil {
		t.Fatalf("query revision id: %v", err)
	}
	res, err := d.ExecContext(context.Background(), `
		INSERT INTO executions (project_id, bead_id, bead_revision_id, trace_path, started_at)
		VALUES (?, ?, ?, ?, '2026-01-01T00:00:00Z')`, pid, beadID, revID, tracePath)
	if err != nil {
		t.Fatalf("seed execution: %v", err)
	}
	execID, _ := res.LastInsertId()

	req := httptest.NewRequest(http.MethodGet, "/trace/"+strconv.FormatInt(execID, 10), nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "hello from trace") {
		t.Errorf("expected response to contain trace file content, got: %s", w.Body.String())
	}
}

// TestHandleTrace_UnknownExecutionNotFound confirms an execution ID with no
// matching row 404s rather than attempting any filesystem read.
func TestHandleTrace_UnknownExecutionNotFound(t *testing.T) {
	s, _ := openTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/trace/999999", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleTrace_NoArbitraryPathParameter reproduces the Stage 8 audit
// finding: the old route (GET /trace?path=<anything>) let a client read any
// file on disk the process could access. The route now only accepts a
// numeric execution ID segment, so a bare "/trace" request (the old
// query-only form, with no id path segment) must not match any handler at
// all — confirming the vulnerable route is gone, not just discouraged.
func TestHandleTrace_NoArbitraryPathParameter(t *testing.T) {
	s, _ := openTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/trace?path=/etc/passwd", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected the old query-param route to 404 (no matching handler), got %d: %s", w.Code, w.Body.String())
	}
}
