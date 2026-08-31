package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestNewUsesHourLongTimeout locks in the bump from 30 to 60 minutes
// (2026-08-28, connect-four-v5 and tictactoe-v1 both hit REFINE_TESTS_CRITIQUE
// exceeding the old 30-minute bound on a legitimately slow, not stalled,
// tool-calling loop). The timeout must stay bounded, not become 0
// (unbounded) — see handoffClientTimeout's doc comment for why every
// handoff verb except EXECUTE_BEAD/MONITOR_EXECUTION needs this as its
// only protection against a genuinely stalled request.
func TestNewUsesHourLongTimeout(t *testing.T) {
	c := New("http://example.invalid")
	if got := c.httpClient.Timeout; got != time.Hour {
		t.Errorf("New() httpClient.Timeout = %v, want 1h", got)
	}
}

// TestChatSetsJSONFormat confirms Chat() asks Ollama for grammar-constrained
// JSON output — the fix for a real ADJUDICATE_NEXT_EXECUTION escalation
// where a model quoted Go source with unescaped tabs/quotes inside a JSON
// string field (see ExtractJSON's escapeRawControlCharsInStrings doc
// comment and Chat()'s own doc comment for the full incident). Without
// format:"json" in the request body, that malformed-output class is left to
// chance; this locks in that every Chat() call asks for the constraint.
func TestChatSetsJSONFormat(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":{"role":"assistant","content":"{}"},"done":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	if _, err := c.Chat(context.Background(), "some-model", []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotBody["format"] != "json" {
		t.Errorf(`request body format = %v, want "json"`, gotBody["format"])
	}
}

// TestChatWithToolsSetsJSONFormat is TestChatSetsJSONFormat's counterpart
// for the tool-calling path (REFINE_TESTS_WRITE, REFINE_TESTS_CRITIQUE,
// ADJUDICATE_NEXT_EXECUTION) — confirmed live that format:"json" and tools
// coexist without Ollama refusing to emit a real tool call; this locks in
// that the request actually carries the constraint.
func TestChatWithToolsSetsJSONFormat(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":{"role":"assistant","content":"{}"},"done":true}` + "\n"))
	}))
	defer srv.Close()

	c := New(srv.URL)
	tools := []Tool{{Type: "function", Function: ToolFunction{Name: "run_go_snippet"}}}
	if _, err := c.ChatWithTools(context.Background(), "some-model", []Message{{Role: "user", Content: "hi"}}, tools, nil, nil); err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if gotBody["format"] != "json" {
		t.Errorf(`request body format = %v, want "json"`, gotBody["format"])
	}
}

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

// captureLogs swaps the default slog logger for one writing to a buffer,
// restoring the previous default on test cleanup, and returns the buffer.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestChatWithToolsLogsEmptyTurnWithDoneReason reproduces a real
// ADJUDICATE_NEXT_EXECUTION escalation (connect-four-v1, bead 47,
// 2026-08-26): all 3 attempts stored a 0-byte raw_output with no logged
// indication of why — json.Unmarshal on that empty string produced only a
// generic "unexpected end of JSON input" many steps downstream. Confirms
// ChatWithTools now logs done_reason at the point of occurrence whenever a
// turn has neither content nor a tool call, so a future one is directly
// diagnosable (e.g. "length" means the model ran out of context/output
// budget mid-turn) instead of requiring after-the-fact inference.
func TestChatWithToolsLogsEmptyTurnWithDoneReason(t *testing.T) {
	buf := captureLogs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"message":{"role":"assistant","content":"","tool_calls":null},"done":true,"done_reason":"length"}`+"\n")
	}))
	defer srv.Close()

	c := New(srv.URL)
	tools := []Tool{{Type: "function", Function: ToolFunction{Name: "run_go_snippet"}}}
	msg, err := c.ChatWithTools(context.Background(), "some-model", []Message{{Role: "user", Content: "hi"}}, tools, nil, nil)
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if msg.Content != "" || len(msg.ToolCalls) != 0 {
		t.Fatalf("expected an empty turn, got content=%q toolCalls=%v", msg.Content, msg.ToolCalls)
	}
	logged := buf.String()
	if !strings.Contains(logged, "neither content nor a tool call") {
		t.Errorf("expected empty-turn warning in log, got: %s", logged)
	}
	if !strings.Contains(logged, "done_reason=length") {
		t.Errorf("expected done_reason=length in log, got: %s", logged)
	}
}

// TestChatWithToolsNoWarningOnNormalTurn confirms a well-formed turn
// (either real content or a real tool call) never logs the empty-turn
// warning — it must only fire for the genuinely degenerate case.
func TestChatWithToolsNoWarningOnNormalTurn(t *testing.T) {
	buf := captureLogs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"message":{"role":"assistant","content":"looks fine"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	defer srv.Close()

	c := New(srv.URL)
	tools := []Tool{{Type: "function", Function: ToolFunction{Name: "run_go_snippet"}}}
	if _, err := c.ChatWithTools(context.Background(), "some-model", []Message{{Role: "user", Content: "hi"}}, tools, nil, nil); err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if strings.Contains(buf.String(), "neither content nor a tool call") {
		t.Errorf("unexpected empty-turn warning for a normal turn: %s", buf.String())
	}
}

func boolPtr(b bool) *bool { return &b }

// TestChatOmitsThinkByDefault: a caller passing no Options (every verb today)
// must produce a request with no `think` key at all, so Ollama leaves the
// model's own default in force. Regression guard for Phase 0 of
// docs/schema-mode-reasoning-field.md — the plumbing must be additive.
func TestChatOmitsThinkByDefault(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":{"role":"assistant","content":"{}"},"done":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	if _, err := c.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, present := gotBody["think"]; present {
		t.Errorf("request body has a `think` key with nil Options.Think; want it omitted: %v", gotBody["think"])
	}
}

// TestChatSendsThinkTopLevel: Options.Think must land as a TOP-LEVEL request
// field (not inside `options`, where Ollama silently drops it — the bug this
// plumbing replaces).
func TestChatSendsThinkTopLevel(t *testing.T) {
	for _, want := range []bool{true, false} {
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &gotBody)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"message":{"role":"assistant","content":"{}"},"done":true}`))
		}))

		c := New(srv.URL)
		_, err := c.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}},
			&Options{Think: boolPtr(want)})
		srv.Close()
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if got, ok := gotBody["think"].(bool); !ok || got != want {
			t.Errorf("top-level think = %v (ok=%v), want %v", gotBody["think"], ok, want)
		}
		if opts, ok := gotBody["options"].(map[string]any); ok {
			if _, leaked := opts["think"]; leaked {
				t.Errorf("`think` leaked into options: %v", opts["think"])
			}
		}
	}
}

// TestChatWithToolsThinkPlumbing: the tool-calling path must omit `think`
// with nil Options (previously it hard-coded options.think=false, a no-op)
// and send it top-level when set.
func TestChatWithToolsThinkPlumbing(t *testing.T) {
	capture := func(opts *Options) map[string]any {
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &gotBody)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"message":{"role":"assistant","content":"{}"},"done":true}`+"\n")
		}))
		defer srv.Close()
		c := New(srv.URL)
		if _, err := c.ChatWithTools(context.Background(), "m",
			[]Message{{Role: "user", Content: "hi"}}, nil, opts, nil); err != nil {
			t.Fatalf("ChatWithTools: %v", err)
		}
		return gotBody
	}

	def := capture(nil)
	if _, present := def["think"]; present {
		t.Errorf("nil Options: `think` present top-level, want omitted: %v", def["think"])
	}
	if opts, ok := def["options"].(map[string]any); ok {
		if _, leaked := opts["think"]; leaked {
			t.Errorf("nil Options: stale `think` in options map: %v", opts["think"])
		}
	}

	set := capture(&Options{Think: boolPtr(false)})
	if got, ok := set["think"].(bool); !ok || got != false {
		t.Errorf("Think=false: top-level think = %v (ok=%v), want false", set["think"], ok)
	}
}

// TestChatDiscardsThinkingField: a response carrying a separated `thinking`
// field must not break Chat() — it returns Content only, thinking is logged
// and dropped.
func TestChatDiscardsThinkingField(t *testing.T) {
	buf := captureLogs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":{"role":"assistant","content":"{\"ok\":true}","thinking":"let me consider..."},"done":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	got, err := c.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got != `{"ok":true}` {
		t.Errorf("Chat content = %q, want the JSON only", got)
	}
	if !strings.Contains(buf.String(), "separated thinking") {
		t.Errorf("expected a thinking-discarded log line, got: %s", buf.String())
	}
}

// TestChatWithToolsCapturesThinking: streamed `thinking` chunks accumulate
// onto the returned Message.Thinking while Content stays exactly the answer.
func TestChatWithToolsCapturesThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"message":{"role":"assistant","thinking":"step one "}}`+"\n")
		io.WriteString(w, `{"message":{"role":"assistant","thinking":"step two"}}`+"\n")
		io.WriteString(w, `{"message":{"role":"assistant","content":"{\"done\":1}"},"done":true}`+"\n")
	}))
	defer srv.Close()

	c := New(srv.URL)
	msg, err := c.ChatWithTools(context.Background(), "m",
		[]Message{{Role: "user", Content: "hi"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if msg.Content != `{"done":1}` {
		t.Errorf("Content = %q, want the answer only", msg.Content)
	}
	if msg.Thinking != "step one step two" {
		t.Errorf("Thinking = %q, want the two chunks joined", msg.Thinking)
	}
}
