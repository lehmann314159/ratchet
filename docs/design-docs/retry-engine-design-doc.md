# retry-engine — Design Document

## Overview

`retry-engine` is a Go library providing two independent, composable resilience
primitives and a driver that ties them together:

- a **circuit breaker** — the classic three-state machine (closed → open →
  half-open → closed) that stops hammering a failing dependency;
- an **exponential backoff** with seeded jitter — deterministic delay
  computation for retry loops;
- a **retry driver** (`Retry`) that runs an operation up to N times, sleeping
  the backoff between attempts, optionally gated by a breaker.

Everything is **deterministic and injectable**: the breaker takes a
`Now func() time.Time`, the backoff takes a `*rand.Rand`, and `Retry` takes a
`Sleep func(time.Duration)`. Tests advance a fake clock and record fake sleeps;
nothing wall-clock happens. A CLI (`main.go`) is a stdin-driven breaker +
backoff simulator with a fixed config, so the whole state machine is
exercisable line by line.

Consumers: application code wrapping calls to a flaky downstream service.
Runtime model: **library** plus a **CLI** simulator. No goroutines, no real
timers, no network.

**Domain parameters — all explicit, do not infer:**

- **Breaker states are exactly three**: `StateClosed`, `StateOpen`,
  `StateHalfOpen`. `String()` gives `"closed"`, `"open"`, `"half-open"`.
- **`Allow` mutates; `State` does not.** The open → half-open transition (after
  `OpenTimeout` elapses on the injected clock) happens **inside `Allow`**, on
  the first `Allow` call after the timeout. `State()` returns the last stored
  state and never advances it on its own. This is a deliberate design choice
  (state advances on interaction, not on wall-clock alone) — pin it.
- **`Allow`/`Record` pairing.** Every `Allow` that returns `(true, nil)` must be
  followed by exactly one `Record(success)`. In half-open, `Allow` reserves a
  trial slot and `Record` releases it; a `Record` in half-open with no slot
  reserved is a no-op.
- **Closed → Open** on `FailureThreshold` **consecutive** failures (a success
  resets the failure count to 0). **Half-open → Closed** on `SuccessThreshold`
  **consecutive** trial successes. **Half-open → Open** on the **first** trial
  failure (resets the open timer). Opening always resets all counters.
- **Half-open concurrency**: at most `HalfOpenMax` trial requests may be
  in-flight at once; `Allow` returns `(false, ErrTooManyHalfOpen)` beyond that.
- **`NewBreaker` defaults** for any field `<= 0` / nil: `FailureThreshold` 5,
  `SuccessThreshold` 1, `HalfOpenMax` 1, `OpenTimeout` 30s, `Now` `time.Now`.
- **Backoff formula.** For a 1-based `attempt`, `raw = Base * Factor^(attempt-1)`
  computed in `float64`, then **capped at `Max`** (also capped if the float
  overflows to +Inf). `attempt <= 1` uses `Base` (exponent 0). `Factor < 1` is
  treated as `1`.
- **Jitter.** If `Rand == nil` or `JitterFrac <= 0`, `Delay` returns exactly
  `raw` (truncated to whole nanoseconds). Otherwise it draws **one**
  `Rand.Float64()` per `Delay` call and returns
  `raw * (1 - JitterFrac + JitterFrac*f)` — so the delay lies in
  `[raw*(1-JitterFrac), raw]` (jitter only ever *reduces* the delay).
  `JitterFrac > 1` is treated as `1`.
- **`Retry` semantics.** `MaxAttempts <= 0` is treated as `1`. `Retry` stops
  early on a `nil` error, or on any error with `errors.Is(err, ErrPermanent)`
  (returned as-is). On exhaustion it returns an error that wraps **both**
  `ErrExhausted` and the last operation error (Go 1.20 multi-`%w`), so
  `errors.Is(result, ErrExhausted)` and `errors.Is(result, lastErr)` both hold.
- **`Retry` + breaker.** If a breaker is attached (`WithBreaker`) and `Allow`
  denies an attempt, `Retry` returns that `Allow` error **immediately** — it
  does not run the operation, does not sleep, and does not count the attempt.
- **CLI fixed config** (documented so the simulator is reproducible):
  breaker `FailureThreshold` 3, `SuccessThreshold` 2, `HalfOpenMax` 1,
  `OpenTimeout` 10s; backoff `Base` 100ms, `Factor` 2, `Max` 10s,
  `JitterFrac` 0.5, `rand.NewSource(42)`; sim clock starts at `t = 0`.

**Out of scope:** real concurrency / goroutine-safe breakers (this breaker is
single-goroutine); sliding-window or percentage-based failure counting (only
consecutive counts); "full jitter" or "decorrelated jitter" variants (only the
one reduce-toward-`raw*(1-JitterFrac)` form above); context cancellation;
metrics/observability hooks.

## Architecture

```
retry-engine/
├── state.go     — type State + constants + String; the four sentinel errors
├── breaker.go   — type Breaker, type BreakerConfig, NewBreaker, Allow, Record, State
├── backoff.go   — type Backoff, Delay
├── retry.go     — Retry, type RetryConfig, type RetryOption, WithBreaker
├── main.go      — func main, run, newSim, type sim  — the CLI simulator
└── *_test.go    — one test file per source file above, plus integration_test.go
```

All `.go` files use `package main` at the project root — one flat package, no
subdirectories. Module name is `retryengine`. `go.mod` and
`do_not_use_this_test.go` are generated by scaffolding.

**File assignment rules (strict):**

- `state.go` contains `type State`, `StateClosed` / `StateOpen` /
  `StateHalfOpen`, `(State).String`, and
  `var ErrOpen / ErrTooManyHalfOpen / ErrExhausted / ErrPermanent`. No breaker
  logic.
- `breaker.go` contains `type Breaker`, `type BreakerConfig`, `NewBreaker`,
  `(*Breaker).Allow`, `(*Breaker).Record`, `(*Breaker).State`, and whatever
  unexported transition helpers it needs. No backoff, no retry.
- `backoff.go` contains exactly `type Backoff` and `(*Backoff).Delay`.
- `retry.go` contains `Retry`, `type RetryConfig`, `type RetryOption`,
  `WithBreaker`, and an unexported options struct. It calls `Allow`/`Record` and
  `Delay`; it does not re-implement either.
- `main.go` contains `func main()`, `run(...)`, `newSim()`, and `type sim`.
- Do **not** put `Delay` in `retry.go`. Do **not** put `Allow`/`Record` logic in
  `retry.go`. Do **not** put the `State` enum in `breaker.go`.

## Data Types and Function Signatures

All `.go` source files use `package main`. Module name is `retryengine`.
Requires Go 1.22.

```go
// ---- state.go ----

type State int

const (
	StateClosed   State = iota
	StateOpen
	StateHalfOpen
)

func (s State) String() string

var (
	ErrOpen            = errors.New("circuit breaker is open")
	ErrTooManyHalfOpen = errors.New("half-open trial limit reached")
	ErrExhausted       = errors.New("retry attempts exhausted")
	ErrPermanent       = errors.New("permanent error, not retryable")
)

// ---- breaker.go ----

type BreakerConfig struct {
	FailureThreshold int
	SuccessThreshold int
	HalfOpenMax      int
	OpenTimeout      time.Duration
	Now              func() time.Time
}

type Breaker struct {
	// unexported fields: cfg, state, failCount, succCount, halfOpenInFlight,
	// openedAt. Not part of the API — tests use Allow / Record / State.
}

func NewBreaker(cfg BreakerConfig) *Breaker
func (b *Breaker) Allow() (bool, error)
func (b *Breaker) Record(success bool)
func (b *Breaker) State() State

// ---- backoff.go ----

type Backoff struct {
	Base       time.Duration
	Max        time.Duration
	Factor     float64
	JitterFrac float64
	Rand       *rand.Rand
}

func (bo *Backoff) Delay(attempt int) time.Duration

// ---- retry.go ----

type RetryConfig struct {
	MaxAttempts int
	Backoff     *Backoff
	Sleep       func(time.Duration)
}

type RetryOption func(*retryOpts)

func WithBreaker(b *Breaker) RetryOption
func Retry(cfg RetryConfig, op func() error, opts ...RetryOption) error

// ---- main.go ----

func main()
func run(in *bufio.Scanner, out *bufio.Writer)
func newSim() *sim
type sim struct{ /* unexported: start, now, b, bo */ }
```

### Export signatures

```go
var _ func(State) string = State.String
var _ func(BreakerConfig) *Breaker = NewBreaker
var _ func(*Breaker) (bool, error) = (*Breaker).Allow
var _ func(*Breaker, bool) = (*Breaker).Record
var _ func(*Breaker) State = (*Breaker).State
var _ func(*Backoff, int) time.Duration = (*Backoff).Delay
var _ func(RetryConfig, func() error, ...RetryOption) error = Retry
var _ func(*Breaker) RetryOption = WithBreaker
```

## Behavioral Specification

### `state.go` — `State`, `String`, errors

- **`(State).String()`** — `StateClosed`→`"closed"`, `StateOpen`→`"open"`,
  `StateHalfOpen`→`"half-open"`, any other value → `"unknown"`.
- The four `Err*` values are distinct sentinels compared with `errors.Is`.
  `ErrPermanent` is meant to be wrapped by an operation
  (`fmt.Errorf("...: %w", ErrPermanent)`) to tell `Retry` "do not retry this".

### `breaker.go` — the three-state machine

`NewBreaker(cfg)` fills defaults (Overview) and returns a `*Breaker` in
`StateClosed` with all counters 0.

**`Allow() (bool, error)`** — dispatch on the current state:

- **Closed** → `(true, nil)`. (Closed always permits; `Record` does the
  counting.)
- **Open**: if `Now().Sub(openedAt) >= OpenTimeout` → transition to
  **HalfOpen** (`succCount = 0`, `halfOpenInFlight = 0`) and continue to the
  HalfOpen case below. Otherwise → `(false, ErrOpen)`.
- **HalfOpen**: if `halfOpenInFlight < HalfOpenMax` → `halfOpenInFlight++`,
  return `(true, nil)`. Otherwise → `(false, ErrTooManyHalfOpen)`.

**`Record(success bool)`** — dispatch on the current state:

- **Closed**: `success` → `failCount = 0`. Failure → `failCount++`; if
  `failCount >= FailureThreshold` → **open** (state `StateOpen`,
  `openedAt = Now()`, all counters reset to 0).
- **HalfOpen**: if `halfOpenInFlight == 0` → **return, no-op** (a stray
  `Record` with no matching `Allow`). Otherwise `halfOpenInFlight--`, then:
  `success` → `succCount++`; if `succCount >= SuccessThreshold` → **close**
  (state `StateClosed`, all counters 0). Failure → **open** (state `StateOpen`,
  `openedAt = Now()`, all counters 0) — one failed trial re-opens the circuit.
- **Open**: no-op (a `Record` while open cannot have been granted by `Allow`).

**`State()`** — returns the stored `state` field verbatim. It does **not** check
the clock and does **not** perform the open → half-open transition. So after
`OpenTimeout` has elapsed but before any `Allow` call, `State()` still returns
`StateOpen`.

### `backoff.go` — `Delay`

**`(*Backoff).Delay(attempt int)`**:

1. `exp = attempt - 1` if `attempt > 1`, else `exp = 0`.
2. `factor = Factor`, clamped up to `1` if `Factor < 1`.
3. `rawF = float64(Base) * math.Pow(factor, float64(exp))`.
4. If `Max > 0` and (`rawF` is +Inf **or** `rawF > float64(Max)`) →
   `rawF = float64(Max)`.
5. If `Rand == nil` **or** `JitterFrac <= 0` → return `time.Duration(rawF)` —
   the `float64`→`time.Duration` conversion drops the fractional nanoseconds
   (`rawF` is always `>= 0` here).
6. Otherwise `jf = min(JitterFrac, 1)`, `f = Rand.Float64()` (one draw), return
   `time.Duration(rawF * (1 - jf + jf*f))`.

### `retry.go` — `Retry`, `WithBreaker`

**`WithBreaker(b)`** — returns a `RetryOption` that stores `b` in the internal
options struct.

**`Retry(cfg, op, opts...)`**:

1. Apply `opts` to a zero `retryOpts`. `sleep = cfg.Sleep` or `time.Sleep` if
   nil. `maxAttempts = max(cfg.MaxAttempts, 1)`.
2. For `attempt := 1; attempt <= maxAttempts; attempt++`:
   - If a breaker is set: `ok, berr := breaker.Allow()`; if `!ok` → **return
     `berr`** (no `op`, no sleep, loop ends).
   - `err := op()`.
   - If a breaker is set: `breaker.Record(err == nil)`.
   - `err == nil` → return `nil`.
   - Save `err` as `lastErr`. If `errors.Is(err, ErrPermanent)` → return `err`.
   - If `attempt == maxAttempts` → break out of the loop.
   - If `cfg.Backoff != nil` → `sleep(cfg.Backoff.Delay(attempt))`. **`attempt`
     here is the 1-based number of the attempt that just failed** — so the first
     sleep is `Delay(1)`.
3. Return `fmt.Errorf("%w (%d attempts): %w", ErrExhausted, maxAttempts, lastErr)`.

### `main.go` — the CLI simulator

`newSim()` builds a `sim` with the fixed config (Overview), a sim clock starting
at `time.Unix(0, 0).UTC()`, a breaker whose `Now` returns the sim clock, and a
backoff seeded `rand.NewSource(42)`.

`run` reads stdin line by line; trims; skips blank lines and `#` comments;
prints `sim.run(line)` per line. Commands:

| command | fields | action | output |
|---|---|---|---|
| `now` | 1 | — | `sim.now.Sub(start).String()` (e.g. `"0s"`, `"12s"`) |
| `advance <dur>` | 2 | `now += ParseDuration(dur)` | `"ok"` (bad duration → `"error: bad duration <dur>"`) |
| `allow` | 1 | `Breaker.Allow()` | `"allow"` / `"deny: open"` / `"deny: half-open full"` |
| `record ok` \| `record fail` | 2 | `Breaker.Record(arg == "ok")` | `"ok"` |
| `state` | 1 | `Breaker.State().String()` | `"closed"` / `"open"` / `"half-open"` |
| `delay <attempt>` | 2 | `Backoff.Delay(attempt)` | Go duration string |

Unknown command → `"error: unknown command <field0>"`. Wrong field count for a
known command → `"error: usage: ..."`. `main` prints each result plus a newline.

## Domain-Specific Test Scenarios

The state-machine transitions and the exact jittered delays are what a test
would otherwise guess. Each bead asserts **these exact sequences / values**;
the matching Decomposition Notes pins carry them into the bead specs.

### `breaker` bead — lifecycle (config: FailThr 3, SuccThr 2, HalfOpenMax 1, OpenTimeout 10s; clock starts at 0)

| # | call | returns | `State()` after |
|---|---|---|---|
| 1 | `Allow()` | `true, nil` | closed |
| 2 | `Record(false)` | — | closed (`failCount` 1) |
| 3 | `Allow()`; `Record(false)` | `true, nil`; — | closed (`failCount` 2) |
| 4 | `Allow()`; `Record(false)` | `true, nil`; — | **open** (`failCount` hit 3) |
| 5 | `Allow()` (clock still 0) | `false, ErrOpen` | open |
| 6 | advance clock to 10s; `State()` | — | **open** (State does not advance) |
| 7 | `Allow()` (clock 10s) | `true, nil` | **half-open** (transition + slot reserved) |
| 8 | `Allow()` again | `false, ErrTooManyHalfOpen` | half-open |
| 9 | `Record(true)` | — | half-open (`succCount` 1) |
| 10 | `Allow()`; `Record(true)` | `true, nil`; — | **closed** (`succCount` hit 2) |

Half-open failure path: from state half-open with a slot reserved,
`Record(false)` → **open**, `openedAt` = current clock.

Consecutive-reset: in closed with `failCount` 2, `Record(true)` → `failCount` 0
(a single success resets the run), so the next two failures do **not** open it.

### `backoff` bead — `Delay(attempt)`

No jitter (`Rand == nil`), `Base` 100ms, `Factor` 2, `Max` 10s:

| attempt | `Delay` |
|---|---|
| 0 | `100ms` |
| 1 | `100ms` |
| 2 | `200ms` |
| 3 | `400ms` |
| 4 | `800ms` |
| 7 | `6.4s` |
| 8 | `10s` (capped: `100ms·2^7 = 12.8s`) |

With jitter (`JitterFrac` 0.5, `rand.NewSource(42)`), calling `Delay(1)` …
`Delay(8)` **in sequence on one `*Backoff`** (one `Float64` draw per call):

| call order | `Delay` |
|---|---|
| `Delay(1)` | `68.651418ms` |
| `Delay(2)` | `106.600049ms` |
| `Delay(3)` | `320.81877ms` |
| `Delay(4)` | `483.527481ms` |
| `Delay(5)` | `835.054766ms` |
| `Delay(6)` | `2.213109279s` |
| `Delay(7)` | `5.801206834s` |
| `Delay(8)` | `6.922229249s` |

### `retry` bead — driver

| scenario | assertions |
|---|---|
| op always fails, `MaxAttempts` 3, `Backoff{Base:1s, Factor:2}`, fake `Sleep` | op ran 3×; recorded sleeps are exactly `[1s, 2s]`; result `errors.Is` both `ErrExhausted` and the last op error |
| op returns `fmt.Errorf("x: %w", ErrPermanent)`, `MaxAttempts` 5 | op ran **1×**; result `errors.Is(_, ErrPermanent)`; result is the op's error unchanged |
| op fails twice then succeeds, `MaxAttempts` 5 | op ran 3×; result `nil` |
| breaker pre-tripped to open, `WithBreaker(b)`, `MaxAttempts` 3 | op ran **0×**; result `errors.Is(_, ErrOpen)` |
| `MaxAttempts` 0, op fails once | treated as 1 attempt; op ran 1×; result `errors.Is(_, ErrExhausted)` |

## Cross-Bead Contracts

### breaker -> retry (protocol)

- **type**: protocol
- **producer**: `breaker`
- **consumer**: `retry`
- **interface**: `Allow() (bool, error)` and `Record(success bool)` on
  `*Breaker`. `Allow` returning `(false, err)` means "do not proceed"; `err` is
  `ErrOpen` or `ErrTooManyHalfOpen`.
- **notes**: `Retry` calls `breaker.Allow()` **before** each attempt; if it
  returns `!ok`, `Retry` returns that `err` verbatim and does **not** call `op`,
  `Record`, or `sleep`. After an attempt that ran, `Retry` calls
  `breaker.Record(err == nil)` exactly once. `Retry` never calls `State()`.

### backoff -> retry (protocol)

- **type**: protocol
- **producer**: `backoff`
- **consumer**: `retry`
- **interface**: `(*Backoff).Delay(attempt int) time.Duration`, `attempt`
  1-based.
- **notes**: `Retry` calls `Delay(attempt)` after a failed attempt numbered
  `attempt` (1-based), only when `attempt < maxAttempts` and `cfg.Backoff !=
  nil`. It passes the result straight to `sleep`. So a 3-attempt retry sleeps
  `Delay(1)` then `Delay(2)` and never `Delay(3)`.

### cli -> breaker, backoff (protocol)

- **type**: protocol
- **producer**: `cli`
- **consumer**: `breaker`, `backoff`
- **interface**: `run(in *bufio.Scanner, out *bufio.Writer)` driving the command
  table in Behavioral Specification → `main.go`.
- **notes**: the CLI holds one `*Breaker` and one `*Backoff` with the fixed
  config. `advance` moves the sim clock that the breaker's `Now` closes over.
  `allow` maps `Allow()`'s three outcomes to `"allow"` / `"deny: open"` /
  `"deny: half-open full"`. Every error line begins with `"error: "`.

## Decomposition Notes

**Bead dependency order (do not reorder):**

1. **state** — `type State`, its three constants, `(State).String`, and the
   four sentinel errors. No dependencies. Owns `state.go`.
2. **breaker** — `type Breaker`, `type BreakerConfig`, `NewBreaker`, `Allow`,
   `Record`, `State`. Depends on bead 1. Owns `breaker.go`.
3. **backoff** — `type Backoff`, `Delay`. No dependencies. Owns `backoff.go`.
4. **retry** — `Retry`, `type RetryConfig`, `type RetryOption`, `WithBreaker`.
   Depends on beads 1, 2, 3. Owns `retry.go`.
5. **cli** — `main`, `run`, `newSim`, `type sim`. Depends on beads 2 and 3.
   Owns `main.go`. sizing rationale: `run` is one command switch over already-
   tested `Allow`/`Record`/`State`/`Delay` calls plus a sim-clock field; no
   shared assembly logic.
6. **integration** — the breaker lifecycle script through the CLI, plus one
   `Retry` driver scenario. Depends on beads 2, 3, 4, 5.

**Integration bead scenario (bounded):**

- Feed this exact script to `run` and assert the exact output lines:
  ```
  allow            -> allow
  record fail      -> ok
  allow            -> allow
  record fail      -> ok
  allow            -> allow
  record fail      -> ok
  state            -> open
  allow            -> deny: open
  advance 10s      -> ok
  allow            -> allow
  record ok        -> ok
  state            -> half-open
  allow            -> allow
  record ok        -> ok
  state            -> closed
  delay 1          -> 68.651418ms
  delay 2          -> 106.600049ms
  delay 3          -> 320.81877ms
  ```
- Separately: `Retry(RetryConfig{MaxAttempts: 3, Backoff: &Backoff{Base: time.Second, Factor: 2}, Sleep: rec}, opAlwaysFails)`
  where `rec` appends to a slice — assert the op ran 3 times, the slice is
  `[1s, 2s]`, and `errors.Is(result, ErrExhausted)`.

- **Pin — `breaker` bead, transition rules (verbatim, must be followed exactly,
  not re-derived or paraphrased):** Closed → Open on `FailureThreshold`
  **consecutive** failures (any success in Closed resets `failCount` to 0);
  opening sets `openedAt = Now()` and resets every counter. Open →
  (false, `ErrOpen`) until `Now().Sub(openedAt) >= OpenTimeout`; the **first
  `Allow` after that** transitions to HalfOpen (this is the only place the
  transition happens — `State()` never advances it). HalfOpen: `Allow` reserves
  one of `HalfOpenMax` slots or returns (false, `ErrTooManyHalfOpen`); `Record`
  with no slot reserved is a no-op; `SuccessThreshold` **consecutive** trial
  successes → Closed; **one** trial failure → Open. `NewBreaker` defaults for a
  `<= 0`/nil field: FailureThreshold 5, SuccessThreshold 1, HalfOpenMax 1,
  OpenTimeout 30s, Now `time.Now`.
- **Pin — `breaker` bead, lifecycle scenario (verbatim):** with config
  (FailThr 3, SuccThr 2, HalfOpenMax 1, OpenTimeout 10s) and clock at 0 —
  3 consecutive `Allow`+`Record(false)` → `State()` is `open` and the next
  `Allow()` returns `(false, ErrOpen)`; after advancing the clock to 10s,
  `State()` is still `open`; the next `Allow()` returns `(true, nil)` and
  `State()` becomes `half-open`; a second immediate `Allow()` returns
  `(false, ErrTooManyHalfOpen)`; then `Record(true)`, `Allow()`, `Record(true)`
  → `State()` is `closed`.
- **Pin — `backoff` bead, exact delays (verbatim):** no jitter, `Base` 100ms,
  `Factor` 2, `Max` 10s: `Delay(0)` = `Delay(1)` = `100ms`, `Delay(2)` =
  `200ms`, `Delay(3)` = `400ms`, `Delay(4)` = `800ms`, `Delay(7)` = `6.4s`,
  `Delay(8)` = `10s` (capped). With `JitterFrac` 0.5 and
  `rand.New(rand.NewSource(42))`, calling `Delay(1)`…`Delay(8)` in order on one
  `*Backoff` yields exactly: `68.651418ms`, `106.600049ms`, `320.81877ms`,
  `483.527481ms`, `835.054766ms`, `2.213109279s`, `5.801206834s`,
  `6.922229249s`. Jitter formula: `raw * (1 - JitterFrac + JitterFrac*f)`, one
  `Float64` draw per call.
- **Pin — `retry` bead, driver behaviour (verbatim):** `MaxAttempts <= 0` → 1.
  Stops on `nil` or `errors.Is(err, ErrPermanent)` (permanent error returned
  as-is, op **not** retried). On exhaustion returns
  `fmt.Errorf("%w (%d attempts): %w", ErrExhausted, maxAttempts, lastErr)` so
  `errors.Is` matches both `ErrExhausted` and `lastErr`. Sleep between attempts
  is `Backoff.Delay(attempt)` with `attempt` the 1-based just-failed attempt —
  a 3-attempt run sleeps `Delay(1)`, `Delay(2)`, never `Delay(3)`. With
  `WithBreaker`: `Allow()` before every attempt (deny → return the `Allow`
  error immediately, no op / no sleep / no attempt counted), `Record(err==nil)`
  after every attempt that ran.
- **Pin — `cli` / `integration` beads, output (verbatim):** `allow` →
  `"allow"` / `"deny: open"` / `"deny: half-open full"`; `state` →
  `"closed"` / `"open"` / `"half-open"`; `record ok|fail` and `advance <dur>` →
  `"ok"`; `now` → the sim elapsed as a Go duration string; `delay <n>` → a Go
  duration string. Unknown command → `"error: unknown command <name>"`. Each
  result is printed on its own line.
