# Ollama stream idle timeout + transient-error retry

Follow-ups #4 and #5 from `project_execute_progress_detection` (exprvm-web-baseline-15,
2026-09-05).

## The bug

During bead 328's ADJUDICATE, `qwen3.6:35b-a3b`'s streaming response went silent
mid-generation: `ChatWithTools`'s `dec.Decode` loop (`internal/ollama/client.go`)
received no chunks for 38+ minutes while Ollama itself stayed responsive to other
requests. It hit this twice in one run.

Two gaps:

1. **No mid-stream bound.** The only limit on a `ChatWithTools` response was
   `handoffClientTimeout = 60 * time.Minute` (whole-response). A mid-stream stall
   therefore wasted up to an hour before the client aborted.
2. **A transient stall burned a strike.** `dispatch.recordRunFailure` recorded the
   error as `run_error: …`, which `strikeCount` counts. `verbTolerance` is a flat
   `2`, so 3 stalls on one job → escalation with nothing actually wrong.

## Fix

### 1. Idle watchdog in `ChatWithTools` (`internal/ollama/client.go`)

The streaming decode loop runs under a request-scoped context (`reqCtx`). A
watchdog goroutine polls every `streamIdlePollInterval` (15s) and cancels
`reqCtx` — unblocking a `dec.Decode` stuck in a kernel read — when the stream has
produced nothing for too long:

- **before the first chunk** (prompt eval / first token): bound at
  `streamFirstChunkTimeout` (10m). Warmup already made the model resident, so
  this only covers prompt eval; 10m is far past anything real on this fleet while
  still closing the pre-first-token gap the idle bound can't see.
- **after the first chunk**: bound each inter-chunk gap at `streamIdleTimeout`
  (3m). The fleet streams ~9 tok/s (≈ one chunk sub-second), and a reasoning
  model streams its `thinking` channel incrementally too, so a long
  muse-glimmer think keeps resetting the timer. 3m of total silence is an
  unambiguously dead stream.

On fire the decode error is translated to `ErrStreamIdle` (a sentinel). All three
durations are package `var`s so tests can shrink them. The watchdog is joined
before `ChatWithTools` returns.

Applies to both `New` (bounded handoff client) and `NewUnbounded` (EXECUTE_BEAD,
bakeoff) — it is a pure transport-liveness check. For EXECUTE_BEAD it only
converts a *slow* infra failure (bounded by PR #8's `execCeiling`) into a *fast*
one; PR #8's `progressTracker`, which only evaluates between turns, is untouched.

`Chat` (non-streaming: DECOMPOSE, AUDIT, RECONCILE, ANALYZE, …) is out of scope —
there are no chunks to watch — but it still benefits from the transient
classification below (5xx, request timeout).

### 2. Transient errors don't strike (`internal/orchestrator/`)

`ollama.IsTransient(err)` classifies infrastructure hiccups a retry might clear:

- `ErrStreamIdle`, `io.ErrUnexpectedEOF`
- `syscall.ECONNRESET`, `syscall.EPIPE` (mid-request connection drop)
- `*ollama.HTTPStatusError` with status `>= 500` or `429`
- any `net.Error` with `Timeout()` (catches the 60m `Client.Timeout`)

Deliberately **narrow**: connection *refused* / DNS failure is a persistent
misconfiguration (Ollama down, wrong URL, model never pulled) and still strikes so
it escalates promptly — existing `TestDispatch_OllamaWarmupFailure…` behavior is
unchanged.

`recordRunFailure` routes a transient error to `recordTransientRunFailure`: the
attempt is written as `transient: …` (visible in `handoff_attempts`, excluded from
`strikeCount`), the job returns to `failed_retry` with **no strike**. Bounded by
`transientRetryCap` (5) counted via `transientRetryCount` — a persistently dead
endpoint still escalates within minutes-to-tens-of-minutes.

`HTTPStatusError` replaces the inline `fmt.Errorf("ollama %d: …")` in both `Chat`
and `ChatWithTools` so a 5xx is classifiable.

## Not included

- PR #8 (`feat/execute-progress-detection`) is independent and unaffected. It
  still waits for a clean live `stalled` test / the decouple-cadence follow-up.
- Follow-up #1 (decouple `execCheckpointInterval` from `execution_budget`) — kept
  separate.

## Tests

- `internal/ollama/client_test.go`: idle abort → `ErrStreamIdle`; first-chunk
  timeout; slow-but-steady stream succeeds; `IsTransient` table; typed
  `HTTPStatusError`.
- `internal/orchestrator/dispatch_test.go`: transient failures don't strike and
  stay retryable; `transientRetryCap` is still a backstop; a deterministic Run
  error still escalates at `verbTolerance` regardless of preceding transients.
