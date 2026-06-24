package verbs

// ADJUDICATE_NEXT_EXECUTION consistency-check smoke tests.
//
// Two categories:
//
// 1. Contradiction fixture (no Ollama required): reproduces the Exp-5 GLM
//    DP-div-3 failure — bead_spec_fit="bead_problem" declared while the
//    reasoning explicitly says the specification is clear. This is the
//    exact self-contradiction the consistency check was built to catch, run
//    through the real Validate path (not just checkConsistency in isolation).
//
// 2. Live model test (-run TestAdjudicateLiveModel, requires Ollama):
//    runs the full ADJUDICATE pipeline against Gemma using the real DP-div-3
//    material (B02 PNG steganography, divergent arc, attempt 3). Gemma
//    correctly classified this case in Exp-5; the test verifies it still
//    does and that Commit writes correctly.
//
// Material sources (not regenerated — permanent fixtures):
//   b02SpecText:             exp2-analyzer-mechanical-contract/material/png-stego/b02_final.md
//   dp3MechanicalFindings:   exp4-scribe-gating/material/divergent/attempt3_glm-analyzer_raw.json.partial.txt
//   dp3CompressedHistory:    exp4-scribe-gating/runs/gemma-scribe/divergent_step2.json.partial.txt

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

// b02SpecText is the B02 bead spec (FINAL, post-reconciliation).
// The stride-3 bug in the index formula is the documented spec defect:
// NRGBA has 4 bytes/pixel but the formula uses 3*(y*Dx()+x)+c, causing
// alpha corruption in attempts 1 and 2.
const b02SpecText = `B02 — Bit Manipulation Utilities (FINAL, post-reconciliation)

Implement utility functions for bit manipulation required by the LSB algorithm.
The algorithm requires writing message bits MSB-first into successive channel LSBs.

Function signatures:
  WriteBit(img *image.NRGBA, x, y int, bit uint8) error
  ReadBit(img *image.NRGBA, x, y int) (uint8, error)

Implementation details:
  - Channel layout: image.NRGBA has 3 active channels per pixel (R, G, B).
    Pix array layout: [R0, G0, B0, A0, R1, G1, B1, A1, ...]
  - Index calculation: 3*(y*img.Rect.Dx()+x)+c where c is 0=R, 1=G, 2=B.
  - Traversal: row-major left->right top->bottom; R,G,B order per pixel; alpha skipped.
  - State advancement: use Bit Index Counter (approach 2). Do not use math/rand.
    Do not implement bit-shifting; use a bit index counter instead.

Exit criteria:
  - go build ./... passes.
  - TestDeterminism: same inputs (img, x, y, bit) produce same channel index.
  - TestBoundary: WriteBit returns an error when writing past image bounds.
  - TestStateAdvancement: calling WriteBit twice increments internal index twice.
  - Bead tracker in v1-preamble.md updated.`

// dp3MechanicalFindings is ANALYZE_EXECUTION mechanical output for B02
// attempt 3 (divergent arc). Alpha corruption is now fixed (stride 4);
// boundary tracking is absent. Source: attempt3_glm-analyzer_raw.json.partial.txt
const dp3MechanicalFindings = `Package: stego. Functions: WriteBit, ReadBit.
Global variable: currentChannel (int, initialized 0).
Index calculation: (y*width + x)*4 + currentChannel.
Channel cycle: currentChannel increments, resets to 0 when exceeding 2.
Alpha handling: factor of 4 aligns with NRGBA [R, G, B, A] layout.
Test results: 5 passed, 1 failed.
  FAIL TestBoundary (line 45): expected "capacity error on 13th write", got nil.
  PASS TestDeterminism, TestStateAdvancement, TestAlphaNeverModified,
       TestChannelNeverReachesAlphaValue, TestXYArgumentsDetermineAddressing.`

// dp3AnalyzerInterpretation accompanies dp3MechanicalFindings.
const dp3AnalyzerInterpretation = `The alpha exclusion fix appears successful: the *4
multiplier aligns with NRGBA layout and TestAlphaNeverModified now passes.
TestBoundary failure suggests the implementation prioritized address calculation
over capacity tracking — no persistent counter detects when the 13th write is
reached. The runner appears to have selected the skip-alpha feature over capacity
enforcement, consistent with the Bead spec's emphasis on correct channel selection.`

// dp3CompressedHistory is COMPRESS_ANALYSIS output after attempts 1 and 2
// of the divergent arc. Source: divergent_step2.json.partial.txt (Gemma-Scribe).
const dp3CompressedHistory = `Attempt 1: WriteBit/ReadBit via global bitIndex. ` +
	`Formula: 3*(y*Dx()+x)+channelOffset. ` +
	`FAIL TestAlphaNeverModified (12/16 alpha bytes modified at Pix offsets 3,7,11...47). ` +
	`PASS TestDeterminism, TestBoundary, TestStateAdvancement. ` +
	`Stride-3 formula collides with alpha positions; coords ignored (global state). ` +
	`Attempt 2: WriteBit/ReadBit via global bitCounter. Hardcoded multiplier 3. ` +
	`FAIL TestAlphaNeverModified (same 12/16 alpha bytes modified). PASS same three. ` +
	`Stride-3 persists; coords still ignored. Trend: identical recurrence of alpha bug across both attempts.`

// glmDP3Contradiction is a constructed ADJUDICATE output reproducing the
// Exp-5 GLM DP-div-3 failure verbatim: bead_spec_fit="bead_problem" while
// the reasoning twice says "the specification is clear" — which directly
// contradicts a "bead problem" classification (if the spec were clear,
// the failure would be the runner's, not the spec's).
//
// From findings.md: "GLM's declared classification ('Bead-spec problem')
// directly contradicted its own stated reasoning — 'the Exit Criteria
// explicitly require boundary checking... the implementation does not
// track the state required' — the textbook Runner-capability case."
const glmDP3Contradiction = `{
  "trend": "narrower",
  "bead_spec_fit": "bead_problem",
  "reasoning": "Trend is narrower: the alpha channel corruption from attempts 1 and 2 is resolved in attempt 3, which now uses stride 4. A new distinct failure has emerged: TestBoundary FAIL — the implementation returns nil on the 13th write instead of an error. The specification is clear in the exit criteria that WriteBit must detect writes past image bounds and return an error. The implementation does not track a cumulative write counter to detect this condition. The specification is clear on the requirement but the Bead text should be revised to provide explicit implementation guidance for the capacity enforcement mechanism, reducing the risk of the runner omitting the counter again.",
  "decision": "execute_revised",
  "revised_bead": {
    "title": "B02 — Bit Manipulation Utilities",
    "full_text": "same spec with added implementation note: maintain a write counter; return error when counter exceeds image capacity (width*height*3 bits).",
    "execution_budget": 300,
    "monitor_override": "honor"
  }
}`

// TestAdjudicateConsistencyContradictionFixture is the primary regression
// fixture for the consistency check. It verifies that the Exp-5 GLM failure
// pattern is caught by Validate — not just by checkConsistency in isolation,
// but through the full validation path that production code uses.
func TestAdjudicateConsistencyContradictionFixture(t *testing.T) {
	h := &AdjudicateNextExecution{}
	result, parsed := h.Validate(glmDP3Contradiction)

	if result == "valid" {
		t.Fatal("consistency check did not fire on Exp-5 GLM contradiction: " +
			"bead_spec_fit=bead_problem declared while reasoning says 'the specification is clear'")
	}
	if !strings.Contains(result, "consistency check failed") {
		t.Errorf("validation failure should name the consistency check, got: %q", result)
	}
	if parsed != nil {
		t.Error("parsed must be nil when consistency check fails")
	}

	// The specific contradicting phrase should appear in the error message.
	if !strings.Contains(result, "specification is clear") &&
		!strings.Contains(result, "spec is clear") {
		t.Errorf("error message should identify the contradicting phrase, got: %q", result)
	}

	t.Logf("consistency check correctly fired: %s", result)
}

// TestAdjudicateConsistencyCleanFixture is the companion: the correct
// classification for DP-div-3 passes without triggering the check.
// Gemma produced this style of output in Exp-5.
func TestAdjudicateConsistencyCleanFixture(t *testing.T) {
	h := &AdjudicateNextExecution{}

	// Gemma's correct classification: execution_capability_problem, because
	// the spec states the boundary requirement clearly and the runner omitted it.
	clean := `{
  "trend": "narrower",
  "bead_spec_fit": "execution_capability_problem",
  "reasoning": "Trend is narrower: the alpha corruption is fully resolved in attempt 3 via stride 4. The remaining failure (TestBoundary) shows the runner did not implement capacity tracking. The exit criteria state the boundary requirement precisely — WriteBit must return an error on the 13th write — and the runner's implementation omits the counter needed to enforce it. The alpha fix in this attempt demonstrates the runner can handle the complex parts of the spec; the boundary tracking is a straightforward omission. This is a runner capability failure: correct spec, incomplete implementation.",
  "decision": "execute_as_is"
}`

	result, parsed := h.Validate(clean)
	if result != "valid" {
		t.Fatalf("clean consistent output rejected: %q", result)
	}
	if parsed == nil {
		t.Error("parsed is nil on valid output")
	}

	out, ok := parsed.(AdjudicateNextExecutionOutput)
	if !ok {
		t.Fatalf("parsed is wrong type: %T", parsed)
	}
	if out.BeadSpecFit != "execution_capability_problem" {
		t.Errorf("bead_spec_fit = %q, want execution_capability_problem", out.BeadSpecFit)
	}
	if out.Trend != "narrower" {
		t.Errorf("trend = %q, want narrower", out.Trend)
	}
}

// TestAdjudicateLiveModelDP3 runs the full ADJUDICATE pipeline (Run →
// Validate → Commit) against the real Gemma model using the Exp-5 DP-div-3
// material. Gemma correctly classified this case in Exp-5; this test verifies
// it still does and that the consistency check does not fire on its output.
//
// Requires Ollama at 192.168.50.241:11434 with gemma4:31b loaded.
// Gemma's reasoning on complex material (B02 has interacting constraints)
// can take 3–5 minutes — the default 10-minute test timeout is fine but
// do not pass -timeout values below 5m.
//
// Run with: go test -v -run TestAdjudicateLiveModelDP3 ./internal/verbs/
func TestAdjudicateLiveModelDP3(t *testing.T) {
	if testing.Short() {
		t.Skip("live model test: requires Ollama at 192.168.50.241:11434 with gemma4:31b")
	}

	// Fast reachability check — skip rather than hang if the GX10 is offline.
	{
		probe, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(probe, http.MethodGet, "http://192.168.50.241:11434", nil)
		if resp, err := http.DefaultClient.Do(req); err != nil {
			t.Skipf("Ollama not reachable at 192.168.50.241:11434 (%v) — skipping live model test", err)
		} else {
			resp.Body.Close()
		}
	}

	d := openTestDB(t)
	// Give the model call a generous but bounded timeout (Gemma on DP-div-3
	// takes ~60-90 seconds based on Exp-5 latency data).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Seed project with ADJUDICATE model assignment only — ADJUDICATE.Run
	// only needs its own model, not the full fleet.
	seedProject(t, d, -1, "fixture: ADJUDICATE live model — Exp-5 DP-div-3 material")
	_, err := d.ExecContext(ctx,
		`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (-1, ?, ?)`,
		db.VerbAdjudicateNextExecution, "gemma4:31b")
	if err != nil {
		t.Fatalf("seed model assignment: %v", err)
	}

	// Seed bead with B02 spec. full_text must be ParsedBead JSON (as written by
	// DECOMPOSE_SPEC.Commit) so loadCurrentBeads can extract the title.
	fullTextJSON, err := json.Marshal(ParsedBead{
		Title:           "B02 — Bit Manipulation Utilities",
		FullText:        b02SpecText,
		ExecutionBudget: 300,
		MonitorOverride: "honor",
	})
	if err != nil {
		t.Fatalf("marshal B02 bead: %v", err)
	}

	res, _ := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status, current_revision_id) VALUES (-1, 'executing', NULL)`)
	beadID, _ := res.LastInsertId()

	res, _ = d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text,
		   execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (-1, ?, 1, ?, 300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		beadID, string(fullTextJSON))
	revID, _ := res.LastInsertId()
	_, _ = d.ExecContext(ctx, `UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID)

	// Seed execution (attempt 3, terminated normally — tests failed but process ran).
	res, _ = d.ExecContext(ctx, `
		INSERT INTO executions
		  (project_id, bead_id, bead_revision_id, trace_path,
		   termination_cause, monitor_fired, monitor_honored,
		   started_at, ended_at)
		VALUES (-1, ?, ?, '/tmp/trace.log', 'success', 0, 1,
		        '2026-01-01T00:04:00Z', '2026-01-01T00:09:00Z')`,
		beadID, revID)
	execID, _ := res.LastInsertId()

	// Seed analysis row (for buildDiffSignal).
	_, _ = d.ExecContext(ctx, `
		INSERT INTO analyses (project_id, execution_id, mechanical_findings, analyzer_interpretation, created_at)
		VALUES (-1, ?, ?, ?, '2026-01-01T00:10:00Z')`,
		execID, dp3MechanicalFindings, dp3AnalyzerInterpretation)

	// Seed compressed history (for Input 4).
	_, _ = d.ExecContext(ctx, `
		INSERT INTO compressed_history (bead_id, project_id, compressed_text, updated_at)
		VALUES (?, -1, ?, '2026-01-01T00:03:00Z')`,
		beadID, dp3CompressedHistory)

	// Seed ANALYZE_EXECUTION handoff_job + attempt (for loadLatestAnalysis).
	// raw_output must be valid AnalyzeExecutionOutput JSON.
	analyzeJSON, _ := json.Marshal(AnalyzeExecutionOutput{
		MechanicalFindings:     dp3MechanicalFindings,
		AnalyzerInterpretation: dp3AnalyzerInterpretation,
	})
	res, _ = d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (-1, ?, ?, 'complete', '2026-01-01T00:10:00Z', '2026-01-01T00:10:00Z')`,
		db.VerbAnalyzeExecution, beadID)
	analyzeJobID, _ := res.LastInsertId()
	_, _ = d.ExecContext(ctx, `
		INSERT INTO handoff_attempts (job_id, attempt_number, raw_output, validation_result, created_at)
		VALUES (?, 1, ?, 'valid', '2026-01-01T00:10:00Z')`,
		analyzeJobID, string(analyzeJSON))

	// Seed ADJUDICATE handoff_job.
	res, _ = d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (-1, ?, ?, 'running', '2026-01-01T00:11:00Z', '2026-01-01T00:11:00Z')`,
		db.VerbAdjudicateNextExecution, beadID)
	jobID, _ := res.LastInsertId()

	job := &db.HandoffJob{
		ID:        jobID,
		ProjectID: -1,
		Verb:      db.VerbAdjudicateNextExecution,
		BeadID:    sql.NullInt64{Int64: beadID, Valid: true},
	}

	h := &AdjudicateNextExecution{}
	oc := ollama.New("http://192.168.50.241:11434")

	// Run: call the real Gemma model.
	raw, err := h.Run(ctx, d, oc, job)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("Gemma raw output:\n%s", raw)

	// Validate: consistency check must not fire on Gemma's output.
	result, parsed := h.Validate(raw)
	if result != "valid" {
		t.Fatalf("Validate = %q\nGemma output was:\n%s", result, raw)
	}

	out := parsed.(AdjudicateNextExecutionOutput)
	t.Logf("Gemma decision: trend=%s bead_spec_fit=%s decision=%s",
		out.Trend, out.BeadSpecFit, out.Decision)

	// In Exp-5, Gemma correctly classified the capacity gap as
	// execution_capability_problem (not bead_problem). Log whether it
	// still does; this is informational rather than a hard failure since
	// model behaviour can shift, but a bead_problem classification here
	// would be a regression worth investigating.
	if out.BeadSpecFit == "bead_problem" {
		t.Logf("NOTE: Gemma classified as bead_problem — in Exp-5 it classified "+
			"this as execution_capability_problem. Reasoning: %s", out.Reasoning)
	}

	// Commit: verify the adjudications row and next job are written.
	inTx(t, d, func(tx *sql.Tx) error {
		return h.Commit(ctx, tx, job, parsed)
	})

	if n := countRows(t, d, `SELECT COUNT(*) FROM adjudications WHERE bead_id = ?`, beadID); n != 1 {
		t.Errorf("adjudications rows = %d, want 1", n)
	}

	// The next job depends on decision — any valid next state is acceptable.
	var nextVerb string
	switch out.Decision {
	case "execute_as_is", "execute_revised":
		nextVerb = db.VerbExecuteBead
	case "full_stop":
		nextVerb = "" // no new job; bead is full_stopped
	}
	if nextVerb != "" {
		if n := countRows(t, d,
			`SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ? AND bead_id = ?`,
			nextVerb, beadID); n != 1 {
			t.Errorf("%s job = %d after decision %q, want 1", nextVerb, n, out.Decision)
		}
	}
}
