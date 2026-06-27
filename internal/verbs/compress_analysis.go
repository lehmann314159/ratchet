package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

// compressPassthroughThreshold is the minimum number of completed executions
// before COMPRESS_ANALYSIS makes a model call. Below this threshold the raw
// mechanical findings are written through directly, saving a model round-trip
// on early attempts where there is little history to compress and the staleness
// risk (baking wrong conclusions into a summary) outweighs the compression gain.
const compressPassthroughThreshold = 3

type CompressAnalysis struct{}

func (h *CompressAnalysis) Verb() string { return db.VerbCompressAnalysis }

func (h *CompressAnalysis) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	if !job.BeadID.Valid {
		return "", fmt.Errorf("%s job %d has no bead_id", db.VerbCompressAnalysis, job.ID)
	}
	beadID := job.BeadID.Int64

	analysis, err := loadLatestAnalysis(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	// Count completed executions for this bead. Below the threshold, write
	// raw findings through without a model call. This avoids baking early
	// wrong conclusions into a summary that ADJUDICATE then inherits.
	var execCount int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM executions WHERE bead_id = ? AND termination_cause IS NOT NULL`,
		beadID,
	).Scan(&execCount); err != nil {
		return "", fmt.Errorf("count executions for bead %d: %w", beadID, err)
	}
	if execCount < compressPassthroughThreshold {
		// Accumulate raw findings rather than replace, so the model at attempt 3
		// receives the full history of attempts 1 and 2 as context.
		existing, err := loadCompressedHistory(ctx, d, beadID)
		if err != nil {
			return "", err
		}
		entry := fmt.Sprintf("Attempt %d (raw — compression starts at attempt %d):\n\n%s",
			execCount, compressPassthroughThreshold, analysis.MechanicalFindings)
		var combined string
		if existing != "" {
			combined = existing + "\n\n" + entry
		} else {
			combined = entry
		}
		out, _ := json.Marshal(CompressAnalysisOutput{CompressedText: combined})
		return string(out), nil
	}

	history, err := loadCompressedHistory(ctx, d, beadID)
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
		sb.WriteString("## Existing Compressed History\n\n(none)\n\n")
	}
	sb.WriteString("## Latest Analysis\n\n")
	sb.WriteString("### Mechanical Findings\n\n")
	sb.WriteString(analysis.MechanicalFindings)
	if analysis.AnalyzerInterpretation != "" {
		sb.WriteString("\n\n### Analyzer Interpretation\n\n")
		sb.WriteString(analysis.AnalyzerInterpretation)
	}

	raw, err := oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: compressAnalysisSystemPrompt},
		{Role: "user", Content: sb.String()},
	}, nil)
	if err != nil {
		return "", err
	}

	// Post-process: inject RESOLVED tags for RECURRING failure classes whose
	// signals are absent from the latest mechanical_findings.
	var out CompressAnalysisOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err == nil {
		out.CompressedText = injectResolvedTags(out.CompressedText, analysis.MechanicalFindings)
		updated, _ := json.Marshal(out)
		return string(updated), nil
	}
	return raw, nil // parse failed; Validate will catch it
}

var (
	reTestName     = regexp.MustCompile(`\bTest[A-Z]\w*\b`)
	reUndefinedSym = regexp.MustCompile(`\bundefined:\s+(\w+)`)
)

// extractFailureSignals returns strings that would appear in mechanical_findings
// if the described failure is still active. Returns nil if no signals are found,
// in which case the caller leaves the line unchanged (safe default).
func extractFailureSignals(line string) []string {
	var sigs []string
	for _, name := range reTestName.FindAllString(line, -1) {
		// go test stdout for a still-failing test contains "FAIL: TestName"
		sigs = append(sigs, "FAIL: "+name)
	}
	for _, m := range reUndefinedSym.FindAllStringSubmatch(line, -1) {
		if len(m) > 1 {
			// go build/test stderr for a still-undefined symbol contains "undefined: Name"
			sigs = append(sigs, "undefined: "+m[1])
		}
	}
	return sigs
}

// injectResolvedTags post-processes the model's compressed_text: for each line
// tagged RECURRING, it extracts failure signals and checks whether any appear in
// mechanicalFindings. If none do, the failure class is absent from the latest
// attempt and the line is annotated [RESOLVED — absent from latest attempt].
// Lines with no extractable signals are left unchanged.
func injectResolvedTags(compressedText, mechanicalFindings string) string {
	lines := strings.Split(compressedText, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "RECURRING") || strings.Contains(line, "RESOLVED") {
			continue
		}
		sigs := extractFailureSignals(line)
		if len(sigs) == 0 {
			continue
		}
		stillPresent := false
		for _, sig := range sigs {
			if strings.Contains(mechanicalFindings, sig) {
				stillPresent = true
				break
			}
		}
		if !stillPresent {
			lines[i] = line + " [RESOLVED — absent from latest attempt]"
		}
	}
	return strings.Join(lines, "\n")
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
