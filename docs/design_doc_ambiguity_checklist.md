# Design doc ambiguity checklist

A living list of ways a design doc can state something true while still leaving a gap that a
model fills confidently and inconsistently across independent passes. Read by both the
mechanical checker (`go run ./cmd/checkdesigndoc --checks=ambiguity --doc <path>` — covers
classes 1, 2, 6, 7, 17 as an over-flagging report) and the `design-doc-ambiguity-check`
skill's review subagent. Grow this list by adding an entry every time an incident traces
back to a class not yet here — do not try to enumerate every possible ambiguity up front.

## The general test

Does the sentence, applied literally, pin down exactly one value or behavior — or is it
compatible with two or more different implementations that a careful, literal reader could
each defend? If a downstream model could produce two contradictory-but-individually-defensible
readings, it's ambiguous, even if it doesn't feel ambiguous to whoever wrote it (they already
have one reading in mind and can't see the other).

## Catalogued classes

### 1. Directional/geometric relationships
Relative-direction language ("moves toward lower row indices", "up the board") is not enough.
Requires a worked coordinate example: exact Δrow/Δcol for every distinct movement type.
**Detectable:** mechanically (relative-direction word list + absence of a nearby coordinate
pair) for a first pass; judgment for full coverage.
**Source:** checkers occupancy-rule and direction incidents.

### 2. Spec-derived arithmetic
State the exact computed number (army counts, capacities, hash values, ID sequences), not just
the formula or constraint it satisfies.
**Detectable:** mechanically (formula/constraint language without a nearby literal number).
**Source:** worked-example rule generalized from checkers geometry to kafka-sim's FNV-1a hash.

### 3. Copy/reference/ownership semantics
Any function returning a pointer, a slice of pointers, or a map must state explicitly whether
the caller receives an independent copy (safe to mutate) or a shared reference (mutation leaks
through to the source), down to the field level if the container is copied but its elements are
not.
**Detectable:** judgment only. Mechanical layer can flag the site (return type matches
`[]*T`/`map[...]*T`/bare pointer) but cannot determine whether nearby prose actually resolves
it.
**Source:** tasklist `(*TaskStore).All() []*Task`, 2026-07-19 — see
[[project_design_doc_ambiguity_checklist]] in memory for the full incident.

### 4. Fragment/partial-update scope (web apps)
Any HTMX/AJAX swap target must be stated to contain every piece of UI state that changes after
the triggering action (turn indicator, score, counts) — not just the primary content. State
outside the swap target goes silently stale.
**Detectable:** judgment only.
**Source:** four consecutive game projects hit this before it was written down.

### 5. Protocol contract completeness
A handler contract is only complete if it states the obligation for every return variant, not
just the success/happy path (e.g., an AI actor returning "passed" must be specified to trigger
the same state transition a human pass does).
**Detectable:** judgment only.
**Source:** othello-v1, gogame-v3 — both omitted the AI-pass branch.

### 6. Concrete literals over symbolic references
When a spec refers to a type or constant defined elsewhere ("use the Color type from
game-state"), inline the literal values ("White is Color(-1), Black is Color(1)") rather than
pointing at where they're defined.
**Detectable:** mechanically, partially (references to "the X type/constant from Y" without a
nearby literal value nearby).
**Source:** REVISE_PENDING design rationale, session 25.

### 7. Package/entry-point declaration
`main.go`'s spec must say `package main` explicitly. `go build` gives zero error signal if the
model writes a different package name — it just produces a static archive instead of an
executable.
**Detectable:** mechanically (main.go in output_files without a literal `package main` string
nearby).
**Source:** othello-v1.

### 8. Redundant bidirectional gloss
Don't pair a precise, checkable rule with a looser relative restatement of the same fact
("Red moves toward lower row indices (up the board)"). If a downstream step ever echoes back
only the gloss half, the two can silently diverge. State the fact once, at its most precise
form, not twice at different precision levels.
**Detectable:** judgment only.
**Source:** checkers-v2 session 55 — the gloss was echoed back incorrectly downstream.

### 9. Collection ordering guarantees across mutating operations
When a collection is exposed via an accessor (e.g. `All()`) and other operations can mutate it
(delete, insert, reorder), the accessor's ordering guarantee relative to those mutations must be
stated explicitly (e.g. "insertion order, preserved across deletes"). Otherwise an
order-preserving removal (`append(s[:i], s[i+1:]...)`) and a swap-with-last removal
(`s[i] = s[len(s)-1]; s = s[:len(s)-1]`) are both valid readings of "removes the element," and
they diverge on any scenario with 3+ elements where a non-last one is removed — a worked example
with only 2 elements at removal time won't surface the disagreement.
**Detectable:** judgment only — requires tracing state across more than one method.
**Source:** tasklist `Delete()`/`All()` interaction, found by the `design-doc-ambiguity-check`
skill's first live run, 2026-07-19.

### 10. Illustrative alternatives standing in for a required literal
"e.g. X or Y" phrasing for a value that's produced by one bead and consumed/asserted by another
(a CSS class, an HTML marker, a status string) doesn't commit either bead to the same choice —
each can independently pick a different one of the offered alternatives. Lower risk when the
value is only ever used and checked within a single bead; a real problem the moment it crosses a
producer/consumer boundary between beads.
**Detectable:** judgment only — requires knowing which values cross bead boundaries.
**Source:** tasklist "done indicator" (`class="done"` vs. a checked symbol vs. other markers),
found by the `design-doc-ambiguity-check` skill's first live run, 2026-07-19.

### 11. Exhaustive-looking list/algorithm silently omitting a required step
A spec that enumerates "all special cases" or numbered algorithm steps reads as complete, but
omits a step actually required elsewhere in the system — a state-transition function's spec
lists every special case it handles except the one that flips whose turn it is; an algorithm
step dereferences a field whose comment says it's normally nil, without restating the guard. The
exhaustive framing is exactly what makes this dangerous: a reader has no textual cue that
anything is missing.
**Detectable:** judgment only — requires knowing what invariant the surrounding system actually
depends on, not just reading the list in isolation.
**Source:** chess `ApplyMove` (Turn flip omitted from "handles all special cases"), goban
`PlaceStone` step 2 (KoPoint nil guard dropped from the algorithm restatement despite being
documented on the struct field), othello `FindFlips` (never checks the target cell is empty
despite an otherwise exhaustive direction-scan), chess `promotion` field (default given, other
three letters' mapping never stated) — 2026-07-19 batch review of chess/goban/othello/fractal/
kafka-sim.

### 12. Persistence/mutation site left unstated
When mutable state lives in a package-level variable and the functions operating on it are
explicitly non-mutating (return a new value rather than modifying in place), some caller must be
the one that writes the result back — the spec must state which caller, and when, or two readers
will disagree about whether/how often the state actually persists.
**Detectable:** judgment only.
**Source:** chess `HandleMove` (never states it reassigns the package-level `game` var, unlike
its sibling `HandleReset` which does explicitly), othello `HandleIndex` (calls `NewGame()`
unconditionally, contradicting the doc's own "one persistent game" invariant unless lazy-init is
inferred) — 2026-07-19 batch review.

### 13. Internal contradiction between two authoritative-looking statements
The same doc states the same fact twice, in different sections, and the two don't agree — an
architecture diagram lists a function in one file while a "strict" file-assignment-rules section
lists it in another; an exit criterion quotes a literal substring the doc's own rendering rule
guarantees never actually occurs; a local behavioral-spec paragraph omits a copy-semantics detail
that a cross-bead-contract section 175 lines away states correctly. Both statements read as
ground truth in isolation; nothing marks either as the override.
**Detectable:** judgment only — requires cross-referencing distant sections against each other,
not reading either in isolation.
**Source:** goban (diagram vs. file-assignment rules; exit-criterion literal vs. actual rendered
class attribute), chess (`IsInCheck`'s local spec vs. cross-bead contract on copy semantics),
fractal (worked test example's coordinates vs. the pixel-mapping formula it's derived from) —
2026-07-19 batch review.

### 14. Test-fixture base/precondition state left unstated
A worked test scenario describes specific pieces/values/state without saying what the rest of
the world looks like at that moment — a fresh empty container, or a continuation of whatever a
previous scenario left behind. Both readings are literally consistent with the words given, but
usually only one is actually consistent with the scenario's own stated conclusion once you trace
through it — which the reader has to independently verify (or not notice) rather than being told
directly.
**Detectable:** judgment only — requires actually tracing whether the stated fact holds under
each reading, the way classes 1/2's worked-example checks do.
**Source:** chess (test scenarios 1-6 never say "board cleared," unlike scenarios 7-8, and the
standard-setup pieces would actually block the stated line-of-sight claims), kafka-sim ("the
same topic" phrasing contradicts the stated offsets if state truly carries over from the prior
subtest) — 2026-07-19 batch review.

### 15. Overloaded sentinel value used as a control-flow gate
A single return value is documented to mean two different things depending on context (e.g. a
"winner" function returns the same sentinel for "game still in progress" and "game over, ended
in a tie"), and the spec then reuses that same value elsewhere as its official test for a third,
unrelated question ("is the game over yet"). The overload is invisible unless a reader traces
every call site back to the type's full documented range.
**Detectable:** judgment only.
**Source:** othello `CheckWinner() == Empty` used simultaneously as "not over" and "over, tied,"
then reused as the AI-move-gate's over/not-over check — 2026-07-19 batch review.

### 16. Iteration/concurrency model unstated for multi-entity processing
When a function processes multiple independent sub-entities (partitions, files, players) via a
caller-supplied callback, and the surrounding doc promises concurrency primitives (goroutines,
channels, mutexes) elsewhere, the function's own spec must state whether entities are processed
sequentially or concurrently, and whether the callback must be safe for concurrent invocation —
otherwise two implementations differ not just in performance but in the correctness obligations
placed on every caller.
**Detectable:** judgment only.
**Source:** kafka-sim `ConsumerGroup.Subscribe`'s per-partition delivery model, left unstated
despite the doc's Overview promising goroutines/channels/mutexes — 2026-07-19 batch review.

### 17. Self-describing category phrase standing in for the rule it names
A phrase that names a category as if the name were itself the specification ("standard
movement rules", "normal betting rules", "usual tie-breaking procedure") reads as complete to
anyone who already knows what the category means — which includes an independent judgment-pass
reviewer whenever the reviewer shares the author's domain fluency. This is not the same failure
as class 1 (geometric claims stated with relative language): class 1's phrasing at least
gestures at needing a specific answer ("moves toward lower row indices" implies a direction
exists to state); a self-describing category phrase doesn't gesture at anything missing at all,
because to a fluent reader it isn't missing anything. A judgment-pass reviewer with the same
fluency the author has will not flag it, on repeated independent runs, for exactly the same
reason the author didn't write it out — the gap is invisible from inside the shared fluency, not
merely unlikely to be caught.
**Detectable:** mechanically, and *only* mechanically — grep the doc for
`standard|normal|usual|conventional|typical` (or similar) adjacent to a rules/behavior/procedure
noun. Judgment review is not a substitute here; a same-fluency reviewer has the identical blind
spot as the author, confirmed empirically (see below) rather than assumed.
**Source:** chess `PseudoLegalMoves`' original "Knight, Bishop, Rook, Queen, King: standard
movement rules" (zero content) went unflagged by an independent fresh-subagent review of the
full document — the reviewer caught six other real issues in the same pass but never mentioned
this line. Only fully axiomatizing the movement rules (explicit (Δrank, Δfile) sets for every
piece) removed the gap; a second independent review of the rewrite raised zero findings on that
section, confirming the fix worked by removing the ambiguity at the source, not by the reviewer
catching it. 2026-07-19, first live test of the "write for zero domain knowledge" discipline —
see [[project_implicit_domain_knowledge]] in memory for the full experiment writeup.

## Detectability summary

The mechanical checker for the first three rows is **built** — `cmd/checkdesigndoc`'s
`ambiguity` check. It is a tuned, over-flagging report (see `docs/design-doc-drafting-tool.md`
Phase 2 for the exact trigger/clearing rules and per-fixture flag ceilings), not a gate.

- **Mechanically detectable (first-pass filter):** 1, 2, 6, 7, 17
- **Judgment required (no substitute for a review pass):** 3, 4, 5, 8, 9, 10, 11, 12, 13, 14, 15, 16
- **Mechanically detectable, and judgment review cannot substitute (reviewer shares the same
  blind spot as the author):** 17 — this is a distinct category from the other two rows, not a
  third point on the same spectrum.

The mechanical layer is a cheap pre-filter, not a substitute for the judgment pass — most
catalogued classes, including the one that motivated this checklist, require actual reasoning
about whether nearby prose resolves the ambiguity.