// Package execution implements the EXECUTE_BEAD + MONITOR_EXECUTION execution
// window: the one part of the pipeline that runs two subprocesses concurrently
// rather than making a single in-process model call.
package execution

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"ratchet/internal/db"
)

const (
	monitorPollInterval = 30 * time.Second
	graceWindow         = 10 * time.Second
	// How often the window checks whether the monitor has written monitor_fired=1.
	// Independent of monitorPollInterval (the model-call cadence inside the monitor).
	windowPollInterval = 5 * time.Second
	// Brief wait after execute-bead exits to allow its final DB write to commit.
	dbWriteSettle = 150 * time.Millisecond
)

// RunExecutionWindow handles an EXECUTE_BEAD handoff job end-to-end:
//  1. Creates the executions row.
//  2. Starts ratchet execute-bead and ratchet monitor as subprocesses.
//  3. Waits for execute-bead to finish (normally or via monitor fire).
//  4. Implements the two-stage kill on monitor fire: SIGTERM → grace → SIGKILL.
//  5. Kills the monitor, writes ended_at, enqueues ANALYZE_EXECUTION.
func RunExecutionWindow(ctx context.Context, d *db.DB, ollamaURL string, job *db.HandoffJob) error {
	if !job.BeadID.Valid {
		return fmt.Errorf("execute-bead job %d missing bead_id", job.ID)
	}
	beadID := job.BeadID.Int64

	// Load the current bead revision: execution_budget and monitor_override.
	var revID int64
	var budget int
	var monitorOverride string
	if err := d.QueryRowContext(ctx, `
		SELECT br.id, br.execution_budget, br.monitor_override
		FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID,
	).Scan(&revID, &budget, &monitorOverride); err != nil {
		return fmt.Errorf("load bead revision: %w", err)
	}

	// Load project folder for the traces directory.
	var folderPath string
	if err := d.QueryRowContext(ctx,
		`SELECT folder_path FROM projects WHERE id = ?`, job.ProjectID,
	).Scan(&folderPath); err != nil {
		return fmt.Errorf("load project folder: %w", err)
	}

	// Attempt number = existing execution count for this bead + 1.
	var attemptN int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*)+1 FROM executions WHERE bead_id = ?`, beadID,
	).Scan(&attemptN); err != nil {
		return fmt.Errorf("count executions: %w", err)
	}

	// Create the traces directory and an empty trace file.
	tracesDir := filepath.Join(folderPath, "traces")
	if err := os.MkdirAll(tracesDir, 0o755); err != nil {
		return fmt.Errorf("create traces dir: %w", err)
	}
	tracePath := filepath.Join(tracesDir, fmt.Sprintf("bead-%d-attempt-%d.log", beadID, attemptN))
	f, err := os.Create(tracePath)
	if err != nil {
		return fmt.Errorf("create trace file: %w", err)
	}
	f.Close()

	monitorHonored := 0
	if monitorOverride == "honor" {
		monitorHonored = 1
	}

	// Insert the executions row. termination_cause, monitor_fired, ended_at are
	// written later by their respective owners.
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := d.ExecContext(ctx, `
		INSERT INTO executions
		  (project_id, bead_id, bead_revision_id, trace_path, monitor_honored, started_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		job.ProjectID, beadID, revID, tracePath, monitorHonored, now)
	if err != nil {
		return fmt.Errorf("insert execution: %w", err)
	}
	execID, _ := res.LastInsertId()

	// Both subprocesses are ratchet subcommands of the current binary.
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}

	// Start execute-bead.
	executeCmd := exec.CommandContext(ctx, self, "execute-bead",
		fmt.Sprintf("--execution-id=%d", execID),
		fmt.Sprintf("--db=%s", d.Path),
	)
	executeCmd.Stdout = os.Stdout
	executeCmd.Stderr = os.Stderr
	if err := executeCmd.Start(); err != nil {
		return fmt.Errorf("start execute-bead: %w", err)
	}

	executeDone := make(chan error, 1)
	go func() { executeDone <- executeCmd.Wait() }()

	// Start monitor.
	monitorCmd := exec.Command(self, "monitor",
		fmt.Sprintf("--execution-id=%d", execID),
		fmt.Sprintf("--db=%s", d.Path),
		fmt.Sprintf("--ollama=%s", ollamaURL),
	)
	monitorCmd.Stdout = os.Stdout
	monitorCmd.Stderr = os.Stderr
	if err := monitorCmd.Start(); err != nil {
		// Non-fatal: if the monitor fails to start, the execution proceeds
		// without loop detection. Log and mark monitor_fired=0.
		slog.Error("failed to start monitor; execution continues unmonitored",
			"execution_id", execID, "error", err)
		_ = writeMonitorFired(d, execID, false)
	}

	monitorDone := make(chan error, 1)
	if monitorCmd.Process != nil {
		go func() { monitorDone <- monitorCmd.Wait() }()
	} else {
		close(monitorDone)
	}

	// --- Lifecycle loop ---

	windowPollTicker := time.NewTicker(windowPollInterval)
	defer windowPollTicker.Stop()

	var forcedTermCause string // set only when orchestrator hard-kills execute-bead

	slog.Info("execution window started",
		"execution_id", execID, "bead_id", beadID,
		"budget_s", budget, "monitor_honored", monitorHonored == 1)

loop:
	for {
		select {
		case <-ctx.Done():
			// Orchestrator shutting down — kill both subprocesses.
			killSubprocess(executeCmd, executeDone, graceWindow)
			killMonitor(monitorCmd, monitorDone)
			return ctx.Err()

		case <-executeDone:
			// execute-bead exited on its own (success, timeout, or crash).
			break loop

		case <-windowPollTicker.C:
			if monitorHonored == 0 {
				continue // monitor may run, but its fire signal is suppressed for this bead
			}
			fired, err := readMonitorFired(d, execID)
			if err != nil || !fired {
				continue
			}
			// Monitor fired. Two-stage kill.
			slog.Info("monitor fired — sending SIGTERM to execute-bead", "execution_id", execID)
			if err := executeCmd.Process.Signal(syscall.SIGTERM); err == nil {
				select {
				case <-executeDone:
					// execute-bead exited gracefully; it wrote 'monitor_terminated'.
				case <-time.After(graceWindow):
					// Still running past grace window — SIGKILL.
					slog.Warn("execute-bead did not exit within grace window; sending SIGKILL",
						"execution_id", execID)
					_ = executeCmd.Process.Kill()
					<-executeDone
					forcedTermCause = "monitor_force_killed"
				}
			}
			break loop
		}
	}

	// Kill the monitor (gently, so it can write monitor_fired=0 if it hasn't yet).
	killMonitor(monitorCmd, monitorDone)

	// Allow execute-bead's final DB write to commit before we read it.
	time.Sleep(dbWriteSettle)

	// If the orchestrator hard-killed execute-bead, write the termination cause.
	if forcedTermCause != "" {
		if _, err := d.ExecContext(ctx,
			`UPDATE executions SET termination_cause = ? WHERE id = ?`, forcedTermCause, execID,
		); err != nil {
			slog.Error("write forced termination_cause", "error", err)
		}
	}

	// Safety net: if execute-bead exited without writing termination_cause (crash),
	// write 'success' so ANALYZE_EXECUTION can proceed rather than getting stuck.
	var tc *string
	_ = d.QueryRowContext(ctx, `SELECT termination_cause FROM executions WHERE id = ?`, execID).Scan(&tc)
	if tc == nil {
		slog.Warn("execute-bead exited without termination_cause; defaulting to success",
			"execution_id", execID)
		_, _ = d.ExecContext(ctx,
			`UPDATE executions SET termination_cause = 'success' WHERE id = ?`, execID)
	}

	return finalizeExecution(ctx, d, execID, beadID, job.ProjectID)
}

// finalizeExecution writes ended_at and enqueues ANALYZE_EXECUTION atomically.
func finalizeExecution(ctx context.Context, d *db.DB, execID, beadID, projectID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finalize tx: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE executions SET ended_at = ? WHERE id = ?`, now, execID,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("write ended_at: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?)`,
		projectID, db.VerbAnalyzeExecution, beadID, now, now,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("enqueue ANALYZE_EXECUTION: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit finalize: %w", err)
	}
	slog.Info("execution window complete", "execution_id", execID, "bead_id", beadID)
	return nil
}

// killSubprocess sends SIGTERM, waits up to grace, then SIGKILLs.
func killSubprocess(cmd *exec.Cmd, done <-chan error, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(grace):
		_ = cmd.Process.Kill()
		<-done
	}
}

// killMonitor sends SIGTERM to the monitor and gives it 2 seconds to write
// monitor_fired before falling back to SIGKILL.
func killMonitor(cmd *exec.Cmd, done <-chan error) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

// readMonitorFired returns true if monitor_fired=1 has been written for execID.
func readMonitorFired(d *db.DB, execID int64) (bool, error) {
	var fired *int
	if err := d.QueryRowContext(context.Background(),
		`SELECT monitor_fired FROM executions WHERE id = ?`, execID,
	).Scan(&fired); err != nil {
		return false, err
	}
	return fired != nil && *fired == 1, nil
}

// writeMonitorFired writes monitor_fired to the executions row.
func writeMonitorFired(d *db.DB, execID int64, fired bool) error {
	v := 0
	if fired {
		v = 1
	}
	_, err := d.ExecContext(context.Background(),
		`UPDATE executions SET monitor_fired = ? WHERE id = ?`, v, execID)
	return err
}
