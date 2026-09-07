# EXECUTE_BEAD empty-turn redirect

Status: implemented on `feat/execute-empty-turn-redirect` (off `main` `fe9ee41`).
Extends the PR #3 tool-loop cap/redirect pattern and PR #8's `progressTracker`
(`docs/execute-progress-detection-plan.md`).

## Problem (lsystem-baseline-1 bead 2, 2026-09-06)

`muse-glimmer:30b-q8_0-dflash` stalled EXECUTE_BEAD twice on the `grammar` bead
(~150 lines / 6 functions). Each attempt was a single ~14-minute turn of pure
chain-of-thought — `content_chars=0` the whole time, `thinking_chars` climbing to
~33k — that ended with either **no tool call at all** (attempt 2) or **one
throwaway `read_file`** (attempt 1). No `write_file` call ever landed; zero bytes
were written.

The existing backstops caught it, but slowly and without recovering:

- attempt 1: the 12-minute `execCheckpointInterval` fired, saw no productive
  write, injected `buildStallFinalizeDirective` ("write your best current version
  and stop"). muse kept thinking. `stalled` at ~19 min.
- attempt 2: the 15-minute `execStallWindow` ("no output file changed for 15m")
  fired. Same finalize directive, same result. `stalled` at ~29 min.

`escalateOnRepeatedStall` (2×) then escalated to the user — correct outcome, but
the generic finalize directive never had a chance of breaking a planning spiral,
and each attempt burned 15–30 min of GPU time to reach a verdict that was
mechanically knowable the instant turn 1 ended.

## Fix

A new per-turn signal, additive to `progressTracker`:

**empty turn** = a completed turn that made no productive write *and* nothing has
been written to disk at all this attempt (`write_file` call count still zero).
Covers both observed shapes — zero tool calls, and a lone `read_file` after a
long think — under one predicate: "emitted nothing actionable, and there is still
no output file." A turn that wrote something earlier in the attempt is never
"empty"; that case stays with the wall-clock backstops.

- `progressTracker.emptyTurnStreak` counts consecutive empty turns. Any
  productive write, or any non-empty turn once something is on disk, resets it.
- **First empty turn** → inject `buildEmptyTurnRedirect` once, **before the
  wall-clock checks run for that turn**: *"Your last turn wrote nothing to disk …
  Stop planning now. In your very next turn, call write_file … with a minimal
  version that compiles — it does not need to be complete or correct yet. You can
  revise it with further write_file calls afterward."* Explicit permission to
  start rough is what works with a reasoning model's nature; "write your best
  version and stop" does not.
  - The ordering matters. A single muse-glimmer think turn routinely runs 15–24
    minutes (attempt 2: one 24-minute turn, `done_reason=length`), so by the time
    turn 1 *ends*, `execStallWindow` (15m) is already exceeded. If `wall()` were
    checked first it would inject the generic finalize directive and the redirect
    would never fire — this is exactly what the first committed draft did. So on
    the first empty turn the redirect wins; from turn 2 on, `wall()` (spiral /
    15m window / turn cap) owns escalation again.
- **`execEmptyTurnStreakLimit` (3) consecutive empty turns** → end the attempt
  `termination_cause='stalled'` immediately. This is the final catch for *fast*
  empty turns (code as prose, quick no-op responses) that never trip a wall-clock
  predicate. For the slow planning-spiral case, `wall()` → finalize → `stalled`
  normally gets there first (redirect turn 1, finalize turn 2, `stalled` turn 3).
  Downstream is unchanged either way: `stalled` feeds `stalledExecutionNote` +
  `countTrailingStalls` + `escalateOnRepeatedStall`.

Detection is the instant a turn ends. The redirect is a nudge, not a terminator:
a model that just needed one long think before writing is unaffected (its first
write clears the streak). A legitimately productive long EXECUTE makes
`write_file` calls throughout and never accumulates a streak.

### Cost on non-recovery

If the redirect does **not** work, escalation is slower than PR #8 alone for the
slow-turn shape: the redirect buys the model one more full think turn (~one
checkpoint interval) before `wall()` fires the finalize directive, so a
non-recovering attempt runs to roughly `execAbsoluteCeiling` (45m) instead of
~19–29m. Accepted: the upside is completing the bead instead of escalating, and
the ceiling still bounds the downside. Fast empty turns are strictly faster than
before.

### What stays as the backstop

`execCheckpointInterval` / `execStallWindow` / `execAbsoluteCeiling` and the
`buildStallFinalizeDirective` path are untouched. They remain the mechanism for:

- a model that makes productive writes but never converges (extends past soft
  checkpoints, ends `timeout` at the ceiling), and
- a model that wrote something and then went quiet (15-minute window → finalize
  → `stalled`).

The empty-turn path only front-runs them for the "never emits anything
actionable" case.

### `no_write`

The old `termination_cause='no_write'` (emitted after the `buildNoWriteWarning`
nudge produced a second zero-tool-call turn) is folded into `stalled` — same
"label this distinctly, don't call it success" intent, but now on the code path
that ADJUDICATE actually has handling for. `no_write` stays in the DB `CHECK`
constraint (historical rows) and is still emitted by the offline `bakeoff`
harness; `buildNoWriteWarning` is retained for that harness only.

## Scope note (broadened from the original brief)

The framework brief defined an empty turn as "NO tool calls AND no file delta".
The live trace showed attempt 1 ending its 14-minute think with a single
`read_file`, so the literal definition would have missed half the real failure.
The predicate here is the broader "no productive write while nothing has ever
been written", which catches both attempts and still cannot fire on a run that is
making progress.

## Tests

- `progress_test.go`: `TestProgressTracker_EmptyTurnStreak` — streak accumulation
  and reset semantics.
- `workspace_test.go`:
  - `TestRunExecuteBeadReal_EmptyTurnRedirectBreaksTheSpiral` — turn 1 empty,
    redirect, then a real write → `success`, streak cleared.
  - `TestRunExecuteBeadReal_LsystemBead2PlanningSpiralRegression` — the real
    failure shape (long think + lone `read_file`, never writes) → one redirect →
    `stalled` in ≤ 4 turns, no wall-clock wait, no finalize directive.
  - `TestRunExecuteBeadReal_ReadOnlyNeverWritingIsStalledAfterRedirect` —
    `read_file`-only model → redirect → `stalled`, does not route through the
    graceful-finalize path.
  - `TestRunExecuteBeadReal_SteadyProgressIsNotStalled` — asserts a
    write-every-turn model never sees the redirect.
- `no_write_test.go`: `TestRunExecuteBeadReal_PersistentEmptyTurnsAreStalled`
  (renamed from `…IsLabeledNoWrite`) — now asserts `stalled`.

Full `internal/execution` suite + `go vet ./...` + `go test -race
./internal/execution/...` green.

## Verification against the real case (done 2026-09-06, offline)

`clone-project --from=-1` on `qual-corpus-lsystem-1/corpus.db` → new project, bead
1 (`expr`) preserved as succeeded, bead 13 (`grammar`, the old bead 2) revived to
`pending` at **revision 3** — the 5,078-char spec ADJUDICATE built from the two
live stalls. Branch binary (`8f91258`) deployed to the corpus dir only; live
`ratchet.db` untouched. Daemon on `:7475`, `muse-glimmer:30b-q8_0-dflash` fleet.

### Result: mechanism works, bead still does not complete — and that is correct

**Attempt 1** (`bead-13-attempt-1.log`, exec 5), 3 turns / **8m47s**:

| Turn | model action | loop |
|---|---|---|
| 1 | think → `read_file grammar.go` | `[injected: write-now redirect …]` |
| 2 | think → `read_file rewrite.go` | `[progress] still nothing written to disk (2 turns) after the redirect` |
| 3 | think → `run_command grep -rn ParseSystem` | `[terminated: stalled — 3 consecutive turns wrote nothing to disk after the redirect]` |

`termination_cause=stalled`. Compare the two live attempts: **~19 min** and
**~29 min**, each a single 15–24-minute pure-think turn caught by the 12-min
checkpoint / 15-min window. The empty-turn streak now catches the same failure in
**~9 min, deterministically, on turn count** — no wall-clock wait, no generic
finalize directive. Detection fired on turn 1's *lone `read_file`* — the shape the
original brief's "no tool calls" definition would have missed (see Scope note).

**Attempt 2** (`bead-13-attempt-2.log`, exec 6, ADJUDICATE's revised rev 4),
**30m34s**: turn 1 short (`read_file` → redirect), but **turn 2 was a single
~24-minute thinking spiral** degenerating into literal near-repetition
(`Potential issue: ParseSystem: they don't handle case where … Error.` over and
over). When turn 2 finally ended, `handleEmptyTurn` returned "" (streak 2, redirect
spent) and fell through to `wall()`, whose 15-minute window fired the finalize
directive → turn 3 → `stalled`. Then ADJUDICATE → **`ESCALATION — repeated
EXECUTE_BEAD stall, trailing_stalls=2`** — the `escalateOnRepeatedStall` path
consumes the new `stalled` cause end-to-end, confirmed.

**The redirect does not speed up the one-giant-turn shape.** Attempt 2 (and both
original live attempts) spent ~24 min inside a single `content_chars=0` thinking
turn; the redirect fires once on turn 1 but cannot interrupt a turn already in
progress, so `wall()` / the ceiling still governs. Net time was ~30 min, ≈ the
pre-PR-#10 baseline (~29 min) — neutral, not a regression. The streak only
accelerates escalation when the empty turns are *short* (attempt 1: 3 × ~3 min →
9 min). Closing the giant-turn gap needs a separate mechanism — a "nothing
written after N minutes → stalled" ceiling, or a mid-stream content-stall
watchdog (`memory/project_execute_progress_detection`).

### Why the redirect does not break *this* spiral

The trace shows it is **not planning paralysis** — it is a spec contradiction the
model correctly refuses to guess past. `grammar_test.go` (regenerated by
REFINE_TESTS this run) uses rule syntax `A(x) -> F(x)` (parameters in the head).
The bead spec's `parseRule` prose says *"if `bodyStr` starts with `(`, parse
formal parameters"* — i.e. parameters **after** `->`. The design doc
(`docs/design-docs/lsystem-studio-design-doc.md` §"Rule syntax") is unambiguous
and agrees with the test: one-letter head, then optional `(params)`, then `->`.
DECOMPOSE's rev 1 garbled that, and every revision after it (including ADJUDICATE
pasting an "implementation" that followed the garbled version) carried the error
forward. muse spends turns 2–3 reading `rewrite.go` and grepping for `ParseSystem`
usages looking for a tiebreaker, and will not commit a guess.

"Write a minimal version that compiles" does not help when the blocker is *not
knowing what the code should do*. This is the right outcome: a bead whose spec
contradicts its locked tests should escalate to a human, fast and cleanly — which
is exactly what the empty-turn path now delivers. It is not the redirect's job to
make the model invent a grammar.

### Takeaways

- **Mechanism validated end-to-end**: empty-turn detection (incl. the
  lone-`read_file` shape), the one redirect, `emptyTurnStreak → stalled`, and
  ADJUDICATE's `escalateOnRepeatedStall` (2× → `ESCALATION`) all fire as designed
  on real `muse-glimmer` traffic. ~2× faster than the wall-clock backstops when
  the empty turns are short; neutral when the model does one giant thinking turn.
- **The redirect's recovery hypothesis did not hold for this bead** — but the
  bead was unrecoverable for a reason no EXECUTE-side fix addresses (garbled
  spec). The correct next lever is DECOMPOSE fidelity to the design doc
  (`memory/project_decompose_precision`), not more EXECUTE nudging.
- The stall is **deterministic** (3rd occurrence on this bead). Whether it is
  `muse-glimmer`-specific is untested — but a spec/test contradiction would trip
  any model that does not guess.
- No regression: bead 1 (`expr`) and the whole bootstrap ran clean; one transient
  `REFINE_TESTS_WRITE` `failed_retry` recovered on its own (PR #9 path).
