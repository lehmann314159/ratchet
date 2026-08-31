package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

// reconcileRejectCap bounds how many times RECONCILE_DECOMPOSITION's own
// proposed fix will be mechanically rejected and retried after
// forwardFileReferenceChecks finds a bead-ordering violation in it, before
// escalating for human review. Mirrors decomposeRedecomposeCap's shape
// (decompose_spec.go) but escalates the job rather than full-stopping the
// project: by this stage the decomposition already passed the same check
// once (at DECOMPOSE_SPEC commit); only this specific RECONCILE attempt is
// broken, so there's no reason to discard the whole decomposition over it.
const reconcileRejectCap = 3

// ReconcileDecomposition stores critique context between Run and Commit so
// Commit can write the round row without a second in-transaction query.
// Safe because the orchestrator runs one job at a time.
type ReconcileDecomposition struct {
	lastCritique    string
	lastRoundsSoFar int
	lastBeads       []beadState
	knownTitles     map[string]bool
	budgetDefault   int
	folderPath      string
	designDoc       string
}

func (h *ReconcileDecomposition) Verb() string { return db.VerbReconcileDecomposition }

func (h *ReconcileDecomposition) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	doc, err := loadDesignDoc(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	beads, err := loadCurrentBeads(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	critique, roundsSoFar, err := latestAuditCritique(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	history, err := loadDebateHistory(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	rejectFeedback, err := latestReconcileRejectFeedback(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbReconcileDecomposition)
	if err != nil {
		return "", err
	}
	project, err := loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}

	// Cache for Validate/Commit (single-goroutine orchestrator; no race).
	h.lastCritique = critique
	h.lastRoundsSoFar = roundsSoFar
	h.lastBeads = beads
	h.knownTitles = make(map[string]bool, len(beads))
	for _, b := range beads {
		h.knownTitles[b.Title] = true
	}
	h.budgetDefault = project.ExecutionBudgetDefault
	h.folderPath = project.FolderPath
	h.designDoc = doc

	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: reconcileDecompositionSystemPrompt(detectLang(project.FolderPath, beadOutputFiles(beads)), project.ReconcileSelfResolve)},
		{Role: "user", Content: buildReconcileUserMsg(doc, beads, history, critique, rejectFeedback)},
	}, &ollama.Options{Format: ReconcileDecompositionSchema, Think: disableThink()})
}

// latestReconcileRejectFeedback returns the critique_text of the most recent
// 'reconcile_rejected' audit_reconcile_rounds row for projectID, or "" if
// RECONCILE's own proposed fix has never been mechanically rejected. Mirrors
// decompose_spec.go's latestRedecomposeFeedback.
func latestReconcileRejectFeedback(ctx context.Context, d *db.DB, projectID int64) (string, error) {
	var text string
	err := d.QueryRowContext(ctx,
		`SELECT critique_text FROM audit_reconcile_rounds
		 WHERE project_id = ? AND outcome = 'reconcile_rejected'
		 ORDER BY id DESC LIMIT 1`, projectID,
	).Scan(&text)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load reconcile reject feedback: %w", err)
	}
	return text, nil
}

// buildReconcileUserMsg constructs the user message for RECONCILE_DECOMPOSITION.
// When previous debate rounds are present (round 2+), they are included so
// the model can see what was already argued before responding to the new critique.
// rejectFeedback is non-empty only immediately after this project's own prior
// RECONCILE attempt was mechanically rejected (see commitReject) — surfaced
// first and separately from the debate history, since it's feedback on
// RECONCILE's own output, not another round of AUDIT's critique.
func buildReconcileUserMsg(doc string, beads []beadState, history []debateRound, critique, rejectFeedback string) string {
	var sb strings.Builder
	if rejectFeedback != "" {
		sb.WriteString("## Your Previous Fix Attempt Was Rejected\n\n")
		sb.WriteString("Your last response had a structural problem that would make a bead unable to pass ")
		sb.WriteString("(a bead-ordering violation, an exit criterion inconsistent with its own bead, or a ")
		sb.WriteString("required test function with no supporting prose). Fix this in your new response:\n\n")
		sb.WriteString(rejectFeedback)
		sb.WriteString("\n\n")
	}
	sb.WriteString("## Design Document\n\n")
	sb.WriteString(doc)
	sb.WriteString("\n\n## Current Decomposition\n\n")
	for _, b := range beads {
		fmt.Fprintf(&sb, "### %s\n\n%s\n\n", b.Title, b.FullText)
	}

	if len(history) > 0 {
		sb.WriteString("## Previous Debate History\n\n")
		for _, r := range history {
			fmt.Fprintf(&sb, "### Round %d (outcome: %s)\n\n", r.RoundNumber, r.Outcome)
			sb.WriteString("**Audit Critique:**\n\n")
			sb.WriteString(r.CritiqueText)
			sb.WriteString("\n\n**Reconcile Response:**\n\n")
			sb.WriteString(formatReconcileResponses(r.Reconciliation))
			sb.WriteString("\n\n")
		}
	}

	sb.WriteString("## Current Critique\n\n")
	sb.WriteString(critique)
	return sb.String()
}

// formatReconcileResponses renders a stored ReconcileDecompositionOutput JSON
// as a human-readable bullet list for inclusion in the debate history.
// Falls back to the raw string if parsing fails.
func formatReconcileResponses(reconciliationJSON string) string {
	var out ReconcileDecompositionOutput
	if err := json.Unmarshal([]byte(reconciliationJSON), &out); err != nil {
		return reconciliationJSON
	}
	var sb strings.Builder
	for _, r := range out.Responses {
		action := strings.ToUpper(strings.ReplaceAll(r.Action, "_", " "))
		fmt.Fprintf(&sb, "- %s: %s — %s\n", r.BeadTitle, action, r.Reason)
	}
	return sb.String()
}

func (h *ReconcileDecomposition) Validate(raw string) (string, any) {
	var out ReconcileDecompositionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if len(out.Responses) == 0 {
		return "malformed: responses array is empty", nil
	}
	logSchemaReasoning(db.VerbReconcileDecomposition, out.Reasoning, "responses", len(out.Responses))
	for i, r := range out.Responses {
		if r.Action != "agree_and_fix" && r.Action != "disagree" {
			return fmt.Sprintf("malformed: responses[%d] action must be \"agree_and_fix\" or \"disagree\", got %q", i, r.Action), nil
		}
		if r.Action == "agree_and_fix" && r.UpdatedBead == nil {
			return fmt.Sprintf("malformed: responses[%d] action is agree_and_fix but updated_bead is absent", i), nil
		}
		if r.Action == "agree_and_fix" && !h.knownTitles[r.UpdatedBead.Title] {
			return fmt.Sprintf("malformed: responses[%d] updated_bead.title %q does not match any current bead title — do not rename a bead when fixing it", i, r.UpdatedBead.Title), nil
		}
		if r.Action == "disagree" && strings.TrimSpace(r.Reason) == "" {
			return fmt.Sprintf("malformed: responses[%d] action is disagree but reason is empty", i), nil
		}
		if r.AlreadyAddressed && r.Action != "disagree" {
			return fmt.Sprintf("malformed: responses[%d] already_addressed is true but action is %q, not disagree", i, r.Action), nil
		}
	}
	return "valid", out
}

// Commit writes the audit_reconcile_rounds row, applies any agree_and_fix
// updates to bead_revisions, and enqueues the next job.
//
// Convergence comparator (mechanical, non-verb per the architecture): all
// agree_and_fix → converged; any disagree → continue the loop, or escalate
// if the round cap is reached. RECONCILE is explicitly not given authority
// to declare convergence itself — the comparator is this code, not a model.
//
// One conditional exception, gated by the project's reconcile_self_resolve
// setting: when true, a disagree response with AlreadyAddressed set does not
// count against convergence — RECONCILE is self-certifying, in the same
// single judgment call that produced the disagreement, that this is a finding
// it already disputed in an earlier round with no new argument from AUDIT.
// This used to be inferred mechanically by comparing finding text across
// rounds (isRepeatDisagreement), which was fragile to AUDIT paraphrasing an
// already-conceded finding (project 105/fractal-smoke-2: AUDIT reworded a
// finding to acknowledge it was fixed but still listed it, and the
// text-comparison missed the paraphrase, forcing an unnecessary escalation).
// Asking RECONCILE to state the judgment directly and reading it mechanically
// is both simpler and more robust than inferring it from a text-similarity
// proxy. This does not apply when AUDIT raises anything new or restates a
// finding with a new argument — RECONCILE is expected to leave
// AlreadyAddressed false in that case, which still follows the normal
// continue-or-escalate path below.
//
// reconcile_self_resolve defaults to false (cautious): AlreadyAddressed is
// never honored, so every live disagreement rides the round cap to a real
// human escalation. This does reintroduce the exact false-escalation risk
// the mechanism above was built to fix — a paraphrased-but-already-settled
// finding will ride the cap too — but under the loop-mode philosophy that's
// an acceptable, cheap-to-resolve cost (a human glances at it and closes it
// out) rather than the correctness risk of letting RECONCILE be the sole
// judge of its own rebuttal in a genuinely live disagreement.
func (h *ReconcileDecomposition) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(ReconcileDecompositionOutput)
	now := time.Now().UTC().Format(time.RFC3339)

	// Re-run the same mechanical ordering check DECOMPOSE_SPEC runs on its own
	// output (forwardFileReferenceChecks), this time against the *proposed*
	// decomposition RECONCILE's agree_and_fix edits would produce if
	// committed. Nothing else ever re-checks this after DECOMPOSE_SPEC's own
	// commit, so RECONCILE moving a file's ownership between beads without
	// also fixing dispatch order previously went uncaught until AUDIT (or a
	// real bead execution) discovered the symptom rounds later.
	// Source-side gate: bead ordering + exit-criteria/prose/output_files
	// consistency (including the differential check that RECONCILE didn't add a
	// required test function without prose for it — the exprvm-web bead-135
	// TestHandlerRuntime orphan), checked against what the mechanical-repair
	// pass would produce. Reject-and-retry via commitReject.
	before := make(map[string]ParsedBead, len(h.lastBeads))
	for _, b := range h.lastBeads {
		before[b.Title] = ParsedBead{Title: b.Title, FullText: b.FullText, OutputFiles: b.OutputFiles, ExitCriteria: b.ExitCriteria}
	}
	lang := detectLang(h.folderPath, beadOutputFiles(h.lastBeads))
	if violations := beadConsistencyViolations(lang, mergeProposedBeads(h.lastBeads, out), before); len(violations) > 0 {
		return h.commitReject(ctx, tx, job, out, violations, now)
	}

	var roundCap int
	var reconcileSelfResolve bool
	if err := tx.QueryRowContext(ctx,
		`SELECT audit_reconcile_round_cap, reconcile_self_resolve FROM projects WHERE id = ?`,
		job.ProjectID,
	).Scan(&roundCap, &reconcileSelfResolve); err != nil {
		return fmt.Errorf("load round cap: %w", err)
	}

	nextRound := h.lastRoundsSoFar + 1
	hasDisagree := false
	allDisagreesAreRepeats := true
	for _, r := range out.Responses {
		if r.Action == "disagree" {
			hasDisagree = true
			if !reconcileSelfResolve || !r.AlreadyAddressed {
				allDisagreesAreRepeats = false
			}
		}
	}

	outcome := "converged"
	if hasDisagree && !allDisagreesAreRepeats {
		if nextRound >= roundCap {
			outcome = "escalated"
		} else {
			outcome = "disagreed_continuing"
		}
	}

	roundNumber, err := nextRoundNumber(ctx, tx, job.ProjectID)
	if err != nil {
		return err
	}

	reconciliationJSON, _ := json.Marshal(out)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_reconcile_rounds
		  (project_id, round_number, critique_text, reconciliation, outcome, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		job.ProjectID, roundNumber, h.lastCritique, string(reconciliationJSON), outcome, now,
	); err != nil {
		return fmt.Errorf("insert audit_reconcile_round: %w", err)
	}

	if err := h.applyFixes(ctx, tx, job.ProjectID, out, now); err != nil {
		return err
	}

	switch outcome {
	case "converged":
		return enqueueDecompositionApproved(ctx, tx, job.ProjectID, now)
	case "disagreed_continuing":
		_, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, NULL, 'pending', ?, ?)`,
			job.ProjectID, db.VerbAuditDecomposition, now, now)
		return err
	case "escalated":
		// Mark the current job escalated; the orchestrator notifies Mike.
		_, err := tx.ExecContext(ctx,
			`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`,
			now, job.ID)
		return err
	}
	return nil
}

func (h *ReconcileDecomposition) applyFixes(ctx context.Context, tx *sql.Tx, projectID int64, out ReconcileDecompositionOutput, now string) error {
	// Detect language once before the loop. Collect all output_files from the
	// updated beads so detectLang can fall back to extension scanning when the
	// layout bead has not yet run and go.mod / equivalents do not exist yet.
	var allOutputFiles []string
	for _, r := range out.Responses {
		if r.UpdatedBead != nil {
			allOutputFiles = append(allOutputFiles, r.UpdatedBead.OutputFiles...)
		}
	}
	lang := detectLang(h.folderPath, allOutputFiles)
	pins := extractDecompositionNotesPins(h.designDoc)

	// Deduplicate: multiple findings may all request updates to the same bead
	// (e.g. three findings about missing test files each produce an updated_bead
	// for "layout"). Preserve insertion order; last response per title wins.
	order := []string{}
	byTitle := map[string]*ReconcileResponse{}
	for i := range out.Responses {
		r := &out.Responses[i]
		if r.Action != "agree_and_fix" || r.UpdatedBead == nil {
			continue
		}
		title := r.UpdatedBead.Title
		if _, seen := byTitle[title]; !seen {
			order = append(order, title)
		}
		byTitle[title] = r
	}

	for _, title := range order {
		r := byTitle[title]

		var beadID int64
		// Use r.UpdatedBead.Title (not r.BeadTitle) to find the bead to update.
		// r.BeadTitle names the bead cited in the finding; the model sometimes
		// sets it to the problematic bead rather than the bead being fixed, which
		// are not always the same (e.g. a finding about "game-state" whose fix
		// is to update "layout").
		if err := tx.QueryRowContext(ctx, `
			SELECT b.id
			FROM beads b
			JOIN bead_revisions br ON br.id = b.current_revision_id
			WHERE b.project_id = ?
			  AND json_extract(br.full_text, '$.title') = ?`,
			projectID, title,
		).Scan(&beadID); err != nil {
			return fmt.Errorf("find bead %q for fix: %w", title, err)
		}
		// Bead-wide max, not current revision + 1 — keeps revision numbering
		// collision-free the same way as the ADJUDICATE/REVISE_PENDING insert sites.
		var maxRevNum int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(revision_number), 0) FROM bead_revisions WHERE bead_id = ?`, beadID,
		).Scan(&maxRevNum); err != nil {
			return fmt.Errorf("load max revision number for bead %q: %w", title, err)
		}

		applyMechanicalBeadFixes(lang, r.UpdatedBead)
		injectDecompositionNotesPin(r.UpdatedBead, pins)

		fullText, _ := json.Marshal(r.UpdatedBead)
		res, err := tx.ExecContext(ctx, `
			INSERT INTO bead_revisions
			  (project_id, bead_id, revision_number, full_text,
			   execution_budget, monitor_override, created_by_verb, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			projectID, beadID, maxRevNum+1, string(fullText),
			h.budgetDefault, r.UpdatedBead.MonitorOverride,
			db.VerbReconcileDecomposition, now)
		if err != nil {
			return fmt.Errorf("insert revision for bead %q: %w", title, err)
		}
		revID, _ := res.LastInsertId()

		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
			return fmt.Errorf("update bead %q current_revision_id: %w", title, err)
		}
	}
	return nil
}

// mergeProposedBeads builds the full decomposition RECONCILE's response would
// produce if committed: current beads in dispatch order, with each
// agree_and_fix response's updated_bead substituted in by title. Used to
// mechanically re-check the *proposed whole* decomposition, not just the
// beads this round touched — the violation can involve a bead the round
// never touched (e.g. an earlier bead whose full_text references a file
// whose ownership only moved onto a different, later bead this round, as
// happened with checkers-v9's "templates"/"http-handlers" pair).
func mergeProposedBeads(current []beadState, out ReconcileDecompositionOutput) []ParsedBead {
	updates := make(map[string]*ParsedBead, len(out.Responses))
	for i := range out.Responses {
		r := &out.Responses[i]
		if r.Action == "agree_and_fix" && r.UpdatedBead != nil {
			updates[r.UpdatedBead.Title] = r.UpdatedBead
		}
	}
	merged := make([]ParsedBead, len(current))
	for i, b := range current {
		if u, ok := updates[b.Title]; ok {
			merged[i] = *u
			continue
		}
		merged[i] = ParsedBead{
			Title:           b.Title,
			FullText:        b.FullText,
			ExecutionBudget: b.ExecutionBudget,
			MonitorOverride: b.MonitorOverride,
			OutputFiles:     b.OutputFiles,
			ExitCriteria:    b.ExitCriteria,
		}
	}
	return merged
}

// commitReject rejects a RECONCILE_DECOMPOSITION response whose own
// agree_and_fix edits would reintroduce a bead-ordering violation: none of
// the proposed bead_revisions are written, a 'reconcile_rejected'
// audit_reconcile_rounds row records the violations (read back by
// latestReconcileRejectFeedback on the next attempt, and rendered into the
// next attempt's prompt), and either another RECONCILE_DECOMPOSITION job is
// enqueued or, once reconcileRejectCap is reached, this job is escalated for
// human review — mirroring DecomposeSpec.commitRedecompose's reject-then-
// retry-then-give-up shape, but escalating the job rather than full-stopping
// the project (see reconcileRejectCap's doc comment for why).
func (h *ReconcileDecomposition) commitReject(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, out ReconcileDecompositionOutput, violations []string, now string) error {
	var attemptCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_reconcile_rounds WHERE project_id = ? AND outcome = 'reconcile_rejected'`,
		job.ProjectID,
	).Scan(&attemptCount); err != nil {
		return fmt.Errorf("count reconcile_rejected attempts: %w", err)
	}
	attemptCount++

	roundNumber, err := nextRoundNumber(ctx, tx, job.ProjectID)
	if err != nil {
		return err
	}

	critique := "Structural problems in your proposed fix (mechanically detected — not a model judgment call). " +
		"Fix these and resubmit:\n- " +
		strings.Join(violations, "\n- ")
	reconciliationJSON, _ := json.Marshal(out)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_reconcile_rounds (project_id, round_number, critique_text, reconciliation, outcome, created_at)
		VALUES (?, ?, ?, ?, 'reconcile_rejected', ?)`,
		job.ProjectID, roundNumber, critique, string(reconciliationJSON), now,
	); err != nil {
		return fmt.Errorf("insert reconcile_rejected round: %w", err)
	}

	if attemptCount >= reconcileRejectCap {
		slog.Error("ESCALATION — RECONCILE_DECOMPOSITION: bead-ordering violations persisted in its own fix after cap",
			"project_id", job.ProjectID, "cap", reconcileRejectCap, "violations", violations)
		_, err := tx.ExecContext(ctx,
			`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`,
			now, job.ID)
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'pending', ?, ?)`,
		job.ProjectID, db.VerbReconcileDecomposition, now, now)
	if err != nil {
		return fmt.Errorf("enqueue retry %s: %w", db.VerbReconcileDecomposition, err)
	}
	return nil
}
