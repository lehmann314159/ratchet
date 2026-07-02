package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
)

type DecomposeSpec struct {
	budgetDefault int
	folderPath    string
}

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
	h.budgetDefault = project.ExecutionBudgetDefault
	h.folderPath = project.FolderPath

	surveyDocPath := filepath.Join(project.FolderPath, "survey.md")
	surveyDoc, err := os.ReadFile(surveyDocPath)
	if err != nil {
		return "", fmt.Errorf("read survey.md at %s: %w", surveyDocPath, err)
	}

	userMsg := buildDecomposeUserMsg(doc, string(surveyDoc))
	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: guidance.Inject(decomposeSpecSystemPrompt(), project.FolderPath)},
		{Role: "user", Content: userMsg},
	}, nil)
}

func buildDecomposeUserMsg(designDoc, surveyDoc string) string {
	var sb strings.Builder
	sb.WriteString("## Survey Document\n\n")
	sb.WriteString(surveyDoc)
	sb.WriteString("\n\n## Design Document\n\n")
	sb.WriteString(designDoc)
	return sb.String()
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
		if b.MonitorOverride != "honor" && b.MonitorOverride != "ignore" {
			return fmt.Sprintf("malformed: bead[%d] (%s) monitor_override must be \"honor\" or \"ignore\", got %q", i, b.Title, b.MonitorOverride), nil
		}
		if len(b.OutputFiles) == 0 {
			return fmt.Sprintf("malformed: bead[%d] (%s) output_files is missing or empty", i, b.Title), nil
		}
		if len(b.ExitCriteria) == 0 {
			return fmt.Sprintf("malformed: bead[%d] (%s) exit_criteria is missing or empty", i, b.Title), nil
		}
	}
	return "valid", out
}

func (h *DecomposeSpec) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(DecomposeSpecOutput)
	now := time.Now().UTC().Format(time.RFC3339)

	var allOutputFiles []string
	for _, pb := range out.Beads {
		allOutputFiles = append(allOutputFiles, pb.OutputFiles...)
	}
	lang := detectLang(h.folderPath, allOutputFiles)
	for _, pb := range out.Beads {
		applyMechanicalBeadFixes(lang, &pb)

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
			h.budgetDefault, pb.MonitorOverride,
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
