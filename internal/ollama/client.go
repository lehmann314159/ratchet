package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const DefaultTemperature = 0.3

type Client struct {
	BaseURL    string
	httpClient *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL:    baseURL,
		httpClient: &http.Client{},
	}
}

type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Tool defines a function the model may call.
type Tool struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  ToolParameters `json:"parameters"`
}

type ToolParameters struct {
	Type       string                `json:"type"` // "object"
	Properties map[string]ToolProperty `json:"properties"`
	Required   []string              `json:"required"`
}

type ToolProperty struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// ToolCall is a tool invocation returned by the model.
type ToolCall struct {
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type Options struct {
	Temperature float64
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []Message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

type chatResponse struct {
	Message Message `json:"message"`
	Done    bool    `json:"done"`
	Error   string  `json:"error,omitempty"`
}

// Chat sends a non-streaming chat request and returns the assistant's reply.
func (c *Client) Chat(ctx context.Context, model string, msgs []Message, opts *Options) (string, error) {
	temp := DefaultTemperature
	if opts != nil && opts.Temperature > 0 {
		temp = opts.Temperature
	}

	req := chatRequest{
		Model:    model,
		Messages: msgs,
		Stream:   false,
		Options:  map[string]any{"temperature": temp},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("chat: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if cr.Error != "" {
		return "", fmt.Errorf("ollama: %s", cr.Error)
	}

	return cr.Message.Content, nil
}

// ChatWithTools sends a non-streaming chat request with tool definitions and
// returns the full assistant Message, which may contain ToolCalls instead of
// (or in addition to) Content. The caller is responsible for the multi-turn
// loop: executing tool calls and feeding results back as tool messages.
func (c *Client) ChatWithTools(ctx context.Context, model string, msgs []Message, tools []Tool, opts *Options) (Message, error) {
	temp := DefaultTemperature
	if opts != nil && opts.Temperature > 0 {
		temp = opts.Temperature
	}

	req := struct {
		Model    string         `json:"model"`
		Messages []Message      `json:"messages"`
		Tools    []Tool         `json:"tools"`
		Stream   bool           `json:"stream"`
		Options  map[string]any `json:"options,omitempty"`
	}{
		Model:    model,
		Messages: msgs,
		Tools:    tools,
		Stream:   false,
		Options:  map[string]any{"temperature": temp},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return Message{}, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return Message{}, fmt.Errorf("chat: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Message{}, fmt.Errorf("read: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Message{}, fmt.Errorf("ollama %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return Message{}, fmt.Errorf("parse response: %w", err)
	}
	if cr.Error != "" {
		return Message{}, fmt.Errorf("ollama: %s", cr.Error)
	}

	return cr.Message, nil
}

// ExtractJSON strips Qwen3-style <think>…</think> blocks and markdown code
// fences, returning the innermost JSON text.
func ExtractJSON(raw string) string {
	s := raw
	// Strip <think>...</think> (Qwen3-Coder uses these).
	for {
		start := strings.Index(s, "<think>")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "</think>")
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + s[start+end+len("</think>"):]
	}
	s = strings.TrimSpace(s)
	// Strip markdown fences.
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
