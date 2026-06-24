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

const compressAnalysisSystemPrompt = `You maintain a compressed record of execution history for a single Bead.
Given the existing compressed history and the latest analysis, produce an updated compressed record.

Requirements:
- Preserve the convergent/divergent trend signal: the direction of change across attempts must remain
  correctly inferrable from your output.
- Do not add judgment language about whether the Bead should be retried or stopped. That is
  ADJUDICATE_NEXT_EXECUTION's job.
- Keep the compressed record bounded. Older detail can be summarized; the most recent attempt
  should be represented accurately.

Respond with JSON only, no prose before or after:
{
  "compressed_text": "<updated compressed history>"
}`

type CompressAnalysis struct{}

func (h *CompressAnalysis) Verb() string { return db.VerbCompressAnalysis }

func (h *CompressAnalysis) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	if !job.BeadID.Valid {
		return "", fmt.Errorf("%s job %d has no bead_id", db.VerbCompressAnalysis, job.ID)
	}
	beadID := job.BeadID.Int64

	history, err := loadCompressedHistory(ctx, d, beadID)
	if err != nil {
		return "", err
	}
	analysis, err := loadLatestAnalysis(ctx, d, beadID)
	if err != nil {
		return "", err
	}
	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbCompressAnalysis)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	if history != "" {
		sb.WriteString("## Existing Compressed History\n\n")
		sb.WriteString(history)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("## Existing Compressed History\n\n(none — this is the first attempt)\n\n")
	}
	sb.WriteString("## Latest Analysis\n\n")
	sb.WriteString("### Mechanical Findings\n\n")
	sb.WriteString(analysis.MechanicalFindings)
	if analysis.AnalyzerInterpretation != "" {
		sb.WriteString("\n\n### Analyzer Interpretation\n\n")
		sb.WriteString(analysis.AnalyzerInterpretation)
	}

	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: compressAnalysisSystemPrompt},
		{Role: "user", Content: sb.String()},
	}, nil)
}

func (h *CompressAnalysis) Validate(raw string) (string, any) {
	var out CompressAnalysisOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if strings.TrimSpace(out.CompressedText) == "" {
		return "malformed: compressed_text is empty", nil
	}
	return "valid", out
}

func (h *CompressAnalysis) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(CompressAnalysisOutput)
	now := time.Now().UTC().Format(time.RFC3339)
	beadID := job.BeadID.Int64

	// Upsert: one evolving row per Bead.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO compressed_history (bead_id, project_id, compressed_text, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (bead_id) DO UPDATE SET
		  compressed_text = excluded.compressed_text,
		  updated_at      = excluded.updated_at`,
		beadID, job.ProjectID, out.CompressedText, now,
	); err != nil {
		return fmt.Errorf("upsert compressed_history: %w", err)
	}

	// Enqueue ADJUDICATE_NEXT_EXECUTION for this bead.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?)`,
		job.ProjectID, db.VerbAdjudicateNextExecution, beadID, now, now)
	return err
}
