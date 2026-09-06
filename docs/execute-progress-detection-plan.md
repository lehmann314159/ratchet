# EXECUTE_BEAD progress / stall detection — build plan

**Status:** MERGED to `main` 2026-09-06 (PR #8, commits `0077893` + `57224fc`,
fast-forwarded to `7e3952a` together with PR #9). Full suite + `go vet ./...` +
`-race` green. Live no-regression validated exprvm-web-baseline-15 (`0077893`
only) and baseline-16 (full bundle); the `termination_cause='stalled'` path
itself had no live coverage at merge and is expected to arrive organically —
merged on the fake-Ollama e2e + `-race` coverage per the "keep branch structure
simple" call. Live `ratchet.db` gets `migrateExecutionsTerminationCauseStalled`
on the next daemon start (dry-run validated against a copy: 247 execs preserved,
idempotent, integrity ok).

**Superseded detail:** the "One deviation" note below (budget checkpoint extends
with no cap; absolute `execCeiling` = 3×budget ≤ 60m) was itself superseded by
`57224fc` (`docs/execute-checkpoint-decouple-plan.md`) — checkpoint cadence and
ceiling are now **fixed constants** (`execCheckpointInterval` = 12m,
`execAbsoluteCeiling` = 45m), not budget-derived, and the timeout-doubling
machinery is retired entirely.

One deviation from the design below, decided during Phase 2: the budget
checkpoint **extends on any forward progress, with no `execMaxBudgetExtensions`
cap**. The cap produced a false finalize-directive for a genuinely-progressing
but slow model once it was spent; the absolute `execCeiling` (3×budget, ≤ 60m)
is the real bound and terminates such a model as `timeout` (progress was seen),
routing to ADJUDICATE's normal timeout path. `extensionsUsed` survives only as a
log counter.
**Origin:** `memory/project_execute_progress_detection`, root-caused during
`exprvm-web-baseline-14` bead 318 (`handlers-templates`, combined) —
`~/Documents/ratchet-projects/qual-corpus-baseline-14-REPORT.md`,
`memory/handoff_exprvm_web_baseline_14`.
**Separate-conversation note:** framework work
(`memory/feedback_separate_framework_from_project_runs`) — its own conversation.
**Family:** one level up from `docs/` tool-loop length-cap fix
(`memory/project_tool_loop_length_cap_fix`, PR #3) — that bounds a single
reasoning turn; this bounds no-progress *across* turns/attempts. Touches the same
code path as PR #7 (`internal/execution/workspace.go` + `bead.go`
`runExecuteBeadReal`).

## The failure (mechanically observed, baseline-14 bead 318)

`EXECUTE_BEAD` = `muse-glimmer:30b-q8_0-dflash`. Its only stop condition is the
wall-clock `execution_budget`. On `termination_cause=timeout`,
`ADJUDICATE_NEXT_EXECUTION` mechanically doubles the budget
(`countTrailingTimeouts` ≥ 1 → `enforcedTimeoutBudget`: 900 → 1800 → 3600, capped
8×default) and retries the **identical spec**.

- attempt 1: `timeout` @ 900s — wrote `handlers.go` + `templates.go` (good), PR #7
  copied them back.
- attempt 2: `timeout` @ 1800s — pure reasoning spiral. TURN 3 reached in 32 min
  (one non-terminating ~16 384-token think stream per turn ≈ 30 min at ~9 tok/s),
  **zero `write_file` calls**, no-write warning injected, kept spiralling.
- ADJUDICATE: `execute_revised`, `trend=same`, spec "copied verbatim", budget
  1800 → 3600. Its own thinking: *"the budget keeps climbing mechanically each
  time"* — it knows more time is not the fix; doubling is its only lever.
- attempt 3: 3600s window, manually `full_stop`ped.

There is a real underlying bug (a `handlers_test` persistence assertion expecting
6 history entries), so the bead genuinely was not done — but the model never
committed an edit after attempt 1. **Zero forward progress across attempts;
nothing detects "same spec, same wall, no measurable progress — stop retrying."**
Budget *size* is not the failure; the absence of a progress/stall signal is.

## What exists today (verified against `7aa8c02`)

| Piece | Location | Behaviour |
|---|---|---|
| EXECUTE tool loop | `internal/execution/bead.go` `runExecuteBeadReal` | **unbounded** `for turn := 1; ;`. Per-turn `NumPredict: 16384`, unbounded HTTP client. |
| Budget stop | same | goroutine: budget timer → `close(softStopCh)` (soft, checked between turns) + `time.AfterFunc(writeGracePeriod=2m)` → `terminationCh <- "timeout"; cancel()` (hard). |
| Zero-tool-call turn | same | `execcheck.VerifyExitCriteria` → pass ⇒ `success`; else one no-write warning ⇒ `no_write`/`success`. |
| In-sandbox work | PR #7, `workspace.go` | loop + in-loop exit check run in a per-attempt temp dir; `copyBack(expectedFiles)` on every exit path (defer). |
| Budget doubling | `adjudicate_next_execution.go` `countTrailingTimeouts` / `enforcedTimeoutBudget` / `timeoutBudgetNote` | keys on `termination_cause='timeout'` **only** (`monitor_terminated` / `no_write` already excluded). |
| Schema | `internal/db/schema.sql:96` | `termination_cause CHECK IN ('success','timeout','monitor_terminated','monitor_force_killed','no_write')`. Adding a value ⇒ table-rebuild migration (`migrateExecutionsTerminationCause` is the exact template). |
| Monitor stall check | `internal/execution/monitor.go` `isWriteFileStall` | independent, mechanical: 24 stale ticks (~12 min) + last trace line is a `[TURN` marker ⇒ FIRE (catches "stuck mid-`write_file`-arg"). Stays as-is. |

## Design

### 1. Progress tracker — `internal/execution/progress.go` (+ `progress_test.go`)

Pure type, no DB / ctx, owned by the loop goroutine. Publishes one
`atomic.Int64` (`lastProductiveUnixNano`, initialised to loop start) that the
timer goroutine reads.

**A turn is _productive_ iff** a `write_file` call this turn
  - targeted a `filepath.Clean` member of `expectedFiles` (the this-attempt
    writable list already computed in `bead.go`), **and**
  - its tool result began with `ok:`, **and**
  - left that file at a SHA-256 it has **not held before this attempt**
    (per-file set of seen hashes — also rejects A→B→A oscillation, not just
    byte-identical re-writes).

Per turn the loop feeds the tracker one observation:

```go
type turnObs struct {
    turn          int
    productive    bool
    lengthCapEmpty bool // msg.DoneReason=="length" && len(ToolCalls)==0 && Content==""
    identicalCall  bool // same {tool name+args -> result} as the immediately prior turn, no productive write between
}
```

Tracker state: `nonProductiveStreak`, `spiralStreak` (consecutive
`lengthCapEmpty`), `identicalStreak` (consecutive `identicalCall`),
`lastProductiveUnixNano`.

`func (t *progressTracker) wall(now time.Time) (stall bool, reason string)` — true iff any:

| Predicate | Threshold | Catches |
|---|---|---|
| `now - lastProductive > execStallWindow` **and** `nonProductiveStreak ≥ 1` | `execStallWindow = 15m` | baseline-14 attempt-2 (turns advance, reads/thinks, zero novel writes) |
| `spiralStreak` | `≥ 2` | pure reasoning spiral (16 384-token think, nothing emitted, twice running) |
| `identicalStreak` | `≥ 3` | fast degenerate tool loop that would not trip the wall-clock for a while |
| `turn` | `≥ execMaxTurns = 50` | pure backstop against a pathological fast loop |

**Deliberately out of scope:** a *productive* turn (novel hash) that never
converges — "thrashing". That is slow progress, not a stall; it falls through to
the wall-clock ceiling ⇒ `timeout` ⇒ the existing double-and-retry / attempt-cap
path, which is acceptable. Adding an exit-criteria-pass-count signal to catch it
would mean running `go test` mid-loop every productive turn; revisit only if
baseline-15+ shows thrashing is common (skip-to-general-fix does not apply —
"no progress" is the general fix here; thrashing is a different phenomenon the
`timeout` path already handles).

### 2. Wall-clock: soft checkpoint + absolute ceiling (folds in the extension bonus)

Replace the single budget timer + `softStopCh` with a goroutine managing two
timers and a small command channel from the loop:

```
budgetDur       = execution_budget (unchanged — ADJUDICATE still owns it)
ceilingDur      = clamp(3*budgetDur, budgetDur+5m, execMaxWall=60m)
finalizeGrace   = 5m
```

Goroutine `select`s on: `sigCh` (→ `monitor_terminated`; unchanged),
`ctx.Done()`, `timers.extend` (loop → goroutine: reset the soft timer),
`timers.finalize` (loop → goroutine: `hard.Reset(finalizeGrace)`, set a
`finalizing` flag), `soft.C` (→ non-blocking notify `budgetCheckpointCh`, then
`soft.Reset(budgetDur)` — keeps pinging every interval), `hard.C` (→
`terminationCh <- cause; cancel()` where `cause = "stalled"` if `finalizing` or
`now - lastProductive > execStallWindow`, else `"timeout"`).

**Loop, between turns**, on `budgetCheckpointCh` (as built):
- `tracker.lastProductive().After(lastCheckpoint)` — a productive turn happened
  since the previous checkpoint ⇒ trace `[progress] budget checkpoint N — forward
  progress detected, extending`, `extendCh <- {}`. (No extension cap — see the
  deviation note at the top; `execCeiling` bounds it.)
- else if the finalize directive is not yet injected ⇒ inject it (§3),
  `finalizeCh <- {}`.
- else ⇒ `[terminated: stalled]`, `writeTerminationCause(execID, "stalled")`.

A genuinely progressing long bead thus gets up to `ceilingDur` (≤ 60 min)
instead of the fixed `budgetDur`; a stalled one gets the finalize directive at
the first checkpoint that shows no progress. No blanket `execution_budget_default`
change (that would invalidate the corpus).

### 3. On any stall (in-loop predicate OR checkpoint-with-no-progress)

Inject **one** graceful-finalize user turn (`buildStallFinalizeDirective`):

> No measurable progress in the last turns — no output file has changed and no
> exit criterion has newly passed. Stop analyzing. Write your best current
> version of each output file now — one `write_file` call each — then stop. The
> execution ends after your next turn.

Set `finalizeInjected = true`, `timers.finalize <- {}`, `continue`. The model
gets **exactly one** more turn. At the end of that turn (regardless of what it
did): `execcheck.VerifyExitCriteria` passes on disk ⇒ `success`; else ⇒
`termination_cause = "stalled"`. PR #7 copy-back has already taken disk state on
the defer path.

### 4. `termination_cause = "stalled"` — distinct from `timeout`

- `internal/db/schema.sql`: add `'stalled'` to the CHECK.
- `internal/db/db.go`: `migrateExecutionsTerminationCauseStalled` — rebuild
  (`migrateExecutionsTerminationCause` template: check `sqlite_master.sql`
  contains `'stalled'`; `PRAGMA foreign_keys=OFF` + `legacy_alter_table=ON`;
  rename / recreate with the full current column set incl. `infra_failure`,
  `test_first_attempt`; column-list `INSERT ... SELECT`; drop). Call it from
  `applyTableMigrations` after `migrateExecutionsTerminationCause`.
- `internal/db/models.go:146` comment: add `| 'stalled'`.
- `trace.GenerateMechanicalFindings` already prints `Termination cause: %s`
  verbatim — no change.

### 5. ADJUDICATE handles `stalled` ≠ `timeout`

`internal/verbs/adjudicate_next_execution.go`:

- **No budget doubling** — automatic: `countTrailingTimeouts` ignores non-`timeout`
  causes (a `stalled` row breaks the run). Add a regression test pinning it.
- New `stalledExecutionNote(ctx, d, beadID)` — fires when the latest qualifying
  execution's `termination_cause = 'stalled'`. Injected into `findings` in `Run`
  alongside the other notes:

  > [Stalled execution] The previous attempt made no measurable forward progress
  > (no output file changed, no new exit criterion passed) before finalizing.
  > This is NOT a wall-clock-budget problem — the budget was not increased and
  > must not be. Do NOT issue execute_revised with a substantively unchanged
  > spec. If the output files on disk already pass the exit criteria →
  > declare_success. Otherwise the spec is too large or too vague for the model
  > to execute in one attempt: either execute_revised with a **materially
  > narrowed** spec (fewer output files, a sharper contract), or full_stop if it
  > cannot be narrowed.

- New `countTrailingStalls(ctx, d, beadID)` (mirror of `countTrailingTimeouts`,
  cause `'stalled'`, current lineage, real attempts only). Cached on the handler
  like `trailingTimeouts`. **`≥ 2` ⇒ mechanically escalate the ADJUDICATE job**
  in `Run` (before the model call) — `UPDATE handoff_jobs SET status='escalated'`,
  `report.WriteBead(... "escalated")`, distinct `slog.Error("ESCALATION —
  repeated stall")`. Stops a stall→revise→stall→revise loop; the user triages
  whether the bead needs splitting (`memory/project_decompose_precision`
  bead-sizing).
- The existing `declare_success` mechanical exit-criteria gate already covers
  "the files are actually fine".

Monitor is **not** involved — a Monitor-model "is it stuck?" judgment has the
same coin-flip risk as JUDGE (`memory/project_test_action_noise_tolerance`). The
Monitor keeps its own independent `isWriteFileStall`; the two mechanical checks
have disjoint coverage (mid-arg freeze vs. multi-turn no-progress).

## Constants (one place, `bead.go` or `progress.go`)

```go
execStallWindow          = 15 * time.Minute
execHardCeilingFactor    = 3
execMaxWall              = 60 * time.Minute
execFinalizeGrace        = 5 * time.Minute
execMaxTurns             = 50
execSpiralStreakLimit    = 2
execIdenticalStreakLimit = 3
```
(all in `internal/execution/progress.go`. `testExecBudget` — a package var in
`bead.go` — overrides the budget interval for tests.)

## Regression coverage

**`progress_test.go`** (pure): streak transitions; a productive turn resets
`nonProductiveStreak` and pushes `lastProductive`; hash-oscillation A→B→A — the
second A is non-productive; `spiralStreak`/`identicalStreak` thresholds;
`turn ≥ execMaxTurns`.

**`workspace_test.go`-style e2e** through `runExecuteBeadReal` with the
`httptest` fake Ollama (helpers already there):
1. **Stall → finalize → clean terminal** — fake model returns `read_file` calls
   every turn (non-productive, non-zero-tool-call). With a tiny `budgetDur`, the
   checkpoint fires ⇒ finalize directive appears in the trace ⇒ next turn still
   `read_file` ⇒ `termination_cause = "stalled"`.
2. **Slow progress → not killed** — fake model writes a *changed* `game.go`
   every turn for 5 turns then `writeDone()` ⇒ `success`, no `[progress] stall`
   line, no premature termination.
3. **Budget checkpoint extends on progress** — tiny `budgetDur`; fake model
   writes a novel `game.go` each turn; assert `[progress] budget checkpoint 1 …
   extending` in the trace and the run completes `success` past the first
   checkpoint.
4. **`lengthCapEmpty` twice ⇒ stalled** — fake model returns
   `done_reason:"length"`, empty content, no tool calls, twice ⇒ finalize ⇒
   `stalled`.

**`verbs` e2e** (`commit_test.go` / `adjudicate_smoke_test.go` patterns):
- Seed two `stalled` executions → `Run` escalates the ADJUDICATE job (status
  `escalated`, no EXECUTE re-queued).
- Seed one `stalled` execution → `execute_revised` → committed `bead_revisions`
  row `execution_budget == default` (**not** doubled); `stalledExecutionNote`
  text present in the built user message.
- `countTrailingTimeouts` returns 0 when the latest cause is `stalled`.

Full suite + `go vet` green before the merge request.

## Blast radius (audited)

| Path | Interaction |
|---|---|
| `RunExecutionWindow` (`window.go`) | none — still waits on `executeDone`; a `stalled` exit is an ordinary process exit that wrote `termination_cause`. `finalizeExecution` enqueues ANALYZE as always. |
| Monitor subprocess | none — independent; if it FIREs first, `monitor_terminated` wins (already excluded from doubling). |
| PR #7 sandbox / copy-back | none — the stall paths all return through the same defers. |
| `exec-bakeoff` | none — does not call `runExecuteBeadReal`. |
| `no_write` / existing zero-tool-call handling | unchanged and checked first each turn; the tracker's between-turn `wall()` check runs after it, so `success`/`no_write` still take precedence. |
| verb-io capture | none — `EXECUTE_BEAD` excluded. |
| Old DBs | migration adds `'stalled'`; DBs with `no_write` but not `stalled` are handled by the `sqlite_master.sql` contains-check. |

## Phases / gates — all DONE

1. **Progress tracker + unit tests** — `internal/execution/progress.go` +
   `progress_test.go` (9 tests). Tracker has no I/O (`time` + `sync/atomic`
   only). ✅
2. **Wire into `runExecuteBeadReal`** — per-turn `turnObs`, timer-goroutine
   restructure (soft checkpoint + absolute ceiling + `extendCh`/`finalizeCh`),
   `injectFinalize`, `stalled` cause, `hashFileHex` / `toolTurnSignature` /
   `buildStallFinalizeDirective`, `testExecBudget` seam. 4 new e2e tests in
   `workspace_test.go` (stall→finalize→stalled, partial-progress-survives-stall,
   reasoning-spiral→stalled, steady-progress→success+extend);
   `no_write_test.go` + existing `workspace_test.go` unchanged and green. ✅
3. **Schema + migration** — `schema.sql` CHECK, `migrateExecutionsTerminationCauseStalled`
   wired after `migrateExecutionsTerminationCause`, `models.go` comment. Tests:
   `TestMigrateExecutionsTerminationCauseStalledPreservesData` (+ idempotency +
   bogus-value rejection), `TestFreshSchemaHasStalledInCheck`. ✅
4. **ADJUDICATE branch** — `stalledExecutionNote` (injected in `Run` after
   `orientationOnlyNote`), `countTrailingStalls`, `h.trailingStalls`,
   `escalateOnRepeatedStall` (called in the `execute_as_is` / `execute_revised`
   branches after `atExecutionCap`). 5 tests in `stalled_execution_test.go`
   including "a stall does NOT double the budget" and "2 consecutive stalls
   escalate". ✅

Full suite + `go vet ./...` green; `go build -o ratchet ./cmd/ratchet/` OK.

## After merge

- PR; squash. Update `memory/project_execute_progress_detection` (status +
  as-built design), `MEMORY.md`, note the PR number / commit.
- Deploy the binary to `/Users/mike/Documents/ratchet-projects/ratchet`.
- `clone-project --from=-4` (fixture in `qual-corpus-baseline-14/corpus.db`) →
  `exprvm-web-baseline-15`, restarts at bead 318 — a genuine test (real
  `handlers_test` bug + possible `handlers-templates` split still needed), not a
  guaranteed green.
