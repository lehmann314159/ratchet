*A companion piece to [the Ratchet series](https://claude.ai/code/artifact/168dda2b-2945-41dc-920e-abc5458b22a4) — not a new part of it, just one run from [the burn-in](https://claude.ai/code/artifact/46056184-99b7-4196-a475-170ad8d0f586) followed step by step, verb by verb, instead of summarized.*

## Why this run

The burn-in ran twenty-three project instances across nine design docs. This is the twenty-third — the literal last run of the entire exercise, seeded at 13:50 MDT on what was already the burn-in's final day. It happens to also be the run whose last bead produced the single most dramatic finding of the whole corpus: a five-round disagreement between two verbs about one objectively checkable fact, resolved without a human ever being asked to look at it. That's not the only reason it's worth walking through, though — three of its four beads hit something genuinely interesting on the way, and none of them turned into an escalation. That combination — friction that never becomes a fire — is what the rest of this piece is trying to make concrete, one step at a time.

The project: a **task list web server**. Add named tasks, mark them done, delete them. A single HTML page updated in place via HTMX — no full-page reloads. In-memory storage; the list resets on restart. The design doc breaks it into four files, and — as you'll see below — four beads: `store.go` (the data layer), `templates.go` (HTML rendering), `handlers.go` (the HTTP layer), and an integration bead that wires the other three together and gets the one end-to-end test.

The doc for this run was byte-identical to the one used for run 1 of the same project, three hours earlier the same day. Same prompt, same model fleet, same everything — which is part of what makes comparing the two runs interesting: one defect reproduced exactly; its consequences didn't.

**Headline:** 4 beads, all succeeded. ~2h58m wall time, seed to complete. Zero escalations, zero full-stops. Two named findings absorbed along the way.

---

## Bootstrap — before any bead runs

1. **SURVEY_SPEC** reads the design doc and the (empty) codebase, and produces a manifest of what needs to exist. Nothing notable here — this step is the same shape every time and rarely where the interesting stuff happens.
2. **VERIFY_MANIFEST** — model-free. A mechanical check confirms the manifest's shape is sane before anyone pays for a model call to certify it.
3. **CERTIFY_MANIFEST** approves the manifest.
4. **DECOMPOSE_SPEC** breaks the doc into the four beads named above, in dependency order: `task-store` → `templates` → `http-handlers` → `integration`.
5. **AUDIT_DECOMPOSITION** — a second model reviews the plan before any code exists, and it found something: the `http-handlers` bead's exit criteria only tested one handler function (`HandleCreate`) out of four. *Interesting note: this is the same underlying gap AUDIT caught on run 1 of this exact doc three hours earlier — but not the identical finding. Run 1's version of the complaint was "only a build check, no runtime test at all"; this run's version was "the exit criteria don't cover the other three handlers." Same class of gap, worded completely differently by a fresh model call working from the same prose. That's worth sitting with: DECOMPOSE's under-specification of this one bead's exit criteria is apparently a stable weak point of this particular doc, even though the exact shape of the complaint isn't.*
6. **RECONCILE_DECOMPOSITION** fixes it: names all four handler test functions explicitly and updates the exit criteria to require all of them. `agree_and_fix` — the same resolution run 1 reached, independently, for its differently-worded version of the same problem.

Bootstrap took about ten minutes. Four beads queued; `task-store` goes first.

---

## Bead 1 — `task-store`

The data layer: an in-memory list of tasks with add/toggle/delete, plus a pinned scenario the doc requires — after deleting a task, its ID is never reused, so a monotonically-increasing counter has to survive deletions.

1. **WRITE** produces a test against the pinned ID-reuse scenario. Compiles clean.
2. **CRITIQUE** is where this bead gets interesting. It spends **about 24 minutes** — long enough that this is worth calling out as its own event — re-deriving, by hand, what should happen to a `TaskStore`'s internal counter across a sequence of `Add`, `Delete(1)`, `Add` calls. It runs its own `run_go_snippet` verification, gets a result it doesn't expect, and visibly cycles through wrong hypotheses in its own reasoning trace ("*maybe the code isn't correctly... maybe the problem is the mutex?*"). At one point it produces a turn with no usable output at all — the kind of empty turn that, elsewhere in this corpus, sometimes needs a watchdog to intervene. *Interesting note: nothing intervenes here, because nothing needs to. A few turns later, CRITIQUE's own reasoning trace shows it correctly re-deriving the mutex and index logic on its own, and its final verdict is a clean `all_correct: true`. This is 24 minutes of a small model visibly struggling and then getting there anyway — the least dramatic possible outcome, which is exactly why it's worth noting: not every stall is a bug, and not every slow cycle needs a mechanism to catch it.*
3. **JUDGE** approves immediately, on the strength of CRITIQUE's now-correct verdict.
4. **EXECUTE** runs clean — `termination_cause=success`, no stall, first attempt.
5. **ANALYZE → COMPRESS → ADJUDICATE**: `declare_success`. Bead done.

Net cost: one REFINE cycle, unusually slow, zero consequence.

---

## Bead 2 — `templates`

HTML rendering: an index page and a partial re-render of the task list for HTMX to swap in. This is the bead this whole run was being watched most closely for, because run 1's equivalent bead had already hit something worth confirming: the auto-generated scaffold for `templates.go` was missing one of its three required functions (`RenderTaskList`) — an artifact of how DECOMPOSE scaffolds this particular file, not a one-off. Would it happen again, identically, on a byte-identical doc?

1. **REVISE_PENDING** lightly revises this bead's spec now that `task-store` has succeeded and its real code exists on disk.
2. **Before REFINE even starts**, reading the freshly-generated scaffold directly settles the open question: `RenderTaskList` is missing again. Structurally identical gap to run 1 — same two functions stubbed, same one absent, same doc, same DECOMPOSE. *Interesting note: this promotes the scaffold gap from "an interesting one-off" to a confirmed, reproducible defect in how DECOMPOSE scaffolds this file — 2 out of 2. What it doesn't yet tell you is whether it matters this time.*
3. **WRITE** produces a test. Compiles clean.
4. **CRITIQUE, cycle 1** — and this is where the bead earns its place in this write-up. CRITIQUE raises a real, legitimate finding (a test over-specifies by checking for visible task-ID text the doc never requires) — and, in the same breath, a second, confidently wrong one: that the test's literal `<script src="...">` tag will get HTML-escaped by `html/template` into `&lt;script&gt;`. It states this as settled fact. *It is not.* `html/template` only escapes values interpolated through `{{ }}`; literal markup that's part of the template source passes through untouched. A quick throwaway Go snippet reproducing the exact scenario confirms this directly: the tag comes out the other side exactly as written, unescaped. This attempt also happens to trip a transient malformed-JSON parse strike, gets auto-retried, and — on the retry, now on its third consecutive `run_go_snippet` call — CRITIQUE tests its own claim empirically instead of asserting it from memory, and drops the wrong finding. Final cycle-1 verdict: just the one legitimate over-specification finding, correctly stated.
5. **JUDGE, cycle 1** approves the one real finding cleanly, unaware anything else was ever in play — the wrong claim never survived long enough to reach it.
6. **WRITE, cycle 2** applies the fix.
7. **CRITIQUE, cycle 2** — the wrong escaping claim comes back. Independently re-derived from scratch, in a fresh context, with no memory of cycle 1 correcting it — and this time it's worse: not one finding but three, applied to every non-ID assertion in the test. *Interesting note: this is the sharper version of the cycle-1 event. It isn't that CRITIQUE made a mistake and learned from it. It's a belief about a specific fact — how `html/template` handles literal markup — that this model can independently regenerate wrong, from nothing, on a fresh pass, in either direction, no matter how confidently the previous pass corrected it.* This wrong 3-finding verdict also never reaches JUDGE — a different malformed-JSON strike catches it first, and the retry attempt correctly re-derives "the test is correct," matching the fact-check, before landing on a clean verdict.
8. **JUDGE, cycle 2** approves.
9. **EXECUTE** runs clean — `termination_cause=success`, no stall.
10. **ANALYZE → COMPRESS → ADJUDICATE**: `declare_success`.

*The scaffold-gap question, resolved:* reading the finished file directly, `RenderTaskList` is present and correct — EXECUTE filled in the missing function from the full design doc, same as run 1. But run 1's REFINE cycle hit a costly pileup because CRITIQUE happened to flag the missing function by name, triggering a chain JUDGE and WRITE couldn't resolve (about an hour and fifteen minutes on one bead). This run's CRITIQUE never once raised that specific complaint, across two full cycles of otherwise-thorough review — it went looking for trouble in a different place entirely, and found a different kind of trouble instead. **Same scaffold defect, twice. Same downstream cost, only once.** The consequence turned out to depend on which finding a model happened to reach for, not on whether the underlying gap existed.

Net cost: two REFINE cycles, ~53 minutes — slower than a clean bead, far cheaper than run 1's version of the same defect, zero deliverable impact either time.

---

## Bead 3 — `http-handlers`

The HTTP layer — the bead AUDIT flagged back at bootstrap for thin exit criteria, now built against the corrected version.

1. **REVISE_PENDING**, in light of the completed `templates` bead.
2. **WRITE** produces a test against all four handlers, per the corrected exit criteria.
3. **CRITIQUE** raises a finding: it wants the test to assert an *un-pinned* "Done" status marker the design doc only gives as an illustrative example, not a required literal.
4. **JUDGE** approves the test as written and rejects CRITIQUE's finding in the same sentence: *"critique findings demanding un-pinned Done status assertions are invalid over-specifications."* *Interesting note: this is the review layer working exactly as intended, in one clean move — a second model catching an over-eager finding from the first and saying so plainly, rather than deferring to it by default. It's also a quiet contrast with what happens to the doc's identical "Done status" ambiguity one bead later.*
5. **EXECUTE** runs clean, first attempt.
6. **ANALYZE → COMPRESS → ADJUDICATE**: `declare_success`.

One REFINE cycle. The fastest bead in the run — about eighteen minutes, start to finish.

---

## Bead 4 — `integration`

The last bead of the entire twenty-three-run corpus. Wires `task-store`, `templates`, and `http-handlers` together behind a real HTTP server and writes the one test that exercises the whole stack. The question at its center: does a task's "done" status render, in the real HTML, as the literal word `"true"` — or as a CSS class or checkmark, the way the design doc's own illustrative example happened to phrase it? Only one of these matches the actual code, and this single fact gets independently re-derived five times, by four different verbs, before the bead is done.

1. **WRITE, cycle 1** — a normal draft.
2. **JUDGE, cycle 1** finds two legitimate issues (an over-specified literal-string assertion; a test that mutates global state directly) and sends it back.
3. **WRITE, cycle 2** refactors properly — introduces a request-scoped wrapper, verifies a substring check via `run_go_snippet`. Compiles clean.
4. **CRITIQUE, cycle 2 → JUDGE, cycle 2** approve cleanly.
5. **EXECUTE** runs against the real implementation — and stalls. Not a mechanical stall: the test genuinely fails, because it checks for the substring `"done"` and the actual server renders the boolean field as the literal string `"true"`.
6. **ADJUDICATE** diagnoses this precisely, running its own `run_go_snippet` against the real response body to confirm which string is actually there before deciding anything. Verdict: `re_refine` — change the assertion to check for `"true"`. *This is the first of five independent re-derivations of the same fact, and the only one anyone had to ask for directly.*
7. **JUDGE, cycle 3** adopts ADJUDICATE's verified fix correctly.
8. **WRITE, cycle 4** applies it, compiles clean.
9. **JUDGE, cycle 4** — **reverses it.** Instructs WRITE back to the doc's illustrative "class or checkmark" phrasing, discarding the execution-verified ground truth from two steps earlier. *This is the pivot the rest of the bead turns on: the fact was already settled by direct evidence, and JUDGE re-litigated it against a hypothetical instead.*
10. **WRITE, cycle 5** visibly thrashes — its own reasoning trace re-derives, live, that no such class or symbol exists anywhere in the actual template, proposing and discarding one compromise after another, before settling on a hedge that checks for either substring and genuinely tests nothing.
11. **CRITIQUE, cycle 5** ignores the hedge and **independently re-derives the correct fact a third time**, unprompted, matching the original diagnosis exactly.
12. **JUDGE, cycle 5** — **overrides CRITIQUE's correct finding and approves the wrong test anyway**, describing the correct catch as "over-literal." JUDGE is now 0-for-2 on a question three other verbs had already gotten right.
13. **EXECUTE**, with the wrong assertion locked in and nothing left to send it back through, does something it's fully entitled to do: since `integration_test.go` is this bead's own output file, it rewrites the assertion to the correct one itself, before even running it, and ships a passing result — the fourth independent correct re-derivation, and the one that actually reaches the codebase.
14. **ADJUDICATE** confirms the fix holds with one more `run_go_snippet` check against the live server, and declares success.

**Final tally on this one question, across this one bead: ADJUDICATE right, JUDGE right, JUDGE wrong, CRITIQUE right, JUDGE wrong, EXECUTE right.** One verb wrong twice in a row on an already-settled, mechanically-checkable fact; three other verbs a combined three-for-three. Cost: five REFINE cycles, about half an hour of extra churn, on the very last bead the entire corpus had left to run. Zero escalation. Zero defect in the shipped code.

---

## What one run adds up to

Four beads. Three of them hit something worth stopping for — a slow but self-correcting model, a scaffold defect that reproduced exactly but cost nothing the second time, and a verb that was confidently, repeatedly wrong about a fact three other verbs had already nailed down. None of it needed a human. The project's own final state doesn't even hint that any of this happened — `SELECT status FROM beads WHERE project_id=24` reads `succeeded, succeeded, succeeded, succeeded`, same as a run where nothing interesting occurred at all.

That's arguably the actual point of a system built this way: friction survives in the log, not in the outcome, as long as the layer that's supposed to catch it — a self-correcting CRITIQUE, an execution-time ground-truth check, a mechanically-verified assertion — actually does. This run is what that looks like when it works: not the absence of mistakes, but mistakes that never got the chance to become anyone's problem.

---

*Sourced from the burn-in's live run log (`tasklist run 2`, project id 24) — job timestamps, verdict text, and file contents read directly from the record, not reconstructed from summary. For the surrounding context — what the burn-in was, and how the layered review architecture in this story got built — see [the series](https://claude.ai/code/artifact/168dda2b-2945-41dc-920e-abc5458b22a4).*
