package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"ratchet/internal/db"
	"ratchet/internal/project"
)

type ProjectRow struct {
	ID         int64
	Label      string
	Status     string
	FolderPath string
	DesignDoc  string
	CreatedAt  string

	// Pause configuration — only meaningful for the active-project view when
	// Status == "paused". See project.Project for the semantics.
	PauseAfterReconcile  bool
	PauseAfterVerb       sql.NullString
	PauseAfterBeadID     sql.NullInt64
	ReconcileSelfResolve bool

	// Loop-mode lineage / cascade provenance (see project.Project).
	LineageRootID            sql.NullInt64
	IterationNumber          int
	CascadeBaselineProjectID sql.NullInt64

	AuditReconcileRoundCap int

	// NextJobVerb / NextJobBeadID describe the pending handoff_job that was
	// enqueued right before the project paused — what resuming will dispatch.
	// Populated only for a paused project (fillPauseNextJob).
	NextJobVerb   string
	NextJobBeadID sql.NullInt64
}

// IsCascade reports whether this project is a cascade iteration
// (clone-project --design-doc) rather than a fresh or plainly-cloned project.
func (p ProjectRow) IsCascade() bool { return p.CascadeBaselineProjectID.Valid }

// projectColumns is the shared SELECT list for every projects-table query, in
// the exact order scanProjectRow expects.
const projectColumns = `id, label, status, folder_path, design_doc_path, created_at,
	pause_after_reconcile, pause_after_verb, pause_after_bead_id, reconcile_self_resolve,
	lineage_root_id, iteration_number, cascade_baseline_project_id, audit_reconcile_round_cap`

type scanner interface{ Scan(dest ...any) error }

func scanProjectRow(sc scanner) (*ProjectRow, error) {
	p := &ProjectRow{}
	err := sc.Scan(&p.ID, &p.Label, &p.Status, &p.FolderPath, &p.DesignDoc, &p.CreatedAt,
		&p.PauseAfterReconcile, &p.PauseAfterVerb, &p.PauseAfterBeadID, &p.ReconcileSelfResolve,
		&p.LineageRootID, &p.IterationNumber, &p.CascadeBaselineProjectID, &p.AuditReconcileRoundCap)
	return p, err
}

// fillPauseNextJob populates NextJobVerb/NextJobBeadID for a paused project —
// the pending job every pause point enqueues right before pausing, which
// resume-project will dispatch. No-op for a non-paused project.
func fillPauseNextJob(ctx context.Context, d *db.DB, p *ProjectRow) error {
	if p.Status != "paused" {
		return nil
	}
	err := d.QueryRowContext(ctx, `
		SELECT verb, bead_id FROM handoff_jobs
		WHERE project_id = ? AND status = 'pending'
		ORDER BY created_at LIMIT 1`, p.ID,
	).Scan(&p.NextJobVerb, &p.NextJobBeadID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("paused project next job: %w", err)
	}
	return nil
}

type BeadRow struct {
	ID             int64
	Status         string
	Title          string
	Attempts       int
	MaxAttempts    int
	InfraFailures  int    // executions with infra_failure=1 (orphans, SQLITE_BUSY crashes)
	Budget         int    // execution_budget from current revision
	ElapsedSeconds int    // seconds since execution started; 0 if not executing
}

type JobRow struct {
	ID              int64
	Verb            string
	BeadID          sql.NullInt64
	BeadTitle       string
	Status          string
	UpdatedAt       string
	ElapsedFormatted string
}

type EscalatedRow struct {
	ID               int64
	ProjectID        int64
	Verb             string
	BeadID           sql.NullInt64
	BeadTitle        string
	Strikes          int
	ValidationResult string
	RawOutput        sql.NullString
	UpdatedAt        string
	Budget           int // current execution_budget from bead_revisions; 0 if not bead-scoped
}

// queryActiveProject returns the project the dashboard should feature: the one
// the orchestrator is actually working. The orchestrator (queue.go's
// activeProject) picks `status = 'active' ORDER BY id LIMIT 1` and ignores
// 'paused' entirely, so we mirror that exactly. Only when no project is active
// do we fall back to the most-recently-touched paused project — one that's
// waiting on a human. Returns (nil, nil) when there is neither.
//
// The old query included 'paused' in the same ORDER BY id set, so a low-id
// paused iteration could shadow the higher-id active project the orchestrator
// was really running (a real hazard once loop-mode started leaving multiple
// non-terminal projects around).
func queryActiveProject(ctx context.Context, d *db.DB) (*ProjectRow, error) {
	p, err := scanProjectRow(d.QueryRowContext(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE status = 'active' ORDER BY id LIMIT 1`))
	if err == sql.ErrNoRows {
		p, err = scanProjectRow(d.QueryRowContext(ctx,
			`SELECT `+projectColumns+` FROM projects WHERE status = 'paused' ORDER BY updated_at DESC, id DESC LIMIT 1`))
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("active project: %w", err)
	}
	if err := fillPauseNextJob(ctx, d, p); err != nil {
		return nil, err
	}
	return p, nil
}

// queryProjectByID loads one project for the detail page.
func queryProjectByID(ctx context.Context, d *db.DB, id int64) (*ProjectRow, error) {
	p, err := scanProjectRow(d.QueryRowContext(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("project %d: %w", id, err)
	}
	if err := fillPauseNextJob(ctx, d, p); err != nil {
		return nil, err
	}
	return p, nil
}

func queryAllProjects(ctx context.Context, d *db.DB) ([]ProjectRow, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+projectColumns+` FROM projects ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("all projects: %w", err)
	}
	defer rows.Close()
	var out []ProjectRow
	for rows.Next() {
		p, err := scanProjectRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// queryOtherNonTerminalProjects lists active/paused projects other than
// exceptID — surfaced as a one-line notice on the dashboard so a second
// non-terminal project can't hide behind the featured one.
func queryOtherNonTerminalProjects(ctx context.Context, d *db.DB, exceptID int64) ([]ProjectRow, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+projectColumns+`
		FROM projects WHERE status IN ('active', 'paused') AND id != ? ORDER BY id`, exceptID)
	if err != nil {
		return nil, fmt.Errorf("other non-terminal projects: %w", err)
	}
	defer rows.Close()
	var out []ProjectRow
	for rows.Next() {
		p, err := scanProjectRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// LineageGroup is one iteration lineage: every project sharing a
// lineage_root_id, ordered by iteration_number. A standalone project is a
// group of one.
type LineageGroup struct {
	RootID  int64
	Members []ProjectRow
}

// Multi reports whether this lineage has more than one iteration.
func (g LineageGroup) Multi() bool { return len(g.Members) > 1 }

// Label is the group's display name — iteration 1's label.
func (g LineageGroup) Label() string {
	if len(g.Members) == 0 {
		return ""
	}
	return g.Members[0].Label
}

// groupByLineage buckets projects by lineage_root_id (falling back to the
// project's own id when the column is NULL — pre-backfill rows), ordering
// members by iteration_number and groups by their highest project id
// descending, so the most recently created lineage sorts first.
func groupByLineage(projects []ProjectRow) []LineageGroup {
	idx := make(map[int64]int)
	var groups []LineageGroup
	for _, p := range projects {
		root := p.ID
		if p.LineageRootID.Valid {
			root = p.LineageRootID.Int64
		}
		gi, ok := idx[root]
		if !ok {
			idx[root] = len(groups)
			groups = append(groups, LineageGroup{RootID: root})
			gi = len(groups) - 1
		}
		groups[gi].Members = append(groups[gi].Members, p)
	}
	for gi := range groups {
		sort.Slice(groups[gi].Members, func(a, b int) bool {
			return groups[gi].Members[a].IterationNumber < groups[gi].Members[b].IterationNumber
		})
	}
	sort.Slice(groups, func(a, b int) bool {
		return maxProjectID(groups[a].Members) > maxProjectID(groups[b].Members)
	})
	return groups
}

func maxProjectID(ps []ProjectRow) int64 {
	var m int64
	for _, p := range ps {
		if p.ID > m {
			m = p.ID
		}
	}
	return m
}

// CascadeBeadRow describes one bead of a cascade iteration and whether
// CASCADE_REVIEW reset it (its spec diffed against the baseline) or left it
// inherited from the clone.
type CascadeBeadRow struct {
	BeadID int64
	Title  string
	Status string
	Reset  bool // a traces/_bead-{id}-cascade-* snapshot exists
}

// queryCascadeBeads returns every bead of a cascade project with its title and
// status. The caller marks each row's Reset flag from the on-disk cascade
// snapshot dirs (resetBeadForRerun always snapshots before touching a bead, so
// a snapshot's presence is the reliable "was reset" signal).
func queryCascadeBeads(ctx context.Context, d *db.DB, projectID int64) ([]CascadeBeadRow, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT b.id, b.status, COALESCE(json_extract(br.full_text, '$.title'), '')
		FROM beads b
		LEFT JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.project_id = ?
		ORDER BY b.id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("cascade beads: %w", err)
	}
	defer rows.Close()
	var out []CascadeBeadRow
	for rows.Next() {
		var r CascadeBeadRow
		if err := rows.Scan(&r.BeadID, &r.Status, &r.Title); err != nil {
			return nil, err
		}
		if r.Title == "" {
			r.Title = fmt.Sprintf("bead-%d", r.BeadID)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// queryLineageMembers returns every project in the lineage rooted at rootID,
// ordered by iteration_number.
func queryLineageMembers(ctx context.Context, d *db.DB, rootID int64) ([]ProjectRow, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+projectColumns+`
		FROM projects WHERE lineage_root_id = ? ORDER BY iteration_number`, rootID)
	if err != nil {
		return nil, fmt.Errorf("lineage members: %w", err)
	}
	defer rows.Close()
	var out []ProjectRow
	for rows.Next() {
		p, err := scanProjectRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// --- W4: AUDIT/RECONCILE decomposition debate ---

type RoundRow struct {
	RoundNumber   int
	Critique      string
	Reconciliation string
	Outcome       string
	CreatedAt     string
}

func queryAuditReconcileRounds(ctx context.Context, d *db.DB, projectID int64) ([]RoundRow, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT round_number, critique_text, reconciliation, outcome, created_at
		FROM audit_reconcile_rounds WHERE project_id = ? ORDER BY round_number`, projectID)
	if err != nil {
		return nil, fmt.Errorf("audit/reconcile rounds: %w", err)
	}
	defer rows.Close()
	var out []RoundRow
	for rows.Next() {
		var r RoundRow
		if err := rows.Scan(&r.RoundNumber, &r.Critique, &r.Reconciliation, &r.Outcome, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- W5: manifest bootstrap (SURVEY → VERIFY → CERTIFY → DECOMPOSE) ---

type BootstrapStage struct {
	Verb   string
	Status string // "complete" | "running" | "pending" | "failed_retry" | "escalated" | "" (not reached)
}

type VerifyChecks struct {
	AttemptNumber          int
	FilePresence           bool
	NoBehavioralTests      bool
	Compile                bool
	APICheck               bool
	StubPurity             bool
	Violations             string
	CreatedAt              string
}

type CertificationRow struct {
	Preliminary string
	Final       string
	Reasoning   string
	Feedback    string
	CreatedAt   string
}

type BootstrapState struct {
	Stages       []BootstrapStage
	LatestVerify *VerifyChecks
	Certs        []CertificationRow
	RejectCount  int // final_decision='reject' — 5 full-stops the project
}

// HasData reports whether any bootstrap forensics exist yet.
func (b BootstrapState) HasData() bool {
	return b.LatestVerify != nil || len(b.Certs) > 0
}

var bootstrapVerbs = []string{"SURVEY_SPEC", "VERIFY_MANIFEST", "CERTIFY_MANIFEST", "DECOMPOSE_SPEC"}

func queryBootstrapState(ctx context.Context, d *db.DB, projectID int64) (*BootstrapState, error) {
	b := &BootstrapState{}

	for _, verb := range bootstrapVerbs {
		// The most-advanced status any job for this verb has reached. Rank so a
		// completed earlier attempt doesn't mask a later pending retry.
		var status string
		_ = d.QueryRowContext(ctx, `
			SELECT status FROM handoff_jobs
			WHERE project_id = ? AND verb = ?
			ORDER BY CASE status
			  WHEN 'running' THEN 0 WHEN 'escalated' THEN 1 WHEN 'failed_retry' THEN 2
			  WHEN 'pending' THEN 3 WHEN 'complete' THEN 4 ELSE 5 END, id DESC
			LIMIT 1`, projectID, verb).Scan(&status)
		b.Stages = append(b.Stages, BootstrapStage{Verb: verb, Status: status})
	}

	v := &VerifyChecks{}
	var fp, nbt, cp, apc, sp int
	var violations sql.NullString
	err := d.QueryRowContext(ctx, `
		SELECT attempt_number, file_presence_pass, no_behavioral_tests_pass,
		       compile_pass, api_check_pass, stub_purity_pass, violations, created_at
		FROM verify_attempts WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
		projectID,
	).Scan(&v.AttemptNumber, &fp, &nbt, &cp, &apc, &sp, &violations, &v.CreatedAt)
	if err == nil {
		v.FilePresence, v.NoBehavioralTests, v.Compile, v.APICheck, v.StubPurity = fp == 1, nbt == 1, cp == 1, apc == 1, sp == 1
		v.Violations = violations.String
		b.LatestVerify = v
	} else if err != sql.ErrNoRows {
		return nil, fmt.Errorf("latest verify attempt: %w", err)
	}

	rows, err := d.QueryContext(ctx, `
		SELECT preliminary_decision, final_decision,
		       COALESCE(model_reasoning, ''), COALESCE(feedback, ''), created_at
		FROM certifications WHERE project_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("certifications: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c CertificationRow
		if err := rows.Scan(&c.Preliminary, &c.Final, &c.Reasoning, &c.Feedback, &c.CreatedAt); err != nil {
			return nil, err
		}
		if c.Final == "reject" {
			b.RejectCount++
		}
		b.Certs = append(b.Certs, c)
	}
	return b, rows.Err()
}

// --- W6: REFINE_TESTS write→critique→judge cycles ---

type RefinementTurn struct {
	Turn      int
	Verb      string
	Changed   bool
	Summary   string
	Decision  string // JUDGE only: "approved" | "revise"
	CreatedAt string
}

type RefinementCycle struct {
	CycleID int64
	Turns   []RefinementTurn
	Verdict string // the cycle's JUDGE decision, if it has one yet
}

const refinementCycleCap = 5

func queryTestRefinements(ctx context.Context, d *db.DB, beadID int64) ([]RefinementCycle, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT cycle_id, turn, verb, changed, COALESCE(summary, ''), decision, created_at
		FROM test_refinements WHERE bead_id = ? ORDER BY cycle_id, turn`, beadID)
	if err != nil {
		return nil, fmt.Errorf("test refinements: %w", err)
	}
	defer rows.Close()

	var cycles []RefinementCycle
	idx := make(map[int64]int)
	for rows.Next() {
		var t RefinementTurn
		var cid int64
		var changed int
		if err := rows.Scan(&cid, &t.Turn, &t.Verb, &changed, &t.Summary, &t.Decision, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Changed = changed == 1
		ci, ok := idx[cid]
		if !ok {
			idx[cid] = len(cycles)
			cycles = append(cycles, RefinementCycle{CycleID: cid})
			ci = len(cycles) - 1
		}
		cycles[ci].Turns = append(cycles[ci].Turns, t)
		if t.Verb == "REFINE_TESTS_JUDGE" && t.Decision != "" {
			cycles[ci].Verdict = t.Decision
		}
	}
	return cycles, rows.Err()
}

func queryBeads(ctx context.Context, d *db.DB, projectID int64) ([]BeadRow, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT b.id, b.status, COALESCE(br.full_text, '{}'),
		       COALESCE(br.execution_budget, 0),
		       (SELECT COUNT(*) FROM executions e WHERE e.bead_id = b.id AND e.infra_failure = 0 AND e.test_first_attempt = 0),
		       COALESCE(b.execution_attempts_override, p.max_execution_attempts),
		       (SELECT COUNT(*) FROM executions e WHERE e.bead_id = b.id AND e.infra_failure = 1),
		       COALESCE((
		         SELECT CAST(ROUND((julianday('now') - julianday(e2.started_at)) * 86400) AS INTEGER)
		         FROM executions e2
		         WHERE e2.bead_id = b.id AND e2.termination_cause IS NULL
		         ORDER BY e2.started_at DESC LIMIT 1
		       ), 0)
		FROM beads b
		JOIN projects p ON p.id = b.project_id
		LEFT JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.project_id = ?
		ORDER BY b.id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("beads: %w", err)
	}
	defer rows.Close()

	var out []BeadRow
	for rows.Next() {
		var r BeadRow
		var fullText string
		if err := rows.Scan(&r.ID, &r.Status, &fullText, &r.Budget, &r.Attempts, &r.MaxAttempts, &r.InfraFailures, &r.ElapsedSeconds); err != nil {
			return nil, err
		}
		var parsed struct {
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(fullText), &parsed) == nil && parsed.Title != "" {
			r.Title = parsed.Title
		} else {
			r.Title = fmt.Sprintf("bead-%d", r.ID)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func queryRecentJobs(ctx context.Context, d *db.DB, projectID int64) ([]JobRow, error) {
	// Elapsed is summed from handoff_attempts' own created_at/ended_at
	// (actual dispatch-to-completion windows), not hj.created_at/updated_at.
	// The job row's created_at is set at *enqueue* time, which can sit in
	// the pending queue arbitrarily long — most visibly across a
	// pause-after-reconcile/verb/bead gap, where a job can be created
	// minutes before a human even reviews the pause, then sit for however
	// long the review takes before resume-project dispatches it. That
	// queue-wait was previously indistinguishable from real model-call
	// time (connect-four-v3 bead 59, 2026-08-27: REFINE_TESTS_WRITE showed
	// as taking ~76 minutes; the actual call was ~19).
	//
	// handoff_attempts rows are only inserted once an attempt *completes*
	// (dispatch.go/queue.go's commitAttempt-style writes) — there is never a
	// row with ended_at IS NULL representing an in-flight call. So a job
	// still on its first attempt has zero attempt rows and the sum above
	// alone would show 0 the entire time it's actually running (caught live,
	// connect-four-v4 bead RECONCILE_DECOMPOSITION, 2026-08-27 — the very
	// first thing checked after deploying the sum-based fix). For a
	// currently-'running' job, add now() - hj.updated_at: queue.go's
	// dispatch sets status='running' *and* bumps updated_at in the same
	// write, on every dispatch including retries, so it always marks the
	// start of whichever attempt is currently in flight — on top of the sum
	// of whatever earlier attempts already completed and recorded their own
	// real duration.
	// EXECUTE_BEAD is a different beast from every other verb: it never
	// writes to handoff_attempts at all (confirmed live, connect-four-v5
	// bead 71, 2026-08-28 — showed as "-" in the UI, not merely inflated).
	// Its actual timing lives in executions (started_at/ended_at), the same
	// table queryBeadDetail already reads for the bead-detail page. There's
	// no direct FK from a handoff_jobs row to its executions row, but both
	// are created in strict 1:1 lockstep per bead (one EXECUTE_BEAD
	// dispatch, one new executions row), so position matches position: an
	// execution's 0-based rank within its bead (by started_at) is matched
	// against how many earlier EXECUTE_BEAD jobs that bead already had (by
	// id) — the same positional correlation queryBeadDetail's own
	// ROW_NUMBER() OVER (ORDER BY e.started_at) already relies on. This has
	// to be a rank comparison in WHERE, not LIMIT/OFFSET — SQLite rejects a
	// correlated subquery inside LIMIT/OFFSET (caught immediately when
	// verifying this fix against live data before deploying it).
	rows, err := d.QueryContext(ctx, `
		SELECT hj.id, hj.verb, hj.bead_id,
		       COALESCE(json_extract(br.full_text, '$.title'), ''),
		       hj.status, hj.updated_at,
		       CASE WHEN hj.verb = 'EXECUTE_BEAD' THEN (
		           SELECT CAST(ROUND((julianday(COALESCE(e.ended_at, datetime('now'))) - julianday(e.started_at)) * 86400) AS INTEGER)
		           FROM executions e
		           WHERE e.bead_id = hj.bead_id
		             AND (SELECT COUNT(*) FROM executions e2 WHERE e2.bead_id = e.bead_id AND e2.started_at <= e.started_at) - 1
		                 = (SELECT COUNT(*) FROM handoff_jobs hj2
		                    WHERE hj2.bead_id = hj.bead_id AND hj2.verb = 'EXECUTE_BEAD' AND hj2.id < hj.id)
		       ) ELSE (
		           (SELECT CAST(COALESCE(SUM(
		               ROUND((julianday(ha.ended_at) - julianday(ha.created_at)) * 86400)
		           ), 0) AS INTEGER)
		            FROM handoff_attempts ha WHERE ha.job_id = hj.id)
		           + CASE WHEN hj.status = 'running'
		                  THEN CAST(ROUND((julianday('now') - julianday(hj.updated_at)) * 86400) AS INTEGER)
		                  ELSE 0 END
		       ) END
		FROM handoff_jobs hj
		LEFT JOIN beads b ON b.id = hj.bead_id
		LEFT JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE hj.project_id = ?
		ORDER BY hj.id DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("recent jobs: %w", err)
	}
	defer rows.Close()

	var out []JobRow
	for rows.Next() {
		var r JobRow
		var elapsedSecs *int
		if err := rows.Scan(&r.ID, &r.Verb, &r.BeadID, &r.BeadTitle, &r.Status, &r.UpdatedAt, &elapsedSecs); err != nil {
			return nil, err
		}
		if elapsedSecs != nil && *elapsedSecs > 0 {
			r.ElapsedFormatted = formatElapsed(*elapsedSecs)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func formatElapsed(s int) string {
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%ds", s/60, s%60)
}

func queryEscalatedJobs(ctx context.Context, d *db.DB) ([]EscalatedRow, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT hj.id, hj.project_id, hj.verb, hj.bead_id,
		       COALESCE(json_extract(br.full_text, '$.title'), ''),
		       (SELECT COUNT(*) FROM handoff_attempts ha
		        WHERE ha.job_id = hj.id AND ha.validation_result != 'valid') AS strikes,
		       COALESCE(
		         (SELECT ha.validation_result FROM handoff_attempts ha
		          WHERE ha.job_id = hj.id ORDER BY ha.attempt_number DESC LIMIT 1),
		         ''),
		       (SELECT ha.raw_output FROM handoff_attempts ha
		        WHERE ha.job_id = hj.id ORDER BY ha.attempt_number DESC LIMIT 1),
		       hj.updated_at,
		       COALESCE(br.execution_budget, 0)
		FROM handoff_jobs hj
		LEFT JOIN beads b ON b.id = hj.bead_id
		LEFT JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE hj.status = 'escalated'
		ORDER BY hj.updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("escalated jobs: %w", err)
	}
	defer rows.Close()

	var out []EscalatedRow
	for rows.Next() {
		var r EscalatedRow
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Verb, &r.BeadID, &r.BeadTitle,
			&r.Strikes, &r.ValidationResult, &r.RawOutput, &r.UpdatedAt, &r.Budget); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func queryEscalatedJobByID(ctx context.Context, d *db.DB, id int64) (*EscalatedRow, error) {
	r := &EscalatedRow{}
	err := d.QueryRowContext(ctx, `
		SELECT hj.id, hj.project_id, hj.verb, hj.bead_id,
		       COALESCE(json_extract(br.full_text, '$.title'), ''),
		       (SELECT COUNT(*) FROM handoff_attempts ha
		        WHERE ha.job_id = hj.id AND ha.validation_result != 'valid') AS strikes,
		       COALESCE(
		         (SELECT ha.validation_result FROM handoff_attempts ha
		          WHERE ha.job_id = hj.id ORDER BY ha.attempt_number DESC LIMIT 1),
		         ''),
		       (SELECT ha.raw_output FROM handoff_attempts ha
		        WHERE ha.job_id = hj.id ORDER BY ha.attempt_number DESC LIMIT 1),
		       hj.updated_at,
		       COALESCE(br.execution_budget, 0)
		FROM handoff_jobs hj
		LEFT JOIN beads b ON b.id = hj.bead_id
		LEFT JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE hj.id = ?`, id,
	).Scan(&r.ID, &r.ProjectID, &r.Verb, &r.BeadID, &r.BeadTitle,
		&r.Strikes, &r.ValidationResult, &r.RawOutput, &r.UpdatedAt, &r.Budget)
	if err != nil {
		return nil, fmt.Errorf("escalated job %d: %w", id, err)
	}
	return r, nil
}

type ExecutionRow struct {
	ID               int64
	AttemptNum       int
	TerminationCause string
	BudgetSeconds    int
	ElapsedSeconds   int
	MonitorFired     bool
	StartedAt        string
	Decision         string // adjudication decision, empty if none yet
	DecisionReasoning string
	TracePath        string
}

type RevisionRow struct {
	RevisionNumber  int
	ExecutionBudget int
	CreatedByVerb   string
	CreatedAt       string
	FullText        string
}

type beadDetailData struct {
	baseData
	BeadID             int64
	BeadTitle          string
	BeadStatus         string
	HasEscalatedJob    bool
	Executions         []ExecutionRow
	Revisions          []RevisionRow
	RewindSnapshots    []int
	GuidanceNotes      []project.GuidanceNote
	RefinementCycles   []RefinementCycle
	RefinementCycleCap int
}

func queryBeadDetail(ctx context.Context, d *db.DB, beadID int64) (*beadDetailData, error) {
	out := &beadDetailData{BeadID: beadID}

	// Title, status, and the Human Guidance Log from the current revision.
	// bead_revisions.full_text is a JSON blob; the guidance log lives inside
	// its "full_text" prose field (see project.ParseGuidanceLog), not at the
	// top level.
	var fullText string
	_ = d.QueryRowContext(ctx, `
		SELECT COALESCE(br.full_text, '{}'), b.status FROM beads b
		LEFT JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID).Scan(&fullText, &out.BeadStatus)
	var parsed struct {
		Title string `json:"title"`
		Prose string `json:"full_text"`
	}
	if json.Unmarshal([]byte(fullText), &parsed) == nil && parsed.Title != "" {
		out.BeadTitle = parsed.Title
	} else {
		out.BeadTitle = fmt.Sprintf("bead-%d", beadID)
	}
	_, out.GuidanceNotes = project.ParseGuidanceLog(parsed.Prose)

	var escalated int
	_ = d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM handoff_jobs WHERE bead_id = ? AND status = 'escalated'`,
		beadID).Scan(&escalated)
	out.HasEscalatedJob = escalated > 0

	out.RefinementCycleCap = refinementCycleCap
	out.RefinementCycles, _ = queryTestRefinements(ctx, d, beadID)

	// Execution history.
	rows, err := d.QueryContext(ctx, `
		SELECT e.id,
		       ROW_NUMBER() OVER (ORDER BY e.started_at) AS attempt_num,
		       COALESCE(e.termination_cause, 'running'),
		       br.execution_budget,
		       CAST(ROUND((julianday(COALESCE(e.ended_at, 'now')) - julianday(e.started_at)) * 86400) AS INTEGER),
		       COALESCE(e.monitor_fired, 0),
		       e.started_at,
		       COALESCE(adj.decision, ''),
		       COALESCE(adj.reasoning_text, ''),
		       e.trace_path,
		       e.infra_failure
		FROM executions e
		JOIN bead_revisions br ON br.bead_id = e.bead_id
		  AND br.revision_number = (
		    SELECT MAX(br2.revision_number) FROM bead_revisions br2
		    WHERE br2.bead_id = e.bead_id AND br2.created_at <= e.started_at
		  )
		LEFT JOIN adjudications adj ON adj.execution_id = e.id
		WHERE e.bead_id = ?
		ORDER BY e.started_at`, beadID)
	if err != nil {
		return nil, fmt.Errorf("execution history: %w", err)
	}
	defer rows.Close()
	var attemptNum int
	for rows.Next() {
		var r ExecutionRow
		var monitorFired, infraFailure int
		if err := rows.Scan(&r.ID, &attemptNum, &r.TerminationCause,
			&r.BudgetSeconds, &r.ElapsedSeconds, &monitorFired,
			&r.StartedAt, &r.Decision, &r.DecisionReasoning, &r.TracePath, &infraFailure); err != nil {
			return nil, err
		}
		r.AttemptNum = attemptNum
		r.MonitorFired = monitorFired == 1
		// infra_failure executions are recorded with termination_cause='success'
		// as a placeholder (the schema's CHECK constraint has no dedicated
		// "crashed" value) — override the displayed cause so a crash-recovered
		// execution isn't shown as an indistinguishable real success.
		if infraFailure == 1 {
			r.TerminationCause = "infra_failure"
		}
		out.Executions = append(out.Executions, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Revision log.
	revRows, err := d.QueryContext(ctx, `
		SELECT revision_number, execution_budget, created_by_verb, created_at, full_text
		FROM bead_revisions WHERE bead_id = ? ORDER BY revision_number`, beadID)
	if err != nil {
		return nil, fmt.Errorf("revisions: %w", err)
	}
	defer revRows.Close()
	for revRows.Next() {
		var r RevisionRow
		if err := revRows.Scan(&r.RevisionNumber, &r.ExecutionBudget, &r.CreatedByVerb, &r.CreatedAt, &r.FullText); err != nil {
			return nil, err
		}
		out.Revisions = append(out.Revisions, r)
	}
	return out, revRows.Err()
}

func queryEscalatedCount(ctx context.Context, d *db.DB) int {
	var n int
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM handoff_jobs WHERE status = 'escalated'`).Scan(&n)
	return n
}

func queryTracePath(ctx context.Context, d *db.DB, execID int64) (string, error) {
	var path string
	err := d.QueryRowContext(ctx, `SELECT trace_path FROM executions WHERE id = ?`, execID).Scan(&path)
	return path, err
}

func queryBeadProjectFolder(ctx context.Context, d *db.DB, beadID int64) (string, error) {
	var folder string
	err := d.QueryRowContext(ctx, `
		SELECT p.folder_path FROM beads b JOIN projects p ON p.id = b.project_id WHERE b.id = ?`,
		beadID).Scan(&folder)
	return folder, err
}

func queryProjectFolder(ctx context.Context, d *db.DB, projectID int64) (string, error) {
	var folder string
	err := d.QueryRowContext(ctx, `SELECT folder_path FROM projects WHERE id = ?`, projectID).Scan(&folder)
	return folder, err
}
