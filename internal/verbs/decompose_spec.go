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

func decomposeSpecSystemPrompt(budgetDefault int) string {
	return fmt.Sprintf(`You decompose a design document into a list of Beads — well-scoped, independently executable units of work, each with a clear done-condition.

Each Bead must be independently executable: it must not assume that code written by
other Beads already exists or is in a particular state. For small projects where all
code lives in a single file, prefer fewer, larger Beads that each produce a complete,
runnable state of the codebase rather than many fine-grained Beads that each modify
the same file in sequence. A Bead that only makes sense after another Bead has run
is not a valid decomposition.

For every Bead you issue you must set:
- execution_budget: integer seconds, the maximum wall-clock time for one execution attempt.
  The project default is %d seconds — use this as your baseline. Each execution involves
  multiple model calls plus test runs, so budgets below 60 seconds are never appropriate.
  Adjust up for complex Beads (many interacting constraints, large test suites) or down
  for trivial ones, but stay within an order of magnitude of the default.
- monitor_override: "honor" (MONITOR_EXECUTION may terminate this Bead on loop detection) or "ignore" (loop detection signal is suppressed — use only for legitimately repetitive work)

Surface ambiguities in the design doc explicitly in the ambiguities field. Do not silently resolve them.

Respond with JSON only, no prose before or after:
{
  "beads": [
    {
      "title": "<short identifier, unique within this decomposition>",
      "full_text": "<complete, self-contained Bead specification>",
      "execution_budget": <integer seconds>,
      "monitor_override": "honor" | "ignore"
    }
  ],
  "ambiguities": ["<any unresolved ambiguities in the design doc>"]
}`, budgetDefault)
}

type DecomposeSpec struct{}

func (h *DecomposeSpec) Verb() string { return db.VerbDecomposeSpec }

func (h *DecomposeSpec) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	doc, err := loadDesignDoc(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbDecomposeSpec)
	if err != nil {
		return "", err
	}
	project, err := loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: decomposeSpecSystemPrompt(project.ExecutionBudgetDefault)},
		{Role: "user", Content: doc},
	}, nil)
}

func (h *DecomposeSpec) Validate(raw string) (string, any) {
	var out DecomposeSpecOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if len(out.Beads) == 0 {
		return "malformed: beads array is empty", nil
	}
	for i, b := range out.Beads {
		if b.Title == "" {
			return fmt.Sprintf("malformed: bead[%d] missing title", i), nil
		}
		if b.FullText == "" {
			return fmt.Sprintf("malformed: bead[%d] (%s) missing full_text", i, b.Title), nil
		}
		if b.ExecutionBudget <= 0 {
			return fmt.Sprintf("malformed: bead[%d] (%s) execution_budget must be a positive integer", i, b.Title), nil
		}
		if b.MonitorOverride != "honor" && b.MonitorOverride != "ignore" {
			return fmt.Sprintf("malformed: bead[%d] (%s) monitor_override must be \"honor\" or \"ignore\", got %q", i, b.Title, b.MonitorOverride), nil
		}
	}
	return "valid", out
}

func (h *DecomposeSpec) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(DecomposeSpecOutput)
	now := time.Now().UTC().Format(time.RFC3339)

	for _, pb := range out.Beads {
		// Write the bead row first (current_revision_id NULL until revision exists).
		res, err := tx.ExecContext(ctx, `
			INSERT INTO beads (project_id, status, current_revision_id)
			VALUES (?, 'pending', NULL)`, job.ProjectID)
		if err != nil {
			return fmt.Errorf("insert bead %q: %w", pb.Title, err)
		}
		beadID, _ := res.LastInsertId()

		// Write full_text as a JSON object so the title is preserved alongside
		// the spec text (the title field isn't a separate column in bead_revisions).
		fullText, err := json.Marshal(pb)
		if err != nil {
			return fmt.Errorf("marshal bead %q: %w", pb.Title, err)
		}

		res, err = tx.ExecContext(ctx, `
			INSERT INTO bead_revisions
			  (project_id, bead_id, revision_number, full_text,
			   execution_budget, monitor_override, created_by_verb, created_at)
			VALUES (?, ?, 1, ?, ?, ?, ?, ?)`,
			job.ProjectID, beadID, string(fullText),
			pb.ExecutionBudget, pb.MonitorOverride,
			db.VerbDecomposeSpec, now)
		if err != nil {
			return fmt.Errorf("insert revision for bead %q: %w", pb.Title, err)
		}
		revID, _ := res.LastInsertId()

		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
			return fmt.Errorf("set current_revision_id for bead %q: %w", pb.Title, err)
		}
	}

	// Enqueue AUDIT_DECOMPOSITION (project-scoped, bead_id NULL).
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'pending', ?, ?)`,
		job.ProjectID, db.VerbAuditDecomposition, now, now)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", db.VerbAuditDecomposition, err)
	}

	// Log ambiguities if any.
	if len(out.Ambiguities) > 0 {
		// Store as a project-level note in the project label for now;
		// a dedicated table is a future enhancement.
		note := "AMBIGUITIES: " + strings.Join(out.Ambiguities, "; ")
		if _, err := tx.ExecContext(ctx,
			`UPDATE projects SET label = label || ? WHERE id = ?`,
			" | "+note, job.ProjectID); err != nil {
			return fmt.Errorf("record ambiguities: %w", err)
		}
	}

	return nil
}
