# Decomposition framework — build plan

**Status:** all four items landed (2026-09-08). 1 (`checkdesigndoc` lint), 2
(DECOMPOSE bead-merge gate), 3 (`draft-design-doc` guidance), 4 (`goFixBeadSpec`
test-file reconciliation). Not yet deployed; item 2 needs the deploy check below.
**Gate that produced this:** `memory/project_execute_model_bakeoff` decision #3
("then focused framework work on decomposition") + `memory/handoff_lsystem_run_6`
(doc-side decomposition of the `grammar` monolith into three `expr`-sized
sub-beads — all clean, first end-to-end lsystem run).
**Payoff target:** oversized beads become visible to the design-doc author
*before* a run pays for them (an `expr`-sized bead is ~100% one-shot for muse; a
150-line integration bead spirals to escalation).
**Not in scope:** the ADJUDICATE Q4/quant bakeoff, decomposing the `handlers`
bead of the lsystem doc (project-driving), the `v0.4` tag.

---

## The responsibility split (the design principle)

Bead sizing is a **judgment call that requires knowing the EXECUTE model's
capability ceiling** — "will muse spiral on this?" The EXECUTE bakeoff
(`project_execute_model_bakeoff`: 0 passes / 31 runs, generous budget made the
best coder *worse*) showed fleet models can't tell their own convergence from
thrashing; gemma4:31b (DECOMPOSE) already fails the easier job of reconciling a
test-file name (run-5, 3/3 identical rejections). Asking DECOMPOSE to estimate
bead complexity *and* know muse's ceiling is the same class of ask that just
failed.

That knowledge lives with the design-doc author (Claude, whole-system context,
one slow careful pass, human in the loop). So:

- **Claude owns bead sizing.** The `## Decomposition Notes` "Bead dependency
  order" list *is* the decomposition. DECOMPOSE's prompt already treats it as an
  "authoritative override" (`prompts.go:111`).
- **DECOMPOSE owns faithful mechanical transcription** of that list into bead
  specs + verbatim pin carry-through. It already does this well (11 beads 1:1 in
  baseline-1, 13 in run-6). Its one discretionary lever — *merging* two doc beads
  into one — is exactly what caused baseline-14/15 (handlers+templates merged →
  EXECUTE spiral / 3 REFINE cycles; same doc, same fleet, pure run-to-run
  variance). This plan removes that discretion.
- **The lint is a safety net for the author**, not an oracle. Report-only,
  consistent with every existing `checkdesigndoc` check, cleared by a one-line
  note. This stays inside the `handoff_lsystem_baseline_1` constraint ("a
  surface-area lint may still be a minor nice-to-have, not the fix") — it is
  *not* an auto-preventer and DECOMPOSE never acts on it.

---

## The metric: size × integration (two-signal AND)

Raw line count fails — `expr` (~230 lines, one-shot every run) is bigger than the
`grammar` monolith (~150 lines, escalated 5 runs). The signal is **size AND
integration fan-in together**: a bead spirals only when it is both a pile of
distinct responsibilities *and* wired into several prior beads.

Per bead, computed from doc structure already in the `draft-design-doc` template:

- **size signal** — count of the bead's owned **functions/methods**, read from
  the `## Data Types and Function Signatures` Go block (`func Name(` /
  `func (recv) Name(` under the `// ---- file.go ----` marker for a file the
  bead owns). **high = ≥ 4 owned functions.** (Lowered from 5 to 4 after
  calibration: exprvm-web's merged `handlers+templates` bead has exactly 4
  declared functions and did spiral; `fractal-core` has 4 but zero integration,
  so the AND still passes it.)
- **integration signal** —
  (a) fan-in: distinct prior bead numbers referenced in the bullet ("Depends on
      beads 8, 9, 10", "Calls beads 2, 3, 4, 5", "(bead 1)");
  (b) contract participation: distinct `### <producer> → <consumer>` blocks in
      `## Cross-Bead Contracts` that name this bead on either side.
  **high = fan-in ≥ 3 OR contract participation ≥ 3.**
- **FLAG = size high AND integration high**, unless the bullet carries a
  `sizing rationale:` / `sizing note:` phrase (escape hatch).

**Hidden-complexity NOTE (advisory, not a flag).** A bead that declares ≤ 1
function but is integration-high and has a ≥ 30-line Behavioral Specification
subsection is *under-specified*, not necessarily oversized — the func count
can't see its real surface. The pre-split lsystem `grammar` bead is exactly
this: `## Data Types` listed only `ParseSystem`, so `funcCount = 1` and the FLAG
rule is blind, but its 45-line spec describes three parsers. The NOTE asks the
author to list the unexported helper signatures in `## Data Types`; once they're
there (as run-6's split doc has them) the FLAG rule catches the bead normally.
This is the hand-off into item 3.

The AND is load-bearing: `grammar-modules` (2 functions, 4 contracts) is highly
connected but small → PASS; the `grammar` monolith (6 functions, same fan-in) →
FLAG. The size axis alone separates the run-6 split from the thing it replaced. A
big self-contained bead (`expr`, `fractal-core`: fan-in 0) passes regardless of
size.

### Calibration (implemented + verified 2026-09-08 — `cmd/checkdesigndoc/beadsize.go`, `TestBeadSize_CorpusGate`)

Actual check output. `fns` = functions declared in `## Data Types`; the doc's
declared API, which for a monolith that hides helpers is an undercount (see the
NOTE row).

| doc / bead | fns | fan-in | contracts | verdict | actual outcome |
|---|---|---|---|---|---|
| fractalviz — 7 beads (fractal-core, render, params, save, templates, main, integration) | ≤4 | — | — | PASS | all clean (8/8) |
| **fractalviz — handlers** | 5 | 4 | 5 | **FLAG** | ran clean (1 cycle); accepted borderline — see below |
| lsystem run-6 — 12 beads (expr, grammar-modules/rules/system, rewrite, turtle, render, studio, save, templates, main, integration) | ≤3 | — | — | PASS | all clean (13/13) |
| **lsystem run-6 — handlers** | 4 | 3 | 3 | **FLAG** | muse stalled 2× (decomposition candidate) |
| lsystem **pre-split** — grammar | 1 | 1 | 3 | **NOTE** | escalated 5 runs — see NOTE row below |
| lsystem pre-split — handlers | 4 | 3 | 3 | **FLAG** | (same monolith-era bead) |
| exprvm-web — lexer, parser, env, compiler, vm, cli | ≤2 | — | — | PASS | baseline-13/16 clean |
| **exprvm-web — handlers+templates** | 5 | 4 | 2 | **FLAG** | EXECUTE spiral + 3 REFINE cycles (baseline-14/15) |
| exprvm — all 5 beads | ≤2 | — | — | PASS | completed clean |
| connect-four-v1, tictactoe-v1, tasklist, kafka-sim, haiku-generator, checkers, fractal | — | — | — | SKIP | no numbered bead list (prose / table) |

**The one debatable cell — fractalviz `handlers` (FLAG, ran clean):** it is the
largest, most-wired bead in that doc (5 handlers, 4 deps, 5 contracts). Any
threshold that un-flags it also un-flags lsystem/exprvm-web `handlers` (nearly
identical shape), which *did* spiral. Given report-only output + a one-sentence
escape hatch, and the asymmetric cost (over-flag = one sentence; missed
oversized bead = an escalated run), flagging all three is correct. The
fractalviz report also notes CRITIQUE was "blind on handlers — only 3 of 5
handlers tested," so it is also the bead where review quality degraded.

**The pre-split `grammar` monolith surfaces as a NOTE, not a FLAG** — the doc
declared only `ParseSystem` (funcCount 1), so the size axis is blind. The NOTE
("under-specified: 45-line behavioral subsection, 1 declared function — list the
helper signatures") is the correct honest output for a doc written that way, and
it hands straight to item 3: once the helpers are in `## Data Types` (as run-6's
split doc has them), the bead flags normally. `studio` (fan-in 4, 1 function,
14-line spec) stays PASS — a thin pipeline-composition bead is genuinely fine,
and the behavioral-line-count guard distinguishes it.

**7 SKIPs, 0 false positives on clean beads, every known-bad bead caught.**

### Escape hatch (non-brittle)

A flag is cleared by a **sizing rationale** — a `## Decomposition Notes` bullet
(or sub-bullet) for the bead whose text contains a recognised marker
(`sizing rationale:` / `sizing note:`). Mirrors the construction-form check's
"cleared by a `&T{` example" pattern. A false flag costs the author one sentence.

---

## Work items

### 1. `checkdesigndoc --checks=bead-size` — DONE 2026-09-08

`cmd/checkdesigndoc/beadsize.go` (+ `beadsize_test.go`); wired into `main.go`'s
`--checks` (and `all`). Report-only, house style.

What it parses (all from the established `draft-design-doc` structure):
- **beads** — the numbered list under `## Decomposition Notes` ("1. **name** —
  …" / "1. **name**: …"). No numbered list → `SKIPPED` (7 of 12 current docs —
  prose or a table; item 3 makes the list a requirement).
- **funcCount** — `func Name(` / `func (recv) Name(` declarations under the
  `// ---- file.go ----` marker for each file the bead owns, read from the
  `## Data Types and Function Signatures` Go block. File→bead via "Owns
  `x.go`", else title match (`vm`→`vm.go`, `handlers+templates`→both,
  `cli`/`main`→`main.go`), else the bullet's leading backticked-symbol list.
- **fanIn** — every distinct bead number the bullet references ("beads 8, 9,
  10", "(bead 1)", "bead 2's output type"), minus self.
- **contractCount** — distinct `### <producers> → <consumers>` blocks in
  `## Cross-Bead Contracts` naming the bead (title split on `+`; endpoint names
  normalised, trailing-`s` stripped so `handler`≡`handlers`).
- **behavioralLines** — longest `### ` subsection in `## Behavioral
  Specification` whose heading names the bead or one of its symbols (for the
  hidden-complexity NOTE only).

Verdicts: `FLAG` (size high ∧ integration high, no rationale), `PASS (sizing
rationale noted)`, `NOTE` (hidden complexity), `PASS`.

**Acceptance gate — MET.** `TestBeadSize_CorpusGate` locks it: FLAG on
fractalviz/lsystem `handlers` and exprvm-web `handlers+templates`; NOTE on the
reconstructed pre-split `grammar` (fixture
`~/Documents/ratchet-projects/qual-corpus-lsystem-1/lsystem/design_doc.md`);
PASS on all 30-odd beads that reached COMPLETE (expr, the 3 grammar sub-beads,
fractal-core, render, params, save, templates, studio, main, integration,
lexer/parser/env/compiler/vm/cli); SKIP on the 7 prose/table docs. Zero new
false positives.

**Known limitation** (documented in the file header, handed to item 3): a bead
that hides its complexity behind one fat exported function — the pre-split
`grammar` — gets a NOTE, not a FLAG, because `## Data Types` only listed
`ParseSystem`. Item 3 requires significant helper signatures in `## Data Types`,
after which such beads FLAG normally.

### 2. Remove DECOMPOSE's bead-merge discretion — DONE 2026-09-08

`internal/verbs/mechanical_checks.go` — `beadStructureViolations(designDoc,
proposed)` + `parseDecompositionNotesBeadList`. Called from
`DecomposeSpec.Commit` alongside `beadConsistencyViolations`; violations route
through the existing `commitRedecompose` reject-retry (cap 3).

- **Merge** (gated): one proposed bead covers ≥ 2 listed beads → violation
  naming the merged bead and both. "Covers" = normalised-title match **or** the
  proposed bead owns the `.go` file the doc bullet assigns to the listed bead.
- **Drop** (gated): a listed *non-integration* bead that no proposed bead
  covers → violation. Listed beads whose title contains "integration" are
  exempt (prose-specified, DECOMPOSE has latitude per the prompt).
- **Benign rename not flagged**: baseline-9's `cli` → `main` — the proposed
  `main` bead owns `main.go`, which the doc's `cli` bullet names via `(main.go)`,
  so the file-match covers it.
- **Extra beads not flagged**: an unrequested split, or the integration beads
  the doc's prose calls for, add beads without violating — the split-vs-merge
  asymmetry (over-split is cheap, merged escalates) means splitting is left to
  the author + the bead-size lint; DECOMPOSE-side extras are already surfaced by
  `unconsumedPinTargets` + AUDIT_DECOMPOSITION.
- **Skipped** when the doc has no numbered bead list (`< 2` parsed entries) —
  older prose/table docs are unaffected.
- Prompt nudge in `prompts.go`: the Decomposition Notes "authoritative override"
  paragraph now states that a numbered list fixes the bead *count and titles*,
  merge/drop is rejected mechanically, an extra integration bead is fine.

Tests: `internal/verbs/bead_structure_test.go` — parser (em-dash / colon /
`(main.go)` forms), merge, drop, rename, integration exemption, extra-bead, and
`TestBeadStructureViolations_RealDocsClean` (every real doc's natural 1:1
decomposition → zero violations, the false-positive baseline).

**Pre-existing unrelated test breakage noted:** `TestExtractDecompositionNotesPins_RealDocs`
and `TestUnconsumedPinTargets_CorpusGate` (`mechanical_checks_test.go`) fail on
`main` right now — they expect a `grammar` bead in the lsystem doc, which Mike's
*uncommitted* run-6 edits split into `grammar-modules`/`-rules`/`-system`. Not
touched by this item; a run-6 follow-up (update the fixtures when the doc edits
land).

### 3. `draft-design-doc` guidance (skill + guide) — DONE 2026-09-08

`docs/design_doc_guide.md`:
- **Decomposition Notes section reframed.** The old "start without this section /
  do not pre-write the full bead table" advice was stale — every recent
  end-to-end doc has a numbered list. Now: skip only for a small single-file
  library; for a multi-file project (or any parser/pipeline/handlers bead)
  write the **numbered bead-dependency list** (name, owned symbols, deps, owned
  file — structure only, not spec prose). Stated authoritative (item 2:
  DECOMPOSE emits one bead per entry, cannot merge/drop).
- **New `### Bead sizing` subsection.** The size × integration heuristic in
  author terms ("< ~4 functions, OR < 3 deps and < 3 contracts"); the five-run
  lsystem `grammar` cost; the run-6 three-way split as the worked example
  ("its own `###` subsection + its own contract entries is what makes a split
  real"); the `sizing rationale:` escape hatch; what a NOTE means.
- **Two web-I/O two-reading rules** added to "Specific patterns that need
  explicit guidance" — `r.ParseForm()` decodes `+` as space; `html/template`
  context escaping. Plus 4 Common-Mistakes rows (oversized bead, form `+`,
  html-escape assertion) and 3 Web-application checklist items.
- "How the pipeline uses your design doc" notes the numbered list is enforced
  mechanically.

`docs/design_doc_template.md`: Decomposition Notes section now shows the
numbered-list skeleton with the sizing rule inline.

`.claude/skills/draft-design-doc.md`: hard rules 4 (numbered bead list +
sizing) and 5 (web-I/O rules); the Finish step resolves `bead-size` FLAG/NOTE
before hand-off (like a pin mismatch), not deferred to `check-design-doc`.
`.claude/skills/check-design-doc.md` already updated in item 1.

### 4. `goFixBeadSpec` test-file-name reconciliation (run-5 abort cause) — DONE 2026-09-08

`internal/verbs/mechanical_checks.go`. `goFixBeadSpec` previously only *added* a
missing `_test.go`; a bead that already owned one fell straight through and
`checkBeadCriteriaConsistency` I7 rejected it with nothing the repair pass could
do (run-5: DECOMPOSE paired `grammar_test.go` with `grammar.go` while the exit
criterion's grep guard named `grammar_modules_test.go` → 3 byte-identical
redecompose rounds → abort).

New reconciliation step (runs after the grep-fix passes, before the
grep-guard/derive passes), driven by `oneUnownedGuardTestFile(bead)` — the
single `*_test.go` the bead's grep guards reference but do not own:
- **owns exactly one behavioral `*_test.go`** → rename it to the guarded name
  and rewrite any exit criterion referencing the old name (per the run-6
  handoff: "reconcile the test-file name *to the exit criterion*" — the criteria
  are the internally-consistent, multiply-referenced signal; the file pairing is
  a single reflex).
- **owns zero behavioral `*_test.go`** (but ≥1 impl `.go`) → add the guarded
  name (in the impl file's dir), so `deriveTestFileName` below doesn't
  synthesize a *different* name.
- **≥2 owned behavioral test files, or ≥2 distinct unowned guarded names** →
  ambiguous, not touched — left for the now-**prescriptive** I7 feedback
  ("the grep guard and the owned `*_test.go` must use the same name: put X in
  output_files, or rename the guard… Convention: the test file for `foo.go` is
  `foo_test.go`").

Tests: 3 new `TestGoFixBeadSpec` subtests (rename, add, ambiguous→prescriptive
I7). `TestDecomposeSpecCommitRejectsInconsistentCriteria` updated — its fixture
is now the *ambiguous* shape (2 owned test files) so it still exercises the
redecompose path; the single-file case it used to test is now auto-repaired.

**Note:** the run-6 handoff's *content* fix (fold the 4 types into
`grammar_modules.go`, no separate `grammar.go`) is already in the uncommitted
lsystem doc. This item fixes the *framework* gap so the next hand-split doesn't
need that workaround.

---

## Out of scope / deferred

- **DECOMPOSE deciding to split** an oversized section — ruled out by the
  bakeoff; the lint + author cover it.
- **`project_decompose_precision` pin-propagation on split** — `b4ae16e` already
  carries doc pins into named beads; the residual gap is only DECOMPOSE
  *deciding* to split, which is out of scope here. Re-check during item 2 that a
  pre-split doc's pins land correctly (run-6 says they do).
- **A REFINE-stage "bead too big" detector** (baseline-15: N consecutive cycles,
  CRITIQUE flags a different subtest each time) — a downstream backstop, separate
  effort; the lint attacks the same problem upstream where it is cheaper.
- **`progressTracker` convergence-vs-thrashing signal** — dormant while
  EXECUTE = muse (muse stalls or converges, doesn't thrash);
  `project_execute_progress_detection`.

## Order

All four done. Remaining before this can be called finished: deploy check for 2
(below), and a from-scratch run to confirm the guidance actually changes what
the author does and 2 doesn't false-reject a real decomposition.

**Deploy note for 2:** it is the only live-DECOMPOSE change. `TestBeadStructureViolations_RealDocsClean`
is the false-positive baseline (every real doc's natural 1:1 decomposition →
zero violations). Before deploying, rebuild the live binary
(`go build -o ratchet ./cmd/ratchet/`) and, ideally, replay a completed
baseline's stored DECOMPOSE output through `beadStructureViolations` to confirm
no reject on a decomposition that actually shipped.

## Validation philosophy

Unit tests + corpus replay, not full runs (`precision-chain-plan` philosophy).
The `grammar` monolith is a ready-made acceptance case. One from-scratch run only
after all four items land, to confirm the lint's output actually changes what the
author does and item 2 doesn't false-reject a real decomposition.
