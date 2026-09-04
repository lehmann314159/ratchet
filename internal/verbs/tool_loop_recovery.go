package verbs

import (
	"context"
	"log/slog"
	"strings"

	"ratchet/internal/ollama"
)

// This file addresses the reasoning-model tool-loop spiral confirmed across
// ADJUDICATE_NEXT_EXECUTION (qwen3.6:35b-a3b) and REFINE_TESTS_CRITIQUE
// (qwen3:32b) — see ~/Documents/ratchet-projects/framework-prompt-numpredict-
// spiral.md and handoff_exprvm_web_baseline_9/_10 in memory for the incident
// history this was built from.
//
// A ChatWithTools turn whose model ran out of its per-turn token budget
// (toolLoopNumPredict in internal/ollama, 8192) while still reasoning ends
// with done_reason "length", zero Content, and zero ToolCalls — identical on
// the surface to an ordinary empty final-answer turn (done_reason "stop"),
// which every one of these loops already treats as "give it one nudge, then
// accept whatever it says." That's the wrong reaction here: reading the
// captured `thinking` text of every occurrence in the qual-corpus-p48 /
// -baseline-9 / -baseline-10 corpora (12/12 calls with done_reason=length all
// had empty Content and no ToolCalls) showed two distinct shapes hiding
// behind the one signature:
//   - a verbose-but-still-progressing model (REFINE_TESTS_WRITE/gemma4:31b,
//     p48 bead 318) that finished cleanly once given another turn — more
//     room would very plausibly have let it finish in one turn instead of
//     three;
//   - a genuinely degenerate reasoning loop (REFINE_TESTS_CRITIQUE/qwen3:32b,
//     baseline-9 bead 315) whose last ~1500 captured characters are a
//     verbatim-repeating sentence ("Therefore, the test is correct, and the
//     implementation is wrong. However, ..." ×5+) — more room does not
//     converge this, it only relocates the same failure further out.
//
// Since the two shapes can't be told apart in advance, the recovery is
// staged: try the cheap fix (bigger budget, a directive to stop analyzing)
// once; if the SAME condition recurs, that's confirmed as the non-convergent
// shape, so stop feeding it the same growing context and force one
// maximally-stripped last-resort turn instead of repeating the identical
// dead turn for the rest of the budget (baseline-10 job 2014: 3 independent
// top-level attempts each burned turns 1-3 of 6 on this exact loop before
// running out of turns entirely — see forceStrippedFinalAnswer below).

// lengthCapRetryNumPredict is the token budget for the one-shot recovery
// attempt after a tool-loop turn hits toolLoopNumPredict (8192, defined in
// internal/ollama) while still reasoning. 2x: large enough that a model that
// was merely close to finishing (the WRITE/gemma shape above) has real room,
// small enough that a genuinely non-convergent loop (the CRITIQUE/qwen3
// shape) doesn't burn an unbounded amount of wall time chasing it — that
// case is caught by lengthCapStrikes instead of a bigger number.
const lengthCapRetryNumPredict = 2 * 8192

// lengthCapStopReasoningNudge replaces the mandatory-verification nudge
// ("call run_go_snippet...") on a length-cap-empty turn. That nudge asks for
// MORE reasoning ("verify a claim"), which is actively wrong when the
// problem is that the model already produced too much reasoning to fit its
// budget — it would just feed the same spiral. This asks for the opposite.
const lengthCapStopReasoningNudge = "Your last turn ran out of room while still reasoning and produced no " +
	"answer. Stop the extended analysis. In your next reply, go straight to a conclusion — either your " +
	"final JSON output, or a single direct tool call with no narration first."

// lengthCapFinalNudge is appended to the stripped last-resort turn (see
// forceStrippedFinalAnswer): a request for the answer only, no more
// analysis, no tools.
const lengthCapFinalNudge = "\n\nReasoning about this case has not converged within the available turns. " +
	"Do not analyze further and do not call any tool. Output ONLY your final JSON answer now, using your " +
	"best judgment from what has already been established."

// turnCapFinalizeNudge is used when a tool loop exhausts its entire turn
// budget without ever reaching a zero-tool-call "final answer" turn — i.e.
// every turn, including the last, called a tool. The loop's per-turn logic
// only ever checks "is this a final answer" on a turn with no tool calls, so
// running out of turns while still mid-verification previously fell through
// to returning whatever Content happened to be last recorded, which is
// usually empty (the model was still calling tools, not answering) and reads
// downstream as a bare "malformed: JSON parse error: unexpected end of JSON
// input" with no indication why (confirmed baseline-10 job 2014: all 3
// top-level attempts ended this way). This forces one explicit turn asking
// for the decision directly instead of silently trusting empty leftover state.
const turnCapFinalizeNudge = "You are out of turns to call tools. Do not call any more tools. Give your " +
	"final JSON decision now, based on everything established so far."

// isLengthCapEmpty reports whether a ChatWithTools turn hit the per-turn
// token cap while still reasoning: no tool call, no content, and Ollama's
// own done_reason says why ("length" — as opposed to "stop", which is the
// ordinary "model chose to answer with nothing more to say" case already
// handled elsewhere in each loop).
func isLengthCapEmpty(msg ollama.Message) bool {
	return len(msg.ToolCalls) == 0 && strings.TrimSpace(msg.Content) == "" && msg.DoneReason == "length"
}

// withNumPredict returns opts with NumPredict overridden to n, leaving every
// other field (Format, OmitFormat, Think, ...) as the caller already set it.
// A nil opts becomes a fresh Options carrying only NumPredict.
func withNumPredict(opts *ollama.Options, n int) *ollama.Options {
	if opts == nil {
		return &ollama.Options{NumPredict: n}
	}
	cp := *opts
	cp.NumPredict = n
	return &cp
}

// forceStrippedFinalAnswer is the last-resort turn after isLengthCapEmpty
// has fired twice in the same job — see the file doc comment for why a
// second occurrence is treated as the non-convergent shape rather than
// retried again. It drops the entire accumulated tool-call transcript (which
// only reproduces the same reasoning path that already failed twice) back
// down to the original system+user framing, doubles the token budget, and
// asks directly for the final answer with no tools available. Whatever comes
// back — even if still empty — is handled exactly like any other final turn
// by the caller's existing Validate/strike path; this only stops the loop
// from burning its remaining turns on a guaranteed-empty repeat.
func forceStrippedFinalAnswer(ctx context.Context, oc *ollama.Client, model, systemPrompt, userMsg string, opts *ollama.Options) (string, error) {
	stripped := []ollama.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMsg + lengthCapFinalNudge},
	}
	msg, err := oc.ChatWithTools(ctx, model, stripped, nil, withNumPredict(opts, lengthCapRetryNumPredict), nil)
	if err != nil {
		return "", err
	}
	return msg.Content, nil
}

// forceFinalizeAfterTurnCap makes one extra ChatWithTools call, with tools
// omitted and an explicit "stop calling tools, decide now" instruction
// appended to the transcript, for the case documented at turnCapFinalizeNudge:
// the loop's turn budget ran out while the model was still legitimately
// calling tools, so no turn ever reached the "give your final answer" branch.
func forceFinalizeAfterTurnCap(ctx context.Context, oc *ollama.Client, model string, messages []ollama.Message, opts *ollama.Options) (string, error) {
	finalMessages := append(append([]ollama.Message{}, messages...), ollama.Message{
		Role: "user", Content: turnCapFinalizeNudge,
	})
	msg, err := oc.ChatWithTools(ctx, model, finalMessages, nil, opts, nil)
	if err != nil {
		return "", err
	}
	return msg.Content, nil
}

// logTurnCapFallthrough records the turnCapFinalizeNudge condition (see
// above) at the point it's detected, before forceFinalizeAfterTurnCap makes
// the recovery call — matching how isLengthCapEmpty's own condition is
// logged, so a future occurrence is directly diagnosable instead of only
// inferable from a downstream malformed-JSON strike.
func logTurnCapFallthrough(verb string, beadID int64, maxTurns int) {
	slog.Warn(verb+": turn budget exhausted while still calling tools, forcing one finalize-only turn",
		"bead_id", beadID, "max_turns", maxTurns)
}
