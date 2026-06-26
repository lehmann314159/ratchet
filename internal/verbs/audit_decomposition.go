package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
)

const auditDecompositionSystemPrompt = `You review a decomposition against its source design document, checking for the following:

1. Correctness drift: does each Bead accurately reflect the design document? For each finding,
   cite the specific Bead and the exact design-doc text it drifts from.

2. Independence: compare the output_files lists across all non-layout Beads (Beads 2+). If two
   or more non-layout Beads share a file in output_files, they are potentially non-independent.
   Use judgment: if both Beads clearly document a sequential dependency, the overlap may be
   acceptable. If undocumented or avoidable — flag it. Name all affected Beads and shared files,
   and suggest whether a merge or clearer sequential dependency would resolve it.
   File overlap between Bead 1 (the layout Bead) and any other Bead is expected and must NOT
   be flagged — the layout Bead creates the stubs that all other Beads fill in.
   Use "N/A — structural" for design_doc_reference on independence findings.

3. Exit criteria quality: check each Bead's exit_criteria list. Each entry must be a concrete,
   runnable check — a shell command, a test invocation, or a specific measurable output. Flag any
   entry that is vague ("review the code"), untestable ("ensure correctness"), or out of scope for
   what the Bead actually produces. A Bead with no runnable exit criterion is a structural problem:
   it likely cannot be executed independently and should be merged with a related Bead.
   Also flag: any exit criterion that runs a test command (e.g. ` + "`go test`" + `, ` + "`pytest`" + `) when the
   Bead's output_files contains no test file. A test command with no owned test file exits 0
   with "no test files" — it verifies nothing (vacuous pass). Name the specific Bead and criterion.

4. Layout Bead (Bead 1): the first Bead must be a layout Bead — its purpose is to establish file
   structure and stub implementations only, with no logic. Flag if: (a) Bead 1 contains non-trivial
   implementation logic rather than stubs; (b) any non-layout Bead creates new source files instead
   of filling in stubs from Bead 1; (c) Bead 1's exit criteria do not include a build check (e.g.
   ` + "`go build ./...`" + `); (d) Bead 1's output_files does not include a signature verification file
   (e.g. ` + "`api_check_test.go`" + `) — without compile-time type assertions for every exported function
   in the design doc's API, incorrect stubs can pass the build check silently and propagate wrong
   signatures into all subsequent Beads. Use "N/A — structural" for design_doc_reference on layout findings.

5. Bead complexity: each non-layout Bead must implement a single logical concern and is expected to
   require no more than 200 lines of new or modified code. Flag any non-layout Bead that: (a) bundles
   two or more distinct algorithms or concerns that could be independently tested; (b) clearly requires
   more than 200 lines to implement correctly. Use "N/A — structural" for design_doc_reference on
   complexity findings.

6. Paired behaviors: scan the design document for functions whose correctness is defined jointly —
   where one function's output feeds another's input, or where a round-trip invariant (e.g.
   decode(encode(x)) == x) spans two Beads. Flag if: (a) paired Beads exist but no integration
   Bead is present to verify the joint invariant; (b) an individual paired Bead's exit criteria
   include round-trip or cross-function tests instead of isolation-only checks (error handling,
   output type, bounds checks). An integration Bead that lists paired Beads' output files in its
   own output_files is an expected sequential dependency — do not flag it as an independence
   violation. Use "N/A — structural" for design_doc_reference on paired-behavior findings.

You are an independent reviewer — you did not author this decomposition.
A clean decomposition with no findings is a valid outcome. Do not fabricate findings on clean material.
Your contract does not change across debate rounds — same correctness criterion every time.

Respond with JSON only, no prose before or after:
{
  "findings": [
    {
      "bead_title": "<title of the affected Bead>",
      "issue": "<specific description of the drift or independence violation>",
      "design_doc_reference": "<exact quote or section reference, or \"N/A — structural\" for independence findings>"
    }
  ],
  "overall_verdict": "no_issues" | "issues_found"
}`

type AuditDecomposition struct{}

func (h *AuditDecomposition) Verb() string { return db.VerbAuditDecomposition }

func (h *AuditDecomposition) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	doc, err := loadDesignDoc(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	beads, err := loadCurrentBeads(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbAuditDecomposition)
	if err != nil {
		return "", err
	}

	project, err := loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	userMsg := buildAuditUserMsg(doc, beads)
	raw, err := oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: guidance.Inject(auditDecompositionSystemPrompt, project.FolderPath)},
		{Role: "user", Content: userMsg},
	}, nil)
	if err != nil {
		return "", err
	}
	return injectMechanicalFindings(raw, project.FolderPath, beads), nil
}

func buildAuditUserMsg(doc string, beads []beadState) string {
	var sb strings.Builder
	sb.WriteString("## Design Document\n\n")
	sb.WriteString(doc)
	sb.WriteString("\n\n## Decomposition\n\n")
	for i, b := range beads {
		position := fmt.Sprintf("Bead %d", i+1)
		if i == 0 {
			position += " [Layout Bead]"
		}
		fmt.Fprintf(&sb, "### %s — %s\n\n%s\n\n", position, b.Title, b.FullText)
		if len(b.OutputFiles) > 0 {
			fmt.Fprintf(&sb, "**Output files:** %s\n\n", strings.Join(b.OutputFiles, ", "))
		}
		if len(b.ExitCriteria) > 0 {
			sb.WriteString("**Exit criteria:**\n")
			for _, c := range b.ExitCriteria {
				fmt.Fprintf(&sb, "- %s\n", c)
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func (h *AuditDecomposition) Validate(raw string) (string, any) {
	var out AuditDecompositionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if out.OverallVerdict != "no_issues" && out.OverallVerdict != "issues_found" {
		return fmt.Sprintf("malformed: overall_verdict must be \"no_issues\" or \"issues_found\", got %q", out.OverallVerdict), nil
	}
	if out.OverallVerdict == "issues_found" && len(out.Findings) == 0 {
		return "malformed: overall_verdict is \"issues_found\" but findings array is empty", nil
	}
	for i, f := range out.Findings {
		if f.BeadTitle == "" {
			return fmt.Sprintf("malformed: findings[%d] missing bead_title", i), nil
		}
		if f.Issue == "" {
			return fmt.Sprintf("malformed: findings[%d] missing issue", i), nil
		}
	}
	return "valid", out
}

// Commit either skips straight to execution (no_issues) or enqueues
// RECONCILE_DECOMPOSITION (issues_found). The critique text is stored in
// handoff_attempts (via the dispatch layer) and read by RECONCILE's Run;
// audit_reconcile_rounds is written atomically by RECONCILE's Commit once
// both the critique and reconciliation are available.
func (h *AuditDecomposition) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	now := time.Now().UTC().Format(time.RFC3339)
	out := parsed.(AuditDecompositionOutput)

	if out.OverallVerdict == "no_issues" {
		return enqueueAllBeadsForExecution(ctx, tx, job.ProjectID, now)
	}

	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'pending', ?, ?)`,
		job.ProjectID, db.VerbReconcileDecomposition, now, now)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", db.VerbReconcileDecomposition, err)
	}
	return nil
}
