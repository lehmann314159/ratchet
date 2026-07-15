package execution

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
	"ratchet/internal/trace"
)

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

	var lastSize int64
	var staleCount int

	for {
		select {
		case <-ctx.Done():
			// Execution window sent SIGTERM — execute-bead is done.
			return nil

		case <-ticker.C:
			traceBytes, err := os.ReadFile(tracePath)
			if err != nil || len(traceBytes) == 0 {
				continue // trace not yet written; wait for next tick
			}
			traceStr := string(traceBytes)

			// Mechanical stall check: if the trace hasn't grown for 24 consecutive
			// ticks (~12min) and the last event is a TURN marker, the model is likely
			// stuck generating a tool-call argument (write_file content doesn't
			// stream to the trace). Fire without calling the model.
			currentSize := int64(len(traceBytes))
			if currentSize == lastSize {
				staleCount++
			} else {
				staleCount = 0
			}
			lastSize = currentSize

			if staleCount >= 24 && isWriteFileStall(traceStr) {
				fired = true
				if err := writeMonitorFired(d, execID, true); err != nil {
					return fmt.Errorf("write monitor_fired (stall): %w", err)
				}
				slog.Info("monitor fired — write-file stall detected",
					"execution_id", execID, "stale_ticks", staleCount)
				return nil
			}

			// Mechanical check for the two "explicit loop patterns" documented in
			// monitorSystemPrompt. These were previously enforced purely by the
			// model reading the rule out of the prompt against raw trace text, with
			// no mechanical backstop — a weaker model (mistral-small3.2:24b) could
			// miss an instance the rule was written to catch (observed: checkers-v8
			// bead 627 attempt 2, a repeated identical self-check command did not
			// fire). trace.Parse already produces the structured data needed to
			// check both rules directly; ANALYZE_EXECUTION already relies on it.
			if reason := mechanicalLoopPatternCheck(traceStr); reason != "" {
				fired = true
				if err := writeMonitorFired(d, execID, true); err != nil {
					return fmt.Errorf("write monitor_fired (loop pattern): %w", err)
				}
				slog.Info("monitor fired — mechanical loop pattern detected",
					"execution_id", execID, "reason", reason)
				return nil
			}

			decision, err := callMonitorModel(ctx, oc, model, traceStr)
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

// isWriteFileStall returns true when the trace suggests the model is stuck
// generating a tool-call argument. This happens when the model goes directly
// into a write_file (or other) tool call without emitting content tokens —
// the tool-call JSON doesn't stream to the trace, so the file appears frozen
// at the TURN marker. We check that the last non-empty trace line is a TURN
// marker (not a [tool:] or [result] line, which would mean a command is running).
//
// We require at least two TURN markers before firing. A trace with only [TURN 1]
// means the model is still on its first response — it may be streaming a large
// write_file argument and simply hasn't finished yet. Firing here (the project 64
// pattern) kills the model mid-generation. Two markers means the model completed
// at least one turn and is now stalling on a subsequent one (the project 62 pattern).
func isWriteFileStall(trace string) bool {
	if strings.Count(trace, "[TURN ") < 2 {
		return false // first turn not yet complete; model may still be generating
	}
	lines := strings.Split(strings.TrimRight(trace, "\n "), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		return strings.HasPrefix(line, "[TURN ")
	}
	return false
}

// mechanicalLoopPatternCheck mechanically evaluates the two "Explicit loop
// patterns" rules documented in monitorSystemPrompt, returning a non-empty
// reason string if either fires. Runs before the model call as a backstop —
// the model is also instructed to apply these same rules, but nothing
// previously verified it actually did.
func mechanicalLoopPatternCheck(traceStr string) string {
	// Rule 2: repeated write_file missing-path error.
	if strings.Count(traceStr, "write_file requires a 'path' argument") >= 2 {
		return "write_file missing-path error appeared 2+ times"
	}

	// Rule 1: the same run_command producing identical stdout+stderr twice
	// with no successful write_file in between. Walk commands and successful
	// writes in chronological (turn) order; a successful write clears the
	// recurrence tracking, matching "no intervening write" in the rule.
	pt := trace.Parse([]byte(traceStr))
	type event struct {
		turn      int
		seq       int // stable tie-break for same-turn events
		isWrite   bool
		signature string
	}
	events := make([]event, 0, len(pt.Commands)+len(pt.WriteFiles))
	for i, c := range pt.Commands {
		events = append(events, event{turn: c.Turn, seq: i * 2, signature: c.Command + "\x00" + c.Stdout + "\x00" + c.Stderr})
	}
	for i, w := range pt.WriteFiles {
		if w.Succeeded {
			events = append(events, event{turn: w.Turn, seq: i*2 + 1, isWrite: true})
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].turn != events[j].turn {
			return events[i].turn < events[j].turn
		}
		return events[i].seq < events[j].seq
	})

	seen := make(map[string]bool)
	for _, e := range events {
		if e.isWrite {
			seen = make(map[string]bool)
			continue
		}
		if seen[e.signature] {
			return "same run_command produced identical output twice with no intervening write_file"
		}
		seen[e.signature] = true
	}
	return ""
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
