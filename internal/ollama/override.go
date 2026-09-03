package ollama

import "context"

// ChatOverride, when installed on a context via WithChatOverride, is applied by
// Chat and ChatWithTools on top of the per-call Options a caller passed —
// after those, so it wins.
//
// Nothing in the pipeline installs one. It exists for the model-qualification
// harness (cmd/qualify-model), which needs to sweep the grammar-constraint and
// reasoning dimensions for a verb without editing that verb's Run() code: a
// tool-loop verb hard-codes format:"json" (or a flat schema) on every turn,
// which suppresses a reasoning model's separate thinking phase, so a candidate
// reasoning model can't be evaluated on equal footing otherwise.
type ChatOverride struct {
	// ForceOmitFormat drops the request's `format` constraint entirely,
	// identical to passing Options{OmitFormat: true}.
	ForceOmitFormat bool
	// Think, when non-nil, sets the request's top-level `think` field,
	// overriding whatever the caller's Options specified.
	Think *bool
}

type chatOverrideKey struct{}

// WithChatOverride returns a context carrying ov. A zero-value ov still
// registers (ForceOmitFormat=false, Think=nil is a valid "change nothing"
// state); callers that want a true no-op should simply not call this.
func WithChatOverride(ctx context.Context, ov ChatOverride) context.Context {
	return context.WithValue(ctx, chatOverrideKey{}, ov)
}

func chatOverrideFrom(ctx context.Context) (ChatOverride, bool) {
	ov, ok := ctx.Value(chatOverrideKey{}).(ChatOverride)
	return ov, ok
}
