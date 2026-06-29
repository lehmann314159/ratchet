package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultTemperature = 0.3

// Context window sizes sent with every request via num_ctx. These cap the KV
// cache allocation so multiple models can coexist in unified memory.
// Chat() (single-turn handoff prompts) fits well under 16K.
// ChatWithTools() accumulates tool-call history and needs more headroom.
const (
	chatNumCtx     = 16384
	executeNumCtx  = 32768
)

type Client struct {
	BaseURL    string
	httpClient *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Minute},
	}
}

// NewUnbounded returns a Client with no HTTP timeout. Use for execute-bead
// and monitor, which have their own budget/lifecycle controls and can legitimately
// run a single model call for many minutes.
func NewUnbounded(baseURL string) *Client {
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

// Warmup sends a trivial "hello" chat request to the given model with a
// 1-minute timeout. This forces the model into VRAM before the real request,
// so a cold model-swap costs at most 1 minute instead of 30. If the warmup
// times out, the caller should treat it as an infrastructure error and retry.
func (c *Client) Warmup(ctx context.Context, model string) error {
	wc := &http.Client{Timeout: time.Minute}
	req := chatRequest{
		Model:    model,
		Messages: []Message{{Role: "user", Content: "hello"}},
		Stream:   false,
		Options:  map[string]any{"temperature": 0.0, "num_ctx": chatNumCtx},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("warmup marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("warmup: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := wc.Do(httpReq)
	if err != nil {
		return fmt.Errorf("warmup: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("warmup: ollama returned %d", resp.StatusCode)
	}
	return nil
}

// Chat sends a non-streaming chat request and returns the assistant's complete reply.
// Handoff verbs (DECOMPOSE, AUDIT, RECONCILE, etc.) use this path. Streaming is
// intentionally off here: per-token HTTP flushing adds overhead that compounds
// across the thousands of tokens a large model generates, and these calls already
// have a 30-minute client timeout. Observability for handoff verbs comes from the
// structured outputs stored in handoff_attempts, not from a token stream.
func (c *Client) Chat(ctx context.Context, model string, msgs []Message, opts *Options) (string, error) {
	temp := DefaultTemperature
	if opts != nil && opts.Temperature > 0 {
		temp = opts.Temperature
	}

	req := chatRequest{
		Model:    model,
		Messages: msgs,
		Stream:   false,
		Options:  map[string]any{"temperature": temp, "num_ctx": chatNumCtx},
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

// ChatWithTools sends a streaming chat request with tool definitions and
// returns the full assistant Message, which may contain ToolCalls instead of
// (or in addition to) Content. Content tokens are written to tokenWriter as
// they arrive if non-nil, giving real-time observability. The caller is
// responsible for the multi-turn loop: executing tool calls and feeding
// results back as tool messages.
func (c *Client) ChatWithTools(ctx context.Context, model string, msgs []Message, tools []Tool, opts *Options, tokenWriter io.Writer) (Message, error) {
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
		Stream:   true,
		Options:  map[string]any{"temperature": temp, "num_ctx": executeNumCtx},
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

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return Message{}, fmt.Errorf("ollama %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var contentSB strings.Builder
	var toolCalls []ToolCall
	dec := json.NewDecoder(resp.Body)
	for {
		// Check context before each decode so a cancelled budget timer unblocks
		// the loop even if the underlying HTTP transport hasn't closed the
		// connection yet (e.g. data already buffered in the kernel receive buffer).
		if err := ctx.Err(); err != nil {
			return Message{}, err
		}
		var chunk chatResponse
		if err := dec.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return Message{}, fmt.Errorf("decode stream: %w", err)
		}
		if chunk.Error != "" {
			return Message{}, fmt.Errorf("ollama: %s", chunk.Error)
		}
		if chunk.Message.Content != "" {
			contentSB.WriteString(chunk.Message.Content)
			if tokenWriter != nil {
				io.WriteString(tokenWriter, chunk.Message.Content)
			}
		}
		if len(chunk.Message.ToolCalls) > 0 {
			toolCalls = append(toolCalls, chunk.Message.ToolCalls...)
		}
		if chunk.Done {
			break
		}
	}
	// Terminate streamed content with a newline so subsequent trace lines start clean.
	if tokenWriter != nil && contentSB.Len() > 0 {
		fmt.Fprintln(tokenWriter)
	}
	return Message{
		Role:      "assistant",
		Content:   contentSB.String(),
		ToolCalls: toolCalls,
	}, nil
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
