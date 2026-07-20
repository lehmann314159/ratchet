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