# Precision chain — order of operations

**Status:** plan — 2026-09-07. Planning artifact only; no code changes in this
doc. Execute in a dedicated framework conversation
([[feedback_separate_framework_from_project_runs]]).

**Why this doc exists:** we have several precision fixes in mind
(`decompose-precision-plan.md`, the abandoned `critique-redesign.md`,
`project_designdoc_precision_guidance`, `project_test_action_noise_tolerance`)
plus three findings from the 2026-09-07 lsystem-demo-run session, and **no single
document sequences them**. The `decompose-precision-plan.md` "REVISED ORDER" list
is DECOMPOSE-centric and predates the demo-run findings.

**The core constraint this plan is built around:** precision fixes to
design-doc / DECOMPOSE / CRITIQUE can only be *fully* validated by a full
from-scratch run (hours, and it confounds multiple changes at once). We must not
land a series of these blind and then discover the interactions in a live run.
The plan's answer: sequence by dependency and blast radius, validate every piece
offline against real corpus fixtures, and confine full-run confounding to a
single integration phase with an explicit settling budget.

---

## The motivating case: the lsystem `grammar` bead

One bead has now blocked three from-scratch lsystem runs, each on a **different**
upstream-precision defect. It is the best integration test we have.

| Run | Blocked on | Owning fix |
|---|---|---|
| lsystem-baseline-1 (2026-09-06) | DECOMPOSE compressed the design doc's "head is the substring **before the first `(`**" into "head = one ASCII letter", which then contradicted the tests | DECOMPOSE precision (A) |
| lsystem-demo-run attempt 1 (2026-09-06) | EXECUTE/MONITOR GPU-contention stream stall | fixed — `main` `893b156` |
| lsystem-demo-run attempt 2 (2026-09-07) | `grammar_test.go:135`: `axiom: F[+]` asserted to produce **3** modules; design doc line 448 says "`[` and `]` **each** emit a bracket module" → 4. REFINE_TESTS wrote a test contradicting the doc; CRITIQUE rubber-stamped it; muse spiralled and stalled | CRITIQUE spec-contradiction detection (C) + `stalledExecutionNote` re_refine branch (E) |

The demo-run attempt-2 failure also exposed that when a bead is blocked by an
impossible **locked** test, the failure surfaces as an EXECUTE *stall* (the agent
cannot satisfy the assertion), and `stalledExecutionNote`
(`internal/verbs/adjudicate_next_execution.go:311`) steers ADJUDICATE toward only
`{declare_success, execute_revised-narrowed, full_stop}` — it never offers
`re_refine`. So the CRITIQUE/REFINE_TESTS precision gap currently has **no
downstream recovery path**; it escalates.

---

## Session findings folded into this plan (2026-09-07)

- **(E) `stalledExecutionNote` has no `re_refine` branch.** A stall caused by an
  unsatisfiable locked assertion is indistinguishable, in that note's framing,
  from "spec too big for one attempt." Needs a fourth branch: *if every plausible
  implementation would fail the same locked assertion, the test itself may be
  wrong → `re_refine` with guidance.*
- **(C) CRITIQUE misses the spec-interpretation defect class.** Confirmed again:
  the `F[+]`→3 test is exactly the class `critique-redesign.md` identified as
  uncaught by every model in the pool (b314-c1 single-newline case, 0/4 across
  both bakeoff runs). The mechanical pre-pass approach is **abandoned**
  (negative result). A different angle is required — see Phase 2.
- **(F) The giant single think-turn gap.** `execEmptyAttemptCeiling` (PR #11,
  20m, nothing-written) only fires *at a turn boundary*. muse spent 25+ min in
  one continuous think turn on the `F[+]` contradiction with `content_chars=0`
  and the ceiling could not fire. PR #11's in-pocket "mid-stream content-stall
  watchdog" closes this.

---

## Order of operations

### Phase 0 — safety nets (small, localized, unit-testable, no run required) — **DONE 2026-09-07**

Both landed on `main`. They make the pipeline fail *toward* `re_refine` instead
of escalation while the larger upstream work is in flight.

1. **`stalledExecutionNote` gains a `re_refine` branch (E).** **DONE.**
   `internal/verbs/adjudicate_next_execution.go` — `stalledExecutionNote` now
   appends a fourth paragraph when **all three** hold: the latest execution's
   trace shows zero `write_file` calls (`latestExecutionWroteNothing`, new
   helper), the bead went through REFINE_TESTS (`beadHasRefinements`), and it is
   the **first** stall in the lineage (`countTrailingStalls < 2`). The paragraph
   tells ADJUDICATE that a stall with nothing written on a locked-test bead is
   the signature of an unsatisfiable LOCKED assertion, to check the locked
   assertions against Input 5 (the authoritative design excerpt), and to choose
   `re_refine` naming the specific defective assertion (test fn + wrong expected
   value + what the doc says) — only when it can point to the specific defect.
   - Interaction decisions (per Mike, 2026-09-07): **`refinementCycleCap` (5,
     Commit's `re_refine` branch) is the only loop backstop.** `re_refine` out of
     a stall does not create a `bead_revision`, so `countTrailingStalls` keeps
     climbing across cycles; gating the paragraph on `< 2` means it only nudges
     the first stall. `escalateOnRepeatedStall` was **not** added to the
     `re_refine` branch — a legitimate re_refine that found a real bad assertion
     shouldn't be blocked by a second stall (could be a second bad assertion).
   - Tests: `stalled_execution_test.go` — `TestStalledExecutionNote_ReRefineBranch`
     (4 subtests: branch present / suppressed on 2nd stall / absent for non-
     refinement bead / absent when a write happened).
   - Offline replay status: the `qual-corpus-lsystem-2` fixture `-1` predates the
     `893b156` infra fix, so all its bead-2 executions are `infra_failure=1` or
     incomplete — there is **no clean `stalled` execution to replay against**.
     Confirmed the real bead-2 traces (attempts 1–5) all have **zero write_file
     calls** and the bead has `test_refinements` rows, so the branch *would* fire
     on a clean re-run. The "does ADJUDICATE actually pick re_refine" end-to-end
     check needs a live model and is folded into Phase 3 step 7.

2. **Mid-stream content-stall watchdog (F).** **DONE.**
   - `internal/ollama`: new opt-in `Options.ContentStallTimeout`. When > 0,
     `ChatWithTools`'s stream watchdog also trips when the stream produces only
     `thinking` tokens (no content, no tool-call delta) for that long after the
     first chunk → returns the new sentinel `ErrContentStall`. **Not** transient
     (`IsTransient` unchanged) — it is real non-convergence signal. The clock is
     seeded on the first chunk (so prompt-eval doesn't eat the budget) and reset
     by any content token or tool-call delta.
   - `internal/execution`: `execContentStallTimeout = 10m` (`progress.go`), passed
     via `execOpts`; `testExecContentStallTimeout` override. `runExecuteBeadReal`
     maps `ollama.ErrContentStall` to a `stalled` termination (feeds
     `stalledExecutionNote` + `escalateOnRepeatedStall`, same as the
     turn-boundary ceilings). Sits below `execEmptyAttemptCeiling` (20m,
     turn-boundary only) and above `streamIdleTimeout` (3m, reset by every
     thinking chunk).
   - Tests: `internal/ollama/client_test.go` —
     `TestChatWithToolsContentStallAborts`, `…ResetsOnToolCall`, + `TestIsTransient`
     case. `internal/execution/workspace_test.go` —
     `TestRunExecuteBeadReal_MidTurnContentStallIsStalled`,
     `…ContentStallNotTrippedByProductiveTurn`. `go test ./... -race` + `go vet` green.

**Phase 0 exit:** both landed on `main` with unit tests. The one deferred item
(live confirmation ADJUDICATE reaches `re_refine` on the real grammar stall) is
Phase 3 step 7 — the existing fixture cannot exercise it.

### Phase 1 — upstream precision

**ORDER CORRECTED 2026-09-07 (Mike): (A) DECOMPOSE precision goes BEFORE (B)
design-doc guidance.** The "doc before DECOMPOSE, measure against improved doc
output" rationale below only bites if the docs need doc-level improvement that
changes DECOMPOSE's input — and the mature driver docs (lsystem, fractalviz,
exprvm-web) are already well-pinned. The acceptance criteria step 3 originally
gave were self-contradictory: the two lsystem grammar defects (`F[+]`→3, head
"before the first `(`") are **not** doc ambiguities — the doc is unambiguous and
pinned on both (lines 437-444 + Pin bullets 1057/1060; line 448 + worked example
772-774 + Pin 1054). They are DECOMPOSE-carry (A) and CRITIQUE (C) failures.
`project_refine_precision_phase0` also ranked B the weaker lever.

3. **DECOMPOSE precision (A).** `decompose-precision-plan.md`.
   - **Phase 1 CORE LANDED + DEPLOYED 2026-09-07 (`main` `b4ae16e`)**, scope
     "core now, escalation deferred": multi-pin-per-bead + multi-bead-pin
     injection fix (`extractDecompositionNotesPins` accumulates; `pinBeadTargets`
     / `pinTargetsRe`); `RECONCILE` re-injects over all beads each round;
     report-only `unconsumedPinTargets` (`slog.Warn` + AUDIT input).
     `TestUnconsumedPinTargets_CorpusGate` is the committed offline gate.
   - **Deferred:** structural placement (split-detection / fuzzy match) +
     reject-retry + escalate-to-user — until a baseline sizes the false-positive
     rate of the report-only check.
   - **Still to do in A:** prose-literal / construction-form pins (Phase 2 of
     `decompose-precision-plan.md`), excerpt-header reconciliation (Phase 3),
     the post-bakeoff baseline fold-in (Phase 4). **Re-measure Phase 0's payoff
     against a fresh muse-WRITE baseline before locking Phase 2/3 scope.**

4. **Design-doc precision guidance (B).** `project_designdoc_precision_guidance`.
   - Sharpen `draft-design-doc` + `cmd/checkdesigndoc` for: one unambiguous
     reading per rule; explicit anti-examples; construction-form pins; bead
     surface-area caps. Scope is two-reading rules + construction-form + bead
     caps + fresh-project onboarding — **NOT** the lsystem grammar defects.
   - Validation: `cmd/checkdesigndoc` against the `docs/design-docs/` corpus; no
     new false positives on the docs that produced clean runs (fractalviz).

**Exit Phase 1 when:** DECOMPOSE replay shows load-bearing clauses and all pins
surviving; the Phase 0 bucket re-measure shows the expected reduction; the B
checks catch their (revised) targets with no fractalviz false positives.

### Phase 2 — downstream detection (depends on Phase 1 output quality)

5. **CRITIQUE spec-contradiction angle (C).** `project_critique_repetition_detection`.
   - The mechanical pre-pass is abandoned. New angle TBD in the framework
     conversation — candidate directions: a targeted "every locked assertion
     traced to a design-doc sentence" pass; a coverage-gate nudge; feeding
     CRITIQUE the design doc diff/pins explicitly. Whatever it is, the acceptance
     bar is the same.
   - Validation: replay against the `F[+]`→3 test, the b314-c1 single-newline
     case, and the 36-catch corpus from `critique-redesign.md`. Acceptance:
     catches the `F[+]` class without losing the concrete-defect catch rate
     (67% incumbent) or adding false positives (0 incumbent).

6. **Noise-tolerant re_refine loop (D).** `project_test_action_noise_tolerance`,
   `critique-redesign.md` item 2.
   - Make CRITIQUE + JUDGE a cheap filter rather than load-bearing; sharpen /
     lower the ADJUDICATE `re_refine` threshold; add a cheaper targeted
     `re_refine` path that skips CRITIQUE/JUDGE when ADJUDICATE already has a
     concrete assertion-level fix.
   - Depends on 5 landing (the threshold change assumes CRITIQUE's signal is
     what it is post-5).
   - Validation: replay the REFINE_TESTS loop against corpus cycles JUDGE sent
     back; latency + verdict-agreement measurement per
     `refine-adjudicate-bakeoff-plan.md`.

**Exit Phase 2 when:** CRITIQUE replay catches the `F[+]` class at no cost to the
incumbent metrics; the re_refine loop's per-cycle cost drops without catch-rate
regression.

### Pre-lsystem gate — CLEARED 2026-09-07

**Decision (Mike, 2026-09-07): the landed stack is sufficient to attempt the next
lsystem run now, as an explicit settling run — do NOT wait for CRITIQUE (C).**

What clears the gate:

| lsystem blocker | addressed by | landed |
|---|---|---|
| baseline-1: DECOMPOSE garbled the "head before the first `(`" rule | the verbatim head-vs-params **pin** | `abac583` (pre-session) |
| demo-run #1: EXECUTE/MONITOR GPU-contention stall | safe-subset MONITOR cap + transient routing | `893b156` |
| demo-run #2: `F[+]`→3 locked test → EXECUTE spiral → escalation | Phase 0 (E) `re_refine`-from-stall + (F) content-stall watchdog | `7315948` |
| pin carry-through (2 `grammar` pins, only last injected) | Phase 1 (A) core multi-pin injection | `b4ae16e` |

**Not** cleared / deliberately deferred past this run: CRITIQUE (C) *prevents*
the bad `F[+]` test; Phase 0 (E) only *recovers* from it. Running lsystem now is
the end-to-end test of that recovery path — the `qual-corpus-lsystem-2` fixture
cannot replay it (all its bead-2 executions are `infra_failure` or incomplete,
pre-`893b156`). If E fires correctly on the `F[+]` class the run validates it; if
not, that is the highest-value next finding.

### Phase 3 — integration (the only place full-run confounding is allowed)

7. ~~**Build the offline integration fixture.**~~ **Not feasible** — the
   `qual-corpus-lsystem-2` fixture's bead-2 executions are all infra-masked or
   incomplete (pre-`893b156`), so there is no clean `stalled` execution to
   replay. The live run in step 8 is the integration test instead.

8. **One full from-scratch lsystem run** (settling run — see the pre-lsystem gate
   above). Expect 1–2 settling findings from cross-change interactions. This is
   planned, not a failure — do not call the first post-chain run "done." Watch:
   does bead 2 (`grammar`) still get a doc-contradicting locked test, and if so
   does ADJUDICATE route the resulting stall to `re_refine` (Phase 0 E) rather
   than burning retries to escalation? Does (F) cap the spiral near ~10 min?

9. **Settle.** Fix the settling findings → second full run → if clean,
   `save-fixture` + promote to a fleet baseline. Until then, **fractalviz**
   (8/8 clean, 2026-09-06) remains the fleet-qualification baseline —
   lsystem is the precision-work driver, not the baseline, until Phase 3
   closes.

---

## Sequencing rationale

- **Phase 0 before everything** so the recovery net exists while the imperfect
  upstream work is landing. If Phase 1/2 still let a bad test through
  occasionally, `re_refine` catches it instead of an escalation — this is the
  single biggest lever on "settling cleanup" risk.
- **DECOMPOSE before design-doc guidance** (3 before 4, corrected 2026-09-07):
  the mature driver docs are already well-pinned, so the "measure DECOMPOSE
  against improved doc output" argument doesn't apply; the corpus-verified live
  lever is pins not surviving DECOMPOSE. B's residual scope (two-reading rules,
  construction-form pins, bead caps, fresh-project onboarding) does not gate A.
- **CRITIQUE before the re_refine loop** (5 before 6): the threshold tuning in 6
  assumes CRITIQUE's post-5 signal quality.
- **Full runs only in Phase 3.** Everything in Phase 0–2 is unit-testable +
  corpus-replayable. The grammar bead's three independent real-world defects are
  a ready-made offline acceptance harness — full-run confounding is confined to
  steps 8–9, with an explicit settling budget.

## Related

- `docs/decompose-precision-plan.md` — (A), items 1–2 done, item 3 is Phase 1
  step 4 here.
- `docs/critique-redesign.md` — (C) mechanical pre-pass, ABANDONED; kept as the
  record of what does not work. Item 2 (noise-tolerant re_refine) is Phase 2
  step 6 here.
- `docs/refine-adjudicate-bakeoff-plan.md` — the verb replay harness used for
  Phase 1–2 validation.
- `docs/execute-monitor-contention.md` — the `893b156` infra fix that unblocked
  the demo-run's attempt-1 stall; deferred MONITOR redesign is out of scope for
  this plan.
- Memory: [[project_decompose_precision]], [[project_critique_repetition_detection]],
  [[project_designdoc_precision_guidance]], [[project_test_action_noise_tolerance]],
  [[project_refine_tests_loop_cost]], [[project_execute_progress_detection]] (PR
  #10/#11, finding F), [[handoff_lsystem_demo_run]], [[handoff_lsystem_baseline_1]].
