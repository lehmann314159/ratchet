package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/execcheck"
	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
	"ratchet/internal/report"
	"ratchet/internal/splice"
	"ratchet/internal/trace"
)

// consistencyKeywords maps each bead_spec_fit value to keyword sets.
// If the declared value is present but the reasoning contains none of the
// expected keywords (and does contain counterpart keywords), flag inconsistency.
// This catches the Experiment 5 failure: GLM declared "bead_problem" while
// reasoning described "textbook Runner-capability case".
// checkConsistency validates that the declared bead_spec_fit matches the
// reasoning text. The check targets the concrete failure mode from Experiment 5:
// a model declaring "bead_problem" while its own reasoning described the spec
// as clear and unambiguous ("textbook runner-capability case").
//
// Two-signal check per field:
//   - counterpart phrases: reasoning language that directly contradicts the field
//   - exonerating phrases: reasoning that explicitly clears the "accused" party
//
// Either signal alone is sufficient to flag inconsistency. Keyword matching
// is approximate; the store of record is the adjudications table, where
// a human can review trend/bead_spec_fit against reasoning_text directly.
func checkConsistency(fit, reasoning string) (bool, string) {
	lower := strings.ToLower(reasoning)

	switch fit {
	case "bead_problem":
		// Inconsistent: reasoning uses runner/capability language OR
		// explicitly says the spec is NOT the problem.
		contradict := []string{
			"runner-capability", "runner capability",
			"capability problem", "capability case",
			"execution error", "implementation error",
			// Spec-exonerating phrases (Exp-5 pattern: "despite the spec being unambiguous")
			"spec being unambiguous", "spec is clear", "spec is correct",
			"spec is unambiguous", "despite the spec", "unambiguous spec",
			"clear specification", "specification is clear",
		}
		if p, ok := firstUnnegatedMatch(lower, contradict); ok {
			return false, fmt.Sprintf(
				"declared bead_spec_fit=%q but reasoning contains contradicting phrase %q",
				fit, p,
			)
		}

	case "execution_capability_problem":
		// Inconsistent: reasoning blames the spec rather than execution.
		// Note: "bead specification is" (bare) is intentionally absent — it fires
		// on exonerating language ("the bead specification is clear") and produces
		// false positives. Only forms that affirmatively blame the spec are listed.
		contradict := []string{
			"spec problem", "spec is unclear", "spec is ambiguous",
			"specification wrong", "specification is unclear", "specification is ambiguous",
			"bead specification is missing", "bead specification is wrong",
			"bead specification is unclear", "bead specification is ambiguous",
			"bead specification is incorrect",
			"ambiguous requirement", "unclear requirement",
			"missing from the spec", "specification does not",
		}
		if p, ok := firstUnnegatedMatch(lower, contradict); ok {
			return false, fmt.Sprintf(
				"declared bead_spec_fit=%q but reasoning contains contradicting phrase %q",
				fit, p,
			)
		}
	}
	return true, ""
}

// negationCues are words/contractions that, found shortly before a
// contradicting phrase, flip its meaning (e.g. "not a spec problem" is
// consistent with execution_capability_problem, not a contradiction of it).
// Trailing spaces on multi-word cues avoid mid-word false hits (e.g. "not "
// vs. "notable"); the contraction forms need none since they're unambiguous.
var negationCues = []string{
	"not ", "no ", "never ", "no longer ",
	"isn't", "wasn't", "aren't", "weren't",
	"doesn't", "didn't", "don't",
	"can't", "cannot ", "won't", "wouldn't", "shouldn't", "couldn't",
}

// negationWindow is how many characters before a contradicting phrase to
// scan for a negation cue — wide enough for "is not a spec problem" or
// "doesn't look like a spec problem", narrow enough to avoid picking up
// negations from an unrelated, earlier clause.
const negationWindow = 24

// firstUnnegatedMatch returns the first phrase in phrases that appears in
// lower without being immediately preceded by a negation cue, and whether
// one was found.
func firstUnnegatedMatch(lower string, phrases []string) (string, bool) {
	for _, p := range phrases {
		idx := strings.Index(lower, p)
		if idx == -1 {
			continue
		}
		start := idx - negationWindow
		if start < 0 {
			start = 0
		}
		if containsNegationCue(lower[start:idx]) {
			continue
		}
		return p, true
	}
	return "", false
}

func containsNegationCue(window string) bool {
	for _, cue := range negationCues {
		if strings.Contains(window, cue) {
			return true
		}
	}
	return false
}

// vacuousPassNote returns a non-empty structural note to inject into the
// mechanical findings when the vacuous test pass is Type B (inherent) — the
// bead's output_files contain no *_test.go, so the test named in the exit
// criterion was never part of this bead's deliverable.
//
// Type A (test file IS in output_files but tests didn't run) returns "" — the
// standard vacuous-pass principle in the ADJUDICATE prompt applies there.
func vacuousPassNote(bead *beadState, mechanicalFindings string) string {
	hasTestCriterion := false
	for _, c := range bead.ExitCriteria {
		if strings.Contains(c, "go test") {
			hasTestCriterion = true
			break
		}
	}
	if !hasTestCriterion {
		return ""
	}
	lower := strings.ToLower(mechanicalFindings)
	isVacuous := strings.Contains(lower, "no tests to run") ||
		strings.Contains(lower, "[no test files]") ||
		strings.Contains(lower, "no test files")
	if !isVacuous {
		return ""
	}
	if hasTestGoFile(bead.OutputFiles) {
		return "" // Type A — test file was in scope; standard rule applies
	}
	return "[Structural note: Type B vacuous pass] This bead's output_files contain no " +
		"*_test.go file, so the test named in the exit criterion is outside this bead's " +
		"scope. The vacuous-pass rule does not block declare_success here. Evaluate only " +
		"whether the non-test output files listed in output_files were correctly written " +
		"(file exists, content is correct for the bead's stated purpose)."
}

// orientationOnlyNote detects the pattern where the latest execution ended with
// no write_file calls at all — the agent spent its entire budget on read-only
// orientation commands and never began writing. Covers both timeout and
// monitor_terminated termination causes (MONITOR fires after 10+ turns with no
// write_file, producing the same orientation-only pattern as a timeout).
// Returns a note to inject into mechanical findings so ADJUDICATE can apply the
// orientation-only fast path without having to infer the pattern from field names
// that do not appear in the mechanical findings output.
// Not emitted for REFINE_TESTS beads — for those, the [REFINE_TESTS bead] note
// already covers the case and re_refine is the appropriate path when tests repeat.
func orientationOnlyNote(ctx context.Context, d *db.DB, beadID int64) string {
	if beadHasRefinements(ctx, d, beadID) {
		return ""
	}
	var tracePath, terminationCause string
	err := d.QueryRowContext(ctx, `
		SELECT trace_path, termination_cause FROM executions
		WHERE bead_id = ? ORDER BY id DESC LIMIT 1`, beadID).Scan(&tracePath, &terminationCause)
	if err != nil {
		return ""
	}
	if terminationCause != "timeout" && terminationCause != "monitor_terminated" {
		return ""
	}
	data, err := os.ReadFile(tracePath)
	if err != nil {
		return ""
	}
	pt := trace.Parse(data)
	if len(pt.WriteFiles) > 0 {
		return "" // agent made at least one write attempt — not orientation-only
	}
	return "[Fast path — orientation only] The previous attempt ran only read-only commands " +
		"(ls, read_file, etc.) and made no write_file calls before terminating. The agent did " +
		"not begin the task. The content of the bead spec is not the problem.\n\n" +
		"Action: issue execute_revised immediately. Set trend=same, " +
		"bead_spec_fit=execution_capability_problem, execution_budget doubled. Prepend exactly " +
		"one sentence to the existing full_text: \"Begin writing to output_files immediately; " +
		"do not re-run ls or other orientation commands before starting implementation.\" " +
		"If that sentence is already present, do not prepend it again. Make no other changes to the spec."
}

// countTrailingTimeouts returns how many of the most recent consecutive
// executions for beadID (current lineage, real attempts only) ended with
// termination_cause='timeout'. monitor_terminated is deliberately excluded:
// that is MONITOR stopping a misbehaving agent, where more wall-clock makes
// things worse, not a steady generator that ran out of time.
func countTrailingTimeouts(ctx context.Context, d *db.DB, beadID int64) int {
	lineageIDs, err := currentLineageRevisionIDs(ctx, d, beadID)
	if err != nil {
		return 0
	}
	rows, err := d.QueryContext(ctx, `
		SELECT termination_cause, bead_revision_id
		FROM executions
		WHERE bead_id = ? AND infra_failure = 0 AND test_first_attempt = 0
		  AND termination_cause IS NOT NULL
		ORDER BY id DESC`, beadID)
	if err != nil {
		return 0
	}
	defer rows.Close()
	run := 0
	for rows.Next() {
		var cause string
		var revID int64
		if err := rows.Scan(&cause, &revID); err != nil {
			return run
		}
		if !lineageIDs[revID] {
			continue // pre-rewind attempt — not part of the current lineage
		}
		if cause != "timeout" {
			break
		}
		run++
	}
	return run
}

// enforcedTimeoutBudget is the execution_budget a retry gets after a run of
// >=2 consecutive timeouts: double the just-executed revision's value, capped
// at 8x the project default (900s default -> 7200s ceiling). Compounds
// naturally across attempts because priorBudget is itself the last enforced
// value.
func enforcedTimeoutBudget(priorBudget, budgetDefault int) int {
	enforced := priorBudget * 2
	if ceiling := budgetDefault * 8; enforced > ceiling {
		enforced = ceiling
	}
	return enforced
}

// recurringTimeoutNote fires when the last >=2 in-lineage executions all ended
// termination_cause='timeout'. Unlike orientationOnlyNote it is NOT suppressed
// for REFINE_TESTS beads — a single-turn timeout is orthogonal to whether the
// locked tests are correct, and re_refine is never the right answer for a run
// that never reached the tests. It tells ADJUDICATE the budget will be raised
// mechanically in Commit no matter what it returns, so the revision should go
// toward making the implementation reachable within a turn rather than toward
// spec prose or re_refine.
func recurringTimeoutNote(run, currentBudget, enforcedBudget int) string {
	if run < 2 {
		return ""
	}
	return fmt.Sprintf("[Fast path — repeated timeout] The last %d execution attempts all ended in "+
		"termination_cause=timeout. The agent is generating output but cannot reach a terminal state "+
		"(a completed write_file, a test run) within the %ds budget — this is a wall-clock limit, not "+
		"a spec defect and not a test defect.\n\n"+
		"Action: issue execute_revised with trend=same, bead_spec_fit=execution_capability_problem. "+
		"The orchestrator will set execution_budget to %ds mechanically regardless of what you put in "+
		"revised_bead — do not spend the revision on the budget number, and do NOT choose re_refine "+
		"(the tests were never reached). Prepend exactly one sentence to the existing full_text telling "+
		"the agent to write each output file with a minimal compiling skeleton FIRST and flesh it out "+
		"in later turns, rather than composing the whole file before the first write_file call. If that "+
		"sentence is already present, do not add it again. Make no other changes to the spec.",
		run, currentBudget, enforcedBudget)
}

// partialProgressNote checks whether some (but not all) output_files for the
// bead already exist on disk. When partial state is present, ADJUDICATE must
// know which files are done and which remain — otherwise it misreads the
// attempt as "no progress" and gives contradictory orientation instructions.
func partialProgressNote(folderPath string, outputFiles []string) string {
	if len(outputFiles) == 0 {
		return ""
	}
	type fileStatus struct {
		name string
		size int64
	}
	var present, absent []fileStatus
	for _, f := range outputFiles {
		info, err := os.Stat(filepath.Join(folderPath, f))
		if err == nil {
			present = append(present, fileStatus{f, info.Size()})
		} else {
			absent = append(absent, fileStatus{f, 0})
		}
	}
	if len(present) == 0 || len(absent) == 0 {
		return "" // all present (success path) or all absent (normal start)
	}
	var parts []string
	for _, f := range present {
		parts = append(parts, fmt.Sprintf("%s present (%d bytes)", f.name, f.size))
	}
	for _, f := range absent {
		parts = append(parts, fmt.Sprintf("%s not yet written", f.name))
	}
	return "[Partial progress] Some output_files already exist on disk: " +
		strings.Join(parts, "; ") + ". Do NOT rewrite files that are already present " +
		"and passing — focus only on the missing files listed above."
}

// stubImplNote fires when all output files are present on disk but some
// non-test Go functions within them have zero-value stub bodies (e.g. return nil).
// This catches the case where a prior attempt wrote a partial implementation —
// NewGame correct, ApplyMove returning nil — and ADJUDICATE needs to direct the
// next attempt to fill in only the stubs rather than rewrite from scratch.
func stubImplNote(folderPath string, outputFiles []string) string {
	var stubs []string
	for _, f := range outputFiles {
		if !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") {
			continue
		}
		stubs = append(stubs, detectStubFuncs(filepath.Join(folderPath, f), f)...)
	}
	if len(stubs) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"[Stub implementation] The following functions have zero-value stub bodies and are not yet implemented: %s. "+
			"The surrounding code in these files is likely correct. "+
			"ADJUDICATE should instruct the executor to: read the file(s) first, then overwrite with "+
			"a single write_file call that keeps all existing correct implementations and fills in "+
			"only the stub function bodies listed above.",
		strings.Join(stubs, "; "))
}

// detectStubFuncs parses a Go source file and returns names of functions whose
// bodies consist of a single zero-value return statement.
func detectStubFuncs(path, basename string) []string {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil
	}
	var names []string
	for _, decl := range node.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		if fd.Type.Results == nil || fd.Type.Results.NumFields() == 0 {
			continue
		}
		if len(fd.Body.List) == 1 {
			ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
			if ok && isZeroValueReturn(ret) {
				names = append(names, fmt.Sprintf("%s (in %s)", fd.Name.Name, basename))
			}
		}
	}
	return names
}

func isZeroValueReturn(ret *ast.ReturnStmt) bool {
	for _, r := range ret.Results {
		if !isZeroValueExpr(r) {
			return false
		}
	}
	return true
}

func isZeroValueExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "nil" || e.Name == "false"
	case *ast.BasicLit:
		switch e.Kind {
		case token.INT:
			return e.Value == "0"
		case token.FLOAT:
			f, err := strconv.ParseFloat(e.Value, 64)
			return err == nil && f == 0
		case token.STRING:
			return e.Value == `""` || e.Value == "``"
		}
	case *ast.CompositeLit:
		return len(e.Elts) == 0
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			cl, ok := e.X.(*ast.CompositeLit)
			return ok && len(cl.Elts) == 0
		}
	}
	return false
}

// testFirstCompleteNote fires when the bead has *_test.go files that exist on
// disk but non-test implementation files are absent — i.e., the previous
// attempt was a test-first attempt that wrote only test files.
// Returns a note to inject into mechanical findings so ADJUDICATE knows to
// always emit execute_revised, lock the test files, and direct the next
// attempt to write only the implementation.
func testFirstCompleteNote(folderPath string, outputFiles []string) string {
	var testFiles, implFiles []string
	for _, f := range outputFiles {
		_, err := os.Stat(filepath.Join(folderPath, f))
		if strings.HasSuffix(f, "_test.go") {
			if err == nil {
				testFiles = append(testFiles, f)
			}
		} else if strings.HasSuffix(f, ".go") {
			if err != nil {
				implFiles = append(implFiles, f)
			}
		}
	}
	if len(testFiles) == 0 || len(implFiles) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"[Test-first complete] Test files were written in the previous (test-first) attempt: %s. "+
			"Implementation files are absent: %s.\n\n"+
			"ADJUDICATE INSTRUCTIONS — this note requires specific handling:\n"+
			"1. If the mechanical findings above contain \"[Test-first verification]\" MISMATCH entries: "+
			"Decision MUST be test_reject. Set test_rejection_guidance to a bulleted list of corrections "+
			"(test function name, wrong value → correct value, cite the spec or convention that proves it). "+
			"Do NOT issue execute_revised when MISMATCH entries are present.\n"+
			"2. If there are NO MISMATCH entries (all MATCH or no verification output): "+
			"Decision MUST be execute_revised — tests written but implementation absent; never execute_as_is or declare_success. "+
			"The revised full_text MUST state: \"The test file(s) %s are LOCKED — do NOT modify them. "+
			"Write ONLY the implementation file(s): %s.\"",
		strings.Join(testFiles, ", "), strings.Join(implFiles, ", "),
		strings.Join(testFiles, ", "), strings.Join(implFiles, ", "),
	)
}

// missingPathNote detects the pattern where the latest execution ended with a
// write_file call that omitted the path argument. The model generated correct
// content but the file was never written. Returns a note to inject into
// mechanical findings so ADJUDICATE can apply the fast path.
func missingPathNote(ctx context.Context, d *db.DB, beadID int64) string {
	var tracePath string
	err := d.QueryRowContext(ctx, `
		SELECT trace_path FROM executions
		WHERE bead_id = ? ORDER BY id DESC LIMIT 1`, beadID).Scan(&tracePath)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(tracePath)
	if err != nil {
		return ""
	}
	pt := trace.Parse(data)
	if len(pt.WriteFiles) == 0 {
		return ""
	}
	// Any successful write means the file landed — not this failure mode.
	for _, wf := range pt.WriteFiles {
		if wf.Succeeded {
			return ""
		}
	}
	// Last write_file had no path argument.
	if pt.WriteFiles[len(pt.WriteFiles)-1].Path != "" {
		return ""
	}
	return "[Fast path — missing write_file path] The previous attempt generated correct " +
		"content but called write_file without a path argument. No file was written. " +
		"The content itself is not the problem.\n\n" +
		"Action: issue execute_revised immediately. Set trend=same, " +
		"bead_spec_fit=execution_capability_problem, same execution_budget. Prepend exactly " +
		"one sentence to the existing full_text: \"Your previous attempt generated correct " +
		"content but called write_file without a path argument — begin immediately by calling " +
		"write_file with an explicit path= argument naming the output file; do not re-read " +
		"files or regenerate content from scratch.\" Make no other changes to the spec."
}

// reFailedTestName matches "--- FAIL: <name>" lines from `go test` output,
// which includes the "/" for subtests (e.g. "TestPlaceStone/KoCreation").
var reFailedTestName = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)

// Go-specific: parses `go test -v` output text directly (the "=== RUN"/
// "--- FAIL:" convention below). When ratchet supports additional languages,
// this and recurringTestFailureNote's regex-based parsing will need a
// per-language equivalent (pytest, cargo test, jest, ... each have their own
// failure-output format) — revisit consolidating this into a generic
// mechanical-checks section at that point rather than duplicating per language.

// extractTestOutput returns the free-text output (t.Errorf/t.Log lines) a
// specific named (sub)test printed between its "=== RUN   <name>" line and
// the next test boundary in `go test -v` output. Empty if not found.
//
// The boundary markers tolerate leading whitespace on their line (\n\s*
// rather than a bare \n) because the "stdout:" block ANALYZE_EXECUTION
// captures gets a uniform indent applied to every line when embedded under
// "Turn N: ... stdout:" (confirmed against real captured findings — actual
// `go test -v` output never indents "=== RUN", only nested "--- PASS/FAIL"
// summary lines do). Without this, the boundary never matches, the capture
// runs on past the intended stop point into unrelated trailing content
// (turn numbers, elapsed times, full file dumps) that differs between
// attempts by chance, and two attempts with an identical failure line never
// compare byte-identical — silently defeating recurringTestFailureNote's
// "identical" tier (and therefore the mechanical re_refine override that
// depends on it) exactly the way the 2026-07-20 exprvm-web-v1 bead 33
// incident's second escalation showed: the failure line was in fact
// identical across two attempts, but this bug made the comparison see two
// different strings. reFailedTestName already tolerates this correctly
// (`^\s*--- FAIL:`); this brings extractTestOutput in line with it.
func extractTestOutput(findings, testName string) string {
	pattern := `(?s)=== RUN\s+` + regexp.QuoteMeta(testName) + `\s*\n(.*?)(?:\n\s*=== RUN|\n\s*--- (?:FAIL|PASS):|\n\s*PASS\n|\n\s*FAIL\n|\z)`
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ""
	}
	m := re.FindStringSubmatch(findings)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// latestExecutionPassed reports whether the single most recent qualifying
// execution (infra_failure=0, test_first_attempt=0) for beadID actually ran
// its tests and passed: zero "--- FAIL:" lines AND at least one "--- PASS:"
// line in its captured findings. Requiring an explicit PASS (not just the
// absence of FAIL) avoids a false positive on an attempt that got killed
// before it ever ran the test command at all — that has zero FAIL lines too,
// but is "no data," not "passing." Returns false (safe default: proceed
// with the recurrence check) if there is no qualifying execution yet, or
// its data can't be read.
func latestExecutionPassed(ctx context.Context, d *db.DB, beadID int64) bool {
	var findings string
	err := d.QueryRowContext(ctx, `
		SELECT a.mechanical_findings
		FROM executions e
		JOIN analyses a ON a.execution_id = e.id
		WHERE e.bead_id = ? AND e.infra_failure = 0 AND e.test_first_attempt = 0
		ORDER BY e.id DESC LIMIT 1`, beadID,
	).Scan(&findings)
	if err != nil {
		return false
	}
	if len(reFailedTestName.FindAllStringSubmatch(findings, -1)) > 0 {
		return false
	}
	return rePassedTestName.MatchString(findings)
}

// rePassedTestName matches "--- PASS: <name>" lines, tolerating the same
// leading whitespace reFailedTestName does (see extractTestOutput's doc
// comment for why real captured findings text is indented).
var rePassedTestName = regexp.MustCompile(`(?m)^\s*--- PASS:`)

// recurringTestFailureNote detects the pattern behind the [REFINE_TESTS bead]
// guidance below: the same named subtest failing identically across the last
// two attempts that actually revised the implementation. That guidance was
// prose-only and ADJUDICATE was not reliably acting on it — it kept revising
// the bead spec instead of recognizing a test the implementation cannot
// satisfy. This makes the "2 or more identical failures" threshold mechanical
// rather than left to the model's judgment.
//
// Only counts executions where a non-test output file was actually written
// (excludes orientation-only or compile-failure attempts, which produce no
// meaningful --- FAIL lines or share stub-vs-stub failures that aren't
// evidence the test itself is broken).
//
// A shared failing name alone is ambiguous — it's equally consistent with
// "the implementation keeps making the same mistake" and "the test can't be
// satisfied." When the two attempts' captured failure *text* is also
// byte-identical despite genuinely different implementation code, that's much
// stronger: two independently regenerated implementations converging on the
// exact same output is far more likely to mean the code is correct and the
// test's expectation is wrong, than that both attempts made the identical
// mistake. That case gets its own, more assertive note instead of leaving the
// distinction to the model's judgment.
// recurringTestFailureNote's return also surfaces the "identical" tier by
// name/text so the caller can enforce it mechanically (see the
// forcedReRefine* fields on AdjudicateNextExecution and its use in
// Validate) instead of leaving it as prose the model can talk itself out of
// — see the 2026-07-20 exprvm-web-v1 bead 33 incident referenced below.
//
// Guards on latestExecutionPassed first: a second, separate exprvm-web-v1
// bead 33 incident (same day) showed this function's "last 2 attempts"
// window walks backward through whichever executions qualify, with no check
// on whether a *more recent* execution already passed. It paired two stale,
// pre-fix failing attempts (from before REFINE_TESTS_WRITE's next cycle
// rewrote the test correctly) and forced re_refine on that basis, even
// though the actual latest execution — one the window's own age-based walk
// had already stepped past — succeeded outright. A pass supersedes any
// prior failure pattern; if the bead is currently passing there is no
// "recurring failure" to explain, regardless of history.
func recurringTestFailureNote(ctx context.Context, d *db.DB, beadID int64) (note string, identicalNames []string, identicalText map[string]string) {
	if latestExecutionPassed(ctx, d, beadID) {
		return "", nil, nil
	}
	rows, err := d.QueryContext(ctx, `
		SELECT e.trace_path, a.mechanical_findings
		FROM executions e
		JOIN analyses a ON a.execution_id = e.id
		WHERE e.bead_id = ? AND e.infra_failure = 0 AND e.test_first_attempt = 0
		ORDER BY e.id DESC LIMIT 5`, beadID)
	if err != nil {
		return "", nil, nil
	}
	defer rows.Close()

	var failNames []map[string]bool
	var findingsByAttempt []string
	for rows.Next() && len(failNames) < 2 {
		var tracePath, findings string
		if err := rows.Scan(&tracePath, &findings); err != nil {
			return "", nil, nil
		}
		data, err := os.ReadFile(tracePath)
		if err != nil {
			continue
		}
		pt := trace.Parse(data)
		wroteImpl := false
		for _, wf := range pt.WriteFiles {
			if wf.Succeeded && !strings.HasSuffix(wf.Path, "_test.go") {
				wroteImpl = true
				break
			}
		}
		if !wroteImpl {
			continue // orientation-only attempt; not evidence about the test
		}
		names := map[string]bool{}
		for _, m := range reFailedTestName.FindAllStringSubmatch(findings, -1) {
			names[m[1]] = true
		}
		failNames = append(failNames, names)
		findingsByAttempt = append(findingsByAttempt, findings)
	}
	if err := rows.Err(); err != nil || len(failNames) < 2 {
		return "", nil, nil
	}

	var shared []string
	for name := range failNames[0] {
		if strings.Contains(name, "/") && failNames[1][name] {
			shared = append(shared, name)
		}
	}
	if len(shared) == 0 {
		return "", nil, nil
	}
	sort.Strings(shared)

	// Split shared failures by whether the captured failure text is
	// byte-identical across both attempts (strong signal) or merely the same
	// subtest name with different content (weaker — still worth a note, but
	// less conclusive).
	var identical, differing []string
	identicalText = map[string]string{}
	for _, name := range shared {
		latest := extractTestOutput(findingsByAttempt[0], name)
		prior := extractTestOutput(findingsByAttempt[1], name)
		if latest != "" && latest == prior {
			identical = append(identical, name)
			identicalText[name] = latest
		} else {
			differing = append(differing, name)
		}
	}

	var b strings.Builder
	if len(identical) > 0 {
		b.WriteString("[Recurring test failure — identical output] The following subtest(s) " +
			"produced byte-identical failure output across the last two attempts that each " +
			"genuinely rewrote the implementation:\n\n")
		for _, name := range identical {
			fmt.Fprintf(&b, "  %s:\n    %s\n\n", name, identicalText[name])
		}
		b.WriteString("Two independently regenerated implementations converging on the exact " +
			"same output is strong mechanical evidence the code is correct and the test's own " +
			"expected value is wrong — not that the model keeps repeating the same mistake. " +
			"Re-derive by hand, from the test's own setup, what a correct implementation must " +
			"return; if it matches the actual output shown above, the test's assertion is the " +
			"defect. Default to decision=re_refine for these subtests unless you can point to a " +
			"specific implementation change that would produce a *different* result — not just " +
			"restate the existing spec language, since that has already been tried and produced " +
			"this exact output twice.\n\n")
	}
	if len(differing) > 0 {
		b.WriteString("[Recurring test failure] The following subtest(s) failed across the last " +
			"two attempts that revised the implementation, though with different failure output " +
			"each time: " + strings.Join(differing, ", ") + ". Revising the bead spec's " +
			"implementation prose alone has not resolved this so far — treat that as a strong " +
			"signal, not proof the test itself is at fault.\n\n" +
			"Before choosing a decision: check whether the failure looks like an implementation " +
			"defect (a crash, a runtime/template error, a wrong computed value traceable to a " +
			"specific, nameable logic bug) rather than a genuinely unsatisfiable assertion — a " +
			"recurring failure can mean \"the implementation keeps making the same mistake\" just " +
			"as easily as \"the test is wrong.\" Only use decision=re_refine if you can state, for " +
			"each listed subtest, why its assertion cannot be satisfied by any correct " +
			"implementation given how the test sets up its inputs — then explain that in " +
			"re_refine_guidance. If instead you can name a specific, untried implementation change " +
			"that would satisfy the assertion, use execute_revised and describe that change.")
	}

	// Only the identical tier is eligible for mechanical enforcement (see
	// Validate), and only when the actual generated implementation differs
	// between the two attempts. Byte-identical test failure text from
	// byte-identical (or near-identical) implementation code is exactly the
	// case a prior audit finding walked back an unconditional "issue
	// decision=re_refine" command for — a real, unfixed implementation bug the
	// model keeps regenerating unchanged looks identical to an unsatisfiable
	// assertion at this signal alone. Requiring the code to have actually
	// changed while the failure stayed identical is what makes it safe to
	// enforce: two *different* implementations converging on the same result.
	if len(identical) > 0 && implementationChangedBetweenAttempts(findingsByAttempt[0], findingsByAttempt[1]) {
		return strings.TrimSpace(b.String()), identical, identicalText
	}
	return strings.TrimSpace(b.String()), nil, nil
}

// reOutputFileBlock matches one checkOutputFiles-produced entry: a relative
// path, its "present (N bytes...)" status line, and the fenced code block
// holding its full content. Only present when the file is under
// maxInlineFileBytes — an omitted-content file yields no match here, which
// implementationChangedBetweenAttempts treats as unverifiable (see its
// doc comment).
var reOutputFileBlock = regexp.MustCompile("(?ms)^(\\S+): present \\(\\d+ bytes(?:, \\d+ test function\\(s\\))?\\)\\n```\\n(.*?)\\n```$")

// nonTestImplementationBlocks extracts path -> full file content for every
// non-test output file checkOutputFiles inlined into an ANALYZE_EXECUTION
// mechanical_findings blob.
func nonTestImplementationBlocks(findings string) map[string]string {
	blocks := map[string]string{}
	for _, m := range reOutputFileBlock.FindAllStringSubmatch(findings, -1) {
		if strings.HasSuffix(m[1], "_test.go") {
			continue
		}
		blocks[m[1]] = m[2]
	}
	return blocks
}

// implementationChangedBetweenAttempts reports whether any non-test output
// file's actual content differs between two attempts' mechanical findings.
// Returns false — the safe default — when either side yields no extractable
// implementation blocks at all (e.g. a file exceeded maxInlineFileBytes, or
// output_files changed shape between attempts): an unverifiable comparison
// must never be treated as "confirmed different," since that's the signal
// that gates mechanical enforcement in recurringTestFailureNote.
func implementationChangedBetweenAttempts(findingsA, findingsB string) bool {
	blocksA := nonTestImplementationBlocks(findingsA)
	blocksB := nonTestImplementationBlocks(findingsB)
	if len(blocksA) == 0 || len(blocksB) == 0 {
		return false
	}
	if len(blocksA) != len(blocksB) {
		return true
	}
	for path, contentA := range blocksA {
		if contentB, ok := blocksB[path]; !ok || contentA != contentB {
			return true
		}
	}
	return false
}

// --- re_refine scope probe ---
//
// When ADJUDICATE routes a REFINE_TESTS failure to decision=re_refine, the fix
// it prescribes must be one REFINE_TESTS can actually make: an edit confined to
// a *_test.go file. A conflict between two non-test source files — one file
// declaring `var t *text/template.Template`, another a
// `func InitT() *html/template.Template` assigned to it — type-checks nowhere
// and no re_refine can fix it, yet REFINE_TESTS will grind an entire cycle on
// it before its compile gate escalates (exprvm-web bead 246, 2026-09-01).
// ADJUDICATE's run_go_snippet check can't catch this: it's stdlib-only and
// standalone, with no access to the project package. probeReRefineEdit applies
// the prescribed test-level change as a throwaway probe test and compiles the
// real package; a type-conflict-class failure is mechanical proof the fix lives
// outside re_refine's scope and the decision should have been execute_revised.

// reGuidanceStmt recognizes a line/span that reads as a Go statement
// (assignment or call) rather than a declaration or prose.
var reGuidanceStmt = regexp.MustCompile(`^[\w.]+\s*(:?=[^=]|\()`)

var reInlineCode = regexp.MustCompile("`([^`]+)`")

// reCrossFileTypeConflict matches the go/types error families that mean two
// declarations are structurally incompatible — the signature of a defect no
// *_test.go edit can resolve. Deliberately excludes `undefined:` and syntax
// errors: those usually mean the probe simply lacks a helper the real test file
// defines, or the extracted fragment was imperfect, not that the prescribed fix
// is genuinely out of scope.
var reCrossFileTypeConflict = regexp.MustCompile(
	`cannot use .+ as .+ value|mismatched types|does not implement |cannot convert |` +
		`redeclared |previous declaration|ambiguous selector|incompatible type`)

// extractGoStatements pulls candidate Go statements out of free-text
// re_refine_guidance: lines inside fenced code blocks and inline `backtick`
// spans that read as a statement rather than a declaration. Capped and
// deduped; an empty result means the guidance named no concrete edit to probe.
func extractGoStatements(guidance string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ";"))
		if s == "" || len(s) > 200 || seen[s] || !reGuidanceStmt.MatchString(s) {
			return
		}
		for _, p := range []string{"func ", "import ", "package ", "//", "type ", "return ", "var ", "const "} {
			if strings.HasPrefix(s, p) {
				return
			}
		}
		seen[s] = true
		out = append(out, s)
	}
	inFence := false
	for _, line := range strings.Split(guidance, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			add(t)
			continue
		}
		for _, m := range reInlineCode.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
	}
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// probeReRefineEdit returns the failing compile output when the statement(s)
// ADJUDICATE prescribed in re_refine_guidance fail to type-check against the
// bead's real package with a cross-file type conflict; "" otherwise (clean
// compile, nothing concrete to probe, or an unrelated error kind).
//
// The finding is trusted only when the package compiles cleanly WITHOUT the
// probe and breaks WITH it: that isolates the prescribed edit as the cause. If
// the tree already fails to compile (a pre-existing broken test file is the
// norm for a REFINE_TESTS bead at ADJUDICATE time), the probe can't attribute
// anything, so it stays out of the way and lets the model's judgment plus the
// recurring-failure machinery decide.
func probeReRefineEdit(ctx context.Context, folderPath, guidance string) string {
	stmts := extractGoStatements(guidance)
	if len(stmts) == 0 || folderPath == "" {
		return ""
	}
	probePath := filepath.Join(folderPath, "zz_ratchet_adjudicate_probe_test.go")
	_ = os.Remove(probePath) // clear any stale probe from an interrupted run

	if baseOK, _ := runCompile(ctx, folderPath); !baseOK {
		return "" // can't attribute a failure to the probe if the tree is already broken
	}

	src := "package " + splice.DetectPackage(folderPath) + "\n\nimport \"testing\"\n\n" +
		"func TestRatchetAdjudicateReRefineProbe(t *testing.T) {\n\t_ = t\n\t" +
		strings.Join(stmts, "\n\t") + "\n}\n"
	if os.WriteFile(probePath, []byte(src), 0o644) != nil {
		return ""
	}
	defer os.Remove(probePath)
	ok, compileOut := runCompile(ctx, folderPath)
	if ok || !reCrossFileTypeConflict.MatchString(compileOut) {
		return ""
	}
	return compileOut
}

type AdjudicateNextExecution struct {
	budgetDefault int    // cached from Run for use in Commit
	folderPath    string // cached from Run for use in Commit

	// reRefineProbeFailed caches probeReRefineEdit's compile output when the
	// re_refine fix the model prescribed does not type-check against the real
	// package (a cross-file conflict no *_test.go edit can resolve). Set in Run
	// after its in-loop nudge; consumed by Validate as a backstop that forces
	// execute_revised if the model insisted on re_refine anyway.
	reRefineProbeFailed string

	// forcedReRefineNames/Text cache recurringTestFailureNote's "identical" tier
	// (byte-identical failure across the last 2 genuinely-rewritten attempts) so
	// Validate can enforce decision=re_refine mechanically rather than merely
	// suggesting it in the prompt. See the 2026-07-20 exprvm-web-v1 bead 33
	// incident: ADJUDICATE saw this exact evidence across 3+ rounds and instead
	// of recognizing the test's assertion was unsatisfiable (html/template
	// correctly escaping '+' to '&#43;', which the test's literal substring
	// check could never match), it invented and progressively re-asserted a
	// fabricated code-level defect (claiming the model wrote "</div" instead of
	// "</div>" — textually identical strings in its own reasoning) that wasn't
	// grounded in the actual generated source at all. Once that false claim
	// entered the bead's revision log via a prior execute_revised, it kept
	// resurfacing and getting more confident each round, overriding the
	// "default to re_refine" prompt guidance every time. Prompt wording alone
	// is not a reliable enforcement mechanism here — prefer the mechanical
	// check already computed above it.
	forcedReRefineNames []string
	forcedReRefineText  map[string]string

	// currentBeadSpec caches the pre-revision bead spec (from Run) so Commit's
	// execute_revised path can diff a revised_bead against it — checking the
	// revised spec is internally consistent and that ADJUDICATE didn't invent a
	// new required test function (which is re_refine's job, not execute_revised's).
	currentBeadSpec ParsedBead

	// trailingTimeouts caches, from Run, how many of the most recent
	// consecutive in-lineage executions ended termination_cause='timeout'. A run
	// of >=2 means wall-clock, not the spec, is the bottleneck; Commit's retry
	// paths then force execution_budget up regardless of the value the model
	// returned (recurringTimeoutNote warns the model this is coming). Born from
	// the exprvm-web-baseline-6 bead 269 incident: gemma4:31b streamed a whole
	// recursive-descent parser in one turn, got killed before the first
	// write_file across 3 attempts, and ADJUDICATE kept reading "stub file +
	// panicking test" as execution_capability_problem and rewriting spec prose
	// while leaving the budget at 900. orientationOnlyNote's budget-double fast
	// path is suppressed for REFINE_TESTS beads, so this one had no timeout
	// handling at all — hence a mechanical enforcement independent of that note.
	trailingTimeouts int
}

func (h *AdjudicateNextExecution) Verb() string { return db.VerbAdjudicateNextExecution }

func (h *AdjudicateNextExecution) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	if !job.BeadID.Valid {
		return "", fmt.Errorf("%s job %d has no bead_id", db.VerbAdjudicateNextExecution, job.ID)
	}
	beadID := job.BeadID.Int64

	// Input 1: current Bead state.
	beads, err := loadCurrentBeads(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	var currentBead *beadState
	for i := range beads {
		if beads[i].BeadID == beadID {
			currentBead = &beads[i]
			break
		}
	}
	if currentBead == nil {
		return "", fmt.Errorf("bead %d not found in project %d", beadID, job.ProjectID)
	}
	h.currentBeadSpec = ParsedBead{
		Title: currentBead.Title, FullText: currentBead.FullText,
		OutputFiles: currentBead.OutputFiles, ExitCriteria: currentBead.ExitCriteria,
		ExecutionBudget: currentBead.ExecutionBudget,
	}

	// Input 2: revision log.
	revLog, err := loadBeadRevisionLog(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	// Input 3: latest ANALYZE_EXECUTION mechanical_findings (not interpretation).
	analysis, err := loadLatestAnalysis(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	// Input 4: COMPRESS_ANALYSIS compressed history.
	compressedHistory, err := loadCompressedHistory(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	// Compute the diff-signal: which failure categories each revision targeted
	// and the last two executions' termination causes.
	diffSignal, err := buildDiffSignal(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	project, err := loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	h.budgetDefault = project.ExecutionBudgetDefault
	h.folderPath = project.FolderPath
	h.trailingTimeouts = countTrailingTimeouts(ctx, d, beadID)

	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbAdjudicateNextExecution)
	if err != nil {
		return "", err
	}

	findings := analysis.MechanicalFindings
	// Suppress test-first machinery for beads that went through REFINE_TESTS —
	// tests are pre-certified and "tests present + impl absent" is normal state.
	if !beadHasRefinements(ctx, d, beadID) {
		if note := testFirstCompleteNote(h.folderPath, currentBead.OutputFiles); note != "" {
			findings += "\n\n" + note
		}
	}
	if note := vacuousPassNote(currentBead, findings); note != "" {
		findings += "\n\n" + note
	}
	if note := partialProgressNote(h.folderPath, currentBead.OutputFiles); note != "" {
		findings += "\n\n" + note
	}
	if note := stubImplNote(h.folderPath, currentBead.OutputFiles); note != "" {
		findings += "\n\n" + note
	}
	if note := orientationOnlyNote(ctx, d, beadID); note != "" {
		findings += "\n\n" + note
	}
	if h.trailingTimeouts >= 2 {
		findings += "\n\n" + recurringTimeoutNote(h.trailingTimeouts, h.currentBeadSpec.ExecutionBudget,
			enforcedTimeoutBudget(h.currentBeadSpec.ExecutionBudget, h.budgetDefault))
	}
	if note := missingPathNote(ctx, d, beadID); note != "" {
		findings += "\n\n" + note
	}
	if beadHasRefinements(ctx, d, beadID) {
		findings += "\n\n[REFINE_TESTS bead] This bead's tests were written by REFINE_TESTS and " +
			"are LOCKED during EXECUTE_BEAD: the execution agent cannot create, modify, or add " +
			"setup to any *_test.go file, and an execute_revised spec cannot direct it to. " +
			"Route the fix by where it actually lives:\n" +
			"  - The fix needs a TEST-FILE change — a wrong expected value, missing or " +
			"inconsistent setup (e.g. one test initializes a package-level variable the code " +
			"under test dereferences and a sibling test in the same file does not, so the " +
			"handler panics before any assertion runs), a wrong fixture: use re_refine. Put the " +
			"exact required change, per function, in re_refine_guidance. Do NOT describe a " +
			"test-file change in an execute_revised spec — the agent will not act on it.\n" +
			"  - A change to an IMPLEMENTATION (non-test) file makes the current test pass as " +
			"written — a defensive nil check, lazy initialization of package state, a corrected " +
			"algorithm: use execute_revised with that implementation change.\n" +
			"If the same test functions fail identically across 2+ attempts that each genuinely " +
			"revised the implementation, prefer re_refine — the assertion is likely " +
			"unsatisfiable. re_refine_guidance should name each function to fix, the specific " +
			"defect, and the exact correction."
		if note, identicalNames, identicalText := recurringTestFailureNote(ctx, d, beadID); note != "" {
			findings += "\n\n" + note
			if len(identicalNames) > 0 {
				findings += "\n\n[NOTE: the identical-output subtest(s) above will have " +
					"decision mechanically forced to re_refine regardless of what you answer, " +
					"per the note text — you may still explain your own reasoning, but set " +
					"re_refine_guidance as if decision=re_refine either way.]"
				h.forcedReRefineNames = identicalNames
				h.forcedReRefineText = identicalText
			}
		}
	}
	docExcerpt := loadDesignDocExcerptForBead(ctx, d, job.ProjectID, currentBead)
	userMsg := buildAdjudicateUserMsg(currentBead, revLog, findings, compressedHistory, diffSignal, docExcerpt)
	messages := []ollama.Message{
		{Role: "system", Content: guidance.InjectForVerbPath(adjudicateNextExecutionSystemPrompt, project.FolderPath, db.VerbAdjudicateNextExecution, "")},
		{Role: "user", Content: userMsg},
	}

	// Tool loop requiring at least one run_go_snippet call before any final
	// decision — same enforcement as REFINE_TESTS_CRITIQUE, and for the same
	// reason: "verify when uncertain" fails against confident-but-wrong
	// claims, which is exactly ADJUDICATE's original incident (the </div vs
	// </div> hallucination — textually identical strings asserted as
	// different across several rounds, never checked against the real file
	// content already available in Input 3). Critically, that hallucination
	// drove execute_as_is/execute_revised, not re_refine — so gating the
	// requirement only on re_refine (the decision that most obviously
	// involves an assertion-satisfiability claim) would have missed the
	// actual incident entirely. Every decision here rests on some claim
	// about what the code does or would do; requiring verification
	// universally, not conditioned on the model's own sense of whether this
	// particular call needs it, is what closes that gap.
	var lastContent string
	usedTool := false
	reRefineProbed := false
	for turn := 1; turn <= snippetVerificationTurns; turn++ {
		msg, toolErr := oc.ChatWithTools(ctx, model, messages, []ollama.Tool{runGoSnippetTool}, nil, nil)
		if toolErr != nil {
			return "", toolErr
		}
		if strings.TrimSpace(msg.Content) != "" {
			lastContent = msg.Content
		}
		if len(msg.ToolCalls) == 0 {
			// re_refine scope probe: if the model wants to route this to
			// re_refine, verify the fix it prescribes actually type-checks as a
			// test-file edit before accepting it. A cross-file type conflict
			// can't be fixed from a *_test.go file — nudge the model to
			// execute_revised/full_stop once, then let Validate enforce it.
			if !reRefineProbed {
				var tentative AdjudicateNextExecutionOutput
				if json.Unmarshal([]byte(ollama.ExtractJSON(msg.Content)), &tentative) == nil &&
					tentative.Decision == "re_refine" {
					reRefineProbed = true
					if probeOut := probeReRefineEdit(ctx, h.folderPath, tentative.ReRefineGuidance); probeOut != "" {
						h.reRefineProbeFailed = probeOut
						messages = append(messages, msg)
						messages = append(messages, ollama.Message{Role: "user", Content: "You chose " +
							"decision=re_refine, but the change your re_refine_guidance prescribes does not " +
							"type-check. Applied to a *_test.go file and compiled against the real package, " +
							"it fails:\n\n```\n" + probeOut + "\n```\n\nDuring EXECUTE_BEAD every *_test.go " +
							"file is LOCKED, and re_refine can only change *_test.go files — so a conflict " +
							"between non-test source files (two files declaring incompatible types, or " +
							"importing different packages under the same name) cannot be resolved through " +
							"re_refine. Choose execute_revised with the implementation change that makes the " +
							"types agree — name the file and the exact type or import to change — or " +
							"full_stop if that file is one this bead does not own. Then give your final " +
							"JSON decision."})
						continue
					}
				}
			}
			if usedTool || turn == snippetVerificationTurns {
				if !usedTool {
					slog.Warn("ADJUDICATE_NEXT_EXECUTION finalized without ever calling run_go_snippet",
						"bead_id", beadID, "turn", turn)
				}
				return msg.Content, nil
			}
			messages = append(messages, msg)
			messages = append(messages, ollama.Message{Role: "user", Content: "Before giving your final " +
				"decision, call run_go_snippet at least once to verify a specific claim about Go/stdlib " +
				"runtime behavior your reasoning depends on — even if you feel confident. Confidence is " +
				"not evidence; that gap is exactly what this tool exists to close. Then give your final " +
				"JSON decision."})
			continue
		}

		messages = append(messages, msg)
		for _, tc := range msg.ToolCalls {
			var result string
			if tc.Function.Name != "run_go_snippet" {
				result = fmt.Sprintf("error: unknown tool %q — only run_go_snippet is available", tc.Function.Name)
			} else if src, _ := tc.Function.Arguments["source"].(string); strings.TrimSpace(src) == "" {
				result = "error: source is empty"
			} else if out, rerr := runGoSnippet(ctx, src); rerr != nil {
				result = fmt.Sprintf("error: %v", rerr)
			} else if out == "" {
				result = "(snippet ran with no output — add a print statement to see a result)"
			} else {
				result = out
			}
			usedTool = true
			messages = append(messages, ollama.Message{Role: "tool", Content: result})
		}
	}
	return lastContent, nil
}

func buildAdjudicateUserMsg(bead *beadState, revLog []revisionEntry, mechanicalFindings, compressedHistory, diffSignal, docExcerpt string) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "## Input 1: Current Bead State\n\nBead ID: %d\nActual execution budget: %ds\n\n%s\n\n", bead.BeadID, bead.ExecutionBudget, bead.FullText)

	sb.WriteString("## Input 2: Bead Revision Log\n\n")
	for _, r := range revLog {
		fmt.Fprintf(&sb, "### Revision %d (created by %s)\n\n%s\n\n", r.RevisionNumber, r.CreatedByVerb, r.FullText)
	}
	sb.WriteString("### Diff Signal\n\n")
	sb.WriteString(diffSignal)
	sb.WriteString("\n\n")

	sb.WriteString("## Input 3: Latest Mechanical Findings\n\n")
	sb.WriteString(mechanicalFindings)
	sb.WriteString("\n\n")

	sb.WriteString("## Input 4: Compressed History\n\n")
	if compressedHistory != "" {
		sb.WriteString(compressedHistory)
	} else {
		sb.WriteString("(none — this is the first attempt)")
	}

	if docExcerpt != "" {
		sb.WriteString("\n\n## Input 5: Authoritative Design Document (excerpts)\n\n")
		sb.WriteString(docExcerpt)
	}

	return sb.String()
}

// buildDiffSignal computes the revision diff signal from the architecture:
// "a diff of each revision against the version it replaced, compared against
// the failure category ANALYZE_EXECUTION reports on subsequent attempts."
// Test-ID correspondence is the primary signal.
func buildDiffSignal(ctx context.Context, d *db.DB, beadID int64) (string, error) {
	// Restrict to the current lineage — otherwise a rewound bead's diff signal
	// keeps citing pre-rewind attempts (e.g. Ko-rule failures against a test
	// file that no longer exists) as if they were still relevant. See
	// currentLineageRevisionIDs.
	lineageIDs, err := currentLineageRevisionIDs(ctx, d, beadID)
	if err != nil {
		return "(no execution history yet)", nil
	}

	rows, err := d.QueryContext(ctx, `
		SELECT e.id, e.termination_cause, a.mechanical_findings,
		       e.bead_revision_id, e.ended_at
		FROM executions e
		JOIN analyses a ON a.execution_id = e.id
		WHERE e.bead_id = ?
		ORDER BY e.ended_at`, beadID)
	if err != nil {
		return "(no execution history yet)", nil
	}
	defer rows.Close()

	type execRow struct {
		ExecID, RevID    int64
		TerminationCause string
		Findings         string
		EndedAt          string
	}
	var execs []execRow
	for rows.Next() {
		var r execRow
		if err := rows.Scan(&r.ExecID, &r.TerminationCause, &r.Findings, &r.RevID, &r.EndedAt); err != nil {
			return "", err
		}
		if !lineageIDs[r.RevID] {
			continue
		}
		execs = append(execs, r)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(execs) == 0 {
		return "(no execution history yet)", nil
	}

	var sb strings.Builder
	for i, e := range execs {
		fmt.Fprintf(&sb, "Attempt %d (revision %d, ended %s): termination=%s\nFindings: %s\n",
			i+1, e.RevID, e.EndedAt, e.TerminationCause, e.Findings)
		if i > 0 {
			sb.WriteString("(diff against previous revision: see revision log above)\n")
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func (h *AdjudicateNextExecution) Validate(raw string) (string, any) {
	var out AdjudicateNextExecutionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}

	validTrends := map[string]bool{"same": true, "narrower": true, "unrelated": true, "not_applicable": true}
	if !validTrends[out.Trend] {
		return fmt.Sprintf("malformed: trend must be \"same\", \"narrower\", \"unrelated\", or \"not_applicable\", got %q", out.Trend), nil
	}

	validFits := map[string]bool{"bead_problem": true, "execution_capability_problem": true, "not_applicable": true}
	if !validFits[out.BeadSpecFit] {
		return fmt.Sprintf("malformed: bead_spec_fit must be \"bead_problem\", \"execution_capability_problem\", or \"not_applicable\", got %q", out.BeadSpecFit), nil
	}

	if strings.TrimSpace(out.Reasoning) == "" {
		return "malformed: reasoning is empty", nil
	}

	validDecisions := map[string]bool{"execute_as_is": true, "execute_revised": true, "full_stop": true, "declare_success": true, "test_reject": true, "re_refine": true}
	if !validDecisions[out.Decision] {
		return fmt.Sprintf("malformed: decision must be \"execute_as_is\", \"execute_revised\", \"full_stop\", \"declare_success\", \"test_reject\", or \"re_refine\", got %q", out.Decision), nil
	}

	// Mechanical override: recurringTestFailureNote found byte-identical test
	// failure output across the last 2 genuinely-rewritten implementation
	// attempts (see forcedReRefineNames' doc comment for the incident this
	// closes). That is strong enough evidence that a correct implementation
	// cannot satisfy the assertion that the decision is forced to re_refine
	// here rather than left to the model, which has been observed to instead
	// invent an ungrounded code-level explanation and repeat it with growing
	// confidence across rounds rather than reconsider. The model's own
	// diagnosis (whatever it was) is preserved as context inside the forced
	// re_refine_guidance below, not discarded.
	if len(h.forcedReRefineNames) > 0 && out.Decision != "re_refine" {
		slog.Warn("ADJUDICATE decision mechanically overridden to re_refine",
			"model_decision", out.Decision, "forced_subtests", h.forcedReRefineNames)
		var g strings.Builder
		g.WriteString("[This decision was mechanically forced to re_refine by the orchestrator " +
			"— the model originally chose decision=\"" + out.Decision + "\". The following " +
			"subtest(s) produced byte-identical failure output across the last two attempts " +
			"that each genuinely rewrote the implementation, which is conclusive evidence the " +
			"test's assertion — not the implementation — is the defect:]\n\n")
		for _, name := range h.forcedReRefineNames {
			fmt.Fprintf(&g, "  %s:\n    %s\n\n", name, h.forcedReRefineText[name])
		}
		g.WriteString("Re-derive by hand, from the test's own setup, what a correct " +
			"implementation must produce (including any output-encoding/escaping the spec " +
			"requires); rewrite the assertion to match that. The model's own original " +
			"reasoning, preserved for context, was:\n\n" + out.Reasoning)
		out.Decision = "re_refine"
		out.ReRefineGuidance = g.String()
		out.RevisedBead = nil
	}

	// Backstop for the re_refine scope probe (see reRefineProbeFailed's doc
	// comment and Run's in-loop nudge): the test-level fix the model prescribed
	// does not type-check against the real package with a cross-file type
	// conflict, which no *_test.go edit can resolve. If the model still returned
	// re_refine after being told this once, force execute_revised against the
	// unmodified spec plus a diagnostic prefix. Skipped when forcedReRefine
	// already fired — that path is a stronger, independent signal that the
	// assertion itself is unsatisfiable.
	if h.reRefineProbeFailed != "" && out.Decision == "re_refine" && len(h.forcedReRefineNames) == 0 {
		slog.Warn("ADJUDICATE decision mechanically overridden to execute_revised — "+
			"prescribed re_refine fix fails the cross-file type-check probe",
			"bead_spec", h.currentBeadSpec.Title)
		budget := h.budgetDefault
		if budget <= 0 {
			budget = h.currentBeadSpec.ExecutionBudget
		}
		out.Decision = "execute_revised"
		out.ReRefineGuidance = ""
		out.Trend = "same"
		out.BeadSpecFit = "execution_capability_problem"
		out.RevisedBead = &ParsedBead{
			Title:           h.currentBeadSpec.Title,
			OutputFiles:     h.currentBeadSpec.OutputFiles,
			ExitCriteria:    h.currentBeadSpec.ExitCriteria,
			ExecutionBudget: budget,
			MonitorOverride: "honor",
			FullText: "[Mechanically routed to execute_revised by the orchestrator] ADJUDICATE " +
				"proposed fixing the failing test by editing the test file, but that change does not " +
				"compile against the current sources:\n\n" + h.reRefineProbeFailed + "\n\nThis is a " +
				"conflict between non-test source files — the test file is LOCKED and cannot resolve " +
				"it. Fix the implementation so the types agree: read the affected file(s) first, then " +
				"write the corrected file(s) with only the type/import change applied. Do not modify " +
				"any *_test.go file.\n\n--- Original bead spec ---\n\n" + h.currentBeadSpec.FullText,
		}
		// Reasoning is replaced (not appended to) so it can't trip the
		// execution_capability_problem consistency check with spec-referential
		// phrasing carried over from the model's re_refine argument. The model's
		// original output is preserved verbatim in handoff_attempts.raw_output.
		out.Reasoning = "The re_refine fix ADJUDICATE prescribed does not type-check against the " +
			"current sources — a cross-file type conflict that no *_test.go edit can resolve. The " +
			"orchestrator routed this to execute_revised for an implementation fix. Probe compile " +
			"output:\n" + h.reRefineProbeFailed
	}

	if out.Decision == "execute_revised" {
		if out.RevisedBead == nil {
			return "malformed: decision is execute_revised but revised_bead is absent", nil
		}
		if out.RevisedBead.MonitorOverride != "honor" && out.RevisedBead.MonitorOverride != "ignore" {
			return fmt.Sprintf("malformed: revised_bead monitor_override must be \"honor\" or \"ignore\", got %q", out.RevisedBead.MonitorOverride), nil
		}
		if len(out.RevisedBead.OutputFiles) == 0 {
			return "malformed: revised_bead output_files is missing or empty", nil
		}
		if idx := firstEmptyStringIndex(out.RevisedBead.OutputFiles); idx != -1 {
			return fmt.Sprintf("malformed: revised_bead output_files[%d] is an empty string", idx), nil
		}
		if len(out.RevisedBead.ExitCriteria) == 0 {
			return "malformed: revised_bead exit_criteria is missing or empty", nil
		}
		if idx := firstEmptyStringIndex(out.RevisedBead.ExitCriteria); idx != -1 {
			return fmt.Sprintf("malformed: revised_bead exit_criteria[%d] is an empty string", idx), nil
		}
	}

	if out.Decision == "test_reject" {
		if strings.TrimSpace(out.TestRejectionGuidance) == "" {
			return "malformed: decision is test_reject but test_rejection_guidance is absent or empty", nil
		}
	}

	if out.Decision == "re_refine" {
		if strings.TrimSpace(out.ReRefineGuidance) == "" {
			return "malformed: decision is re_refine but re_refine_guidance is absent or empty", nil
		}
	}

	// For retry/stop decisions, "not_applicable" is forbidden and the consistency
	// check applies. Terminal decisions (declare_success, test_reject, re_refine)
	// may use any valid value — trend/bead_spec_fit are not used downstream on those
	// paths, so enforcing "not_applicable" only causes spurious validation failures
	// when a model correctly chooses a terminal decision but also records its analysis.
	isTerminal := out.Decision == "declare_success" || out.Decision == "test_reject" || out.Decision == "re_refine"
	if !isTerminal {
		if out.Trend == "not_applicable" {
			return "malformed: trend \"not_applicable\" is only valid for terminal decisions (declare_success, test_reject, re_refine)", nil
		}
		if out.BeadSpecFit == "not_applicable" {
			return "malformed: bead_spec_fit \"not_applicable\" is only valid for terminal decisions (declare_success, test_reject, re_refine)", nil
		}
		if ok, reason := checkConsistency(out.BeadSpecFit, out.Reasoning); !ok {
			return "malformed: consistency check failed: " + reason, nil
		}
	}

	return "valid", out
}

// Commit writes the adjudications row and enqueues the next action.
// Zero-strike tolerance: Commit is only reached on a valid output.
func (h *AdjudicateNextExecution) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(AdjudicateNextExecutionOutput)
	now := time.Now().UTC().Format(time.RFC3339)
	beadID := job.BeadID.Int64

	// Load the latest execution for metadata.
	var execID int64
	var budgetCost float64
	var monitorEscalated bool
	if err := tx.QueryRowContext(ctx, `
		SELECT e.id,
		       CAST(julianday(e.ended_at) - julianday(e.started_at) AS REAL) * 86400.0,
		       COALESCE(e.monitor_fired, 0)
		FROM executions e
		WHERE e.bead_id = ? AND e.termination_cause IS NOT NULL
		ORDER BY e.ended_at DESC LIMIT 1`, beadID,
	).Scan(&execID, &budgetCost, &monitorEscalated); err != nil {
		return fmt.Errorf("load execution for adjudication: %w", err)
	}

	monitorEscalatedInt := 0
	if monitorEscalated {
		monitorEscalatedInt = 1
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO adjudications
		  (project_id, bead_id, execution_id, trend, bead_spec_fit, reasoning_text,
		   attempt_budget_cost, monitor_escalation_status, decision, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ProjectID, beadID, execID,
		out.Trend, out.BeadSpecFit, out.Reasoning,
		budgetCost, monitorEscalatedInt, out.Decision, now,
	); err != nil {
		return fmt.Errorf("insert adjudication: %w", err)
	}

	switch out.Decision {
	case "execute_as_is":
		if atCap, err := h.atExecutionCap(ctx, tx, job.ProjectID, beadID, now, job.ID); err != nil || atCap {
			return err
		}
		// Same trailing-timeout escalation as execute_revised, but execute_as_is
		// makes no new revision — bump the current revision's budget in place so
		// the identical retry isn't handed the same wall clock that already
		// killed it twice.
		if h.trailingTimeouts >= 2 {
			enforced := enforcedTimeoutBudget(h.currentBeadSpec.ExecutionBudget, h.budgetDefault)
			if _, err := tx.ExecContext(ctx, `
				UPDATE bead_revisions SET execution_budget = ?
				WHERE id = (SELECT current_revision_id FROM beads WHERE id = ?)
				  AND execution_budget < ?`, enforced, beadID, enforced); err != nil {
				return fmt.Errorf("escalate budget on repeated timeout: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET status = 'pending' WHERE id = ?`, beadID); err != nil {
			return fmt.Errorf("reset bead to pending: %w", err)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbExecuteBead, beadID, now, now)
		return err

	case "execute_revised":
		if atCap, err := h.atExecutionCap(ctx, tx, job.ProjectID, beadID, now, job.ID); err != nil || atCap {
			return err
		}
		// Write a new bead_revision for the revised spec. Use the bead-wide max,
		// not the current revision's number + 1: after rewind-bead resets
		// current_revision_id back to revision 1, a naive current+1 collides with
		// the pre-rewind revision 2 that's still in the table (see sameRevisionResumeNote
		// commit history — rewound beads keep their old revisions for audit purposes,
		// they're just no longer current).
		var currentRevNum int
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(revision_number), 0) FROM bead_revisions WHERE bead_id = ?`, beadID,
		).Scan(&currentRevNum); err != nil {
			return fmt.Errorf("load max revision number: %w", err)
		}

		// Clamp execution_budget to at least the project default so ADJUDICATE
		// cannot accidentally starve a retry with a too-small budget estimate.
		// Apply the clamp to the struct before marshaling so full_text stored in
		// the DB reflects the enforced budget — ADJUDICATE reads full_text on the
		// next round and would otherwise anchor to the unclamped value.
		if out.RevisedBead.ExecutionBudget < h.budgetDefault {
			out.RevisedBead.ExecutionBudget = h.budgetDefault
		}
		// Mechanical trailing-timeout escalation: when the last >=2 in-lineage
		// executions all timed out, wall-clock is the bottleneck. Force the
		// budget to double the just-executed revision's value (capped), over
		// whatever the model returned — the prompt's prose "double the budget"
		// rule depends on the model classifying a stub-file timeout correctly
		// and a weak model does not (see h.trailingTimeouts).
		if h.trailingTimeouts >= 2 {
			enforced := enforcedTimeoutBudget(h.currentBeadSpec.ExecutionBudget, h.budgetDefault)
			if out.RevisedBead.ExecutionBudget < enforced {
				slog.Warn("ADJUDICATE budget mechanically escalated on repeated timeout",
					"bead_id", beadID, "trailing_timeouts", h.trailingTimeouts,
					"prior_budget", h.currentBeadSpec.ExecutionBudget,
					"model_budget", out.RevisedBead.ExecutionBudget, "enforced_budget", enforced)
				out.RevisedBead.ExecutionBudget = enforced
			}
		}
		budget := out.RevisedBead.ExecutionBudget

		// Apply language-specific structural fixes to the revised spec before
		// storing it, catching the same class of errors that DECOMPOSE and
		// RECONCILE fix at decomposition time (e.g. go test without a test file).
		lang := detectLang(h.folderPath, out.RevisedBead.OutputFiles)
		applyMechanicalBeadFixes(lang, out.RevisedBead)

		// Source-side gate: don't commit a revised spec that is internally
		// inconsistent (an orphan -run name, a grep guard for a file the bead
		// doesn't own) or that invents a new required test function — inventing
		// a test function is re_refine's job, not execute_revised's. On a
		// violation, downgrade to execute_as_is: retry the bead against its
		// current, unmodified spec rather than a broken revision.
		if v := beadConsistencyViolations(lang, []ParsedBead{*out.RevisedBead},
			map[string]ParsedBead{h.currentBeadSpec.Title: h.currentBeadSpec}); len(v) > 0 {
			slog.Warn("ADJUDICATE execute_revised downgraded to execute_as_is — revised spec failed consistency checks",
				"bead_id", beadID, "violations", v)
			if _, err := tx.ExecContext(ctx,
				`UPDATE beads SET status = 'pending' WHERE id = ?`, beadID); err != nil {
				return fmt.Errorf("reset bead to pending: %w", err)
			}
			_, err := tx.ExecContext(ctx, `
				INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
				VALUES (?, ?, ?, 'pending', ?, ?)`,
				job.ProjectID, db.VerbExecuteBead, beadID, now, now)
			return err
		}

		fullText, _ := json.Marshal(out.RevisedBead)
		res, err := tx.ExecContext(ctx, `
			INSERT INTO bead_revisions
			  (project_id, bead_id, revision_number, full_text,
			   execution_budget, monitor_override, created_by_verb, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			job.ProjectID, beadID, currentRevNum+1, string(fullText),
			budget, out.RevisedBead.MonitorOverride,
			db.VerbAdjudicateNextExecution, now)
		if err != nil {
			return fmt.Errorf("insert revised bead_revision: %w", err)
		}
		revID, _ := res.LastInsertId()

		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET status = 'pending', current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbExecuteBead, beadID, now, now)
		return err

	case "test_reject":
		if atCap, err := h.atExecutionCap(ctx, tx, job.ProjectID, beadID, now, job.ID); err != nil || atCap {
			return err
		}
		// Load the current bead revision so we can copy its spec and delete its test files.
		var currentFullText string
		var currentBudget int
		var currentMonitor string
		if err := tx.QueryRowContext(ctx, `
			SELECT br.full_text, br.execution_budget, br.monitor_override
			FROM beads b JOIN bead_revisions br ON br.id = b.current_revision_id
			WHERE b.id = ?`, beadID,
		).Scan(&currentFullText, &currentBudget, &currentMonitor); err != nil {
			return fmt.Errorf("load current bead for test_reject: %w", err)
		}
		var currentSpec ParsedBead
		if err := json.Unmarshal([]byte(currentFullText), &currentSpec); err != nil {
			return fmt.Errorf("parse current bead spec for test_reject: %w", err)
		}
		// Delete test files from disk so the next EXECUTE re-enters test-first mode.
		for _, f := range currentSpec.OutputFiles {
			if strings.HasSuffix(f, "_test.go") {
				_ = os.Remove(filepath.Join(h.folderPath, f))
			}
		}
		// Build revised spec: prepend the rejection guidance so the model sees
		// what was wrong and can correct it when rewriting the test files.
		revisedSpec := currentSpec
		revisedSpec.FullText = "[Test-first rejection] The previous test-first attempt wrote test files " +
			"with incorrect assertions. The test files have been deleted. Rewrite them with the " +
			"following corrections applied:\n\n" + out.TestRejectionGuidance + "\n\n" +
			currentSpec.FullText
		// Bead-wide max, not currentRevNum+1 — see the execute_revised branch above
		// for why (rewind-bead can leave a lower current revision number in place
		// while higher-numbered stale revisions remain in the table).
		var maxRevNum int
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(revision_number), 0) FROM bead_revisions WHERE bead_id = ?`, beadID,
		).Scan(&maxRevNum); err != nil {
			return fmt.Errorf("load max revision number: %w", err)
		}
		fullText, _ := json.Marshal(revisedSpec)
		res, err := tx.ExecContext(ctx, `
			INSERT INTO bead_revisions
			  (project_id, bead_id, revision_number, full_text,
			   execution_budget, monitor_override, created_by_verb, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			job.ProjectID, beadID, maxRevNum+1, string(fullText),
			currentBudget, currentMonitor, db.VerbAdjudicateNextExecution, now)
		if err != nil {
			return fmt.Errorf("insert test_reject bead_revision: %w", err)
		}
		revID, _ := res.LastInsertId()
		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET status = 'pending', current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbExecuteBead, beadID, now, now)
		return err

	case "re_refine":
		// Determine the next refinement cycle (max existing + 1).
		var maxCycle int64
		_ = tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(refinement_cycle_id), 0) FROM handoff_jobs
			WHERE bead_id = ? AND verb = ?`, beadID, db.VerbRefineTestsWrite,
		).Scan(&maxCycle)
		nextCycle := maxCycle + 1

		if nextCycle > refinementCycleCap {
			slog.Error("ESCALATION — re_refine: refinement cycle cap reached",
				"bead_id", beadID, "next_cycle", nextCycle, "cap", refinementCycleCap)
			_, err := tx.ExecContext(ctx,
				`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`, now, job.ID)
			return err
		}

		// Inject ADJUDICATE's diagnosis as CRITIQUE findings via test_refinements so JUDGE
		// can read it via its fallback query. JUDGE will produce functions_to_rewrite +
		// instructions, and enqueue WRITE in revision mode to fix only the broken functions.
		if err := insertRefinement(ctx, tx, job.ProjectID, beadID, nextCycle,
			db.VerbRefineTestsCritique, out.ReRefineGuidance, ""); err != nil {
			return fmt.Errorf("inject re_refine guidance into test_refinements: %w", err)
		}

		// Grant a fresh set of execution attempts so the fixed tests get a fair run.
		var currentExecCount, maxAttempts int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM executions WHERE bead_id = ?`, beadID).Scan(&currentExecCount)
		_ = tx.QueryRowContext(ctx, `SELECT max_execution_attempts FROM projects WHERE id = ?`, job.ProjectID).Scan(&maxAttempts)
		if maxAttempts == 0 {
			maxAttempts = 5
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET execution_attempts_override = ? WHERE id = ?`,
			currentExecCount+maxAttempts, beadID); err != nil {
			return fmt.Errorf("grant re_refine execution attempts: %w", err)
		}

		// Enqueue JUDGE (not WRITE) at nextCycle — it reads the injected diagnosis and
		// produces functions_to_rewrite + instructions, then enqueues WRITE for cycle+1.
		_, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
			job.ProjectID, db.VerbRefineTestsJudge, beadID, nextCycle, now, now)
		return err

	case "full_stop":
		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET status = 'full_stopped' WHERE id = ?`, beadID); err != nil {
			return err
		}
		report.WriteBead(ctx, tx, h.folderPath, beadID, "full_stopped")

		// Collect cascade bead IDs before the bulk update.
		cascadeIDs, _ := queryCascadeBeadIDs(ctx, tx, job.ProjectID, beadID)

		// Mark all subsequent pending beads stopped — they will never run now.
		if _, err := tx.ExecContext(ctx, `
			UPDATE beads SET status = 'full_stopped'
			WHERE project_id = ? AND id > ? AND status = 'pending'`,
			job.ProjectID, beadID,
		); err != nil {
			return fmt.Errorf("mark remaining beads full_stopped: %w", err)
		}
		for _, cascadeID := range cascadeIDs {
			report.WriteBead(ctx, tx, h.folderPath, cascadeID,
				fmt.Sprintf("full_stopped (cascade — stopped by bead %d)", beadID))
		}
		if err := h.checkProjectTerminal(ctx, tx, job.ProjectID, "full_stopped", now); err != nil {
			return err
		}
		report.WriteProject(ctx, tx, job.ProjectID, h.folderPath)
		return nil

	case "declare_success":
		var currentFullText string
		if err := tx.QueryRowContext(ctx, `
			SELECT br.full_text FROM beads b
			JOIN bead_revisions br ON br.id = b.current_revision_id
			WHERE b.id = ?`, beadID,
		).Scan(&currentFullText); err != nil {
			return fmt.Errorf("load current bead for exit-criteria gate: %w", err)
		}
		var currentBead ParsedBead
		if err := json.Unmarshal([]byte(currentFullText), &currentBead); err != nil {
			return fmt.Errorf("parse current bead spec for exit-criteria gate: %w", err)
		}
		if ok, detail := execcheck.VerifyExitCriteria(ctx, h.folderPath, currentBead.ExitCriteria); !ok {
			slog.Warn("ADJUDICATE declare_success rejected by mechanical exit-criteria gate",
				"bead_id", beadID, "detail", detail)
			if atCap, err := h.atExecutionCap(ctx, tx, job.ProjectID, beadID, now, job.ID); err != nil || atCap {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE beads SET status = 'pending' WHERE id = ?`, beadID); err != nil {
				return fmt.Errorf("reset bead to pending after failed declare_success gate: %w", err)
			}
			_, err := tx.ExecContext(ctx, `
				INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
				VALUES (?, ?, ?, 'pending', ?, ?)`,
				job.ProjectID, db.VerbExecuteBead, beadID, now, now)
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET status = 'succeeded' WHERE id = ?`, beadID); err != nil {
			return fmt.Errorf("mark bead succeeded: %w", err)
		}
		report.WriteBead(ctx, tx, h.folderPath, beadID, "succeeded")
		regenerateAPICheckTest(ctx, tx, job.ProjectID, h.folderPath)

		var pendingCount int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM beads WHERE project_id = ? AND status = 'pending'`,
			job.ProjectID,
		).Scan(&pendingCount); err != nil {
			return fmt.Errorf("count pending beads: %w", err)
		}

		if pendingCount == 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE projects SET status = 'complete', updated_at = ? WHERE id = ?`,
				now, job.ProjectID); err != nil {
				return err
			}
			report.WriteProject(ctx, tx, job.ProjectID, h.folderPath)
			return nil
		}

		// Fire REVISE_PENDING to update remaining specs before dispatching next bead.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbRevisePending, beadID, now, now,
		); err != nil {
			return err
		}
		if pause, err := shouldPauseAfterVerb(ctx, tx, job.ProjectID, db.VerbAdjudicateNextExecution); err != nil {
			return err
		} else if pause {
			return pauseProject(ctx, tx, job.ProjectID, now)
		}
		return nil
	}
	return nil
}

// regenerateAPICheckTest re-derives apiCheckTestFilename from the immutable
// SURVEY_SPEC manifest after every bead success. writeAPICheckTest's output
// depends only on that manifest, so this is idempotent by construction — it
// self-heals apiCheckTestFilename back to pure compile-time assertions if
// anything (a stray write, a future bug) ever pollutes it with hand-written
// content, regardless of the source. Best-effort: a Go-only file, and not
// worth failing bead completion over, so errors are logged and swallowed.
func regenerateAPICheckTest(ctx context.Context, tx *sql.Tx, projectID int64, folderPath string) {
	if _, err := os.Stat(filepath.Join(folderPath, apiCheckTestFilename)); os.IsNotExist(err) {
		return // not a Go project (or scaffolding hasn't run) — nothing to heal.
	}
	manifest, err := latestSurveyManifestTx(ctx, tx, projectID)
	if err != nil {
		slog.Warn("regenerateAPICheckTest: load manifest failed", "project_id", projectID, "error", err)
		return
	}
	if err := writeAPICheckTest(manifest.Package, folderPath, manifest.Files); err != nil {
		slog.Warn("regenerateAPICheckTest: write failed", "project_id", projectID, "error", err)
	}
}

// atExecutionCap returns true if the bead has reached the project's
// max_execution_attempts limit. When the cap is reached, the ADJUDICATE job is
// escalated so Mike can review rather than looping indefinitely.
func (h *AdjudicateNextExecution) atExecutionCap(ctx context.Context, tx *sql.Tx, projectID, beadID int64, now string, jobID int64) (bool, error) {
	var cap, count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(b.execution_attempts_override, p.max_execution_attempts)
		FROM beads b JOIN projects p ON p.id = b.project_id
		WHERE b.id = ?`, beadID,
	).Scan(&cap); err != nil {
		return false, fmt.Errorf("load max_execution_attempts: %w", err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM executions WHERE bead_id = ? AND infra_failure = 0 AND test_first_attempt = 0`, beadID,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("count executions for bead %d: %w", beadID, err)
	}
	if count < cap {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`,
		now, jobID,
	); err != nil {
		return true, fmt.Errorf("escalate at cap: %w", err)
	}
	slog.Error("ESCALATION — max execution attempts reached",
		"project_id", projectID, "bead_id", beadID,
		"attempts", count, "cap", cap, "job_id", jobID)
	report.WriteBead(ctx, tx, h.folderPath, beadID, "escalated")
	return true, nil
}

// queryCascadeBeadIDs returns IDs of pending beads after beadID in the project.
// Called before the bulk full_stop update so we can write cascade reports.
func queryCascadeBeadIDs(ctx context.Context, tx *sql.Tx, projectID, afterBeadID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM beads
		WHERE project_id = ? AND id > ? AND status = 'pending'
		ORDER BY id`, projectID, afterBeadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// checkProjectTerminal checks whether all beads in the project have reached a
// terminal state ('full_stopped' or 'succeeded'). If so, it marks the project
// with terminalStatus. Called from the full_stop branch.
func (h *AdjudicateNextExecution) checkProjectTerminal(ctx context.Context, tx *sql.Tx, projectID int64, terminalStatus, now string) error {
	var activeBeads int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM beads
		WHERE project_id = ? AND status NOT IN ('full_stopped', 'succeeded')`,
		projectID,
	).Scan(&activeBeads); err != nil {
		return fmt.Errorf("count active beads: %w", err)
	}
	if activeBeads == 0 {
		_, err := tx.ExecContext(ctx,
			`UPDATE projects SET status = ?, updated_at = ? WHERE id = ?`,
			terminalStatus, now, projectID)
		return err
	}
	return nil
}
