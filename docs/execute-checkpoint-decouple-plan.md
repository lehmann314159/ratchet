# Decouple EXECUTE_BEAD progress cadence from `execution_budget`

Follow-up #1 (+ #2 folded in, #3 closed as moot) from
`memory/project_execute_progress_detection`. Built on top of PR #8
(`feat/execute-progress-detection`) — this makes PR #8's stall detection the
*only* wall-clock mechanism and retires the timeout-doubling machinery it left
in place.

## Why

PR #8 replaced the "fixed budget → timeout → ADJUDICATE doubles it → retry
identical spec" loop with mechanical stall detection, but kept `execution_budget`
driving three things at once:

1. the soft **checkpoint interval** (`budgetDur`),
2. the absolute **ceiling** (`execCeiling(budgetDur)`),
3. ADJUDICATE's **timeout reflex** (`enforcedTimeoutBudget`, `timeoutBudgetNote`,
   Commit-side budget bumps).

baseline-15 (2026-09-05): a clone carried a doubled `execution_budget=3600`
forward, so the first checkpoint wasn't due until 60 min and the
extend-or-finalize mechanism never ran. User's framing: *"the whole notion of
timeout doubling and large budgets is undercut by the better detection, and the
large budgets keep the new mechanisms from firing."*

## Decision (locked with Mike, 2026-09-06)

**Full retirement.** EXECUTE_BEAD ignores `execution_budget` for timing. Fixed
checkpoint interval + fixed ceiling. Remove timeout-doubling entirely; a repeated
`timeout` escalates instead (mirrors the 2-stall escalation). The
`execution_budget` / `execution_budget_default` columns stay in the schema (no
migration) but carry no wall-clock meaning.

## Changes

### `internal/execution/progress.go`

- Remove `execHardCeilingFactor`, `execMaxWall`, `func execCeiling`.
- Add:
  - `execCheckpointInterval = 12 * time.Minute` — soft checkpoint cadence. ≥ one
    full non-terminating muse-glimmer think turn; short enough for 3 checkpoint
    decisions inside the ceiling.
  - `execAbsoluteCeiling = 45 * time.Minute` — hard wall-clock backstop for one
    attempt. ≈ the old `execMaxWall` (60m) minus the headroom that existed only
    to absorb budget-doubling.
- Unchanged: `execStallWindow` (15m), `execFinalizeGrace` (5m), `execMaxTurns`,
  `execSpiralStreakLimit`, `execIdenticalStreakLimit`, `progressTracker`,
  `hashLedger`, `turnObs`, `wall()`.

Relationship: `execCheckpointInterval` (12m) < `execStallWindow` (15m). The
checkpoint is the primary "no forward progress" trigger; the per-turn
`execStallWindow` path in `wall()` is a backstop for a stall that develops
between checkpoint boundaries. Both inject the same one finalize directive.

### `internal/execution/bead.go` — `runExecuteBeadReal`

- `budgetDur` → `execCheckpointInterval`; `ceilingDur` → `execAbsoluteCeiling`
  (drop the `execCeiling(...)` call). `soft`/`hard` timer init, `soft.Reset`,
  `drainReset(soft, …)` all use the constants.
- Stop reading `br.execution_budget` for timing. Keep it in the query only if the
  cost row still needs it (see below).
- `slog.Info("execute-bead started", …)` — `budget_s`/`ceiling_s` →
  `checkpoint_s`/`ceiling_s`.
- `hard.C` handler unchanged: `cause = "timeout"` unless `finalizing ||
  time.Since(tracker.lastProductive()) > execStallWindow`. A model making a novel
  write every checkpoint but never converging by 45m ⇒ `timeout` ⇒ new
  repeated-timeout escalation.
- `testExecBudget` var → `testExecCheckpointInterval`; add `testExecCeiling` if a
  test needs to force the hard wall. Rename `withTestExecBudget` in
  `workspace_test.go` accordingly.
- Stub-mode `budgetTimer` (`--mode` smoke path) left as-is.

### `internal/verbs/adjudicate_next_execution.go`

Retire timeout-doubling:

- Delete `enforcedTimeoutBudget()`, `timeoutBudgetNote()`.
- Delete the `if h.trailingTimeouts >= 1 { findings += timeoutBudgetNote(...) }`
  injection in `Run`.
- Delete the Commit-side `execution_budget` bumps in the `execute_as_is` and
  `execute_revised` branches. Delete the `execute_revised` `< budgetDefault`
  clamp (inert once budget has no timing effect).
- `orientationOnlyNote`: drop the "`execution_budget doubled`" sentence; keep the
  "Begin writing to output_files immediately" prepend directive.

Add repeated-timeout escalation (follow-up #2):

- `escalateOnRepeatedTimeout(ctx, tx, projectID, beadID, now, jobID)` — exact
  mirror of `escalateOnRepeatedStall`: `h.trailingTimeouts >= 2` → set the
  ADJUDICATE job `escalated`, `report.WriteBead(..., "escalated")`, return
  `true`. Called in both `execute_as_is` and `execute_revised` immediately after
  `escalateOnRepeatedStall`.
- `timeoutExecutionNote(ctx, d, beadID)` replacing `timeoutBudgetNote` — fires
  when the latest qualifying execution ended `timeout`. Framing mirrors
  `stalledExecutionNote`: the attempt made forward progress but did not converge
  within the fixed 45-minute ceiling ⇒ the bead is likely too large or the spec
  too broad; **more wall-clock will not help** (the ceiling is fixed and the
  budget number is inert). Steer to `execute_revised` narrowing scope (name the
  slice to keep) or `full_stop`. A second consecutive timeout escalates
  automatically. Never `re_refine` (tests were never reached).
- Keep `countTrailingTimeouts` / `h.trailingTimeouts` (now feeds only the
  escalation + note).

### `internal/verbs/prompts.go` — `adjudicateNextExecutionSystemPrompt`

Rewrite the "Budget guidance for execute_revised" block:

- Drop "On ANY timeout … double the current `execution_budget`" and the
  "orchestrator ALSO raises it mechanically 900→1800→3600→7200" text.
- Replace: `execution_budget` / `monitor_override` are still stated explicitly
  but no longer affect wall-clock; a `timeout` is a **scope** signal (bead too
  big), not a budget signal — narrow the spec or `full_stop`; a second
  consecutive timeout escalates automatically.
- Keep the existing "do not rewrite the spec into an implementation guide on a
  timeout" and skeleton-first guidance.

### Schema / cost

- **No migration.** `bead_revisions.execution_budget`,
  `projects.execution_budget_default` stay.
- `attempt_budget_cost`: switch execute-bead's cost calculation to
  `execAbsoluteCeiling` (constant) so the column stays populated and comparable
  across attempts. Confirm the exact formula during implementation.
- DECOMPOSE still emits a per-bead `execution_budget` — now inert. Trimming the
  DECOMPOSE prompt is a separate cleanup, out of scope here.

### Follow-up #3 → closed

Nothing inflates `execution_budget` any more, so `clone` /
`reviveFullStoppedBeads` carrying it forward is harmless. Removed from the
backlog.

## Tests

| File | Change |
|---|---|
| `execution/progress_test.go` | drop `execCeiling` cases |
| `execution/workspace_test.go` | rename `testExecBudget` / `withTestExecBudget`; `TestRunExecuteBeadReal_SteadyProgressIsNotStalled` still asserts a checkpoint extension (fixed interval); drop `seedRunExecutionBudget` if unused |
| `verbs/adjudicate_next_execution_test.go` | delete `TestEnforcedTimeoutBudget` and the "Commit doubles budget on timeout" subtests; add no-double + escalate-at-2 |
| `verbs/stalled_execution_test.go` | keep stall tests; add `TestAdjudicateEscalatesOnSecondConsecutiveTimeout`, `TestAdjudicateSingleTimeoutRetriesWithoutBudgetChange` |
| `verbs/commit_test.go`, `verbs/adjudicate_smoke_test.go` | audit for budget-doubling assertions, rewrite |
| `project/*_test.go`, `verbs/debate_test.go`, `verbs/reconcile_reject_test.go` | verify unaffected (seed the column only) |

## Validation

`go test ./...` + `go vet ./...` + `go test -race ./internal/execution/
./internal/verbs/` + `go build -o ratchet ./cmd/ratchet/`. Not deployed. Then a
baseline exercises PR #8 stall + this + PR #9 idle timeout together.
