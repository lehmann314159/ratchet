package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ratchet/internal/db"
)

// TestRunGoSnippet covers the 2026-07-20 exprvm-web-v1 incident directly:
// two separate real REFINE_TESTS_CRITIQUE calls confidently reasoned wrong
// about Go/stdlib runtime behavior in prose (html/template's escaping of
// '+', and fmt.Errorf-produced error equality). These cases prove
// runGoSnippet actually gives the correct, ground-truth answer for both,
// which is the whole point of giving CRITIQUE the tool instead of leaving
// it to reason unverified.
func TestRunGoSnippet(t *testing.T) {
	t.Run("reveals html/template escapes '+' — the bead 33 question", func(t *testing.T) {
		src := `package main

import (
	"html/template"
	"os"
)

func main() {
	t := template.Must(template.New("x").Parse(` + "`{{.}}`" + `))
	t.Execute(os.Stdout, "1 + 1")
}
`
		out, err := runGoSnippet(context.Background(), src)
		if err != nil {
			t.Fatalf("runGoSnippet error: %v", err)
		}
		if out != "1 &#43; 1" {
			t.Errorf("output = %q, want %q — this is the exact fact a CRITIQUE call got wrong for real", out, "1 &#43; 1")
		}
	})

	t.Run("reveals fmt.Errorf values compare unequal with == — the bead 28 question", func(t *testing.T) {
		src := `package main

import "fmt"

func main() {
	err1 := fmt.Errorf("boom")
	err2 := fmt.Errorf("boom")
	fmt.Println(err1 == err2)
}
`
		out, err := runGoSnippet(context.Background(), src)
		if err != nil {
			t.Fatalf("runGoSnippet error: %v", err)
		}
		if out != "false" {
			t.Errorf("output = %q, want %q — two distinct fmt.Errorf values are never == even with identical text", out, "false")
		}
	})

	t.Run("captures a compile error as output, not as a Go error", func(t *testing.T) {
		src := `package main

func main() {
	this is not valid go
}
`
		out, err := runGoSnippet(context.Background(), src)
		if err != nil {
			t.Fatalf("runGoSnippet returned an infra error for a snippet-level compile failure: %v", err)
		}
		if out == "" {
			t.Error("expected non-empty compiler error output")
		}
	})

	t.Run("enforces a timeout on a runaway snippet", func(t *testing.T) {
		orig := maxSnippetRuntime
		maxSnippetRuntime = 200 * time.Millisecond
		defer func() { maxSnippetRuntime = orig }()

		src := `package main

func main() {
	select {}
}
`
		_, err := runGoSnippet(context.Background(), src)
		if err == nil {
			t.Fatal("expected a timeout error for a snippet that never terminates")
		}
		if !strings.Contains(err.Error(), "timeout") {
			t.Errorf("error = %v, want it to mention timeout", err)
		}
	})

	t.Run("plain successful output is returned verbatim", func(t *testing.T) {
		src := `package main

import "fmt"

func main() {
	fmt.Print("ok")
}
`
		out, err := runGoSnippet(context.Background(), src)
		if err != nil {
			t.Fatalf("runGoSnippet error: %v", err)
		}
		if out != "ok" {
			t.Errorf("output = %q, want %q", out, "ok")
		}
	})
}

func TestRunGoSnippetToolDefinition(t *testing.T) {
	if runGoSnippetTool.Function.Name != "run_go_snippet" {
		t.Errorf("tool name = %q, want %q", runGoSnippetTool.Function.Name, "run_go_snippet")
	}
	if _, ok := runGoSnippetTool.Function.Parameters.Properties["source"]; !ok {
		t.Error("tool parameters missing required \"source\" property")
	}
	found := false
	for _, r := range runGoSnippetTool.Function.Parameters.Required {
		if r == "source" {
			found = true
		}
	}
	if !found {
		t.Error("\"source\" must be a required parameter")
	}
}

func TestRunGoSnippetCaseToolDefinition(t *testing.T) {
	if runGoSnippetCaseTool.Function.Name != "run_go_snippet" {
		t.Errorf("tool name = %q, want %q", runGoSnippetCaseTool.Function.Name, "run_go_snippet")
	}
	for _, want := range []string{"source", "for_case"} {
		if _, ok := runGoSnippetCaseTool.Function.Parameters.Properties[want]; !ok {
			t.Errorf("tool parameters missing %q property", want)
		}
		found := false
		for _, r := range runGoSnippetCaseTool.Function.Parameters.Required {
			if r == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q must be a required parameter", want)
		}
	}
}

// TestExtractSubtestCases covers the 2026-07-21 exprvm-web-v1 bead 34
// incident directly: handlers_test.go had one top-level TestHandlers
// function wrapping 8 t.Run subtests, 3 of which hand-built a raw '+' into
// an x-www-form-urlencoded request body — a bug REFINE_TESTS_CRITIQUE missed
// after a single, unrelated run_go_snippet call satisfied the old flat
// "called at least once" gate. extractSubtestCases is what lets the gate
// scale to the actual number of claims being certified.
func TestExtractSubtestCases(t *testing.T) {
	t.Run("splits a subtest-bearing function into its t.Run names", func(t *testing.T) {
		src := `package main

import "testing"

func TestHandlers(t *testing.T) {
	t.Run("HandleIndex", func(t *testing.T) {})
	t.Run("HandleEval_Success", func(t *testing.T) {})
	t.Run("HandleEval_CompileError", func(t *testing.T) {})
}
`
		got := extractSubtestCases(src)
		want := []string{"HandleIndex", "HandleEval_Success", "HandleEval_CompileError"}
		if len(got["TestHandlers"]) != len(want) {
			t.Fatalf("cases = %v, want %v", got["TestHandlers"], want)
		}
		for i, w := range want {
			if got["TestHandlers"][i] != w {
				t.Errorf("case[%d] = %q, want %q", i, got["TestHandlers"][i], w)
			}
		}
	})

	t.Run("a Test function with no subtests maps to its own name", func(t *testing.T) {
		src := `package main

import "testing"

func TestSimple(t *testing.T) {
	if 1+1 != 2 {
		t.Fail()
	}
}
`
		got := extractSubtestCases(src)
		if len(got["TestSimple"]) != 1 || got["TestSimple"][0] != "TestSimple" {
			t.Errorf("cases = %v, want [TestSimple]", got["TestSimple"])
		}
	})

	t.Run("unparseable source returns nil, not a panic", func(t *testing.T) {
		if got := extractSubtestCases("this is not valid go"); got != nil {
			t.Errorf("cases = %v, want nil", got)
		}
	})
}

func TestCaseCovered(t *testing.T) {
	covered := map[string]bool{"TestHandlers/HandleEval_Success": true}
	if !caseCovered(covered, "HandleEval_Success") {
		t.Error("expected bare subtest name to match the fuller Test/Subtest tag")
	}
	if caseCovered(covered, "HandleEval_CompileError") {
		t.Error("expected an unrelated subtest name not to match")
	}
}

func TestMissingWriteCases(t *testing.T) {
	required := map[string][]string{
		"TestHandlers": {"HandleIndex", "HandleEval_Success", "HandleEval_CompileError"},
	}
	covered := map[string]bool{"HandleIndex": true}
	missing := missingWriteCases(required, covered)
	if len(missing) != 2 {
		t.Fatalf("missing = %v, want 2 entries", missing)
	}

	// Covering every case (even via the fuller Test/Subtest tag form) clears it.
	covered["TestHandlers/HandleEval_Success"] = true
	covered["HandleEval_CompileError"] = true
	if missing := missingWriteCases(required, covered); len(missing) != 0 {
		t.Errorf("missing = %v, want none once every case is covered", missing)
	}
}

func TestPendingFuncCount(t *testing.T) {
	if got := pendingFuncCount([]string{"TestA", "TestB"}, nil); got != 2 {
		t.Errorf("cycle 1 (allowedFuncs nil): got %d, want len(requiredFuncs)=2", got)
	}
	if got := pendingFuncCount(nil, nil); got != 0 {
		t.Errorf("no required funcs and no restriction: got %d, want 0", got)
	}
	// Cycle 2+: allowedFuncs (JUDGE's rewrite list) restricts the call to a
	// subset of requiredFuncs — pending must follow the restriction, not the
	// full exit-criteria list.
	allowed := map[string]bool{"TestB": true}
	if got := pendingFuncCount([]string{"TestA", "TestB"}, allowed); got != 1 {
		t.Errorf("cycle 2+ restricted to 1 function: got %d, want 1", got)
	}
	// An empty-but-non-nil allowedFuncs is still "restricted", just to nothing.
	if got := pendingFuncCount([]string{"TestA", "TestB"}, map[string]bool{}); got != 0 {
		t.Errorf("cycle 2+ restricted to 0 functions: got %d, want 0", got)
	}
}

func TestComputeMaxTurns(t *testing.T) {
	// A single-function bead (or no functions counted yet) gets the base budget.
	if got := computeMaxTurns(1, 0); got != refinementWriteAttempts {
		t.Errorf("pending=1, totalReq=0: got %d, want base %d", got, refinementWriteAttempts)
	}
	if got := computeMaxTurns(0, 0); got != refinementWriteAttempts {
		t.Errorf("pending=0, totalReq=0: got %d, want base %d", got, refinementWriteAttempts)
	}
	// baseline-9 bead 316 (2026-09-04): a 2-function bead must get more than
	// the base budget even before any write lands (totalReq=0).
	if got, want := computeMaxTurns(2, 0), 2+refinementWriteAttempts; got != want {
		t.Errorf("pending=2, totalReq=0: got %d, want %d (the fix for the baseline-9 wedge)", got, want)
	}
	// A bead needing many functions scales further.
	if got, want := computeMaxTurns(5, 0), 5+refinementWriteAttempts; got != want {
		t.Errorf("pending=5, totalReq=0: got %d, want %d", got, want)
	}
	// totalReq (accepted-write subtest cases) still raises the floor
	// independently, and the two floors compose by taking the max, not adding.
	if got, want := computeMaxTurns(1, 10), 12; got != want {
		t.Errorf("pending=1, totalReq=10: got %d, want %d (totalReq+2)", got, want)
	}
	if got, want := computeMaxTurns(5, 10), 12; got != want {
		t.Errorf("pending=5, totalReq=10: got %d, want %d (totalReq+2 dominates the pending floor)", got, want)
	}
}

func TestWriteBeforeVerifyError(t *testing.T) {
	// baseline-9 bead 316 (2026-09-04): the exact scenario this closes — a
	// run_go_snippet call arriving before any write_function has landed must
	// be rejected, not executed.
	if got := writeBeforeVerifyError(map[string]string{}); got == "" {
		t.Error("nothing written yet: want a blocking error, got none")
	}
	if got := writeBeforeVerifyError(nil); got == "" {
		t.Error("nil writtenFuncs (equivalent to empty, cycle start): want a blocking error, got none")
	}
	// Once at least one function has been accepted, verification proceeds
	// normally — this only gates the very first call of a WRITE session.
	written := map[string]string{"TestCompile": "func TestCompile(t *testing.T) {}"}
	if got := writeBeforeVerifyError(written); got != "" {
		t.Errorf("one function already written: want no error, got %q", got)
	}
}

func TestNormalizeCompileErrors(t *testing.T) {
	// Real captures from exprvm-web-baseline-3 bead 246 (2026-09-01): turn 4 had
	// two errors, turn 5 had one — but the html/html-template mismatch is the same
	// underlying error in both, and turn 6 repeated turn 5 verbatim. After
	// normalization, turn 5 and turn 6 must produce an identical signature so the
	// early-bail fires; turn 4 (a superset) must not.
	turn4 := "# exprvm-web [exprvm-web.test]\n" +
		"./handlers_test.go:17:7: cannot use &sync.Mutex{} (value of type *\"sync\".Mutex) as \"sync\".Mutex value in assignment\n" +
		"./handlers_test.go:18:14: cannot use InitTemplates() (value of type *\"html/template\".Template) as *\"text/template\".Template value in assignment"
	turn5 := "# exprvm-web [exprvm-web.test]\n" +
		"./handlers_test.go:18:14: cannot use InitTemplates() (value of type *\"html/template\".Template) as *\"text/template\".Template value in assignment"
	turn6 := "# exprvm-web [exprvm-web.test]\n" +
		"./handlers_test.go:19:2: cannot use InitTemplates() (value of type *\"html/template\".Template) as *\"text/template\".Template value in assignment"

	if got := normalizeCompileErrors(turn5); got != normalizeCompileErrors(turn6) {
		t.Errorf("turn5 vs turn6 signatures differ despite same error at a moved line:\n%q\n%q", got, normalizeCompileErrors(turn5))
	}
	if normalizeCompileErrors(turn4) == normalizeCompileErrors(turn5) {
		t.Error("turn4 (two errors) must not match turn5 (one error)")
	}
	if normalizeCompileErrors("") != "" || normalizeCompileErrors("# pkg\n") != "" {
		t.Error("no error lines must normalize to empty")
	}
	// "too many errors" truncation marker is noise, not a distinct error.
	withMarker := turn5 + "\n./handlers_test.go:18:14: too many errors"
	if normalizeCompileErrors(withMarker) != normalizeCompileErrors(turn5) {
		t.Error("'too many errors' marker must be ignored")
	}
}

func TestStuckCompileErr(t *testing.T) {
	mismatch := "cannot use InitTemplates() (value of type *\"html/template\".Template) as *\"text/template\".Template value in assignment"
	sig := func(lines ...string) string { return strings.Join(lines, "\n") }

	// The real blocker (mismatch) recurs while the model thrashes a different
	// secondary error on the middle turn — whole-signature equality between
	// consecutive turns never matches (turn 2 differs from both neighbours),
	// but the recurrence check sees the mismatch line twice. This is the real
	// exprvm-web-baseline-5 bead 264 shape: mismatch / undefined:text / mismatch.
	thrash := []string{
		sig(mismatch),
		sig("undefined: text"),
		sig(mismatch),
	}
	if line, stuck := stuckCompileErr(thrash, stuckCompileMinRecur); !stuck || line != mismatch {
		t.Errorf("thrash case: got (%q, %v), want (%q, true)", line, stuck, mismatch)
	}

	// Genuine progress: every turn's error set is disjoint, nothing recurs.
	progressing := []string{
		sig("undefined: a", "undefined: b"),
		sig("missing return"),
		sig("undefined: c"),
		sig("syntax error: unexpected }"),
	}
	if line, stuck := stuckCompileErr(progressing, stuckCompileMinRecur); stuck {
		t.Errorf("progressing case must not be flagged as stuck; got recurring line %q", line)
	}

	if _, stuck := stuckCompileErr(nil, stuckCompileMinRecur); stuck {
		t.Error("empty window must not be stuck")
	}

	// A single failed turn is never stuck (need the error to actually recur).
	if _, stuck := stuckCompileErr([]string{sig(mismatch, "undefined: x")}, stuckCompileMinRecur); stuck {
		t.Error("one turn must not be stuck")
	}

	// Deterministic line choice: most-recurring wins.
	pick := []string{sig("err A", "err B"), sig("err A"), sig("err A", "err B")}
	if line, _ := stuckCompileErr(pick, 2); line != "err A" {
		t.Errorf("expected the most-recurring line 'err A', got %q", line)
	}
}

func TestStuckCompileSummary(t *testing.T) {
	out := "./handlers_test.go:18:14: cannot use InitTemplates() ... in assignment"
	withErr := stuckCompileSummary("some error line", out)
	if !strings.HasPrefix(withErr, stuckCompileSummaryPrefix) {
		t.Errorf("summary must start with the stable prefix Commit keys off; got %q", withErr)
	}
	// Hypothesis, not verdict: both possible causes are named, and the compile
	// output is included for the human to judge.
	if !strings.Contains(withErr, "some error line") ||
		!strings.Contains(withErr, "non-test source file") ||
		!strings.Contains(withErr, "within the test file") ||
		!strings.Contains(withErr, out) {
		t.Errorf("summary missing expected content: %q", withErr)
	}
	if strings.Contains(stuckCompileSummary("", out), "`") {
		t.Error("with no persistent error line, the summary must not emit an empty backtick pair")
	}
}

func TestMissingVerificationCases(t *testing.T) {
	allCases := map[string][]string{
		"TestHandlers": {"HandleIndex", "HandleEval_Success", "HandleEval_CompileError"},
	}

	t.Run("certifying a function with only one covered case leaves the rest missing", func(t *testing.T) {
		content := `{"findings":[],"verified_functions":["TestHandlers"],"all_correct":true,"summary":"all good"}`
		covered := map[string]bool{"HandleIndex": true}
		missing := missingVerificationCases(content, allCases, covered)
		if len(missing) != 2 {
			t.Fatalf("missing = %v, want 2 entries (this is exactly what let CRITIQUE approve all 8 bead-34 subtests off one call)", missing)
		}
	})

	t.Run("covering every case clears the function", func(t *testing.T) {
		content := `{"findings":[],"verified_functions":["TestHandlers"],"all_correct":true,"summary":"all good"}`
		covered := map[string]bool{"HandleIndex": true, "HandleEval_Success": true, "HandleEval_CompileError": true}
		if missing := missingVerificationCases(content, allCases, covered); len(missing) != 0 {
			t.Errorf("missing = %v, want none", missing)
		}
	})

	t.Run("a function with findings (not verified) requires no coverage", func(t *testing.T) {
		content := `{"findings":["TestHandlers — bug"],"verified_functions":[],"all_correct":false,"summary":"1 problem"}`
		if missing := missingVerificationCases(content, allCases, map[string]bool{}); len(missing) != 0 {
			t.Errorf("missing = %v, want none for an unverified function", missing)
		}
	})

	t.Run("malformed content falls back to the at-least-one-call floor", func(t *testing.T) {
		if missing := missingVerificationCases("not json", allCases, map[string]bool{}); len(missing) == 0 {
			t.Error("expected the fallback floor to require at least one covered case")
		}
		if missing := missingVerificationCases("not json", allCases, map[string]bool{"anything": true}); len(missing) != 0 {
			t.Errorf("missing = %v, want none once at least one case is covered", missing)
		}
	})
}

// TestRefineTestsWriteCommitHardCompletenessGate: the write loop's in-turn
// completeness nag is soft (abandoned on the last turn). Commit must escalate
// when a required test function is still missing, rather than enqueue CRITIQUE
// on an incomplete file that then gets rubber-stamped. (exprvm-web bead 144:
// TestHandlerRuntime was never written; the bead thrashed to escalation ~1h
// later on the grep guard instead.)
func TestRefineTestsWriteCommitHardCompletenessGate(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	dir := t.TempDir()

	must := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module widget\n\ngo 1.22\n")
	must("handlers.go", "package widget\n\nfunc HandleIndex() {}\n")
	// The test file compiles but is missing the required TestHandlerRuntime.
	must("handlers_test.go", "package widget\n\nimport \"testing\"\n\nfunc TestHandleIndex(t *testing.T) {}\n")

	if _, err := d.ExecContext(ctx, `
		INSERT INTO projects (id, label, folder_path, design_doc_path, status,
			monitor_override_default, execution_budget_default, audit_reconcile_round_cap,
			created_at, updated_at)
		VALUES (-1, 'fixture: REFINE_TESTS_WRITE hard completeness gate', ?, 'design_doc.md', 'active',
			'honor', 300, 2, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, dir); err != nil {
		t.Fatal(err)
	}
	specBytes, _ := json.Marshal(ParsedBead{
		Title:       "handlers-templates",
		FullText:    "Implement HandleIndex. Also write TestHandlerRuntime, an integration test.",
		OutputFiles: []string{"handlers.go", "handlers_test.go"},
		ExitCriteria: []string{
			"grep -q 'func TestHandleIndex' handlers_test.go && go test -run TestHandleIndex .",
			"grep -q 'func TestHandlerRuntime' handlers_test.go && go test -run TestHandlerRuntime .",
		},
	})
	spec := string(specBytes)
	res, err := d.ExecContext(ctx, `INSERT INTO beads (project_id, status, current_revision_id) VALUES (-1, 'pending', NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	beadID, _ := res.LastInsertId()
	rr, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions (project_id, bead_id, revision_number, full_text,
			execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (-1, ?, 1, ?, 300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`, beadID, spec)
	if err != nil {
		t.Fatal(err)
	}
	revID, _ := rr.LastInsertId()
	if _, err := d.ExecContext(ctx, `UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
		t.Fatal(err)
	}

	job := seedJob(t, d, -1, db.VerbRefineTestsWrite, sql.NullInt64{Int64: beadID, Valid: true})
	inTx(t, d, func(tx *sql.Tx) error {
		return (&RefineTestsWrite{}).Commit(ctx, tx, job, RefineTestsWriteOutput{Summary: "wrote what I could"})
	})

	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, job.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "escalated" {
		t.Errorf("job status = %q, want escalated (required TestHandlerRuntime missing)", status)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE verb = ? AND bead_id = ?`, db.VerbRefineTestsCritique, beadID); n != 0 {
		t.Errorf("CRITIQUE jobs = %d, want 0 (must not proceed on an incomplete file)", n)
	}
}
