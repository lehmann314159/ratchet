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
	"ratchet/internal/ollama"
)

const analyzeExecutionSystemPrompt = `You analyze a completed execution trace. Your output has two strictly separated sections.

MECHANICAL_FINDINGS: Objective facts only. No causal language. No interpretation.
Forbidden phrases: "due to", "because", "caused by", "causes", "results in", "the reason", "the error is", "fails because".
Any forbidden phrase in mechanical_findings causes the entire output to be rejected and the attempt to not count.
State what happened: test names, exit codes, line numbers, error messages verbatim.

  WRONG: "The test failed due to a missing import."
  RIGHT: "TestFoo: FAIL. Exit code: 1. Compiler error: undefined: FooFunc at main.go:12."

ANALYZER_INTERPRETATION: Your read on what the mechanical findings mean. This section is explicitly labeled
as interpretation. Use hedged language: "suggests", "appears to", "may indicate", "consistent with".

Respond with JSON only, no prose before or after:
{
  "mechanical_findings": "<fielded facts, no causal language>",
  "analyzer_interpretation": "<labeled, hedged interpretation>"
}`

// forbiddenPhrases are checked in mechanical_findings during validation.
// Experiment 2 showed Qwen3-Coder has a ~11% contamination rate on this
// field; catching it at validation time enforces the architecture's causal-
// language discipline without depending on per-run model behavior.
var forbiddenPhrases = []string{
	"due to",
	"because",
	"caused by",
	"causes",
	"results in",
	"the reason",
	"the error is",
	"fails because",
}

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

	trace, err := os.ReadFile(tracePath)
	if err != nil {
		return "", fmt.Errorf("read trace %s: %w", tracePath, err)
	}

	outputFileStatus := checkOutputFiles(beadFullTextJSON, folderPath)

	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbAnalyzeExecution)
	if err != nil {
		return "", err
	}

	lastFailure, err := loadLastValidationFailure(ctx, d, job.ID)
	if err != nil {
		return "", fmt.Errorf("load last validation failure: %w", err)
	}

	userMsg := buildAnalyzeUserMsg(execID, terminationCause, monitorFired, monitorHonored, outputFileStatus, string(trace), lastFailure)
	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: analyzeExecutionSystemPrompt},
		{Role: "user", Content: userMsg},
	}, nil)
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

func buildAnalyzeUserMsg(execID int64, cause string, monitorFired, monitorHonored *bool, outputFileStatus, trace, lastFailure string) string {
	fired := "unknown"
	if monitorFired != nil {
		if *monitorFired {
			fired = "yes"
		} else {
			fired = "no"
		}
	}
	honored := "unknown"
	if monitorHonored != nil {
		if *monitorHonored {
			honored = "yes (override flag was 'honor')"
		} else {
			honored = "no (override flag was 'ignore')"
		}
	}
	// monitor_force_killed means EXECUTE_BEAD did not respond to SIGTERM within
	// the grace window and was hard-killed by the orchestrator. The trace may
	// have a truncated final line.
	causeNote := cause
	if cause == "monitor_force_killed" {
		causeNote = "monitor_force_killed (EXECUTE_BEAD did not respond to graceful signal; trace may be truncated)"
	}

	msg := fmt.Sprintf(`## Composite Record

Execution ID: %d
Termination cause: %s
Monitor fired: %s
Monitor honored: %s

## Output Files (filesystem state at analysis time)

%s

## Execution Trace

%s`, execID, causeNote, fired, honored, outputFileStatus, trace)

	if lastFailure != "" {
		msg += fmt.Sprintf("\n\n## Previous Attempt Rejected\n\nYour previous attempt was rejected for this reason: %s\n\nReview your mechanical_findings before responding to ensure no forbidden phrases appear.", lastFailure)
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
	// Causal-language discipline check (Experiment 2 finding).
	lower := strings.ToLower(out.MechanicalFindings)
	for _, phrase := range forbiddenPhrases {
		if strings.Contains(lower, phrase) {
			return fmt.Sprintf("malformed: causal language in mechanical_findings: found %q", phrase), nil
		}
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
