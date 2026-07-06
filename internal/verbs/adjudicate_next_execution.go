package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
	"ratchet/internal/report"
	"ratchet/internal/trace"
)

// consistencyKeywords maps each bead_spec_fit value to keyword sets.
// If the declared value is present but the reasoning contains none of the
// expected keywords (and does contain counterpart keywords), flag inconsistency.
// This catches the Experiment 5 failure: GLM declared "bead_problem" while
// reasoning described "textbook Runner-capability case".
// checkConsistency validates that the declared bead_spec_fit matches the
// reasoning text. The check targets the concrete failure mode from Experiment 5:
// a model declaring "bead_problem" while its own reasoning described the spec
// as clear and unambiguous ("textbook runner-capability case").
//
// Two-signal check per field:
//   - counterpart phrases: reasoning language that directly contradicts the field
//   - exonerating phrases: reasoning that explicitly clears the "accused" party
//
// Either signal alone is sufficient to flag inconsistency. Keyword matching
// is approximate; the store of record is the adjudications table, where
// a human can review trend/bead_spec_fit against reasoning_text directly.
func checkConsistency(fit, reasoning string) (bool, string) {
	lower := strings.ToLower(reasoning)

	switch fit {
	case "bead_problem":
		// Inconsistent: reasoning uses runner/capability language OR
		// explicitly says the spec is NOT the problem.
		contradict := []string{
			"runner-capability", "runner capability",
			"capability problem", "capability case",
			"execution error", "implementation error",
			// Spec-exonerating phrases (Exp-5 pattern: "despite the spec being unambiguous")
			"spec being unambiguous", "spec is clear", "spec is correct",
			"spec is unambiguous", "despite the spec", "unambiguous spec",
			"clear specification", "specification is clear",
		}
		for _, p := range contradict {
			if strings.Contains(lower, p) {
				return false, fmt.Sprintf(
					"declared bead_spec_fit=%q but reasoning contains contradicting phrase %q",
					fit, p,
				)
			}
		}

	case "execution_capability_problem":
		// Inconsistent: reasoning blames the spec rather than execution.
		// Note: "bead specification is" (bare) is intentionally absent — it fires
		// on exonerating language ("the bead specification is clear") and produces
		// false positives. Only forms that affirmatively blame the spec are listed.
		contradict := []string{
			"spec problem", "spec is unclear", "spec is ambiguous",
			"specification wrong", "specification is unclear", "specification is ambiguous",
			"bead specification is missing", "bead specification is wrong",
			"bead specification is unclear", "bead specification is ambiguous",
			"bead specification is incorrect",
			"ambiguous requirement", "unclear requirement",
			"missing from the spec", "specification does not",
		}
		for _, p := range contradict {
			if strings.Contains(lower, p) {
				return false, fmt.Sprintf(
					"declared bead_spec_fit=%q but reasoning contains contradicting phrase %q",
					fit, p,
				)
			}
		}
	}
	return true, ""
}

// vacuousPassNote returns a non-empty structural note to inject into the
// mechanical findings when the vacuous test pass is Type B (inherent) — the
// bead's output_files contain no *_test.go, so the test named in the exit
// criterion was never part of this bead's deliverable.
//
// Type A (test file IS in output_files but tests didn't run) returns "" — the
// standard vacuous-pass principle in the ADJUDICATE prompt applies there.
func vacuousPassNote(bead *beadState, mechanicalFindings string) string {
	hasTestCriterion := false
	for _, c := range bead.ExitCriteria {
		if strings.Contains(c, "go test") {
			hasTestCriterion = true
			break
		}
	}
	if !hasTestCriterion {
		return ""
	}
	lower := strings.ToLower(mechanicalFindings)
	isVacuous := strings.Contains(lower, "no tests to run") ||
		strings.Contains(lower, "[no test files]") ||
		strings.Contains(lower, "no test files")
	if !isVacuous {
		return ""
	}
	if hasTestGoFile(bead.OutputFiles) {
		return "" // Type A — test file was in scope; standard rule applies
	}
	return "[Structural note: Type B vacuous pass] This bead's output_files contain no " +
		"*_test.go file, so the test named in the exit criterion is outside this bead's " +
		"scope. The vacuous-pass rule does not block declare_success here. Evaluate only " +
		"whether the non-test output files listed in output_files were correctly written " +
		"(file exists, content is correct for the bead's stated purpose)."
}

// orientationOnlyNote detects the pattern where the latest execution ended with
// no write_file calls at all — the agent spent its entire budget on read-only
// orientation commands and never began writing. Covers both timeout and
// monitor_terminated termination causes (MONITOR fires after 10+ turns with no
// write_file, producing the same orientation-only pattern as a timeout).
// Returns a note to inject into mechanical findings so ADJUDICATE can apply the
// orientation-only fast path without having to infer the pattern from field names
// that do not appear in the mechanical findings output.
func orientationOnlyNote(ctx context.Context, d *db.DB, beadID int64) string {
	var tracePath, terminationCause string
	err := d.QueryRowContext(ctx, `
		SELECT trace_path, termination_cause FROM executions
		WHERE bead_id = ? ORDER BY id DESC LIMIT 1`, beadID).Scan(&tracePath, &terminationCause)
	if err != nil {
		return ""
	}
	if terminationCause != "timeout" && terminationCause != "monitor_terminated" {
		return ""
	}
	data, err := os.ReadFile(tracePath)
	if err != nil {
		return ""
	}
	pt := trace.Parse(data)
	if len(pt.WriteFiles) > 0 {
		return "" // agent made at least one write attempt — not orientation-only
	}
	return "[Fast path — orientation only] The previous attempt ran only read-only commands " +
		"(ls, read_file, etc.) and made no write_file calls before terminating. The agent did " +
		"not begin the task. The content of the bead spec is not the problem."
}

// partialProgressNote checks whether some (but not all) output_files for the
// bead already exist on disk. When partial state is present, ADJUDICATE must
// know which files are done and which remain — otherwise it misreads the
// attempt as "no progress" and gives contradictory orientation instructions.
func partialProgressNote(folderPath string, outputFiles []string) string {
	if len(outputFiles) == 0 {
		return ""
	}
	type fileStatus struct {
		name string
		size int64
	}
	var present, absent []fileStatus
	for _, f := range outputFiles {
		info, err := os.Stat(filepath.Join(folderPath, f))
		if err == nil {
			present = append(present, fileStatus{f, info.Size()})
		} else {
			absent = append(absent, fileStatus{f, 0})
		}
	}
	if len(present) == 0 || len(absent) == 0 {
		return "" // all present (success path) or all absent (normal start)
	}
	var parts []string
	for _, f := range present {
		parts = append(parts, fmt.Sprintf("%s present (%d bytes)", f.name, f.size))
	}
	for _, f := range absent {
		parts = append(parts, fmt.Sprintf("%s not yet written", f.name))
	}
	return "[Partial progress] Some output_files already exist on disk: " +
		strings.Join(parts, "; ") + ". Do NOT rewrite files that are already present " +
		"and passing — focus only on the missing files listed above."
}

// stubImplNote fires when all output files are present on disk but some
// non-test Go functions within them have zero-value stub bodies (e.g. return nil).
// This catches the case where a prior attempt wrote a partial implementation —
// NewGame correct, ApplyMove returning nil — and ADJUDICATE needs to direct the
// next attempt to fill in only the stubs rather than rewrite from scratch.
func stubImplNote(folderPath string, outputFiles []string) string {
	var stubs []string
	for _, f := range outputFiles {
		if !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") {
			continue
		}
		stubs = append(stubs, detectStubFuncs(filepath.Join(folderPath, f), f)...)
	}
	if len(stubs) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"[Stub implementation] The following functions have zero-value stub bodies and are not yet implemented: %s. "+
			"The surrounding code in these files is likely correct. "+
			"ADJUDICATE should instruct the executor to: read the file(s) first, then overwrite with "+
			"a single write_file call that keeps all existing correct implementations and fills in "+
			"only the stub function bodies listed above.",
		strings.Join(stubs, "; "))
}

// detectStubFuncs parses a Go source file and returns names of functions whose
// bodies consist of a single zero-value return statement.
func detectStubFuncs(path, basename string) []string {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil
	}
	var names []string
	for _, decl := range node.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		if fd.Type.Results == nil || fd.Type.Results.NumFields() == 0 {
			continue
		}
		if len(fd.Body.List) == 1 {
			ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
			if ok && isZeroValueReturn(ret) {
				names = append(names, fmt.Sprintf("%s (in %s)", fd.Name.Name, basename))
			}
		}
	}
	return names
}

func isZeroValueReturn(ret *ast.ReturnStmt) bool {
	for _, r := range ret.Results {
		if !isZeroValueExpr(r) {
			return false
		}
	}
	return true
}

func isZeroValueExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "nil" || e.Name == "false"
	case *ast.BasicLit:
		switch e.Kind {
		case token.INT:
			return e.Value == "0"
		case token.STRING:
			return e.Value == `""` || e.Value == "``"
		}
	case *ast.CompositeLit:
		return len(e.Elts) == 0
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			cl, ok := e.X.(*ast.CompositeLit)
			return ok && len(cl.Elts) == 0
		}
	}
	return false
}

// testFirstCompleteNote fires when the bead has *_test.go files that exist on
// disk but non-test implementation files are absent — i.e., the previous
// attempt was a test-first attempt that wrote only test files.
// Returns a note to inject into mechanical findings so ADJUDICATE knows to
// always emit execute_revised, lock the test files, and direct the next
// attempt to write only the implementation.
func testFirstCompleteNote(folderPath string, outputFiles []string) string {
	var testFiles, implFiles []string
	for _, f := range outputFiles {
		_, err := os.Stat(filepath.Join(folderPath, f))
		if strings.HasSuffix(f, "_test.go") {
			if err == nil {
				testFiles = append(testFiles, f)
			}
		} else if strings.HasSuffix(f, ".go") {
			if err != nil {
				implFiles = append(implFiles, f)
			}
		}
	}
	if len(testFiles) == 0 || len(implFiles) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"[Test-first complete] Test files were written in the previous (test-first) attempt: %s. "+
			"Implementation files are absent: %s.\n\n"+
			"ADJUDICATE INSTRUCTIONS — this note requires specific handling:\n"+
			"1. Decision MUST be execute_revised — tests written but implementation absent; never execute_as_is or declare_success.\n"+
			"2. If the mechanical findings above contain \"[Test-first verification]\" MISMATCH entries: "+
			"incorporate the exact corrections into the revised full_text (specific file, line context, old value → new value). "+
			"Instruct the executor to apply each correction before writing any implementation file.\n"+
			"3. The revised full_text MUST state: \"The test file(s) %s are LOCKED — do NOT modify them "+
			"(except for the corrections specified above). Write ONLY the implementation file(s): %s.\"",
		strings.Join(testFiles, ", "), strings.Join(implFiles, ", "),
		strings.Join(testFiles, ", "), strings.Join(implFiles, ", "),
	)
}

// missingPathNote detects the pattern where the latest execution ended with a
// write_file call that omitted the path argument. The model generated correct
// content but the file was never written. Returns a note to inject into
// mechanical findings so ADJUDICATE can apply the fast path.
func missingPathNote(ctx context.Context, d *db.DB, beadID int64) string {
	var tracePath string
	err := d.QueryRowContext(ctx, `
		SELECT trace_path FROM executions
		WHERE bead_id = ? ORDER BY id DESC LIMIT 1`, beadID).Scan(&tracePath)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(tracePath)
	if err != nil {
		return ""
	}
	pt := trace.Parse(data)
	if len(pt.WriteFiles) == 0 {
		return ""
	}
	// Any successful write means the file landed — not this failure mode.
	for _, wf := range pt.WriteFiles {
		if wf.Succeeded {
			return ""
		}
	}
	// Last write_file had no path argument.
	if pt.WriteFiles[len(pt.WriteFiles)-1].Path != "" {
		return ""
	}
	return "[Fast path — missing write_file path] The previous attempt generated correct " +
		"content but called write_file without a path argument. No file was written. " +
		"The content itself is not the problem."
}

type AdjudicateNextExecution struct {
	budgetDefault int    // cached from Run for use in Commit
	folderPath    string // cached from Run for use in Commit
}

func (h *AdjudicateNextExecution) Verb() string { return db.VerbAdjudicateNextExecution }

func (h *AdjudicateNextExecution) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	if !job.BeadID.Valid {
		return "", fmt.Errorf("%s job %d has no bead_id", db.VerbAdjudicateNextExecution, job.ID)
	}
	beadID := job.BeadID.Int64

	// Input 1: current Bead state.
	beads, err := loadCurrentBeads(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	var currentBead *beadState
	for i := range beads {
		if beads[i].BeadID == beadID {
			currentBead = &beads[i]
			break
		}
	}
	if currentBead == nil {
		return "", fmt.Errorf("bead %d not found in project %d", beadID, job.ProjectID)
	}

	// Input 2: revision log.
	revLog, err := loadBeadRevisionLog(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	// Input 3: latest ANALYZE_EXECUTION mechanical_findings (not interpretation).
	analysis, err := loadLatestAnalysis(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	// Input 4: COMPRESS_ANALYSIS compressed history.
	compressedHistory, err := loadCompressedHistory(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	// Compute the diff-signal: which failure categories each revision targeted
	// and the last two executions' termination causes.
	diffSignal, err := buildDiffSignal(ctx, d, beadID)
	if err != nil {
		return "", err
	}

	project, err := loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	h.budgetDefault = project.ExecutionBudgetDefault
	h.folderPath = project.FolderPath

	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbAdjudicateNextExecution)
	if err != nil {
		return "", err
	}

	findings := analysis.MechanicalFindings
	if note := testFirstCompleteNote(h.folderPath, currentBead.OutputFiles); note != "" {
		findings += "\n\n" + note
	}
	if note := vacuousPassNote(currentBead, findings); note != "" {
		findings += "\n\n" + note
	}
	if note := partialProgressNote(h.folderPath, currentBead.OutputFiles); note != "" {
		findings += "\n\n" + note
	}
	if note := stubImplNote(h.folderPath, currentBead.OutputFiles); note != "" {
		findings += "\n\n" + note
	}
	if note := orientationOnlyNote(ctx, d, beadID); note != "" {
		findings += "\n\n" + note
	}
	if note := missingPathNote(ctx, d, beadID); note != "" {
		findings += "\n\n" + note
	}
	userMsg := buildAdjudicateUserMsg(currentBead, revLog, findings, compressedHistory, diffSignal)
	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: guidance.InjectForVerbPath(adjudicateNextExecutionSystemPrompt, project.FolderPath, db.VerbAdjudicateNextExecution, "")},
		{Role: "user", Content: userMsg},
	}, nil)
}

func buildAdjudicateUserMsg(bead *beadState, revLog []revisionEntry, mechanicalFindings, compressedHistory, diffSignal string) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "## Input 1: Current Bead State\n\nBead ID: %d\nActual execution budget: %ds\n\n%s\n\n", bead.BeadID, bead.ExecutionBudget, bead.FullText)

	sb.WriteString("## Input 2: Bead Revision Log\n\n")
	for _, r := range revLog {
		fmt.Fprintf(&sb, "### Revision %d (created by %s)\n\n%s\n\n", r.RevisionNumber, r.CreatedByVerb, r.FullText)
	}
	sb.WriteString("### Diff Signal\n\n")
	sb.WriteString(diffSignal)
	sb.WriteString("\n\n")

	sb.WriteString("## Input 3: Latest Mechanical Findings\n\n")
	sb.WriteString(mechanicalFindings)
	sb.WriteString("\n\n")

	sb.WriteString("## Input 4: Compressed History\n\n")
	if compressedHistory != "" {
		sb.WriteString(compressedHistory)
	} else {
		sb.WriteString("(none — this is the first attempt)")
	}

	return sb.String()
}

// buildDiffSignal computes the revision diff signal from the architecture:
// "a diff of each revision against the version it replaced, compared against
// the failure category ANALYZE_EXECUTION reports on subsequent attempts."
// Test-ID correspondence is the primary signal.
func buildDiffSignal(ctx context.Context, d *db.DB, beadID int64) (string, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT e.id, e.termination_cause, a.mechanical_findings,
		       e.bead_revision_id, e.ended_at
		FROM executions e
		JOIN analyses a ON a.execution_id = e.id
		WHERE e.bead_id = ?
		ORDER BY e.ended_at`, beadID)
	if err != nil {
		return "(no execution history yet)", nil
	}
	defer rows.Close()

	type execRow struct {
		ExecID, RevID    int64
		TerminationCause string
		Findings         string
		EndedAt          string
	}
	var execs []execRow
	for rows.Next() {
		var r execRow
		if err := rows.Scan(&r.ExecID, &r.TerminationCause, &r.Findings, &r.RevID, &r.EndedAt); err != nil {
			return "", err
		}
		execs = append(execs, r)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(execs) == 0 {
		return "(no execution history yet)", nil
	}

	var sb strings.Builder
	for i, e := range execs {
		fmt.Fprintf(&sb, "Attempt %d (revision %d, ended %s): termination=%s\nFindings: %s\n",
			i+1, e.RevID, e.EndedAt, e.TerminationCause, e.Findings)
		if i > 0 {
			sb.WriteString("(diff against previous revision: see revision log above)\n")
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func (h *AdjudicateNextExecution) Validate(raw string) (string, any) {
	var out AdjudicateNextExecutionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}

	validTrends := map[string]bool{"same": true, "narrower": true, "unrelated": true, "not_applicable": true}
	if !validTrends[out.Trend] {
		return fmt.Sprintf("malformed: trend must be \"same\", \"narrower\", \"unrelated\", or \"not_applicable\", got %q", out.Trend), nil
	}

	validFits := map[string]bool{"bead_problem": true, "execution_capability_problem": true, "not_applicable": true}
	if !validFits[out.BeadSpecFit] {
		return fmt.Sprintf("malformed: bead_spec_fit must be \"bead_problem\", \"execution_capability_problem\", or \"not_applicable\", got %q", out.BeadSpecFit), nil
	}

	if strings.TrimSpace(out.Reasoning) == "" {
		return "malformed: reasoning is empty", nil
	}

	validDecisions := map[string]bool{"execute_as_is": true, "execute_revised": true, "full_stop": true, "declare_success": true}
	if !validDecisions[out.Decision] {
		return fmt.Sprintf("malformed: decision must be \"execute_as_is\", \"execute_revised\", \"full_stop\", or \"declare_success\", got %q", out.Decision), nil
	}

	if out.Decision == "execute_revised" {
		if out.RevisedBead == nil {
			return "malformed: decision is execute_revised but revised_bead is absent", nil
		}
		if out.RevisedBead.MonitorOverride != "honor" && out.RevisedBead.MonitorOverride != "ignore" {
			return fmt.Sprintf("malformed: revised_bead monitor_override must be \"honor\" or \"ignore\", got %q", out.RevisedBead.MonitorOverride), nil
		}
		if len(out.RevisedBead.OutputFiles) == 0 {
			return "malformed: revised_bead output_files is missing or empty", nil
		}
		if len(out.RevisedBead.ExitCriteria) == 0 {
			return "malformed: revised_bead exit_criteria is missing or empty", nil
		}
	}

	if out.Decision == "declare_success" {
		// declare_success requires both classification fields to be "not_applicable" —
		// there is no failure to attribute when the bead succeeded.
		if out.Trend != "not_applicable" {
			return fmt.Sprintf("malformed: decision is declare_success but trend is %q — must be \"not_applicable\"", out.Trend), nil
		}
		if out.BeadSpecFit != "not_applicable" {
			return fmt.Sprintf("malformed: decision is declare_success but bead_spec_fit is %q — must be \"not_applicable\"", out.BeadSpecFit), nil
		}
	} else {
		// For retry/stop decisions, "not_applicable" is forbidden and the consistency
		// check applies (zero-strike tolerance — a mismatch is a validation failure).
		if out.Trend == "not_applicable" {
			return "malformed: trend \"not_applicable\" is only valid when decision is \"declare_success\"", nil
		}
		if out.BeadSpecFit == "not_applicable" {
			return "malformed: bead_spec_fit \"not_applicable\" is only valid when decision is \"declare_success\"", nil
		}
		if ok, reason := checkConsistency(out.BeadSpecFit, out.Reasoning); !ok {
			return "malformed: consistency check failed: " + reason, nil
		}
	}

	return "valid", out
}

// Commit writes the adjudications row and enqueues the next action.
// Zero-strike tolerance: Commit is only reached on a valid output.
func (h *AdjudicateNextExecution) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(AdjudicateNextExecutionOutput)
	now := time.Now().UTC().Format(time.RFC3339)
	beadID := job.BeadID.Int64

	// Load the latest execution for metadata.
	var execID int64
	var budgetCost float64
	var monitorEscalated bool
	if err := tx.QueryRowContext(ctx, `
		SELECT e.id,
		       CAST(julianday(e.ended_at) - julianday(e.started_at) AS REAL) * 86400.0,
		       COALESCE(e.monitor_fired, 0)
		FROM executions e
		WHERE e.bead_id = ? AND e.termination_cause IS NOT NULL
		ORDER BY e.ended_at DESC LIMIT 1`, beadID,
	).Scan(&execID, &budgetCost, &monitorEscalated); err != nil {
		return fmt.Errorf("load execution for adjudication: %w", err)
	}

	monitorEscalatedInt := 0
	if monitorEscalated {
		monitorEscalatedInt = 1
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO adjudications
		  (project_id, bead_id, execution_id, trend, bead_spec_fit, reasoning_text,
		   attempt_budget_cost, monitor_escalation_status, decision, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ProjectID, beadID, execID,
		out.Trend, out.BeadSpecFit, out.Reasoning,
		budgetCost, monitorEscalatedInt, out.Decision, now,
	); err != nil {
		return fmt.Errorf("insert adjudication: %w", err)
	}

	switch out.Decision {
	case "execute_as_is":
		if atCap, err := h.atExecutionCap(ctx, tx, job.ProjectID, beadID, now, job.ID); err != nil || atCap {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET status = 'pending' WHERE id = ?`, beadID); err != nil {
			return fmt.Errorf("reset bead to pending: %w", err)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbExecuteBead, beadID, now, now)
		return err

	case "execute_revised":
		if atCap, err := h.atExecutionCap(ctx, tx, job.ProjectID, beadID, now, job.ID); err != nil || atCap {
			return err
		}
		// Write a new bead_revision for the revised spec.
		var currentRevNum int
		if err := tx.QueryRowContext(ctx, `
			SELECT br.revision_number FROM beads b
			JOIN bead_revisions br ON br.id = b.current_revision_id
			WHERE b.id = ?`, beadID,
		).Scan(&currentRevNum); err != nil {
			return fmt.Errorf("load current revision number: %w", err)
		}

		// Clamp execution_budget to at least the project default so ADJUDICATE
		// cannot accidentally starve a retry with a too-small budget estimate.
		// Apply the clamp to the struct before marshaling so full_text stored in
		// the DB reflects the enforced budget — ADJUDICATE reads full_text on the
		// next round and would otherwise anchor to the unclamped value.
		if out.RevisedBead.ExecutionBudget < h.budgetDefault {
			out.RevisedBead.ExecutionBudget = h.budgetDefault
		}
		budget := out.RevisedBead.ExecutionBudget

		// Apply language-specific structural fixes to the revised spec before
		// storing it, catching the same class of errors that DECOMPOSE and
		// RECONCILE fix at decomposition time (e.g. go test without a test file).
		applyMechanicalBeadFixes(
			detectLang(h.folderPath, out.RevisedBead.OutputFiles),
			out.RevisedBead,
		)

		fullText, _ := json.Marshal(out.RevisedBead)
		res, err := tx.ExecContext(ctx, `
			INSERT INTO bead_revisions
			  (project_id, bead_id, revision_number, full_text,
			   execution_budget, monitor_override, created_by_verb, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			job.ProjectID, beadID, currentRevNum+1, string(fullText),
			budget, out.RevisedBead.MonitorOverride,
			db.VerbAdjudicateNextExecution, now)
		if err != nil {
			return fmt.Errorf("insert revised bead_revision: %w", err)
		}
		revID, _ := res.LastInsertId()

		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET status = 'pending', current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbExecuteBead, beadID, now, now)
		return err

	case "full_stop":
		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET status = 'full_stopped' WHERE id = ?`, beadID); err != nil {
			return err
		}
		report.WriteBead(ctx, tx, h.folderPath, beadID, "full_stopped")

		// Collect cascade bead IDs before the bulk update.
		cascadeIDs, _ := queryCascadeBeadIDs(ctx, tx, job.ProjectID, beadID)

		// Mark all subsequent pending beads stopped — they will never run now.
		if _, err := tx.ExecContext(ctx, `
			UPDATE beads SET status = 'full_stopped'
			WHERE project_id = ? AND id > ? AND status = 'pending'`,
			job.ProjectID, beadID,
		); err != nil {
			return fmt.Errorf("mark remaining beads full_stopped: %w", err)
		}
		for _, cascadeID := range cascadeIDs {
			report.WriteBead(ctx, tx, h.folderPath, cascadeID,
				fmt.Sprintf("full_stopped (cascade — stopped by bead %d)", beadID))
		}
		if err := h.checkProjectTerminal(ctx, tx, job.ProjectID, "full_stopped", now); err != nil {
			return err
		}
		report.WriteProject(ctx, tx, job.ProjectID, h.folderPath)
		return nil

	case "declare_success":
		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET status = 'succeeded' WHERE id = ?`, beadID); err != nil {
			return fmt.Errorf("mark bead succeeded: %w", err)
		}
		report.WriteBead(ctx, tx, h.folderPath, beadID, "succeeded")

		var pendingCount int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM beads WHERE project_id = ? AND status = 'pending'`,
			job.ProjectID,
		).Scan(&pendingCount); err != nil {
			return fmt.Errorf("count pending beads: %w", err)
		}

		if pendingCount == 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE projects SET status = 'complete', updated_at = ? WHERE id = ?`,
				now, job.ProjectID); err != nil {
				return err
			}
			report.WriteProject(ctx, tx, job.ProjectID, h.folderPath)
			return nil
		}

		// Fire REVISE_PENDING to update remaining specs before dispatching next bead.
		_, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbRevisePending, beadID, now, now)
		return err
	}
	return nil
}

// atExecutionCap returns true if the bead has reached the project's
// max_execution_attempts limit. When the cap is reached, the ADJUDICATE job is
// escalated so Mike can review rather than looping indefinitely.
func (h *AdjudicateNextExecution) atExecutionCap(ctx context.Context, tx *sql.Tx, projectID, beadID int64, now string, jobID int64) (bool, error) {
	var cap, count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(b.execution_attempts_override, p.max_execution_attempts)
		FROM beads b JOIN projects p ON p.id = b.project_id
		WHERE b.id = ?`, beadID,
	).Scan(&cap); err != nil {
		return false, fmt.Errorf("load max_execution_attempts: %w", err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM executions WHERE bead_id = ? AND infra_failure = 0 AND test_first_attempt = 0`, beadID,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("count executions for bead %d: %w", beadID, err)
	}
	if count < cap {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`,
		now, jobID,
	); err != nil {
		return true, fmt.Errorf("escalate at cap: %w", err)
	}
	slog.Error("ESCALATION — max execution attempts reached",
		"project_id", projectID, "bead_id", beadID,
		"attempts", count, "cap", cap, "job_id", jobID)
	report.WriteBead(ctx, tx, h.folderPath, beadID, "escalated")
	return true, nil
}

// queryCascadeBeadIDs returns IDs of pending beads after beadID in the project.
// Called before the bulk full_stop update so we can write cascade reports.
func queryCascadeBeadIDs(ctx context.Context, tx *sql.Tx, projectID, afterBeadID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM beads
		WHERE project_id = ? AND id > ? AND status = 'pending'
		ORDER BY id`, projectID, afterBeadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// checkProjectTerminal checks whether all beads in the project have reached a
// terminal state ('full_stopped' or 'succeeded'). If so, it marks the project
// with terminalStatus. Called from the full_stop branch.
func (h *AdjudicateNextExecution) checkProjectTerminal(ctx context.Context, tx *sql.Tx, projectID int64, terminalStatus, now string) error {
	var activeBeads int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM beads
		WHERE project_id = ? AND status NOT IN ('full_stopped', 'succeeded')`,
		projectID,
	).Scan(&activeBeads); err != nil {
		return fmt.Errorf("count active beads: %w", err)
	}
	if activeBeads == 0 {
		_, err := tx.ExecContext(ctx,
			`UPDATE projects SET status = ?, updated_at = ? WHERE id = ?`,
			terminalStatus, now, projectID)
		return err
	}
	return nil
}
