# Post-audit stress test + library/UI roadmap

Started 2026-07-16, right after the Stage 1-10 audit (`docs/audit-checklist.md`) and
the fixture/clone workflow (`docs/fixtures.md`) both landed. Motivation: the audit
fixed a long list of framework bugs, but every one of them was found reactively —
by running a specific project and hitting a specific failure. This roadmap is a
deliberate attempt to generate a longer, broader run of real bead-execution activity
against the now-fixed framework, using the fixture/clone workflow so each run starts
from a clean post-decomposition state instead of re-paying SURVEY/DECOMPOSE every time.

Three phases, each gated on the previous one producing enough signal. Not expected to
run straight through without incident — checkers alone took 9 attempts (v1-v9) to
reach a clean decomposition during the audit era. Handle failures as they come, the
same way as any other project: rewind the bead, root-cause, fix the framework, don't
patch the DB or the test files directly ([[feedback_no_db_patches]],
[[feedback_no_manual_test_fixes]], [[feedback_rewind_vs_manual_patch]]).

---

## Phase A — repeated stress runs via clone-project

**Goal**: exercise EXECUTE_BEAD/ANALYZE/COMPRESS/ADJUDICATE/REVISE_PENDING repeatedly
against the audited framework, using `clone-project` from the existing fixtures so
decomposition never has to re-run. Roughly 3 attempts each of checkers, chess, and
goban — no fixed target count, since the whole point is to run "as we go" and stop
whenever enough signal has accumulated.

Fixtures available to clone from (`docs/fixtures.md` §2-3):
- `-1` — checkers post-RECONCILE (9 pending beads)
- `-2` — chess post-decomposition
- `-3` — goban post-decomposition

- [ ] checkers-try-1 — project 100, cloned from `-1` 2026-07-16, bead 684 in progress
- [ ] checkers-try-2
- [ ] checkers-try-3
- [ ] chess-try-1
- [ ] chess-try-2
- [ ] chess-try-3
- [ ] goban-try-1
- [ ] goban-try-2
- [ ] goban-try-3

**Note on pacing**: the orchestrator only ever drives one `status='active'` project at
a time (`internal/orchestrator/queue.go:30`, `LIMIT 1`, oldest-id-first) — clones queue
and run strictly sequentially, not in parallel. Clone new attempts as we go rather than
batching all 9 up front.

**If a framework bug surfaces**: fix it in place (same discipline as the audit stages),
then decide per bug whether prior attempts in this phase need re-running or whether the
fix only affects forward progress. Log real bugs found here the same way the audit
stages did, so this phase's own memory entry accumulates a bug list.

**Use fixture cloning to iterate**, not just to seed initial attempts: if a
EXECUTE_BEAD/REFINE_TESTS fix needs testing against a bead already known to be
troublesome, clone straight to that state rather than reproducing it from scratch.

---

## Phase B — http-handlers assessment → ratchet-http scoping

**Gated on**: enough successful (or far-enough-progressed) Phase A runs across all
three games to compare real `handlers.go`/`templates.go`/routing code side by side.
This revives the original roadmap item ([[project_ratchet_http_framework]],
[[project_roadmap]]) that was paused for the audit — the underlying plan is unchanged:
three independent data points, not one, to avoid over-fitting a shared library to
chess's specifics.

- [ ] Collect the http-handlers-shaped bead output from every Phase A run that reached
      that bead (not just fully-COMPLETE projects — a run that got through
      http-handlers before failing later still counts as a data point).
- [ ] Diff across checkers/chess/goban: what's byte-for-byte or structurally identical
      (routing boilerplate, htmx wiring, template funcMap patterns) vs. what's
      genuinely game-specific.
- [ ] Scope the `ratchet-http` library API surface from what's actually shared, not
      from guessing ahead of the data (per the original roadmap's explicit reasoning).
- [ ] Design cross-project dependency support in ratchet if the library needs it
      (go.mod `replace` directive handling — flagged as a requirement in the original
      roadmap, not yet built).

---

## Phase C — new use case: altering an existing (already-complete) project

**Gated on**: enough Phase A success to justify investing in a capability the
pipeline has never had. Motivating example: replace chess's text-based board with
images for pieces and click-to-move interaction, on top of an already-COMPLETE chess
project.

**This is not just "run the pipeline again."** Every existing verb path (SURVEY →
DECOMPOSE → ... ) assumes a fresh design doc describing a from-scratch build, with no
existing code as ground truth. Altering a complete project is a structurally different
problem: the "survey" of what exists is the real codebase, not a model-authored
manifest, and decomposition needs to produce beads that modify/extend existing files
without the rest of DECOMPOSE's from-scratch assumptions (e.g. layout-bead stub
scaffolding) applying.

- [ ] Design what "alter an existing project" means mechanically before writing any
      code — candidate shapes to weigh: (a) a new entry verb that surveys real disk
      state instead of running SURVEY_SPEC fresh, (b) a DECOMPOSE variant that takes
      the existing survey.md + a delta design doc and produces beads scoped to
      changed/new files only, (c) something else. Don't assume a shape here — this
      needs its own design pass, per [[feedback_propose_before_apply]].
- [ ] Once designed: pick a completed chess project as the target, write the delta
      design doc (image pieces + click-to-move), run it through the new path.

---

**How to apply**: when resuming this thread, update the checkboxes above rather than
re-deriving phase scope from scratch. `[[project_roadmap]]` memory should carry only a
one-line status pointer here, same as it does for `docs/audit-checklist.md`.