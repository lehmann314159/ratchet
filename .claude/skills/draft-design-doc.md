# draft-design-doc

Turn a prose description of a project into a draft ratchet design doc that follows
`docs/design_doc_template.md`, doing the authoring work Claude already does in
conversation — worked examples verified with real scripts, exact signatures, pins for
load-bearing literals — but as a repeatable step instead of an ad-hoc one.

This produces a **draft**. It is not trusted for `new-project` until it has been through
`check-design-doc` (mechanical scan + independent judgment pass + pin consistency +
your sign-off). Chain into that skill when you finish, or tell the user to run it next.

## Args

`$1` — path to a file containing the prose, or a directory to write the draft into. If
neither is given, ask the user to paste the prose or point at a file, and ask where the
draft should be written.

## What to do

### 1. Read the inputs

- The prose (the whole thing — do not skim).
- `docs/design_doc_guide.md` in full. It explains what each section is for, how the
  pipeline consumes it, and the failure mode each one prevents. Do not work from a
  remembered version.
- `docs/design_doc_template.md` for the exact section order and headings — these are
  load-bearing (`cmd/checkdesigndoc` parses by heading; SURVEY/DECOMPOSE read sections
  by name).

### 2. Draft the doc section by section

Follow the template's seven sections in order. Omit a *conditional* section entirely
(no empty heading) when it does not apply — Architecture (skip for < 4 source files and
no strong layout opinion), Domain-Specific Test Scenarios (skip when no bead tests
non-obvious geometry), Cross-Bead Contracts (skip when nothing crosses a bead
boundary), Decomposition Notes (skip when DECOMPOSE's heuristics suffice).

Write for a model with zero domain knowledge. Where the guide's "Writing for small
models" section or its Common Mistakes table names a pattern that applies here, apply
it — pseudocode for BFS/flood-fill, explicit step-ordering dependencies, coordinate
mapping tables, `package main` stated, template storage form stated.

### 3. Hard rules — not judgment calls

These are the disciplines that make a design doc trustworthy. They are requirements of
this skill, enforced by doing them, not by deciding whether they seem necessary.

1. **Every worked example is script-verified before it goes in the doc.** Every
   Δrow/Δcol, every arithmetic result, every hash value, every ID-sequence value, every
   exact format string — computed by running a real throwaway script (a scratch Go
   `main`, or shell) *in this session*. Not derived from memory, not "this is
   obviously". After each verified example, leave an HTML-comment citation of what was
   run and what it produced, e.g.
   `<!-- verified: go run /tmp/div.go  =>  -7/2 == -3, -7%2 == -1 -->`.
   An example with no citation is an incomplete draft. This rule exists because
   "verify when uncertain" does not help someone who is confidently wrong and never
   feels uncertain — see `memory` feedback_confident_wrong_needs_enforcement.

2. **Gaps are surfaced, never silently filled.** Whenever the prose does not pin down
   something the template requires — an exact type signature, a worked-example number,
   copy-vs-reference semantics for a returned pointer/slice/map, enum literal values,
   `package main`, a coordinate-system mapping, an out-of-scope boundary — add an entry
   to a trailing `## Open Questions` section that quotes the under-specified spot and
   states the concrete alternatives a literal reader could each pick. Never invent a
   plausible value and move on.

3. **Pin every load-bearing literal.** For any worked scenario whose *specific values*
   (not just its governing rule) must survive into a bead's spec, add a
   `- **Pin ...**` bullet to Decomposition Notes naming the exact bead and the exact
   values. Prose in Behavioral Specification or Domain-Specific Test Scenarios is *not*
   reliably carried by DECOMPOSE; a Pin bullet is.

### 4. Resolve Open Questions

- The `## Open Questions` section always stays in the draft as written — it is the
  durable artifact and it works with no live conversation.
- **Additionally**, when running interactively: after the draft is written, batch the
  open questions into `AskUserQuestion` calls (group related ones). Fold each answer
  into the relevant section, and delete the resolved entry from `## Open Questions`.
  Anything the user skips or defers stays in the section verbatim.
- Re-verify with a script any new worked example an answer introduces (rule 1 still
  applies).

### 5. Finish

1. Write the draft to a file (`<name>-design-doc.md` next to the prose, or the path the
   user gave).
2. Run `go run ./cmd/checkdesigndoc --doc <draft>` yourself. Resolve any pin/scenario
   count mismatch it reports before handing off. The ambiguity-scan hits are for
   `check-design-doc` to work through with the user — note them, do not silently edit
   around them.
3. Report: the draft path, the count of remaining Open Questions, and the
   `checkdesigndoc` summary. Then run `check-design-doc <draft>` (or, if the user would
   rather stop here, tell them that is the next step).

## Notes

- Drafting is done inline, by you. Independence matters for *checking* the doc, not for
  writing it — and `check-design-doc` provides that with a fresh subagent. Do not
  dispatch a subagent to write the draft.
- Do not skip straight to `new-project`. A draft that has not been through
  `check-design-doc` has had no independent pass and no sign-off — that is exactly the
  risk this two-step workflow exists to remove.
