package execution

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ratchet/internal/db"
)

// RunExecuteBeadMain is the entry point for the `ratchet execute-bead` subcommand.
//
// For Step 3, this is a stub that simulates a coding agent: it writes periodic
// log lines to the trace file, then exits. Controlled by --mode:
//
//   - success (default): writes a short varied work log, exits with 'success'
//   - loop:              writes repeating identical failure lines (triggers monitor)
//   - hang:              writes periodic lines but never exits (tests timeout and SIGTERM)
//
// Regardless of mode, SIGTERM always writes 'monitor_terminated' and exits
// immediately, and the execution_budget is enforced as a hard timeout.
func RunExecuteBeadMain(args []string) {
	flags := flag.NewFlagSet("execute-bead", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to SQLite database")
	execID := flags.Int64("execution-id", 0, "executions row ID")
	mode := flags.String("mode", "success", "stub mode: success|loop|hang")
	_ = flags.Parse(args)

	if *execID == 0 {
		slog.Error("execute-bead: --execution-id is required")
		os.Exit(1)
	}

	d, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("execute-bead: open db", "error", err)
		os.Exit(1)
	}
	defer d.Close()

	if err := runExecuteBead(d, *execID, *mode); err != nil {
		slog.Error("execute-bead exiting with error", "execution_id", *execID, "error", err)
		os.Exit(1)
	}
}

func runExecuteBead(d *db.DB, execID int64, mode string) error {
	ctx := context.Background()

	// Load trace path and execution budget from the executions + bead_revisions join.
	var tracePath string
	var budget int
	if err := d.QueryRowContext(ctx, `
		SELECT e.trace_path, br.execution_budget
		FROM executions e
		JOIN bead_revisions br ON br.id = e.bead_revision_id
		WHERE e.id = ?`, execID,
	).Scan(&tracePath, &budget); err != nil {
		return fmt.Errorf("load execution %d: %w", execID, err)
	}

	traceFile, err := os.OpenFile(tracePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open trace file %s: %w", tracePath, err)
	}
	defer traceFile.Close()

	// One-time SIGTERM handler: write 'monitor_terminated', exit.
	// "One-time" means the handler does not loop — it fires once and the function
	// returns, which exits the subprocess.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)

	budgetTimer := time.NewTimer(time.Duration(budget) * time.Second)
	defer budgetTimer.Stop()

	// Write a work line every 5 seconds.
	workTicker := time.NewTicker(5 * time.Second)
	defer workTicker.Stop()

	step := 0

	slog.Info("execute-bead started", "execution_id", execID, "mode", mode, "budget_s", budget)

	for {
		select {
		case <-sigCh:
			// Monitor-triggered termination (or orchestrator shutdown).
			writeLine(traceFile, fmt.Sprintf("[step %d] received SIGTERM — flushing and exiting", step))
			return writeTerminationCause(d, execID, "monitor_terminated")

		case <-budgetTimer.C:
			writeLine(traceFile, fmt.Sprintf("[step %d] execution budget exhausted", step))
			return writeTerminationCause(d, execID, "timeout")

		case <-workTicker.C:
			step++
			line := stubLine(mode, step)
			writeLine(traceFile, line)

			// success mode: exit cleanly after 5 steps.
			if mode == "success" && step >= 5 {
				writeLine(traceFile, "all steps complete")
				return writeTerminationCause(d, execID, "success")
			}
			// loop and hang modes run until SIGTERM or budget.
		}
	}
}

// stubLine returns a trace line appropriate for the given mode and step.
func stubLine(mode string, step int) string {
	ts := time.Now().UTC().Format("15:04:05")
	switch mode {
	case "loop":
		// Identical content every step — designed to trigger the loop detector.
		return fmt.Sprintf("[%s] TestReadBit FAIL: exit status 1, nil pointer dereference at line 88", ts)
	default: // "success" and "hang"
		steps := []string{
			"parsing bead specification",
			"scaffolding package structure",
			"implementing core logic",
			"writing unit tests",
			"running test suite — 3 passed, 0 failed",
		}
		if step <= len(steps) {
			return fmt.Sprintf("[%s] step %d: %s", ts, step, steps[step-1])
		}
		return fmt.Sprintf("[%s] step %d: working...", ts, step)
	}
}

func writeLine(f *os.File, line string) {
	_, _ = fmt.Fprintln(f, line)
}

// writeTerminationCause updates the executions row. Called by the subprocess
// itself; the orchestrator writes 'monitor_force_killed' only when it hard-kills.
func writeTerminationCause(d *db.DB, execID int64, cause string) error {
	_, err := d.ExecContext(context.Background(),
		`UPDATE executions SET termination_cause = ? WHERE id = ?`, cause, execID)
	if err != nil {
		return fmt.Errorf("write termination_cause=%s for execution %d: %w", cause, execID, err)
	}
	slog.Info("execute-bead done", "execution_id", execID, "termination_cause", cause)
	return nil
}
