package ollama

import "testing"

func execTools() []Tool {
	mk := func(n string) Tool { return Tool{Type: "function", Function: ToolFunction{Name: n}} }
	return []Tool{mk("write_file"), mk("read_file"), mk("run_command")}
}

func TestRecoverToolCallsFromContent(t *testing.T) {
	tools := execTools()

	t.Run("bare flat object (qwen2.5-coder shape)", func(t *testing.T) {
		c := `{"name": "write_file", "arguments": {"path": "grammar.go", "content": "package main\n"}}`
		got := recoverToolCallsFromContent(c, tools)
		if len(got) != 1 || got[0].Function.Name != "write_file" {
			t.Fatalf("got %+v", got)
		}
		if got[0].Function.Arguments["path"] != "grammar.go" {
			t.Fatalf("args not recovered: %+v", got[0].Function.Arguments)
		}
		if got[0].Function.Arguments["content"] != "package main\n" {
			t.Fatalf("content not recovered: %q", got[0].Function.Arguments["content"])
		}
	})

	t.Run("wrapped in <tool_call>", func(t *testing.T) {
		c := "<tool_call>\n{\"name\": \"read_file\", \"arguments\": {\"path\": \"expr.go\"}}\n</tool_call>"
		got := recoverToolCallsFromContent(c, tools)
		if len(got) != 1 || got[0].Function.Name != "read_file" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("nested function form", func(t *testing.T) {
		c := `{"function": {"name": "run_command", "arguments": {"command": "go build ./..."}}}`
		got := recoverToolCallsFromContent(c, tools)
		if len(got) != 1 || got[0].Function.Name != "run_command" ||
			got[0].Function.Arguments["command"] != "go build ./..." {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("arguments as JSON-encoded string", func(t *testing.T) {
		c := `{"name": "write_file", "arguments": "{\"path\": \"a.go\", \"content\": \"x\"}"}`
		got := recoverToolCallsFromContent(c, tools)
		if len(got) != 1 || got[0].Function.Arguments["path"] != "a.go" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("two sequential calls with prose", func(t *testing.T) {
		c := `First: {"name":"read_file","arguments":{"path":"a"}} then {"name":"write_file","arguments":{"path":"b","content":"c"}}`
		got := recoverToolCallsFromContent(c, tools)
		if len(got) != 2 || got[0].Function.Name != "read_file" || got[1].Function.Name != "write_file" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("ignores non-tool JSON (verb final answer)", func(t *testing.T) {
		c := `{"all_correct": false, "findings": [{"name": "issue", "detail": "x"}], "summary": "bad"}`
		if got := recoverToolCallsFromContent(c, tools); got != nil {
			t.Fatalf("should not recover: %+v", got)
		}
	})

	t.Run("ignores tool-call example inside <think>", func(t *testing.T) {
		c := `<think>I could call {"name":"write_file","arguments":{"path":"x"}} but let me plan more.</think>Planning...`
		if got := recoverToolCallsFromContent(c, tools); got != nil {
			t.Fatalf("should not recover from think block: %+v", got)
		}
	})

	t.Run("unknown tool name rejected", func(t *testing.T) {
		c := `{"name": "delete_everything", "arguments": {}}`
		if got := recoverToolCallsFromContent(c, tools); got != nil {
			t.Fatalf("should not recover unknown tool: %+v", got)
		}
	})

	t.Run("empty / no tools", func(t *testing.T) {
		if got := recoverToolCallsFromContent("", tools); got != nil {
			t.Fatalf("got %+v", got)
		}
		if got := recoverToolCallsFromContent(`{"name":"write_file","arguments":{}}`, nil); got != nil {
			t.Fatalf("got %+v", got)
		}
	})
}
