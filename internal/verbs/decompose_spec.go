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

func decomposeSpecSystemPrompt() string {
	return fmt.Sprintf(`You decompose a design document into a list of Beads — well-scoped, independently executable units of work, each with a clear done-condition.

**Layout Bead — always first:** The very first Bead must be a layout Bead. Its sole job
is to establish the project's complete file and package structure: correct directory layout,
module files, and stub implementations — every exported function, type, constant, and error
variable declared with correct signatures but containing no logic (function bodies return zero
values or a "not implemented" error). The layout Bead's exit criterion must verify that
` + "`go build ./...`" + ` (or equivalent) passes with the stubs in place. All subsequent Beads
fill in stubs from the layout Bead — they do not create new source files. File overlap between
the layout Bead and any other Bead is expected and will not be flagged by AUDIT as an
independence violation.

The layout Bead must include a signature verification file in its output_files (e.g.
` + "`api_check_test.go`" + `). This file contains only compile-time type assertions for each exported
function declared in the design doc's API/scope section — one assignment per function:

  var _ func(<param types>) <return types> = FuncName

If the stubs carry the wrong signature, ` + "`go build ./...`" + ` fails immediately. This locks the
API before any logic Bead runs and prevents signature drift across the project.

**Single logical concern:** Each non-layout Bead must implement exactly one coherent unit of
functionality. Two algorithms that happen to be short are still two concerns if they can be
independently tested and implemented. When in doubt, split.

**200-line cap:** Each non-layout Bead's implementation is expected to require no more than
200 lines of new or modified code. If a Bead's scope would require more, split it. The layout
Bead is exempt from this cap.

**Independence:** Each non-layout Bead must be independently executable — it must not assume
that code written by other non-layout Beads already exists. The only permitted sequential
dependency is on the layout Bead.

**Paired behaviors and integration Beads:** Before finalizing your decomposition, scan the
design document for paired behaviors — functions whose correctness is defined jointly rather
than independently. The signal is any of: (a) one function's output is the direct input of
another (encode/decode, serialize/deserialize, compress/decompress, encrypt/decrypt,
push/pop); (b) the spec uses language like "round-trip", "recover", "reconstruct", or
"inverse"; (c) a correctness statement spans two functions (e.g. "encoding then decoding
returns the original value"). When paired behaviors are present:
- Each individual Bead's exit criteria must only verify what that function can demonstrate
  in isolation: error handling, output type, bounds or capacity checks. Do not include
  round-trip or cross-function tests in individual Bead exit criteria.
- Add a dedicated integration Bead immediately after the paired Beads. Its sole purpose is
  verifying the joint correctness invariant (round-trip tests, inverse property checks). It
  writes only test files. Its sequential dependency on the paired Beads' output files is
  expected and will not be flagged by AUDIT as an independence violation.

For every Bead you issue you must set:
- monitor_override: "honor" (MONITOR_EXECUTION may terminate this Bead on loop detection) or "ignore" (loop detection signal is suppressed — use only for legitimately repetitive work)
- output_files: a non-empty list of file paths this Bead will create or modify (e.g. ["main.go", "go.mod"]).
  This field drives the independence check in AUDIT_DECOMPOSITION: if two non-layout Beads share
  a file in output_files without a clearly documented sequential dependency, AUDIT will flag it as
  a non-independence violation. Be precise — list only files this Bead actually writes.
- exit_criteria: a non-empty list of concrete, runnable checks that define when this Bead is done.
  Each entry must be a specific, executable verification step — a shell command with expected output,
  a test that must pass, or a measurable observable result. Vague statements ("review the code",
  "ensure correctness") are not acceptable. If you cannot write a runnable exit criterion for a Bead,
  that is a signal the Bead is scoped too narrowly to be independently verifiable — merge it with
  a related Bead that produces a testable artifact.
  If any exit criterion runs a test command (e.g. ` + "`go test`" + `, ` + "`pytest`" + `, ` + "`npm test`" + `), at least one test
  file (e.g. ` + "`*_test.go`" + `, ` + "`test_*.py`" + `) must appear in ` + "`output_files`" + `. A test command with no
  owned test file reports "no test files" and exits 0 without running anything — the criterion
  is vacuously satisfied and provides no verification.

Surface ambiguities in the design doc explicitly in the ambiguities field. Do not silently resolve them.

Respond with JSON only, no prose before or after:
{
  "beads": [
    {
      "title": "<short identifier, unique within this decomposition>",
      "full_text": "<complete, self-contained Bead specification>",
      "monitor_override": "honor" | "ignore",
      "output_files": ["<file path>", ...],
      "exit_criteria": ["<runnable check>", ...]
    }
  ],
  "ambiguities": ["<any unresolved ambiguities in the design doc>"]
}`)
}

type DecomposeSpec struct {
	budgetDefault int
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
	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: guidance.Inject(decomposeSpecSystemPrompt(), project.FolderPath)},
		{Role: "user", Content: doc},
	}, nil)
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

	for _, pb := range out.Beads {
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
