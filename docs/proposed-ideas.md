# Proposed ideas — consolidated for synthesis pass

Pulled together 2026-07-19 from memory, for a pass on what should actually drive
ratchet's forward direction. This is raw material, not a recommendation or a
priority order — each item below is either "proposed, not designed," "designed,
not built," or "built, not deployed/committed." Status is stated plainly per item.
Cross-references note real dependencies and unresolved tensions between items;
none of those are resolved here.

---

## 1. Escalate smarter instead of guessing — three related, not-yet-reconciled ideas

All three are variations on the same shape: stop a pipeline stage from silently
guessing past a gap, and surface it instead. They are grouped here because they
overlap enough that building all three independently risks redundant or
conflicting mechanisms — worth deciding as a set, not one at a time.

### 1a. Behavioral-divergence escalation (proposed 2026-07-19, newest)

Don't ask fleet models (gemma3/qwen3/mistral, 24B-32B) to self-report
uncertainty — they lack the introspective access to distinguish "reading this
from the spec" from "extrapolating from training-data priors" (the checkers
`BlockedPieces` incident: confident, not hesitant, when inventing an untested,
chess-knowledge-derived test). Instead, trigger escalation on **observable
divergence**:
- Pre-execution, mechanical: the ambiguity checklist's mechanically-detectable
  classes (1, 2, 6, 7, 17 — see §2 below) as a pre-flight scan before SURVEY_SPEC.
- Execution-time, behavioral: a model's output diverging from what the spec
  states (invented untested content), or independent regenerations disagreeing,
  or ADJUDICATE's existing recurring-byte-identical-failure detector firing —
  used as a trigger to stop retrying and ask the user a specific question,
  instead of spending another execute/adjudicate cycle.

Status: proposed only, not designed.

### 1b. DECOMPOSE low-confidence escalation (older, same family)

If DECOMPOSE can't determine exact signatures or finds the design doc
ambiguous, emit a structured low-confidence signal; the pipeline routes to a
new `ESCALATE_DECOMPOSITION` state instead of proceeding to
AUDIT_DECOMPOSITION → RECONCILE_DECOMPOSITION (which can reshuffle wording but
can't resolve genuine ambiguity). Async, not blocking — user resolves via the
UI, pipeline re-runs DECOMPOSE. A precursor is already live: the DECOMPOSE
prompt instructs the model to state ambiguity explicitly in `full_text` rather
than guess.

Status: proposed, one precursor step already shipped; the actual
`ESCALATE_DECOMPOSITION` state is not built.

### 1c. Post-execution model triage (older, different mechanism for a related problem)

Feed a post-execution report to one fleet model (qwen3:32b first — strongest
reasoner in the fleet) and have it triage an escalation: tool-calling failure
vs. monitor interference vs. genuine implementation bug vs. wrong test
expectation vs. bead-spec ambiguity. Motivated by bead 82 (checkers
move-generation): a human read the report and spotted "wrong test expectation"
in seconds; the framework had already blamed the model for 9 attempts.

**Tension with 1a, unresolved:** this idea *does* ask a model to self-assess,
which 1a is explicitly skeptical of. They might layer rather than conflict —
1a's divergence signal decides *when* to stop retrying, 1c's triage prompt (or
the user) decides *what* the cause is once escalated — but that's a real open
question, not settled.

Status: proposed, nothing built or tested against real escalation reports yet.

---

## 2. Design-doc ambiguity checklist + mechanical checker

Already active and partially built — tracked in full at
`docs/design_doc_ambiguity_checklist.md` (17 classes as of 2026-07-19) and
`.claude/skills/design-doc-ambiguity-check.md` (the judgment-pass skill,
dispatches one fresh subagent per review). Included here only because it's the
direct dependency for 1a's "pre-execution, mechanical" tier, and because its
own unbuilt piece is itself a proposed idea:

**The mechanical checker (classes 1, 2, 6, 7, 17) is now built** —
`cmd/checkdesigndoc`'s `ambiguity` check (`--checks=ambiguity`), an over-flagging
report tuned against all 17 in-repo design docs plus a git-restored pre-fix `chess.md`
as the regression anchor. It catches these 5 classes before SURVEY_SPEC ever runs, on
any design doc, without spending a review-subagent call — but it is a pre-filter, not a
substitute for the judgment pass (class 17 excepted, where judgment review provably
cannot help).

Status: **all three phases built (2026-08-29)** — see `docs/design-doc-drafting-tool.md`.
Phase 2 is `cmd/checkdesigndoc`'s `ambiguity` check (above). Phase 1 is
`.claude/skills/draft-design-doc.md` (prose → draft doc, worked examples script-verified,
gaps to an Open Questions section). Phase 3 is `.claude/skills/check-design-doc.md`
(mechanical scan + independent judgment pass + pin consistency + item-by-item sign-off
before a doc is cleared for `new-project`). Not yet run on a real prose→draft→check
cycle — first use is the validation step.

**Separately, `docs/stress-test-roadmap.md` Phase D** (non-game
complexity-axis stress tests: expression-language interpreter, worker-pool
scheduler, mini KV store over TCP, tiered billing calculator) is the
*application* of this checklist's discipline going forward, now that games are
deprioritized. Not duplicated here — see that doc directly.

---

## 3. Extensions roadmap — capabilities beyond from-scratch builds

Full detail in `docs/extensions-roadmap.md` (2026-07-18) — five concrete cases
for extending ratchet beyond fresh design-doc builds:

1. **Modify an existing project** (e.g., chess text UI → graphical click-to-move)
2. **Fix a reported defect from a symptom** (no design doc — e.g., "castling
   queenside gives the wrong result")
3. **Reuse a project as a library/import**
4. **Reuse a project as an external executable dependency**
5. **Sequential composition via a published interface** (backend → frontend)

**Verified, not assumed:** Case 5 already works today, unmodified — Cross-Bead
Contract `producer`/`consumer` fields are free-text prose with zero structural
validation.

**Two shared primitives** cover the rest:
- **Unowned files** — a project-scoped list of paths no bead owns. Needed by
  Case 1 (files from a prior succeeded bead) and Case 3a/copy-in (files copied
  from elsewhere). Currently enforced ad hoc in three separate places
  (`certify_manifest.go`, `scaffold_go.go`'s `wipeGoProject`,
  `analyze_execution.go`'s `checkUndeclaredFiles`) that can drift out of sync.
- **Resurvey-from-real-code + delta decomposition** — needed directly by Case
  1, and as a prerequisite for Case 4's "give the dependency a CLI" sub-problem.
  Bigger lift, not designed in detail yet.

**Case 2 (bug-fix) is structurally different** — no behavioral spec to expand,
only a symptom. Reframed as a new diagnostic verb chain (something like
DIAGNOSE_DEFECT → LOCALIZE) that terminates by handing off exactly where
DECOMPOSE_SPEC would, rather than a forked app — deliberately not forked,
because the orchestrator/model-fleet/trace-logging/UI/DB substrate is real
accumulated value a fork would have to re-earn. **Not yet verified** — the plan
is to prototype the diagnostic front end against the chess castling-queenside
bug (root cause already known) before committing further.

Agreed sequencing: unowned-files primitive → resurvey + delta decomposition →
bug-fix pipeline shape (evaluated empirically).

Status: planning only, nothing implemented. Two open questions explicitly
deferred: copy-in vs. true Go module dependency for Case 3, and where the
unowned-files list actually lives (schema question).

---

## 4. ratchet-http shared library

Planned since 2026-07-10: a small opinionated library (`testkit.NewServer`,
form-parsing helpers, template-init pattern, prescribed test-file import
lists) that web-app projects import, collapsing repeated boilerplate failures
(global state init, httptest setup, import lists) to one right answer.
Explicitly scoped to be designed from **at least two concrete real projects**,
not one, to avoid overfitting.

**Needs re-scoping now.** The original plan gated this on 3 comparable
HTTP-handler implementations across checkers/chess/goban — which is moot now
that games are deprioritized (see `docs/stress-test-roadmap.md`'s 2026-07-19
status update). Tasklist-shaped (CRUD, non-game) HTTP projects could serve the
same comparative-data-points purpose if this is revived, but that re-scoping
hasn't happened yet.

Status: planning only, paused, needs a scoping decision before it's actionable
again.

---

## 5. Known bugs / technical debt (not proposals, but relevant to "where to invest")

Not new capability ideas, but standing, reproducible issues that any synthesis
of "what should ratchet work on next" should weigh against new-feature work:

- **Orchestrator: escalation blocks the queue for other projects.**
  `activeProject()` always picks the oldest `status='active'` project with no
  fallback; an escalated job leaves `projects.status` stuck at `'active'`
  forever (there's no `'escalated'` value in the CHECK constraint), so the
  same stuck project gets re-selected every tick indefinitely until a human
  runs `full-stop-project`. **Correction on severity** (user pushback,
  2026-07-17): this is a hygiene issue given the orchestrator is deliberately
  single-project-at-a-time, not a critical bug — don't inflate it. Directly
  relevant to 1a/1b/1c above: any of those escalation-smarter ideas will
  interact with this queue behavior. Not fixed.
- **UI job elapsed-time includes queue-pending wait, not just model-call
  duration.** `queryRecentJobs` computes elapsed as
  `updated_at - created_at`, which includes time a job sat `pending` (most
  visible right after `resume-project`). Real work time is in
  `handoff_attempts`. Fix identified (use dispatch time, not enqueue time) but
  not implemented.
- **`remove-project` FK-constraint failures under the live running process**
  (not reproducible via direct SQL) — best theory is contention on the
  single shared DB connection (`SetMaxOpenConns(1)`) between the orchestrator's
  tick loop and the UI's HTTP handlers, not confirmed. Worked around via direct
  SQL for the one cleanup that hit it; root cause still open.
- **`NewUnbounded()`'s zero per-turn timeout** means one hung/zero-token model
  turn can consume an entire bead's execution budget, with no faster
  detect-and-retry path than the full budget expiring. Deliberate tradeoff
  (genuinely slow generations shouldn't be killed by an arbitrary timeout), but
  the observed failure signature (zero tokens, not slow generation) suggests a
  short per-turn watchdog distinct from the overall budget could recover
  faster. Flagged, not designed, not blocking anything currently.

---

## 6. Independent producer in the produce→critique→decide cycles (proposed 2026-09-02)

**Status: execute-cycle version SHIPPED + VALIDATED (commit `9814a08`;
EXECUTE_BEAD→muse-glimmer, ADJUDICATE stays gemma — exprvm-web-baseline-7 /
project 47 reached COMPLETE 9/9, the first baseline in the lineage to finish);
refine-tests version proposed, gated on validating muse test-writing; decompose
version open.**

Every three-verb cycle currently has the *decider* run on the *producer's*
model:

| cycle        | produce (gemma4:31b) | critique (qwen3:32b) | decide (gemma4:31b)   |
|--------------|----------------------|----------------------|-----------------------|
| decompose    | DECOMPOSE_SPEC       | AUDIT_DECOMPOSITION   | RECONCILE_DECOMPOSITION |
| refine-tests | REFINE_TESTS_WRITE    | REFINE_TESTS_CRITIQUE | REFINE_TESTS_JUDGE     |
| execute      | EXECUTE_BEAD         | ANALYZE_EXECUTION     | ADJUDICATE_NEXT_EXECUTION |

The decider takes the critic's output as input but makes the final call with
the producer model's own judgment — so a model is effectively the arbiter of
whether to accept criticism of work its own weights produced. Models have a
blind spot for their own reasoning errors, and the failure mode is documented
repeatedly: the exprvm-web-v1 bead-33 incident (ADJUDICATE invented and
progressively re-asserted a fabricated code-level defect, overriding the
"default to re_refine" prompt guidance every round —
`internal/verbs/adjudicate_next_execution.go` struct comment); RECONCILE
self-certification on a brittle tie-break (`project_reconcile_self_certification`);
`feedback_dont_assume_bad_verdict_propagates`.

**The fix is to move the PRODUCER to a different model, not the decider.**
Keep gemma4:31b as the decider (JUDGE/ADJUDICATE/RECONCILE are schema-mode
JSON decision verbs and gemma is a known-adequate quantity there); put a
different model in the WRITE/EXECUTE seat. ADJUDICATE is also *more* useful
judging a different model's output — it can't wave a problem away with "I'd
have done it this way" when it didn't produce the code.

- **execute cycle**: EXECUTE_BEAD → muse-glimmer, ANALYZE → qwen3,
  ADJUDICATE → gemma. This falls out **for free** of the EXECUTE_BEAD model
  swap decided in `model-qualification-harness.md` follow-up / the
  exprvm-web-baseline-6 handoff — no extra work, the cycle just stops being
  self-grading.
- **refine-tests cycle**: REFINE_TESTS_WRITE → muse, CRITIQUE → qwen3,
  JUDGE → gemma. Same shape, gated on validating muse's *test*-writing
  (currently untested — staying on gemma for the next baseline).
- **decompose cycle**: can't move the producer — DECOMPOSE_SPEC emits
  schema-mode `beads[]` JSON, which is muse's known weak spot (2026-08-31
  fleet session: "muse DECOMPOSE-bare-json degenerate"). Leave as-is or find
  a third schema-capable producer.

**Do NOT put muse in the decider seat** — the malformed-schema history +
2026-09-02 ATEM tool-parse error make it below-average at exactly the
structured output an arbiter must produce.

Not a separate initiative for the execute cycle (it's a side effect of the
model swap). For refine-tests it needs muse test-writing validated first via
the bakeoff harness.

---

## 7. REFINE_TESTS is now the pipeline bottleneck — optimize the loop + generalize the bakeoff harness (proposed 2026-09-02)

**Status: measured on a full run; not designed. Gated on nothing except doing
the EXECUTE_BEAD→muse work first (done). Do this in a framework-only session,
not interleaved with a live project run.**

With EXECUTE_BEAD on muse-glimmer, EXECUTE is no longer the slow phase.
Measured over the full exprvm-web-baseline-7 run (project 47, 9.4h wall-clock,
9 beads, 11 executions):

| phase | total | share |
|-------|-------|-------|
| REFINE_TESTS (write + critique + judge) | 349 min | **62%** |
| EXECUTE_BEAD | 88 min | 16% |
| ADJUDICATE_NEXT_EXECUTION | 85 min | 15% |
| everything else | ~35 min | 6% |

Worst single line: **REFINE_TESTS_CRITIQUE, ~15 min/run** (qwen3:32b running the
mandatory `run_go_snippet` verification loop). REFINE_TESTS_WRITE ~12 min
(gemma4:31b). One re_refine happened for ~half the beads.

Second finding, same run: **ADJUDICATE-on-gemma spiral.** One verdict
(bead 309 rev 1) took **29 min**, including a `done_reason=length` turn that
produced neither content nor a tool call — the loop tolerated the dead turn and
advanced. Same "one giant thinking stream hits the generation cap" pathology
that motivated moving EXECUTE off gemma. n=1 (other ~10 ADJUDICATE runs in the
project were 4–13 min); quantify frequency before building. Verdict *quality*
was fine — it caught two real gaps the locked tests missed.

**The work, as one initiative:**

- **Generalize `internal/execution/bakeoff.go`** (`ratchet exec-bakeoff`,
  commit `6fae86a`) into the verb-qualification harness sketched in
  `docs/model-qualification-harness.md`: replay recorded verb inputs against a
  candidate model N×, score speed / stability / output-validity.
- **Run role bakeoffs** for REFINE_TESTS_WRITE / CRITIQUE / JUDGE and
  ADJUDICATE_NEXT_EXECUTION. Candidate pools (from the EXECUTE bakeoff
  evidence): WRITE — muse-glimmer, gemma4:31b, qwen3.6:35b-a3b; CRITIQUE (must
  differ from WRITE) — qwen3:32b (incumbent = latency target), qwen3.6:35b-a3b
  (3B active — the potential big speed win), mistral-small3.2:24b; JUDGE /
  ADJUDICATE — gemma4:31b, qwen3.6:35b-a3b, mistral-small3.2:24b (not muse —
  schema-mode JSON is its weak spot).
- **Grading is harder than exec-bakeoff's locked oracle.** WRITE: mutation-
  style — generated tests must pass a known-good impl and fail a seeded-bad one;
  plus compile-against-real-package and exit-criteria coverage. CRITIQUE: a
  labelled good/bad test-file set (baseline runs produce it) → catch rate on
  bad, false-positive rate on good, latency. JUDGE / ADJUDICATE: replay recorded
  inputs → agreement with a reference verdict + schema validity + latency.
- **"Fail-fast for ADJUDICATE"** — note it's the *opposite* lever from EXECUTE
  fail-fast (EXECUTE adds wall-clock; ADJUDICATE needs to *cut* deliberation).
  Options: treat 1–2 `done_reason=length` dead turns as a hard stop → bounded
  fallback verdict or escalate; add a wall-clock cap on the verb (none today);
  or the parked mid-stream circuit breaker (§5's `NewUnbounded` watchdog).
- **Also fold in**: whether every bead needs the full write→critique→judge
  round-trip (skip on a clean first pass?), tightening the re_refine trigger,
  schema-mode for the CRITIQUE turn, and giving REFINE_TESTS_JUDGE a rationale
  field (its flat schema makes revise verdicts illegible — bead 309 cycles 1–2).
- Connects to §6: if muse's test-writing qualifies here, REFINE_TESTS_WRITE→muse
  gives the refine-tests cycle its independent-producer property too.

---

## Cross-cutting observations (not a synthesis — just what's visibly connected)

- §1's three ideas and §2's mechanical checker are one coherent initiative if
  pursued together: mechanical pre-flight catches what's catchable before
  execution; the escalation-trigger question is about everything that survives
  to execution time anyway.
- §3 (extensions) and §4 (ratchet-http) both stall on "need a second/third real
  example before generalizing" — a recurring methodological stance in this
  project, not a coincidence.
- §4 is now blocked on a decision that only exists because of the game
  deprioritization decided earlier this session — it didn't have an open
  question before today.
- None of §5's bugs are proposals for new capability, but 1a/1b/1c (smarter
  escalation) will make the orchestrator's escalation-blocks-queue behavior
  more visible, not less, since the whole point is escalating more precisely
  and probably more often for ambiguity (vs. blind retries) — worth having an
  opinion on §5's queue bug before shipping more escalation paths, even if the
  bug itself stays low-priority.