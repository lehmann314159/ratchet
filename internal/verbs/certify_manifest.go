package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
)

type CertifyManifest struct {
	folderPath string
}

func (h *CertifyManifest) Verb() string { return db.VerbCertifyManifest }

func (h *CertifyManifest) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	verify, err := latestVerifyAttempt(ctx, d, job.ProjectID)
	if err != nil {
		return "", fmt.Errorf("load verify attempt: %w", err)
	}

	project, err := loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	h.folderPath = project.FolderPath

	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbCertifyManifest)
	if err != nil {
		return "", err
	}

	// Preliminary decision: any failed check → reject.
	preliminary := "approve"
	if !verify.FilePresencePass || !verify.NoBehavioralTestsPass ||
		!verify.CompilePass || !verify.APICheckPass || !verify.StubPurityPass {
		preliminary = "reject"
	}

	manifest, mErr := latestSurveyManifest(ctx, d, job.ProjectID)
	if mErr != nil {
		return "", fmt.Errorf("load survey manifest: %w", mErr)
	}

	userMsg := buildCertifyUserMsg(verify, preliminary, manifest)

	raw, err := oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: guidance.InjectForVerbPath(certifyManifestSystemPrompt(), h.folderPath, db.VerbCertifyManifest, "")},
		{Role: "user", Content: userMsg},
	}, nil)
	if err != nil {
		return "", err
	}

	var modelResp struct {
		ModelReasoning string `json:"model_reasoning"`
		FinalDecision  string `json:"final_decision"`
		Feedback       string `json:"feedback"`
	}
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &modelResp); err != nil {
		return "", fmt.Errorf("parse certify model response: %w", err)
	}

	out := CertifyManifestOutput{
		PreliminaryDecision: preliminary,
		ModelReasoning:      modelResp.ModelReasoning,
		FinalDecision:       modelResp.FinalDecision,
		Feedback:            modelResp.Feedback,
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func buildCertifyUserMsg(verify *VerifyManifestOutput, preliminary string, manifest *SurveySpecOutput) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Preliminary Decision: %s\n\n", preliminary)

	sb.WriteString("## Mechanical Check Results\n\n")
	fmt.Fprintf(&sb, "- file_presence: %s\n", passFailStr(verify.FilePresencePass))
	fmt.Fprintf(&sb, "- no_behavioral_tests: %s\n", passFailStr(verify.NoBehavioralTestsPass))
	fmt.Fprintf(&sb, "- compile: %s\n", passFailStr(verify.CompilePass))
	fmt.Fprintf(&sb, "- api_check: %s\n", passFailStr(verify.APICheckPass))
	fmt.Fprintf(&sb, "- stub_purity: %s\n", passFailStr(verify.StubPurityPass))

	if len(verify.Violations) > 0 {
		sb.WriteString("\n## Violations\n\n")
		for _, v := range verify.Violations {
			fmt.Fprintf(&sb, "- %s\n", v)
		}
	}
	if verify.VerifierInterpretation != "" {
		sb.WriteString("\n## Verifier Interpretation\n\n")
		sb.WriteString(verify.VerifierInterpretation)
	}

	// Include the SURVEY manifest for structural review.
	sb.WriteString("\n## SURVEY Manifest\n\n")
	fmt.Fprintf(&sb, "module: %s  package: %s\n\n", manifest.Module, manifest.Package)
	for _, f := range manifest.Files {
		fmt.Fprintf(&sb, "### %s\n\n```go\n%s\n```\n\n", f.Path, f.Declarations)
	}

	return sb.String()
}

func passFailStr(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

func (h *CertifyManifest) Validate(raw string) (string, any) {
	var out CertifyManifestOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if out.PreliminaryDecision != "approve" && out.PreliminaryDecision != "reject" {
		return fmt.Sprintf("malformed: preliminary_decision must be \"approve\" or \"reject\", got %q", out.PreliminaryDecision), nil
	}
	if out.FinalDecision != "approve" && out.FinalDecision != "reject" {
		return fmt.Sprintf("malformed: final_decision must be \"approve\" or \"reject\", got %q", out.FinalDecision), nil
	}
	return "valid", out
}

func (h *CertifyManifest) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(CertifyManifestOutput)
	now := time.Now().UTC().Format(time.RFC3339)

	// Look up the verify_attempt_id for this project's latest verify attempt.
	var verifyAttemptID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM verify_attempts WHERE project_id = ? ORDER BY created_at DESC LIMIT 1`,
		job.ProjectID,
	).Scan(&verifyAttemptID); err != nil {
		return fmt.Errorf("load verify_attempt_id: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO certifications (
			project_id, verify_attempt_id, preliminary_decision,
			model_reasoning, final_decision, feedback, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		job.ProjectID, verifyAttemptID,
		out.PreliminaryDecision, nullableStr(out.ModelReasoning),
		out.FinalDecision, nullableStr(out.Feedback), now,
	); err != nil {
		return fmt.Errorf("insert certification: %w", err)
	}

	if out.FinalDecision == "approve" {
		return h.commitApprove(ctx, tx, job, now)
	}
	return h.commitReject(ctx, tx, job, now)
}

func (h *CertifyManifest) commitApprove(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, now string) error {
	// Load the manifest (needed for survey.md extraction).
	manifest, err := latestSurveyManifestTx(ctx, tx, job.ProjectID)
	if err != nil {
		return fmt.Errorf("load manifest for extraction: %w", err)
	}

	// Extract survey.md from the materialized files and write it to disk.
	if err := writeSurveyDoc(h.folderPath, manifest); err != nil {
		return fmt.Errorf("write survey.md: %w", err)
	}

	// Enqueue DECOMPOSE_SPEC.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'pending', ?, ?)`,
		job.ProjectID, db.VerbDecomposeSpec, now, now,
	); err != nil {
		return fmt.Errorf("enqueue %s: %w", db.VerbDecomposeSpec, err)
	}
	return nil
}

func (h *CertifyManifest) commitReject(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, now string) error {
	// Wipe all scaffolded Go files so the next SURVEY attempt starts clean.
	wipeGoProject(h.folderPath)

	// Count total rejections for this project (including the one just written).
	var rejectCount int
	_ = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM certifications WHERE project_id = ? AND final_decision = 'reject'`,
		job.ProjectID,
	).Scan(&rejectCount)

	if rejectCount >= 5 {
		_, err := tx.ExecContext(ctx,
			`UPDATE projects SET status = 'full_stopped', updated_at = ? WHERE id = ?`,
			now, job.ProjectID)
		return err
	}

	// Enqueue another SURVEY_SPEC retry.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'pending', ?, ?)`,
		job.ProjectID, db.VerbSurveySpec, now, now)
	return err
}

// latestSurveyManifestTx queries for the latest valid SURVEY_SPEC output
// using a transaction (for use inside Commit).
func latestSurveyManifestTx(ctx context.Context, tx *sql.Tx, projectID int64) (*SurveySpecOutput, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `
		SELECT ha.raw_output
		FROM handoff_jobs hj
		JOIN handoff_attempts ha ON ha.job_id = hj.id
		WHERE hj.project_id = ? AND hj.verb = ?
		  AND hj.status = 'complete'
		  AND ha.validation_result = 'valid'
		ORDER BY hj.created_at DESC
		LIMIT 1`,
		projectID, db.VerbSurveySpec,
	).Scan(&raw); err != nil {
		return nil, fmt.Errorf("latest survey manifest (tx) for project %d: %w", projectID, err)
	}
	var out SurveySpecOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return nil, fmt.Errorf("parse survey manifest: %w", err)
	}
	return &out, nil
}

// writeSurveyDoc generates survey.md from the SURVEY manifest and the
// scaffolded files on disk, then writes it to {folderPath}/survey.md.
func writeSurveyDoc(folderPath string, manifest *SurveySpecOutput) error {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# Survey — %s\n\n", manifest.Module)
	fmt.Fprintf(&sb, "## Module\nmodule: %s\npackage: %s\n\n", manifest.Module, manifest.Package)

	sb.WriteString("## Files\n")
	for _, f := range manifest.Files {
		fmt.Fprintf(&sb, "- %s\n", f.Path)
	}
	sb.WriteString("\n")

	typesAndConsts, packageVars := extractGoDeclarations(folderPath, manifest)
	if typesAndConsts != "" {
		sb.WriteString("## Types and Constants\n\n```go\n")
		sb.WriteString(typesAndConsts)
		sb.WriteString("\n```\n\n")
	}
	if packageVars != "" {
		sb.WriteString("## Package-Level Variables\n\n```go\n")
		sb.WriteString(packageVars)
		sb.WriteString("\n```\n\n")
	}

	// apiCheckTestFilename — read from the scaffolded file on disk.
	if apiContent, err := os.ReadFile(filepath.Join(folderPath, apiCheckTestFilename)); err == nil {
		sb.WriteString("## " + apiCheckTestFilename + "\n\n```go\n")
		sb.WriteString(string(apiContent))
		sb.WriteString("\n```\n\n")
	}

	// Source file declarations (the raw SURVEY output, before scaffolding).
	sb.WriteString("## File Declarations\n")
	for _, f := range manifest.Files {
		if strings.HasSuffix(f.Path, ".go") {
			fmt.Fprintf(&sb, "\n### %s\n\n```go\n%s\n```\n", f.Path, f.Declarations)
		} else {
			fmt.Fprintf(&sb, "\n### %s\n\n```\n%s\n```\n", f.Path, f.Declarations)
		}
	}

	return os.WriteFile(filepath.Join(folderPath, "survey.md"), []byte(sb.String()), 0644)
}
