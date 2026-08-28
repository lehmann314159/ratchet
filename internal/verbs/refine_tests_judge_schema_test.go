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

// TestRefineTestsJudgeRequestsSchemaConstrainedFormat locks in the fix for a
// real live escalation (connect-four-v2 bead 56, 2026-08-27): 3/3
// REFINE_TESTS_JUDGE attempts omitted the required "summary" field despite
// the prompt listing it, because format:"json" only constrains JSON syntax,
// not which keys are present. Run must pass a real JSON Schema (via
// ollama.Options.Format) with "summary" in its required array, not the bare
// string "json" every other verb still uses — without this test, a future
// refactor of Run's oc.ChatWithTools call could silently drop the schema
// argument and reintroduce the exact bug the schema was added to close.
//
// The fake server simulates a real two-turn round trip (a tool call, then
// the final decision) because Run now mandates at least one run_go_snippet
// call before finalizing (see TestRefineTestsJudgeRequiresRunGoSnippetVerification)
// — a fake server that just returns final content on the first turn no
// longer exercises Run's real path.
func TestRefineTestsJudgeRequestsSchemaConstrainedFormat(t *testing.T) {
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

	// Every turn should carry the schema, including the tool-call turn —
	// confirmed live before wiring this in that Ollama only applies it to
	// turns that actually produce content, not tool-call-only turns.
	format, ok := bodies[len(bodies)-1]["format"].(map[string]any)
	if !ok {
		t.Fatalf("final request body format = %#v, want a JSON Schema object, not a bare string", bodies[len(bodies)-1]["format"])
	}
	required, ok := format["required"].([]any)
	if !ok {
		t.Fatalf("format.required = %#v, want a string array", format["required"])
	}
	found := false
	for _, r := range required {
		if r == "summary" {
			found = true
		}
	}
	if !found {
		t.Errorf("format.required = %v, want it to include \"summary\"", required)
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
