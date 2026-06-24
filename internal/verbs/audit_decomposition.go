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

const auditDecompositionSystemPrompt = `You review a decomposition against its source design document, checking for drift.
You are an independent reviewer — you did not author this decomposition.

For each finding, cite the specific Bead and the exact design-doc text it drifts from.
A clean decomposition with no findings is a valid outcome. Do not fabricate findings on clean material.

Your contract does not change across debate rounds — same correctness criterion every time.

Respond with JSON only, no prose before or after:
{
  "findings": [
    {
      "bead_title": "<title of the affected Bead>",
      "issue": "<specific description of the drift>",
      "design_doc_reference": "<exact quote or section reference from the design doc>"
    }
  ],
  "overall_verdict": "no_issues" | "issues_found"
}`

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
	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbAuditDecomposition)
	if err != nil {
		return "", err
	}

	userMsg := buildAuditUserMsg(doc, beads)
	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: auditDecompositionSystemPrompt},
		{Role: "user", Content: userMsg},
	}, nil)
}

func buildAuditUserMsg(doc string, beads []beadState) string {
	var sb strings.Builder
	sb.WriteString("## Design Document\n\n")
	sb.WriteString(doc)
	sb.WriteString("\n\n## Decomposition\n\n")
	for _, b := range beads {
		fmt.Fprintf(&sb, "### %s\n\n%s\n\n", b.Title, b.FullText)
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

// Commit enqueues RECONCILE_DECOMPOSITION. The critique text is stored in
// handoff_attempts (via the dispatch layer) and read by RECONCILE's Run;
// audit_reconcile_rounds is written atomically by RECONCILE's Commit once
// both the critique and reconciliation are available.
func (h *AuditDecomposition) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'pending', ?, ?)`,
		job.ProjectID, db.VerbReconcileDecomposition, now, now)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", db.VerbReconcileDecomposition, err)
	}
	return nil
}
