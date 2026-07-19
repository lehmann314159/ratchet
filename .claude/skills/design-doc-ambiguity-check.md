# design-doc-ambiguity-check

Review a ratchet design doc for ambiguous specification language before it's used for
DECOMPOSE — the kind of gap that reads as fine to whoever wrote it but lets a downstream model
produce two different, individually-defensible implementations.

This is a judgment pass, not a mechanical one (no mechanical checker exists yet — this skill is
the whole check for now). Its value depends entirely on independence: the review must be done
by a fresh subagent with no memory of writing the doc, not by re-reading it in the current
conversation.

## Args

`$1` — path to the design doc to review (required). If not given, ask which doc.

## What to do

1. Resolve the design doc path. Confirm it exists before proceeding.
2. Read `docs/design_doc_ambiguity_checklist.md` in the repo root for the current catalogued
   ambiguity classes — do not skip this, the checklist changes over time and you should not
   rely on a remembered version of it.
3. Launch exactly one subagent with the Agent tool, `subagent_type: general-purpose`,
   `run_in_background: false` (the result is needed immediately). The prompt must be **fully
   self-contained** — do not reference this conversation, do not mention who authored the doc
   or why, do not summarize what you already think is wrong with it. The subagent should form
   its own independent judgment. Use this prompt shape:

   ```
   Review the design doc at <path> for ambiguous specification language.

   Read docs/design_doc_ambiguity_checklist.md first for the general test and the catalogued
   ambiguity classes.

   For every function signature, protocol description, geometric/directional claim, and
   numeric/arithmetic claim in the design doc, apply the checklist's general test: does this,
   read literally, pin down exactly one value or behavior, or could two careful readers each
   defend a different implementation? Check explicitly against each catalogued class, but also
   flag anything that fails the general test and doesn't fit a catalogued class — name it as a
   candidate new class rather than forcing it into an existing one or silently dropping it.

   Do not flag stylistic issues, missing features, or anything that isn't a genuine multiple-
   reading ambiguity. A sentence that's merely terse but has only one honest reading is not a
   finding.

   For each finding, report:
   - The exact quoted line(s) from the design doc
   - Which catalogued class it matches (by number), or "uncatalogued" with a one-line
     description of the new class
   - The two (or more) concretely different implementations a literal reader could produce
   - A suggested rewrite that would close the gap

   If you find zero genuine ambiguities, say so plainly — do not manufacture findings to seem
   thorough.
   ```

4. Relay the subagent's findings to the user verbatim in structure, without softening or
   editorializing. If the subagent found nothing, report that plainly — a clean result is a
   valid, useful result, not a failure of the check.

## Notes

- This skill exists because the checklist alone was not sufficient — see
  `docs/design_doc_ambiguity_checklist.md`'s incident notes. The independence of the subagent
  (no shared context with whoever authored the doc) is the mechanism that's supposed to work
  where self-review didn't; do not collapse this into reviewing the doc yourself "to save a
  step."
- A mechanical checker for classes 1, 2, 6, 7 (see the checklist's detectability summary) does
  not exist yet. Until it does, this skill is the entire check, including for the mechanically-
  detectable classes.
- If the subagent's findings seem to miss something you already suspect is wrong, do not
  silently add it yourself — that reintroduces the same-context bias this skill exists to avoid.
  If you think the subagent missed something, say so to the user explicitly as your own
  (non-independent) opinion, clearly labeled as such.