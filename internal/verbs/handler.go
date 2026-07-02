// Package verbs implements the six one-shot verb handlers.
// Each handler satisfies the Handler interface: it loads inputs from the DB
// and filesystem, calls the assigned Ollama model, validates the raw output
// against the verb's schema, and commits the result in a transaction.
//
// EXECUTE_BEAD and MONITOR_EXECUTION are not here; they are subprocess-based
// and live in a separate package (Step 3).
package verbs

import (
	"context"
	"database/sql"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

// Handler is the interface every one-shot verb implements.
type Handler interface {
	// Verb returns the canonical verb name (one of the db.Verb* constants).
	Verb() string

	// Run loads inputs, calls the Ollama model, and returns the raw text
	// output. Errors here are infrastructure failures (network, DB), not
	// malformed output — the orchestrator does not count them as strikes.
	Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (rawOutput string, err error)

	// Validate parses rawOutput against the verb's output schema.
	// Returns ("valid", parsedOutput) on success, or ("malformed: <reason>", nil).
	// parsedOutput is typed per verb (see outputs.go) and passed to Commit.
	Validate(rawOutput string) (validationResult string, parsed any)

	// Commit writes the validated result to the DB within tx, then enqueues
	// the next job(s). Called only when Validate returns "valid".
	Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error
}

// All returns the registry of all one-shot verb handlers, keyed by verb name.
// VERIFY_MANIFEST is included despite being model-free — it satisfies the
// Handler interface and is dispatched through the same path with warmup skipped.
func All(ollamaBase string) map[string]Handler {
	return map[string]Handler{
		db.VerbSurveySpec:              &SurveySpec{},
		db.VerbVerifyManifest:          &VerifyManifest{},
		db.VerbCertifyManifest:         &CertifyManifest{},
		db.VerbDecomposeSpec:           &DecomposeSpec{},
		db.VerbAuditDecomposition:      &AuditDecomposition{},
		db.VerbReconcileDecomposition:  &ReconcileDecomposition{},
		db.VerbAnalyzeExecution:        &AnalyzeExecution{},
		db.VerbCompressAnalysis:        &CompressAnalysis{},
		db.VerbAdjudicateNextExecution: &AdjudicateNextExecution{},
	}
}
