package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const DefaultTemperature = 0.3

// Context window sizes sent with every request via num_ctx. These cap the KV
// cache allocation. defaultNumCtx covers every verb by default (both Chat()
// handoff prompts and ChatWithTools() tool-call accumulation) — headroom
// checked against this deployment's actual unified-memory budget, not a
// generic guess (see internal/ollama's git history for the sizing rationale).
// MonitorNumCtx is the one deliberate exception: MONITOR_EXECUTION runs a
// tight, frequent polling loop (a short trace snippet in, a FIRE/NO_FIRE
// decision out) and never touches a design doc or bead history, so it never
// needs — and shouldn't pay the KV-cache cost of — the full default.
//
// 40960 is qwen3:32b's native trained context — the fleet's tightest window
// (gemma4:31b is 262144, mistral-small3.2:24b 131072). Setting num_ctx to
// exactly the native max avoids rope-scaling degradation while giving the
// downstream verbs room for the design-doc excerpts now fed to
// REFINE_TESTS_{WRITE,CRITIQUE,JUDGE} and ADJUDICATE_NEXT_EXECUTION alongside
// the bead spec, impl context, and current test file. RAM is not the
// constraint here — one model is resident at a time and the host has 119 GiB
// unified memory.
const (
	defaultNumCtx = 40960
	MonitorNumCtx = 16384
)

// toolLoopNumPredict caps generated tokens per ChatWithTools turn. A tool-loop
// turn's legitimate output is small — a tool call or two, or a short final JSON
// answer, preceded by at most a few thousand tokens of reasoning (observed max
// ~1,900 on this fleet). 8192 leaves generous headroom while bounding a
// degenerate turn — specifically a reasoning model whose thinking stream never
// terminates to call a tool (gemma4:31b on REFINE_TESTS_WRITE, exprvm-web-baseline
// bead 224, 2026-08-31: 14.5K tokens and still climbing, wedging Ollama's single
// slot for the full 60-minute client timeout). At the cap Ollama returns
// done_reason:"length" and ends the turn cleanly, so the caller's loop proceeds
// to its normal (eventually escalating) path instead of hanging. Callers that
// legitimately need more can override via Options.NumPredict.
const toolLoopNumPredict = 8192

// streamProgressLogEvery is how many stream chunks ChatWithTools consumes
// between "still in progress" log lines. At ~1 token/chunk and a large model's
// ~9 tok/s that is roughly one line per minute — enough to make a runaway
// generation (a model that streams steadily but never stops) visible while it
// is happening, instead of only inferable after the client timeout fires many
// minutes later.
const streamProgressLogEvery = 500

// logSnippetReplacer keeps a stream-tail sample on one log line while still
// showing whitespace runaways (endless "\n" or "\t") rather than collapsing them.
var logSnippetReplacer = strings.NewReplacer("\n", "\\n", "\r", "\\r", "\t", "\\t")

// lastRunes returns up to the final n runes of s as a single-line log snippet.
func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[len(r)-n:]
	}
	return logSnippetReplacer.Replace(string(r))
}

type Client struct {
	BaseURL    string
	httpClient *http.Client
}

// handoffClientTimeout bounds every handoff verb's HTTP client (everything
// except EXECUTE_BEAD/MONITOR_EXECUTION, which use NewUnbounded — see its
// doc comment for why those two are exempt). This is the only thing
// standing between a genuinely stalled Ollama request and a handoff verb
// hanging the (single-threaded) orchestrator forever, so it should stay
// bounded, not be removed — but 30 minutes turned out to be too tight for
// a legitimately slow, not stalled, multi-turn tool-calling loop: REFINE_TESTS_CRITIQUE
// hit this for real twice (connect-four-v5 and tictactoe-v1, both
// 2026-08-28), each time burning ~37 minutes on an attempt that was still
// making progress when the client cut it off, then repeating the same
// wait on a full retry. Raised from 30 to 60 minutes so a legitimately
// slow attempt usually finishes instead of being thrown away and redone.
const handoffClientTimeout = 60 * time.Minute

func New(baseURL string) *Client {
	return &Client{
		BaseURL:    baseURL,
		httpClient: &http.Client{Timeout: handoffClientTimeout},
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
	Role    string `json:"role"`
	Content string `json:"content"`
	// Thinking carries a reasoning model's separated chain-of-thought when
	// Ollama returns it in a distinct channel (top-level `think` enabled, no
	// grammar constraint blocking the think tags). It is captured for logging
	// and debugging only — never fed to a downstream verb. Empty for
	// non-reasoning models and whenever a `format` grammar suppresses the
	// separate thinking phase. omitempty so outbound request messages don't
	// carry a stray key.
	Thinking  string     `json:"thinking,omitempty"`
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
	Type       string                  `json:"type"` // "object"
	Properties map[string]ToolProperty `json:"properties"`
	Required   []string                `json:"required"`
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
	// NumCtx overrides the default context window size (see defaultNumCtx)
	// when positive. Zero means "use the default."
	NumCtx int
	// Format overrides the request's format constraint when non-nil.
	// Ollama accepts either the loose string "json" (any syntactically
	// valid JSON — the default every call gets when this is nil) or a full
	// JSON Schema object (map[string]any, matching
	// https://github.com/ollama/ollama/blob/main/docs/api.md's structured
	// outputs support), which additionally grammar-constrains which keys
	// must be present. Confirmed live against this deployment (Ollama
	// 0.30.6) that a schema's "required" array is actually enforced at
	// generation time, not just documentation: a field the model's own
	// reasoning never planned to emit still appeared in the output because
	// the schema required it. Use this for a verb whose Validate has been
	// burned by a model omitting a required key (e.g. REFINE_TESTS_JUDGE's
	// "summary" — 3/3 identical failures on connect-four-v2 bead 56,
	// 2026-08-27) rather than relying on retry-after-rejection alone.
	Format any
	// Think controls Ollama's top-level `think` request field, which toggles
	// reasoning-model chain-of-thought separation. Nil (the default for every
	// caller that passes no Options) omits the field entirely, leaving the
	// model's own default in force — this preserves historical behavior.
	// A non-nil pointer sends `"think": true` / `"think": false`.
	//
	// NOTE: `think` is a TOP-LEVEL field of the /api/chat request, not an
	// entry in `options`. An earlier version of ChatWithTools set
	// `options: {"think": false}`, which Ollama silently ignores (unknown
	// option keys are dropped) — a confirmed no-op. This field sends it in
	// the right place.
	//
	// `think` and a `format` JSON-schema grammar are effectively mutually
	// exclusive: the grammar masks the think tags, so a reasoning model
	// cannot emit a separate thinking phase under a schema (see
	// docs/schema-mode-reasoning-field.md). Schema-mode verbs pass
	// Think: ptr(false) and carry their reasoning in a schema field instead.
	Think *bool
	// NumPredict overrides the per-request generated-token cap when positive.
	// Zero means: ChatWithTools uses toolLoopNumPredict; Chat sets no cap
	// (its runaway bound is the schema reasoning field's maxLength).
	NumPredict int
	// OmitFormat drops the `format` field from the request entirely, overriding
	// the default `format:"json"` (and any Format set above). Only honored by
	// ChatWithTools. Use it on a tool-primary loop whose final turn is NOT
	// parsed as model-emitted JSON — REFINE_TESTS_WRITE, whose Content is taken
	// verbatim as a free-text summary. The loose "json" grammar there buys
	// nothing and actively breaks native tool-calling for any model that
	// doesn't emit a separate thinking pass first: gemma4:31b under
	// disableThink, and every non-reasoning model, dump the tool payload as
	// content and never call the tool (root-caused 2026-08-31,
	// docs/format-json-tool-turn.md).
	OmitFormat bool
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []Message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Think    *bool          `json:"think,omitempty"`
	Format   any            `json:"format,omitempty"`
	Options  map[string]any `json:"options,omitempty"`
}

type chatResponse struct {
	Message    Message `json:"message"`
	Done       bool    `json:"done"`
	DoneReason string  `json:"done_reason,omitempty"`
	Error      string  `json:"error,omitempty"`
	// Timing/token counters, present on the final (done:true) chunk. Nanoseconds
	// for durations. Surfaced via CallRecorder; zero when Ollama omits them.
	TotalDuration      int64 `json:"total_duration"`
	LoadDuration       int64 `json:"load_duration"`
	PromptEvalCount    int64 `json:"prompt_eval_count"`
	PromptEvalDuration int64 `json:"prompt_eval_duration"`
	EvalCount          int64 `json:"eval_count"`
	EvalDuration       int64 `json:"eval_duration"`
}

func (r chatResponse) stats() Stats {
	return Stats{
		TotalDuration:      r.TotalDuration,
		LoadDuration:       r.LoadDuration,
		PromptEvalCount:    r.PromptEvalCount,
		PromptEvalDuration: r.PromptEvalDuration,
		EvalCount:          r.EvalCount,
		EvalDuration:       r.EvalDuration,
	}
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
		Options:  map[string]any{"temperature": 0.0, "num_ctx": defaultNumCtx},
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
//
// format:"json" is set on every call: Ollama enforces this via grammar-
// constrained decoding, restricting the sampler to only JSON-syntactically-
// valid token continuations at every step — including proper escaping of
// quotes and control characters inside string values. Without it, a model
// composing a long free-text reasoning field (routinely quoting Go source it
// just examined via run_go_snippet) will drift into normal prose habits —
// literal tabs for code indentation, a bare `"` for emphasis — that are
// invalid inside a JSON string but otherwise read as perfectly natural
// writing. Confirmed against a real ADJUDICATE_NEXT_EXECUTION escalation
// (connect-four-v1, bead 47, 2026-08-26): 3 of 3 attempts had this exact
// shape of defect, none of them a real judgment call, all three burning a
// full retry cycle before exhausting tolerance and escalating. A downstream
// text-repair pass was tried and rejected: it fixes simple isolated cases,
// but a stray unescaped quote that happens to be followed by a comma is
// structurally identical to a real string boundary, and guessing wrong
// there risks silently accepting a wrong split and producing garbled field
// values instead of just failing loudly and retrying. format:"json" avoids
// the ambiguity entirely by making the malformed byte sequence unreachable
// during generation, rather than trying to disambiguate it after the fact.
func (c *Client) Chat(ctx context.Context, model string, msgs []Message, opts *Options) (content string, err error) {
	temp := DefaultTemperature
	numCtx := defaultNumCtx
	numPredict := 0
	var format any = "json"
	var think *bool

	// Per-call recording for the qualification harness / capture instrumentation.
	// No-op unless a recorder is installed on ctx.
	var recResp Message
	var recStats Stats
	var recDoneReason string
	if rec := recorderFrom(ctx); rec != nil {
		started := time.Now()
		defer func() {
			errStr := ""
			if err != nil {
				errStr = err.Error()
			}
			rec.RecordCall(CallRecord{
				Kind: "chat", Turn: 1, Model: model, Messages: msgs,
				NumPredict: numPredict, Format: format,
				Response: recResp, DoneReason: recDoneReason, Stats: recStats,
				WallMillis: time.Since(started).Milliseconds(), Err: errStr,
			})
		}()
	}

	if opts != nil {
		if opts.Temperature > 0 {
			temp = opts.Temperature
		}
		if opts.NumCtx > 0 {
			numCtx = opts.NumCtx
		}
		if opts.NumPredict > 0 {
			numPredict = opts.NumPredict
		}
		if opts.Format != nil {
			format = opts.Format
		}
		think = opts.Think
	}

	// Harness-only override (cmd/qualify-model); no-op in the pipeline.
	if ov, ok := chatOverrideFrom(ctx); ok {
		if ov.ForceOmitFormat {
			format = nil
		}
		if ov.Think != nil {
			think = ov.Think
		}
	}

	reqOptions := map[string]any{"temperature": temp, "num_ctx": numCtx}
	if numPredict > 0 {
		reqOptions["num_predict"] = numPredict
	}
	req := chatRequest{
		Model:    model,
		Messages: msgs,
		Stream:   false,
		Think:    think,
		Format:   format,
		Options:  reqOptions,
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
	if uerr := json.Unmarshal(raw, &cr); uerr != nil {
		return "", fmt.Errorf("parse response: %w", uerr)
	}
	recResp, recStats, recDoneReason = cr.Message, cr.stats(), cr.DoneReason
	if cr.Error != "" {
		return "", fmt.Errorf("ollama: %s", cr.Error)
	}
	if cr.Message.Thinking != "" {
		slog.Info("chat: model returned separated thinking (discarded)",
			"model", model, "thinking_chars", len(cr.Message.Thinking), "content_chars", len(cr.Message.Content))
	}
	return cr.Message.Content, nil
}

// ChatWithTools sends a streaming chat request with tool definitions and
// returns the full assistant Message, which may contain ToolCalls instead of
// (or in addition to) Content. Content tokens are written to tokenWriter as
// they arrive if non-nil, giving real-time observability. The caller is
// responsible for the multi-turn loop: executing tool calls and feeding
// results back as tool messages.
//
// format:"json" is set here by default, same rationale as Chat(): most callers
// (REFINE_TESTS_CRITIQUE, REFINE_TESTS_JUDGE, ADJUDICATE_NEXT_EXECUTION)
// eventually parse the final turn's Content as a single JSON object via
// ollama.ExtractJSON, exactly like the plain Chat() path, just reached through
// a tool-calling loop instead of a single request.
//
// The "format:json and tools coexist without interference" claim was only ever
// validated against reasoning models with thinking ON — which emit a separate
// thinking pass and then a clean tool_call. It does NOT hold otherwise: under
// gemma4:31b+disableThink, or any non-reasoning model, the loose "json" grammar
// makes the model serialize the tool payload as Content and never emit a
// tool_call (root-caused 2026-08-31, docs/format-json-tool-turn.md). A
// tool-primary loop whose final turn is not parsed as model JSON should pass
// Options.OmitFormat to drop the constraint — REFINE_TESTS_WRITE does.
func (c *Client) ChatWithTools(ctx context.Context, model string, msgs []Message, tools []Tool, opts *Options, tokenWriter io.Writer) (result Message, err error) {
	temp := DefaultTemperature
	numCtx := defaultNumCtx
	numPredict := toolLoopNumPredict
	var format any = "json"
	var think *bool
	omitFormat := false

	// Per-call recording for the qualification harness / capture instrumentation.
	// One record per ChatWithTools call (== one turn; the caller runs the loop).
	var recStats Stats
	var recDoneReason string
	if rec := recorderFrom(ctx); rec != nil {
		started := time.Now()
		defer func() {
			errStr := ""
			if err != nil {
				errStr = err.Error()
			}
			rec.RecordCall(CallRecord{
				Kind: "tools", Turn: 1, Model: model, Messages: msgs, Tools: tools,
				NumPredict: numPredict, Format: format, OmitFormat: omitFormat,
				Response: result, DoneReason: recDoneReason, Stats: recStats,
				WallMillis: time.Since(started).Milliseconds(), Err: errStr,
			})
		}()
	}

	if opts != nil {
		if opts.Temperature > 0 {
			temp = opts.Temperature
		}
		if opts.NumCtx > 0 {
			numCtx = opts.NumCtx
		}
		if opts.NumPredict > 0 {
			numPredict = opts.NumPredict
		}
		if opts.Format != nil {
			format = opts.Format
		}
		if opts.OmitFormat {
			format = nil
			omitFormat = true
		}
		think = opts.Think
	}

	// Harness-only override (cmd/qualify-model); no-op in the pipeline.
	if ov, ok := chatOverrideFrom(ctx); ok {
		if ov.ForceOmitFormat {
			format = nil
			omitFormat = true
		}
		if ov.Think != nil {
			think = ov.Think
		}
	}

	req := struct {
		Model    string         `json:"model"`
		Messages []Message      `json:"messages"`
		Tools    []Tool         `json:"tools"`
		Stream   bool           `json:"stream"`
		Think    *bool          `json:"think,omitempty"`
		Format   any            `json:"format,omitempty"`
		Options  map[string]any `json:"options,omitempty"`
	}{
		Model:    model,
		Messages: msgs,
		Tools:    tools,
		Stream:   true,
		Think:    think,
		Format:   format,
		// `think` goes top-level (above), not in options: Ollama silently
		// drops unknown option keys, so the previous `options: {"think": false}`
		// here was a confirmed no-op. nil think == field omitted == model default,
		// preserving historical behavior for callers that pass no Options.
		//
		// num_predict is always set here (default toolLoopNumPredict): a
		// tool-loop turn should be short, and an uncapped one lets a degenerate
		// reasoning stream run until the client timeout — see toolLoopNumPredict.
		Options: map[string]any{"temperature": temp, "num_ctx": numCtx, "num_predict": numPredict},
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

	// Close the response body immediately when ctx is cancelled. This interrupts
	// any blocking dec.Decode() call without waiting for the transport's async
	// cancellation path to drain the kernel TCP receive buffer first.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			resp.Body.Close()
		case <-done:
		}
	}()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return Message{}, fmt.Errorf("ollama %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var contentSB strings.Builder
	var thinkingSB strings.Builder
	var toolCalls []ToolCall
	var doneReason string
	chunks := 0
	nextProgressLog := streamProgressLogEvery
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
		if chunk.Message.Thinking != "" {
			thinkingSB.WriteString(chunk.Message.Thinking)
			// Tee thinking to the trace writer too. Without this, a model that
			// runs away inside its thinking stream (never emitting content or a
			// tool call) leaves no record of what it was doing — only a length
			// count after the fact.
			if tokenWriter != nil {
				io.WriteString(tokenWriter, chunk.Message.Thinking)
			}
		}
		if len(chunk.Message.ToolCalls) > 0 {
			toolCalls = append(toolCalls, chunk.Message.ToolCalls...)
		}
		chunks++
		if chunks >= nextProgressLog {
			slog.Info("ChatWithTools: streaming still in progress",
				"model", model, "chunks", chunks,
				"content_chars", contentSB.Len(), "thinking_chars", thinkingSB.Len(),
				"content_tail", lastRunes(contentSB.String(), 200),
				"thinking_tail", lastRunes(thinkingSB.String(), 200))
			nextProgressLog += streamProgressLogEvery
		}
		if chunk.Done {
			doneReason = chunk.DoneReason
			recStats = chunk.stats()
			break
		}
	}
	recDoneReason = doneReason
	// Terminate streamed content with a newline so subsequent trace lines start clean.
	if tokenWriter != nil && contentSB.Len() > 0 {
		fmt.Fprintln(tokenWriter)
	}
	// A turn with neither content nor a tool call is never useful to a
	// caller — every ChatWithTools caller's loop (REFINE_TESTS_WRITE,
	// REFINE_TESTS_CRITIQUE, ADJUDICATE_NEXT_EXECUTION) treats "no tool
	// calls" as "the model gave its final answer" and reads Content as that
	// answer. An empty answer here previously surfaced only indirectly, many
	// steps downstream, as a bare "unexpected end of JSON input" from
	// json.Unmarshal on an empty string — logging done_reason at the actual
	// point of occurrence makes a future one directly diagnosable (e.g.
	// "length" means the model ran out of context/output budget mid-turn,
	// as opposed to some other cause) instead of requiring after-the-fact
	// inference from an empty handoff_attempts row. Confirmed against a real
	// ADJUDICATE_NEXT_EXECUTION escalation (connect-four-v1, bead 47,
	// 2026-08-26): all 3 attempts stored a 0-byte raw_output with no
	// indication of why.
	if contentSB.Len() == 0 && len(toolCalls) == 0 {
		slog.Warn("ChatWithTools: turn produced neither content nor a tool call",
			"model", model, "done_reason", doneReason, "message_count", len(msgs))
	}
	if thinkingSB.Len() > 0 {
		slog.Info("ChatWithTools: model returned separated thinking (not fed downstream)",
			"model", model, "thinking_chars", thinkingSB.Len(), "content_chars", contentSB.Len())
	}
	result = Message{
		Role:      "assistant",
		Content:   contentSB.String(),
		Thinking:  thinkingSB.String(),
		ToolCalls: toolCalls,
	}
	return result, nil
}

// ExtractJSON strips Qwen3-style <think>…</think> blocks and markdown code
// fences, returning the innermost JSON text.
//
// The boundary is found structurally (brace/bracket depth, honoring quoted
// strings and escapes) rather than by searching for a closing "```" marker.
// A naive "last ``` in the string" search breaks as soon as the model adds
// any trailing commentary of its own containing a code fence — e.g. quoting
// a `go test` failure or a code snippet after the JSON block — because that
// trailing fence, not the real one, is then the last "```" in the text, and
// everything up to it (including the trailing prose) gets swept into what's
// returned as "JSON". A JSON string value legitimately containing "```" runs
// into the same ambiguity in reverse. Scanning structurally sidesteps both:
// markdown fences never need to be located at all, only the real '{'/'[' and
// its matching close.
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

	// If fenced, narrow the search to after the opening fence's marker line
	// (e.g. "```json\n" or "```\n") so a stray '{'/'[' in the language tag
	// itself can't be mistaken for the JSON start.
	searchFrom := 0
	if strings.HasPrefix(s, "```") {
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			searchFrom = nl + 1
		}
	}

	start := indexOfJSONStart(s[searchFrom:])
	if start < 0 {
		return s
	}
	start += searchFrom

	end := matchingJSONEnd(s, start)
	if end < 0 {
		// Unterminated (truncated model output) — best effort: everything
		// from the JSON start onward, at least free of leading fence noise.
		return escapeRawControlCharsInStrings(strings.TrimSpace(s[start:]))
	}
	return escapeRawControlCharsInStrings(s[start : end+1])
}

// escapeRawControlCharsInStrings escapes raw tab, newline, carriage-return,
// and other sub-0x20 control bytes that appear *inside* JSON string
// literals, leaving structural whitespace between tokens untouched.
//
// A model frequently quotes a Go code snippet verbatim inside a JSON string
// field (a reasoning/explanation field citing the implementation it just
// examined, e.g. via the mandatory run_go_snippet verification step) — real
// Go source uses literal tabs for indentation, but RFC 8259 requires every
// control character inside a JSON string to be escaped, and Go's strict
// encoding/json rejects a raw one outright ("invalid character '\t' in
// string literal"). Confirmed against a real ADJUDICATE_NEXT_EXECUTION
// escalation, 2026-08-26 (connect-four-v1, bead 47): the model's own
// "reasoning" field mixed correctly-escaped "\n" sequences with literal,
// unescaped tab bytes from a quoted `IsFull` implementation in the same
// string, three attempts in a row, before it exhausted retry tolerance and
// escalated for a purely mechanical formatting defect — never a real
// judgment call for a human to review. Applied here, in ExtractJSON itself,
// so every JSON-parsing verb gets the fix for free rather than requiring a
// per-verb patch; every verb's Validate already funnels through ExtractJSON
// before json.Unmarshal.
//
// Only bytes encountered while inString is true are rewritten — the same
// string/escape tracking matchingJSONEnd already uses above, so structural
// JSON whitespace (indentation between keys, newlines between array
// elements) is left exactly as the model wrote it.
func escapeRawControlCharsInStrings(s string) string {
	var sb strings.Builder
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			sb.WriteByte(c)
			continue
		}
		if escaped {
			escaped = false
			sb.WriteByte(c)
			continue
		}
		switch {
		case c == '\\':
			escaped = true
			sb.WriteByte(c)
		case c == '"':
			inString = false
			sb.WriteByte(c)
		case c == '\t':
			sb.WriteString(`\t`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c < 0x20:
			fmt.Fprintf(&sb, `\u%04x`, c)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// indexOfJSONStart returns the byte index of the first '{' or '[' in s, or -1.
func indexOfJSONStart(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '{' || s[i] == '[' {
			return i
		}
	}
	return -1
}

// matchingJSONEnd scans s starting at the '{' or '[' index start and returns
// the index of its matching closing brace/bracket, tracking nested depth and
// skipping over quoted-string content (including escaped quotes) so neither
// contributes false structural characters. Returns -1 if depth never returns
// to zero before the end of s (truncated output).
func matchingJSONEnd(s string, start int) int {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
