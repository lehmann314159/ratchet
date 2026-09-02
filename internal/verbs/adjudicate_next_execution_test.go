package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ratchet/internal/db"
)

// writeCrossFileMismatchPkg lays out a minimal buildable package with the same
// shape as exprvm-web bead 246: one file declares a package-level
// *html/template.Template, another a func returning *text/template.Template.
// `go build`/`go test -c` pass on their own — the conflict only bites when a
// test assigns one to the other, which is exactly the fix ADJUDICATE prescribed
// via re_refine and REFINE_TESTS could never apply.
func writeCrossFileMismatchPkg(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module probe\n\ngo 1.21\n",
		"render.go": "package probe\n\nimport \"html/template\"\n\nvar tmpl *template.Template\n\n" +
			"func Render() *template.Template { return tmpl }\n",
		"initx.go": "package probe\n\nimport \"text/template\"\n\n" +
			"func InitX() *template.Template { return template.New(\"x\") }\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestExtractGoStatements(t *testing.T) {
	guidance := "- In TestFoo setup, add `tmpl = InitX()` before the assertion.\n" +
		"- Change the expected count from 3 to 4.\n" +
		"```go\ntmpl = InitX()\nfoo.Bar(1, 2)\nfunc Helper() {}\n```\n" +
		"- Also call `SomethingUndefined()`."
	got := extractGoStatements(guidance)
	want := map[string]bool{"tmpl = InitX()": true, "foo.Bar(1, 2)": true, "SomethingUndefined()": true}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected extracted statement %q", s)
		}
		delete(want, s)
	}
	for s := range want {
		t.Errorf("missing expected statement %q (got %v)", s, got)
	}
	// Prose-only guidance yields nothing to probe.
	if s := extractGoStatements("- Loosen the assertion to check only that an error is returned."); len(s) != 0 {
		t.Errorf("prose guidance must extract no statements, got %v", s)
	}
}

func TestProbeReRefineEdit(t *testing.T) {
	dir := writeCrossFileMismatchPkg(t)

	// The prescribed cross-file assignment must not type-check → non-empty finding.
	out := probeReRefineEdit(context.Background(), dir, "- Add `tmpl = InitX()` to the test setup.")
	if !reCrossFileTypeConflict.MatchString(out) {
		t.Fatalf("expected a cross-file type-conflict compile error, got %q", out)
	}
	// The probe file must be cleaned up.
	if _, err := os.Stat(filepath.Join(dir, "zz_ratchet_adjudicate_probe_test.go")); !os.IsNotExist(err) {
		t.Errorf("probe test file was not removed")
	}
	// A statement that does type-check → no finding.
	if out := probeReRefineEdit(context.Background(), dir, "- Add `_ = tmpl` to the setup."); out != "" {
		t.Errorf("a compiling edit must yield no finding, got %q", out)
	}
	// Nothing concrete to probe → no finding.
	if out := probeReRefineEdit(context.Background(), dir, "- Loosen the assertion."); out != "" {
		t.Errorf("prose guidance must yield no finding, got %q", out)
	}
}

func TestAdjudicateValidate_ReRefineProbeForcesExecuteRevised(t *testing.T) {
	dir := writeCrossFileMismatchPkg(t)
	probeOut := probeReRefineEdit(context.Background(), dir, "- Add `tmpl = InitX()` to setup.")
	if probeOut == "" {
		t.Fatal("precondition: probe should have found a conflict")
	}

	h := &AdjudicateNextExecution{
		budgetDefault:       300,
		folderPath:          dir,
		reRefineProbeFailed: probeOut,
		currentBeadSpec: ParsedBead{
			Title:        "render bead",
			FullText:     "Wire up template rendering.",
			OutputFiles:  []string{"render.go", "render_test.go"},
			ExitCriteria: []string{"go test ./..."},
		},
	}
	raw, _ := json.Marshal(AdjudicateNextExecutionOutput{
		Trend: "same", BeadSpecFit: "bead_problem",
		Reasoning: "The test asserts a rendered value the specification does not mandate; loosen it.",
		Decision:  "re_refine", ReRefineGuidance: "- Add `tmpl = InitX()` to TestRender setup.",
	})

	result, parsed := h.Validate(string(raw))
	if result != "valid" {
		t.Fatalf("Validate = %q, want valid", result)
	}
	out := parsed.(AdjudicateNextExecutionOutput)
	if out.Decision != "execute_revised" {
		t.Fatalf("decision = %q, want execute_revised", out.Decision)
	}
	if out.RevisedBead == nil {
		t.Fatal("execute_revised must carry a revised_bead")
	}
	if len(out.RevisedBead.OutputFiles) != 2 || out.RevisedBead.ExecutionBudget != 300 {
		t.Errorf("revised bead not derived from current spec: %+v", out.RevisedBead)
	}
	if !strings.Contains(out.RevisedBead.FullText, "does not compile") &&
		!strings.Contains(out.RevisedBead.FullText, probeOut) {
		t.Errorf("revised full_text missing the compile evidence: %q", out.RevisedBead.FullText)
	}
	if !strings.Contains(out.RevisedBead.FullText, h.currentBeadSpec.FullText) {
		t.Error("revised full_text must retain the original spec")
	}

	// Negative: a re_refine whose fix DOES compile is left untouched.
	h2 := &AdjudicateNextExecution{budgetDefault: 300, folderPath: dir, currentBeadSpec: h.currentBeadSpec}
	result2, parsed2 := h2.Validate(string(raw))
	if result2 != "valid" || parsed2.(AdjudicateNextExecutionOutput).Decision != "re_refine" {
		t.Errorf("with no probe failure cached, decision must stay re_refine; got %q / %v",
			result2, parsed2.(AdjudicateNextExecutionOutput).Decision)
	}
}

// TestDetectStubFuncs_FloatZeroValue reproduces a gap found while auditing
// the fractal-smoke stress test: isZeroValueExpr recognized "0" (token.INT)
// as a stub return but not "0.0"/"0."/".0" (token.FLOAT), so a genuinely
// unimplemented float-returning stub like `func Scale() float64 { return
// 0.0 }` slipped past detectStubFuncs — no "[Stub implementation]" nudge
// ever reached ADJUDICATE for it. Covers both the previously-missed float
// zero forms and a non-zero float, which must NOT be flagged.
func TestDetectStubFuncs_FloatZeroValue(t *testing.T) {
	dir := t.TempDir()
	src := `package main

func ZeroDotZero() float64 { return 0.0 }
func ZeroDot() float64 { return 0. }
func DotZero() float64 { return .0 }
func NonZero() float64 { return 0.5 }
func RealWork() float64 { x := 2.0; return x * x }
`
	path := filepath.Join(dir, "stubs.go")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	stubs := detectStubFuncs(path, "stubs.go")

	want := map[string]bool{"ZeroDotZero": true, "ZeroDot": true, "DotZero": true}
	got := map[string]bool{}
	for _, s := range stubs {
		got[s] = true
	}
	for name := range want {
		found := false
		for s := range got {
			if len(s) >= len(name) && s[:len(name)] == name {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %s to be detected as a float-zero-value stub; got %v", name, stubs)
		}
	}
	for _, notWant := range []string{"NonZero", "RealWork"} {
		for s := range got {
			if len(s) >= len(notWant) && s[:len(notWant)] == notWant {
				t.Errorf("%s must not be flagged as a stub (not a zero-value return): %v", notWant, stubs)
			}
		}
	}
}

func TestEnforcedTimeoutBudget(t *testing.T) {
	cases := []struct{ prior, def, want int }{
		{900, 900, 1800},  // first escalation
		{1800, 900, 3600}, // compounds
		{3600, 900, 7200}, // reaches ceiling (8x default)
		{7200, 900, 7200}, // clamped at ceiling
		{5000, 900, 7200}, // clamped from below the doubled value
	}
	for _, c := range cases {
		if got := enforcedTimeoutBudget(c.prior, c.def); got != c.want {
			t.Errorf("enforcedTimeoutBudget(%d, %d) = %d, want %d", c.prior, c.def, got, c.want)
		}
	}
}

func TestCountTrailingTimeouts(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, -1, "fixture: trailing-timeout counting for ADJUDICATE budget escalation")
	beadID, revID := seedBead(t, d, -1, "parser")

	// Not a timeout most-recently → run is 0 even with an earlier timeout.
	seedExecution(t, d, -1, beadID, revID, "timeout", nil)
	seedExecution(t, d, -1, beadID, revID, "success", nil)
	if got := countTrailingTimeouts(context.Background(), d, beadID); got != 0 {
		t.Fatalf("after [timeout, success] trailing count = %d, want 0", got)
	}

	// Two more timeouts on top → run is 2 (monitor_terminated does not extend it).
	seedExecution(t, d, -1, beadID, revID, "timeout", nil)
	seedExecution(t, d, -1, beadID, revID, "timeout", nil)
	if got := countTrailingTimeouts(context.Background(), d, beadID); got != 2 {
		t.Fatalf("after [...timeout, timeout] trailing count = %d, want 2", got)
	}

	seedExecution(t, d, -1, beadID, revID, "monitor_terminated", nil)
	if got := countTrailingTimeouts(context.Background(), d, beadID); got != 0 {
		t.Fatalf("monitor_terminated must not count as a timeout; got %d, want 0", got)
	}
}

// TestAdjudicateCommit_RepeatedTimeoutEscalatesBudget locks in the fix for the
// exprvm-web-baseline-6 bead 269 incident: a REFINE_TESTS bead timed out three
// times in one-turn generation and ADJUDICATE left execution_budget at 900 each
// round. Commit must now double it mechanically regardless of the model's value.
func TestAdjudicateCommit_RepeatedTimeoutEscalatesBudget(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE mechanical budget escalation on repeated timeout")
	beadID, revID := seedBead(t, d, -1, "parser")
	seedExecution(t, d, -1, beadID, revID, "timeout", nil)
	seedExecution(t, d, -1, beadID, revID, "timeout", nil)

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})

	h := &AdjudicateNextExecution{
		budgetDefault:    900,
		folderPath:       t.TempDir(),
		trailingTimeouts: 2,
		currentBeadSpec: ParsedBead{
			Title: "parser", FullText: "spec for parser",
			OutputFiles: []string{"parser.go"}, ExitCriteria: []string{"go test ./..."},
			ExecutionBudget: 900,
		},
	}
	parsed := AdjudicateNextExecutionOutput{
		Trend: "same", BeadSpecFit: "execution_capability_problem",
		Reasoning: "timed out again; the agent runs out of time before writing",
		Decision:  "execute_revised",
		RevisedBead: &ParsedBead{
			Title: "parser", FullText: "Write parser.go with a compiling skeleton first.",
			OutputFiles: []string{"parser.go"}, ExitCriteria: []string{"go test ./..."},
			ExecutionBudget: 900, // model left it unchanged — Commit must override
			MonitorOverride: "honor",
		},
	}

	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, parsed) })

	var budget int
	if err := d.QueryRowContext(ctx, `
		SELECT br.execution_budget
		FROM beads b JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID).Scan(&budget); err != nil {
		t.Fatalf("read revised revision budget: %v", err)
	}
	if budget != 1800 {
		t.Errorf("revised execution_budget = %d, want 1800 (mechanical 2x escalation)", budget)
	}
}
