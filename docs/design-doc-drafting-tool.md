# Design doc drafting + rigorizing tool — design

Status: **combined design pass, ready for implementation review.** Supersedes the
2026-08-29 planning sketch (git 2338750). Written 2026-08-29. Nothing implemented yet —
this document is the design; implementation follows sign-off, phase by phase.

## Motivation

Today there is exactly one path to a ratchet design doc: the user gives a vague
description in conversation, and Claude interactively expands it into a full
prescriptive doc — worked examples verified with throwaway scripts, exact type
signatures, Decomposition Notes pins, ambiguities resolved via `AskUserQuestion` as
they come up. This works, but it's bespoke to whichever conversation happens to write
it, and nothing about the process is repeatable or independently checked.

The risk this creates is not hypothetical — it already landed twice:

- **Tasklist bead 9** — `REFINE_TESTS` churned 4+ cycles because a Claude-authored
  design doc line was ambiguous between shallow- and deep-copy semantics, in the same
  session whose own commit message described applying worked-example discipline
  elsewhere. See `docs/design_doc_ambiguity_checklist.md`'s origin incident.
- **connect-four-v5 bead 71** (2026-08-28) — a full-board draw grid was added to
  Domain-Specific Test Scenarios without its matching Decomposition Notes pin; the
  escalation cost a full bead's worth of `REFINE_TESTS` cycles. This is what
  `cmd/checkdesigndoc` was built to catch.
- **exprvm-web-v2** (this session) — a Claude-authored doc whose Decomposition Notes
  pins were real but not in the format later tooling expected. Correct content, never
  independently checked against the convention that came after it.

The user's ask: hand over something closer to prose — more than a one-line
description, less than a fully-interrogated doc — and get a rigorous draft back,
checked by both judgment and mechanical passes before it's trusted for `new-project`.

**The risk to design against.** Loosening the input from "interrogated live,
ambiguities forced to the surface" to "prose I hand over and don't get asked about"
removes the one mechanism currently catching ambiguities at authoring time. A drafting
tool that goes straight from prose to a doc trusted for `new-project` with no
independent check doesn't remove that risk — it relocates it. So the drafting step and
the checking step are separate, and the checking step is not optional.

## Scope and boundaries

**In scope:** two Claude Code skills and one extension to an existing Go CLI, all on
the *human* side of the `SURVEY_SPEC` boundary.

**Explicitly not in scope:** no changes to the ratchet Go pipeline. `SURVEY_SPEC` stays
exactly as it is — 100% dependent on a finished, human-approved design doc. This is not
a new pipeline verb. It is also distinct from `proposed-ideas.md` §1a's
"behavioral-divergence escalation" (that catches drift at *execution* time; this is
about doc quality before a project starts).

## Pre-work: reconcile the template with the guide

`docs/design_doc_template.md` has drifted. It omits the **Architecture** and
**Domain-Specific Test Scenarios** sections that `docs/design_doc_guide.md` describes
in full and that every real fixture doc (`docs/fixture-design-docs/*.md`) actually
uses — and that `cmd/checkdesigndoc` already depends on by name
(`## Domain-Specific Test Scenarios`).

Before Phase 1, bring `design_doc_template.md` in line with the guide's seven-section
structure:

1. Overview (+ **Domain parameters** bullet list)
2. Architecture (file map, package layout, module name)
3. Data Types and Function Signatures (+ Export signatures block)
4. Behavioral Specification
5. Domain-Specific Test Scenarios (*conditional* — geometry/coordinate domains only)
6. Cross-Bead Contracts (*conditional* — omit if no cross-bead interfaces)
7. Decomposition Notes (*conditional* — omit if DECOMPOSE's heuristics suffice)

This is a small mechanical edit, but it must land first: the drafting skill produces a
doc *against the template*, and the checker parses *by section heading*. They need one
authoritative structure.

---

## Phase 1 — `draft-design-doc` skill

**Input.** Prose describing the project — a file path (`$1`) or, if none given, ask the
user to paste it or point at a file. Accepts anything from a few paragraphs to a rough
spec; the point is it has *not* been interrogated.

**What it does.** Produces a draft design doc following the seven-section template,
section by section — the same authoring work Claude already does in conversation, made
explicit and repeatable.

**Non-negotiable disciplines** (written into the skill as hard requirements, not left
to judgment — per `feedback_confident_wrong_needs_enforcement`, "verify when uncertain"
does not help a model that is confidently wrong and never feels uncertain):

1. **Every worked example is script-verified before it enters the doc.** Every Δrow/Δcol,
   every arithmetic result, every hash value, every format string, every ID-sequence
   value — produced by running a real throwaway script (Go via a scratch `main`, or
   shell) *in this session*, not derived from memory. The skill's instructions require
   the verification to actually run and require a one-line inline citation of what was
   run (e.g. `<!-- verified: go run scratch/div.go → -7/2 == -3 -->`). A worked example
   with no verification citation is a skill-compliance failure, not a style nit.

2. **Gaps are surfaced, never silently filled.** Wherever the source prose does not pin
   something the template requires — an exact type signature, a worked-example number, a
   copy/reference semantics call, enum literal values, `package main`, a coordinate-system
   mapping — the draft gets an entry in a trailing **`## Open Questions`** section
   quoting the under-specified spot and stating the concrete alternatives. The skill
   never invents a plausible value and moves on.

3. **Decomposition Notes pins are generated for every load-bearing literal.** For any
   worked scenario whose *specific values* (not just its governing rule) must survive
   into a bead spec, the skill adds the matching `**Pin ...**` bullet to Decomposition
   Notes naming the exact bead and values — the mechanism `design_doc_guide.md` already
   prescribes and that keeps getting missed by hand. After drafting, the skill runs
   `go run ./cmd/checkdesigndoc --doc <draft>` itself and resolves any count mismatch
   before handing off.

**Open Questions handling — resolved decision (hybrid, superset of the plan's
recommendation):**

- The draft *always* contains the written `## Open Questions` section. That is the
  durable artifact and it works with no live turn.
- *Additionally*, when run interactively, after producing the draft the skill batches
  the open questions into `AskUserQuestion` calls, folds the answers back into the
  relevant sections, and removes the resolved entries. Anything the user skips or
  defers stays in the section verbatim.

This is strictly a superset of both options in the original plan — the offline artifact
always exists; the interactive pass is an accelerator, not a coupling.

**Drafting is done inline, not by a subagent.** This is Claude doing authoring work it
already does well. Independence matters for *checking*, not drafting — and the check
(Phase 3) is a separate step with its own fresh-subagent pass.

**Output.** The draft doc written to a file (alongside the prose, or a path the user
gives), with its `## Open Questions` section, plus a printed pointer: run
`check-design-doc <path>` next. If run interactively and the user agrees, chain
straight into it.

---

## Phase 2 — mechanical ambiguity scan (extend `cmd/checkdesigndoc`) — BUILT

**Decision: extend `cmd/checkdesigndoc` rather than add a second binary.** Same category
of tool — parse one design-doc markdown file by section heading, print a report a human
reads, never a pass/fail oracle. The existing pin-vs-scenario check became one block of
a longer report.

**As built** (`cmd/checkdesigndoc/main.go`, `main_test.go`):

- `--checks=pins,ambiguity` flag, default `all`. `ambiguity` runs first so its report
  always prints even when the pin check skips.
- Missing pin sections are a non-fatal `SKIPPED:` line now, not `os.Exit(1)`. The only
  non-zero exit is file-not-found (`--doc` unreadable) and `--doc` absent.
- `stripFences` blanks every ```` ``` ````-fenced block (preserving line count so
  reported line numbers stay right) before classes 1/2/6/17 scan — directory trees and
  Go signature blocks were the dominant noise source. Class 7 still scans the raw doc so
  a `package main` inside an example block counts as a declaration.
- **Section scoping:** classes 1, 2, 6 scan only **Architecture**, **Behavioral
  Specification**, **Domain-Specific Test Scenarios** / **Required Test Scenarios**, and
  **Cross-Bead Contracts**. Classes 7 and 17 scan the whole doc.

**Tuning outcome** (ran against all 9 fixture docs + all 8 design-docs + a git-restored
pre-fix `chess.md`):

- **Class 1** narrowed hard — bare `above`/`below`/`left of`/`right of` were dropped
  (in these docs they are almost always cross-references, not geometry). Kept:
  `diagonal(ly)`, `orthogonal(ly)`, `toward(s)`, `forward(s)`, `backward(s)`,
  `clockwise`, `counter-clockwise`, `up/down the board`, `row above/below`, `column to
  the left/right`. Cleared by a nearby `Δrow/col/rank/file`, `dr/dc = n`, a `(±n, ±n)`
  or `{±n, ±n}` pair, or an `a1 -> b2` algebraic delta in the same paragraph.
- **Class 2** is keyword-only. The "arithmetic operator between identifiers" heuristic
  was removed entirely — it flagged every Go pointer type (`g *Game`). Triggers now:
  `sum of`, `product of`, `divided by`, `modulo`, `mod`, `FNV`, `hash of`,
  `the formula`, `floor(`, `ceil(`, `number of … is`. Cleared by a computed number
  nearby (`= n`, `e.g. n`, `→ n`, `partition n`, `n bytes/cells/slots/…`).
- **Class 6** trigger set trimmed to `use the <T> (type|constant|value|enum) from/in`,
  `as defined above/below/elsewhere`, `see … for the value/definition`, `the standard
  value`. Same-line clearing (backtick literal, `= <value>`, `Foo(-1)`, a bare number).
- **Class 7** unchanged in intent: `main.go` mentioned + no literal `package main`
  anywhere → one finding.
- **Class 17** adjective must sit within two words of the noun
  (`standard\s+(\w+\s+){0,2}(rules?|movement|…)`), else "standard starting position
  with White to move" false-positives on `move`. Dropped `regular`/`ordinary` from the
  adjective list (checkers uses "regular piece"/"regular move" for the non-king piece
  type). Every hit reported verbatim; the report block states plainly that judgment
  review will not catch this class.

**Regression anchors** (locked in `TestFixtureDocs_regressionAnchors` + focused unit
tests): fixed `chess.md` → **0** flags; git-restored pre-fix `chess.md` (commit
`a17074c`) → **2** (class 1 pawn-capture geometry, class 17 "standard movement rules").
Per-fixture flag ceilings: checkers 4, kafka-sim 4, exprvm/exprvm-web/goban 1, the rest
0 — every nonzero fixture is one whose ambiguity class originated there.

---

## Phase 3 — `check-design-doc` skill (orchestrator)

One skill, run on any design doc (drafted by Phase 1 or hand-written), that executes
the full sign-off sequence and presents a single consolidated verdict.

**Args.** `$1` — path to the doc (required; ask if missing).

**Sequence:**

1. **Confirm the doc exists** and parses (has the required section headings).
2. **Mechanical pass** — run `go run ./cmd/checkdesigndoc --doc <path> --checks=all`,
   capture the full report (pin consistency + all five ambiguity classes).
3. **Judgment pass** — invoke the existing `design-doc-ambiguity-check` skill. Its
   subagent forms its **own independent** findings first, from only the doc + the
   checklist — the mechanical report is **not** fed in at this stage (preserves the
   independence that is the whole point of the fresh subagent).
4. **Reconcile** — after the subagent returns, the orchestrator diffs the mechanical
   hits against the subagent's findings. For any mechanically-flagged site the subagent
   did **not** address, it is carried into the final report as *unresolved, mechanical
   only* — never silently dropped, never silently accepted. Class 17 hits are always
   carried as unresolved regardless of what the subagent said (per the checklist:
   judgment cannot clear this class).
5. **Consolidated report** — one table, every row: `{class, quoted line(s), source
   (mechanical / judgment / both), the divergent readings, suggested rewrite,
   status: NEEDS DECISION}`.
6. **Sign-off** — present the table to the user. Each item is resolved explicitly:
   accept the rewrite, provide a different answer, or affirmatively waive it (with the
   reason recorded inline in the doc as an HTML comment). Resolved edits are folded into
   the doc.
7. **Clearance** — only after every row is resolved does the skill print
   "cleared for new-project" and, if the user confirms, copy the doc into the project
   folder. Nothing about this step is automatic.

**This mirrors DECOMPOSE → AUDIT → RECONCILE** — adversarial review applied one layer
earlier, to the doc itself, before the pipeline ever sees it.

**Why two skills and not one.** `draft-design-doc` and `check-design-doc` are separate
so the check runs on hand-written docs too (the common case for the fixture docs and
for anything predating this tooling). `draft-design-doc` chains into `check-design-doc`
at the end when run interactively; that is convenience wiring, not a merge.

---

## Implementation sequence

| Step | Work | Depends on |
|------|------|-----------|
| A | Reconcile `design_doc_template.md` with the guide's 7 sections | — |
| B | Extend `cmd/checkdesigndoc`: `--checks` flag, 5 ambiguity classes, validate against all 17 real docs, tune | A |
| C | `draft-design-doc` skill | A, B (runs `checkdesigndoc` itself) |
| D | `check-design-doc` orchestrator skill | B, C |
| E | Update `docs/design_doc_ambiguity_checklist.md` ("mechanical checker: built"), `.claude/skills/design-doc-ambiguity-check.md` (drop "no mechanical checker exists"), `docs/proposed-ideas.md` §2 | B, C, D |

Per `feedback_propose_before_apply`, steps B–D each get a quick design confirmation
before code lands, but this document covers the shape of all three. Steps are small
enough to land as separate commits on this branch.

## Open decisions for the user

1. **Merge vs. separate binary** for the mechanical scan — this design assumes *merge
   into `cmd/checkdesigndoc`*. (Recommended.)
2. **One orchestrator vs. two skills** — this design assumes *two skills, check
   auto-chained from draft*. (Recommended.)
3. **Fix the drifted template now** (step A) — this design assumes *yes*. (Recommended.)
4. **After sign-off on this doc**, implement B→C→D→E in this thread, showing each step
   before moving to the next — or split across threads. (Recommended: one thread, this
   is framework work with no live project running.)
