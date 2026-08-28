package verbs

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

// TestRefineTestsJudgeRequestsSchemaConstrainedFormat locks in the fix for a
// real live escalation (connect-four-v2 bead 56, 2026-08-27): 3/3
// REFINE_TESTS_JUDGE attempts omitted the required "summary" field despite
// the prompt listing it, because format:"json" only constrains JSON syntax,
// not which keys are present. Run must pass a real JSON Schema (via
// ollama.Options.Format) with "summary" in its required array, not the bare
// string "json" every other verb still uses — without this test, a future
// refactor of Run's oc.Chat call could silently drop the schema argument and
// reintroduce the exact bug the schema was added to close.
func TestRefineTestsJudgeRequestsSchemaConstrainedFormat(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":{"role":"assistant","content":"{\"decision\":\"approved\",\"summary\":\"ok\"}"},"done":true}`))
	}))
	defer srv.Close()

	d := openTestDB(t)
	seedProject(t, d, -1, "TestRefineTestsJudgeRequestsSchemaConstrainedFormat")
	beadID, _ := seedBead(t, d, -1, "game-mechanics")
	if _, err := d.ExecContext(context.Background(),
		`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (-1, ?, 'test-model')`,
		db.VerbRefineTestsJudge,
	); err != nil {
		t.Fatalf("seed verb_model_assignments: %v", err)
	}
	job := seedJob(t, d, -1, db.VerbRefineTestsJudge, sql.NullInt64{Int64: beadID, Valid: true})

	oc := ollama.New(srv.URL)
	h := &RefineTestsJudge{}
	if _, err := h.Run(context.Background(), d, oc, job); err != nil {
		t.Fatalf("Run: %v", err)
	}

	format, ok := gotBody["format"].(map[string]any)
	if !ok {
		t.Fatalf("request body format = %#v (%T), want a JSON Schema object, not a bare string", gotBody["format"], gotBody["format"])
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
