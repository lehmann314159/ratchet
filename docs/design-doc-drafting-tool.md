# Design doc drafting tool — plan

Status: **planning only, nothing implemented.** Written 2026-08-29 for later execution —
per [[feedback_separate_framework_from_project_runs]], don't start building this while a
live project (e.g. exprvm-web-v2) is running in the same thread.

## Motivation

Today there is exactly one path to a ratchet design doc: the user gives a vague
description in conversation, and Claude interactively expands it into a full
prescriptive doc — worked examples verified with throwaway scripts, exact type
signatures, Decomposition Notes pins, ambiguities resolved via `AskUserQuestion` as
they come up. This works, but it's bespoke to whichever conversation happens to write
it, and nothing about the process is repeatable or independently checked.

The risk this creates is not hypothetical — it already landed once. Tasklist bead 9's
`REFINE_TESTS` churned 4+ cycles because a Claude-authored design doc line was
ambiguous between shallow- and deep-copy semantics, in the same session whose own
commit message described applying worked-example discipline elsewhere. See
`docs/design_doc_ambiguity_checklist.md`'s origin incident. And this session's own
exprvm-web-v2 pass found a second, milder case of the same category: a Claude-authored
doc whose Decomposition Notes pins were real but not in the format later tooling
expected — correct content, never independently checked against the convention that
came after it.

The user's ask: let them hand over something closer to prose — more than a one-line
description, less than a fully-interrogated doc — and get a draft back. The risk to
design against: loosening the input from "interrogated live, ambiguities forced to
the surface" to "prose I hand over and don't get asked about" removes the one thing
currently catching ambiguities at authoring time. A drafting tool that goes straight
from prose to a doc trusted enough for `new-project`, with no independent check,
doesn't remove that risk — it relocates it. So this plan is two stages, not one, and
the second stage is not optional.

## Relationship to existing plans

This connects two efforts that were already separately proposed and never finished —
see `docs/proposed-ideas.md` §2 for both:

- **The design-doc ambiguity checklist + judgment-pass skill** (`docs/design_doc_ambiguity_checklist.md`,
  `.claude/skills/design-doc-ambiguity-check.md`) — built and live-tested (chess, goban,
  othello, fractal, kafka-sim, exprvm), 17 ambiguity classes identified. This plan's
  Stage 2 is built on top of this, unchanged.
- **The mechanical checker for the pattern-matchable classes** (1 directional/geometric,
  2 spec-derived arithmetic, 6 concrete-literals-over-symbolic-refs, 7
  package/entry-point declaration, 17 implicit-domain-knowledge) — proposed 2026-07-19,
  never built. Phase 2 below finally builds it, in the same style as
  `cmd/checkdesigndoc` (a report the human reads, not a pass/fail oracle).

Explicitly **not** in scope: no changes to the ratchet Go pipeline itself. `SURVEY_SPEC`
stays exactly as it is today — 100% dependent on a finished, human-approved design doc.
This plan is tooling on the human side of that boundary, not a new pipeline verb. It's
also distinct from `proposed-ideas.md` §1a's "behavioral-divergence escalation" idea —
that's about catching drift at *execution* time; this is about doc quality before a
project ever starts.

## Plan

### Phase 1 — Formalize the drafting step as a Claude Code skill

A skill (working name `draft-design-doc`) that takes prose input (pasted text or a
file) and produces a draft doc following `docs/design_doc_template.md` section by
section — the same authoring work Claude already does in conversation, but made
explicit and structured instead of ad hoc.

Non-negotiable discipline, carried over from how design docs are already written well
today (not new invention):

- **Verify, don't derive from memory.** Any worked example — geometry, arithmetic,
  formatting — gets checked with a real throwaway script before it goes in the doc,
  same as exprvm's division-truncation cases and connect-four's window-scoring
  values were checked. Per [[feedback_confident_wrong_needs_enforcement]], this must
  be a hard requirement in the skill's own instructions, not left to judgment — the
  whole point of that memory is that "verify when uncertain" doesn't help a model
  (or an assistant) that's confidently wrong and never feels uncertain.
- **Surface what was assumed, don't silently fill gaps.** Wherever the source prose
  doesn't pin a specific value the template requires (an exact type, a worked-example
  number, a copy/reference semantics call), the draft must flag it explicitly — e.g.
  an "Open Questions" appendix or inline markers — rather than inventing a plausible
  value and moving on. This is what currently happens via live `AskUserQuestion`
  turns; the skill needs an equivalent that survives without a live back-and-forth.

Open question to resolve before starting: does the skill ask its open questions back
to the user interactively (closer to today's process, but couples the skill to a live
turn), or always emit a written "Open Questions" section for a separate review pass
(fully offline, but a slower loop when something needs a quick answer)? Recommend
starting with the interactive form — it's strictly closer to what's already validated
to work — and revisiting a batch/offline mode only if the interactive form turns out
to be the bottleneck.

### Phase 2 — Build the mechanical ambiguity checker

The piece deferred since 2026-07-19. A new tool, same shape and philosophy as
`cmd/checkdesigndoc` (a report, not a gate — "whether a given passage is legitimately
unambiguous is a judgment call this tool doesn't attempt"):

- Class 1 (directional/geometric): flag relative-direction language ("above",
  "diagonally adjacent") not accompanied by a worked Δrow/Δcol example nearby.
- Class 2 (spec-derived arithmetic): flag a stated formula with no computed example
  value nearby.
- Class 6 (concrete literals over symbolic refs): flag a value referenced by name
  ("as defined above", "the standard value") rather than inlined.
- Class 7 (package/entry-point declaration): flag a `main.go`/entry-point file listed
  in Data Types and Function Signatures with no explicit package declaration.
- Class 17 (implicit domain knowledge): the hardest one to pattern-match — start with
  the heuristic from [[project_implicit_domain_knowledge]] (a same-fluency blind spot,
  caught previously only by a fresh subagent) and expect this class to stay
  judgment-pass-only for a while; include it here only as a placeholder to revisit,
  not a committed mechanical check.

Classes 3, 4, 5, 8 and beyond stay judgment-pass-only, per the existing checklist's
own "needs judgment" tagging — no change proposed there.

### Phase 3 — Wire the stages together

One skill invocation, in order, each stage gating the next:

1. `draft-design-doc` (Phase 1) produces a draft + an explicit Open Questions list.
2. Open questions resolved with the user.
3. Mechanical ambiguity checker (Phase 2) runs, report surfaced.
4. `design-doc-ambiguity-check` skill's judgment pass runs — fresh subagent, no
   authoring context, existing 17-class checklist.
5. `cmd/checkdesigndoc`'s pin-consistency check runs (already built).
6. Final doc + every flagged item from steps 3-5 presented to the user for one
   explicit sign-off. Only after that does the doc get copied into a project folder
   for `new-project`.

This mirrors DECOMPOSE → AUDIT → RECONCILE's own adversarial-review shape — applied
one layer earlier, to the doc itself, before any of that pipeline ever sees it.

## Sequencing

Phase 1 and Phase 2 don't depend on each other and could be built in either order or
in parallel across separate threads. Phase 3 depends on both. Per
[[feedback_propose_before_apply]], each phase still gets its own design pass before
code/skill changes land — this document is the plan, not the implementation.
