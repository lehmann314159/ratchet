-- Ratchet schema.
-- WAL mode and FK enforcement are set by the application on Open, not here,
-- because PRAGMA statements cannot run inside the transaction this schema runs in.

CREATE TABLE IF NOT EXISTS projects (
  id                          INTEGER PRIMARY KEY,
  label                       TEXT    NOT NULL,
  folder_path                 TEXT    NOT NULL,
  design_doc_path             TEXT    NOT NULL,
  status                      TEXT    NOT NULL CHECK (status IN ('active', 'full_stopped', 'complete')),
  recovered_from_project_id   INTEGER REFERENCES projects(id),
  monitor_override_default    TEXT    NOT NULL CHECK (monitor_override_default IN ('honor', 'ignore')),
  execution_budget_default    INTEGER NOT NULL,
  audit_reconcile_round_cap   INTEGER NOT NULL DEFAULT 2,
  created_at                  TIMESTAMP NOT NULL,
  updated_at                  TIMESTAMP NOT NULL
);

-- Application-layer constraints enforced by SetVerbModelAssignment (not expressible as SQL across rows):
--   model(DECOMPOSE_SPEC) == model(RECONCILE_DECOMPOSITION)
--   model(AUDIT_DECOMPOSITION) != model(DECOMPOSE_SPEC)
CREATE TABLE IF NOT EXISTS verb_model_assignments (
  project_id  INTEGER NOT NULL REFERENCES projects(id),
  verb        TEXT    NOT NULL,
  model       TEXT    NOT NULL,
  PRIMARY KEY (project_id, verb)
);

-- current_revision_id is nullable: a bead exists before any revision is written.
-- The circular FK (beads -> bead_revisions -> beads) is safe in SQLite:
-- the FK is nullable and FK checks run at DML time, not schema-creation time.
CREATE TABLE IF NOT EXISTS beads (
  id                  INTEGER PRIMARY KEY,
  project_id          INTEGER NOT NULL REFERENCES projects(id),
  status              TEXT    NOT NULL CHECK (status IN ('pending', 'executing', 'succeeded', 'failed', 'full_stopped')),
  current_revision_id INTEGER REFERENCES bead_revisions(id)
);

CREATE TABLE IF NOT EXISTS bead_revisions (
  id               INTEGER PRIMARY KEY,
  project_id       INTEGER NOT NULL REFERENCES projects(id),
  bead_id          INTEGER NOT NULL REFERENCES beads(id),
  revision_number  INTEGER NOT NULL,
  full_text        TEXT    NOT NULL,
  execution_budget INTEGER NOT NULL,
  monitor_override TEXT    NOT NULL CHECK (monitor_override IN ('honor', 'ignore')),
  created_by_verb  TEXT    NOT NULL CHECK (created_by_verb IN ('DECOMPOSE_SPEC', 'RECONCILE_DECOMPOSITION', 'ADJUDICATE_NEXT_EXECUTION')),
  created_at       TIMESTAMP NOT NULL
);

-- Full text on every round. A 2-round cap means this is not unbounded.
CREATE TABLE IF NOT EXISTS audit_reconcile_rounds (
  id             INTEGER PRIMARY KEY,
  project_id     INTEGER NOT NULL REFERENCES projects(id),
  round_number   INTEGER NOT NULL,
  critique_text  TEXT    NOT NULL,
  reconciliation TEXT    NOT NULL,
  outcome        TEXT    NOT NULL CHECK (outcome IN ('converged', 'disagreed_continuing', 'escalated')),
  created_at     TIMESTAMP NOT NULL
);

-- Three independently-owned facts written by three different actors at three different times.
-- No single INSERT populates all three nullable columns.
CREATE TABLE IF NOT EXISTS executions (
  id                INTEGER PRIMARY KEY,
  project_id        INTEGER NOT NULL REFERENCES projects(id),
  bead_id           INTEGER NOT NULL REFERENCES beads(id),
  bead_revision_id  INTEGER NOT NULL REFERENCES bead_revisions(id),
  trace_path        TEXT    NOT NULL,
  -- 'monitor_force_killed' is written by the orchestrator, not EXECUTE_BEAD,
  -- because EXECUTE_BEAD didn't get to write anything before the hard kill.
  termination_cause TEXT    CHECK (termination_cause IN ('success', 'timeout', 'monitor_terminated', 'monitor_force_killed')),
  monitor_fired     INTEGER,  -- BOOLEAN: 0/1/NULL
  monitor_honored   INTEGER,  -- BOOLEAN: read off bead_revisions.monitor_override at execution time
  started_at        TIMESTAMP NOT NULL,
  ended_at          TIMESTAMP
);

CREATE TABLE IF NOT EXISTS analyses (
  id                      INTEGER PRIMARY KEY,
  project_id              INTEGER NOT NULL REFERENCES projects(id),
  execution_id            INTEGER NOT NULL REFERENCES executions(id),
  mechanical_findings     TEXT    NOT NULL,
  analyzer_interpretation TEXT,
  created_at              TIMESTAMP NOT NULL
);

-- One evolving row per Bead. Bounded by definition (that is COMPRESS_ANALYSIS's job).
CREATE TABLE IF NOT EXISTS compressed_history (
  bead_id         INTEGER PRIMARY KEY REFERENCES beads(id),
  project_id      INTEGER NOT NULL REFERENCES projects(id),
  compressed_text TEXT    NOT NULL,
  updated_at      TIMESTAMP NOT NULL
);

-- trend and bead_spec_fit are separate NOT NULL columns so the consistency check
-- (declared field vs. stated reasoning) is a queryable comparison, not re-parsed from prose.
CREATE TABLE IF NOT EXISTS adjudications (
  id                        INTEGER PRIMARY KEY,
  project_id                INTEGER NOT NULL REFERENCES projects(id),
  bead_id                   INTEGER NOT NULL REFERENCES beads(id),
  execution_id              INTEGER NOT NULL REFERENCES executions(id),
  trend                     TEXT    NOT NULL CHECK (trend IN ('same', 'narrower', 'unrelated', 'not_applicable')),
  bead_spec_fit             TEXT    NOT NULL CHECK (bead_spec_fit IN ('bead_problem', 'execution_capability_problem', 'not_applicable')),
  reasoning_text            TEXT    NOT NULL,
  attempt_budget_cost       REAL    NOT NULL,
  monitor_escalation_status INTEGER NOT NULL,  -- BOOLEAN: 0/1
  decision                  TEXT    NOT NULL CHECK (decision IN ('execute_as_is', 'execute_revised', 'full_stop', 'declare_success')),
  created_at                TIMESTAMP NOT NULL
);

-- AUDIT_DECOMPOSITION and RECONCILE_DECOMPOSITION share the round-cap counter
-- (audit_reconcile_rounds) rather than having rows here.
-- MONITOR_EXECUTION has no row: its outputs have no malformed-output shape to validate.
CREATE TABLE IF NOT EXISTS handoff_jobs (
  id          INTEGER PRIMARY KEY,
  project_id  INTEGER NOT NULL REFERENCES projects(id),
  verb        TEXT    NOT NULL,
  bead_id     INTEGER REFERENCES beads(id),  -- NULL for project-scoped verbs
  status      TEXT    NOT NULL CHECK (status IN ('pending', 'running', 'failed_retry', 'escalated', 'complete')),
  created_at  TIMESTAMP NOT NULL,
  updated_at  TIMESTAMP NOT NULL
);

-- Strike count is never stored. It is COUNT(*) WHERE validation_result != 'valid'.
CREATE TABLE IF NOT EXISTS handoff_attempts (
  id                INTEGER PRIMARY KEY,
  job_id            INTEGER NOT NULL REFERENCES handoff_jobs(id),
  attempt_number    INTEGER NOT NULL,
  raw_output        TEXT,
  validation_result TEXT    NOT NULL,
  created_at        TIMESTAMP NOT NULL
);

-- Indexes for the orchestrator's primary query (oldest pending job per project).
CREATE INDEX IF NOT EXISTS idx_handoff_jobs_pending
  ON handoff_jobs (project_id, status, created_at);

CREATE INDEX IF NOT EXISTS idx_bead_revisions_bead
  ON bead_revisions (bead_id, revision_number);

CREATE INDEX IF NOT EXISTS idx_executions_bead
  ON executions (bead_id);

CREATE INDEX IF NOT EXISTS idx_analyses_execution
  ON analyses (execution_id);

CREATE INDEX IF NOT EXISTS idx_adjudications_bead
  ON adjudications (bead_id);

CREATE INDEX IF NOT EXISTS idx_handoff_attempts_job
  ON handoff_attempts (job_id);
