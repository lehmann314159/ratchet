# Pre-mortem: ratchet, a weekend of stress-testing

Working notes captured in real time across this weekend's session, for turning into a
write-up afterward. Not a framework doc — a record of what happened, in enough detail
to reconstruct the narrative later without relying on memory. Dates are approximate
where the underlying work spanned a session; see git log / memory files for exact
timestamps if needed.

---

## What ratchet is, in one paragraph

Ratchet takes a design doc and a fleet of small local models (24B–32B, via Ollama) and
turns it into a working, tested Go project through a fixed pipeline of verbs —
SURVEY_SPEC → VERIFY_MANIFEST → CERTIFY_MANIFEST → DECOMPOSE_SPEC →
AUDIT_DECOMPOSITION → RECONCILE_DECOMPOSITION → per-bead
REFINE_TESTS_WRITE/CRITIQUE/JUDGE → EXECUTE_BEAD → ANALYZE_EXECUTION →
COMPRESS_ANALYSIS → ADJUDICATE_NEXT_EXECUTION → REVISE_PENDING, looping per bead until
the project completes or escalates. The bet: small models can build real software if
the scaffolding around them — decomposition into independent chunks, mechanical exit
criteria, an adjudication layer that decides whether to retry/revise/escalate — is
disciplined enough. The weekend's work has mostly been an argument for one specific,
recurring form of that discipline: **prefer a mechanical, ground-truth check over an
LLM judgment call, everywhere the two disagree.**

## The arc, roughly in order

**Audit stages 1–10** (docs/audit-checklist.md) — a systematic pass through the whole
pipeline before this weekend even started, fixing a long list of framework bugs found
reactively: stub-purity checks that were never built, a round-number collision, a
test-clobbering bug, dispatch.go clobbering infra-failure job status, an arbitrary file
read via `/trace?path=`, and more. All fixed, all tested. This is the baseline the
weekend's stress-testing builds on.

**Fixture/clone workflow** (docs/fixtures.md) — pause points, `save-fixture`,
`clone-project`. Lets a project be frozen right after decomposition converges and
cloned repeatedly, so bead-execution testing doesn't have to re-pay SURVEY/DECOMPOSE
every time. Five fixtures were cut this way: checkers, chess, goban, othello, tasklist.

**Phase A stress-testing** — running real projects against the audited framework to
generate broad bead-execution signal instead of narrow, hand-picked test cases.
Checkers alone took 9 attempts (v1–v9) across the whole engagement to reach a clean
decomposition; goban took 3; chess and othello each had their own multi-version
histories. Real bugs found this way, this weekend specifically: a no-write
false-positive, a vacuous bare-negated-grep check, rewind-bead deleting `go.mod`,
rewind-bead's budget merge sourcing a stale JSON field, a UI attempt-count mismatch,
two more EXECUTE_BEAD-level bugs (content-checks, unescaped grep asterisks), and a real
contradiction in `decomposeSpecSystemPrompt`'s layout-bead cap/dependency exemption
that explained checkers' whole bead-684/636 saga.

**RECONCILE self-certification** — a brittle text-comparison tie-break in
RECONCILE's escalation logic, fixed and validated live: a requeued escalation
converged clean on the first try afterward.

**Import resolution unification** — a goimports-based fix plus an `isZeroValueExpr`
float-zero-value gap, deployed live; also surfaced and fixed a second, unrelated bug
(the web UI's resume button, broken pre-decomposition).

**Fractal-smoke** (project 104→105) — the first project to use the real
`design_doc_template.md`/`design_doc_guide.md` instead of an ad hoc format. Completed
clean, 6/6 beads, on the first full run. Demonstrated the library-project shape
(Mandelbrot/Julia/Sierpinski PNG generation, no HTTP layer) working end to end, and
confirmed by direct visual inspection (rendered sample images) that the Julia
z/c-swap bug the design doc explicitly warned about did not occur.

**Kafka-sim-smoke** — a non-game domain (topic/partition/consumer-group simulation,
FNV-1a routing). Reached `complete`, 0 escalations, on its first real run; separately,
its design doc turned out to be fully ad hoc (predated the guide entirely) and got a
full rewrite plus a verified worked-hash example, the same "compute the exact number"
discipline the game docs already had for geometry, applied to hash arithmetic instead.

## This weekend, specifically: design docs, then extension planning, then checkers

**The five (then six, then seven) fixture design docs got rewritten.** Motivation: had
we actually improved the guide-driven authoring discipline, or was that still
theoretical? Concretely:
- **checkers** and **othello** had real, confirmed gaps — no worked geometry examples
  at all for either (checkers' diagonal/jump/king-move arithmetic, othello's 8-direction
  flip logic), despite both being exactly the shape of problem the guide's
  "Domain-Specific Test Scenarios" section exists to solve.
- **chess** and **goban** were already excellent (goban arguably exemplary) — light
  structural touch-ups only (an Architecture section, a heading rename).
- **tasklist** got a worked example for an ID-reuse-after-delete edge case — the
  generalized arithmetic-worked-example principle applied to something with zero
  geometry at all.
- **kafka-sim** got the full treatment described above.
- **fractal** needed nothing; it was already the reference case.

**A demo, then a second one.** Built a small Go driver to actually run the fractal
library and render real sample images (not claim they'd probably work) — confirmed the
Julia set's z/c parameterization visually. Then did the same for kafka-sim: a real
simulation run (not hand-computed data), visualized as an interactive swimlane diagram
showing partition routing and consumer-offset progress. Caught and fixed a bug in the
demo driver itself along the way (a synthetic single-partition Topic that would have
silently corrupted offset bookkeeping) before trusting its output — worth remembering
as an example of the same discipline applied to throwaway scratch code, not just the
framework.

**Extension planning** (docs/extensions-roadmap.md) — before building anything new,
laid out five concrete cases for extending ratchet beyond from-scratch builds: modify
an existing project (chess text→graphical UI), fix a reported defect from a symptom
description (no design doc to expand), reuse a project as a library/import, reuse a
project as an external executable dependency, and sequential composition via a
published interface (backend → frontend). Verified one surprising thing before
assuming anything: the "sequential composition" case already works today, unmodified —
Cross-Bead Contract `producer`/`consumer` fields are pure free-text prose with zero
structural validation, so nothing stops a frontend design doc from citing a backend's
published API docs. The other four don't share one mechanism; they share exactly two
primitives ("unowned files" — a project-scoped list of paths no bead owns, needed by
both "modify" and "leverage by copy"; "resurvey real code + delta decomposition" —
needed by "modify" and by "give a library a CLI so it can be an executable
dependency"), plus one case (bug-fixing from a symptom) that's genuinely a different
pipeline shape, deliberately not forked into a separate app to avoid the bit-rot risk
of two codebases re-discovering the same substrate bugs independently.

**Then checkers, mid-flight, gave the best concrete example of the recurring theme.**

## The checkers bead-2 story

This is the one worth telling in detail, because it's a clean, fully-traced example of
exactly the failure mode the whole weekend kept finding in different shapes.

Checkers' `move-generation` bead (implementing `ValidMoves`/`AllValidMoves`) failed its
5th and final execution attempt and escalated. The adjudication history blamed the
model each time: *"The agent has failed to implement the occupancy check for
destination squares across three attempts, despite explicit verbatim logic
requirements in the spec. This is a recurring capability failure."* By the final
attempt, the bead spec had accumulated a verbatim Go code snippet ADJUDICATE had
injected, insisting the model implement the occupancy check exactly as shown.

The actual implementation was correct the whole time. The test was wrong.

Tracing it end to end: the design doc's rewrite (this weekend, described above) added
worked geometry examples for every move type — but never stated the destination-must-
be-empty rule at all, because it's so basic a frontier-model author doesn't think to
write it down (exactly the blind spot `design_doc_guide.md`'s "Writing for small
models" section warns about — the author cannot make the mistake they're blind to).
During its very first execution attempt, unprompted by anything in the spec, the model
wrote its own extra test — `TestAllValidMoves/BlockedPieces` — drawing on general
checkers knowledge. But it placed three pieces on the board, correctly blocked only
one of them, and then asserted the *entire side* had zero legal moves. The other two
pieces it placed had wide-open, perfectly legal moves. No correct implementation could
ever pass that assertion. `ADJUDICATE_NEXT_EXECUTION`, seeing the failure, didn't
question whether the test's own setup supported its own assertion — it concluded the
implementation had a gap and formally added "BlockedPieces" to the bead's permanent
required-scenarios list. Every attempt after that reproduced the same test, the same
failure, and the same misdiagnosis, three times running, right up to the escalation.

Verified by hand (and cross-checked against the real trace logs) that the model's
occupancy-check code was correct on every attempt, and that the four "extra" moves the
test complained about were exactly the legal moves the two unblocked pieces should
produce.

## What shipped as a result

Two framework fixes, both committed and deployed:

1. **ADJUDICATE now recognizes byte-identical recurring failures as a distinct,
   stronger signal.** A prior mechanism (`recurringTestFailureNote`) already matched
   subtest *names* across attempts and injected an advisory note — it had actually
   fired here, which is why the adjudication reasoning uses the word "recurring" from
   attempt 3 onward. But matching names alone wasn't a strong enough signal to
   overrule the LLM's judgment, which chose the wrong diagnosis three times anyway.
   Now the check also compares the captured failure *text* itself: two independently
   regenerated implementations converging on byte-identical output is much harder
   evidence to argue past than a recurring name, and gets its own, more assertive note.

2. **rewind-bead now preserves REVISE_PENDING's contributions.** Rewinding a bead
   (the standard remediation for an escalation) reverted its prose unconditionally to
   revision 1, discarding *everything* added since — including any genuinely useful
   cross-bead learning REVISE_PENDING might have added, not just the bad content
   ADJUDICATE had accumulated. This time REVISE_PENDING's revision happened to add
   nothing of substance, so nothing was actually lost — "we might not be so lucky next
   time" was the exact framing that prompted the fix. Verified against the entire
   archived project history before changing anything: every bead that ever accumulated
   an ADJUDICATE_NEXT_EXECUTION revision had all its REVISE_PENDING/
   RECONCILE_DECOMPOSITION revisions strictly before that point, never after — so the
   fix (revert to the last revision *not* created by ADJUDICATE_NEXT_EXECUTION) only
   ever narrows what gets discarded, never widens it.

Both shipped with tests reproducing the original bug, both verified against real
history before implementation, not just the one motivating case.

## Where things stand as of this writing

- Checkers (project 1) is running a full, unattended run (not stopped at the fixture
  point) — bead 1 succeeded, bead 2 was rewound after the escalation above and is
  re-running against the fixed binary; its fresh test suite this time does **not**
  include a self-invented "BlockedPieces" test.
- Six more projects (chess, goban, othello, tasklist, fractal, kafka-sim) are queued
  behind checkers with `--pause-after-reconcile`, each set to freeze right after
  decomposition converges so they can become fresh, validated fixtures.
- `docs/extensions-roadmap.md` is the planning doc for what comes after this round:
  the unowned-files primitive first, then resurvey+delta decomposition, then the
  bug-fix pipeline shape — evaluated empirically (starting with a known bug, chess's
  castling-queenside defect) rather than assumed to need a new app.
- The whole `ratchet-projects` working directory from before this weekend's redo was
  archived, not deleted, to `ratchet-projects-archive-2026-07-18/` — the raw evidence
  behind every claim in this document (and every memory file from earlier in the
  engagement) is still there if it needs re-checking.

## The theme, if there's one sentence for it

Every fix this weekend — the RECONCILE tie-break, the mechanical checks preferred over
LLM judgment, the checkers misdiagnosis, the rewind-bead over-reversion — is the same
shape: somewhere in the pipeline, a judgment call was being made that a cheap,
ground-truth, mechanical check could have made more reliably instead. The interesting
result isn't any single bug. It's that this pattern kept recurring across completely
unrelated parts of the system, which is itself evidence about where the framework's
real remaining risk lives.
