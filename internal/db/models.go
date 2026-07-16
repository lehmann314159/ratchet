package db

import (
	"database/sql"
	"time"
)

// Verb constants.
const (
	VerbSurveySpec                 = "SURVEY_SPEC"
	VerbVerifyManifest             = "VERIFY_MANIFEST"
	VerbCertifyManifest            = "CERTIFY_MANIFEST"
	VerbDecomposeSpec              = "DECOMPOSE_SPEC"
	VerbAuditDecomposition         = "AUDIT_DECOMPOSITION"
	VerbReconcileDecomposition     = "RECONCILE_DECOMPOSITION"
	VerbExecuteBead                = "EXECUTE_BEAD"
	VerbMonitorExecution           = "MONITOR_EXECUTION"
	VerbAnalyzeExecution           = "ANALYZE_EXECUTION"
	VerbCompressAnalysis           = "COMPRESS_ANALYSIS"
	VerbAdjudicateNextExecution    = "ADJUDICATE_NEXT_EXECUTION"
	VerbRevisePending              = "REVISE_PENDING"
	VerbRefineTestsWrite           = "REFINE_TESTS_WRITE"
	VerbRefineTestsCritique        = "REFINE_TESTS_CRITIQUE"
	VerbRefineTestsJudge           = "REFINE_TESTS_JUDGE"
)

// AllVerbs lists every model-assigned verb in FSM order.
// VERIFY_MANIFEST is excluded: it is model-free and has no verb_model_assignments row.
var AllVerbs = []string{
	VerbSurveySpec,
	VerbCertifyManifest,
	VerbDecomposeSpec,
	VerbAuditDecomposition,
	VerbReconcileDecomposition,
	VerbExecuteBead,
	VerbMonitorExecution,
	VerbAnalyzeExecution,
	VerbCompressAnalysis,
	VerbAdjudicateNextExecution,
	VerbRevisePending,
	VerbRefineTestsWrite,
	VerbRefineTestsCritique,
	VerbRefineTestsJudge,
}

// Project represents a row in the projects table.
type Project struct {
	ID                       int64
	Label                    string
	FolderPath               string
	DesignDocPath            string
	Status                   string // 'active' | 'full_stopped' | 'complete' | 'paused' | 'fixture'
	RecoveredFromProjectID   sql.NullInt64
	MonitorOverrideDefault   string // 'honor' | 'ignore'
	ExecutionBudgetDefault   int
	AuditReconcileRoundCap   int
	MaxExecutionAttempts     int
	Language                 string // 'go' | 'python' | ... (default 'go')
	PauseAfterReconcile      bool
	PauseAfterVerb           sql.NullString
	PauseAfterBeadID         sql.NullInt64
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// VerbModelAssignment represents a row in the verb_model_assignments table.
type VerbModelAssignment struct {
	ProjectID int64
	Verb      string
	Model     string
}

// Bead represents a row in the beads table.
type Bead struct {
	ID               int64
	ProjectID        int64
	Status           string // 'pending' | 'executing' | 'succeeded' | 'failed' | 'full_stopped'
	CurrentRevisionID sql.NullInt64
}

// BeadRevision represents a row in the bead_revisions table.
type BeadRevision struct {
	ID              int64
	ProjectID       int64
	BeadID          int64
	RevisionNumber  int
	FullText        string
	ExecutionBudget int
	MonitorOverride string // 'honor' | 'ignore'
	CreatedByVerb   string // 'DECOMPOSE_SPEC' | 'RECONCILE_DECOMPOSITION' | 'ADJUDICATE_NEXT_EXECUTION'
	CreatedAt       time.Time
}

// AuditReconcileRound represents a row in the audit_reconcile_rounds table.
type AuditReconcileRound struct {
	ID             int64
	ProjectID      int64
	RoundNumber    int
	CritiqueText   string
	Reconciliation string
	Outcome        string // 'converged' | 'disagreed_continuing' | 'escalated' | 'redecompose' | 'reconcile_rejected'
	CreatedAt      time.Time
}

// Execution represents a row in the executions table.
// TerminationCause, MonitorFired, and MonitorHonored are written by three
// separate actors at three separate times; they are nullable until written.
type Execution struct {
	ID               int64
	ProjectID        int64
	BeadID           int64
	BeadRevisionID   int64
	TracePath        string
	TerminationCause sql.NullString // 'success' | 'timeout' | 'monitor_terminated' | 'monitor_force_killed'
	MonitorFired     sql.NullBool
	MonitorHonored   sql.NullBool
	StartedAt        time.Time
	EndedAt          sql.NullTime
}

// Analysis represents a row in the analyses table.
type Analysis struct {
	ID                     int64
	ProjectID              int64
	ExecutionID            int64
	MechanicalFindings     string
	AnalyzerInterpretation sql.NullString
	CreatedAt              time.Time
}

// CompressedHistory represents a row in the compressed_history table.
type CompressedHistory struct {
	BeadID         int64
	ProjectID      int64
	CompressedText string
	UpdatedAt      time.Time
}

// Adjudication represents a row in the adjudications table.
type Adjudication struct {
	ID                      int64
	ProjectID               int64
	BeadID                  int64
	ExecutionID             int64
	Trend                   string  // 'same' | 'narrower' | 'unrelated'
	BeadSpecFit             string  // 'bead_problem' | 'execution_capability_problem'
	ReasoningText           string
	AttemptBudgetCost       float64
	MonitorEscalationStatus bool
	Decision                string // 'execute_as_is' | 'execute_revised' | 'full_stop'
	CreatedAt               time.Time
}

// HandoffJob represents a row in the handoff_jobs table.
type HandoffJob struct {
	ID                int64
	ProjectID         int64
	Verb              string
	BeadID            sql.NullInt64 // NULL for project-scoped verbs
	Status            string        // 'pending' | 'running' | 'failed_retry' | 'escalated' | 'complete'
	RefinementCycleID sql.NullInt64 // non-NULL only for REFINE_TESTS_A/B jobs
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// HandoffAttempt represents a row in the handoff_attempts table.
type HandoffAttempt struct {
	ID               int64
	JobID            int64
	AttemptNumber    int
	RawOutput        sql.NullString
	ValidationResult string // 'valid' | 'malformed: <reason>'
	CreatedAt        time.Time
	EndedAt          sql.NullTime
}
