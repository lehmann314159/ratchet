# UI modernization plan

The web UI (`internal/ui`) was last meaningfully extended early in the
`loop-mode` branch (`184eda3` surfaced post-exec reports, `a3305b3` surfaced
rewind snapshots). Everything the branch added *after* that — iteration
lineages, cascade iterations, the human guidance log, `reconcile_self_resolve` —
has no UI representation, and several older pipeline phases (decomposition
debate, manifest bootstrap, REFINE_TESTS cycle) were never surfaced at all.

This doc is the multi-session backlog for closing that gap. Each workstream is
self-contained. Do them roughly in the **Sequencing** order at the bottom, but
any one can be picked up cold.

## How to use this doc

- Each workstream has a **Status** line: `NOT STARTED` / `IN PROGRESS (notes)` /
  `DONE (commit)`.
- Update the Status line and the Progress log in the same commit as the work.
- "Files" lists are starting points, not fences.
- Every workstream ends with a **Tests** note — `internal/ui/handlers_test.go`
  already has `openTestServer` / `seedProject` / `seedBead` helpers and an
  in-memory DB; extend those rather than inventing a new harness.

## Progress log

| Date | Workstream | What landed | Commit |
|------|-----------|-------------|--------|
| 2026-08-30 | — | Plan written | ab75e12 |
| 2026-08-30 | Milestone A | W7 (remove-project FK guard), W8 (status CSS), W13 htmx vendored | 9a9a51a |
| 2026-08-30 | Milestone B | W3 (guidance-log render + rewind-from-UI), W10 (pause reason) | 2d67153 |
| 2026-08-30 | Milestone C | new GET /projects/{id} page, W1 (lineages), W9 (active-project), W2 (cascade) | 7937138 |

---

## Guiding constraints

- **Minimal dependencies.** `go.mod` has exactly one direct dependency
  (`modernc.org/sqlite`). Adding a markdown renderer or any other library is a
  decision to raise with Mike first (see **Open decisions**).
- **Read-mostly.** The UI is a dashboard + a small set of guarded state
  transitions on escalated jobs / projects. New write actions (W3's rewind) must
  follow the existing pattern: delegate to `internal/project`, never hand-roll
  DB mutation logic in a handler that duplicates a CLI path.
- **Server-resolved paths only.** `handleTrace` / `handleBeadReport` resolve
  every filesystem path from the DB, never from the request. Any new file-
  serving handler does the same (`c83c7e1` fixed an arbitrary-read hole here).
- **Path values, `html/template`, `ServeMux` patterns** — Go 1.22+ routing, no
  router library. Keep it that way.
- **Concurrency guards.** Every state-changing handler claims its row with a
  status-guarded `UPDATE ... WHERE status = 'expected'` and treats
  `RowsAffected() == 0` as a 409. New actions match this.

---

## W1 — Iteration lineages

**Status:** DONE (Milestone C, 2026-08-30). `ProjectRow` carries
`LineageRootID`/`IterationNumber`/`CascadeBaselineProjectID` via a shared
`projectColumns` + `scanProjectRow` helper (replaced three hand-written column
lists). Dashboard "All Projects" table is grouped by lineage (`groupByLineage`
→ `[]LineageGroup`): multi-iteration lineages get a header row + indented
members ordered by iteration; standalone projects render as plain rows. The new
`GET /projects/{id}` page shows `iteration N`, an "Iteration Lineage" strip of
all siblings, and prev/next-iteration links (`queryLineageMembers`). Tests:
`TestDashboard_GroupsLineage`, `TestProjectDetail_ShowsIterationNav`.

**Goal.** Make the loop-mode iteration structure a first-class axis of the
dashboard instead of showing iterations 1/2/3 of a lineage as unrelated rows.

**Why.** The whole loop-mode premise is "human reviews output, redirects into
the next iteration." The iteration chain is the primary object a human navigates
and it is currently invisible.

**Data available.**
- `projects.lineage_root_id` — FK to the id of the lineage's first project.
  Every project has one (backfilled by `backfillLineageRootID`, populated by
  `project.Create`). A standalone project points at its own id.
- `projects.iteration_number` — 1-based position in the lineage.
- `clone.go` guarantees at most one project per `(lineage_root_id,
  iteration_number)`.

**Files.** `internal/ui/queries.go` (`queryAllProjects`, new
`queryLineages`), `internal/ui/handlers.go` (`dashboardData`),
`internal/ui/templates/dashboard.html`, maybe a new `templates/lineage.html`.

**Steps.**
1. Add `LineageRootID int64`, `IterationNumber int` to `ProjectRow`; select them
   in `queryActiveProject` / `queryAllProjects`.
2. In the "All Projects" table, group rows by `lineage_root_id`. Single-project
   lineages render exactly as today. Multi-iteration lineages render as a group
   header (label of iteration 1 + "· N iterations") with the iterations nested
   and ordered by `iteration_number`.
3. Show `iteration N` as a column/badge.
4. On the bead-detail and (new, W1.5 optional) project-detail pages, add
   prev/next-iteration links computed from `(lineage_root_id, iteration_number
   ± 1)`.
5. Mark the row that is the current cascade/active member of the lineage.

**Tests.** Seed a 3-project lineage (roots + two clones); assert the dashboard
groups them and orders by iteration_number; assert a standalone project is not
grouped.

---

## W2 — Cascade iterations

**Status:** DONE (Milestone C, 2026-08-30). `ProjectRow.IsCascade()` from
`cascade_baseline_project_id`. The `GET /projects/{id}` page shows a "Cascade
iteration" banner (linking the baseline project) and a "Cascade Review" table:
per bead, `reset — spec changed` vs `unchanged — inherited from baseline`,
derived from whether a `traces/_bead-{id}-cascade-*` snapshot dir exists
(`listSnapshots`, generalized from `listRewindSnapshots`). `queryCascadeBeads`
+ handler-side snapshot check. Not yet done: reusing `handleBeadSnapshot` to
*display* a cascade snapshot's contents (rewind snapshots already have this;
cascade ones only get the reset/unchanged flag) — folded into W11's forensics
pass. Test: `TestProjectDetail_CascadeReview`.

**Goal.** When a `clone-project --design-doc` cascade project is running or
done, show that it is a cascade and what its review concluded per bead.

**Why.** Cascade is a distinct bootstrap path (state-machine doc §2) with its
own semantics (`CASCADE_REVIEW`, title-matched spec diff, `succeeded` beads
reset, zero-diff → `complete`). Today it presents as a generic project with a
lone unexplained `AUDIT_DECOMPOSITION` job.

**Data available.**
- `projects.cascade_baseline_project_id` — set **only** by
  `clone-project --design-doc`. Its presence is the cascade marker.
- Beads are matched to the baseline **by title** (`cascade_review.go`).
- After `CASCADE_REVIEW`: a changed bead is `pending` with
  `execution_attempts_override = NULL` and stubbed impl; an unchanged bead keeps
  its inherited `succeeded` status and history.
- A rewind/cascade snapshot dir `traces/_bead-{id}-cascade-{n}/` exists for each
  reset bead (`SnapshotBeadFiles` with `kind="cascade"`).

**Files.** `internal/ui/queries.go`, `internal/ui/handlers.go`,
`internal/ui/templates/dashboard.html`, `layout.html` (CSS for a new
`CASCADE_REVIEW` verb / cascade banner).

**Steps.**
1. Add `CascadeBaselineProjectID sql.NullInt64` to `ProjectRow`.
2. When set, render a banner on the dashboard: "Cascade iteration — baseline
   #X, design doc replaced. AUDIT/RECONCILE re-running against the new doc."
   Link the baseline.
3. Add a "Cascade Review" section (populate once the project is past
   `DECOMPOSITION_APPROVED`): per bead, show `changed / reset` vs
   `unchanged (inherited from #X)`. Derive "changed" from: bead status is
   `pending` **and** a `_bead-{id}-cascade-*` snapshot dir exists; "unchanged"
   from status `succeeded` with no cascade snapshot.
4. Handle the zero-diff terminal case: project went straight to `complete` with
   every bead still `succeeded` and no cascade snapshots — say so explicitly.
5. Extend `listRewindSnapshots` (or add a sibling) to also list
   `_bead-{id}-cascade-{n}` dirs, and reuse `handleBeadSnapshot` for them
   (parametrize the `kind` in the path).

**Tests.** Seed a project with `cascade_baseline_project_id` set + a mix of
`pending`/`succeeded` beads + a fake cascade snapshot dir on a temp folder;
assert the section classifies each bead correctly.

---

## W3 — Human guidance log + rewind-from-UI

**Status:** DONE (Milestone B, 2026-08-30).
- Part A: `project.ParseGuidanceLog` exported wrapper + `project.GuidanceNote`;
  `queryBeadDetail` parses the current revision's inner prose and fills
  `GuidanceNotes`; new "Human Guidance Log" section on `bead_detail.html` with
  per-note status styling (`.guidance-note` / `.guidance-inactive`).
- Part B: `project.RewindBead` (exported wrapper over `rewindBead`;
  `rewindResult` → `RewindResult`). Routes `POST /beads/{id}/rewind` and
  `POST /escalations/{id}/rewind` (resolves bead id from the job). Shared
  `s.rewindBead`: 409 on a `succeeded` bead; a bead with no escalated job
  needs `confirm=rewind` (checkbox rendered only when `!HasEscalatedJob`); an
  unknown `--supersedes` maps to 400. "Rewind Bead" form on both the bead
  detail and escalation detail pages. Escalation-page rewind redirects to
  `/escalations`.
- Tests: `TestBeadDetail_RendersGuidanceLog`,
  `TestHandleRewindBead_{EscalatedNoConfirmNeeded,NonEscalatedRequiresConfirm,SucceededRefused}`,
  `TestHandleRewindFromEscalation_{ResolvesBeadID,ProjectScopedJobRejected}`.

**Goal.** (a) Render the guidance log properly on the bead page. (b) Let a
human rewind a bead **with a note** from the browser — the missing half of the
loop-mode human-redirect interaction.

**Why.** `rewind-bead --note/--supersedes` is the sanctioned recovery path for
most per-bead escalations (state-machine doc, escalation points table) and the
main way a human injects direction. Today it is CLI-only, and the note log it
writes is dumped raw inside `full_text` in a `<pre>`.

**Data available.**
- `internal/project/guidance_log.go`: `parseGuidanceLog(fullText) -> (base,
  []guidanceNote{Number, CreatedAt, Status, Text})`. `Status` is `"active"`,
  `"retracted"`, or `"superseded by Note N"`.
- The log lives as a `## Human Guidance Log` section at the end of the current
  revision's `full_text`.
- `internal/project/rewind.go`: `rewindBead(ctx, d, beadID, RewindOptions{Note,
  Supersedes})` — **unexported**. `RewindOptions` is exported; `rewindResult`
  is not.

**Steps.**

*Part A — render (small, do first):*
1. `parseGuidanceLog` is lowercase. Either export a thin
   `project.ParseGuidanceLog` wrapper or move the parse helper somewhere the UI
   can call. Prefer an exported wrapper in `internal/project`.
2. In `queryBeadDetail`, after loading the current revision `full_text`, split
   out the guidance notes and add `GuidanceNotes []GuidanceNote` to
   `beadDetailData`.
3. New "Human Guidance Log" section in `bead_detail.html`: note number,
   timestamp, a status badge (active / superseded / retracted), text. Style
   superseded/retracted muted.

*Part B — rewind action:*
4. Add exported `project.RewindBead(ctx, d, beadID, RewindOptions) (*RewindResult, error)`
   (rename `rewindResult` → `RewindResult` or wrap it). Keep `RunRewindBeadMain`
   calling the same path.
5. New routes:
   `POST /beads/{id}/rewind` (from bead detail) and reuse for
   `POST /escalations/{id}/rewind` (resolve bead id from the job, then same
   handler body). Form fields: `note` (textarea, optional), `supersedes` (int,
   optional).
6. Handler: resolve bead id server-side, call `project.RewindBead`, redirect
   back to the bead page (or `/escalations` when invoked from there). Guard:
   refuse if the bead is `succeeded` (rewind already refuses this internally —
   surface the error as a 409, don't just 500).
7. Add "Rewind bead" forms to `bead_detail.html` and `escalation.html`
   (the escalation page's action row currently has Requeue / Requeue-with-budget
   / Grant-attempts / Close — add Rewind as a distinct, clearly-destructive
   action).

**Resolved.** UI rewind is allowed on escalated **and** stuck (non-escalated,
executing) beads. A non-escalated bead requires a typed confirmation in the
form. `succeeded` beads are still refused (409). See Decisions #2.

**Tests.** Seed a bead with an existing 2-note guidance log in `full_text`;
assert the parse + render. Seed an escalated bead; POST `/beads/{id}/rewind`
with a note; assert `bead_revisions` gained a revision, a snapshot dir was
written, and the note is in the new `full_text`. Assert rewind on a `succeeded`
bead is a 409.

---

## W4 — Decomposition debate (AUDIT ↔ RECONCILE rounds)

**Status:** NOT STARTED

**Goal.** Surface the pre-execution decomposition-review loop: each round's
critique, reconciliation, and outcome, plus round-cap progress.

**Why.** This loop can run several rounds with zero UI visibility — the
dashboard just shows `AUDIT_DECOMPOSITION` / `RECONCILE_DECOMPOSITION` jobs
cycling. Escalation points #1 and #10 land here and the escalation detail page
gives no debate context.

**Data available.**
- `audit_reconcile_rounds(project_id, round_number, critique_text,
  reconciliation, outcome, created_at)`. `outcome` ∈ `converged` /
  `disagreed_continuing` / `escalated` / `redecompose` / `reconcile_rejected`.
- `projects.audit_reconcile_round_cap` — the cap.
- `projects.reconcile_self_resolve` — cautious (default, false) vs permissive.

**Files.** `internal/ui/queries.go` (new `queryAuditReconcileRounds`),
`internal/ui/handlers.go`, `internal/ui/templates/dashboard.html`,
`internal/ui/templates/escalation.html`, `layout.html` (outcome CSS).

**Steps.**
1. `queryAuditReconcileRounds(ctx, d, projectID) []RoundRow`.
2. Add a "Decomposition Review" section to the dashboard, shown whenever the
   project has any rows in `audit_reconcile_rounds` (or is currently in an
   AUDIT/RECONCILE job). Per round: number, outcome badge, collapsible
   critique + reconciliation text. Header line: "round K of cap N ·
   self-resolve: cautious/permissive".
3. On `escalation.html`, when `Job.Verb` is `AUDIT_DECOMPOSITION` /
   `RECONCILE_DECOMPOSITION`, embed the same round history inline.
4. Add CSS classes for the five `outcome` values.

**Tests.** Seed 3 rounds with varied outcomes; assert render + cap line.

---

## W5 — Manifest bootstrap (SURVEY / VERIFY / CERTIFY)

**Status:** NOT STARTED

**Goal.** A view of the once-per-project bootstrap phase and, critically, the
CERTIFY_MANIFEST rejection history that can full-stop a project.

**Why.** Today the only bootstrap signal in the UI is "pipeline has not reached
DECOMPOSE_SPEC yet." Escalation point #2 (5 CERTIFY rejections → project
full_stopped) has no visible trail.

**Data available.**
- `verify_attempts` (`schema.sql:174`) and `certifications` (`schema.sql:189`;
  has `preliminary_decision` + `final_decision`, each `approve`/`reject`) —
  inspect `schema.sql` for the full column list before writing queries.
- Verb order: `SURVEY_SPEC → VERIFY_MANIFEST → CERTIFY_MANIFEST →
  DECOMPOSE_SPEC` (`db.AllVerbs`, state-machine doc §2).
- `handoff_jobs` rows tell you which stage is active/complete.

**Files.** `internal/ui/queries.go`, `internal/ui/handlers.go`,
`internal/ui/templates/dashboard.html`.

**Steps.**
1. Read `schema.sql` for the real `verify_attempts` / `certifications` shape.
2. `queryBootstrapState(ctx, d, projectID)` → per-stage status (from
   `handoff_jobs`) + certification rejection count and reasons.
3. Render a compact 4-stage strip above "Beads" while the project has no beads
   yet (and keep it collapsed-but-available after). Show CERTIFY rejections as
   "N / 5" with the reason text.

**Tests.** Seed a project with a rejected certification row; assert the count
and reason render.

---

## W6 — REFINE_TESTS cycle

**Status:** NOT STARTED

**Goal.** Show the test-first WRITE → CRITIQUE → JUDGE cycles on the bead page,
and distinguish `re_refine` executions from ordinary retries.

**Why.** Bead detail shows executions + revisions but the test-refinement loop
(cap 5 cycles, escalation points #3 and #4) is dark, and `re_refine` (which
bypasses the attempt cap by injecting guidance as a CRITIQUE input) looks
identical to a normal retry in the executions table.

**Data available.**
- `test_refinements(project_id, bead_id, verb, ...)` (`schema.sql:202`; `verb`
  ∈ `REFINE_TESTS_WRITE` / `_CRITIQUE` / `_JUDGE`) and
  `handoff_jobs.refinement_cycle_id` — check `schema.sql` and `refine_tests.go`
  for the exact per-cycle columns (JUDGE verdict, cycle number).
- `adjudications.decision` can be `re_refine` (state-machine doc §3) — currently
  `queryBeadDetail` only surfaces `decision` + `reasoning_text`.
- `refinementCycleCap` = 5.

**Files.** `internal/ui/queries.go` (`queryBeadDetail`),
`internal/ui/templates/bead_detail.html`, `layout.html`
(`re_refine` badge CSS — see W10).

**Steps.**
1. Read `schema.sql` + `refine_tests.go` for the `test_refinements` shape.
2. `queryTestRefinements(ctx, d, beadID)` → per cycle: cycle number, JUDGE
   verdict (approved / revise), timestamp, optionally the critique summary.
3. New "Test Refinement" section on the bead page listing cycles, with "cycle
   K of cap 5".
4. In the execution-history table, tag the execution that followed a
   `re_refine` adjudication (join through `adjudications.decision`).

**Tests.** Seed a bead with 2 refinement cycles + a `re_refine` adjudication;
assert both render.

---

## W7 — Fix: `handleRemoveProject` FK-violates on lineage / cascade rows

**Status:** DONE (Milestone A, 2026-08-30). Two pre-transaction guards in
`handleRemoveProject`: refuse with 400 if the project is still another row's
`lineage_root_id` (excluding its own self-reference) or
`cascade_baseline_project_id`. No null-out path (Decisions #3). Tests:
`TestHandleRemoveProject_{LineageRootWithIterationsBlocked,CascadeBaselineBlocked,StandaloneLineageRootSucceeds}`.

**Goal.** Removing a `full_stopped` / `complete` project must not fail (and
silently roll back) when other rows reference it via the loop-mode FK columns.

**Why.** `handlers.go`'s `steps` list nulls only `recovered_from_project_id`
before `DELETE FROM projects`. `loop-mode` added
`lineage_root_id INTEGER REFERENCES projects(id)` and
`cascade_baseline_project_id INTEGER REFERENCES projects(id)` (`schema.sql:22`,
`:24`), and FK enforcement is on (`db.go:56`,
`_pragma=foreign_keys(1)`). Removing a project that is the lineage root of any
other iteration, or the cascade baseline of another project, fails the final
`DELETE` and rolls back the whole transaction. `saveFixture`'s
`renumberFixtureID` already learned to repoint `lineage_root_id`
(`fixture.go`); this handler didn't.

**Files.** `internal/ui/handlers.go` (`handleRemoveProject`).

**Steps.**
1. **Before** starting the delete transaction, run two guard checks (mirrors the
   existing `status != full_stopped/complete` guard):
   - `SELECT COUNT(*) FROM projects WHERE lineage_root_id = ? AND id != ?` > 0
     → 400 "project #X is the root of a lineage with N later iterations —
     remove those first". (Iteration 1 points at itself; the `id != ?` excludes
     that self-reference.)
   - `SELECT COUNT(*) FROM projects WHERE cascade_baseline_project_id = ?` > 0
     → 400 "project #X is the cascade baseline for N other project(s) — remove
     those first".
   No null-out path — see Decisions #3.
2. Also audit the `steps` list against current `schema.sql` for any *other*
   project-scoped table added since it was written (diff against
   `clone.go` / `removeProject` if one exists in `internal/project`).
3. There is an existing known separate issue —
   `project_remove_project_fk_concurrency` memory — don't conflate; this is the
   static missing-repoint bug, not the concurrency one.

**Tests.** Seed a 2-iteration lineage, full-stop iteration 1, POST
`/projects/1/remove`; assert either a clean 400 with the block message, or (if
null-out chosen) a 303 + iteration 2's `lineage_root_id` is NULL. Seed a cascade
project + its full-stopped baseline; assert removing the baseline works.

---

## W8 — Fix: status CSS enum drift

**Status:** PARTIALLY DONE (Milestone A, 2026-08-30). Added `.status` classes
for `fixture`, `no_write`, `test_reject`, `re_refine`, the five
`audit_reconcile_rounds.outcome` values, and `approve`/`reject`. **Still to do:**
adjudication sub-field badges (`trend`, `bead_spec_fit` values incl.
`not_applicable`) — deferred to land with W11d, which is what will actually
render them. Verbs (`CASCADE_REVIEW` etc.) are rendered as plain text, not
`.status` badges, so no class needed.

**Goal.** Every status/decision/termination value the pipeline can emit renders
with an intentional color.

**Why.** `layout.html` hardcodes status→class. Values with no class today (render
unstyled): `no_write` (termination cause), `redecompose` /
`reconcile_rejected` (audit outcomes), `test_reject` / `re_refine`
(adjudication decisions), `CASCADE_REVIEW`. `beads.status` is now
`pending/executing/succeeded/full_stopped` — no `failed` — reconcile the list
against the CHECK constraints.

**Files.** `internal/ui/templates/layout.html`.

**Steps.**
1. Enumerate every `CHECK (... IN (...))` in `schema.sql` for `status` /
   `decision` / `termination_cause` / `outcome` columns.
2. Add a class per value, grouped by semantic (success-ish green,
   failure-ish red, in-progress blue, neutral grey, attention orange).
3. Add `re_refine` / `test_reject` / `redecompose` / `reconcile_rejected` /
   `no_write` / `CASCADE_REVIEW`.

**Tests.** None needed (CSS); eyeball against a seeded project that has hit each
state, or just grep the templates for `.status ` usages.

---

## W9 — Fix: `queryActiveProject` can show the wrong project

**Status:** DONE (Milestone C, 2026-08-30). `queryActiveProject` now mirrors the
orchestrator's own choice exactly (`queue.go` activeProject: `status='active'
ORDER BY id LIMIT 1`, paused ignored), falling back to the most-recently-updated
`paused` project only when none is active. `queryOtherNonTerminalProjects` feeds
a one-line "Other non-terminal projects" notice on the dashboard so a second
active/paused project can't hide. Tests:
`TestQueryActiveProject_{PrefersActiveOverLowerIDPaused,FallsBackToPausedWhenNoneActive}`.

**Goal.** The dashboard's "current project" is the one the orchestrator is
actually working.

**Why.** `SELECT ... WHERE status IN ('active','paused') ORDER BY id LIMIT 1`.
Loop-mode routinely leaves >1 non-terminal project (a paused iteration next to
an active one; a cascade iteration alongside its still-active predecessor). The
lowest id is not necessarily the live one.

**Files.** `internal/ui/queries.go` (`queryActiveProject`),
`internal/ui/handlers.go`, `dashboard.html`.

**Steps.**
1. Change the selection to: the project owning the most-recently-updated
   `running` job; fall back to most-recently-updated `pending` job; fall back to
   most-recently-updated `active`/`paused` project by `updated_at`.
2. If more than one `active`/`paused` project exists, render a small "other
   non-terminal projects" line under the header so nothing is hidden.
3. Consider folding this into W1 (lineage view already needs multi-project
   awareness).

**Tests.** Seed two active projects where the higher-id one has the running
job; assert it is the one shown.

---

## W10 — Pause reason + `reconcile_self_resolve`

**Status:** DONE (Milestone B, 2026-08-30). `ProjectRow` carries the four pause
columns + the computed `NextJobVerb`/`NextJobBeadID`; `queryActiveProject`
selects them and, when paused, looks up the already-enqueued pending job.
`dashboard.html` renders a pause box under the project header: which knob fired,
what resuming dispatches, and `reconcile self-resolve: cautious/permissive`.
`queryAllProjects` unchanged (the All Projects table doesn't need it). Test:
`TestActiveProject_PausedShowsReasonAndNextJob`. Mirroring these onto the
`GET /projects/{id}` detail page is deferred to Milestone C when that page is
built.

**Goal.** On a paused project, show *why* it paused and what Resume will do.

**Why.** The three pause knobs (`pause_after_reconcile`, `pause_after_verb`,
`pause_after_bead_id`) and `reconcile_self_resolve` mode are invisible. A human
deciding resume-vs-redirect can't see which condition fired or what job is
already queued.

**Data available.**
- `projects.pause_after_reconcile` (bool), `pause_after_verb`
  (nullable string), `pause_after_bead_id` (nullable int),
  `reconcile_self_resolve` (bool).
- At a pause the next job is already `pending` (`resume.go:65` — resume is a
  pure status flip). `project.ResumeProject` returns `nextVerb, nextBeadID`.

**Files.** `internal/ui/queries.go` (`queryActiveProject` — add the pause
columns), `dashboard.html`.

**Steps.**
1. Add the four columns to `ProjectRow`.
2. When `Status == "paused"`, render: the matched condition (derive from which
   knob is set + the current pipeline position), the already-queued next job
   (`SELECT verb, bead_id FROM handoff_jobs WHERE project_id=? AND
   status='pending' ORDER BY id LIMIT 1`), and — near the Resume button — a
   one-liner of what resuming dispatches.
3. Show `reconcile_self_resolve: cautious/permissive` wherever the
   decomposition-review section renders (W4).

**Tests.** Seed a paused project with `pause_after_reconcile=1` and a pending
`EXECUTE_BEAD` job; assert the reason + next-job line render.

---

## W11 — Forensics rendering pass

**Status:** NOT STARTED

Four related sub-items. Do W11a first (unblocks the value of the reports that
already exist).

### W11a — Render report/trace markdown instead of `<pre>`

`bead-report.md` / `project-report.md` are markdown; `report.html` dumps them
raw. **Open decision (see bottom):** add a small markdown dependency
(`github.com/yuin/goldmark`, zero-dep, ~pure Go) vs. a hand-rolled minimal
renderer (headings / code fences / lists / links — enough for these two
generated formats and the trace). Recommend goldmark; it's the standard and the
generated reports use a narrow subset it handles trivially.

Files: `report.html`, `handlers.go` (render to HTML server-side, pass
`template.HTML`).

### W11b — Structured trace view

`internal/trace/parse.go` already produces `ParsedTrace` (turns, run commands,
results) and `trace/findings.go` formats exit-criteria coverage. `trace.html`
ignores all of it and dumps the file.

Steps: in `handleTrace`, also run `trace.Parse(b)` and pass the structured
result; render collapsible turns, command/result pairs, and a per-exit-criterion
pass/fail table. Keep the raw dump available behind a toggle.

### W11c — Compressed history on the bead page

`compressed_history(bead_id, project_id, compressed_text, updated_at)` is the
running per-bead analysis narrative ADJUDICATE actually reads — the most useful
single artifact for "why is this bead stuck" — and it's in no view.

Steps: `queryBeadDetail` already has the bead id; add a
`SELECT compressed_text, updated_at FROM compressed_history WHERE bead_id=?`;
render it as a section on the bead page (markdown per W11a).

### W11d — Adjudication detail panels

`queryBeadDetail` pulls `decision` + `reasoning_text` but the template shows
only `decision` with `reasoning_text` as a title-tooltip. `adjudications` also
has `trend` (`same`/`narrower`/`unrelated`/`not_applicable`), `bead_spec_fit`
(`bead_problem`/`execution_capability_problem`/`not_applicable`),
`attempt_budget_cost`, `monitor_escalation_status`. `decision` itself has six
values (`execute_as_is`/`execute_revised`/`full_stop`/`declare_success`/
`test_reject`/`re_refine`).

Steps: select the extra columns; make each execution row expandable to a panel
with trend / spec-fit / budget-cost / full reasoning.

**Tests.** W11a: assert a `#`-heading in a seeded report file comes out as
`<h1>`. W11c: seed a `compressed_history` row, assert it renders on the bead
page. W11d: seed an adjudication with all fields, assert the panel shows them.

---

## W12 — Escalation detail: the full chain

**Status:** NOT STARTED — largely falls out of W4 + W11

**Goal.** The escalation detail page shows enough to resolve the escalation
without dropping to the CLI or reading raw files.

**Why.** Today it shows only the last attempt's raw output + `validation_result`.

**Steps.**
1. List every failed `handoff_attempts` row for the job (strike number,
   `validation_result`, timestamp) with a link to each attempt's trace where one
   exists.
2. For a bead-scoped escalation: embed the last N executions + their
   adjudications (reuse W11d) and the compressed history (W11c).
3. For AUDIT/RECONCILE: embed the round history (W4).
4. Keep the existing action row; add Rewind (W3).

**Tests.** Seed an escalated bead-scoped job with 2 failed attempts + an
execution + adjudication; assert all appear on the detail page.

---

## W13 — Hygiene

**Status:** IN PROGRESS — htmx vendoring DONE (Milestone A, 2026-08-30); the
rest NOT STARTED.

- **Vendor htmx.** ✅ DONE. `internal/ui/static/htmx-2.0.4.min.js` embedded via
  a new `//go:embed static` FS, served at `/static/` by
  `http.FileServerFS` + a `cacheForever` (immutable, 1yr) wrapper in
  `server.go`. `layout.html` now points at `/static/htmx-2.0.4.min.js`.
  Version is in the filename so the immutable cache is safe across bumps. Test:
  `TestStaticHTMXServedLocally` (also asserts no `unpkg.com` in the layout).
- **`verb_model_assignments` surfacing.** `verb_model_assignments(project_id,
  verb, model)`. When a verb keeps failing, "which model is assigned to it"
  is exactly the missing context. Show it as a small table on the project /
  bead pages, or inline next to a verb in the jobs list.
- **Poll only when live.** `dashboard.html` polls `/hx/status` every 5s
  unconditionally. Stop polling (or back off) when the shown project is
  `complete` / `full_stopped` and no job is `running`/`pending`.
- **FSM-position breadcrumb.** A one-line "where is this project" indicator:
  `bootstrap: CERTIFY 2/5` / `decomposition: round 3/5` / `bead 4/9: EXECUTE
  attempt 2/6` / `complete`. Cheap orientation; compute from `handoff_jobs` +
  the caps.

---

## Sequencing

Milestone-based; each milestone is a sensible stopping point.

### Milestone A — correctness + quick wins (1 session)
- **W7** (remove-project FK bug) — real bug
- **W8** (status CSS drift) — small, bundle with W7
- **W13** htmx vendoring — small, removes an availability risk

### Milestone B — the human-redirect loop (1–2 sessions)
- **W3 Part A** (render guidance log) — small
- **W3 Part B** (rewind-from-UI) — the missing half of loop-mode interaction
- **W10** (pause reason) — pairs naturally with "should I resume or redirect?"

### Milestone C — loop-mode structure — DONE (2026-08-30)
- **`GET /projects/{id}` detail page** — built (`project.html`, `partials.html`
  with shared `beadsTable`/`jobsTable`/`pauseBox`/`projectControls` blocks;
  `GET /projects/{id}/body` for 5s htmx polling). Dashboard links to it.
- **W1** (lineages) — done
- **W9** (correct active-project selection) — done
- **W2** (cascade view) — done

### Milestone D — dark pipeline phases (2 sessions)
- **W4** (decomposition debate)
- **W5** (manifest bootstrap)
- **W6** (REFINE_TESTS cycle)

### Milestone E — forensics depth (1–2 sessions)
- **W11a** (markdown rendering) — do first, unblocks the rest
- **W11b/c/d** (structured trace, compressed history, adjudication panels)
- **W12** (escalation chain — mostly assembly of B/D/E pieces)
- **W13** remainder (model assignments, poll-when-live, FSM breadcrumb)

---

## Decisions (resolved 2026-08-30)

1. **Markdown renderer (W11a).** ✅ Add `github.com/yuin/goldmark` (zero-dep,
   pure Go) as the project's second direct dependency. Use it for the generated
   reports, compressed history, and the trace viewer's prose.
2. **UI rewind scope (W3B).** ✅ Button allowed on escalated **and** stuck
   (non-escalated, executing) beads, matching the CLI. A non-escalated bead
   requires a typed confirmation in the form. Still refuse `succeeded` beads
   (surface as 409).
3. **Lineage-root removal (W7).** ✅ **Block** removal of a project that is still
   the lineage root of later iterations, with the message
   "project #X is the root of a lineage with N later iterations — remove those
   first." Do the same for a project still referenced as a
   `cascade_baseline_project_id`.
4. **Project detail page.** ✅ Add a dedicated `GET /projects/{id}` page,
   introduced in **Milestone C** alongside W1. Dashboard stays a summary; W4/W5/
   W10's project-scoped sections live on the detail page. (W4/W5/W10 below still
   describe rendering "on the dashboard" for the active project — read that as
   "on the project detail page, and mirrored in the dashboard's active-project
   summary" once the detail page exists.)
