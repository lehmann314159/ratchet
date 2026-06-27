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
	"ratchet/internal/trace"
)

type AnalyzeExecution struct{}

func (h *AnalyzeExecution) Verb() string { return db.VerbAnalyzeExecution }

func (h *AnalyzeExecution) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	if !job.BeadID.Valid {
		return "", fmt.Errorf("%s job %d has no bead_id", db.VerbAnalyzeExecution, job.ID)
	}
	beadID := job.BeadID.Int64

	// Load the most recent completed execution for this bead, plus the bead
	// revision's full_text (for output_files) and the project folder path.
	var execID int64
	var tracePath, terminationCause, beadFullTextJSON, folderPath string
	var monitorFired, monitorHonored *bool
	if err := d.QueryRowContext(ctx, `
		SELECT e.id, e.trace_path, e.termination_cause, e.monitor_fired, e.monitor_honored,
		       br.full_text, p.folder_path
		FROM executions e
		JOIN bead_revisions br ON br.id = e.bead_revision_id
		JOIN projects p ON p.id = e.project_id
		WHERE e.bead_id = ? AND e.termination_cause IS NOT NULL
		ORDER BY e.ended_at DESC
		LIMIT 1`, beadID,
	).Scan(&execID, &tracePath, &terminationCause, &monitorFired, &monitorHonored,
		&beadFullTextJSON, &folderPath); err != nil {
		return "", fmt.Errorf("load execution for bead %d: %w", beadID, err)
	}

	traceData, err := os.ReadFile(tracePath)
	if err != nil {
		return "", fmt.Errorf("read trace %s: %w", tracePath, err)
	}

	outputFileStatus := checkOutputFiles(beadFullTextJSON, folderPath)

	var beadSpec struct {
		OutputFiles  []string `json:"output_files"`
		ExitCriteria []string `json:"exit_criteria"`
	}
	json.Unmarshal([]byte(beadFullTextJSON), &beadSpec) //nolint:errcheck — malformed JSON handled by empty slices

	// Layout bead structural check: verify api_check_test.go (if owned by this
	// bead) contains package-level blank-identifier assertions referencing exported
	// identifiers. If the check fails, return a pre-built finding without calling
	// the model — the structural violation is unambiguous and needs no interpretation.
	lang := guidance.Detect(folderPath)
	if finding := checkLayoutBeadOutput(lang, folderPath, beadSpec.OutputFiles); finding != "" {
		out := AnalyzeExecutionOutput{
			MechanicalFindings:     finding,
			AnalyzerInterpretation: "Layout bead structural check failed before model analysis. The signature lock is absent or malformed; all other passing checks are unreliable until this is resolved.",
		}
		data, _ := json.Marshal(out)
		return string(data), nil
	}

	// Generate mechanical_findings from structured trace data — no model call,
	// no causal-language risk.
	pt := trace.Parse(traceData)
	mechanicalFindings := trace.GenerateMechanicalFindings(
		pt, terminationCause, monitorFired, monitorHonored,
		beadSpec.ExitCriteria, outputFileStatus,
	)

	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbAnalyzeExecution)
	if err != nil {
		return "", err
	}

	lastFailure, err := loadLastValidationFailure(ctx, d, job.ID)
	if err != nil {
		return "", fmt.Errorf("load last validation failure: %w", err)
	}

	userMsg := buildAnalyzeUserMsg(mechanicalFindings, lastFailure)
	raw, err := oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: analyzeExecutionSystemPrompt},
		{Role: "user", Content: userMsg},
	}, nil)
	if err != nil {
		return "", err
	}

	// Parse the model's interpretation-only response and assemble the full output.
	var modelOut struct {
		AnalyzerInterpretation string `json:"analyzer_interpretation"`
	}
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &modelOut); err != nil {
		return "", fmt.Errorf("parse analyzer interpretation: %w", err)
	}
	out := AnalyzeExecutionOutput{
		MechanicalFindings:     mechanicalFindings,
		AnalyzerInterpretation: modelOut.AnalyzerInterpretation,
	}
	result, _ := json.Marshal(out)
	return string(result), nil
}

// checkOutputFiles stats each file listed in the bead's output_files and
// returns a human-readable status string for inclusion in the ANALYZE prompt.
// A missing file is an objective fact — no causal language needed here.
func checkOutputFiles(beadFullTextJSON, folderPath string) string {
	var spec struct {
		OutputFiles []string `json:"output_files"`
	}
	if err := json.Unmarshal([]byte(beadFullTextJSON), &spec); err != nil || len(spec.OutputFiles) == 0 {
		return "(output_files not available)"
	}
	var sb strings.Builder
	for _, rel := range spec.OutputFiles {
		info, err := os.Stat(filepath.Join(folderPath, rel))
		if err != nil {
			fmt.Fprintf(&sb, "%s: missing\n", rel)
		} else {
			fmt.Fprintf(&sb, "%s: present (%d bytes)\n", rel, info.Size())
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func buildAnalyzeUserMsg(mechanicalFindings, lastFailure string) string {
	msg := "## Mechanical Findings\n\n" + mechanicalFindings
	if lastFailure != "" {
		msg += "\n\n## Previous Attempt Rejected\n\nYour previous attempt was rejected: " + lastFailure
	}
	return msg
}

func (h *AnalyzeExecution) Validate(raw string) (string, any) {
	var out AnalyzeExecutionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if strings.TrimSpace(out.MechanicalFindings) == "" {
		return "malformed: mechanical_findings is empty", nil
	}
	return "valid", out
}

func (h *AnalyzeExecution) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(AnalyzeExecutionOutput)
	now := time.Now().UTC().Format(time.RFC3339)

	// Load the execution_id for the most recent completed execution of this bead.
	var execID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM executions
		WHERE bead_id = ? AND termination_cause IS NOT NULL
		ORDER BY ended_at DESC LIMIT 1`,
		job.BeadID.Int64,
	).Scan(&execID); err != nil {
		return fmt.Errorf("load execution_id: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO analyses (project_id, execution_id, mechanical_findings, analyzer_interpretation, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		job.ProjectID, execID,
		out.MechanicalFindings, nullableString(out.AnalyzerInterpretation),
		now,
	); err != nil {
		return fmt.Errorf("insert analysis: %w", err)
	}

	// Enqueue COMPRESS_ANALYSIS for this bead.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?)`,
		job.ProjectID, db.VerbCompressAnalysis, job.BeadID.Int64, now, now)
	return err
}

// nullableString returns sql.NullString for the INSERT; used inline as any.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
