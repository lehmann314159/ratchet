# ratchet-status

Show the current state of a Ratchet pipeline run: active project, job queue, bead specs, recent log, and latest trace files. Optionally flag anomalies.

## What to do

Run the following queries and commands, then report a compact summary.

### 1. Active project

```bash
sqlite3 /Users/mike/Documents/ratchet-projects/ratchet.db \
  "SELECT id, label, status FROM projects WHERE status='active';"
```

If no active project, report that and stop.

### 2. Job queue (replace PROJECT_ID with the active project's id)

```bash
sqlite3 /Users/mike/Documents/ratchet-projects/ratchet.db \
  "SELECT id, verb, status, updated_at FROM handoff_jobs WHERE project_id=PROJECT_ID ORDER BY id;"
```

### 3. Beads — id, status, and current spec (title + exit_criteria + output_files extracted from full_text JSON)

```bash
sqlite3 /Users/mike/Documents/ratchet-projects/ratchet.db \
  "SELECT b.id, b.status, br.revision_number, br.execution_budget, br.full_text
   FROM beads b JOIN bead_revisions br ON br.id=b.current_revision_id
   WHERE b.project_id=PROJECT_ID ORDER BY b.id;"
```

### 4. Audit/reconcile rounds

```bash
sqlite3 /Users/mike/Documents/ratchet-projects/ratchet.db \
  "SELECT round_number, outcome, critique_text, reconciliation
   FROM audit_reconcile_rounds WHERE project_id=PROJECT_ID ORDER BY id;"
```

### 5. Recent ratchet log

```bash
tail -30 /tmp/ratchet-v7.log
```

### 6. Latest trace files

```bash
ls -lt /Users/mike/Documents/ratchet-projects/png-stego/traces/ | grep "bead-" | head -8
```

Read the most recent 1-2 trace files that belong to the current project's beads (bead IDs 25+). Skip traces from old projects (bead IDs < 25 for project 9).

### 7. Workspace

```bash
ls -la /Users/mike/Documents/ratchet-projects/png-stego/
```

## Reporting format

Report in this order:

**Pipeline state:** `<project label>` · project `<id>` · `<N>` beads · running job: `<verb>` (job `<id>`)

**Beads:**
| id | title | status | rev | exit_criteria |
|----|-------|--------|-----|---------------|
(one row per bead; extract title from full_text JSON if present)

**Job queue:** list each job with verb + status + updated_at, grouped: complete / running / pending / failed

**AUDIT/RECONCILE:** outcome + one-line summary of any critique

**Recent log:** last 5 meaningful lines (skip heartbeat/info noise)

**Trace summary:** for each recently-completed trace, note: orient fired? (ls → read → build sequence present), output_files respected? (no stray files created), exit criterion real or vacuous? (look for "no test files" in go test output), any anomalies (wrong signatures, subdirectory created, wrong package name)

**Anomalies to flag actively:**
- Function signatures in stego.go don't match design doc (`Encode(carrier image.Image, message string) (image.Image, error)` and `Decode(stego image.Image) (string, error)`)
- `go test` exit criterion passed with "no test files" output (vacuous pass)
- Files written outside output_files (stray file creation)
- `stego/` subdirectory created
- `return errors.New("not implemented")` still present in a non-layout bead
- `execution_budget` field set to non-zero inside full_text JSON (DECOMPOSE should not set budgets)
- traces/ directory being read during orient step

## DB schema reference

Key tables and columns (do not guess — use only these):
- `projects`: id, label, status
- `handoff_jobs`: id, project_id, verb, status, created_at, updated_at, bead_id
- `beads`: id, project_id, status, current_revision_id
- `bead_revisions`: id, project_id, bead_id, revision_number, full_text, execution_budget, monitor_override, created_by_verb, created_at
- `audit_reconcile_rounds`: id, project_id, round_number, critique_text, reconciliation, outcome, created_at
- No `completed_at` column exists on any table — use `updated_at`
- No `position` column on beads — ordering is by `id`

## Notes

- Ratchet DB: `/Users/mike/Documents/ratchet-projects/ratchet.db`
- Workspace: `/Users/mike/Documents/ratchet-projects/png-stego/`
- Log file: `/tmp/ratchet-v7.log`
- Project 9 beads are IDs 25–29; earlier bead IDs are from old projects
- COMPRESS passthrough threshold is 3: no model call on attempts 1 and 2
- Beads run concurrently (pipeline dispatches all pending beads simultaneously)
