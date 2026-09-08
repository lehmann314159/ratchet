package ollama

import (
	"encoding/json"
	"strings"
)

// recoverToolCallsFromContent salvages tool calls that a model emitted as
// literal text in the assistant content channel instead of through Ollama's
// native message.tool_calls field.
//
// Some models never populate message.tool_calls at all under Ollama 0.30.x —
// their chat template serialises the call as a bare JSON object
// {"name": "...", "arguments": {...}}, sometimes wrapped in
// <tool_call>…</tool_call> or a ```json fence. qwen2.5-coder:32b-instruct is the
// confirmed case (execute-bead bakeoff, 2026-09-07: every turn emitted a
// well-formed write_file object as content, the loop saw zero tool calls and
// stalled). Without recovery the caller's loop reads the turn as a final answer,
// which for EXECUTE_BEAD is an empty/planning turn → stall.
//
// It is deliberately conservative. A candidate object is accepted only if its
// name matches one of the tool definitions passed on this call AND it carries an
// "arguments" value. That {name, arguments} shape does not collide with any
// verb's final-answer JSON (CRITIQUE / JUDGE / ADJUDICATE emit objects keyed by
// domain fields, never a tool name + arguments), so running the recovery
// unconditionally on every ChatWithTools turn that produced no native tool call
// is safe.
func recoverToolCallsFromContent(content string, tools []Tool) []ToolCall {
	if strings.TrimSpace(content) == "" || len(tools) == 0 {
		return nil
	}
	known := make(map[string]bool, len(tools))
	for _, t := range tools {
		if t.Function.Name != "" {
			known[t.Function.Name] = true
		}
	}

	// Strip <think>…</think> first: a reasoning model may quote an example
	// tool-call object inside its thinking, which must not be recovered as a
	// real call.
	scan := stripThinkBlocks(content)

	var candidates []string
	// 1. <tool_call>…</tool_call> wrappers (Qwen native format leaking to content).
	rest := scan
	for {
		i := strings.Index(rest, "<tool_call>")
		if i < 0 {
			break
		}
		rest = rest[i+len("<tool_call>"):]
		if j := strings.Index(rest, "</tool_call>"); j >= 0 {
			candidates = append(candidates, rest[:j])
			rest = rest[j+len("</tool_call>"):]
		} else {
			candidates = append(candidates, rest)
			break
		}
	}
	// 2. Every balanced top-level {…} object in the content.
	for i := 0; i < len(scan); i++ {
		if scan[i] != '{' {
			continue
		}
		end := matchingJSONEnd(scan, i)
		if end < 0 {
			break
		}
		candidates = append(candidates, scan[i:end+1])
		i = end
	}

	var out []ToolCall
	seen := make(map[string]bool)
	for _, cand := range candidates {
		tc, ok := parseToolCallObject(cand, known)
		if !ok {
			continue
		}
		// Dedup on the normalised call, not the raw substring: the same object
		// is found twice when it is inside a <tool_call> wrapper (step 1) and
		// also a balanced top-level object (step 2). json.Marshal of a map sorts
		// keys, so this key is stable.
		argsJSON, _ := json.Marshal(tc.Function.Arguments)
		key := tc.Function.Name + "\x00" + string(argsJSON)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tc)
	}
	return out
}

// parseToolCallObject extracts the first balanced JSON object from s and, if it
// has the shape of a tool call for a known tool, returns it. Accepts the flat
// {name, arguments} form and the nested {function:{name, arguments}} /
// {tool_call:{…}} forms. "arguments" may be an object or a JSON-encoded string.
func parseToolCallObject(s string, known map[string]bool) (ToolCall, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ToolCall{}, false
	}
	end := matchingJSONEnd(s, start)
	if end < 0 {
		return ToolCall{}, false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(s[start:end+1]), &raw) != nil {
		return ToolCall{}, false
	}

	nameRaw, argsRaw := raw["name"], raw["arguments"]
	for _, key := range []string{"function", "tool_call"} {
		inner, ok := raw[key]
		if !ok {
			continue
		}
		var f map[string]json.RawMessage
		if json.Unmarshal(inner, &f) != nil {
			continue
		}
		if fn, ok := f["function"]; ok { // {tool_call:{function:{…}}}
			var ff map[string]json.RawMessage
			if json.Unmarshal(fn, &ff) == nil {
				f = ff
			}
		}
		nameRaw, argsRaw = f["name"], f["arguments"]
	}

	var name string
	if json.Unmarshal(nameRaw, &name) != nil || !known[name] {
		return ToolCall{}, false
	}

	args := map[string]any{}
	if len(argsRaw) > 0 {
		if json.Unmarshal(argsRaw, &args) != nil {
			var asStr string
			if json.Unmarshal(argsRaw, &asStr) == nil {
				_ = json.Unmarshal([]byte(asStr), &args)
			}
		}
	}
	return ToolCall{Function: ToolCallFunction{Name: name, Arguments: args}}, true
}

// stripThinkBlocks removes <think>…</think> spans (and a trailing unclosed
// <think>) so their contents are excluded from tool-call recovery.
func stripThinkBlocks(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start < 0 {
			break
		}
		rest := s[start:]
		if end := strings.Index(rest, "</think>"); end >= 0 {
			s = s[:start] + rest[end+len("</think>"):]
		} else {
			s = s[:start]
			break
		}
	}
	return s
}
