package verbs

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every Phase 3 schema puts its reasoning-family field first and caps it.
func TestPhase3SchemasReasoningFirstAndCapped(t *testing.T) {
	cases := []struct {
		name         string
		schema       any
		reasoningKey string
		firstPayload string
	}{
		{"SURVEY_SPEC", SurveySpecSchema, "reasoning", "module"},
		{"CERTIFY_MANIFEST", CertifyManifestSchema, "model_reasoning", "final_decision"},
		{"ANALYZE_EXECUTION", AnalyzeExecutionSchema, "reasoning", "analyzer_interpretation"},
		{"REVISE_PENDING", RevisePendingSchema, "reasoning", "revisions"},
		{"REFINE_TESTS_JUDGE", RefineTestsJudgeSchema, "reasoning", "decision"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.schema)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			s := string(raw)
			props := s[strings.Index(s, `"properties":{`):]
			rIdx := strings.Index(props, `"`+c.reasoningKey+`"`)
			pIdx := strings.Index(props, `"`+c.firstPayload+`"`)
			if rIdx < 0 || pIdx < 0 {
				t.Fatalf("missing %s or %s: %s", c.reasoningKey, c.firstPayload, s)
			}
			if rIdx > pIdx {
				t.Errorf("%s must precede %s: %s", c.reasoningKey, c.firstPayload, s)
			}

			var parsed map[string]any
			json.Unmarshal(raw, &parsed)
			rp := parsed["properties"].(map[string]any)[c.reasoningKey].(map[string]any)
			if rp["maxLength"] == nil {
				t.Errorf("%s field has no maxLength — runaway guard missing", c.reasoningKey)
			}
			req, _ := parsed["required"].([]any)
			hasReasoning := false
			for _, r := range req {
				if r == c.reasoningKey {
					hasReasoning = true
				}
			}
			if !hasReasoning {
				t.Errorf("required = %v, missing %q", req, c.reasoningKey)
			}
		})
	}
}

func TestReasoningMaxLenIsGenerous(t *testing.T) {
	// Genuine deep AUDIT reasoning ran ~13-14K chars in Phase 2 live; the cap
	// must clear that so real chain-of-thought isn't truncated.
	if reasoningMaxLen < 14000 {
		t.Errorf("reasoningMaxLen = %d, too tight for real deep reasoning (~13-14K observed)", reasoningMaxLen)
	}
}

func TestSurveySpecValidateAcceptsReasoning(t *testing.T) {
	h := &SurveySpec{}
	in := `{"reasoning":"one file, one type","module":"m","package":"main",` +
		`"files":[{"path":"a.go","declarations":"type A struct{}"}]}`
	msg, parsed := h.Validate(in)
	if msg != "valid" {
		t.Fatalf("Validate = %q, want valid", msg)
	}
	if out := parsed.(SurveySpecOutput); !strings.Contains(out.Reasoning, "one file") {
		t.Errorf("reasoning not captured: %q", out.Reasoning)
	}
}

func TestRevisePendingValidateAcceptsReasoning(t *testing.T) {
	h := &RevisePending{}
	in := `{"reasoning":"no downstream bead is affected","revisions":[` +
		`{"bead_title":"vm","action":"no_change"}]}`
	msg, parsed := h.Validate(in)
	if msg != "valid" {
		t.Fatalf("Validate = %q, want valid", msg)
	}
	if out := parsed.(RevisePendingOutput); !strings.Contains(out.Reasoning, "no downstream") {
		t.Errorf("reasoning not captured: %q", out.Reasoning)
	}
}

func TestRefineTestsJudgeValidateAcceptsReasoning(t *testing.T) {
	h := &RefineTestsJudge{}
	in := `{"reasoning":"the finding about TestVM is correct","decision":"revise",` +
		`"functions_to_rewrite":["TestVM"],"instructions":"fix the -7/2 case","summary":"1 correction in TestVM"}`
	msg, parsed := h.Validate(in)
	if msg != "valid" {
		t.Fatalf("Validate = %q, want valid", msg)
	}
	if out := parsed.(RefineTestsJudgeOutput); !strings.Contains(out.Reasoning, "TestVM is correct") {
		t.Errorf("reasoning not captured: %q", out.Reasoning)
	}
}
