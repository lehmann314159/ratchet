package verbs

// --- SURVEY_SPEC ---

// SurveyManifestFile is one file entry in the SURVEY_SPEC manifest.
// Declarations holds raw Go declaration text (types, consts, vars, function
// signatures with stub bodies) — no package statement and no import block.
// The scaffolding step in VERIFY generates those mechanically.
type SurveyManifestFile struct {
	Path         string `json:"path"`
	Declarations string `json:"declarations"`
}

// SurveySpecOutput is the structured output of SURVEY_SPEC.
type SurveySpecOutput struct {
	Module  string               `json:"module"`
	Package string               `json:"package"`
	Files   []SurveyManifestFile `json:"files"`
}

// --- VERIFY_MANIFEST ---

// VerifyManifestOutput is the structured output of VERIFY_MANIFEST.
// The five boolean fields mirror the verify_attempts table columns.
type VerifyManifestOutput struct {
	FilePresencePass       bool     `json:"file_presence_pass"`
	NoBehavioralTestsPass  bool     `json:"no_behavioral_tests_pass"`
	CompilePass            bool     `json:"compile_pass"`
	APICheckPass           bool     `json:"api_check_pass"`
	StubPurityPass         bool     `json:"stub_purity_pass"`
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

// --- DECOMPOSE_SPEC ---

type DecomposeSpecOutput struct {
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
	Findings       []AuditFinding `json:"findings"`
	OverallVerdict string         `json:"overall_verdict"` // "no_issues" | "issues_found"
}

// --- RECONCILE_DECOMPOSITION ---

type ReconcileResponse struct {
	BeadTitle   string      `json:"bead_title"`
	Action      string      `json:"action"`               // "agree_and_fix" | "disagree"
	Reason      string      `json:"reason"`
	UpdatedBead *ParsedBead `json:"updated_bead,omitempty"` // present only when action == "agree_and_fix"
}

type ReconcileDecompositionOutput struct {
	Responses []ReconcileResponse `json:"responses"`
}

// --- ANALYZE_EXECUTION ---

type AnalyzeExecutionOutput struct {
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
	Revisions []RevisePendingRevision `json:"revisions"`
}

// --- ADJUDICATE_NEXT_EXECUTION ---

type AdjudicateNextExecutionOutput struct {
	// Trend and BeadSpecFit are required fields checked for consistency
	// against Reasoning (architecture: consistency check).
	Trend       string `json:"trend"`        // "same" | "narrower" | "unrelated"
	BeadSpecFit string `json:"bead_spec_fit"` // "bead_problem" | "execution_capability_problem"
	Reasoning   string `json:"reasoning"`
	Decision    string `json:"decision"` // "execute_as_is" | "execute_revised" | "full_stop" | "declare_success" | "test_reject"
	// RevisedBead is present only when Decision == "execute_revised".
	RevisedBead *ParsedBead `json:"revised_bead,omitempty"`
	// TestRejectionGuidance is present only when Decision == "test_reject".
	// Lists corrections to apply when rewriting the test files.
	TestRejectionGuidance string `json:"test_rejection_guidance,omitempty"`
}
