package execution

import (
	"context"
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
	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
)

// writeGracePeriod is the extra time given to a model response to complete after
// the execution budget fires. The budget stop is "soft": we let the current
// ChatWithTools call finish so any in-flight write_file arguments can fully
// stream in before we exit. A hard cancel fires after this window regardless.
const writeGracePeriod = 2 * time.Minute

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
	var beadID int64
	var revisionID int64

	if err := d.QueryRowContext(ctx, `
		SELECT e.trace_path, br.execution_budget, br.full_text, vma.model, p.folder_path, e.bead_id, br.id
		FROM executions e
		JOIN bead_revisions br ON br.id = e.bead_revision_id
		JOIN beads b ON b.id = e.bead_id
		JOIN projects p ON p.id = e.project_id
		JOIN verb_model_assignments vma
		  ON vma.project_id = e.project_id AND vma.verb = 'EXECUTE_BEAD'
		WHERE e.id = ?`, execID,
	).Scan(&tracePath, &budget, &beadFullTextJSON, &model, &folderPath, &beadID, &revisionID); err != nil {
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
	// softStopCh is closed when the budget fires, signalling the turn loop to
	// exit cleanly after the current ChatWithTools call finishes. This lets an
	// in-flight write_file argument complete before we stop. A hard cancel fires
	// writeGracePeriod later as a backstop so we never hang indefinitely.
	softStopCh := make(chan struct{})

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
			close(softStopCh)
			time.AfterFunc(writeGracePeriod, func() {
				terminationCh <- "timeout"
				cancel()
			})
		case <-ctx.Done():
		}
	}()

	slog.Info("execute-bead started", "execution_id", execID, "model", model, "budget_s", budget)

	contextFiles := loadContextFiles(folderPath, parsedBead.OutputFiles)
	priorHistory := loadPriorAttemptSummary(ctx, d, beadID)
	resumeNote := sameRevisionResumeNote(ctx, d, beadID, revisionID, execID)

	// Compute the files the model was instructed to write this attempt.
	// test-first mode: tests absent → list only test files.
	// tests-locked mode: tests present, impl absent → list only impl files (tests are certified).
	// normal mode: list all output files.
	testFirst := isTestFirstMode(folderPath, parsedBead.OutputFiles)
	testsLocked := !testFirst && isTestsLockedMode(folderPath, parsedBead.OutputFiles)
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
	messages := []ollama.Message{
		{Role: "system", Content: guidance.InjectForVerbPath(executeBeadSystemPrompt, folderPath, db.VerbExecuteBead, "")},
		{Role: "user", Content: buildBeadUserMsg(parsedBead.FullText, parsedBead.OutputFiles, parsedBead.ExitCriteria, contextFiles, priorHistory, resumeNote, folderPath)},
	}

	var writeFileCount int
	var stubWarningInjected bool
	var missingPathWarningInjected bool

	for turn := 1; ; turn++ {
		writeLine(traceFile, fmt.Sprintf("[TURN %d]", turn))

		msg, err := oc.ChatWithTools(ctx, model, messages, tools, nil, traceFile)
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

		// Budget fired while we were streaming — now that the response is complete,
		// stop cleanly rather than starting another turn.
		select {
		case <-softStopCh:
			writeLine(traceFile, "[terminated: timeout]")
			return writeTerminationCause(d, execID, "timeout")
		default:
		}

		if len(msg.ToolCalls) == 0 {
			// If the model declared done without calling write_file at all, it
			// likely output code as prose instead of as a tool call. Inject a
			// one-time warning and force another turn so it can correct itself
			// without burning a whole attempt slot.
			if !stubWarningInjected && writeFileCount == 0 && len(expectedFiles) > 0 {
				stubWarningInjected = true
				writeLine(traceFile, "[injected: no-write warning — model produced prose instead of calling write_file]")
				messages = append(messages, ollama.Message{
					Role:    "user",
					Content: buildNoWriteWarning(expectedFiles),
				})
				continue
			}
			writeLine(traceFile, "[done — no further tool calls]")
			return writeTerminationCause(d, execID, "success")
		}

		var missingPathDetected bool
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name == "write_file" {
				writeFileCount++
			}
			writeLine(traceFile, fmt.Sprintf("[tool: %s %v]", tc.Function.Name, tc.Function.Arguments))
			result := executeTool(ctx, tc, folderPath)
			writeLine(traceFile, fmt.Sprintf("[result]\n%s", result))
			if tc.Function.Name == "write_file" && strings.Contains(result, "write_file requires a 'path' argument") {
				missingPathDetected = true
			}
			messages = append(messages, ollama.Message{
				Role:    "tool",
				Content: result,
			})
		}

		if missingPathDetected && !missingPathWarningInjected {
			missingPathWarningInjected = true
			writeLine(traceFile, "[injected: missing write_file path — prompting model to retry with explicit path]")
			messages = append(messages, ollama.Message{
				Role:    "user",
				Content: buildMissingPathWarning(expectedFiles),
			})
			continue
		}

		select {
		case cause := <-terminationCh:
			writeLine(traceFile, fmt.Sprintf("[terminated: %s]", cause))
			return writeTerminationCause(d, execID, cause)
		default:
		}
		select {
		case <-softStopCh:
			writeLine(traceFile, "[terminated: timeout]")
			return writeTerminationCause(d, execID, "timeout")
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
