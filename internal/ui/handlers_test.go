package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

// --- static assets ---

// TestStaticHTMXServedLocally: htmx is vendored and served from /static/, not a
// CDN, so the dashboard works with no internet route (the daemon runs on a LAN
// next to Ollama). Also asserts the layout references the local path.
func TestStaticHTMXServedLocally(t *testing.T) {
	s, _ := openTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/static/htmx-2.0.4.min.js", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for vendored htmx, got %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Body.String(), "var htmx=function()") {
		t.Errorf("body does not look like htmx: %.40q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable", cc)
	}

	dash := httptest.NewRequest(http.MethodGet, "/", nil)
	drec := httptest.NewRecorder()
	s.ServeHTTP(drec, dash)
	body := drec.Body.String()
	if strings.Contains(body, "unpkg.com") {
		t.Errorf("layout still references a CDN")
	}
	if !strings.Contains(body, `/static/htmx-2.0.4.min.js`) {
		t.Errorf("layout does not reference the vendored htmx path")
	}
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

// --- handleBeadReport / handleProjectReport ---

// seedProjectWithFolder inserts a minimal project row rooted at folder,
// returning its ID (always 1) — like seedProject, but with a caller-chosen
// folder_path so tests can write real traces/ files under a t.TempDir().
func seedProjectWithFolder(t *testing.T, d *db.DB, folder string) int64 {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (1, 'p', ?, 'design.md', 'active', 'honor', 300, 2,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, folder); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return 1
}

// TestHandleBeadReport_ServesReportFile confirms a bead's mechanically
// written traces/bead-{id}-report.md is found via its project's folder_path
// and returned verbatim.
func TestHandleBeadReport_ServesReportFile(t *testing.T) {
	s, d := openTestServer(t)
	dir := t.TempDir()
	pid := seedProjectWithFolder(t, d, dir)
	beadID := seedBead(t, d, pid, 300)

	tracesDir := filepath.Join(dir, "traces")
	if err := os.MkdirAll(tracesDir, 0o755); err != nil {
		t.Fatalf("mkdir traces: %v", err)
	}
	reportPath := filepath.Join(tracesDir, fmt.Sprintf("bead-%d-report.md", beadID))
	if err := os.WriteFile(reportPath, []byte("# Bead Report\nescalated"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/beads/%d/report", beadID), nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "escalated") {
		t.Errorf("expected response to contain report content, got: %s", w.Body.String())
	}
}

// TestHandleBeadReport_MissingReportShowsPlaceholder confirms a bead that
// hasn't reached a terminal state yet (no report file written) renders a
// placeholder instead of erroring.
func TestHandleBeadReport_MissingReportShowsPlaceholder(t *testing.T) {
	s, d := openTestServer(t)
	dir := t.TempDir()
	pid := seedProjectWithFolder(t, d, dir)
	beadID := seedBead(t, d, pid, 300)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/beads/%d/report", beadID), nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not yet generated") {
		t.Errorf("expected placeholder message, got: %s", w.Body.String())
	}
}

// TestHandleBeadReport_UnknownBeadNotFound confirms a nonexistent bead ID
// 404s rather than attempting any filesystem read.
func TestHandleBeadReport_UnknownBeadNotFound(t *testing.T) {
	s, _ := openTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/beads/999999/report", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleProjectReport_ServesReportFile confirms a project's
// traces/project-report.md is found via folder_path and returned verbatim.
func TestHandleProjectReport_ServesReportFile(t *testing.T) {
	s, d := openTestServer(t)
	dir := t.TempDir()
	pid := seedProjectWithFolder(t, d, dir)

	tracesDir := filepath.Join(dir, "traces")
	if err := os.MkdirAll(tracesDir, 0o755); err != nil {
		t.Fatalf("mkdir traces: %v", err)
	}
	reportPath := filepath.Join(tracesDir, "project-report.md")
	if err := os.WriteFile(reportPath, []byte("# Project Report\ncomplete"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/projects/%d/report", pid), nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "complete") {
		t.Errorf("expected response to contain report content, got: %s", w.Body.String())
	}
}

// TestHandleProjectReport_UnknownProjectNotFound confirms a nonexistent
// project ID 404s rather than attempting any filesystem read.
func TestHandleProjectReport_UnknownProjectNotFound(t *testing.T) {
	s, _ := openTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/projects/999999/report", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// --- handleBeadSnapshot ---

// TestHandleBeadSnapshot_ServesSnapshotContent confirms a rewind snapshot's
// README and preserved file content are found via the bead's project
// folder_path and rendered together.
func TestHandleBeadSnapshot_ServesSnapshotContent(t *testing.T) {
	s, d := openTestServer(t)
	dir := t.TempDir()
	pid := seedProjectWithFolder(t, d, dir)
	beadID := seedBead(t, d, pid, 300)

	snapshotDir := filepath.Join(dir, "traces", fmt.Sprintf("_bead-%d-rewind-1", beadID))
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		t.Fatalf("mkdir snapshot dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "README.md"), []byte("# manifest\nfiles preserved"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "game.go"), []byte("package main // broken"), 0o644); err != nil {
		t.Fatalf("write game.go: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/beads/%d/snapshot/1", beadID), nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "files preserved") {
		t.Errorf("expected response to contain README content, got: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "package main // broken") {
		t.Errorf("expected response to contain preserved file content, got: %s", w.Body.String())
	}
}

// TestHandleBeadSnapshot_UnknownBeadNotFound confirms a nonexistent bead ID
// 404s rather than attempting any filesystem read.
func TestHandleBeadSnapshot_UnknownBeadNotFound(t *testing.T) {
	s, _ := openTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/beads/999999/snapshot/1", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestListRewindSnapshots_SortsNumbersAndIgnoresUnrelatedEntries confirms the
// bead-detail page's snapshot list only picks up this bead's own
// _bead-{id}-rewind-{n} directories, sorted numerically, ignoring other
// beads' snapshots and non-directory entries.
func TestListRewindSnapshots_SortsNumbersAndIgnoresUnrelatedEntries(t *testing.T) {
	dir := t.TempDir()
	tracesDir := filepath.Join(dir, "traces")
	for _, name := range []string{"_bead-5-rewind-2", "_bead-5-rewind-1", "_bead-5-rewind-10", "_bead-6-rewind-1"} {
		if err := os.MkdirAll(filepath.Join(tracesDir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(tracesDir, "bead-5-report.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}

	got := listRewindSnapshots(dir, 5)
	want := []int{1, 2, 10}
	if len(got) != len(want) {
		t.Fatalf("listRewindSnapshots = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("listRewindSnapshots = %v, want %v", got, want)
			break
		}
	}
}

// --- handleRemoveProject ---

// TestHandleRemoveProject_WithTestRefinementsSucceeds reproduces a gap found
// while designing save-fixture: the delete steps list was missing
// test_refinements, so removing a project with any REFINE_TESTS history hit
// a live FK violation (test_refinements.bead_id references beads, and beads
// was deleted first) instead of succeeding.
func TestHandleRemoveProject_WithTestRefinementsSucceeds(t *testing.T) {
	s, d := openTestServer(t)
	ctx := context.Background()
	projectID := seedProject(t, d)
	beadID := seedBead(t, d, projectID, 300)

	if _, err := d.ExecContext(ctx, `
		INSERT INTO test_refinements (project_id, bead_id, cycle_id, turn, verb, changed, created_at)
		VALUES (?, ?, 1, 1, 'REFINE_TESTS_WRITE', 1, '2026-01-01T00:00:00Z')`,
		projectID, beadID); err != nil {
		t.Fatalf("seed test_refinements: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET status = 'full_stopped' WHERE id = ?`, projectID); err != nil {
		t.Fatalf("set project full_stopped: %v", err)
	}

	rec := doPost(t, s, "/projects/"+strconv.FormatInt(projectID, 10)+"/remove", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d: %s", rec.Code, rec.Body.String())
	}

	var count int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = ?`, projectID).Scan(&count); err != nil {
		t.Fatalf("query projects: %v", err)
	}
	if count != 0 {
		t.Errorf("project row still present after remove")
	}
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM test_refinements WHERE project_id = ?`, projectID).Scan(&count); err != nil {
		t.Fatalf("query test_refinements: %v", err)
	}
	if count != 0 {
		t.Errorf("test_refinements rows still present after remove")
	}
}

// seedExtraProject inserts a second project row with an explicit id, so tests
// can build a lineage / cascade relationship between two projects.
func seedExtraProject(t *testing.T, d *db.DB, id int64, status string) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (?, ?, '/tmp', 'design.md', ?, 'honor', 300, 2,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, fmt.Sprintf("p%d", id), status); err != nil {
		t.Fatalf("seed extra project %d: %v", id, err)
	}
}

func projectExists(t *testing.T, d *db.DB, id int64) bool {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM projects WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("query project %d: %v", id, err)
	}
	return n > 0
}

// TestHandleRemoveProject_LineageRootWithIterationsBlocked: removing a project
// that is still the lineage_root_id of a later iteration must be refused with a
// 400, not attempted-and-rolled-back. lineage_root_id REFERENCES projects(id)
// and foreign_keys is ON, so the DELETE would fail the FK.
func TestHandleRemoveProject_LineageRootWithIterationsBlocked(t *testing.T) {
	s, d := openTestServer(t)
	ctx := context.Background()
	root := seedProject(t, d) // id 1
	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET status = 'full_stopped', lineage_root_id = id WHERE id = ?`, root); err != nil {
		t.Fatalf("set root: %v", err)
	}
	seedExtraProject(t, d, 2, "active")
	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET lineage_root_id = ?, iteration_number = 2 WHERE id = 2`, root); err != nil {
		t.Fatalf("point iteration 2 at root: %v", err)
	}

	rec := doPost(t, s, "/projects/1/remove", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "later iteration") {
		t.Errorf("expected a lineage message, got %q", rec.Body.String())
	}
	if !projectExists(t, d, 1) {
		t.Errorf("project 1 was removed despite the guard")
	}
}

// TestHandleRemoveProject_CascadeBaselineBlocked: same, for a project still
// referenced as another project's cascade_baseline_project_id.
func TestHandleRemoveProject_CascadeBaselineBlocked(t *testing.T) {
	s, d := openTestServer(t)
	ctx := context.Background()
	baseline := seedProject(t, d) // id 1
	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET status = 'complete' WHERE id = ?`, baseline); err != nil {
		t.Fatalf("set baseline complete: %v", err)
	}
	seedExtraProject(t, d, 2, "active")
	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET cascade_baseline_project_id = ? WHERE id = 2`, baseline); err != nil {
		t.Fatalf("point cascade child at baseline: %v", err)
	}

	rec := doPost(t, s, "/projects/1/remove", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cascade baseline") {
		t.Errorf("expected a cascade message, got %q", rec.Body.String())
	}
	if !projectExists(t, d, 1) {
		t.Errorf("project 1 was removed despite the guard")
	}
}

// TestHandleRemoveProject_StandaloneLineageRootSucceeds: a project that is its
// own lineage root (the common case — Create backfills lineage_root_id = id)
// with no later iterations removes cleanly; the self-reference goes away with
// the row.
func TestHandleRemoveProject_StandaloneLineageRootSucceeds(t *testing.T) {
	s, d := openTestServer(t)
	ctx := context.Background()
	pid := seedProject(t, d)
	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET status = 'full_stopped', lineage_root_id = id WHERE id = ?`, pid); err != nil {
		t.Fatalf("set project: %v", err)
	}

	rec := doPost(t, s, "/projects/"+strconv.FormatInt(pid, 10)+"/remove", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	if projectExists(t, d, pid) {
		t.Errorf("project still present after remove")
	}
}

// --- Milestone E: forensics depth ---

func TestRenderMarkdown_BasicAndSafe(t *testing.T) {
	out := string(renderMarkdown("# Title\n\n**bold** and `code`\n\n<script>alert(1)</script>"))
	if !strings.Contains(out, "<h1") || !strings.Contains(out, "<strong>bold</strong>") {
		t.Errorf("markdown not rendered: %s", out)
	}
	if strings.Contains(out, "<script>") {
		t.Errorf("raw HTML passed through — goldmark unsafe mode is on: %s", out)
	}
}

func TestTraceView_StructuredCommands(t *testing.T) {
	s, d := openTestServer(t)
	ctx := context.Background()
	pid := seedProject(t, d)
	folder := projectFolderTempDir(t, d, pid)
	beadID := seedBead(t, d, pid, 300)
	tracePath := filepath.Join(folder, "t.log")
	if err := os.WriteFile(tracePath, []byte(
		"[TURN 1]\n[tool: run_command map[command:go test ./...]]\n[result]\nstdout:\n--- FAIL\nstderr:\nFAIL\nexit: exit status 1\n[terminated: timeout]\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	var execID int64
	if err := d.QueryRowContext(ctx, `
		INSERT INTO executions (project_id, bead_id, bead_revision_id, trace_path, termination_cause, monitor_honored, started_at, infra_failure, test_first_attempt)
		VALUES (?, ?, (SELECT current_revision_id FROM beads WHERE id = ?), ?, 'timeout', 1, '2026-01-01T00:00:00Z', 0, 0)
		RETURNING id`, pid, beadID, beadID, tracePath).Scan(&execID); err != nil {
		t.Fatalf("insert execution: %v", err)
	}

	body := getBody(t, s, fmt.Sprintf("/trace/%d", execID))
	for _, want := range []string{"Commands", "<code>go test ./...</code>", "exit status 1", "raw trace"} {
		if !strings.Contains(body, want) {
			t.Errorf("trace view missing %q", want)
		}
	}
}

func TestBeadDetail_CompressedHistoryAndAdjudicationPanel(t *testing.T) {
	s, d := openTestServer(t)
	ctx := context.Background()
	pid := seedProject(t, d)
	beadID := seedBead(t, d, pid, 300)
	if _, err := d.ExecContext(ctx,
		`INSERT INTO compressed_history (bead_id, project_id, compressed_text, updated_at)
		 VALUES (?, ?, '## history\n\n- attempt 1 FAIL', '2026-01-02T00:00:00Z')`, beadID, pid); err != nil {
		t.Fatalf("seed compressed_history: %v", err)
	}
	var execID int64
	if err := d.QueryRowContext(ctx, `
		INSERT INTO executions (project_id, bead_id, bead_revision_id, trace_path, termination_cause, monitor_honored, started_at, infra_failure, test_first_attempt)
		VALUES (?, ?, (SELECT current_revision_id FROM beads WHERE id = ?), '/tmp/t', 'timeout', 1, '2026-01-01T00:00:00Z', 0, 0)
		RETURNING id`, pid, beadID, beadID).Scan(&execID); err != nil {
		t.Fatalf("insert execution: %v", err)
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO adjudications (project_id, bead_id, execution_id, trend, bead_spec_fit, reasoning_text, attempt_budget_cost, monitor_escalation_status, decision, created_at)
		VALUES (?, ?, ?, 'narrower', 'execution_capability_problem', 'grant another attempt', 1.5, 0, 'execute_revised', '2026-01-02T00:00:00Z')`,
		pid, beadID, execID); err != nil {
		t.Fatalf("seed adjudication: %v", err)
	}

	body := getBody(t, s, "/beads/"+strconv.FormatInt(beadID, 10))
	for _, want := range []string{"Compressed Analysis History", "attempt 1 FAIL",
		"adjudication detail", "narrower", "execution_capability_problem", "grant another attempt", "1.50"} {
		if !strings.Contains(body, want) {
			t.Errorf("bead detail missing %q", want)
		}
	}
}

func TestEscalationDetail_ChainAndCompressedHistory(t *testing.T) {
	s, d := openTestServer(t)
	ctx := context.Background()
	pid := seedProject(t, d)
	beadID := seedBead(t, d, pid, 300)
	jobID := seedJob(t, d, pid, beadID, "ADJUDICATE_NEXT_EXECUTION", "escalated")
	for i := 1; i <= 2; i++ {
		if _, err := d.ExecContext(ctx, `
			INSERT INTO handoff_attempts (job_id, attempt_number, raw_output, validation_result, created_at, ended_at)
			VALUES (?, ?, '{bad', 'malformed: JSON parse error', '2026-01-02T00:00:00Z', '2026-01-02T00:00:01Z')`,
			jobID, i); err != nil {
			t.Fatalf("seed attempt: %v", err)
		}
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO compressed_history (bead_id, project_id, compressed_text, updated_at)
		 VALUES (?, ?, 'the narrative', '2026-01-02T00:00:00Z')`, beadID, pid); err != nil {
		t.Fatalf("seed compressed_history: %v", err)
	}

	body := getBody(t, s, "/escalations/"+strconv.FormatInt(jobID, 10))
	for _, want := range []string{"Attempts", "malformed: JSON parse error",
		"bead " + strconv.FormatInt(beadID, 10) + " detail", "Compressed Analysis History", "the narrative"} {
		if !strings.Contains(body, want) {
			t.Errorf("escalation detail missing %q", want)
		}
	}
}

func TestProjectDetail_VerbModelsAndPosition(t *testing.T) {
	s, d := openTestServer(t)
	ctx := context.Background()
	pid := seedProject(t, d)
	if _, err := d.ExecContext(ctx,
		`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (?, 'EXECUTE_BEAD', 'qwen2.5-coder:32b')`, pid); err != nil {
		t.Fatalf("seed verb model: %v", err)
	}
	b1 := seedBead(t, d, pid, 300)
	_ = seedBead(t, d, pid, 300)
	if _, err := d.ExecContext(ctx, `UPDATE beads SET status = 'executing' WHERE id = ?`, b1); err != nil {
		t.Fatalf("set bead executing: %v", err)
	}

	body := getBody(t, s, "/projects/"+strconv.FormatInt(pid, 10))
	for _, want := range []string{"Model Assignments", "qwen2.5-coder:32b", "▸ bead 1 of 2 · executing"} {
		if !strings.Contains(body, want) {
			t.Errorf("project detail missing %q", want)
		}
	}
}

func TestDashboard_PollingGatedOnActive(t *testing.T) {
	s, d := openTestServer(t)
	ctx := context.Background()
	pid := seedProject(t, d) // active by default

	if !strings.Contains(getBody(t, s, "/"), "hx-trigger") {
		t.Errorf("active project dashboard should poll")
	}
	if _, err := d.ExecContext(ctx, `UPDATE projects SET status = 'complete' WHERE id = ?`, pid); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if strings.Contains(getBody(t, s, "/"), "hx-trigger") {
		t.Errorf("completed project dashboard should not poll")
	}
}

// --- Milestone C: project detail page, lineages, cascade, active-project selection ---

// seedIteration inserts a project that is iteration `iter` of the lineage
// rooted at `root` (pass root==id for iteration 1).
func seedIteration(t *testing.T, d *db.DB, id, root int64, iter int, status string) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default, audit_reconcile_round_cap,
		   lineage_root_id, iteration_number, created_at, updated_at)
		VALUES (?, 'lin', '/tmp', 'd.md', ?, 'honor', 300, 2, ?, ?,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, status, root, iter); err != nil {
		t.Fatalf("seed iteration %d: %v", id, err)
	}
}

func TestQueryActiveProject_PrefersActiveOverLowerIDPaused(t *testing.T) {
	_, d := openTestServer(t)
	ctx := context.Background()
	pid := seedProject(t, d) // id 1
	if _, err := d.ExecContext(ctx, `UPDATE projects SET status = 'paused' WHERE id = ?`, pid); err != nil {
		t.Fatalf("pause project 1: %v", err)
	}
	seedExtraProject(t, d, 2, "active")

	p, err := queryActiveProject(ctx, d)
	if err != nil {
		t.Fatalf("queryActiveProject: %v", err)
	}
	if p == nil || p.ID != 2 {
		t.Fatalf("featured project = %v, want id 2 (the active one, not the lower-id paused one)", p)
	}

	others, err := queryOtherNonTerminalProjects(ctx, d, 2)
	if err != nil {
		t.Fatalf("queryOtherNonTerminalProjects: %v", err)
	}
	if len(others) != 1 || others[0].ID != 1 {
		t.Errorf("other non-terminal = %v, want [project 1]", others)
	}
}

func TestQueryActiveProject_FallsBackToPausedWhenNoneActive(t *testing.T) {
	_, d := openTestServer(t)
	ctx := context.Background()
	pid := seedProject(t, d)
	if _, err := d.ExecContext(ctx, `UPDATE projects SET status = 'paused' WHERE id = ?`, pid); err != nil {
		t.Fatalf("pause: %v", err)
	}
	p, err := queryActiveProject(ctx, d)
	if err != nil {
		t.Fatalf("queryActiveProject: %v", err)
	}
	if p == nil || p.ID != pid {
		t.Fatalf("featured = %v, want the paused project", p)
	}
}

func TestDashboard_GroupsLineage(t *testing.T) {
	s, d := openTestServer(t)
	seedIteration(t, d, 1, 1, 1, "complete")
	seedIteration(t, d, 2, 1, 2, "full_stopped")
	seedIteration(t, d, 3, 1, 3, "active")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "lineage · lin · 3 iterations") {
		t.Errorf("dashboard missing lineage group header:\n%s", body)
	}
	if !strings.Contains(body, "iter 2") || !strings.Contains(body, "iter 3") {
		t.Errorf("dashboard missing iteration markers")
	}
}

func TestProjectDetail_ShowsIterationNav(t *testing.T) {
	s, d := openTestServer(t)
	seedIteration(t, d, 1, 1, 1, "complete")
	seedIteration(t, d, 2, 1, 2, "full_stopped")
	seedIteration(t, d, 3, 1, 3, "active")

	req := httptest.NewRequest(http.MethodGet, "/projects/2", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(body, "Iteration Lineage") {
		t.Errorf("missing lineage section")
	}
	if !strings.Contains(body, `href="/projects/1"`) || !strings.Contains(body, `href="/projects/3"`) {
		t.Errorf("missing prev/next iteration links")
	}
	if !strings.Contains(body, "iteration 1") || !strings.Contains(body, "iteration 3 →") {
		t.Errorf("missing prev/next iteration labels")
	}
}

func TestProjectDetail_NotFound(t *testing.T) {
	s, _ := openTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/projects/999", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestProjectDetail_CascadeReview(t *testing.T) {
	s, d := openTestServer(t)
	ctx := context.Background()
	baseline := seedProject(t, d) // id 1
	if _, err := d.ExecContext(ctx, `UPDATE projects SET status = 'complete' WHERE id = 1`); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	folder := t.TempDir()
	seedExtraProject(t, d, 2, "active")
	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET cascade_baseline_project_id = ?, folder_path = ?, lineage_root_id = ?, iteration_number = 2 WHERE id = 2`,
		baseline, folder, baseline); err != nil {
		t.Fatalf("make cascade: %v", err)
	}
	reset := seedBead(t, d, 2, 300)     // will get a cascade snapshot dir
	unchanged := seedBead(t, d, 2, 300) // no snapshot

	if err := os.MkdirAll(filepath.Join(folder, "traces", fmt.Sprintf("_bead-%d-cascade-1", reset)), 0o755); err != nil {
		t.Fatalf("mk snapshot dir: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/projects/2", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, body)
	}
	if !strings.Contains(body, "Cascade iteration") || !strings.Contains(body, "Cascade Review") {
		t.Errorf("missing cascade banner/section")
	}
	if !strings.Contains(body, "reset — spec changed") {
		t.Errorf("reset bead %d not marked as changed", reset)
	}
	if !strings.Contains(body, "unchanged — inherited from baseline") {
		t.Errorf("unchanged bead %d not marked inherited", unchanged)
	}
}

// --- Milestone D: decomposition debate, manifest bootstrap, REFINE_TESTS cycle ---

func seedRound(t *testing.T, d *db.DB, projectID int64, n int, critique, reconciliation, outcome string) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO audit_reconcile_rounds (project_id, round_number, critique_text, reconciliation, outcome, created_at)
		VALUES (?, ?, ?, ?, ?, '2026-01-01T00:00:00Z')`,
		projectID, n, critique, reconciliation, outcome); err != nil {
		t.Fatalf("seed round: %v", err)
	}
}

func TestProjectDetail_DecompositionReview(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	seedRound(t, d, pid, 1, "bead 3 forward-references parser.go", "agreed, reordered", "converged")
	seedRound(t, d, pid, 2, "bead 5 precedence still ambiguous", "disagree: spec is clear", "disagreed_continuing")

	body := getBody(t, s, "/projects/"+strconv.FormatInt(pid, 10))
	for _, want := range []string{"Decomposition Review", "round 2 of cap 2", "disagreed_continuing",
		"bead 5 precedence still ambiguous", "agreed, reordered"} {
		if !strings.Contains(body, want) {
			t.Errorf("project detail missing %q", want)
		}
	}
}

func TestProjectDetail_ManifestBootstrap(t *testing.T) {
	s, d := openTestServer(t)
	ctx := context.Background()
	pid := seedProject(t, d)
	jobID := seedJob(t, d, pid, 0, "VERIFY_MANIFEST", "complete")
	if _, err := d.ExecContext(ctx, `
		INSERT INTO verify_attempts
		  (project_id, job_id, attempt_number, file_presence_pass, no_behavioral_tests_pass,
		   compile_pass, api_check_pass, stub_purity_pass, violations, created_at)
		VALUES (?, ?, 2, 1, 0, 1, 1, 1, 'foo.go: behavioral test in stub', '2026-01-02T00:00:00Z')`,
		pid, jobID); err != nil {
		t.Fatalf("seed verify_attempt: %v", err)
	}
	for i, fb := range []string{"Manifest omits the parser package.", "Still missing error-path files."} {
		if _, err := d.ExecContext(ctx, `
			INSERT INTO certifications
			  (project_id, verify_attempt_id, preliminary_decision, final_decision, feedback, created_at)
			VALUES (?, (SELECT id FROM verify_attempts WHERE project_id = ?), 'reject', 'reject', ?, ?)`,
			pid, pid, fb, fmt.Sprintf("2026-01-0%dT00:00:00Z", 3+i)); err != nil {
			t.Fatalf("seed certification: %v", err)
		}
	}

	body := getBody(t, s, "/projects/"+strconv.FormatInt(pid, 10))
	for _, want := range []string{"Manifest Bootstrap", "SURVEY_SPEC", "2 / 5",
		"Manifest omits the parser package.", "no behavioral tests"} {
		if !strings.Contains(body, want) {
			t.Errorf("project detail missing %q", want)
		}
	}
}

func TestEscalationDetail_EmbedsRoundsForReconcile(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	jobID := seedJob(t, d, pid, 0, "RECONCILE_DECOMPOSITION", "escalated")
	seedRound(t, d, pid, 1, "ordering violation persists", "cannot fix without redecompose", "escalated")

	body := getBody(t, s, "/escalations/"+strconv.FormatInt(jobID, 10))
	if !strings.Contains(body, "Decomposition Review") || !strings.Contains(body, "ordering violation persists") {
		t.Errorf("escalation detail missing embedded rounds:\n%s", body)
	}

	// A non-decomposition escalated job gets no rounds section.
	jobID2 := seedJob(t, d, pid, 0, "SURVEY_SPEC", "escalated")
	body2 := getBody(t, s, "/escalations/"+strconv.FormatInt(jobID2, 10))
	if strings.Contains(body2, "Decomposition Review") {
		t.Errorf("non-decomposition escalation should not show rounds")
	}
}

func TestBeadDetail_ShowsRefinementCycles(t *testing.T) {
	s, d := openTestServer(t)
	ctx := context.Background()
	pid := seedProject(t, d)
	beadID := seedBead(t, d, pid, 300)
	turns := []struct {
		cyc, turn      int
		verb, dec, sum string
	}{
		{1, 1, "REFINE_TESTS_WRITE", "", "wrote 4 tests"},
		{1, 2, "REFINE_TESTS_CRITIQUE", "", "2 tests assert impl details"},
		{1, 3, "REFINE_TESTS_JUDGE", "revise", "rewrite TestParsePrecedence"},
		{2, 1, "REFINE_TESTS_WRITE", "", "rewrote per instructions"},
		{2, 2, "REFINE_TESTS_JUDGE", "approved", "looks good"},
	}
	for _, tn := range turns {
		if _, err := d.ExecContext(ctx, `
			INSERT INTO test_refinements (project_id, bead_id, cycle_id, turn, verb, changed, summary, decision, created_at)
			VALUES (?, ?, ?, ?, ?, 0, ?, ?, '2026-01-06T00:00:00Z')`,
			pid, beadID, tn.cyc, tn.turn, tn.verb, tn.sum, tn.dec); err != nil {
			t.Fatalf("seed test_refinement: %v", err)
		}
	}

	body := getBody(t, s, "/beads/"+strconv.FormatInt(beadID, 10))
	for _, want := range []string{"Test Refinement", "Cycle 1", "Cycle 2",
		"JUDGE: revise", "JUDGE: approved", "rewrite TestParsePrecedence"} {
		if !strings.Contains(body, want) {
			t.Errorf("bead detail missing %q", want)
		}
	}
}

// getBody issues a GET and returns the response body, failing on non-200.
func getBody(t *testing.T, s *server, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// --- W3: guidance log + rewind ---

// seedBeadWithProse inserts a bead whose current revision's full_text JSON
// carries the given prose in its inner "full_text" field (where the Human
// Guidance Log lives), returning the bead id.
func seedBeadWithProse(t *testing.T, d *db.DB, projectID int64, prose string) int64 {
	t.Helper()
	ctx := context.Background()
	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status, current_revision_id) VALUES (?, 'executing', NULL)`, projectID)
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	beadID, _ := res.LastInsertId()
	full, _ := json.Marshal(map[string]any{
		"title": "t", "full_text": prose, "output_files": []string{}, "exit_criteria": []string{},
	})
	revRes, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (?, ?, 1, ?, 300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		projectID, beadID, string(full))
	if err != nil {
		t.Fatalf("seed bead_revisions: %v", err)
	}
	revID, _ := revRes.LastInsertId()
	if _, err := d.ExecContext(ctx, `UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
		t.Fatalf("set current_revision_id: %v", err)
	}
	return beadID
}

// projectFolderTempDir points the seeded project (id 1) at a writable temp dir
// so project.RewindBead's filesystem work (snapshot, stub) has somewhere real
// to run.
func projectFolderTempDir(t *testing.T, d *db.DB, projectID int64) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := d.ExecContext(context.Background(),
		`UPDATE projects SET folder_path = ? WHERE id = ?`, dir, projectID); err != nil {
		t.Fatalf("set folder_path: %v", err)
	}
	return dir
}

func TestBeadDetail_RendersGuidanceLog(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	prose := "Base prose.\n\n## Human Guidance Log\n### Note 1 — 2026-01-02T00:00:00Z\nStatus: active\n\nUse Square{Row,Col} field order.\n\n### Note 2 — 2026-01-03T00:00:00Z\nStatus: retracted\n\nOld advice."
	beadID := seedBeadWithProse(t, d, pid, prose)

	req := httptest.NewRequest(http.MethodGet, "/beads/"+strconv.FormatInt(beadID, 10), nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, body)
	}
	for _, want := range []string{"Human Guidance Log", "Note 1", "Use Square{Row,Col} field order.", "retracted"} {
		if !strings.Contains(body, want) {
			t.Errorf("bead detail missing %q", want)
		}
	}
}

func TestHandleRewindBead_EscalatedNoConfirmNeeded(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	projectFolderTempDir(t, d, pid)
	beadID := seedBeadWithProse(t, d, pid, "Base prose.")
	seedJob(t, d, pid, beadID, "ADJUDICATE_NEXT_EXECUTION", "escalated")

	form := url.Values{"note": {"try the recursive form"}}
	rec := doPost(t, s, "/beads/"+strconv.FormatInt(beadID, 10)+"/rewind", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	var verb, prose string
	if err := d.QueryRowContext(context.Background(), `
		SELECT br.created_by_verb, json_extract(br.full_text, '$.full_text')
		FROM beads b JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID).Scan(&verb, &prose); err != nil {
		t.Fatalf("query new revision: %v", err)
	}
	if verb != "REWIND_BEAD" {
		t.Errorf("current revision created_by_verb = %q, want REWIND_BEAD", verb)
	}
	if !strings.Contains(prose, "try the recursive form") {
		t.Errorf("guidance note not in new prose: %q", prose)
	}
	var escalated int
	_ = d.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM handoff_jobs WHERE bead_id = ? AND status = 'escalated'`, beadID).Scan(&escalated)
	if escalated != 0 {
		t.Errorf("escalated job still open after rewind")
	}
}

func TestHandleRewindBead_NonEscalatedRequiresConfirm(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	projectFolderTempDir(t, d, pid)
	beadID := seedBeadWithProse(t, d, pid, "Base prose.")

	rec := doPost(t, s, "/beads/"+strconv.FormatInt(beadID, 10)+"/rewind", url.Values{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without confirm, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doPost(t, s, "/beads/"+strconv.FormatInt(beadID, 10)+"/rewind", url.Values{"confirm": {"rewind"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 with confirm=rewind, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRewindBead_SucceededRefused(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	projectFolderTempDir(t, d, pid)
	beadID := seedBeadWithProse(t, d, pid, "Base prose.")
	if _, err := d.ExecContext(context.Background(),
		`UPDATE beads SET status = 'succeeded' WHERE id = ?`, beadID); err != nil {
		t.Fatalf("set succeeded: %v", err)
	}

	rec := doPost(t, s, "/beads/"+strconv.FormatInt(beadID, 10)+"/rewind", url.Values{"confirm": {"rewind"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a succeeded bead, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRewindFromEscalation_ResolvesBeadID(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	projectFolderTempDir(t, d, pid)
	beadID := seedBeadWithProse(t, d, pid, "Base prose.")
	jobID := seedJob(t, d, pid, beadID, "ADJUDICATE_NEXT_EXECUTION", "escalated")

	rec := doPost(t, s, "/escalations/"+strconv.FormatInt(jobID, 10)+"/rewind", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/escalations" {
		t.Errorf("redirect = %q, want /escalations", loc)
	}
	var beadStatus string
	_ = d.QueryRowContext(context.Background(), `SELECT status FROM beads WHERE id = ?`, beadID).Scan(&beadStatus)
	if beadStatus != "pending" {
		t.Errorf("bead status = %q after rewind, want pending", beadStatus)
	}
}

func TestHandleRewindFromEscalation_ProjectScopedJobRejected(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	jobID := seedJob(t, d, pid, 0, "RECONCILE_DECOMPOSITION", "escalated")

	rec := doPost(t, s, "/escalations/"+strconv.FormatInt(jobID, 10)+"/rewind", url.Values{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a project-scoped job, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- W10: pause reason ---

func TestActiveProject_PausedShowsReasonAndNextJob(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	if _, err := d.ExecContext(context.Background(),
		`UPDATE projects SET status = 'paused', pause_after_reconcile = 1 WHERE id = ?`, pid); err != nil {
		t.Fatalf("set paused: %v", err)
	}
	seedJob(t, d, pid, 0, "DECOMPOSE_SPEC", "pending")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{"Paused", "pause_after_reconcile", "Resuming dispatches", "DECOMPOSE_SPEC", "cautious"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

// --- handleResumeProject ---

// TestHandleResumeProject_PreDecompositionPauseSucceeds reproduces the
// fractal-smoke-2 (project 105) incident: the web UI's resume button hit a
// handler that assumed a bead already existed and tried to hand-pick one to
// enqueue EXECUTE_BEAD for, which fails with "sql: no rows in result set" for
// any pause before DECOMPOSE_SPEC has run (pause_after_verb=CERTIFY_MANIFEST,
// zero beads yet). The handler now delegates to project.ResumeProject, the
// same status-flip-only logic `ratchet resume-project` already used
// correctly.
func TestHandleResumeProject_PreDecompositionPauseSucceeds(t *testing.T) {
	s, d := openTestServer(t)
	pid := seedProject(t, d)
	if _, err := d.ExecContext(context.Background(),
		`UPDATE projects SET status = 'paused' WHERE id = ?`, pid); err != nil {
		t.Fatalf("set project paused: %v", err)
	}
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, 'DECOMPOSE_SPEC', NULL, 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		pid); err != nil {
		t.Fatalf("seed pending DECOMPOSE_SPEC job: %v", err)
	}

	rec := doPost(t, s, "/projects/"+strconv.FormatInt(pid, 10)+"/resume", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d: %s", rec.Code, rec.Body.String())
	}

	var status string
	if err := d.QueryRowContext(context.Background(),
		`SELECT status FROM projects WHERE id = ?`, pid,
	).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q, want active", status)
	}

	var jobCount int
	if err := d.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM handoff_jobs WHERE project_id = ?`, pid,
	).Scan(&jobCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobCount != 1 {
		t.Errorf("handoff_jobs count = %d, want 1 (resume must not enqueue a second job)", jobCount)
	}
}
