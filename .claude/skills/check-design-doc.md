# check-design-doc

Run the full sign-off sequence on a ratchet design doc — mechanical ambiguity scan,
independent judgment pass, pin consistency — and present one consolidated list of
everything flagged for an explicit, item-by-item decision. Only after every item is
resolved is the doc cleared for `new-project`.

Works on any design doc: one drafted by `draft-design-doc`, or a hand-written one.

This mirrors the pipeline's own DECOMPOSE → AUDIT → RECONCILE shape, applied one layer
earlier — to the doc itself, before SURVEY_SPEC ever sees it.

## Args

`$1` — path to the design doc (required). If not given, ask which doc.

## What to do

### 1. Confirm the doc

Check the file exists and has the expected `##` section headings (Overview, Data Types
and Function Signatures, Behavioral Specification at minimum). If it is missing
structural sections, say so and stop — there is nothing to check yet.

### 2. Mechanical pass

Run `go run ./cmd/checkdesigndoc --doc <path> --checks=all` and capture the full
report: the pin-vs-scenario counts, the class 1/2/6/7/17 ambiguity hits, any
`construction-form` hits, and the `bead-size` verdicts. Keep the exact
`path:line` references.

A `construction-form` hit means a Cross-Bead Contract enumerates a polymorphic
type's variants (`` `Stmt` is `AssignStmt{…}`, `PrintStmt{…}`, … ``) without
saying whether instances are struct values or pointers — the b314 defect. Carry
it as a NEEDS DECISION row; resolve it by adding one sentence to the contract
(and, if the value crosses into a test assertion, a Decomposition Notes pin).

A `bead-size` **FLAG** means a Decomposition Notes bead owns ≥4 functions AND is
heavily integrated (≥3 prior-bead deps or ≥3 Cross-Bead Contracts) — the surface
area the EXECUTE model spirals on (the lsystem `grammar` monolith escalated five
runs before it was split into three `expr`-sized sub-beads). Carry it as NEEDS
DECISION: either split the bead (own a separate `.go` file per sub-bead, give
each its own Behavioral Specification subsection + Cross-Bead Contract entries —
see the run-6 `grammar` split), or add a one-line `sizing rationale:` note to the
bullet stating why the surface area is safe. A `bead-size` **NOTE** is advisory, one of two shapes. *Under-specified*: an
integrated bead declares ≤1 function but its behavioral subsection is long — add
the unexported helper signatures to `## Data Types` so its real size is visible
(it may then FLAG). *Dense spec*: a bead with ≤3 functions whose behavioral
subsection is very long (≥70 lines) — a single hard translator (glob→regexp,
chained parsers) that has stalled EXECUTE even at low integration; split it
doc-side into named fragments or add a `sizing rationale:` note. The 70-line
threshold is a conservative placeholder pending burn-in calibration — treat a
borderline NOTE as a prompt to look, not a hard failure.

### 3. Judgment pass — independent

Invoke the `design-doc-ambiguity-check` skill on the same doc. That skill dispatches a
fresh subagent with only the doc and the checklist.

**Do not feed the mechanical report into the subagent's prompt.** The subagent must
form its own findings first — anchoring it to the mechanical hits defeats the
independence that is the whole reason the subagent exists.

### 4. Reconcile

After the subagent returns, diff the mechanical hits against its findings:

- A mechanically-flagged site the subagent **also** raised → one consolidated item,
  source "both".
- A mechanically-flagged site the subagent did **not** address → carry it forward as
  its own item, source "mechanical only, unresolved". Never drop it, never silently
  treat it as cleared.
- **Every class-17 hit is carried forward regardless of what the subagent said.** The
  checklist establishes that a same-fluency judgment reviewer has the author's blind
  spot for this class — the subagent not raising it is expected, not reassurance.
- A subagent finding with no mechanical hit → its own item, source "judgment".

### 5. Consolidated report

Present one table. Every row:

| # | class | quoted line(s) `path:line` | source | the divergent readings | suggested rewrite | status |

`status` starts as **NEEDS DECISION** for every row. If the pin check reported a count
mismatch, that is a row too (class: pin-consistency).

### 6. Sign-off

Go through the table with the user, one item at a time. Each item is resolved by one of:

- accept the suggested rewrite → apply it to the doc;
- the user gives a different answer → apply that;
- the user affirmatively waives it → record the waiver *in the doc* as an HTML comment
  at that spot (`<!-- ambiguity class N waived 2026-..: <reason> -->`), so the next run
  does not re-litigate it silently.

Do not resolve items on the user's behalf. A class-17 item cannot be waived on "a
reviewer would know what this means" — that is the exact reasoning the class exists to
reject; it needs the explicit rules written out, or a concrete statement of why the
category is genuinely fixed for this domain.

### 7. Clearance

Only once every row is resolved: print "cleared for new-project". If the user confirms,
copy the doc into the project folder they name. Nothing here is automatic — the
clearance line and the copy are two separate confirmations.

## Notes

- Steps 2 and 3 are both cheap enough to always run. Do not skip the mechanical pass
  because "the judgment pass is more thorough" — class 17 is the counterexample, and
  the mechanical pass costs no subagent call.
- If you (in this conversation) suspect something the subagent missed, say so to the
  user as your own explicitly-labelled non-independent opinion — do not fold it into
  the subagent's findings.
