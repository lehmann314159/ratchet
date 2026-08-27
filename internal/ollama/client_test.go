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

// TestExtractJSONRawTabInString reproduces a real ADJUDICATE_NEXT_EXECUTION
// escalation (connect-four-v1, bead 47, 2026-08-26): the model's
// "reasoning" field quoted a Go function verbatim, tabs and all, without
// escaping them — RFC 8259 requires every control character inside a JSON
// string to be escaped, and Go's strict encoding/json rejected the raw tab
// outright ("invalid character '\t' in string literal") on all 3 retries
// before the job exhausted tolerance and escalated for what was actually a
// purely mechanical formatting defect, never a real judgment call.
func TestExtractJSONRawTabInString(t *testing.T) {
	raw := "```json\n" +
		"{\"reasoning\": \"func (g *Game) IsFull() bool {\\n\tfor r := 0; r < NumRows; r++ {\\n\t\treturn false\\n\t}\\n}\", \"decision\": \"execute_as_is\"}" +
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

// TestExtractJSONControlCharsOutsideStringsUntouched confirms the raw-tab
// fix only rewrites bytes inside JSON string literals — structural
// whitespace between tokens (indentation, newlines separating array
// elements) is legal JSON as-is and must be left exactly as the model wrote
// it, not escaped into a literal "\t"/"\n" two-character sequence, which
// would corrupt the JSON structure rather than fix it.
func TestExtractJSONControlCharsOutsideStringsUntouched(t *testing.T) {
	raw := "```json\n{\n\t\"a\": 1,\n\t\"b\": 2\n}\n```"
	got := ExtractJSON(raw)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", got, err)
	}
	if m["a"] != float64(1) || m["b"] != float64(2) {
		t.Errorf("m = %v", m)
	}
	if got != "{\n\t\"a\": 1,\n\t\"b\": 2\n}" {
		t.Errorf("structural whitespace was rewritten: got %q", got)
	}
}
