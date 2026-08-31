package verbs

import (
	"encoding/json"
	"strings"
	"testing"
)

// assertReasoningFirst checks that a schema-mode schema marshals with
// `reasoning` ahead of firstPayloadKey in its properties object, and that
// `required` lists everything in wantRequired.
func assertReasoningFirst(t *testing.T, schema any, firstPayloadKey string, wantRequired ...string) {
	t.Helper()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	s := string(raw)

	props := s[strings.Index(s, `"properties":{`):]
	rIdx := strings.Index(props, `"reasoning"`)
	pIdx := strings.Index(props, `"`+firstPayloadKey+`"`)
	if rIdx < 0 || pIdx < 0 {
		t.Fatalf("schema missing reasoning or %s: %s", firstPayloadKey, s)
	}
	if rIdx > pIdx {
		t.Errorf("reasoning (%d) must precede %s (%d): %s", rIdx, firstPayloadKey, pIdx, s)
	}

	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
	req, _ := parsed["required"].([]any)
	have := map[string]bool{}
	for _, r := range req {
		have[r.(string)] = true
	}
	for _, w := range wantRequired {
		if !have[w] {
			t.Errorf("required = %v, missing %q", req, w)
		}
	}
}

func TestAuditDecompositionSchemaReasoningFirst(t *testing.T) {
	assertReasoningFirst(t, AuditDecompositionSchema, "findings", "reasoning", "findings", "overall_verdict")
}

func TestReconcileDecompositionSchemaReasoningFirst(t *testing.T) {
	assertReasoningFirst(t, ReconcileDecompositionSchema, "responses", "reasoning", "responses")
}

func TestRefineTestsCritiqueSchemaReasoningFirst(t *testing.T) {
	assertReasoningFirst(t, RefineTestsCritiqueSchema, "findings", "reasoning", "summary")
}

func TestRefineTestsCritiqueValidateAcceptsReasoning(t *testing.T) {
	h := &RefineTestsCritique{}
	in := `{"reasoning":"TestVM asserts -7/2 == -4; Go truncates toward zero so it's -3",` +
		`"findings":["TestVM — -7/2 should be -3 not -4"],"verified_functions":[],` +
		`"all_correct":false,"summary":"1 problem found in TestVM"}`
	msg, parsed := h.Validate(in)
	if msg != "valid" {
		t.Fatalf("Validate = %q, want valid", msg)
	}
	if out := parsed.(RefineTestsCritiqueOutput); !strings.Contains(out.Reasoning, "truncates toward zero") {
		t.Errorf("out.Reasoning = %q, want it captured", out.Reasoning)
	}
}

// The updated_bead sub-schema must reuse the shared bead object shape, so a
// RECONCILE fix is grammar-constrained to the same fields DECOMPOSE produces.
func TestReconcileUpdatedBeadUsesBeadSchema(t *testing.T) {
	raw, _ := json.Marshal(ReconcileDecompositionSchema)
	var parsed map[string]any
	json.Unmarshal(raw, &parsed)
	props := parsed["properties"].(map[string]any)
	items := props["responses"].(map[string]any)["items"].(map[string]any)
	ub, ok := items["properties"].(map[string]any)["updated_bead"].(map[string]any)
	if !ok {
		t.Fatal("responses.items has no updated_bead property")
	}
	ubProps, _ := ub["properties"].(map[string]any)
	for _, f := range []string{"title", "full_text", "monitor_override", "output_files", "exit_criteria"} {
		if ubProps[f] == nil {
			t.Errorf("updated_bead schema missing %q", f)
		}
	}
	// updated_bead itself is NOT in the response item's required set — it is
	// conditional on action == agree_and_fix, enforced in Validate.
	req, _ := items["required"].([]any)
	for _, r := range req {
		if r == "updated_bead" {
			t.Error("updated_bead must not be in the response item's required set")
		}
	}
}

func TestAuditDecompositionValidateAcceptsReasoning(t *testing.T) {
	h := &AuditDecomposition{}
	in := `{"reasoning":"bead 3 drops the mixed-sign division pin","findings":[` +
		`{"bead_title":"vm","issue":"missing division sign cases","design_doc_reference":"§4.2"}],` +
		`"overall_verdict":"issues_found"}`
	msg, parsed := h.Validate(in)
	if msg != "valid" {
		t.Fatalf("Validate = %q, want valid", msg)
	}
	if out := parsed.(AuditDecompositionOutput); !strings.Contains(out.Reasoning, "mixed-sign division") {
		t.Errorf("out.Reasoning = %q, want it captured", out.Reasoning)
	}
}

func TestReconcileDecompositionValidateAcceptsReasoning(t *testing.T) {
	h := &ReconcileDecomposition{knownTitles: map[string]bool{"vm": true}}
	in := `{"reasoning":"the finding is correct; adding the four sign cases to vm",` +
		`"responses":[{"bead_title":"vm","action":"agree_and_fix","reason":"pin was dropped",` +
		`"updated_bead":{"title":"vm","full_text":"implement the VM incl. all 4 division sign cases",` +
		`"monitor_override":"honor","output_files":["vm.go"],"exit_criteria":["grep -q 'func TestVM' vm_test.go && go test -v -run TestVM ./..."]}}]}`
	msg, parsed := h.Validate(in)
	if msg != "valid" {
		t.Fatalf("Validate = %q, want valid", msg)
	}
	if out := parsed.(ReconcileDecompositionOutput); !strings.Contains(out.Reasoning, "four sign cases") {
		t.Errorf("out.Reasoning = %q, want it captured", out.Reasoning)
	}
}

// injectMechanicalFindings re-marshals the AUDIT output; the schema-mode
// reasoning field must survive that round trip (it's read by RECONCILE).
func TestInjectMechanicalFindingsPreservesReasoning(t *testing.T) {
	beads := []beadState{{
		Title:        "game",
		OutputFiles:  []string{"game.go"}, // no _test.go -> triggers a mechanical finding
		ExitCriteria: []string{"grep -q 'func TestGame' game_test.go && go test -v -run TestGame ./..."},
	}}
	raw := `{"reasoning":"looks fine to me","findings":[],"overall_verdict":"no_issues"}`
	out := injectMechanicalFindings(raw, "/tmp/whatever", beads)

	var parsed AuditDecompositionOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("re-marshaled output not parseable: %v\n%s", err, out)
	}
	if parsed.Reasoning != "looks fine to me" {
		t.Errorf("reasoning lost in re-marshal: %q", parsed.Reasoning)
	}
	if parsed.OverallVerdict != "issues_found" || len(parsed.Findings) == 0 {
		t.Errorf("mechanical finding not injected: %+v", parsed)
	}
}
