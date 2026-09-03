package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

func seedJudgeFixture(t *testing.T, srvURL string) (*db.DB, *db.HandoffJob, *ollama.Client) {
	t.Helper()
	d := openTestDB(t)
	seedProject(t, d, -1, t.Name())
	beadID, _ := seedBead(t, d, -1, "game-mechanics")
	if _, err := d.ExecContext(context.Background(),
		`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (-1, ?, 'test-model')`,
		db.VerbRefineTestsJudge,
	); err != nil {
		t.Fatalf("seed verb_model_assignments: %v", err)
	}
	job := seedJob(t, d, -1, db.VerbRefineTestsJudge, sql.NullInt64{Int64: beadID, Valid: true})
	return d, job, ollama.New(srvURL)
}

// TestRefineTestsJudgeOmitsFormatConstraint locks in the 2026-09-03 bakeoff
// decision: JUDGE sends NO `format` constraint (Options.OmitFormat). History:
// a field-presence JSON Schema was added for connect-four-v2 bead 56
// (2026-08-27, gemma4:31b omitted "summary" 3/3) — but the qualification
// bakeoff showed the grammar's effect is model-specific. gemma needs it;
// qwen3.6:35b-a3b (the current JUDGE model, per db/assignments.go) fights it
// (12% done_reason=length dead turns, 50% baseline agreement with the schema
// vs 62% and 0 dead turns without). Validate still enforces non-empty
// "summary" + a valid decision, so an omitted field triggers the normal retry.
// This test guards against a refactor silently reintroducing a format arg.
//
// The fake server simulates a real two-turn round trip (a tool call, then
// the final decision) because Run mandates at least one run_go_snippet call
// before finalizing (see TestRefineTestsJudgeRequiresRunGoSnippetVerification).
func TestRefineTestsJudgeOmitsFormatConstraint(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var gotBody map[string]any
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		bodies = append(bodies, gotBody)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			w.Write([]byte(`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"run_go_snippet","arguments":{"source":"package main\nfunc main(){}"}}}]},"done":true}`))
			return
		}
		w.Write([]byte(`{"message":{"role":"assistant","content":"{\"decision\":\"approved\",\"summary\":\"ok\"}"},"done":true}`))
	}))
	defer srv.Close()

	d, job, oc := seedJudgeFixture(t, srv.URL)
	h := &RefineTestsJudge{}
	if _, err := h.Run(context.Background(), d, oc, job); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(bodies) < 2 {
		t.Fatalf("got %d requests, want at least 2 (tool call turn + final decision turn)", len(bodies))
	}

	// No turn should carry a `format` constraint — OmitFormat drops the key
	// entirely from the request body.
	for i, b := range bodies {
		if f, present := b["format"]; present && f != nil {
			t.Errorf("request %d carries format=%#v, want the key absent (OmitFormat)", i, f)
		}
	}
}

// TestRefineTestsJudgeRequiresRunGoSnippetVerification locks in the fix for
// a real live bug (tictactoe-v1 bead 81, 2026-08-28): with no verification
// tool at all, JUDGE confidently asserted "HTML escaping of the apostrophe
// is not required in this context" — false, and a direct reversal of
// ADJUDICATE_NEXT_EXECUTION's own earlier, correct, verified diagnosis of
// the same test, sending the bead back into the exact failure it had just
// been fixed out of. A JUDGE attempt that never calls run_go_snippet, even
// after being prompted to, must now fail Run outright (forcing a retry via
// the normal strike mechanism) rather than being silently accepted.
func TestRefineTestsJudgeRequiresRunGoSnippetVerification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":{"role":"assistant","content":"{\"decision\":\"approved\",\"summary\":\"looks fine\"}"},"done":true}`))
	}))
	defer srv.Close()

	d, job, oc := seedJudgeFixture(t, srv.URL)
	h := &RefineTestsJudge{}
	_, err := h.Run(context.Background(), d, oc, job)
	if err == nil {
		t.Fatal("Run: expected an error when run_go_snippet is never called, got nil")
	}
	if !strings.Contains(err.Error(), "run_go_snippet") {
		t.Errorf("Run error = %q, want it to mention run_go_snippet", err.Error())
	}
}
