package verbs

import "testing"

// Each table-driven test covers: one valid case, each required-field
// constraint, and verb-specific logic (causal phrases, consistency check).

func TestDecomposeSpecValidate(t *testing.T) {
	h := &DecomposeSpec{}
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{
			"valid with one bead",
			`{"beads":[{"title":"B01","full_text":"build the widget","execution_budget":300,"monitor_override":"honor","output_files":["widget.go"],"exit_criteria":["go build ./..."]}]}`,
			true,
		},
		{
			"valid with ambiguities field",
			`{"beads":[{"title":"B01","full_text":"spec","execution_budget":60,"monitor_override":"ignore","output_files":["spec.go"],"exit_criteria":["go build ./..."]}],"ambiguities":["scope unclear"]}`,
			true,
		},
		{"empty object", `{}`, false},
		{"empty beads array", `{"beads":[]}`, false},
		{"bead missing title", `{"beads":[{"full_text":"x","execution_budget":60,"monitor_override":"honor"}]}`, false},
		{"bead missing full_text", `{"beads":[{"title":"B01","execution_budget":60,"monitor_override":"honor"}]}`, false},
		{"execution_budget zero", `{"beads":[{"title":"B01","full_text":"x","execution_budget":0,"monitor_override":"honor"}]}`, false},
		{"execution_budget negative", `{"beads":[{"title":"B01","full_text":"x","execution_budget":-1,"monitor_override":"honor"}]}`, false},
		{"monitor_override invalid", `{"beads":[{"title":"B01","full_text":"x","execution_budget":60,"monitor_override":"maybe"}]}`, false},
		{"not JSON", `not json`, false},
		{
			"duplicate bead titles",
			`{"beads":[
				{"title":"B01","full_text":"a","execution_budget":60,"monitor_override":"honor","output_files":["a.go"],"exit_criteria":["go build ./..."]},
				{"title":"B01","full_text":"b","execution_budget":60,"monitor_override":"honor","output_files":["b.go"],"exit_criteria":["go build ./..."]}
			]}`,
			false,
		},
	}
	runValidate(t, h.Validate, tests)
}

func TestAuditDecompositionValidate(t *testing.T) {
	h := &AuditDecomposition{}
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{
			"no issues — empty findings array",
			`{"findings":[],"overall_verdict":"no_issues"}`,
			true,
		},
		{
			"issues found with findings",
			`{"findings":[{"bead_title":"B01","issue":"stride formula wrong","design_doc_reference":"§3"}],"overall_verdict":"issues_found"}`,
			true,
		},
		{
			"issues_found but findings empty",
			`{"findings":[],"overall_verdict":"issues_found"}`,
			false,
		},
		{
			"invalid overall_verdict",
			`{"findings":[],"overall_verdict":"unclear"}`,
			false,
		},
		{
			"finding missing bead_title",
			`{"findings":[{"issue":"drift","design_doc_reference":"§1"}],"overall_verdict":"issues_found"}`,
			false,
		},
		{
			"finding missing issue",
			`{"findings":[{"bead_title":"B01","design_doc_reference":"§1"}],"overall_verdict":"issues_found"}`,
			false,
		},
		{"not JSON", `not json`, false},
	}
	runValidate(t, h.Validate, tests)
}

func TestReconcileDecompositionValidate(t *testing.T) {
	h := &ReconcileDecomposition{knownTitles: map[string]bool{"B01": true}}
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{
			"agree_and_fix with updated_bead",
			`{"responses":[{"bead_title":"B01","action":"agree_and_fix","reason":"correct","updated_bead":{"title":"B01","full_text":"fixed","execution_budget":60,"monitor_override":"honor","output_files":["b01.go"],"exit_criteria":["go build ./..."]}}]}`,
			true,
		},
		{
			"disagree with reason",
			`{"responses":[{"bead_title":"B01","action":"disagree","reason":"finding is wrong — spec is correct"}]}`,
			true,
		},
		{
			"agree_and_fix missing updated_bead",
			`{"responses":[{"bead_title":"B01","action":"agree_and_fix","reason":"ok"}]}`,
			false,
		},
		{
			// Reproduces the applyFixes crash found in the Stage 2 audit: a
			// well-formed agree_and_fix whose updated_bead.title doesn't match
			// any existing bead (typo/rename) used to sail through Validate and
			// only fail deep inside Commit's DB lookup, rolling back the whole
			// transaction. Must now be caught here instead.
			"agree_and_fix with unknown updated_bead title",
			`{"responses":[{"bead_title":"B01","action":"agree_and_fix","reason":"correct","updated_bead":{"title":"B01 Renamed","full_text":"fixed","execution_budget":60,"monitor_override":"honor","output_files":["b01.go"],"exit_criteria":["go build ./..."]}}]}`,
			false,
		},
		{
			"disagree with empty reason",
			`{"responses":[{"bead_title":"B01","action":"disagree","reason":""}]}`,
			false,
		},
		{
			"invalid action",
			`{"responses":[{"bead_title":"B01","action":"abstain","reason":"x"}]}`,
			false,
		},
		{
			"empty responses array",
			`{"responses":[]}`,
			false,
		},
		{"not JSON", `not json`, false},
	}
	runValidate(t, h.Validate, tests)
}

func TestAnalyzeExecutionValidate(t *testing.T) {
	h := &AnalyzeExecution{}
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{
			"valid with both fields",
			`{"mechanical_findings":"TestFoo FAIL exit 1 line 42","analyzer_interpretation":"suggests nil pointer"}`,
			true,
		},
		{
			"valid without interpretation",
			`{"mechanical_findings":"TestFoo PASS"}`,
			true,
		},
		{"empty mechanical_findings", `{"mechanical_findings":""}`, false},
		{"whitespace mechanical_findings", `{"mechanical_findings":"   "}`, false},
		{"missing mechanical_findings", `{"analyzer_interpretation":"x"}`, false},
		{"not JSON", `not json`, false},
	}
	runValidate(t, h.Validate, tests)
}

func TestCompressAnalysisValidate(t *testing.T) {
	h := &CompressAnalysis{}
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"valid", `{"compressed_text":"attempt 1: nil ptr in TestFoo; narrowing across attempts"}`, true},
		{"empty compressed_text", `{"compressed_text":""}`, false},
		{"whitespace only", `{"compressed_text":"   "}`, false},
		{"missing field", `{}`, false},
		{"not JSON", `not json`, false},
	}
	runValidate(t, h.Validate, tests)
}

func TestAdjudicateNextExecutionValidate(t *testing.T) {
	h := &AdjudicateNextExecution{}
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{
			"valid execute_as_is",
			`{"trend":"same","bead_spec_fit":"bead_problem","reasoning":"the bead spec omits the stride constraint","decision":"execute_as_is"}`,
			true,
		},
		{
			"valid full_stop",
			`{"trend":"same","bead_spec_fit":"bead_problem","reasoning":"repeated unresolvable ambiguity in the spec","decision":"full_stop"}`,
			true,
		},
		{
			"valid execute_revised with revised_bead",
			`{"trend":"narrower","bead_spec_fit":"bead_problem","reasoning":"spec missing type constraint","decision":"execute_revised","revised_bead":{"title":"B01","full_text":"revised","execution_budget":300,"monitor_override":"honor","output_files":["b01.go"],"exit_criteria":["go build ./..."]}}`,
			true,
		},
		{"invalid trend", `{"trend":"worse","bead_spec_fit":"bead_problem","reasoning":"x","decision":"execute_as_is"}`, false},
		{"invalid bead_spec_fit", `{"trend":"same","bead_spec_fit":"unknown","reasoning":"x","decision":"execute_as_is"}`, false},
		{"empty reasoning", `{"trend":"same","bead_spec_fit":"bead_problem","reasoning":"","decision":"execute_as_is"}`, false},
		{"invalid decision", `{"trend":"same","bead_spec_fit":"bead_problem","reasoning":"x","decision":"retry"}`, false},
		{
			"execute_revised missing revised_bead",
			`{"trend":"same","bead_spec_fit":"bead_problem","reasoning":"x","decision":"execute_revised"}`,
			false,
		},
		{
			"execute_revised zero execution_budget",
			`{"trend":"same","bead_spec_fit":"bead_problem","reasoning":"x","decision":"execute_revised","revised_bead":{"title":"B01","full_text":"x","execution_budget":0,"monitor_override":"honor"}}`,
			false,
		},
		{
			"execute_revised invalid monitor_override",
			`{"trend":"same","bead_spec_fit":"bead_problem","reasoning":"x","decision":"execute_revised","revised_bead":{"title":"B01","full_text":"x","execution_budget":60,"monitor_override":"maybe"}}`,
			false,
		},
		// declare_success: trend and bead_spec_fit may be "not_applicable" or any valid value —
		// they are not used downstream on terminal paths, so we don't enforce a specific value.
		{
			"valid declare_success with not_applicable",
			`{"trend":"not_applicable","bead_spec_fit":"not_applicable","reasoning":"All exit criteria confirmed met: TestDeterminism, TestBoundary, TestStateAdvancement all passed.","decision":"declare_success"}`,
			true,
		},
		{
			"valid declare_success with meaningful trend",
			`{"trend":"same","bead_spec_fit":"not_applicable","reasoning":"exit criteria met","decision":"declare_success"}`,
			true,
		},
		{
			"valid declare_success with meaningful bead_spec_fit",
			`{"trend":"not_applicable","bead_spec_fit":"bead_problem","reasoning":"exit criteria met","decision":"declare_success"}`,
			true,
		},
		{
			"not_applicable trend without declare_success",
			`{"trend":"not_applicable","bead_spec_fit":"execution_capability_problem","reasoning":"x","decision":"execute_as_is"}`,
			false,
		},
		{
			"not_applicable bead_spec_fit without declare_success",
			`{"trend":"same","bead_spec_fit":"not_applicable","reasoning":"x","decision":"execute_as_is"}`,
			false,
		},
		// Consistency check — the Exp-5 failure mode: declared bead_problem but reasoning
		// describes execution capability. Any of these phrases in reasoning must fail.
		{
			"consistency: bead_problem but reasoning says runner-capability case",
			`{"trend":"same","bead_spec_fit":"bead_problem","reasoning":"this is a textbook runner-capability case","decision":"execute_as_is"}`,
			false,
		},
		{
			"consistency: bead_problem but reasoning says spec is clear",
			`{"trend":"same","bead_spec_fit":"bead_problem","reasoning":"the spec is clear and unambiguous but execution failed","decision":"execute_as_is"}`,
			false,
		},
		{
			"consistency: bead_problem but reasoning says despite the spec",
			`{"trend":"same","bead_spec_fit":"bead_problem","reasoning":"despite the spec being unambiguous the model failed","decision":"execute_as_is"}`,
			false,
		},
		{
			"consistency: execution_capability_problem but reasoning says spec is ambiguous",
			`{"trend":"same","bead_spec_fit":"execution_capability_problem","reasoning":"the spec is ambiguous about the return type","decision":"execute_as_is"}`,
			false,
		},
		{
			"consistency: execution_capability_problem but reasoning blames bead specification",
			`{"trend":"same","bead_spec_fit":"execution_capability_problem","reasoning":"bead specification is missing required fields","decision":"execute_as_is"}`,
			false,
		},
		{"not JSON", `not json`, false},
	}
	runValidate(t, h.Validate, tests)
}

// runValidate is a shared table-driven driver for Validate tests.
func runValidate(t *testing.T, validate func(string) (string, any), tests []struct {
	name  string
	input string
	valid bool
}) {
	t.Helper()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, parsed := validate(tc.input)
			gotValid := result == "valid"
			if gotValid != tc.valid {
				t.Errorf("Validate(%q) = %q; wantValid=%v", tc.input, result, tc.valid)
			}
			if tc.valid && parsed == nil {
				t.Error("Validate returned \"valid\" but parsed is nil")
			}
			if !tc.valid && parsed != nil {
				t.Error("Validate returned non-valid result but parsed is non-nil")
			}
		})
	}
}
