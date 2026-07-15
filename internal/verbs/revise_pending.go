package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
)

type RevisePending struct {
	folderPath string // cached from Run for use in Commit
}

func (h *RevisePending) Verb() string { return db.VerbRevisePending }

func (h *RevisePending) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	if !job.BeadID.Valid {
		return "", fmt.Errorf("REVISE_PENDING job %d has no bead_id", job.ID)
	}
	triggerBeadID := job.BeadID.Int64

	project, err := loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	h.folderPath = project.FolderPath

	triggerBead, err := loadBeadByID(ctx, d, triggerBeadID)
	if err != nil {
		return "", fmt.Errorf("load trigger bead: %w", err)
	}

	pendingBeads, err := loadPendingBeads(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}

	// Read only the trigger bead's non-test output files. Pending bead specs
	// already incorporate everything prior REVISE_PENDING runs knew about
	// earlier beads, so sending the full project source is redundant and
	// inflates the prompt unboundedly as the project grows.
	fileContents := make(map[string]string)
	for _, f := range triggerBead.OutputFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(project.FolderPath, f))
		if rerr == nil {
			fileContents[f] = string(data)
		}
	}

	model, err := loadVerbModelOrFallback(ctx, d, job.ProjectID, db.VerbRevisePending, db.VerbAuditDecomposition)
	if err != nil {
		return "", err
	}

	userMsg := buildRevisePendingUserMsg(triggerBead, fileContents, pendingBeads)
	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: guidance.InjectForVerbPath(revisePendingSystemPrompt, project.FolderPath, db.VerbRevisePending, "")},
		{Role: "user", Content: userMsg},
	}, nil)
}

func (h *RevisePending) Validate(raw string) (string, any) {
	var out RevisePendingOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if len(out.Revisions) == 0 {
		return "malformed: revisions array is empty", nil
	}
	for _, r := range out.Revisions {
		if strings.TrimSpace(r.BeadTitle) == "" {
			return "malformed: revision missing bead_title", nil
		}
		if r.Action != "update_spec" && r.Action != "no_change" {
			return fmt.Sprintf("malformed: action must be \"update_spec\" or \"no_change\", got %q for bead %q", r.Action, r.BeadTitle), nil
		}
		if r.Action == "update_spec" && strings.TrimSpace(r.UpdatedFullText) == "" {
			return fmt.Sprintf("malformed: bead %q has action=update_spec but updated_full_text is empty", r.BeadTitle), nil
		}
	}
	return "valid", out
}

func (h *RevisePending) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(RevisePendingOutput)
	now := time.Now().UTC().Format(time.RFC3339)
	triggerBeadID := job.BeadID.Int64

	revisionMap := make(map[string]RevisePendingRevision, len(out.Revisions))
	for _, r := range out.Revisions {
		revisionMap[r.BeadTitle] = r
	}

	// Load pending beads from the transaction.
	rows, err := tx.QueryContext(ctx, `
		SELECT b.id, br.id, br.revision_number, br.full_text, br.execution_budget, br.monitor_override
		FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.project_id = ? AND b.status = 'pending'
		ORDER BY b.id`, job.ProjectID)
	if err != nil {
		return fmt.Errorf("load pending beads: %w", err)
	}

	type pendingRow struct {
		beadID         int64
		revID          int64
		revNum         int
		fullText       string
		execBudget     int
		monitorOverride string
		title          string
	}
	var pending []pendingRow
	for rows.Next() {
		var r pendingRow
		if err := rows.Scan(&r.beadID, &r.revID, &r.revNum, &r.fullText, &r.execBudget, &r.monitorOverride); err != nil {
			rows.Close()
			return err
		}
		var tmp struct {
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(r.fullText), &tmp) == nil && tmp.Title != "" {
			r.title = tmp.Title
		} else {
			r.title = fmt.Sprintf("bead-%d", r.beadID)
		}
		pending = append(pending, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range pending {
		rev, ok := revisionMap[p.title]
		if !ok {
			continue // not in output — treat as no_change, skip spec_revisions
		}

		if rev.Action != "update_spec" {
			// no_change: write audit record only
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO spec_revisions
				  (project_id, trigger_bead_id, revised_bead_id, old_revision_id, new_revision_id, created_at)
				VALUES (?, ?, ?, ?, NULL, ?)`,
				job.ProjectID, triggerBeadID, p.beadID, p.revID, now); err != nil {
				return fmt.Errorf("insert no_change spec_revision for bead %d: %w", p.beadID, err)
			}
			continue
		}

		// update_spec: parse current full_text, update prose only, re-marshal.
		var pb ParsedBead
		if err := json.Unmarshal([]byte(p.fullText), &pb); err != nil {
			return fmt.Errorf("parse bead %d full_text: %w", p.beadID, err)
		}
		pb.FullText = rev.UpdatedFullText

		newFullText, err := json.Marshal(pb)
		if err != nil {
			return fmt.Errorf("marshal updated bead %d: %w", p.beadID, err)
		}

		// Bead-wide max, not p.revNum+1 — a bead can have been rewind-bead'd while
		// still pending (current_revision_id reset to 1, but higher-numbered stale
		// revisions remain in the table), which would otherwise produce a collision.
		var maxRevNum int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(revision_number), 0) FROM bead_revisions WHERE bead_id = ?`, p.beadID,
		).Scan(&maxRevNum); err != nil {
			return fmt.Errorf("load max revision number for bead %d: %w", p.beadID, err)
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO bead_revisions
			  (project_id, bead_id, revision_number, full_text,
			   execution_budget, monitor_override, created_by_verb, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			job.ProjectID, p.beadID, maxRevNum+1, string(newFullText),
			p.execBudget, p.monitorOverride, db.VerbRevisePending, now)
		if err != nil {
			return fmt.Errorf("insert revised bead_revision for bead %d: %w", p.beadID, err)
		}
		newRevID, _ := res.LastInsertId()

		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET current_revision_id = ? WHERE id = ?`, newRevID, p.beadID); err != nil {
			return fmt.Errorf("update bead %d current_revision_id: %w", p.beadID, err)
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO spec_revisions
			  (project_id, trigger_bead_id, revised_bead_id, old_revision_id, new_revision_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			job.ProjectID, triggerBeadID, p.beadID, p.revID, newRevID, now); err != nil {
			return fmt.Errorf("insert spec_revision for bead %d: %w", p.beadID, err)
		}
	}

	// Dispatch the first pending bead after the trigger bead for execution.
	var nextBeadID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM beads
		WHERE project_id = ? AND id > ? AND status = 'pending'
		ORDER BY id LIMIT 1`, job.ProjectID, triggerBeadID,
	).Scan(&nextBeadID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find next pending bead: %w", err)
	}
	if err := enqueueBeadExecution(ctx, tx, job.ProjectID, nextBeadID, now); err != nil {
		return err
	}
	if pause, err := shouldPauseAfterBead(ctx, tx, job.ProjectID, triggerBeadID); err != nil {
		return err
	} else if pause {
		return pauseProject(ctx, tx, job.ProjectID, now)
	}
	return nil
}


func buildRevisePendingUserMsg(triggerBead *beadState, fileContents map[string]string, pendingBeads []beadState) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "## Completed Bead\n\n**Title:** %s\n**Output files:** %s\n\n",
		triggerBead.Title, strings.Join(triggerBead.OutputFiles, ", "))

	sb.WriteString("## Current Project Files\n\n")
	// Sort filenames for deterministic output.
	names := make([]string, 0, len(fileContents))
	for n := range fileContents {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		ext := filepath.Ext(name)
		lang := ""
		if ext == ".go" {
			lang = "go"
		}
		fmt.Fprintf(&sb, "### %s\n\n```%s\n%s\n```\n\n", name, lang, fileContents[name])
	}

	sb.WriteString("## Pending Bead Specs\n\n")
	for _, bead := range pendingBeads {
		fmt.Fprintf(&sb, "### Bead: %s\n\n", bead.Title)
		fmt.Fprintf(&sb, "**Output files:** %s\n", strings.Join(bead.OutputFiles, ", "))
		if len(bead.ExitCriteria) > 0 {
			fmt.Fprintf(&sb, "**Exit criteria:** %s\n", strings.Join(bead.ExitCriteria, "; "))
		}
		sb.WriteString("\n**Spec:**\n\n")
		// Extract prose from full_text JSON.
		var pb struct {
			FullText string `json:"full_text"`
		}
		if json.Unmarshal([]byte(bead.FullText), &pb) == nil && pb.FullText != "" {
			sb.WriteString(pb.FullText)
		} else {
			sb.WriteString(bead.FullText)
		}
		sb.WriteString("\n\n---\n\n")
	}

	return sb.String()
}
