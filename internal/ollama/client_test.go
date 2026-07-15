package ollama

import (
	"encoding/json"
	"testing"
)

func TestExtractJSONPlain(t *testing.T) {
	got := ExtractJSON(`{"decision": "execute_as_is"}`)
	want := `{"decision": "execute_as_is"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractJSONFenced(t *testing.T) {
	raw := "```json\n{\"decision\": \"execute_as_is\"}\n```"
	got := ExtractJSON(raw)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", got, err)
	}
	if m["decision"] != "execute_as_is" {
		t.Errorf("decision = %v", m["decision"])
	}
}

func TestExtractJSONThinkBlock(t *testing.T) {
	raw := "<think>let me reason about this...</think>\n```json\n{\"x\": 1}\n```"
	got := ExtractJSON(raw)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", got, err)
	}
}

// TestExtractJSONTrailingCodeFence reproduces a Stage 9 audit finding: the
// old implementation found the JSON's end by searching for the LAST "```" in
// the entire raw string. Any trailing commentary after the real closing
// fence that itself contains a code fence — extremely plausible, e.g. the
// model quoting a failing test or a code snippet as part of its explanation
// — made that trailing fence "win", sweeping the prose in between into what
// was returned as JSON and breaking json.Unmarshal on every affected verb
// (ExtractJSON is used by essentially every JSON-handoff verb in the
// system). The fix scans structurally for the matching brace instead of
// searching for markdown fence markers at all.
func TestExtractJSONTrailingCodeFence(t *testing.T) {
	raw := "```json\n" +
		`{"decision": "execute_as_is", "reasoning_text": "ok"}` +
		"\n```\n\nFor reference, here's the failing test:\n```go\nfunc TestX(t *testing.T) {}\n```"
	got := ExtractJSON(raw)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", got, err)
	}
	if m["decision"] != "execute_as_is" {
		t.Errorf("decision = %v", m["decision"])
	}
}

// TestExtractJSONEmbeddedBackticksInString confirms a JSON string value that
// itself contains a triple-backtick sequence (e.g. reasoning text quoting a
// code snippet) is preserved intact rather than truncated mid-string.
func TestExtractJSONEmbeddedBackticksInString(t *testing.T) {
	raw := "```json\n" +
		`{"reasoning_text": "the model output a snippet like ` + "```go\\nfunc f() {}\\n```" + ` in its trace", "decision": "execute_as_is"}` +
		"\n```"
	got := ExtractJSON(raw)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", got, err)
	}
	if m["decision"] != "execute_as_is" {
		t.Errorf("decision = %v", m["decision"])
	}
}

// TestExtractJSONNestedBraces confirms nested objects/arrays don't confuse
// the depth tracking (the matching close must be the outermost one).
func TestExtractJSONNestedBraces(t *testing.T) {
	raw := "```json\n" + `{"a": {"b": [1, 2, {"c": "}"}]}, "d": "ok"}` + "\n```\ntrailing text ```with fence```"
	got := ExtractJSON(raw)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", got, err)
	}
	if m["d"] != "ok" {
		t.Errorf("d = %v", m["d"])
	}
}

func TestExtractJSONTruncated(t *testing.T) {
	// No closing brace at all (model output cut off) — best-effort passthrough,
	// must not panic and must strip the leading fence noise.
	raw := "```json\n{\"decision\": \"execute_as_is\", \"reasoning"
	got := ExtractJSON(raw)
	if got == "" {
		t.Error("expected non-empty best-effort output for truncated input")
	}
	if got[0] != '{' {
		t.Errorf("expected output to start at the JSON, got %q", got)
	}
}

func TestExtractJSONArrayTopLevel(t *testing.T) {
	raw := "```json\n[1, 2, 3]\n```\nnote: ```not json```"
	got := ExtractJSON(raw)
	var arr []int
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", got, err)
	}
	if len(arr) != 3 {
		t.Errorf("len(arr) = %d, want 3", len(arr))
	}
}
