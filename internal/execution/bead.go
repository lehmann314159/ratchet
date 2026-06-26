package execution

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
)

const executeBeadSystemPrompt = `You are a coding agent. Implement the Bead specification provided.

Tools:
- write_file(path, content): create or overwrite a file (path relative to project root)
- read_file(path): read a file (path relative to project root)
- run_command(command): run a shell command in the project root directory

Process:
1. Orient first — before writing any code:
   a. Run ls to see every file in the project root.
   b. Read every .go file present so you know what already exists.
   c. Run go build ./... to see the current compilation state.
   Do this even if the workspace looks empty. Never skip the orient step.
   Do not read files in the traces/ directory — those are execution logs, not source code.
2. Write only to the Output Files listed in the task. Do not create any other files.
   If you find .go files outside that list that contain conflicting declarations left
   by a previous attempt, overwrite them with only the package declaration line to
   clear the conflict.
3. Implement exactly what the Bead specification asks for — nothing more, nothing less.
   Do not create files that are not listed in Output Files.
4. Verify your work by running each item in the Exit Criteria. These are your done condition.
   Run ONLY the commands listed in the Exit Criteria — do not run go test ./... or any other
   broader check. Failures in test files you do not own belong to other Beads; they are not
   your responsibility and you must not attempt to fix them.
5. When every exit criterion passes:
   a. Confirm every file listed in Output Files exists on disk. Run ls to check.
      If any Output File is missing, write it now, then re-run the affected exit criterion.
   b. Only after all Output Files exist AND all exit criteria pass: send your final message
      and call no further tools.

Use relative paths for all file operations. If you cannot make progress, explain why in your final message.`

// RunExecuteBeadMain is the entry point for the "ratchet execute-bead" subcommand.
//
// --mode is available for smoke tests only:
//
//	success: writes a short work log and exits with 'success' (25 seconds)
//	loop:    writes repeating identical failure lines (triggers monitor)
//	hang:    writes periodic lines but never exits (tests SIGTERM contract)
//
// When --mode is empty (the default), the real agentic implementation runs.
func RunExecuteBeadMain(args []string) {
	flags := flag.NewFlagSet("execute-bead", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to SQLite database")
	execID := flags.Int64("execution-id", 0, "executions row ID")
	ollamaURL := flags.String("ollama", "http://192.168.50.241:11434", "Ollama base URL")
	mode := flags.String("mode", "", "stub mode for testing: success|loop|hang (empty = real implementation)")
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

	if *mode != "" {
		if err := runExecuteBeadStub(d, *execID, *mode); err != nil {
			slog.Error("execute-bead stub exiting with error", "execution_id", *execID, "error", err)
			os.Exit(1)
		}
		return
	}

	if err := runExecuteBeadReal(d, *execID, *ollamaURL); err != nil {
		slog.Error("execute-bead exiting with error", "execution_id", *execID, "error", err)
		os.Exit(1)
	}
}

// runExecuteBeadReal runs the agentic tool-calling loop against the assigned model.
func runExecuteBeadReal(d *db.DB, execID int64, ollamaURL string) error {
	ctx := context.Background()

	var tracePath string
	var budget int
	var beadFullTextJSON string
	var model string
	var folderPath string

	if err := d.QueryRowContext(ctx, `
		SELECT e.trace_path, br.execution_budget, br.full_text, vma.model, p.folder_path
		FROM executions e
		JOIN bead_revisions br ON br.id = e.bead_revision_id
		JOIN beads b ON b.id = e.bead_id
		JOIN projects p ON p.id = e.project_id
		JOIN verb_model_assignments vma
		  ON vma.project_id = e.project_id AND vma.verb = 'EXECUTE_BEAD'
		WHERE e.id = ?`, execID,
	).Scan(&tracePath, &budget, &beadFullTextJSON, &model, &folderPath); err != nil {
		return fmt.Errorf("load execution %d: %w", execID, err)
	}

	// The bead's full_text is a JSON-encoded ParsedBead; extract the spec, output files, and exit criteria.
	var parsedBead struct {
		FullText     string   `json:"full_text"`
		OutputFiles  []string `json:"output_files"`
		ExitCriteria []string `json:"exit_criteria"`
	}
	if err := json.Unmarshal([]byte(beadFullTextJSON), &parsedBead); err != nil {
		return fmt.Errorf("parse bead full_text: %w", err)
	}

	traceFile, err := os.OpenFile(tracePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open trace %s: %w", tracePath, err)
	}
	defer traceFile.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	terminationCh := make(chan string, 1)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	budgetTimer := time.NewTimer(time.Duration(budget) * time.Second)
	defer budgetTimer.Stop()

	go func() {
		select {
		case <-sigCh:
			terminationCh <- "monitor_terminated"
			cancel()
		case <-budgetTimer.C:
			terminationCh <- "timeout"
			cancel()
		case <-ctx.Done():
		}
	}()

	slog.Info("execute-bead started", "execution_id", execID, "model", model, "budget_s", budget)

	oc := ollama.NewUnbounded(ollamaURL)
	tools := toolDefinitions()
	messages := []ollama.Message{
		{Role: "system", Content: guidance.Inject(executeBeadSystemPrompt, folderPath)},
		{Role: "user", Content: buildBeadUserMsg(parsedBead.FullText, parsedBead.OutputFiles, parsedBead.ExitCriteria)},
	}

	for turn := 1; ; turn++ {
		writeLine(traceFile, fmt.Sprintf("[TURN %d]", turn))

		msg, err := oc.ChatWithTools(ctx, model, messages, tools, nil)
		if err != nil {
			select {
			case cause := <-terminationCh:
				writeLine(traceFile, fmt.Sprintf("[terminated: %s]", cause))
				return writeTerminationCause(d, execID, cause)
			default:
			}
			return fmt.Errorf("model call: %w", err)
		}

		if msg.Content != "" {
			writeLine(traceFile, msg.Content)
		}
		messages = append(messages, msg)

		if len(msg.ToolCalls) == 0 {
			writeLine(traceFile, "[done — no further tool calls]")
			return writeTerminationCause(d, execID, "success")
		}

		for _, tc := range msg.ToolCalls {
			writeLine(traceFile, fmt.Sprintf("[tool: %s %v]", tc.Function.Name, tc.Function.Arguments))
			result := executeTool(ctx, tc, folderPath)
			writeLine(traceFile, fmt.Sprintf("[result]\n%s", result))
			messages = append(messages, ollama.Message{
				Role:    "tool",
				Content: result,
			})
		}

		select {
		case cause := <-terminationCh:
			writeLine(traceFile, fmt.Sprintf("[terminated: %s]", cause))
			return writeTerminationCause(d, execID, cause)
		default:
		}
	}
}

// runExecuteBeadStub is the original stub implementation, preserved for smoke tests.
func runExecuteBeadStub(d *db.DB, execID int64, mode string) error {
	ctx := context.Background()

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

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)

	budgetTimer := time.NewTimer(time.Duration(budget) * time.Second)
	defer budgetTimer.Stop()

	workTicker := time.NewTicker(5 * time.Second)
	defer workTicker.Stop()

	step := 0

	slog.Info("execute-bead started (stub)", "execution_id", execID, "mode", mode, "budget_s", budget)

	for {
		select {
		case <-sigCh:
			writeLine(traceFile, fmt.Sprintf("[step %d] received SIGTERM — flushing and exiting", step))
			return writeTerminationCause(d, execID, "monitor_terminated")

		case <-budgetTimer.C:
			writeLine(traceFile, fmt.Sprintf("[step %d] execution budget exhausted", step))
			return writeTerminationCause(d, execID, "timeout")

		case <-workTicker.C:
			step++
			line := stubLine(mode, step)
			writeLine(traceFile, line)

			if mode == "success" && step >= 5 {
				writeLine(traceFile, "all steps complete")
				return writeTerminationCause(d, execID, "success")
			}
		}
	}
}

// stubLine returns a trace line for the given stub mode and step.
func stubLine(mode string, step int) string {
	ts := time.Now().UTC().Format("15:04:05")
	switch mode {
	case "loop":
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

// buildBeadUserMsg constructs the user message for the EXECUTE_BEAD agent.
// Output files are presented as a hard write constraint before the spec so the
// agent sees them before reading implementation details. Exit criteria are a
// numbered checklist so the agent has an unambiguous done condition.
func buildBeadUserMsg(specText string, outputFiles []string, exitCriteria []string) string {
	var msg string

	if len(outputFiles) > 0 {
		msg += "## Output Files\n\nYou may ONLY write to these files. Do not create any other files.\n\n"
		for _, f := range outputFiles {
			msg += fmt.Sprintf("- %s\n", f)
		}
		msg += "\n"
	}

	msg += specText

	if len(exitCriteria) > 0 {
		msg += "\n\n## Exit Criteria\n\nYour done condition is exactly: each of the following checks passes AND every Output File above exists on disk. Run only these checks — no other test commands. Stop immediately once all pass.\n\n"
		for i, c := range exitCriteria {
			msg += fmt.Sprintf("%d. %s\n", i+1, c)
		}
	}

	return msg
}

func writeLine(f *os.File, line string) {
	_, _ = fmt.Fprintln(f, line)
}

func writeTerminationCause(d *db.DB, execID int64, cause string) error {
	_, err := d.ExecContext(context.Background(),
		`UPDATE executions SET termination_cause = ? WHERE id = ?`, cause, execID)
	if err != nil {
		return fmt.Errorf("write termination_cause=%s for execution %d: %w", cause, execID, err)
	}
	slog.Info("execute-bead done", "execution_id", execID, "termination_cause", cause)
	return nil
}
