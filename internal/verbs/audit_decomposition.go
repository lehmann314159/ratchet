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

const auditDecompositionSystemPrompt = `You review a decomposition against its source design document, checking for two things:

1. Correctness drift: does each Bead accurately reflect the design document? For each finding,
   cite the specific Bead and the exact design-doc text it drifts from.

2. Independence: compare the output_files lists across all Beads. If two or more Beads share a
   file in output_files, they are potentially non-independent. Use judgment: if both Beads clearly
   document a sequential dependency (e.g. Bead B explicitly states it builds on code written by
   Bead A), the overlap may be acceptable. If the overlap is undocumented or avoidable — flag it.
   A finding for an independence violation should name all the affected Beads and the shared file(s),
   and suggest whether a merge or a clearer sequential dependency would resolve it.
   Use "N/A — structural" for design_doc_reference on independence findings.

3. Exit criteria quality: check each Bead's exit_criteria list. Each entry must be a concrete,
   runnable check — a shell command, a test invocation, or a specific measurable output. Flag any
   entry that is vague ("review the code"), untestable ("ensure correctness"), or out of scope for
   what the Bead actually produces. A Bead with no runnable exit criterion is a structural problem:
   it likely cannot be executed independently and should be merged with a related Bead.

You are an independent reviewer — you did not author this decomposition.
A clean decomposition with no findings is a valid outcome. Do not fabricate findings on clean material.
Your contract does not change across debate rounds — same correctness criterion every time.

Respond with JSON only, no prose before or after:
{
  "findings": [
    {
      "bead_title": "<title of the affected Bead>",
      "issue": "<specific description of the drift or independence violation>",
      "design_doc_reference": "<exact quote or section reference, or \"N/A — structural\" for independence findings>"
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
		return enqueueAllBeadsForExecution(ctx, tx, job.ProjectID, now)
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
