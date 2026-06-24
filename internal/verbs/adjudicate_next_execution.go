package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

const adjudicateNextExecutionSystemPrompt = `You make a decision after a completed execution attempt.

Two output fields are REQUIRED. For retry and stop decisions, they are checked for internal
consistency against your reasoning. For declare_success, both must be "not_applicable":

  trend: "same"           — the failure mode this attempt is the same as or a recurrence of the previous one
         "narrower"       — the same root area but the failure scope has meaningfully narrowed
         "unrelated"      — a genuinely different failure mode from the previous attempt
         "not_applicable" — use only when decision is "declare_success"

  bead_spec_fit: "bead_problem"                — the Bead specification is wrong, ambiguous, or missing detail
                 "execution_capability_problem" — the spec is correct but execution failed to implement it
                 "not_applicable"              — use only when decision is "declare_success"

If your declared trend or bead_spec_fit contradicts your own reasoning (on retry/stop decisions),
the output is treated as invalid.

decision:
  "execute_as_is"   — retry the Bead without changes
  "execute_revised" — retry with a revised Bead (include revised_bead in your output)
  "full_stop"       — stop; the project must restart from DECOMPOSE_SPEC
  "declare_success" — the Bead's exit criteria are confirmed met by the mechanical findings;
                      no further execution needed. Set trend and bead_spec_fit to "not_applicable".

If decision is "execute_revised", include a revised_bead with all required fields
(execution_budget and monitor_override must be explicitly stated, not inherited silently).

Respond with JSON only, no prose before or after:
{
  "trend": "same" | "narrower" | "unrelated" | "not_applicable",
  "bead_spec_fit": "bead_problem" | "execution_capability_problem" | "not_applicable",
  "reasoning": "<your reasoning — for retry/stop decisions must be consistent with trend and bead_spec_fit>",
  "decision": "execute_as_is" | "execute_revised" | "full_stop" | "declare_success",
  "revised_bead": {
    "title": "...",
    "full_text": "...",
    "execution_budget": <int>,
    "monitor_override": "honor" | "ignore"
  }
}`

// consistencyKeywords maps each bead_spec_fit value to keyword sets.
// If the declared value is present but the reasoning contains none of the
// expected keywords (and does contain counterpart keywords), flag inconsistency.
// This catches the Experiment 5 failure: GLM declared "bead_problem" while
// reasoning described "textbook Runner-capability case".
// checkConsistency validates that the declared bead_spec_fit matches the
// reasoning text. The check targets the concrete failure mode from Experiment 5:
// a model declaring "bead_problem" while its own reasoning described the spec
// as clear and unambiguous ("textbook runner-capability case").
//
// Two-signal check per field:
//   - counterpart phrases: reasoning language that directly contradicts the field
//   - exonerating phrases: reasoning that explicitly clears the "accused" party
//
// Either signal alone is sufficient to flag inconsistency. Keyword matching
// is approximate; the store of record is the adjudications table, where
// a human can review trend/bead_spec_fit against reasoning_text directly.
func checkConsistency(fit, reasoning string) (bool, string) {
	lower := strings.ToLower(reasoning)

	switch fit {
	case "bead_problem":
		// Inconsistent: reasoning uses runner/capability language OR
		// explicitly says the spec is NOT the problem.
		contradict := []string{
			"runner-capability", "runner capability",
			"execution capability", "capability problem", "capability case",
			"execution error", "implementation error",
			// Spec-exonerating phrases (Exp-5 pattern: "despite the spec being unambiguous")
			"spec being unambiguous", "spec is clear", "spec is correct",
			"spec is unambiguous", "despite the spec", "unambiguous spec",
			"clear specification", "specification is clear",
		}
		for _, p := range contradict {
			if strings.Contains(lower, p) {
				return false, fmt.Sprintf(
					"declared bead_spec_fit=%q but reasoning contains contradicting phrase %q",
					fit, p,
				)
			}
		}

	case "execution_capability_problem":
		// Inconsistent: reasoning blames the spec rather than execution.
		contradict := []string{
			"spec problem", "spec is unclear", "spec is ambiguous",
			"specification wrong", "specification is unclear", "specification is ambiguous",
			"bead specification is", "ambiguous requirement", "unclear requirement",
			"missing from the spec", "specification does not",
		}
		for _, p := range contradict {
			if strings.Contains(lower, p) {
				return false, fmt.Sprintf(
					"declared bead_spec_fit=%q but reasoning contains contradicting phrase %q",
					fit, p,
				)
			}
		}
	}
	return true, ""
}

type AdjudicateNextExecution struct{}

func (h *AdjudicateNextExecution) Verb() string { return db.VerbAdjudicateNextExecution }

func (h *AdjudicateNextExecution) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	if !job.BeadID.Valid {
		return "", fmt.Errorf("%s job %d has no bead_id", db.VerbAdjudicateNextExecution, job.ID)
	}
	beadID := job.BeadID.Int64

	// Input 1: current Bead state.
	beads, err := loadCurrentBeads(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	var currentBead *beadState
	for i := range beads {
		if beads[i].BeadID == beadID {
			currentBead = &beads[i]
			break
		}
	}
	if currentBead == nil {
		return "", fmt.Errorf("bead %d not found in project %d", beadID, job.ProjectID)
	}

	// Input 2: revision log.
	revLog, err := loadBeadRevisionLog(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	// Input 3: latest ANALYZE_EXECUTION mechanical_findings (not interpretation).
	analysis, err := loadLatestAnalysis(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	// Input 4: COMPRESS_ANALYSIS compressed history.
	compressedHistory, err := loadCompressedHistory(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	// Compute the diff-signal: which failure categories each revision targeted
	// and the last two executions' termination causes.
	diffSignal, err := buildDiffSignal(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbAdjudicateNextExecution)
	if err != nil {
		return "", err
	}

	userMsg := buildAdjudicateUserMsg(currentBead, revLog, analysis.MechanicalFindings, compressedHistory, diffSignal)
	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: adjudicateNextExecutionSystemPrompt},
		{Role: "user", Content: userMsg},
	}, nil)
}

func buildAdjudicateUserMsg(bead *beadState, revLog []revisionEntry, mechanicalFindings, compressedHistory, diffSignal string) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "## Input 1: Current Bead State\n\nBead ID: %d\n\n%s\n\n", bead.BeadID, bead.FullText)

	sb.WriteString("## Input 2: Bead Revision Log\n\n")
	for _, r := range revLog {
		fmt.Fprintf(&sb, "### Revision %d (created by %s)\n\n%s\n\n", r.RevisionNumber, r.CreatedByVerb, r.FullText)
	}
	sb.WriteString("### Diff Signal\n\n")
	sb.WriteString(diffSignal)
	sb.WriteString("\n\n")

	sb.WriteString("## Input 3: Latest Mechanical Findings\n\n")
	sb.WriteString(mechanicalFindings)
	sb.WriteString("\n\n")

	sb.WriteString("## Input 4: Compressed History\n\n")
	if compressedHistory != "" {
		sb.WriteString(compressedHistory)
	} else {
		sb.WriteString("(none — this is the first attempt)")
	}

	return sb.String()
}

// buildDiffSignal computes the revision diff signal from the architecture:
// "a diff of each revision against the version it replaced, compared against
// the failure category ANALYZE_EXECUTION reports on subsequent attempts."
// Test-ID correspondence is the primary signal.
func buildDiffSignal(ctx context.Context, d *db.DB, beadID int64) (string, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT e.id, e.termination_cause, a.mechanical_findings,
		       e.bead_revision_id, e.ended_at
		FROM executions e
		JOIN analyses a ON a.execution_id = e.id
		WHERE e.bead_id = ?
		ORDER BY e.ended_at`, beadID)
	if err != nil {
		return "(no execution history yet)", nil
	}
	defer rows.Close()

	type execRow struct {
		ExecID, RevID    int64
		TerminationCause string
		Findings         string
		EndedAt          string
	}
	var execs []execRow
	for rows.Next() {
		var r execRow
		if err := rows.Scan(&r.ExecID, &r.TerminationCause, &r.Findings, &r.RevID, &r.EndedAt); err != nil {
			return "", err
		}
		execs = append(execs, r)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(execs) == 0 {
		return "(no execution history yet)", nil
	}

	var sb strings.Builder
	for i, e := range execs {
		fmt.Fprintf(&sb, "Attempt %d (revision %d, ended %s): termination=%s\nFindings: %s\n",
			i+1, e.RevID, e.EndedAt, e.TerminationCause, e.Findings)
		if i > 0 {
			sb.WriteString("(diff against previous revision: see revision log above)\n")
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func (h *AdjudicateNextExecution) Validate(raw string) (string, any) {
	var out AdjudicateNextExecutionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}

	validTrends := map[string]bool{"same": true, "narrower": true, "unrelated": true, "not_applicable": true}
	if !validTrends[out.Trend] {
		return fmt.Sprintf("malformed: trend must be \"same\", \"narrower\", \"unrelated\", or \"not_applicable\", got %q", out.Trend), nil
	}

	validFits := map[string]bool{"bead_problem": true, "execution_capability_problem": true, "not_applicable": true}
	if !validFits[out.BeadSpecFit] {
		return fmt.Sprintf("malformed: bead_spec_fit must be \"bead_problem\", \"execution_capability_problem\", or \"not_applicable\", got %q", out.BeadSpecFit), nil
	}

	if strings.TrimSpace(out.Reasoning) == "" {
		return "malformed: reasoning is empty", nil
	}

	validDecisions := map[string]bool{"execute_as_is": true, "execute_revised": true, "full_stop": true, "declare_success": true}
	if !validDecisions[out.Decision] {
		return fmt.Sprintf("malformed: decision must be \"execute_as_is\", \"execute_revised\", \"full_stop\", or \"declare_success\", got %q", out.Decision), nil
	}

	if out.Decision == "execute_revised" {
		if out.RevisedBead == nil {
			return "malformed: decision is execute_revised but revised_bead is absent", nil
		}
		if out.RevisedBead.ExecutionBudget <= 0 {
			return "malformed: revised_bead execution_budget must be a positive integer", nil
		}
		if out.RevisedBead.MonitorOverride != "honor" && out.RevisedBead.MonitorOverride != "ignore" {
			return fmt.Sprintf("malformed: revised_bead monitor_override must be \"honor\" or \"ignore\", got %q", out.RevisedBead.MonitorOverride), nil
		}
	}

	if out.Decision == "declare_success" {
		// declare_success requires both classification fields to be "not_applicable" —
		// there is no failure to attribute when the bead succeeded.
		if out.Trend != "not_applicable" {
			return fmt.Sprintf("malformed: decision is declare_success but trend is %q — must be \"not_applicable\"", out.Trend), nil
		}
		if out.BeadSpecFit != "not_applicable" {
			return fmt.Sprintf("malformed: decision is declare_success but bead_spec_fit is %q — must be \"not_applicable\"", out.BeadSpecFit), nil
		}
	} else {
		// For retry/stop decisions, "not_applicable" is forbidden and the consistency
		// check applies (zero-strike tolerance — a mismatch is a validation failure).
		if out.Trend == "not_applicable" {
			return "malformed: trend \"not_applicable\" is only valid when decision is \"declare_success\"", nil
		}
		if out.BeadSpecFit == "not_applicable" {
			return "malformed: bead_spec_fit \"not_applicable\" is only valid when decision is \"declare_success\"", nil
		}
		if ok, reason := checkConsistency(out.BeadSpecFit, out.Reasoning); !ok {
			return "malformed: consistency check failed: " + reason, nil
		}
	}

	return "valid", out
}

// Commit writes the adjudications row and enqueues the next action.
// Zero-strike tolerance: Commit is only reached on a valid output.
func (h *AdjudicateNextExecution) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(AdjudicateNextExecutionOutput)
	now := time.Now().UTC().Format(time.RFC3339)
	beadID := job.BeadID.Int64

	// Load the latest execution for metadata.
	var execID int64
	var budgetCost float64
	var monitorEscalated bool
	if err := tx.QueryRowContext(ctx, `
		SELECT e.id,
		       CAST(julianday(e.ended_at) - julianday(e.started_at) AS REAL) * 86400.0,
		       COALESCE(e.monitor_fired, 0)
		FROM executions e
		WHERE e.bead_id = ? AND e.termination_cause IS NOT NULL
		ORDER BY e.ended_at DESC LIMIT 1`, beadID,
	).Scan(&execID, &budgetCost, &monitorEscalated); err != nil {
		return fmt.Errorf("load execution for adjudication: %w", err)
	}

	monitorEscalatedInt := 0
	if monitorEscalated {
		monitorEscalatedInt = 1
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO adjudications
		  (project_id, bead_id, execution_id, trend, bead_spec_fit, reasoning_text,
		   attempt_budget_cost, monitor_escalation_status, decision, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ProjectID, beadID, execID,
		out.Trend, out.BeadSpecFit, out.Reasoning,
		budgetCost, monitorEscalatedInt, out.Decision, now,
	); err != nil {
		return fmt.Errorf("insert adjudication: %w", err)
	}

	switch out.Decision {
	case "execute_as_is":
		_, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbExecuteBead, beadID, now, now)
		return err

	case "execute_revised":
		// Write a new bead_revision for the revised spec.
		var currentRevNum int
		if err := tx.QueryRowContext(ctx, `
			SELECT br.revision_number FROM beads b
			JOIN bead_revisions br ON br.id = b.current_revision_id
			WHERE b.id = ?`, beadID,
		).Scan(&currentRevNum); err != nil {
			return fmt.Errorf("load current revision number: %w", err)
		}

		fullText, _ := json.Marshal(out.RevisedBead)
		res, err := tx.ExecContext(ctx, `
			INSERT INTO bead_revisions
			  (project_id, bead_id, revision_number, full_text,
			   execution_budget, monitor_override, created_by_verb, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			job.ProjectID, beadID, currentRevNum+1, string(fullText),
			out.RevisedBead.ExecutionBudget, out.RevisedBead.MonitorOverride,
			db.VerbAdjudicateNextExecution, now)
		if err != nil {
			return fmt.Errorf("insert revised bead_revision: %w", err)
		}
		revID, _ := res.LastInsertId()

		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbExecuteBead, beadID, now, now)
		return err

	case "full_stop":
		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET status = 'full_stopped' WHERE id = ?`, beadID); err != nil {
			return err
		}
		return h.checkProjectTerminal(ctx, tx, job.ProjectID, "full_stopped", now)

	case "declare_success":
		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET status = 'succeeded' WHERE id = ?`, beadID); err != nil {
			return fmt.Errorf("mark bead succeeded: %w", err)
		}
		return h.checkProjectTerminal(ctx, tx, job.ProjectID, "complete", now)
	}
	return nil
}

// checkProjectTerminal checks whether all beads in the project have reached a
// terminal state ('full_stopped' or 'succeeded'). If so, it marks the project
// with terminalStatus. Called from both the full_stop and declare_success branches.
func (h *AdjudicateNextExecution) checkProjectTerminal(ctx context.Context, tx *sql.Tx, projectID int64, terminalStatus, now string) error {
	var activeBeads int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM beads
		WHERE project_id = ? AND status NOT IN ('full_stopped', 'succeeded')`,
		projectID,
	).Scan(&activeBeads); err != nil {
		return fmt.Errorf("count active beads: %w", err)
	}
	if activeBeads == 0 {
		_, err := tx.ExecContext(ctx,
			`UPDATE projects SET status = ?, updated_at = ? WHERE id = ?`,
			terminalStatus, now, projectID)
		return err
	}
	return nil
}
