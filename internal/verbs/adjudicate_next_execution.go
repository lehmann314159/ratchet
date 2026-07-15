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
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
	"ratchet/internal/report"
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

// verifyExitCriteriaMechanically re-runs a bead's exit_criteria commands
// against the current on-disk state, independent of anything the model
// reported during execution or analysis. This is the hard, non-model-
// overridable gate behind declare_success: ADJUDICATE's own narrative
// interpretation of a trace can be wrong even when the mechanical findings
// already contain the correct signal.
//
// Real-world case this catches (checkers-v8, project 98, bead 627): the exit
// criterion's own literal run failed early in the attempt, but the model's
// own later self-check command (`grep ... && echo Pass || echo Fail`) always
// exits 0 regardless of the grep result — that shell construct cannot fail —
// and the analyzer misread the ambiguous "exit 0" as the criterion having
// passed, even though the literal criterion itself was never re-run to a
// passing state. Re-running it here removes all such ambiguity: no model
// narrative involved, matching the "mechanical, not model" philosophy behind
// forwardFileReferenceChecks and the AUDIT/RECONCILE convergence comparator.
func verifyExitCriteriaMechanically(ctx context.Context, folderPath string, exitCriteria []string) (bool, string) {
	for _, criterion := range exitCriteria {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		cmd := exec.CommandContext(cctx, "bash", "-c", criterion)
		cmd.Dir = folderPath
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			detail := fmt.Sprintf("exit criterion %q currently fails on disk (%v)", criterion, err)
			if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
				detail += ":\n" + trimmed
			}
			return false, detail
		}
	}
	return true, ""
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
func recurringTestFailureNote(ctx context.Context, d *db.DB, beadID int64) string {
	rows, err := d.QueryContext(ctx, `
		SELECT e.trace_path, a.mechanical_findings
		FROM executions e
		JOIN analyses a ON a.execution_id = e.id
		WHERE e.bead_id = ? AND e.infra_failure = 0 AND e.test_first_attempt = 0
		ORDER BY e.id DESC LIMIT 5`, beadID)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var failNames []map[string]bool
	for rows.Next() && len(failNames) < 2 {
		var tracePath, findings string
		if err := rows.Scan(&tracePath, &findings); err != nil {
			return ""
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
	}
	if err := rows.Err(); err != nil || len(failNames) < 2 {
		return ""
	}

	var shared []string
	for name := range failNames[0] {
		if strings.Contains(name, "/") && failNames[1][name] {
			shared = append(shared, name)
		}
	}
	if len(shared) == 0 {
		return ""
	}
	sort.Strings(shared)

	return "[Fast path — recurring test failure] The following subtest(s) failed identically " +
		"across the last two attempts that revised the implementation: " + strings.Join(shared, ", ") +
		". Revising the bead spec's implementation prose has not resolved this.\n\n" +
		"Action: issue decision=re_refine, not execute_revised. In re_refine_guidance, explain " +
		"for each listed subtest why its assertion cannot be satisfied by any correct " +
		"implementation given how the test sets up its inputs, and what must change in the test."
}

type AdjudicateNextExecution struct {
	budgetDefault int    // cached from Run for use in Commit
	folderPath    string // cached from Run for use in Commit
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
	if note := missingPathNote(ctx, d, beadID); note != "" {
		findings += "\n\n" + note
	}
	if beadHasRefinements(ctx, d, beadID) {
		findings += "\n\n[REFINE_TESTS bead] This bead's tests were written by REFINE_TESTS. " +
			"If the same test functions fail identically across multiple attempts and the " +
			"implementation logic appears correct, use re_refine after 2 or more identical " +
			"failures — the tests themselves may contain logically impossible assertions. " +
			"Before issuing execute_revised, ask: can any spec change cause the failing " +
			"assertion to pass with a correct implementation? If not, use re_refine. " +
			"re_refine_guidance should identify each broken assertion, why it cannot be " +
			"satisfied by a correct implementation, and which function contains it."
		if note := recurringTestFailureNote(ctx, d, beadID); note != "" {
			findings += "\n\n" + note
		}
	}
	userMsg := buildAdjudicateUserMsg(currentBead, revLog, findings, compressedHistory, diffSignal)
	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: guidance.InjectForVerbPath(adjudicateNextExecutionSystemPrompt, project.FolderPath, db.VerbAdjudicateNextExecution, "")},
		{Role: "user", Content: userMsg},
	}, nil)
}

func buildAdjudicateUserMsg(bead *beadState, revLog []revisionEntry, mechanicalFindings, compressedHistory, diffSignal string) string {
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
		if len(out.RevisedBead.ExitCriteria) == 0 {
			return "malformed: revised_bead exit_criteria is missing or empty", nil
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
		budget := out.RevisedBead.ExecutionBudget

		// Apply language-specific structural fixes to the revised spec before
		// storing it, catching the same class of errors that DECOMPOSE and
		// RECONCILE fix at decomposition time (e.g. go test without a test file).
		applyMechanicalBeadFixes(
			detectLang(h.folderPath, out.RevisedBead.OutputFiles),
			out.RevisedBead,
		)

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
		if ok, detail := verifyExitCriteriaMechanically(ctx, h.folderPath, currentBead.ExitCriteria); !ok {
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
		_, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbRevisePending, beadID, now, now)
		return err
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
