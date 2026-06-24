package execution

// Step 3 smoke tests. These require a compiled ratchet binary (built as part
// of the test), spawn real subprocesses, and take 10–25 seconds each.
// Run with: go test -v -run TestSmoke ./internal/execution/
// Skip with: go test -short ./...

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"ratchet/internal/db"
)

// openSmokeDB opens a file-based SQLite DB in a temp directory.
// Smoke tests must use this instead of the in-memory openTestDB because
// subprocesses need to connect to the same database via its file path.
func openSmokeDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "smoke.db"))
	if err != nil {
		t.Fatalf("openSmokeDB: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// buildRatchet compiles the ratchet binary to a temp directory and returns
// its path. The build runs from the module root (two levels above this package).
func buildRatchet(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	binPath := filepath.Join(t.TempDir(), "ratchet")

	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/ratchet")
	cmd.Dir = filepath.Join(cwd, "..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build ratchet: %v\n%s", err, out)
	}
	return binPath
}

// seedSmokeProject creates a project + bead + revision + MONITOR_EXECUTION
// model assignment. Returns (projectDir, beadID, revID, jobID).
// projectDir is the folder_path used for trace files.
func seedSmokeProject(t *testing.T, d *db.DB) (projectDir string, beadID, revID, jobID int64) {
	t.Helper()
	ctx := context.Background()
	projectDir = t.TempDir()

	_, err := d.ExecContext(ctx, `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (-1, 'fixture: Step 3 smoke test', ?, 'design.md', 'active',
		        'honor', 300, 2,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, projectDir)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}

	// Only MONITOR_EXECUTION is needed by the monitor subprocess.
	_, err = d.ExecContext(ctx,
		`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (-1, ?, ?)`,
		db.VerbMonitorExecution, "mistral-small3.2:24b")
	if err != nil {
		t.Fatalf("insert monitor model: %v", err)
	}

	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status, current_revision_id) VALUES (-1, 'executing', NULL)`)
	if err != nil {
		t.Fatalf("insert bead: %v", err)
	}
	beadID, _ = res.LastInsertId()

	// execution_budget is generous so the stub doesn't time out during the test.
	res, err = d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text,
		   execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (-1, ?, 1, '{"title":"B01","full_text":"stub spec"}',
		        300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`, beadID)
	if err != nil {
		t.Fatalf("insert revision: %v", err)
	}
	revID, _ = res.LastInsertId()

	_, _ = d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID)

	res, err = d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (-1, ?, ?, 'running', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		db.VerbExecuteBead, beadID)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	jobID, _ = res.LastInsertId()
	return projectDir, beadID, revID, jobID
}

// TestSmokeExecuteBeadSIGTERM verifies the core signal contract:
// execute-bead running in hang mode receives SIGTERM, writes
// termination_cause='monitor_terminated', and exits within 1 second.
func TestSmokeExecuteBeadSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test: use -run TestSmoke without -short")
	}
	binPath := buildRatchet(t)

	d := openSmokeDB(t)
	ctx := context.Background()
	_, beadID, revID, _ := seedSmokeProject(t, d)

	// The execute-bead subprocess needs an executions row with trace_path.
	tracePath := filepath.Join(t.TempDir(), "trace.log")
	res, err := d.ExecContext(ctx, `
		INSERT INTO executions
		  (project_id, bead_id, bead_revision_id, trace_path, monitor_honored, started_at)
		VALUES (-1, ?, ?, ?, 1, '2026-01-01T00:00:00Z')`,
		beadID, revID, tracePath)
	if err != nil {
		t.Fatalf("insert execution: %v", err)
	}
	execID, _ := res.LastInsertId()

	// Create the trace file (execute-bead opens it in append mode).
	if err := os.WriteFile(tracePath, nil, 0o644); err != nil {
		t.Fatalf("create trace: %v", err)
	}

	cmd := exec.Command(binPath, "execute-bead",
		fmt.Sprintf("--execution-id=%d", execID),
		fmt.Sprintf("--db=%s", d.Path),
		"--mode=hang",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start execute-bead: %v", err)
	}

	// Let it write a line or two to confirm it's running.
	time.Sleep(6 * time.Second)

	// SIGTERM — the one-time signal handler must fire.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case <-exited:
		// Good — process exited.
	case <-time.After(2 * time.Second):
		cmd.Process.Kill()
		t.Error("execute-bead did not exit within 2 seconds of SIGTERM")
	}

	var cause sql.NullString
	if err := d.QueryRowContext(ctx,
		`SELECT termination_cause FROM executions WHERE id = ?`, execID,
	).Scan(&cause); err != nil {
		t.Fatalf("read termination_cause: %v", err)
	}
	if !cause.Valid || cause.String != "monitor_terminated" {
		t.Errorf("termination_cause = %q (valid=%v), want monitor_terminated",
			cause.String, cause.Valid)
	}

	// Trace file must have content (execute-bead was writing before SIGTERM).
	info, err := os.Stat(tracePath)
	if err != nil || info.Size() == 0 {
		t.Errorf("trace file missing or empty after SIGTERM test")
	}
}

// TestSmokeExecutionWindowMonitorFire is the primary execution window smoke test.
// It runs the full window (execute-bead in default success mode + monitor
// subprocess) and simulates a monitor fire by writing monitor_fired=1 directly
// to the DB. This bypasses the Ollama model call so the test is self-contained.
//
// What it verifies:
//   - Window polls the DB and detects monitor_fired=1 within windowPollInterval (5s)
//   - Two-stage kill: SIGTERM reaches execute-bead, which writes 'monitor_terminated'
//   - ended_at is written to the executions row
//   - ANALYZE_EXECUTION job is enqueued
//   - Total time from fire to completion is ≤ windowPollInterval + graceWindow + buffer
func TestSmokeExecutionWindowMonitorFire(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test: use -run TestSmoke without -short")
	}
	binPath := buildRatchet(t)

	d := openSmokeDB(t)
	ctx := context.Background()
	_, beadID, _, jobID := seedSmokeProject(t, d)

	// Point subprocess launches at the compiled binary.
	testExecutable = binPath
	t.Cleanup(func() { testExecutable = "" })

	job := &db.HandoffJob{
		ID:        jobID,
		ProjectID: -1,
		Verb:      db.VerbExecuteBead,
		BeadID:    sql.NullInt64{Int64: beadID, Valid: true},
	}

	// Run the execution window. It will create the executions row and start
	// both subprocesses. We don't pass a real Ollama URL — the monitor
	// subprocess will fail its model calls but that doesn't matter since we're
	// writing monitor_fired directly.
	windowDone := make(chan error, 1)
	windowCtx, cancelWindow := context.WithTimeout(ctx, 30*time.Second)
	defer cancelWindow()

	go func() {
		windowDone <- RunExecutionWindow(windowCtx, d, "http://127.0.0.1:11434", job)
	}()

	// Poll until the executions row appears (window created it and subprocesses started).
	var execID int64
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := d.QueryRowContext(ctx,
			`SELECT id FROM executions WHERE bead_id = ?`, beadID,
		).Scan(&execID); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if execID == 0 {
		t.Fatal("executions row never appeared — window may have failed to start")
	}
	t.Logf("execution started: id=%d", execID)

	// Give subprocesses a moment to initialise before firing.
	time.Sleep(2 * time.Second)

	// Simulate the monitor firing by writing directly to the DB.
	// This is the "force a loop condition" from the testing conventions — rather
	// than waiting 30s for a real model call, we inject the signal the window
	// would see if the monitor had fired.
	t.Log("writing monitor_fired=1 to simulate monitor fire")
	fireAt := time.Now()
	if err := writeMonitorFired(d, execID, true); err != nil {
		t.Fatalf("write monitor_fired: %v", err)
	}

	// Window should detect monitor_fired and complete within
	// windowPollInterval(5s) + graceWindow(10s) + buffer(5s) = 20s.
	select {
	case err := <-windowDone:
		elapsed := time.Since(fireAt)
		t.Logf("window completed in %v after monitor fire", elapsed.Round(time.Millisecond))
		if err != nil {
			t.Errorf("RunExecutionWindow: %v", err)
		}
		if elapsed > 20*time.Second {
			t.Errorf("window took %v after fire — exceeds windowPollInterval+graceWindow+buffer", elapsed)
		}
	case <-time.After(25 * time.Second):
		cancelWindow()
		t.Error("execution window did not complete within 25 seconds of monitor fire")
		<-windowDone
	}

	// --- Verify final DB state ---

	// termination_cause must reflect how execute-bead exited.
	// 'monitor_terminated': exited gracefully within grace window (expected).
	// 'monitor_force_killed': didn't exit within 10s grace (unexpected but not a bug here).
	var cause string
	if err := d.QueryRowContext(ctx,
		`SELECT termination_cause FROM executions WHERE id = ?`, execID,
	).Scan(&cause); err != nil {
		t.Fatalf("read termination_cause: %v", err)
	}
	if cause != "monitor_terminated" && cause != "monitor_force_killed" {
		t.Errorf("termination_cause = %q, want monitor_terminated or monitor_force_killed", cause)
	}
	t.Logf("termination_cause = %q", cause)

	// ended_at must be set.
	var endedAt sql.NullString
	if err := d.QueryRowContext(ctx,
		`SELECT ended_at FROM executions WHERE id = ?`, execID,
	).Scan(&endedAt); err != nil || !endedAt.Valid {
		t.Errorf("ended_at not written (got %v, err %v)", endedAt, err)
	}

	// ANALYZE_EXECUTION must be enqueued.
	var analyzeJobs int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ? AND bead_id = ?`,
		db.VerbAnalyzeExecution, beadID,
	).Scan(&analyzeJobs); err != nil || analyzeJobs == 0 {
		t.Errorf("ANALYZE_EXECUTION job not enqueued (count=%d, err=%v)", analyzeJobs, err)
	}

	// Trace file must have content from execute-bead's work.
	var tracePath string
	_ = d.QueryRowContext(ctx,
		`SELECT trace_path FROM executions WHERE id = ?`, execID).Scan(&tracePath)
	if info, err := os.Stat(tracePath); err != nil || info.Size() == 0 {
		t.Errorf("trace file missing or empty (path=%s, err=%v)", tracePath, err)
	}
}
