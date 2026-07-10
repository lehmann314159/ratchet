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
	cleaned := sanitizeJSON(ollama.ExtractJSON(raw))
	if err := json.Unmarshal([]byte(cleaned), &out); err == nil {
		out.CompressedText = injectResolvedTags(out.CompressedText, analysis.MechanicalFindings)
		// Fallback: stripping rules can reduce a clean success to empty. Synthesize
		// a minimal entry so Validate never rejects a successful execution.
		if strings.TrimSpace(out.CompressedText) == "" {
			out.CompressedText = synthesizeMinimalEntry(history, analysis.MechanicalFindings)
		}
		updated, _ := json.Marshal(out)
		return string(updated), nil
	}
	return raw, nil // parse failed; Validate will catch it
}

var (
	reTestName     = regexp.MustCompile(`\bTest[A-Z]\w*\b`)
	reUndefinedSym = regexp.MustCompile(`\bundefined:\s+(\w+)`)
	// reBareBackslash matches either a valid \\ pair (preserved) or a lone
	// backslash before an invalid JSON escape char (fixed to \\). The \\\\
	// alternative must come first so the second \ of a valid \\ pair is
	// consumed before the lone-backslash arm can incorrectly grab it.
	reBareBackslash = regexp.MustCompile(`\\\\|\\([^"\\/bfnrtu])`)
)

// sanitizeJSON fixes two classes of invalid JSON that local models commonly emit:
//  1. Lone backslashes before non-special characters (e.g. \A → \\A)
//  2. Raw control characters inside string literals (e.g. literal tab → \t)
func sanitizeJSON(s string) string {
	// Pass 1: fix lone backslashes.
	s = reBareBackslash.ReplaceAllStringFunc(s, func(m string) string {
		if m == `\\` {
			return m
		}
		return `\\` + m[1:]
	})
	// Pass 2: escape raw control characters inside JSON string literals.
	return escapeControlCharsInStrings(s)
}

// escapeControlCharsInStrings walks s as a JSON token stream and replaces raw
// control characters (0x00–0x1F, 0x7F) found inside string literals with their
// JSON escape sequences. Pass 1 must run first so that all backslashes in the
// input are already valid JSON escape leaders.
func escapeControlCharsInStrings(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case c == '\\': // valid escape sequence — consume leader + next byte verbatim
				b.WriteByte(c)
				i++
				if i < len(s) {
					b.WriteByte(s[i])
				}
			case c == '"': // end of string literal
				inString = false
				b.WriteByte(c)
			case c < 0x20 || c == 0x7F: // raw control character — must be escaped in JSON
				switch c {
				case '\t':
					b.WriteString(`\t`)
				case '\n':
					b.WriteString(`\n`)
				case '\r':
					b.WriteString(`\r`)
				case '\b':
					b.WriteString(`\b`)
				case '\f':
					b.WriteString(`\f`)
				default:
					fmt.Fprintf(&b, `\u%04x`, c)
				}
			default:
				b.WriteByte(c)
			}
		} else {
			if c == '"' {
				inString = true
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

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

// synthesizeMinimalEntry builds a one-line compressed history entry when the
// model strips a clean execution down to empty. It infers the attempt number
// from the existing history and the termination cause from mechanical findings.
func synthesizeMinimalEntry(history, mechanicalFindings string) string {
	n := strings.Count(history, "Attempt ") + 1
	cause := "unknown"
	for _, line := range strings.Split(mechanicalFindings, "\n") {
		if strings.HasPrefix(line, "Termination cause: ") {
			cause = strings.TrimPrefix(line, "Termination cause: ")
			cause = strings.TrimSpace(cause)
			break
		}
	}
	return fmt.Sprintf("Attempt %d (%s): no failures to record.", n, cause)
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
	cleaned := sanitizeJSON(ollama.ExtractJSON(raw))
	var out CompressAnalysisOutput
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
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

	// Test-first attempts contain only test scaffolding work, not implementation
	// progress. Injecting them into later implementation attempts adds irrelevant
	// context noise and makes the input heavier without helping the model. Skip
	// the compressed_history upsert for those attempts; ADJUDICATE still runs.
	var testFirstAttempt int
	_ = tx.QueryRowContext(ctx,
		`SELECT test_first_attempt FROM executions WHERE bead_id = ? ORDER BY id DESC LIMIT 1`,
		beadID,
	).Scan(&testFirstAttempt)

	if testFirstAttempt == 0 {
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
	}

	// Enqueue ADJUDICATE_NEXT_EXECUTION for this bead.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?)`,
		job.ProjectID, db.VerbAdjudicateNextExecution, beadID, now, now)
	return err
}
