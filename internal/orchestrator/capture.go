package orchestrator

import (
	"context"

	"ratchet/internal/capture"
)

// config holds optional orchestrator settings supplied via Option.
type config struct {
	capturer *capture.Capturer
}

// Option configures orchestrator.Run.
type Option func(*config)

// WithCapturer installs a verb-IO capturer. Set by
// `ratchet start --capture-verb-io <dir>`; nil (the default) leaves the
// dispatch loop uninstrumented.
func WithCapturer(c *capture.Capturer) Option {
	return func(cfg *config) { cfg.capturer = c }
}

type capturerKey struct{}

func withCapturer(ctx context.Context, c *capture.Capturer) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, capturerKey{}, c)
}

func capturerFrom(ctx context.Context) *capture.Capturer {
	c, _ := ctx.Value(capturerKey{}).(*capture.Capturer)
	return c
}
