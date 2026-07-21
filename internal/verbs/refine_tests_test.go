package verbs

import (
	"context"
	"strings"
	"testing"
	"time"
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