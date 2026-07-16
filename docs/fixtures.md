# Pause points, fixtures, and cloning

Testing one pipeline stage repeatedly — e.g. bead execution — used to mean
re-running the whole pipeline from scratch every time, including
decomposition. This workflow lets you stop a project at a chosen point,
freeze it as a reusable starting point, and clone that starting point as many
times as you want without ever re-running the stages before it.

Three pieces, meant to be used together:

1. **Pause points** — two general knobs (`pause_after_verb`,
   `pause_after_bead_id`) plus the original `pause_after_reconcile`, all three
   built on the same mechanism.
2. **`save-fixture`** — freeze a paused (or otherwise stopped) project in
   place so the orchestrator will never dispatch it again.
3. **`clone-project`** — deep-copy any project (including a fixture) into a
   fresh, independently runnable project.

See `docs/ratchet_state_machine.md` for the surrounding state machine; this
doc covers the `fixture` status and the `active <-> paused` transitions in
depth.

## 1. Pause points

All three pause knobs share one mechanism: at a fixed set of "forward
progress" branch points, the verb always enqueues its normal next
`handoff_job` first, then checks whether it should pause — and if so, flips
`projects.status` to `'paused'` instead of leaving it `'active'`. The job
sits `'pending'` but inert, since the orchestrator only polls
`status = 'active'` projects (`internal/orchestrator/queue.go`). Because of
this **enqueue-then-gate** convention, resuming is always the same operation
regardless of which knob paused the project: `resume-project` just flips
`status` back to `'active'` — there's no next-job state to reconstruct
(`internal/project/resume.go:65`).

All three knobs are **new-project-only flags** — set at `new-project` time,
not changeable on an already-running project via a separate CLI subcommand.

### `pause_after_reconcile` (original, project-wide)

`--pause-after-reconcile` — pauses once `RECONCILE_DECOMPOSITION` converges,
before bead 1 is ever dispatched. Checked inline in
`internal/verbs/reconcile_decomposition.go:223`.

### `pause_after_verb` (verb granularity)

`--pause-after-verb=VERB` — pauses the next time the named verb's
**forward-progress branch** commits. Only five verbs are gated, each at
exactly one branch (not their retry/reject loops):

| Verb | Gated branch | File:line |
|---|---|---|
| `VERIFY_MANIFEST` | its single branch | `internal/verbs/verify_manifest.go:153` |
| `CERTIFY_MANIFEST` | `commitApprove` only (not `commitReject`'s retry loop) | `internal/verbs/certify_manifest.go:189` |
| `RECONCILE_DECOMPOSITION` | `converged` only | `internal/verbs/reconcile_decomposition.go:223` |
| `ADJUDICATE_NEXT_EXECUTION` | `declare_success` → `REVISE_PENDING` only (not `execute_as_is`/`execute_revised`/`full_stop`/`test_reject`/`re_refine`) | `internal/verbs/adjudicate_next_execution.go:1120` |
| `REFINE_TESTS_JUDGE` | `approved` only (not the `revise` retry) | `internal/verbs/refine_tests.go:707` |

The CLI validates `--pause-after-verb` against exactly this list
(`pausableVerbs` in `internal/project/project.go`); any other verb name is
rejected at `new-project` time.

### `pause_after_bead_id` (bead granularity)

`--pause-after-bead=N` — pauses once bead `N` succeeds and
`REVISE_PENDING` is about to dispatch the *next* pending bead. Checked in
exactly one place, keyed on the *trigger* bead (the one that just succeeded),
after `REVISE_PENDING`'s housekeeping (revising remaining pending specs)
already ran: `internal/verbs/revise_pending.go:229`.

**Known UX gap, accepted deliberately**: bead IDs don't exist until
`DECOMPOSE_SPEC` runs, and they're a global auto-increment across every
project, not scoped per-project. So `--pause-after-bead` is only useful once
you already know a real bead ID — typically from a prior run of the same
design doc. It is not validated against real bead IDs at `new-project` time
(it can't be). This is why the fixture/clone workflow below matters: instead
of guessing a future bead ID, run once, stop wherever you land, and clone
from there.

### Shared helpers

Both `pause_after_verb` and `pause_after_bead_id` are checked through two
small helpers in `internal/verbs/inputs.go`:

- `shouldPauseAfterVerb(ctx, tx, projectID, verb)` (`inputs.go:398`)
- `shouldPauseAfterBead(ctx, tx, projectID, beadID)` (`inputs.go:410`)
- `pauseProject(ctx, tx, projectID, now)` (`inputs.go:423`) — the actual status flip, called by both.

Any future pause site must follow the same enqueue-then-gate convention, or
`resume-project`'s generic status-flip logic breaks for it.

## 2. `save-fixture` — freeze a project in place

```
ratchet save-fixture --db=ratchet.db --project=N [--label="stage descriptor"]
```

Converts a live project into a **fixture**: a frozen, reusable starting point
the orchestrator will never dispatch again. It's an **in-place renumber, not
a copy** — cheap, no row duplication, no folder copy:

- `projects.id` is renumbered to a fresh negative ID, allocated
  **sequentially**: one less than the current lowest (most negative) fixture
  ID, or `-1` if there are no fixtures yet. An earlier version negated the
  source project's own ID directly (`98` → `-98`); that collided in
  practice, since `projects.id` has no `AUTOINCREMENT` and SQLite reuses the
  lowest freed ID, so a later project could land back on the exact ID an
  earlier fixture had already claimed the negation of. Sequential allocation
  makes that collision structurally impossible — the new ID is always
  strictly less than every existing fixture ID by construction — at the cost
  of a fixture's ID no longer directly revealing its source project's
  original positive ID (the label carries that context instead).
- Every project-scoped table's `project_id` FK is renumbered to match
  (`fixtureScopedTables` in `internal/project/fixture.go` — 13 tables, kept
  as the single source of truth for "what counts as project-scoped"; cross-
  checked directly against the live schema, not assumed from a summary). The
  shared FK-cascade logic lives in `renumberFixtureID`, used both by
  `save-fixture` itself and by the one-time migration that moved the two
  original fixtures (`98`-derived, `99`-derived) onto this scheme.
- `projects.status` becomes `'fixture'` — the orchestrator's poll already
  filters `WHERE status = 'active'`, so this alone is what makes a fixture
  mechanically undispatchable; no separate "ignore negative IDs" guard exists
  or is needed anywhere else.
- The label gets a `"fixture: "` prefix; `--label` supplies the stage
  descriptor (e.g. `--label="checkers post-RECONCILE"` → `"fixture: checkers
  post-RECONCILE"`), or falls back to the project's own original label.

**Preconditions**: `--project` must be a positive ID (rejects re-fixturing an
already-negative ID) and the project must have zero `'running'`
`handoff_jobs`. A paused project's inert `'pending'` job does **not** block
saving a fixture — that's exactly the state you're usually saving from.

Implementation: `internal/project/fixture.go`. Uncommitted-transaction-safe:
`PRAGMA foreign_keys = OFF` toggled outside the transaction (SQLite refuses
to toggle it mid-transaction), same pattern as `db.go`'s existing
rename+recreate+copy+drop migrations.

## 3. `clone-project` — deep-copy a project (or fixture) to run repeatedly

```
ratchet clone-project --db=ratchet.db --from=N --label="..." --folder=/path/to/new/folder
```

Makes a **true deep copy** — a fresh folder tree on disk plus a fresh row in
every project-scoped table, every internal foreign key remapped to new IDs.
Unlike `save-fixture`'s renumber, the source is left completely untouched:
the whole point is to run the same starting point repeatedly without
mutating it. Works identically whether `--from` is a positive live project or
a negative fixture — one general capability, not fixture-specific.

**Preconditions**:
- the source project must exist,
- it must have zero `'running'` handoff_jobs (a copied `'running'` row would
  be orphaned in the new project — nothing is actually executing it there),
- `--folder` must not already exist (no default-location guessing).

**What happens**:
1. The source folder tree is copied recursively (`internal/project/clone.go`'s
   `copyDir` — pure Go, `filepath.WalkDir` + the existing `copyFile` helper,
   not a shelled-out `cp -r`). This happens *before* the DB transaction; if
   the transaction later fails, the folder is left in place (not rolled
   back) with an error pointing at it, matching how `restart-project` already
   handles this same failure window.
2. One DB transaction inserts a fresh `projects` row (new ID,
   `status = 'active'` always, regardless of the source's status — even when
   cloning a fixture) and then every dependent table in FK order, building
   old-ID → new-ID remap tables for beads, bead_revisions, executions,
   handoff_jobs, and verify_attempts along the way:
   `verb_model_assignments → beads → bead_revisions → (fix up
   beads.current_revision_id) → audit_reconcile_rounds → executions →
   analyses → compressed_history → adjudications → spec_revisions →
   handoff_jobs → handoff_attempts → verify_attempts → certifications →
   test_refinements`.
3. `executions.trace_path` is rewritten to the new folder — only the
   **folder prefix**, not the filename. The filename (e.g.
   `bead-42-attempt-1.log`) still embeds the *old* bead ID on purpose: that's
   the literal file `copyDir` placed under the new folder, and nothing ever
   parses a bead ID back out of a trace filename (it's always read from the
   `bead_id` column instead).
4. All of the new project's config columns (`monitor_override_default`,
   `pause_after_verb`, `pause_after_bead_id`, etc.) are copied verbatim from
   the source — a true deep copy, not reset to defaults.
   `recovered_from_project_id` is left `NULL` on the clone (that column isn't
   actually populated by `restart-project` either despite the name, so
   `clone-project` doesn't invent new semantics for it).

Implementation: `internal/project/clone.go`. Tests:
`internal/project/clone_test.go` — full row/column verification across every
table (including edge cases: a bead with no revision yet, a `bead_id = NULL`
project-scoped job, a `NULL` `spec_revisions.new_revision_id`), independent
mutability after cloning, a dispatchability round-trip checked against the
orchestrator's actual `claimNextJob` WHERE-clause criteria
(`internal/orchestrator/queue.go`), the running-jobs/existing-folder/
not-found rejections, and cloning from a negative fixture ID.

**Live schema note**: `internal/db/schema.sql`'s `CREATE TABLE` text is not
fully authoritative — `internal/db/db.go`'s `columnMigrations` list adds
columns via `ALTER TABLE` on every DB open (new or old) that were never
folded back into `schema.sql`'s text (as of this writing:
`beads.execution_attempts_override`, `executions.infra_failure`,
`executions.test_first_attempt`). Any future code that reads the schema
should verify against `PRAGMA table_info()` on a real opened DB, not
`schema.sql` alone.

## 4. Worked example

Run a design doc until decomposition passes `RECONCILE_DECOMPOSITION`, save
that as a fixture, then clone it repeatedly to test bead execution over and
over without re-running decomposition each time:

```
# Stop right after decomposition converges.
ratchet new-project --db=ratchet.db --label=checkers \
  --folder=/path/to/checkers --pause-after-verb=RECONCILE_DECOMPOSITION
ratchet start --db=ratchet.db --ollama=...
# ... wait for the project to reach status=paused ...

# Freeze it. Say the project landed at id 98.
ratchet save-fixture --db=ratchet.db --project=98 --label="checkers post-RECONCILE"
# -> fixture saved at the next sequential negative id, e.g. -1 if this is
#    the first fixture, or one less than the current lowest fixture id
#    otherwise — not derived from 98 itself.

# Clone it as many times as you want, each one an independent, mutable copy.
ratchet clone-project --db=ratchet.db --from=-1 --label=checkers-try-1 --folder=/path/to/try-1
ratchet clone-project --db=ratchet.db --from=-1 --label=checkers-try-2 --folder=/path/to/try-2
# Each clone comes up status=active with its own copy of every bead spec,
# ready for the orchestrator to dispatch bead execution immediately —
# decomposition never re-runs.
```

## 5. What's not done

`docs/fixtures.md` is task 23, the last item of the original 12-task
breakdown (`pause_after_verb`/`pause_after_bead_id`, `save-fixture`,
`clone-project`, this doc). Nothing else is queued on top of this workflow
unless new gaps surface in use.
