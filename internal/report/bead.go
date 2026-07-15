package report

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"ratchet/internal/trace"
)

// WriteBead generates and writes traces/bead-{id}-report.md at bead terminal state.
// status is one of: "succeeded", "escalated", "full_stopped",
// or "full_stopped (cascade — stopped by bead N)" for cascade-stopped beads.
// File write failures are logged at WARN and do not propagate.
func WriteBead(ctx context.Context, tx *sql.Tx, folderPath string, beadID int64, status string) {
	md, err := buildBeadReport(ctx, tx, folderPath, beadID, status)
	if err != nil {
		slog.Warn("post-execution: build bead report", "bead_id", beadID, "error", err)
		return
	}
	tracesDir := filepath.Join(folderPath, "traces")
	if err := os.MkdirAll(tracesDir, 0o755); err != nil {
		slog.Warn("post-execution: mkdir traces", "bead_id", beadID, "error", err)
		return
	}
	path := filepath.Join(tracesDir, fmt.Sprintf("bead-%d-report.md", beadID))
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		slog.Warn("post-execution: write bead report", "bead_id", beadID, "error", err)
	}
}

// ---- data types ----

type beadRevData struct {
	Number   int
	FullText string
	Budget   int
	Monitor  string
	Verb     string
}

type execData struct {
	ID               int64
	TerminationCause string // empty if no record (killed)
	MonitorFired     bool
	DurationS        int
	TracePath        string
	MechFindings     string // from analyses, empty if missing
}

type adjData struct {
	ExecID    int64
	Trend     string
	Fit       string
	Reasoning string
	Decision  string
	Budget    int // from bead_revisions for this execution
}

// ---- query helpers ----

func queryRevisions(ctx context.Context, tx *sql.Tx, beadID int64) ([]beadRevData, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT revision_number, full_text, execution_budget, monitor_override, created_by_verb
		FROM bead_revisions WHERE bead_id = ? ORDER BY revision_number`, beadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []beadRevData
	for rows.Next() {
		var r beadRevData
		if err := rows.Scan(&r.Number, &r.FullText, &r.Budget, &r.Monitor, &r.Verb); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func queryExecutions(ctx context.Context, tx *sql.Tx, beadID int64) ([]execData, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT e.id,
		       COALESCE(e.termination_cause, ''),
		       COALESCE(e.monitor_fired, 0),
		       CAST(COALESCE(
		           (julianday(e.ended_at) - julianday(e.started_at)) * 86400, 0
		       ) AS INTEGER),
		       e.trace_path,
		       COALESCE(a.mechanical_findings, ''),
		       COALESCE(e.infra_failure, 0)
		FROM executions e
		LEFT JOIN analyses a ON a.execution_id = e.id
		WHERE e.bead_id = ?
		ORDER BY e.started_at`, beadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []execData
	for rows.Next() {
		var e execData
		var monFired, infraFailure int
		if err := rows.Scan(&e.ID, &e.TerminationCause, &monFired, &e.DurationS,
			&e.TracePath, &e.MechFindings, &infraFailure); err != nil {
			return nil, err
		}
		e.MonitorFired = monFired != 0
		// infra_failure executions are recorded with termination_cause='success'
		// as a placeholder (the schema's CHECK constraint has no dedicated
		// "crashed" value) — override the displayed cause so a crash-recovered
		// execution isn't reported as an indistinguishable real success.
		if infraFailure == 1 {
			e.TerminationCause = "infra_failure"
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func queryAdjudications(ctx context.Context, tx *sql.Tx, beadID int64) ([]adjData, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT adj.execution_id, adj.trend, adj.bead_spec_fit,
		       adj.reasoning_text, adj.decision, br.execution_budget
		FROM adjudications adj
		JOIN executions e ON e.id = adj.execution_id
		JOIN bead_revisions br ON br.id = e.bead_revision_id
		WHERE adj.bead_id = ?
		ORDER BY adj.id`, beadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []adjData
	for rows.Next() {
		var a adjData
		if err := rows.Scan(&a.ExecID, &a.Trend, &a.Fit, &a.Reasoning, &a.Decision, &a.Budget); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func queryCompressedHistory(ctx context.Context, tx *sql.Tx, beadID int64) string {
	var text string
	tx.QueryRowContext(ctx, `SELECT compressed_text FROM compressed_history WHERE bead_id = ?`, beadID).Scan(&text)
	return text
}

// ---- rendering ----

func buildBeadReport(ctx context.Context, tx *sql.Tx, folderPath string, beadID int64, status string) (string, error) {
	revs, err := queryRevisions(ctx, tx, beadID)
	if err != nil || len(revs) == 0 {
		return "", fmt.Errorf("load revisions for bead %d: %w", beadID, err)
	}
	execs, err := queryExecutions(ctx, tx, beadID)
	if err != nil {
		return "", fmt.Errorf("load executions for bead %d: %w", beadID, err)
	}
	adjs, err := queryAdjudications(ctx, tx, beadID)
	if err != nil {
		return "", fmt.Errorf("load adjudications for bead %d: %w", beadID, err)
	}
	compressed := queryCompressedHistory(ctx, tx, beadID)

	// Parse title and output_files from latest revision.
	latestRev := revs[len(revs)-1]
	title, outputFiles, exitCriteria := parseBeadSpec(latestRev.FullText)
	if title == "" {
		title = fmt.Sprintf("bead-%d", beadID)
	}

	// Compute total wall time.
	totalS := 0
	for _, e := range execs {
		totalS += e.DurationS
	}

	var b strings.Builder

	// Header
	fmt.Fprintf(&b, "# Bead %d: %s\n\n", beadID, title)
	fmt.Fprintf(&b, "**Status:** %s  \n", status)
	fmt.Fprintf(&b, "**Attempts:** %d  \n", len(execs))
	if totalS > 0 {
		fmt.Fprintf(&b, "**Wall time:** %ds (%dm)  \n", totalS, totalS/60)
	} else {
		b.WriteString("**Wall time:** 0s  \n")
	}
	if len(exitCriteria) > 0 {
		fmt.Fprintf(&b, "**Final exit criterion:** `%s`  \n", exitCriteria[0])
	}
	b.WriteString("\n---\n\n")

	// Spec History
	b.WriteString("## Spec History\n\n")
	for _, r := range revs {
		renderRevision(&b, r)
	}

	// Attempt History
	b.WriteString("## Attempt History\n\n")
	if len(execs) == 0 {
		b.WriteString("*(bead never executed)*\n\n")
	} else {
		b.WriteString("| # | Execution ID | Termination | Duration | Monitor | write_file ok/total | Last test result |\n")
		b.WriteString("|---|---|---|---|---|---|---|\n")
		for i, e := range execs {
			termStr := e.TerminationCause
			if termStr == "" {
				termStr = "(killed/no record)"
			}
			monStr := "no fire"
			if e.MonitorFired {
				monStr = "FIRED"
			}
			wfStr := writeFileStats(e.TracePath)
			testStr := lastTestResult(e.MechFindings, exitCriteria)
			fmt.Fprintf(&b, "| %d | %d | %s | %ds | %s | %s | %s |\n",
				i+1, e.ID, termStr, e.DurationS, monStr, wfStr, testStr)
		}
		b.WriteString("\n")
	}

	// ADJUDICATE Decisions
	if len(adjs) > 0 {
		b.WriteString("## ADJUDICATE Decisions\n\n")
		// Map exec ID to attempt number for labels.
		execIdx := make(map[int64]int)
		for i, e := range execs {
			execIdx[e.ID] = i + 1
		}
		for _, a := range adjs {
			attemptN := execIdx[a.ExecID]
			if a.Decision == "declare_success" {
				fmt.Fprintf(&b, "### After attempt %d → %s\n\n", attemptN, a.Decision)
			} else {
				fmt.Fprintf(&b, "### After attempt %d → %s\n\n", attemptN, a.Decision)
				fmt.Fprintf(&b, "**Trend:** %s  \n", a.Trend)
				fmt.Fprintf(&b, "**Bead spec fit:** %s  \n", a.Fit)
				fmt.Fprintf(&b, "**Actual execution budget:** %ds  \n", a.Budget)
				fmt.Fprintf(&b, "**Reasoning:** %s\n\n", a.Reasoning)
			}
		}
	}
	// Note escalation-at-cap (no adjudication row written in that case).
	if status == "escalated" {
		fmt.Fprintf(&b, "### After attempt %d → escalated (attempt cap reached)\n\n", len(execs))
		b.WriteString("No adjudication written — escalated mechanically at cap.\n\n")
	}

	// Compressed History
	b.WriteString("## Compressed History\n\n")
	if compressed != "" {
		b.WriteString(compressed)
		b.WriteString("\n\n")
	} else {
		b.WriteString("*(none)*\n\n")
	}

	// Final Output Files
	b.WriteString("## Final Output Files\n\n")
	b.WriteString("*State of output_files on disk at report time.*\n\n")
	for _, f := range outputFiles {
		renderOutputFile(&b, folderPath, f)
	}

	// Last Trace Excerpt
	b.WriteString("## Last Trace Excerpt\n\n")
	if len(execs) == 0 {
		b.WriteString("*(none)*\n")
	} else {
		last := execs[len(execs)-1]
		excerpt := tailFile(last.TracePath, 60)
		fmt.Fprintf(&b, "*Final 60 lines of `%s`*\n\n```\n%s\n```\n", filepath.Base(last.TracePath), excerpt)
	}

	return b.String(), nil
}

func renderRevision(b *strings.Builder, r beadRevData) {
	title, outputFiles, exitCriteria := parseBeadSpec(r.FullText)
	_, fullTextProse := parseBeadSpecFull(r.FullText)

	fmt.Fprintf(b, "### Revision %d — created by %s\n\n", r.Number, r.Verb)
	fmt.Fprintf(b, "**Title:** %s  \n", title)
	fmt.Fprintf(b, "**Output files:** %s  \n", strings.Join(outputFiles, ", "))
	if len(exitCriteria) > 0 {
		fmt.Fprintf(b, "**Exit criteria:** `%s`  \n", strings.Join(exitCriteria, "`, `"))
	}
	fmt.Fprintf(b, "**Execution budget:** %ds  \n", r.Budget)
	fmt.Fprintf(b, "**Monitor override:** %s  \n\n", r.Monitor)
	if fullTextProse != "" {
		b.WriteString(fullTextProse)
		b.WriteString("\n\n")
	}
}

func renderOutputFile(b *strings.Builder, folderPath, relPath string) {
	abs := filepath.Join(folderPath, relPath)
	content, err := os.ReadFile(abs)
	if err != nil {
		fmt.Fprintf(b, "### %s\n\n*(not present)*\n\n", relPath)
		return
	}
	lang := codeLanguage(relPath)
	fmt.Fprintf(b, "### %s\n\n```%s\n%s\n```\n\n", relPath, lang, string(content))
}

// ---- helpers ----

type beadSpec struct {
	Title        string   `json:"title"`
	FullText     string   `json:"full_text"`
	OutputFiles  []string `json:"output_files"`
	ExitCriteria []string `json:"exit_criteria"`
}

func parseBeadSpec(fullText string) (title string, outputFiles []string, exitCriteria []string) {
	var s beadSpec
	if err := json.Unmarshal([]byte(fullText), &s); err != nil {
		return "", nil, nil
	}
	return s.Title, s.OutputFiles, s.ExitCriteria
}

func parseBeadSpecFull(fullText string) (title string, prose string) {
	var s beadSpec
	if err := json.Unmarshal([]byte(fullText), &s); err != nil {
		return "", fullText
	}
	return s.Title, s.FullText
}

// writeFileStats parses the trace file and returns "ok/total" counts, or "—" if
// the trace file is unreadable (e.g. killed before trace was written).
func writeFileStats(tracePath string) string {
	data, err := os.ReadFile(tracePath)
	if err != nil {
		return "—"
	}
	pt := trace.Parse(data)
	if len(pt.WriteFiles) == 0 {
		return "0/0"
	}
	ok := 0
	for _, wf := range pt.WriteFiles {
		if wf.Succeeded {
			ok++
		}
	}
	return fmt.Sprintf("%d/%d", ok, len(pt.WriteFiles))
}

// lastTestResult extracts a one-line summary of the exit criterion's last run
// from ANALYZE's mechanical_findings.
func lastTestResult(findings string, exitCriteria []string) string {
	if findings == "" || len(exitCriteria) == 0 {
		return "—"
	}
	// Look for "Last run: turn N, exit " in the findings.
	lines := strings.Split(findings, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Last run:") {
			if strings.Contains(trimmed, "exit: 0") || strings.HasSuffix(trimmed, "exit 0") {
				return "PASS"
			}
			// The run's own result never arrived (killed/timed out mid-command —
			// trace.parseResultLines defaults ExitRaw to "(truncated)" when no
			// "exit: " line was seen). That's neither a pass nor a fail; labeling
			// it FAIL would misreport an execution that was killed before its
			// test could finish as a definitive test failure.
			if strings.HasSuffix(trimmed, "(truncated)") {
				return "unknown (truncated)"
			}
			return "FAIL"
		}
		if strings.Contains(trimmed, "Not run during this execution") {
			return "not run"
		}
	}
	return "not run"
}

// tailFile returns the last n lines of a file as a single string.
func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(trace file unreadable: %v)", err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func codeLanguage(path string) string {
	switch filepath.Ext(path) {
	case ".go":
		return "go"
	case ".html":
		return "html"
	case ".mod":
		return ""
	default:
		return ""
	}
}

