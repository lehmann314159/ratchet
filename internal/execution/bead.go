package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/execcheck"
	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
)

// writeGracePeriod is the extra time given to a model response to complete after
// the execution budget fires. The budget stop is "soft": we let the current
// ChatWithTools call finish so any in-flight write_file arguments can fully
// stream in before we exit. A hard cancel fires after this window regardless.
const writeGracePeriod = 2 * time.Minute

// testExecCheckpointInterval / testExecCeiling, when non-zero, override
// runExecuteBeadReal's soft checkpoint cadence and absolute ceiling. Tests set
// them to sub-second values so the stall-detection paths are reachable without
// waiting minutes. Zero (the default) means: use execCheckpointInterval /
// execAbsoluteCeiling. These are fixed durations — EXECUTE_BEAD timing is no
// longer derived from the bead's execution_budget (see
// docs/execute-checkpoint-decouple-plan.md).
var (
	testExecCheckpointInterval time.Duration
	testExecCeiling            time.Duration
)

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
	var beadFullTextJSON string
	var model string
	var folderPath string
	var beadID int64
	var revisionID int64

	// execution_budget is deliberately NOT selected: EXECUTE_BEAD timing is
	// fixed (execCheckpointInterval / execAbsoluteCeiling), not budget-derived.
	if err := d.QueryRowContext(ctx, `
		SELECT e.trace_path, br.full_text, vma.model, p.folder_path, e.bead_id, br.id
		FROM executions e
		JOIN bead_revisions br ON br.id = e.bead_revision_id
		JOIN beads b ON b.id = e.bead_id
		JOIN projects p ON p.id = e.project_id
		JOIN verb_model_assignments vma
		  ON vma.project_id = e.project_id AND vma.verb = 'EXECUTE_BEAD'
		WHERE e.id = ?`, execID,
	).Scan(&tracePath, &beadFullTextJSON, &model, &folderPath, &beadID, &revisionID); err != nil {
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

	// Progress / stall detection. The tracker accumulates per-turn mechanical
	// signal (see progress.go); the wall-clock goroutine below reads its
	// atomic lastProductive time. Both replace the old "fixed budget -> timeout
	// -> ADJUDICATE doubles the budget and retries the identical spec" loop,
	// which burned hours to no effect on a stalled attempt (baseline-14 bead
	// 318 — memory/project_execute_progress_detection).
	tracker := newProgressTracker(time.Now())

	// Fixed cadence + ceiling — NOT derived from execution_budget. A large budget
	// used to push the first checkpoint out past the point of usefulness
	// (baseline-15). See docs/execute-checkpoint-decouple-plan.md.
	checkpointDur := execCheckpointInterval
	if testExecCheckpointInterval > 0 {
		checkpointDur = testExecCheckpointInterval
	}
	ceilingDur := execAbsoluteCeiling
	if testExecCeiling > 0 {
		ceilingDur = testExecCeiling
	}

	terminationCh := make(chan string, 1)
	// budgetCheckpointCh: the wall-clock goroutine pings this once per checkpoint
	// interval. The loop decides, between turns, whether to extend (forward
	// progress this interval) or request a graceful finalize (none).
	budgetCheckpointCh := make(chan struct{}, 1)
	// extendCh / finalizeCh: loop -> goroutine. extend resets the soft timer for
	// another interval; finalize tightens the hard timer to execFinalizeGrace
	// and makes its expiry terminate as "stalled" rather than "timeout".
	extendCh := make(chan struct{}, 1)
	finalizeCh := make(chan struct{}, 1)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)

	go func() {
		soft := time.NewTimer(checkpointDur)
		defer soft.Stop()
		hard := time.NewTimer(ceilingDur)
		defer hard.Stop()
		finalizing := false
		drainReset := func(t *time.Timer, d time.Duration) {
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			t.Reset(d)
		}
		for {
			select {
			case <-sigCh:
				trySendCause(terminationCh, "monitor_terminated")
				cancel()
				return
			case <-ctx.Done():
				return
			case <-extendCh:
				drainReset(soft, checkpointDur)
			case <-finalizeCh:
				finalizing = true
				drainReset(hard, execFinalizeGrace)
			case <-soft.C:
				trySignal(budgetCheckpointCh)
				soft.Reset(checkpointDur)
			case <-hard.C:
				cause := "timeout"
				if finalizing || time.Since(tracker.lastProductive()) > execStallWindow {
					cause = "stalled"
				}
				trySendCause(terminationCh, cause)
				cancel()
				return
			}
		}
	}()

	slog.Info("execute-bead started", "execution_id", execID, "model", model,
		"checkpoint_s", int(checkpointDur.Seconds()), "ceiling_s", int(ceilingDur.Seconds()))

	// Sandbox all scratch work. write_file / read_file / run_command and the
	// in-loop exit-criteria check operate in a per-attempt temp dir seeded from
	// the live folder; on every exit path only the files the model was allowed
	// to write this attempt are copied back. A stray repro program or a botched
	// run_command therefore cannot poison the shared tree.
	// See docs/execute-workspace-sandbox-plan.md.
	ws, err := newExecWorkspace(folderPath)
	if err != nil {
		return fmt.Errorf("execution %d: %w", execID, err)
	}
	defer ws.cleanup()
	workDir := ws.dir

	contextFiles := loadContextFiles(workDir, parsedBead.OutputFiles)
	priorHistory := loadPriorAttemptSummary(ctx, d, beadID)
	resumeNote := sameRevisionResumeNote(ctx, d, beadID, revisionID, execID)

	// Compute the files the model was instructed to write this attempt.
	// test-first mode: tests absent → list only test files.
	// tests-locked mode: tests present, impl absent → list only impl files (tests are certified).
	// normal mode: list all output files.
	testFirst := isTestFirstMode(workDir, parsedBead.OutputFiles)
	testsLocked := !testFirst && isTestsLockedMode(workDir, parsedBead.OutputFiles)
	var expectedFiles []string
	switch {
	case testFirst:
		for _, f := range parsedBead.OutputFiles {
			if strings.HasSuffix(f, "_test.go") {
				expectedFiles = append(expectedFiles, f)
			}
		}
	case testsLocked:
		for _, f := range parsedBead.OutputFiles {
			if !strings.HasSuffix(f, "_test.go") {
				expectedFiles = append(expectedFiles, f)
			}
		}
	default:
		expectedFiles = parsedBead.OutputFiles
	}

	oc := ollama.NewUnbounded(ollamaURL)
	tools := toolDefinitions()
	// OmitFormat: EXECUTE_BEAD is a pure tool-calling loop (write_file / read_file
	// / run_command) — msg.Content is only streamed to the trace and checked for
	// emptiness, never parsed as JSON (the sole json.Unmarshal above is of the
	// bead spec from the DB). The default format:"json" grammar therefore buys
	// nothing and, same as it did for REFINE_TESTS_WRITE, blocks native
	// tool-calling: gemma4:31b runs its whole budget in a single unterminating
	// thinking stream and never emits a write_file call (exprvm-web-baseline-3
	// bead 242, execs 198/199: content_chars=0 throughout, thinking_chars
	// climbing to the budget wall, one [TURN] marker). See
	// docs/format-json-tool-turn.md.
	// NumPredict: raise the per-turn generated-token cap from the 8192 default.
	// EXECUTE_BEAD is now muse-glimmer:30b-q8_0-dflash, which on its passing
	// parser-bead runs emitted ~32K chars (~8K tokens) of content in a single
	// turn plus thinking — the 8192 cap clips a legitimate one-shot
	// implementation mid-file. 16384 gives headroom while still bounding a
	// degenerate non-terminating thinking stream (exprvm-web bakeoff, 2026-09-02).
	execOpts := &ollama.Options{OmitFormat: true, NumPredict: 16384}
	messages := []ollama.Message{
		{Role: "system", Content: guidance.InjectForVerbPath(executeBeadSystemPrompt, workDir, db.VerbExecuteBead, "")},
		{Role: "user", Content: buildBeadUserMsg(parsedBead.FullText, parsedBead.OutputFiles, parsedBead.ExitCriteria, contextFiles, priorHistory, resumeNote, workDir)},
	}

	// Flush the sandbox back to the live folder on every exit path. Registered
	// after defer ws.cleanup() so it runs first (LIFO). Only expectedFiles (the
	// files the model may write this attempt) are copied back; anything else the
	// model created or modified is discarded and named in the trace for
	// ANALYZE_EXECUTION to surface as behavioral signal.
	defer func() {
		discarded, cbErr := ws.copyBack(expectedFiles)
		if cbErr != nil {
			slog.Warn("execute-bead: copy sandbox output files back to project folder",
				"execution_id", execID, "error", cbErr)
		}
		if line := discardedFilesTraceLine(discarded); line != "" {
			writeLine(traceFile, line)
			slog.Info("execute-bead: discarded out-of-scope workspace files",
				"execution_id", execID, "count", len(discarded))
		}
	}()

	var writeFileCount int
	var stubWarningInjected bool
	var missingPathWarningInjected bool
	var finalizeInjected bool // the one graceful-finalize directive has been sent
	var extensionsUsed int
	prevTurnSig := ""
	lastCheckpoint := time.Now()
	ledger := newHashLedger()
	expectedSet := make(map[string]bool, len(expectedFiles))
	for _, f := range expectedFiles {
		expectedSet[filepath.Clean(f)] = true
	}

	injectFinalize := func(reason string) {
		finalizeInjected = true
		writeLine(traceFile, fmt.Sprintf("[progress] %s — requesting graceful finalize", reason))
		messages = append(messages, ollama.Message{Role: "user", Content: buildStallFinalizeDirective(expectedFiles)})
		trySignal(finalizeCh)
	}

	for turn := 1; ; turn++ {
		writeLine(traceFile, fmt.Sprintf("[TURN %d]", turn))

		msg, err := oc.ChatWithTools(ctx, model, messages, tools, execOpts, traceFile)
		if err != nil {
			select {
			case cause := <-terminationCh:
				writeLine(traceFile, fmt.Sprintf("[terminated: %s]", cause))
				return writeTerminationCause(d, execID, cause)
			default:
			}
			return fmt.Errorf("model call: %w", err)
		}

		// Content was already streamed to traceFile token-by-token during the call.
		messages = append(messages, msg)

		// Monitor SIGTERM or the hard wall-clock ceiling fired while we streamed.
		select {
		case cause := <-terminationCh:
			writeLine(traceFile, fmt.Sprintf("[terminated: %s]", cause))
			return writeTerminationCause(d, execID, cause)
		default:
		}

		lengthCapEmpty := msg.DoneReason == "length" && len(msg.ToolCalls) == 0 &&
			strings.TrimSpace(msg.Content) == ""

		if len(msg.ToolCalls) == 0 {
			// A finished bead always wins over a stall verdict — check the exit
			// criteria on disk first. A prior attempt may already have finished
			// the job (e.g. after ADJUDICATE's "don't rewrite files that are
			// already correct" guidance); this is the same ground-truth check
			// ADJUDICATE's declare_success gate uses.
			if ok, _ := execcheck.VerifyExitCriteria(ctx, workDir, parsedBead.ExitCriteria); ok {
				writeLine(traceFile, "[done — exit criteria already satisfied on disk; no write needed]")
				return writeTerminationCause(d, execID, "success")
			}

			tracker.observe(time.Now(), turnObs{lengthCapEmpty: lengthCapEmpty})

			// The single turn granted after a graceful-finalize directive is over.
			if finalizeInjected {
				writeLine(traceFile, "[terminated: stalled — no output after finalize directive]")
				return writeTerminationCause(d, execID, "stalled")
			}
			if stall, reason := tracker.wall(time.Now(), turn); stall {
				injectFinalize(reason)
				continue
			}

			// Model declared done without ever calling write_file — likely
			// emitted code as prose. One-time nudge, then label distinctly.
			if !stubWarningInjected && writeFileCount == 0 && len(expectedFiles) > 0 {
				stubWarningInjected = true
				writeLine(traceFile, "[injected: no-write warning — model produced prose instead of calling write_file]")
				messages = append(messages, ollama.Message{
					Role:    "user",
					Content: buildNoWriteWarning(expectedFiles),
				})
				continue
			}
			if stubWarningInjected && writeFileCount == 0 {
				// The warning already fired once and the model still wrote
				// nothing on the very next turn — none of success/timeout/
				// monitor_terminated/monitor_force_killed accurately describe
				// this, so label it distinctly rather than mislabeling a
				// zero-output run as a normal completion.
				writeLine(traceFile, "[done — no further tool calls after no-write warning; nothing written]")
				return writeTerminationCause(d, execID, "no_write")
			}
			writeLine(traceFile, "[done — no further tool calls]")
			return writeTerminationCause(d, execID, "success")
		}

		var missingPathDetected bool
		var turnResults []string
		productive := false
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name == "write_file" {
				writeFileCount++
			}
			writeLine(traceFile, fmt.Sprintf("[tool: %s %v]", tc.Function.Name, tc.Function.Arguments))
			result := executeTool(ctx, tc, workDir)
			writeLine(traceFile, fmt.Sprintf("[result]\n%s", result))
			turnResults = append(turnResults, result)
			if tc.Function.Name == "write_file" {
				if strings.Contains(result, "write_file requires a 'path' argument") {
					missingPathDetected = true
				} else if strings.HasPrefix(result, "ok:") {
					// Productive iff the write left an in-scope output file at a
					// content hash it has not held before this attempt (rejects
					// no-op rewrites and A->B->A reverts).
					if p, _ := tc.Function.Arguments["path"].(string); p != "" {
						clean := filepath.Clean(p)
						if expectedSet[clean] {
							if h := hashFileHex(filepath.Join(workDir, clean)); h != "" && ledger.recordWrite(clean, h) {
								productive = true
							}
						}
					}
				}
			}
			messages = append(messages, ollama.Message{
				Role:    "tool",
				Content: result,
			})
		}

		turnSig := toolTurnSignature(msg.ToolCalls, turnResults)
		identicalCall := turnSig != "" && turnSig == prevTurnSig && !productive
		prevTurnSig = turnSig
		tracker.observe(time.Now(), turnObs{productive: productive, identicalCall: identicalCall})

		if missingPathDetected && !missingPathWarningInjected {
			missingPathWarningInjected = true
			writeLine(traceFile, "[injected: missing write_file path — prompting model to retry with explicit path]")
			messages = append(messages, ollama.Message{
				Role:    "user",
				Content: buildMissingPathWarning(expectedFiles),
			})
			continue
		}

		// The single turn granted after a graceful-finalize directive is over:
		// exit criteria decide success vs stalled; PR #7 copy-back has the disk
		// state either way.
		if finalizeInjected {
			if ok, _ := execcheck.VerifyExitCriteria(ctx, workDir, parsedBead.ExitCriteria); ok {
				writeLine(traceFile, "[done — exit criteria satisfied after finalize directive]")
				return writeTerminationCause(d, execID, "success")
			}
			writeLine(traceFile, "[terminated: stalled — no forward progress after finalize directive]")
			return writeTerminationCause(d, execID, "stalled")
		}

		if stall, reason := tracker.wall(time.Now(), turn); stall {
			injectFinalize(reason)
			continue
		}

		// Wall-clock checkpoint: extend if this interval saw forward progress,
		// otherwise ask for a graceful finalize.
		select {
		case <-budgetCheckpointCh:
			progressed := tracker.lastProductive().After(lastCheckpoint)
			lastCheckpoint = time.Now()
			switch {
			case progressed:
				// Forward progress this interval — keep going. The absolute
				// wall-clock ceiling (execAbsoluteCeiling) is the real bound.
				extensionsUsed++
				writeLine(traceFile, fmt.Sprintf(
					"[progress] budget checkpoint %d — forward progress detected, extending", extensionsUsed))
				trySignal(extendCh)
			case !finalizeInjected:
				injectFinalize("no forward progress at budget checkpoint")
			default:
				writeLine(traceFile, "[terminated: stalled — no forward progress at budget checkpoint]")
				return writeTerminationCause(d, execID, "stalled")
			}
		default:
		}

		select {
		case cause := <-terminationCh:
			writeLine(traceFile, fmt.Sprintf("[terminated: %s]", cause))
			return writeTerminationCause(d, execID, cause)
		default:
		}
	}
}

// trySendCause does a non-blocking send of cause on ch (buffered, size 1).
func trySendCause(ch chan string, cause string) {
	select {
	case ch <- cause:
	default:
	}
}

// trySignal does a non-blocking send on a buffered struct{} channel.
func trySignal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// toolTurnSignature builds a stable string identifying one turn's tool calls and
// their results, for detecting a turn that byte-for-byte repeats the previous
// one. fmt's %v on map[string]any sorts keys, so the arguments render
// deterministically. Empty when the turn made no tool calls.
func toolTurnSignature(calls []ollama.ToolCall, results []string) string {
	if len(calls) == 0 {
		return ""
	}
	var b strings.Builder
	for i, tc := range calls {
		fmt.Fprintf(&b, "%s(%v)\x00", tc.Function.Name, tc.Function.Arguments)
		if i < len(results) {
			b.WriteString(results[i])
		}
		b.WriteByte('\x1e')
	}
	return b.String()
}

// hashFileHex returns the hex SHA-256 of the file at path, or "" if it cannot be
// read.
func hashFileHex(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// buildStallFinalizeDirective is the one user turn injected when the attempt has
// walled. It tells the model to write its best current version of each output
// file and stop — the execution ends after the next turn regardless.
func buildStallFinalizeDirective(expectedFiles []string) string {
	list := strings.Join(expectedFiles, ", ")
	return fmt.Sprintf(
		"No measurable progress has been made in the last several turns — no output file has "+
			"changed on disk and no exit criterion has newly passed.\n\n"+
			"Stop analyzing. In your next turn, call write_file once for each output file (%s) with "+
			"your best current version of its complete contents, then stop. The execution ends after "+
			"your next turn — this is your final opportunity to write.",
		list,
	)
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
// contextFiles and priorHistory are injected after the task so the model has
// all necessary context without spending turns on orientation reads.
//
// When the bead is in test-first mode (test files absent, impl files also present
// in output_files), the message is narrowed: only test files are listed in Output
// Files, and the exit criterion is replaced with a compile-only check. This causes
// the model to write tests first so they can be independently verified before
// implementation begins.
func buildBeadUserMsg(specText string, outputFiles []string, exitCriteria []string, contextFiles, priorHistory, resumeNote, folderPath string) string {
	var msg string

	testFirst := isTestFirstMode(folderPath, outputFiles)
	testsLocked := !testFirst && isTestsLockedMode(folderPath, outputFiles)

	// Determine which files to show as write targets.
	displayFiles := outputFiles
	switch {
	case testFirst:
		var tf []string
		for _, f := range outputFiles {
			if strings.HasSuffix(f, "_test.go") {
				tf = append(tf, f)
			}
		}
		displayFiles = tf
	case testsLocked:
		var impl []string
		for _, f := range outputFiles {
			if !strings.HasSuffix(f, "_test.go") {
				impl = append(impl, f)
			}
		}
		if len(impl) > 0 {
			displayFiles = impl
		}
	}

	if len(displayFiles) > 0 {
		msg += "## Output Files\n\nYou may ONLY write to these files. Do not create any other files.\n\n"
		for _, f := range displayFiles {
			msg += fmt.Sprintf("- %s\n", f)
		}
		msg += "\n"
	}

	switch {
	case testFirst:
		msg += "## Test-First Mode\n\n" +
			"This bead delivers both test files and implementation files. On this first attempt, " +
			"write ONLY the test files listed above. Do NOT write any implementation files — " +
			"those will be written in the next attempt after your tests are independently reviewed.\n\n" +
			"The stub implementations already compile. Your tests WILL FAIL against the stubs — " +
			"this is expected. Do not try to make the tests pass on this attempt.\n\n" +
			"Write test cases that correctly verify what the specification says the implementation " +
			"should do. Be precise about expected values — derive them from the specification.\n\n"
	case testsLocked:
		msg += "## Tests Locked\n\n" +
			"The following test files were pre-certified by REFINE_TESTS and are LOCKED:\n"
		for _, f := range outputFiles {
			if strings.HasSuffix(f, "_test.go") {
				msg += fmt.Sprintf("- %s\n", f)
			}
		}
		msg += "\nDo NOT write to these files under any circumstances. " +
			"Write ONLY the implementation files listed in Output Files above.\n\n"
	}

	msg += specText

	if testFirst {
		msg += "\n\n## Exit Criteria (Test-First Mode)\n\n" +
			"Your only exit criterion for this attempt is:\n\n" +
			"1. go test -c -o /dev/null ./...\n\n" +
			"This compiles all source and test files without running any tests. " +
			"A clean compile is your only goal. Do not run the tests."
	} else if len(exitCriteria) > 0 {
		msg += "\n\n## Exit Criteria\n\nYour done condition is exactly: each of the following checks passes AND every Output File above exists on disk. Run only these checks — no other test commands. Stop immediately once all pass.\n\n"
		for i, c := range exitCriteria {
			msg += fmt.Sprintf("%d. %s\n", i+1, c)
		}
	}

	if resumeNote != "" {
		msg += "\n\n## Resuming a Prior Attempt\n\n" + resumeNote
	}

	if priorHistory != "" {
		msg += "\n\n## Prior Attempt History\n\n" + priorHistory
	}

	if contextFiles != "" {
		msg += "\n\n## Current Project Files\n\nThe following files currently exist in the project. Use them as context — do not re-read them with read_file unless you need to verify your own writes.\n\n" + contextFiles
	}

	return msg
}

// isTestFirstMode returns true when the bead has both *_test.go files and
// non-test .go files in output_files, and ALL *_test.go output files are
// absent from disk. In this state, attempt 1 should write only the test files
// so they can be independently verified before implementation begins.
func isTestFirstMode(folderPath string, outputFiles []string) bool {
	hasTest, hasImpl := false, false
	for _, f := range outputFiles {
		if strings.HasSuffix(f, "_test.go") {
			hasTest = true
		} else if strings.HasSuffix(f, ".go") {
			hasImpl = true
		}
	}
	if !hasTest || !hasImpl {
		return false
	}
	// All test files must be absent from disk.
	for _, f := range outputFiles {
		if strings.HasSuffix(f, "_test.go") {
			if _, err := os.Stat(filepath.Join(folderPath, f)); err == nil {
				return false // test file already exists — not a first attempt
			}
		}
	}
	return true
}

// isTestsLockedMode returns true when the bead has *_test.go output files AND
// at least one of them already exists on disk AND the bead also has at least
// one non-test output file. This indicates REFINE_TESTS has already run: the
// test files are certified and must not be modified by EXECUTE_BEAD; the
// executor should write ONLY the implementation files.
//
// The non-test-file requirement matters for pure-test beads (output_files
// consisting entirely of *_test.go — e.g. integration-test beads with no
// implementation files of their own). Without it, a pure-test bead whose file
// survives on disk from a prior attempt would be mistaken for a REFINE_TESTS-
// certified file belonging to someone else, producing a contradictory prompt
// (the file is simultaneously the only "Output File" and explicitly "LOCKED")
// with no legal implementation file to write instead.
func isTestsLockedMode(folderPath string, outputFiles []string) bool {
	hasNonTest := false
	for _, f := range outputFiles {
		if !strings.HasSuffix(f, "_test.go") {
			hasNonTest = true
			break
		}
	}
	if !hasNonTest {
		return false
	}
	for _, f := range outputFiles {
		if strings.HasSuffix(f, "_test.go") {
			if _, err := os.Stat(filepath.Join(folderPath, f)); err == nil {
				return true
			}
		}
	}
	return false
}

// loadContextFiles reads all Go source files and go.mod from folderPath
// (non-recursive) and returns them formatted for prompt injection.
// Files listed in outputFiles are included — the model needs to see their
// current state (stubs on attempt 1, partial work on retries).
func loadContextFiles(folderPath string, _ []string) string {
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return ""
	}

	var sb strings.Builder
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") && name != "go.mod" && name != "go.sum" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(folderPath, name))
		if err != nil {
			continue
		}
		sb.WriteString(fmt.Sprintf("### %s\n\n```\n%s\n```\n\n", name, string(content)))
	}
	return strings.TrimSpace(sb.String())
}

// loadPriorAttemptSummary returns the compressed_history text for beadID,
// or "" if none exists yet (first attempt).
func loadPriorAttemptSummary(ctx context.Context, d *db.DB, beadID int64) string {
	var text string
	err := d.QueryRowContext(ctx,
		`SELECT compressed_text FROM compressed_history WHERE bead_id = ?`, beadID,
	).Scan(&text)
	if err != nil {
		return ""
	}
	return text
}

// sameRevisionResumeNote detects the case where this execution reuses a
// bead_revision that an earlier execution for the same bead already ran
// against. Normal retries (execute_revised) always write a fresh revision
// before re-executing, so this only fires after a re_refine cycle: ADJUDICATE
// diagnosed the *test* as broken and left the spec untouched, so the spec may
// still describe output files as unwritten stubs even though a prior attempt
// already wrote a real implementation for it. Returns "" when this is the
// first execution against the revision.
func sameRevisionResumeNote(ctx context.Context, d *db.DB, beadID, revisionID, execID int64) string {
	var priorCount int
	if err := d.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM executions
		WHERE bead_id = ? AND bead_revision_id = ? AND id != ?`,
		beadID, revisionID, execID,
	).Scan(&priorCount); err != nil || priorCount == 0 {
		return ""
	}
	return "This spec was already attempted in a prior execution against the current output files — " +
		"only the test file was revised since then (the implementation was not judged to be the problem). " +
		"Before making any changes, read the current Output Files below: if they already implement this " +
		"spec correctly, make no changes and stop. Only edit them if something is actually wrong. Do not " +
		"treat already-implemented functions as unwritten stubs just because the spec text describes them " +
		"that way."
}

// buildNoWriteWarning returns a user-turn message injected when the model
// declares done without having called write_file at all. This catches the
// "code as prose" failure mode where the model outputs its implementation as
// response text instead of as a write_file tool call.
func buildNoWriteWarning(expectedFiles []string) string {
	fileList := strings.Join(expectedFiles, ", ")
	return fmt.Sprintf(
		"You have not called write_file during this execution. "+
			"Your output file(s) (%s) have not been written to disk.\n\n"+
			"Outputting code as response text does not save it — you MUST call "+
			"write_file with the correct path and your complete implementation as content. "+
			"Call write_file now.",
		fileList,
	)
}

// buildMissingPathWarning returns a user-turn message injected when the model
// calls write_file without a path argument. The generated content is still in
// context; the model only needs to retry the call with an explicit path= argument.
func buildMissingPathWarning(expectedFiles []string) string {
	if len(expectedFiles) == 0 {
		// Defensive: expectedFiles should never be empty here in practice, but
		// indexing expectedFiles[0] below unconditionally would panic and crash
		// the subprocess with no termination_cause written if some future bead
		// shape ever reaches this with none. Degrade to a generic message instead.
		return "Your write_file call was missing the required 'path' argument, so nothing was written to disk.\n\n" +
			"Your generated content is still in context — do NOT regenerate it. " +
			"Call write_file again immediately with an explicit path= argument naming the file you intended to write."
	}
	fileList := strings.Join(expectedFiles, ", ")
	return fmt.Sprintf(
		"Your write_file call was missing the required 'path' argument, so nothing was written to disk.\n\n"+
			"Your generated content is still in context — do NOT regenerate it. "+
			"Call write_file again immediately with an explicit path= argument naming your output file (%s). "+
			"Example: write_file(path=%q, content=\"...\")",
		fileList,
		expectedFiles[0],
	)
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
