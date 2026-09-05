package verbs

import (
	"encoding/json"
	"strings"
)

// --- SURVEY_SPEC ---

// SurveyManifestFile is one file entry in the SURVEY_SPEC manifest.
// Declarations holds raw Go declaration text (types, consts, vars, function
// signatures with stub bodies) — no package statement and no import block.
// Imports lists the external packages that file's declarations reference
// (stdlib import paths plus module-internal ones). SURVEY owns this list
// because it is the only step that reads the design doc: when the doc names a
// specific import for an otherwise-ambiguous symbol (e.g. html/template vs
// text/template, math/rand/v2 vs math/rand), the choice is a doc-derived
// decision, not one a context-free mechanical import resolver can make.
// The scaffolding step in VERIFY renders the import block and the package
// statement from these fields. Older stored manifests have no imports key —
// it unmarshals to nil and the scaffolder falls back to goimports exactly as
// before.
type SurveyManifestFile struct {
	Path         string   `json:"path"`
	Imports      []string `json:"imports"`
	Declarations string   `json:"declarations"`
}

// SurveySpecOutput is the structured output of SURVEY_SPEC.
type SurveySpecOutput struct {
	// Reasoning: schema-mode chain-of-thought (docs/schema-mode-reasoning-field.md).
	Reasoning string               `json:"reasoning,omitempty"`
	Module    string               `json:"module"`
	Package   string               `json:"package"`
	Files     []SurveyManifestFile `json:"files"`
}

// --- VERIFY_MANIFEST ---

// VerifyManifestOutput is the structured output of VERIFY_MANIFEST.
// The six boolean fields mirror the verify_attempts table columns.
type VerifyManifestOutput struct {
	FilePresencePass       bool     `json:"file_presence_pass"`
	NoBehavioralTestsPass  bool     `json:"no_behavioral_tests_pass"`
	CompilePass            bool     `json:"compile_pass"`
	APICheckPass           bool     `json:"api_check_pass"`
	StubPurityPass         bool     `json:"stub_purity_pass"`
	// CrossFileTypePass is false when the same package-level identifier is
	// scaffolded with a type from a different imported package in two files
	// (the html/template vs text/template shared-symbol conflict).
	CrossFileTypePass      bool     `json:"cross_file_type_pass"`
	Violations             []string `json:"violations,omitempty"`
	VerifierInterpretation string   `json:"verifier_interpretation,omitempty"`
}

// --- CERTIFY_MANIFEST ---

// CertifyManifestOutput is the full output of CERTIFY_MANIFEST, combining
// the mechanical preliminary decision with the model's final decision.
type CertifyManifestOutput struct {
	PreliminaryDecision string `json:"preliminary_decision"` // "approve" | "reject"
	ModelReasoning      string `json:"model_reasoning,omitempty"`
	FinalDecision       string `json:"final_decision"` // "approve" | "reject"
	Feedback            string `json:"feedback,omitempty"`
}

// ParsedBead is a Bead as produced by DECOMPOSE_SPEC or the revise branch of
// ADJUDICATE_NEXT_EXECUTION. Both verbs share this type because the required
// fields (execution_budget, monitor_override) are identical in both contexts.
type ParsedBead struct {
	Title           string   `json:"title"`
	FullText        string   `json:"full_text"`
	ExecutionBudget int      `json:"execution_budget"`
	MonitorOverride string   `json:"monitor_override"` // "honor" | "ignore"
	OutputFiles     []string `json:"output_files"`     // files this bead writes; drives independence check
	ExitCriteria    []string `json:"exit_criteria"`    // concrete, runnable checks that define done
}

// firstEmptyStringIndex returns the index of the first empty (or all-whitespace)
// entry in ss, or -1 if every entry is non-empty. A JSON array can satisfy
// len(ss) != 0 while still containing "" — e.g. exit_criteria: [""] — which
// would otherwise pass validation and then mechanically succeed instantly
// (bash -c "" exits 0) regardless of what the bead actually implements.
func firstEmptyStringIndex(ss []string) int {
	for i, s := range ss {
		if strings.TrimSpace(s) == "" {
			return i
		}
	}
	return -1
}

// --- DECOMPOSE_SPEC ---

type DecomposeSpecOutput struct {
	// Reasoning is the model's schema-mode chain-of-thought (see
	// docs/schema-mode-reasoning-field.md). Captured for logging/inspection
	// only; not consumed by Commit or any downstream verb.
	Reasoning   string       `json:"reasoning,omitempty"`
	Beads       []ParsedBead `json:"beads"`
	Ambiguities []string     `json:"ambiguities,omitempty"`
}

// --- AUDIT_DECOMPOSITION ---

type AuditFinding struct {
	BeadTitle          string `json:"bead_title"`
	Issue              string `json:"issue"`
	DesignDocReference string `json:"design_doc_reference"`
}

type AuditDecompositionOutput struct {
	// Reasoning: schema-mode chain-of-thought (docs/schema-mode-reasoning-field.md).
	// Captured for logging/inspection; survives injectMechanicalFindings' re-marshal.
	Reasoning      string         `json:"reasoning,omitempty"`
	Findings       []AuditFinding `json:"findings"`
	OverallVerdict string         `json:"overall_verdict"` // "no_issues" | "issues_found"
}

// --- RECONCILE_DECOMPOSITION ---

type ReconcileResponse struct {
	BeadTitle string `json:"bead_title"`
	Action    string `json:"action"` // "agree_and_fix" | "disagree"
	Reason    string `json:"reason"`
	// AlreadyAddressed is meaningful only when Action == "disagree". RECONCILE
	// sets it true when this exact finding was already raised and disputed in
	// an earlier round with no new argument from AUDIT — self-certifying, in
	// its own single judgment call, that there is nothing new to respond to.
	// The mechanical convergence comparator (ReconcileDecomposition.Commit)
	// reads this directly instead of inferring repetition from finding text,
	// which is fragile to paraphrasing (see the fractal-smoke-2 incident,
	// project 105: AUDIT reworded an already-conceded finding and a
	// text-comparison check missed it, forcing an unnecessary escalation).
	AlreadyAddressed bool        `json:"already_addressed,omitempty"`
	UpdatedBead      *ParsedBead `json:"updated_bead,omitempty"` // present only when action == "agree_and_fix"
}

type ReconcileDecompositionOutput struct {
	// Reasoning: schema-mode chain-of-thought (docs/schema-mode-reasoning-field.md).
	Reasoning string              `json:"reasoning,omitempty"`
	Responses []ReconcileResponse `json:"responses"`
}

// --- ANALYZE_EXECUTION ---

type AnalyzeExecutionOutput struct {
	// Reasoning: schema-mode chain-of-thought (docs/schema-mode-reasoning-field.md).
	// This is where the working-through goes so MechanicalFindings stays facts-only.
	Reasoning string `json:"reasoning,omitempty"`
	// MechanicalFindings is fielded JSON text: objective facts only, no causal
	// language ("due to", "because", "caused by", "results in").
	MechanicalFindings string `json:"mechanical_findings"`
	// AnalyzerInterpretation is logged but excluded from ADJUDICATE's default
	// inputs (architecture, ADJUDICATE_NEXT_EXECUTION's four inputs, item 3).
	AnalyzerInterpretation string `json:"analyzer_interpretation,omitempty"`
}

// --- COMPRESS_ANALYSIS ---

type CompressAnalysisOutput struct {
	CompressedText string `json:"compressed_text"`
}

// --- REVISE_PENDING ---

// RevisePendingRevision is one entry in the REVISE_PENDING output: a decision
// for a single pending bead. action is "update_spec" or "no_change".
type RevisePendingRevision struct {
	BeadTitle       string `json:"bead_title"`
	Action          string `json:"action"`
	UpdatedFullText string `json:"updated_full_text,omitempty"`
}

// RevisePendingOutput is the structured output of REVISE_PENDING.
type RevisePendingOutput struct {
	// Reasoning: schema-mode chain-of-thought (docs/schema-mode-reasoning-field.md).
	Reasoning string                  `json:"reasoning,omitempty"`
	Revisions []RevisePendingRevision `json:"revisions"`
}

// --- REFINE_TESTS_WRITE / REFINE_TESTS_CRITIQUE / REFINE_TESTS_JUDGE ---

// RefineTestsFile is one test file entry in the REFINE_TESTS_WRITE output.
type RefineTestsFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// RefineTestsWriteOutput is the output of REFINE_TESTS_WRITE.
// Files are written via write_file tool calls during Run(); only the summary
// is returned as structured output.
type RefineTestsWriteOutput struct {
	// NOT schema-mode — REFINE_TESTS_WRITE is tool-primary (write_function);
	// a reasoning field let the model skip the tool calls. See its Run.
	Summary string `json:"summary"`
}

// RefineTestsCritiqueOutput is the output of REFINE_TESTS_CRITIQUE.
type RefineTestsCritiqueOutput struct {
	Findings          []string `json:"findings"`
	VerifiedFunctions []string `json:"verified_functions"` // every Test* function reviewed and found correct
	AllCorrect        bool     `json:"all_correct"`
	Summary           string   `json:"summary"`
}

// flexString unmarshals a JSON field that may be either a string or an array
// of strings. Array elements are joined with newlines. This handles models
// that emit ["item1", "item2"] instead of "item1\nitem2".
type flexString string

func (f *flexString) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var a []string
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*f = flexString(strings.Join(a, "\n"))
	return nil
}

// RefineTestsJudgeOutput is the output of REFINE_TESTS_JUDGE.
type RefineTestsJudgeOutput struct {
	Decision           string     `json:"decision"`             // "approved" or "revise"
	FunctionsToRewrite []string   `json:"functions_to_rewrite"` // only set when decision="revise"
	Instructions       flexString `json:"instructions"`         // only set when decision="revise"
	Summary            string     `json:"summary"`
}

// --- ADJUDICATE_NEXT_EXECUTION ---

type AdjudicateNextExecutionOutput struct {
	// Trend and BeadSpecFit are required fields checked for consistency
	// against Reasoning (architecture: consistency check).
	Trend       string `json:"trend"`         // "same" | "narrower" | "unrelated"
	BeadSpecFit string `json:"bead_spec_fit"` // "bead_problem" | "execution_capability_problem"
	Reasoning   string `json:"reasoning"`
	Decision    string `json:"decision"` // "execute_as_is" | "execute_revised" | "full_stop" | "declare_success" | "test_reject"
	// RevisedBead is present only when Decision == "execute_revised".
	RevisedBead *ParsedBead `json:"revised_bead,omitempty"`
	// TestRejectionGuidance is present only when Decision == "test_reject".
	// Lists corrections to apply when rewriting the test files.
	TestRejectionGuidance string `json:"test_rejection_guidance,omitempty"`
	// ReRefineGuidance is present only when Decision == "re_refine".
	// Diagnosis of which test assertions are logically impossible and why.
	ReRefineGuidance string `json:"re_refine_guidance,omitempty"`
}
