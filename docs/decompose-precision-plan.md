# DECOMPOSE precision — build plan

**Status:** proposed, not started (2026-09-04). Payoff target: the
REFINE_TESTS-loop cost (`docs/refine-adjudicate-bakeoff-plan.md`,
`memory/project_refine_tests_loop_cost`).
**Gate that produced this:** `memory/project_refine_precision_phase0` — corpus.db
retrospective, ~83% of REFINE_TESTS-loop waste is design-doc-precision-shaped;
on current-generation data (projects 47 + 48) the live lever is bucket **(b)**,
"value determined in the doc, dropped by DECOMPOSE/WRITE."

**REVISED ORDER (2026-09-04, after baseline-9 — see "Live validation" below):**

1. **REFINE_TESTS_WRITE turn-budget fix — DONE 2026-09-04.** baseline-9's b316
   escalation (report: `~/Documents/ratchet-projects/qual-corpus-baseline-9-REPORT.md`,
   Finding A) was a framework bug, not purely a weak model: `maxTurns`
   (`internal/verbs/refine_tests.go`) scaled off *accepted writes*, so a WRITE
   dispatch that front-loads `run_go_snippet` verification (the snippet mandate
   read literally) and writes nothing got `totalReq = 0 → maxTurns = 4`, burned
   all 4 turns verifying, and escalated having written zero functions. Fixed
   with two changes: (a) `writeBeforeVerifyError` rejects any `run_go_snippet`
   call before at least one `write_function` has been accepted; (b)
   `computeMaxTurns` adds a floor of `pendingFuncCount + refinementWriteAttempts`
   turns for beads needing more than one function, independent of accepted-write
   progress. The pre-existing stuck-compile early-bail (`turn >=
   refinementWriteAttempts`) is untouched — it still fires at the original
   threshold regardless of the raised ceiling. Unit tests: `TestPendingFuncCount`,
   `TestComputeMaxTurns`, `TestWriteBeforeVerifyError`.
   **Validated by replaying the exact b316-c1 dispatch through `cmd/qualify-model`
   post-fix**: no escalation — gemma wrote both `TestCompile` and
   `TestDisassemble` (turns 1–2), then spent turns 3–14 on per-subtest
   verification (the pre-existing, already-characterized slow-but-not-broken
   cost), `validation=valid`, `fidelity match=true`. See "Live validation" below
   for what the replay's grading result additionally surfaced (a second,
   independent occurrence of the pointer-vs-value defect class from b314).
2. **REFINE_TESTS_WRITE model bakeoff — DONE 2026-09-04.** `qual-corpus-p48`,
   6 beads (b313–b318), n=1 each, mutation-style grading (`good/` fixtures for
   all 6, seeded mutants for b313 only). Result: **muse-glimmer 6/6 genuinely
   correct, gemma4:31b 3/6, qwen3.6:35b-a3b 3/6.** Both incumbents failed the
   two hardest beads (b317 vm, b318 handlers-templates) — gemma via a loud
   30-min timeout, qwen3.6 via a **silent** failure (`validation=valid`,
   generic fallback summary, zero tool calls across all 4 turns, nothing
   written — worse than a timeout for anyone trusting the top-line result).
   **muse-glimmer ADOPTED** for `VerbRefineTestsWrite`
   (`internal/db/assignments.go`); qwen3.6 deliberately stays on JUDGE rather
   than also taking WRITE (would collapse WRITE+JUDGE self-review — worse than
   WRITE+EXECUTE sharing muse, which CRITIQUE+JUDGE still independently review
   between). Two qualify-model reference fixtures fixed along the way
   (`b314/good/parser.go` pointer/value inconsistency,
   `b318/good/handlers.go` untrimmed `Output`) — both independent
   corroborations of recurring defect classes, folded into "Live validation"
   below. Full table: `docs/fleet-qualification.md`. Branch
   `write-turn-budget-fix`, uncommitted to main.
3. **This project (DECOMPOSE precision)** — third. **Re-measure Phase 0's
   expected payoff against a fresh completed baseline on muse-WRITE before
   locking scope** — some of what Phase 0 attributed to doc/DECOMPOSE
   precision gaps was convolved with gemma's specific WRITE pathology and may
   not recur under muse.
4. **Design-doc precision guidance** (`memory/project_designdoc_precision_guidance`)
   — fourth.

## Goal

Every value the design doc *determines* for a test assertion survives into the
bead spec that owns it — mechanically guaranteed, or the run escalates. Not
"hope DECOMPOSE's prose keeps it."

## What already exists (verified against `main` HEAD + qual-corpus-p48)

| mechanism | file | what it does |
|---|---|---|
| `extractDecompositionNotesPins` / `injectDecompositionNotesPin` | `internal/verbs/mechanical_checks.go:889` | mechanically appends the verbatim `- **Pin … \`<bead>\` bead**:` bullet to the named bead's `full_text`; dedupes reflowed copies across DECOMPOSE + RECONCILE rounds. Landed `fb82ad7` (2026-08-29). |
| `loadDesignDocExcerptForBead` | `internal/verbs/designdoc_sections.go` | hands WRITE / CRITIQUE / JUDGE / ADJUDICATE a bead-relevant design-doc excerpt (Test Scenarios + Decomposition Notes unfiltered, Behavioral Spec filtered to the bead's symbols); 30 KB budget; header marks it "AUTHORITATIVE over the bead spec". |
| `checkdesigndoc pins` | `cmd/checkdesigndoc` | counts worked examples vs Pin bullets, warns on mismatch. |
| source-side consistency gate | `internal/verbs/mechanical_checks.go:980+` | exit-criteria ↔ prose ↔ output_files; DECOMPOSE/RECONCILE reject-and-retry on violation. |

The old "CRITIQUE/JUDGE never see the design doc" and "`design_doc_prescriptive.md`
never written" gaps are **closed**; those memories are stale.

## Gaps that remain (each corpus-verified on projects 47/48)

1. **Silent no-op on bead-name mismatch.** `injectDecompositionNotesPin` keys on
   `strings.ToLower(bead.Title)`. If the doc pins to `compiler` and DECOMPOSE (or
   a RECONCILE rename) titles the bead `compiler-disassembler`, the pin vanishes
   with **zero signal** — no test, no warning, not in AUDIT's input. Project 47's
   `compiler` bead has **no pin appendix** despite an exact-match pin bullet and
   title; project 48's does. Non-deterministic across runs — almost certainly a
   RECONCILE-regenerated-bead path that skips re-injection (reconcile only
   re-injects `UpdatedBead`).

2. **Multi-bead pins inject into the first named bead only.**
   `decompositionPinBeadRe.FindStringSubmatch` returns the first `` `x` bead ``
   match. The doc's "Pin the template package to the `handlers-templates` **and
   `cli`** beads" → project 48's `cli` bead got **no pin appendix** (confirmed).
   It compiled only because DECOMPOSE prose happened to keep `html/template`.

3. **Only `- **Pin` bullets are carried.** Exact literals that live only in
   Domain-Specific Test Scenarios / Behavioral Spec prose — the
   `"runtime error: division by zero"` string (47/309, 48/321), output
   trailing-newline trim, HTML-escaped output bytes — are **not injected**. They
   ride on DECOMPOSE prose fidelity + the excerpt reaching WRITE. This is where
   the Phase-0 bucket-(b) current-generation failures actually sit.

4. **The excerpt header tells WRITE to stay loose** — "where an output is
   described loosely ('returns an error', 'Err is non-empty'), assert that
   property, not an incidental string that nothing pins." Correct in general, but
   it undercuts a scenario that *did* pin an exact value once DECOMPOSE's
   bead-spec prose has blurred it to "returns an error."

5. **No DECOMPOSE escalation.** A pin that can't be placed, or a doc internally
   inconsistent about a value, routes to AUDIT → RECONCILE, which shuffle wording
   but can't resolve it (`memory/project_decompose_escalation`).

## Live validation — baseline-9 (2026-09-04)

exprvm-web-baseline-9 (post-PR-#1 fleet, `qual-corpus-baseline-9/`) ran the same
`exprvm-web.md` and **stalled at a b316 (compiler) REFINE_TESTS_WRITE escalation**
(`required test functions still missing after all attempts`, missing
`[TestCompile TestDisassemble]`). Findings, from the captured verb-io:

- **Gap #1 confirmed and worse than "rename".** DECOMPOSE *split*
  `handlers-templates` → `handlers` + `templates` and renamed `cli` → `main`.
  The doc's two pins targeting `handlers-templates` / `cli` (the
  Bytecode-by-error-type rule and the `html/template` package rule) match **no
  bead** and were silently dropped — `has_pin = 0` on beads 318 / 319 / 320.
  Those beads had not run when the project stalled; the dropped pins are the
  exact 47/309 & 48/321 error-string / Bytecode failure class sitting downstream
  with zero protection. **Phase 1 must handle the split case, not just rename.**

- **b316 escalation is NOT a DECOMPOSE-precision failure — it's a WRITE
  turn-budget bug** (baseline-9 report Finding A). The scaffold stubs were
  complete and correct and the compiler bead carried the disassembly pin
  verbatim (`has_pin = 1`). What happened: DECOMPOSE gave b316 *two* required
  functions (`TestCompile` + `TestDisassemble`, vs p48's one `TestCompiler`);
  gemma planned 13 subtests, then — reading the `run_go_snippet` "once for EVERY
  t.Run subtest" mandate literally — front-loaded all verification; `maxTurns`
  counts only turns with accepted writes, so with zero writes it stayed at 4;
  gemma burned all 4 on snippets (turn 1 = 591 s / 16 K-token planning
  monologue, `content=""`) and escalated having written nothing. p48's b316
  *succeeded* under the same model. So: a fixable framework bug, amplified by
  gemma's verbosity and DECOMPOSE's 1→2 function variance — **step 1 of the
  revised order fixes it before the bakeoff**, and it argues gemma is
  unreliable-at-WRITE rather than narrowly bad.

- **b314 (parser) `re_refine` was a pointer-vs-value gap.** ADJUDICATE: "the
  parser implementation correctly returns pointer types (`*ExprStmt`,
  `*AssignStmt`), but the test asserts against value types (`ExprStmt`)." The
  doc shows AST nodes as value literals (`ExprStmt{Value: …}`); the scaffold
  uses value receivers, so both `T` and `*T` satisfy `Stmt`; the impl picked
  pointers, the test picked values. **Nothing pins the construction form.** New
  plan item — see Phase 2.
  (b314 also took a first-attempt 900s timeout: the parser bead is fat — grammar
  + 1-token lookahead + overflow + lexer-error propagation in one bead. That is
  a bead-sizing / EXECUTE concern, not tracked here.)

- **Second, independent confirmation (2026-09-04, validating the turn-budget
  fix below): the compiler bead hit the identical defect class.** Replaying
  b316-c1 through `cmd/qualify-model` post-fix, gemma wrote both required
  functions cleanly (no escalation — see below) but the result **fails
  mutation grading 100%**: `compiles=yes coverage=2/2 kills_mutant=n/a
  passes_good=no`. Every subtest constructs the top-level AST node as a
  pointer — `&ExprStmt{Value: &NumberExpr{Value: 42}}`,
  `&AssignStmt{Name:"x", Value:&NumberExpr{Value:10}}`, etc. — but the
  reference `Compile`'s statement-level switch matches only value types
  (`case AssignStmt: / PrintStmt: / ExprStmt:`, no pointer cases), so every
  call falls through to `default: return fmt.Errorf("unknown statement
  type")` and every subtest fails via `t.Fatalf`. Same root cause as b314
  (pointer-vs-value construction form never pinned), independently occurring
  in a different bead within the same project run — this is not a one-off.
  (Side note, not part of the fix: the reference impl itself is inconsistent —
  its *expression*-level switch handles both `*T` and `T` for every node type,
  but its *statement*-level switch handles only `T`. Worth a fixture cleanup
  when this reference is reused, but doesn't change the finding: the design
  doc's own worked examples are value-typed throughout, so gemma's all-pointer
  construction is the actual deviation.)

- **Fleet-switch validation is only partial.** qwen3.6 JUDGE (no schema) +
  ADJUDICATE behaved coherently across b313–b315 (correct timeout→double-budget
  and pointer/value→re_refine diagnoses), but only 3 beads' worth. The full
  hard-case validation of the PR #1 fleet switch still needs a completed run —
  fold it into the post-WRITE-bakeoff baseline.

Nothing more is needed from baseline-9 for the WRITE bakeoff (corpus is p48) or
for Phase 1's gate (DECOMPOSE + RECONCILE dispatch already captured, 004–006).

## Phased plan

### Phase 1 — pin-consumption assertion (FIRST GATE)

Smallest, highest-confidence, zero false-positive risk.

- After the pin sweep in DECOMPOSE *and* after every RECONCILE round, assert
  **every** pin from `extractDecompositionNotesPins` was consumed by ≥1 bead's
  `full_text` (check for the canonical appendix, not the raw literal).
- Parse *all* `` `x` bead `` / `` `x` and `y` beads `` targets in a bullet;
  inject into each.
- Re-run injection over **all** beads after RECONCILE, not just `UpdatedBead`.
- On an unconsumed pin: first try to **place it structurally** —
  - exact bead-title match (current behavior);
  - **split detection**: the pin's target name is a substring/superstring of, or
    hyphen-joined from, ≥2 actual bead titles (`handlers-templates` →
    `handlers` + `templates`) → inject into every matching bead;
  - single close match (`cli` → `main`, `compiler` → `compiler-disassembler`) →
    inject there but flag it for AUDIT.
  If none of those place it: reject-and-retry DECOMPOSE once, naming the specific
  pin and listing the actual bead titles. Still unplaced → **escalate to user**
  (delivers `memory/project_decompose_escalation` for this one concrete
  mechanical trigger).
- Feed the pin→bead consumption map into AUDIT_DECOMPOSITION's input.

**Gate:** replay DECOMPOSE for projects 48 + baseline-9 (+ 47) through the check.
Must (a) show all 4 exprvm-web pins consumed by every named bead on a p48-style
run, (b) catch a synthetic bead-rename as unplaced, (c) reproduce and now handle
**baseline-9's real case** — `handlers-templates` split into `handlers` +
`templates`, `cli` → `main` — placing (or escalating) all 4 pins where today
`has_pin = 0` on beads 318/319/320, (d) reproduce and catch the project-47
`compiler` miss. baseline-9 gives this phase a genuine positive test case; do not
skip it.

### Phase 2 — promote scenario/behavioral literals to enforced pins

Attacks gap #3.

- Extend `checkdesigndoc` (new check, or extend `pins`): flag every exact quoted
  string literal and `X = <number>` equation inside Domain-Specific Test
  Scenarios / Behavioral Spec that is **not** covered by a `- **Pin` bullet
  naming a bead. Conservative: quoted-literal / explicit-equation only, no prose
  inference — this is the line the abandoned CRITIQUE spec-cross-check crossed
  (`docs/critique-redesign.md`), stay well back of it.
- **Construction-form pins for domain types crossing a bead boundary** (new,
  from baseline-9 b314). When a type defined in one bead is constructed or
  type-asserted in another bead's tests — AST nodes, any struct passed by
  interface — the doc must pin whether it is used as a value or a pointer
  (`ExprStmt{…}` vs `&ExprStmt{…}` in a `[]Stmt`). This is `draft-design-doc`
  rule 2 ("copy-vs-reference semantics for a returned pointer/slice/map")
  applied to construction form. `checkdesigndoc`: flag a Cross-Bead Contract
  type that appears in a bead spec's test guidance with no value/pointer
  statement. Also a hand-off to [[design-doc precision guidance]].
- Makes "author a pin for it" a checkable authoring step — the **hand-off point
  to [[design-doc precision guidance]]**: this check names *which* literals need
  a pin; that guidance covers *how* to word it.

**Gate:** on `docs/fixture-design-docs/exprvm-web.md` the check flags (a) the
`"runtime error: division by zero"` string and the trailing-newline trim as
unpinned values, and (b) the AST node types as having no value-vs-pointer
statement; ≤ small N false positives on the completed-project docs
(`tictactoe-v1`, `connect-four-v1`).

### Phase 3 — reconcile the excerpt header with pinned values

Attacks gap #4.

- When a bead's excerpt contains a `- **Pin` appendix with an exact literal, the
  header for that bead should state the pinned literal is **mandatory to
  assert**, not "stay loose." Two-tier: loose-by-default, but "any value carried
  in a Decomposition Notes pin below is required exactly."

**Gate:** replay WRITE for the `handlers` bead (baseline-9 b318, the 48/321
error-string class) with the revised header + a Phase-1-placed / Phase-2 pin for
the error string; WRITE produces the exact-string assertion on the first cycle.
Compare the cycle outcome vs the project-48 baseline. Use whichever WRITE model
the bakeoff settled on.

### Phase 4 — fold in the post-bakeoff baseline, hand off to design-doc guidance

- Re-run a completed exprvm-web baseline on the settled WRITE model + PR #1
  fleet. This both validates the DECOMPOSE changes end to end (do the placed
  pins hold? does the b316 escalation clear?) and completes the fleet-switch
  hard-case validation baseline-9 only partly covered.
- Fold its corpus into `memory/project_refine_precision_phase0`.
- Hand off to [[design-doc precision guidance]].

## Risk

- Phase 1: near-zero correctness risk — assertion + one retry + escalation on a
  mechanical signal; worst case a spurious escalation on a malformed pin bullet
  (loud, not silent).
- Phase 2: report-only, like the rest of `checkdesigndoc` — no pipeline path.
- Phase 3: the only live-prompt change; gated on a WRITE replay before it lands.
- Surface: `mechanical_checks.go`, `decompose_spec.go`, `reconcile_decomposition.go`,
  `audit_decomposition.go` (input only), `designdoc_sections.go` (Phase 3),
  `cmd/checkdesigndoc` (Phase 2). No change to the DECOMPOSE model prompt's
  structural judgment.

## Calibration

baseline-9's b316 escalation is the reminder: this project makes what reaches
WRITE more precise; it does **not** make WRITE more capable or fix WRITE's
turn-budget mechanics. A run can still escalate because the WRITE loop mismanages
its turns (Finding A) or the model cannot write the test from an adequate spec.
Expected effect of this project is fewer *wasted cycles* (revise + `re_refine`)
on beads WRITE can already handle — not a lower *escalation* rate on its own.
The escalation-rate levers are steps 1–2 of the revised order (turn-budget fix,
then the model bakeoff), which is why they run first.

## Validation assets

qual-corpus-p48 + `qual-corpus-baseline-9/` for DECOMPOSE/RECONCILE replay
(baseline-9's DECOMPOSE+RECONCILE dispatch, verb-io 004–006, is the positive
test case for Phase 1's split handling); `cmd/checkdesigndoc` against every repo
design doc for Phase 2 tuning; a single WRITE replay through `cmd/qualify-model`
for Phase 3. No live project run in the build conversation
(`memory/feedback_separate_framework_from_project_runs`).
