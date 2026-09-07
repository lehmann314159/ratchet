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

### Phase 0 — safety nets (small, localized, unit-testable, no run required)

Do these first. They make the pipeline fail *toward* `re_refine` instead of
escalation while the larger upstream work is in flight — which is precisely what
shrinks the Phase 3 settling risk. Both are self-contained and independently
unit-testable.

1. **`stalledExecutionNote` gains a `re_refine` branch (E).**
   - Change: in `internal/verbs/adjudicate_next_execution.go`, add a fourth
     option to the note — when the disk shows nothing written *and* the bead went
     through REFINE_TESTS (`beadHasRefinements`), instruct ADJUDICATE that an
     unsatisfiable locked assertion is a possibility and `re_refine` is on the
     table, with `re_refine_guidance` naming the suspect assertion. Keep the
     "second consecutive stall escalates" backstop.
   - Interaction to get right: `escalateOnRepeatedStall` currently only runs in
     the `execute_as_is` / `execute_revised` decision branches
     (`adjudicate_next_execution.go:1540,1560`). Adding a `re_refine` path out of
     a stall must not let a bead loop forever — cap via the existing
     `refinementCycleCap` check (line 1713).
   - Validation: unit test (`stalled_execution_test.go` companion); offline
     replay of the current grammar stall (Phase 3 fixture) — ADJUDICATE should
     now be *able* to choose `re_refine` given the doc + test.

2. **Mid-stream content-stall watchdog (F).**
   - Finish PR #11's in-pocket item: a watchdog that trips when a single turn
     streams for more than N minutes with `content_chars` not advancing (pure
     thinking, no `write_file`), faster than the 20m `execEmptyAttemptCeiling`
     and independent of turn boundaries.
   - Validation: the offline stall fixture from the PR #10/#11 work; unit test on
     the watchdog trigger condition.

**Exit Phase 0 when:** both land on `main` with unit tests; the grammar stall
fixture (built in Phase 3 step 7, or a throwaway early cut) shows ADJUDICATE can
reach `re_refine`.

### Phase 1 — upstream precision, source-first

Design doc is the source of truth; DECOMPOSE consumes it. Fix in that order so
DECOMPOSE precision is tested against improved doc output, not stale output.

3. **Design-doc precision guidance (B).** `project_designdoc_precision_guidance`.
   - Sharpen `draft-design-doc` (the prompt) and `cmd/checkdesigndoc` (the
     linter) for: one unambiguous reading per rule; explicit anti-examples;
     construction-form pins (the exact phrasing that must survive to the bead);
     bead surface-area caps.
   - Validation: run `cmd/checkdesigndoc` against the existing
     `docs/design-docs/` corpus and the lsystem doc. Acceptance: it flags (a) the
     bracket-module count ambiguity that produced `F[+]`→3, (b) the "before the
     first `(`" head phrasing that DECOMPOSE later compressed away. No new false
     positives on the design docs that produced clean runs (fractalviz).

4. **DECOMPOSE precision (A).** `decompose-precision-plan.md` item 3, bundling
   `project_decompose_worked_example_compression` +
   `project_decompose_notes_not_enforced` + `project_decompose_escalation`.
   - Known concrete gaps to close: bead-split / multi-bead / multi-pin drop pins
     (multi-pin beads currently inject only the **last** pin verbatim —
     fractalviz-1); prose literals and construction-form not injected;
     compression re-describes load-bearing rule clauses into their opposite
     (the grammar `head` case).
   - **Re-measure Phase 0's expected payoff** (`project_refine_precision_phase0`)
     against a fresh muse-WRITE baseline before locking scope — some of what the
     retrospective attributed to doc/DECOMPOSE gaps was convolved with gemma's
     WRITE pathology and may not recur under muse.
   - Validation: replay against corpus fixtures — grammar c1 (does the "before
     the first `(`" clause now survive verbatim?), fractalviz multi-pin beads
     (do all pins inject?); re-run the Phase 0 corpus bucket measurement.

**Exit Phase 1 when:** `cmd/checkdesigndoc` catches both grammar ambiguities;
DECOMPOSE replay shows load-bearing clauses and all pins surviving; Phase 0
bucket re-measure shows the expected reduction.

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

### Phase 3 — integration (the only place full-run confounding is allowed)

7. **Build the offline integration fixture.** `save-fixture` from
   `qual-corpus-lsystem-2` at the bead-2 boundary (bead 1 succeeded, bead 2
   pending). Replay bead 2 through each affected verb with the full Phase 0–2
   stack landed. Acceptance — all three known grammar defects resolved offline:
   - the "before the first `(`" head clause survives DECOMPOSE verbatim;
   - CRITIQUE (or `cmd/checkdesigndoc` upstream) flags the `F[+]`→3 test;
   - if a bad locked test still slips through, ADJUDICATE routes it to
     `re_refine`, not escalation.

8. **One full from-scratch lsystem run.** Expect 1–2 settling findings from
   cross-change interactions that no offline replay surfaced. This is planned,
   not a failure — do not call the first post-chain run "done."

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
- **Design doc before DECOMPOSE** (3 before 4): DECOMPOSE precision must be
  measured against improved doc output.
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
