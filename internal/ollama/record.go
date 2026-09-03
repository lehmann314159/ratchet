package ollama

import "context"

// Stats carries the timing/token counters Ollama returns on the final
// (`done: true`) chunk of a chat response. Durations are nanoseconds, exactly
// as Ollama reports them. Zero values mean the field was absent (some models /
// error paths omit them).
type Stats struct {
	TotalDuration      int64 `json:"total_duration"`
	LoadDuration       int64 `json:"load_duration"`
	PromptEvalCount    int64 `json:"prompt_eval_count"`
	PromptEvalDuration int64 `json:"prompt_eval_duration"`
	EvalCount          int64 `json:"eval_count"`
	EvalDuration       int64 `json:"eval_duration"`
}

// CallRecord is one model call (a single Chat, or a single turn of a
// ChatWithTools loop) as seen by the client. It is the unit the qualification
// harness replays and the capture instrumentation writes to disk. Everything
// here is the raw request/response — no interpretation.
type CallRecord struct {
	// Kind is "chat" or "tools".
	Kind string `json:"kind"`
	// Turn is 1-based within a ChatWithTools loop; always 1 for Kind=="chat".
	// The client cannot see the loop, so it always records 1 — the caller
	// (dispatch instrumentation) renumbers across turns if it needs to.
	Turn       int       `json:"turn"`
	Model      string    `json:"model"`
	Messages   []Message `json:"messages"`
	Tools      []Tool    `json:"tools,omitempty"`
	NumPredict int       `json:"num_predict"`
	Format     any       `json:"format,omitempty"`
	OmitFormat bool      `json:"omit_format"`
	// Response is the assistant message the call returned (content + thinking
	// + tool calls).
	Response   Message `json:"response"`
	DoneReason string  `json:"done_reason,omitempty"`
	Stats      Stats   `json:"stats"`
	WallMillis int64   `json:"wall_ms"`
	Err        string  `json:"err,omitempty"`
}

// CallRecorder receives one CallRecord per model call when a recorder is
// installed on the context via WithRecorder. Implementations must be safe for
// concurrent use (MONITOR_EXECUTION and EXECUTE_BEAD run their model calls on
// separate goroutines) and must not block — the client calls RecordCall
// synchronously on the request path.
type CallRecorder interface {
	RecordCall(rec CallRecord)
}

type recorderKey struct{}

// WithRecorder returns a context that carries r. Chat and ChatWithTools look
// for it and emit a CallRecord per call when it is present. A nil r is a
// no-op (the context is returned unchanged) so callers can wire it
// unconditionally.
func WithRecorder(ctx context.Context, r CallRecorder) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, recorderKey{}, r)
}

// recorderFrom returns the CallRecorder on ctx, or nil.
func recorderFrom(ctx context.Context) CallRecorder {
	r, _ := ctx.Value(recorderKey{}).(CallRecorder)
	return r
}

// RecorderInstalled reports whether a non-nil CallRecorder is on ctx. For
// tests and debug logging.
func RecorderInstalled(ctx context.Context) bool {
	return recorderFrom(ctx) != nil
}
