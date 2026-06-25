package execution

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

const monitorSystemPrompt = `You watch a live execution trace from a coding agent.

FIRE only if you see definite recurrence: the same failure mode or the same unproductive action appearing two or more times with no meaningful variation between cycles.

Do NOT fire for: building and testing normally (even if tests fail), progressive iteration where each attempt is meaningfully different, or a single failure with no recurrence.

False positives are worse than false negatives — when in doubt, do not fire.

Respond with exactly two lines:
DECISION: FIRE | NO_FIRE
REASON: <one sentence, specific to what you saw in the trace>`

// RunMonitorMain is the entry point for the `ratchet monitor` subcommand.
func RunMonitorMain(args []string) {
	flags := flag.NewFlagSet("monitor", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to SQLite database")
	execID := flags.Int64("execution-id", 0, "executions row ID to monitor")
	ollamaURL := flags.String("ollama", "http://192.168.50.241:11434", "Ollama base URL")
	_ = flags.Parse(args)

	if *execID == 0 {
		slog.Error("monitor: --execution-id is required")
		os.Exit(1)
	}

	d, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("monitor: open db", "error", err)
		os.Exit(1)
	}
	defer d.Close()

	oc := ollama.NewUnbounded(*ollamaURL)

	if err := runMonitor(d, oc, *execID); err != nil {
		slog.Error("monitor exiting with error", "execution_id", *execID, "error", err)
		os.Exit(1)
	}
}

// runMonitor polls the trace file every monitorPollInterval, calls the model,
// and writes monitor_fired when done. It exits when it fires or when it
// receives SIGTERM (from the execution window after execute-bead exits).
func runMonitor(d *db.DB, oc *ollama.Client, execID int64) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Load trace path and model assignment.
	var tracePath string
	var projectID int64
	if err := d.QueryRowContext(ctx,
		`SELECT trace_path, project_id FROM executions WHERE id = ?`, execID,
	).Scan(&tracePath, &projectID); err != nil {
		return fmt.Errorf("load execution %d: %w", execID, err)
	}

	var model string
	if err := d.QueryRowContext(ctx,
		`SELECT model FROM verb_model_assignments WHERE project_id = ? AND verb = ?`,
		projectID, db.VerbMonitorExecution,
	).Scan(&model); err != nil {
		return fmt.Errorf("load monitor model for project %d: %w", projectID, err)
	}

	fired := false
	defer func() {
		// Always write the result — either 1 (fired) or 0 (ran to completion
		// without firing). Uses a fresh context since ours may be cancelled.
		if !fired {
			_ = writeMonitorFired(d, execID, false)
		}
	}()

	ticker := time.NewTicker(monitorPollInterval)
	defer ticker.Stop()

	slog.Info("monitor started", "execution_id", execID, "model", model)

	for {
		select {
		case <-ctx.Done():
			// Execution window sent SIGTERM — execute-bead is done.
			return nil

		case <-ticker.C:
			trace, err := os.ReadFile(tracePath)
			if err != nil || len(trace) == 0 {
				continue // trace not yet written; wait for next tick
			}

			decision, err := callMonitorModel(ctx, oc, model, string(trace))
			if err != nil {
				slog.Warn("monitor model call failed; skipping this tick",
					"execution_id", execID, "error", err)
				continue
			}

			slog.Info("monitor decision", "execution_id", execID, "decision", decision)

			if decision == "FIRE" {
				fired = true
				if err := writeMonitorFired(d, execID, true); err != nil {
					return fmt.Errorf("write monitor_fired: %w", err)
				}
				slog.Info("monitor fired", "execution_id", execID)
				return nil
			}
		}
	}
}

// callMonitorModel calls the model and returns "FIRE" or "NO_FIRE".
func callMonitorModel(ctx context.Context, oc *ollama.Client, model, trace string) (string, error) {
	raw, err := oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: monitorSystemPrompt},
		{Role: "user", Content: "Current trace:\n\n" + trace},
	}, nil)
	if err != nil {
		return "", err
	}
	return parseDecision(raw), nil
}

// parseDecision extracts FIRE or NO_FIRE from a model response.
// Returns NO_FIRE on any parse failure — the safe default.
func parseDecision(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		after, ok := strings.CutPrefix(line, "DECISION:")
		if !ok {
			continue
		}
		val := strings.TrimSpace(after)
		if val == "FIRE" || val == "NO_FIRE" {
			return val
		}
	}
	return "NO_FIRE"
}
