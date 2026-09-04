package verbs

// Regression tests for the reasoning-model tool-loop spiral fix (see
// tool_loop_recovery.go's doc comment and ~/Documents/ratchet-projects/
// framework-prompt-numpredict-spiral.md). Corpus evidence for the two shapes
// exercised below (isLengthCapEmpty recovers vs. genuinely spirals) came from
// reading captured `thinking` text in qual-corpus-p48/-baseline-9/-baseline-10;
// these fixtures reproduce the *signature* (done_reason, empty content, tool
// call presence/absence, request count/shape) that drove the fix, not the
// literal captured transcripts.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

func TestIsLengthCapEmpty(t *testing.T) {
	cases := []struct {
		name string
		msg  ollama.Message
		want bool
	}{
		{"length+empty is the target condition", ollama.Message{DoneReason: "length"}, true},
		{"length but has content (not empty)", ollama.Message{DoneReason: "length", Content: "partial"}, false},
		{"length but has a tool call", ollama.Message{DoneReason: "length", ToolCalls: []ollama.ToolCall{{}}}, false},
		{"stop + empty is an ordinary final turn, not this condition", ollama.Message{DoneReason: "stop"}, false},
		{"whitespace-only content still counts as empty", ollama.Message{DoneReason: "length", Content: "   \n"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isLengthCapEmpty(c.msg); got != c.want {
				t.Errorf("isLengthCapEmpty(%+v) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}

func TestWithNumPredict(t *testing.T) {
	if got := withNumPredict(nil, 16384); got == nil || got.NumPredict != 16384 {
		t.Fatalf("withNumPredict(nil, 16384) = %+v, want NumPredict=16384", got)
	}
	// Preserves every other field on a non-nil input — this is what lets
	// REFINE_TESTS_JUDGE's OmitFormat survive a length-cap budget bump.
	base := &ollama.Options{OmitFormat: true, Temperature: 0.7}
	got := withNumPredict(base, 16384)
	if !got.OmitFormat || got.Temperature != 0.7 || got.NumPredict != 16384 {
		t.Fatalf("withNumPredict(%+v, 16384) = %+v, want OmitFormat/Temperature preserved + NumPredict set", base, got)
	}
	// Must not mutate the caller's Options.
	if base.NumPredict != 0 {
		t.Errorf("withNumPredict mutated the input Options: %+v", base)
	}
}

func seedCritiqueFixture(t *testing.T, srvURL string) (*db.DB, *db.HandoffJob, *ollama.Client) {
	t.Helper()
	d := openTestDB(t)
	seedProject(t, d, -1, t.Name())
	beadID, _ := seedBead(t, d, -1, "empty-file-fixture")
	if _, err := d.ExecContext(context.Background(),
		`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (-1, ?, 'test-model')`,
		db.VerbRefineTestsCritique,
	); err != nil {
		t.Fatalf("seed verb_model_assignments: %v", err)
	}
	job := seedJob(t, d, -1, db.VerbRefineTestsCritique, sql.NullInt64{Int64: beadID, Valid: true})
	return d, job, ollama.New(srvURL)
}

// lengthCapEmptyBody / stopEmptyBody / lengthCapToolCallBody are raw Ollama
// /api/chat non-streaming response bodies for the three request shapes these
// tests distinguish.
func lengthCapEmptyBody() []byte {
	return []byte(`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"length"}`)
}

func toolCallBody() []byte {
	return []byte(`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"run_go_snippet","arguments":{"source":"package main\nfunc main(){}","for_case":"x"}}}]},"done":true,"done_reason":"stop"}`)
}

// TestRefineTestsCritiqueRecoversFromOneLengthCapTurn reproduces the
// REFINE_TESTS_WRITE/gemma4:31b shape from p48 bead 318: the first turn hits
// the token cap mid-reasoning (done_reason=length, empty), and the very next
// turn — given the "stop analyzing" nudge and (per the request body check
// below) a doubled token budget — answers cleanly. Must succeed in exactly 2
// requests, not spend the rest of the turn budget or fall through to the
// stripped last-resort path.
func TestRefineTestsCritiqueRecoversFromOneLengthCapTurn(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var gotBody map[string]any
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		bodies = append(bodies, gotBody)
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			w.Write(lengthCapEmptyBody())
		case 2:
			// The recovery turn: CRITIQUE's mandatory-verification gate still
			// applies once the model is actually producing output again, so
			// it verifies a case here before finalizing on the next turn.
			w.Write(toolCallBody())
		default:
			w.Write([]byte(`{"message":{"role":"assistant","content":"{\"all_correct\":true,\"findings\":[],\"verified_functions\":[],\"summary\":\"ok\"}"},"done":true,"done_reason":"stop"}`))
		}
	}))
	defer srv.Close()

	d, job, oc := seedCritiqueFixture(t, srv.URL)
	h := &RefineTestsCritique{}
	out, err := h.Run(context.Background(), d, oc, job)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("got %d requests, want exactly 3 (one length-cap turn, one recovered tool call, one final verdict)", len(bodies))
	}
	if _, parsed := h.Validate(out); parsed == nil {
		t.Fatalf("Validate rejected recovered output: %q", out)
	}
	// The retry turn (request 2, following the length-cap turn) must ask for
	// the budget-appropriate response, not repeat the ordinary "go verify
	// something" nudge (which would push toward MORE reasoning, the opposite
	// of what a budget-exhausted turn needs).
	msgs := bodies[1]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["content"] != lengthCapStopReasoningNudge {
		t.Errorf("retry turn's trailing message = %q, want the stop-reasoning nudge", last["content"])
	}
	// And it must have asked for more room than the base per-turn cap.
	opts := bodies[1]["options"].(map[string]any)
	if np, _ := opts["num_predict"].(float64); int(np) != lengthCapRetryNumPredict {
		t.Errorf("retry turn num_predict = %v, want %d", opts["num_predict"], lengthCapRetryNumPredict)
	}
}

// TestRefineTestsCritiqueTwoLengthCapTurnsForceStrippedFinalAnswer
// reproduces the REFINE_TESTS_CRITIQUE/qwen3:32b shape from baseline-9 bead
// 315: the length-cap turn recurs even after the budget-bump retry — the
// corpus showed this is a genuinely non-convergent repeating reasoning loop
// (verbatim-repeated sentences in the captured thinking text), not a model
// that just needed a little more room. The 3rd request must drop the entire
// accumulated transcript (down to the original system+user framing) instead
// of feeding the same growing context into the same loop a third time.
func TestRefineTestsCritiqueTwoLengthCapTurnsForceStrippedFinalAnswer(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var gotBody map[string]any
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		bodies = append(bodies, gotBody)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) <= 2 {
			w.Write(lengthCapEmptyBody())
			return
		}
		w.Write([]byte(`{"message":{"role":"assistant","content":"{\"all_correct\":true,\"findings\":[],\"verified_functions\":[],\"summary\":\"recovered\"}"},"done":true,"done_reason":"stop"}`))
	}))
	defer srv.Close()

	d, job, oc := seedCritiqueFixture(t, srv.URL)
	h := &RefineTestsCritique{}
	out, err := h.Run(context.Background(), d, oc, job)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("got %d requests, want exactly 3 (two length-cap turns + one stripped final answer)", len(bodies))
	}
	if _, parsed := h.Validate(out); parsed == nil {
		t.Fatalf("Validate rejected stripped-final-answer output: %q", out)
	}
	// The stripped call must drop the accumulated tool-loop transcript —
	// exactly the original system+user pair, nothing from the two dead turns.
	msgs := bodies[2]["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("stripped final turn carried %d messages, want exactly 2 (system+user, transcript dropped)", len(msgs))
	}
	if tools, _ := bodies[2]["tools"].([]any); len(tools) != 0 {
		t.Errorf("stripped final turn still offered tools: %v, want none", bodies[2]["tools"])
	}
	opts := bodies[2]["options"].(map[string]any)
	if np, _ := opts["num_predict"].(float64); int(np) != lengthCapRetryNumPredict {
		t.Errorf("stripped final turn num_predict = %v, want %d", opts["num_predict"], lengthCapRetryNumPredict)
	}
}

// TestRefineTestsCritiqueTurnCapFallthroughForcesFinalize reproduces the
// second, distinct gap found while reading baseline-10 job 2014's captured
// attempts: a loop can exhaust its entire turn budget while every turn
// (including the last) legitimately calls a tool, so no turn ever reaches
// the "give your final answer" branch — previously this fell through to
// returning empty leftover content, which Validate then rejected as
// malformed JSON with no indication why. The loop must now force one more
// explicit turn asking for the decision directly.
func TestRefineTestsCritiqueTurnCapFallthroughForcesFinalize(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var gotBody map[string]any
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		bodies = append(bodies, gotBody)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) <= snippetVerificationTurns {
			// Every in-budget turn calls the tool — never a zero-tool-call
			// "final answer" turn, so the loop must exhaust its budget.
			w.Write(toolCallBody())
			return
		}
		w.Write([]byte(`{"message":{"role":"assistant","content":"{\"all_correct\":true,\"findings\":[],\"verified_functions\":[],\"summary\":\"finalized\"}"},"done":true,"done_reason":"stop"}`))
	}))
	defer srv.Close()

	d, job, oc := seedCritiqueFixture(t, srv.URL)
	h := &RefineTestsCritique{}
	out, err := h.Run(context.Background(), d, oc, job)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(bodies) != snippetVerificationTurns+1 {
		t.Fatalf("got %d requests, want exactly %d (full turn budget + 1 forced finalize turn)",
			len(bodies), snippetVerificationTurns+1)
	}
	if _, parsed := h.Validate(out); parsed == nil {
		t.Fatalf("Validate rejected forced-finalize output: %q", out)
	}
	msgs := bodies[snippetVerificationTurns]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["content"] != turnCapFinalizeNudge {
		t.Errorf("forced finalize turn's trailing message = %q, want turnCapFinalizeNudge", last["content"])
	}
}

// TestRefineTestsJudgeLengthCapRetryPreservesOmitFormat exercises the same
// recovery path through REFINE_TESTS_JUDGE specifically, because unlike
// ADJUDICATE/CRITIQUE its base Options is non-nil (&ollama.Options{OmitFormat:
// true} — see its Run() doc comment on why the bare grammar is required for
// this verb's model). withNumPredict must preserve that flag when bumping the
// budget, not silently drop back to the default `format:"json"` grammar that
// this verb has deliberately opted out of.
func TestRefineTestsJudgeLengthCapRetryPreservesOmitFormat(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var gotBody map[string]any
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		bodies = append(bodies, gotBody)
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			w.Write(lengthCapEmptyBody())
		case 2:
			// Establishes usedTool=true, which JUDGE requires before a
			// zero-tool-call turn is accepted as final.
			w.Write(toolCallBody())
		default:
			w.Write([]byte(`{"message":{"role":"assistant","content":"{\"decision\":\"approved\",\"summary\":\"ok\"}"},"done":true,"done_reason":"stop"}`))
		}
	}))
	defer srv.Close()

	d, job, oc := seedJudgeFixture(t, srv.URL)
	h := &RefineTestsJudge{}
	out, err := h.Run(context.Background(), d, oc, job)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("got %d requests, want exactly 3", len(bodies))
	}
	if _, parsed := h.Validate(out); parsed == nil {
		t.Fatalf("Validate rejected recovered output: %q", out)
	}
	if f, present := bodies[1]["format"]; present && f != nil {
		t.Errorf("retry turn carries format=%#v, want the key absent (OmitFormat must survive withNumPredict)", f)
	}
	opts := bodies[1]["options"].(map[string]any)
	if np, _ := opts["num_predict"].(float64); int(np) != lengthCapRetryNumPredict {
		t.Errorf("retry turn num_predict = %v, want %d", opts["num_predict"], lengthCapRetryNumPredict)
	}
}

func seedAdjudicateFixture(t *testing.T, srvURL string) (*db.DB, *db.HandoffJob, *ollama.Client) {
	t.Helper()
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, t.Name())
	if _, err := d.ExecContext(ctx,
		`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (-1, ?, 'test-model')`,
		db.VerbAdjudicateNextExecution,
	); err != nil {
		t.Fatalf("seed verb_model_assignments: %v", err)
	}
	beadID, revID := seedBead(t, d, -1, "adjudicate-fixture")
	execID := seedExecution(t, d, -1, beadID, revID, "timeout", nil)
	analyzeJSON, _ := json.Marshal(AnalyzeExecutionOutput{
		MechanicalFindings: "go test timed out after budget", AnalyzerInterpretation: "runner capability issue",
	})
	res, err := d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (-1, ?, ?, 'complete', '2026-01-01T00:10:00Z', '2026-01-01T00:10:00Z')`,
		db.VerbAnalyzeExecution, beadID)
	if err != nil {
		t.Fatalf("seed ANALYZE_EXECUTION job: %v", err)
	}
	analyzeJobID, _ := res.LastInsertId()
	if _, err := d.ExecContext(ctx, `
		INSERT INTO handoff_attempts (job_id, attempt_number, raw_output, validation_result, created_at)
		VALUES (?, 1, ?, 'valid', '2026-01-01T00:10:00Z')`,
		analyzeJobID, string(analyzeJSON)); err != nil {
		t.Fatalf("seed ANALYZE_EXECUTION attempt: %v", err)
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO analyses (project_id, execution_id, mechanical_findings, analyzer_interpretation, created_at)
		VALUES (-1, ?, ?, ?, '2026-01-01T00:10:00Z')`,
		execID, "go test timed out after budget", "runner capability issue"); err != nil {
		t.Fatalf("seed analyses: %v", err)
	}
	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
	return d, job, ollama.New(srvURL)
}

// TestAdjudicateRecoversFromOneLengthCapTurn is the ADJUDICATE-specific
// instance of the corpus-driven fix — this is the verb the original
// baseline-10 job 2014 incident hit (qwen3.6:35b-a3b, 3/3 independent
// top-level attempts each burning turns 1-3 of 6 on this exact condition
// before running out of turns). One length-cap-empty turn followed by a
// real tool call and a final decision must resolve in 3 requests, not
// escalate.
func TestAdjudicateRecoversFromOneLengthCapTurn(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var gotBody map[string]any
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		bodies = append(bodies, gotBody)
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			w.Write(lengthCapEmptyBody())
		case 2:
			w.Write(toolCallBody())
		default:
			w.Write([]byte(`{"message":{"role":"assistant","content":"{\"trend\":\"same\",\"bead_spec_fit\":\"execution_capability_problem\",\"reasoning\":\"the runner timed out\",\"decision\":\"execute_as_is\"}"},"done":true,"done_reason":"stop"}`))
		}
	}))
	defer srv.Close()

	d, job, oc := seedAdjudicateFixture(t, srv.URL)
	h := &AdjudicateNextExecution{}
	out, err := h.Run(context.Background(), d, oc, job)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("got %d requests, want exactly 3", len(bodies))
	}
	if result, parsed := h.Validate(out); parsed == nil {
		t.Fatalf("Validate rejected recovered output (%q): %q", result, out)
	}
	msgs := bodies[1]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["content"] != lengthCapStopReasoningNudge {
		t.Errorf("retry turn's trailing message = %q, want the stop-reasoning nudge", last["content"])
	}
}
