package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

// TestDecomposeSpecSchemaReasoningFirst locks in the property that makes
// schema-mode work for reasoning models (docs/schema-mode-reasoning-field.md):
// `reasoning` must be the FIRST property in the marshaled schema, because
// Ollama's grammar generates object properties in schema declaration order.
// A plain map[string]any would sort alphabetically and put `reasoning` last,
// defeating the chain-of-thought-first design — orderedObject exists to
// prevent exactly that.
func TestDecomposeSpecSchemaReasoningFirst(t *testing.T) {
	raw, err := json.Marshal(DecomposeSpecSchema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	s := string(raw)

	// Structurally: unmarshal preserves nothing about order, so assert on the
	// marshaled text — the bytes Ollama actually receives.
	propsIdx := strings.Index(s, `"properties":{`)
	if propsIdx < 0 {
		t.Fatalf("schema has no properties object: %s", s)
	}
	afterProps := s[propsIdx:]
	rIdx := strings.Index(afterProps, `"reasoning"`)
	bIdx := strings.Index(afterProps, `"beads"`)
	if rIdx < 0 || bIdx < 0 {
		t.Fatalf("schema missing reasoning or beads property: %s", s)
	}
	if rIdx > bIdx {
		t.Errorf("`reasoning` (idx %d) must come before `beads` (idx %d) in the schema: %s", rIdx, bIdx, s)
	}

	// Sanity on the rest of the contract.
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	req, _ := parsed["required"].([]any)
	var haveReasoning, haveBeads bool
	for _, r := range req {
		switch r {
		case "reasoning":
			haveReasoning = true
		case "beads":
			haveBeads = true
		}
	}
	if !haveReasoning || !haveBeads {
		t.Errorf("required = %v, want both reasoning and beads", req)
	}

	props := parsed["properties"].(map[string]any)
	beads := props["beads"].(map[string]any)
	if beads["minItems"] == nil {
		t.Error("beads schema has no minItems — the empty-array degeneration guard is missing")
	}
}

// TestDecomposeSpecValidateAcceptsReasoning: a decomposition carrying the
// schema-mode `reasoning` field validates, and the field lands on the parsed
// output for logging/inspection.
func TestDecomposeSpecValidateAcceptsReasoning(t *testing.T) {
	h := &DecomposeSpec{}
	in := `{"reasoning":"B01 owns widget.go; no dependencies; one bead is enough.",` +
		`"beads":[{"title":"B01","full_text":"build the widget","monitor_override":"honor",` +
		`"output_files":["widget.go"],"exit_criteria":["go build ./..."]}]}`
	msg, parsed := h.Validate(in)
	if msg != "valid" {
		t.Fatalf("Validate = %q, want valid", msg)
	}
	out, ok := parsed.(DecomposeSpecOutput)
	if !ok {
		t.Fatalf("parsed is %T, want DecomposeSpecOutput", parsed)
	}
	if !strings.Contains(out.Reasoning, "B01 owns widget.go") {
		t.Errorf("out.Reasoning = %q, want the model's reasoning captured", out.Reasoning)
	}
}

// TestDecomposeSpecRunSendsSchema is the regression guard for Phase 1: Run
// must send a real JSON Schema in `format` (not the bare string "json") and
// `think:false` top-level. A future refactor dropping either would silently
// reintroduce the reasoning-model degeneration this phase fixes.
func TestDecomposeSpecRunSendsSchema(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "design_doc.md"), []byte("# Widget\n\nBuild a widget.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "survey.md"), []byte("module widget\npackage main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (-1, 'fixture: decompose schema-mode', ?, 'design_doc.md', 'active',
		        'honor', 300, 2, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		dir); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (-1, ?, 'test-model')`,
		db.VerbDecomposeSpec); err != nil {
		t.Fatalf("seed verb_model_assignments: %v", err)
	}
	job := seedJob(t, d, -1, db.VerbDecomposeSpec, sql.NullInt64{})

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":{"role":"assistant","content":"{\"reasoning\":\"x\",\"beads\":[]}"},"done":true}`))
	}))
	defer srv.Close()

	h := &DecomposeSpec{}
	if _, err := h.Run(ctx, d, ollama.New(srv.URL), job); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, isString := gotBody["format"].(string); isString {
		t.Errorf(`format is the bare string %q, want a JSON Schema object`, gotBody["format"])
	}
	format, ok := gotBody["format"].(map[string]any)
	if !ok {
		t.Fatalf("format = %#v, want a JSON Schema object", gotBody["format"])
	}
	if format["type"] != "object" || format["properties"] == nil {
		t.Errorf("format = %#v, want an object schema with properties", format)
	}
	if think, ok := gotBody["think"].(bool); !ok || think {
		t.Errorf("think = %#v, want top-level false", gotBody["think"])
	}
}
