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

type AuditDecomposition struct{}

func (h *AuditDecomposition) Verb() string { return db.VerbAuditDecomposition }

func (h *AuditDecomposition) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	doc, err := loadDesignDoc(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	beads, err := loadCurrentBeads(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	history, err := loadDebateHistory(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbAuditDecomposition)
	if err != nil {
		return "", err
	}

	project, err := loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	userMsg := buildAuditUserMsg(doc, beads, history)
	raw, err := oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: auditDecompositionSystemPrompt(detectLang(project.FolderPath, beadOutputFiles(beads)))},
		{Role: "user", Content: userMsg},
	}, nil)
	if err != nil {
		return "", err
	}
	return injectMechanicalFindings(raw, project.FolderPath, beads), nil
}

func buildAuditUserMsg(doc string, beads []beadState, history []debateRound) string {
	var sb strings.Builder
	sb.WriteString("## Design Document\n\n")
	sb.WriteString(doc)
	sb.WriteString("\n\n## Decomposition\n\n")
	for i, b := range beads {
		position := fmt.Sprintf("Bead %d", i+1)
		fmt.Fprintf(&sb, "### %s — %s\n\n%s\n\n", position, b.Title, b.FullText)
		if len(b.OutputFiles) > 0 {
			fmt.Fprintf(&sb, "**Output files:** %s\n\n", strings.Join(b.OutputFiles, ", "))
		}
		if len(b.ExitCriteria) > 0 {
			sb.WriteString("**Exit criteria:**\n")
			for _, c := range b.ExitCriteria {
				fmt.Fprintf(&sb, "- %s\n", c)
			}
			sb.WriteString("\n")
		}
	}

	if len(history) > 0 {
		sb.WriteString("## Previous Debate History\n\n")
		sb.WriteString("You raised these findings in earlier rounds and RECONCILE already responded. ")
		sb.WriteString("Do not re-raise a finding a prior round disputed unless the decomposition shown above still ")
		sb.WriteString("has the disputed defect — check the current bead text/output_files/exit_criteria before repeating a claim.\n\n")
		for _, r := range history {
			fmt.Fprintf(&sb, "### Round %d (outcome: %s)\n\n", r.RoundNumber, r.Outcome)
			sb.WriteString("**Your Critique:**\n\n")
			sb.WriteString(r.CritiqueText)
			sb.WriteString("\n\n**Reconcile Response:**\n\n")
			sb.WriteString(formatReconcileResponses(r.Reconciliation))
			sb.WriteString("\n\n")
		}
	}

	return sb.String()
}

func (h *AuditDecomposition) Validate(raw string) (string, any) {
	var out AuditDecompositionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if out.OverallVerdict != "no_issues" && out.OverallVerdict != "issues_found" {
		return fmt.Sprintf("malformed: overall_verdict must be \"no_issues\" or \"issues_found\", got %q", out.OverallVerdict), nil
	}
	if out.OverallVerdict == "issues_found" && len(out.Findings) == 0 {
		return "malformed: overall_verdict is \"issues_found\" but findings array is empty", nil
	}
	for i, f := range out.Findings {
		if f.BeadTitle == "" {
			return fmt.Sprintf("malformed: findings[%d] missing bead_title", i), nil
		}
		if f.Issue == "" {
			return fmt.Sprintf("malformed: findings[%d] missing issue", i), nil
		}
	}
	return "valid", out
}

// Commit either skips straight to execution (no_issues) or enqueues
// RECONCILE_DECOMPOSITION (issues_found). The critique text is stored in
// handoff_attempts (via the dispatch layer) and read by RECONCILE's Run;
// audit_reconcile_rounds is written atomically by RECONCILE's Commit once
// both the critique and reconciliation are available.
func (h *AuditDecomposition) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	now := time.Now().UTC().Format(time.RFC3339)
	out := parsed.(AuditDecompositionOutput)

	if out.OverallVerdict == "no_issues" {
		return enqueueFirstBeadForExecution(ctx, tx, job.ProjectID, now)
	}

	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'pending', ?, ?)`,
		job.ProjectID, db.VerbReconcileDecomposition, now, now)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", db.VerbReconcileDecomposition, err)
	}
	return nil
}
