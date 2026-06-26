package verbs

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

// --- ADJUDICATE_NEXT_EXECUTION ---

type AdjudicateNextExecutionOutput struct {
	// Trend and BeadSpecFit are required fields checked for consistency
	// against Reasoning (architecture: consistency check).
	Trend       string `json:"trend"`        // "same" | "narrower" | "unrelated"
	BeadSpecFit string `json:"bead_spec_fit"` // "bead_problem" | "execution_capability_problem"
	Reasoning   string `json:"reasoning"`
	Decision    string `json:"decision"` // "execute_as_is" | "execute_revised" | "full_stop"
	// RevisedBead is present only when Decision == "execute_revised".
	RevisedBead *ParsedBead `json:"revised_bead,omitempty"`
}
