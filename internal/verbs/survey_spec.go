package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
)

type SurveySpec struct{}

func (h *SurveySpec) Verb() string { return db.VerbSurveySpec }

func (h *SurveySpec) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	doc, err := loadDesignDoc(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	project, err := loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbSurveySpec)
	if err != nil {
		return "", err
	}

	// Load rejection feedback from a prior CERTIFY attempt, if any.
	feedback, err := latestCertifyFeedback(ctx, d, job.ProjectID)
	if err != nil {
		return "", fmt.Errorf("load certify feedback: %w", err)
	}

	// Load the last schema-validation failure for this job, if any.
	lastFailure, err := loadLastValidationFailure(ctx, d, job.ID)
	if err != nil {
		return "", fmt.Errorf("load last validation failure: %w", err)
	}

	userMsg := buildSurveyUserMsg(doc, feedback, lastFailure)
	systemPrompt := guidance.InjectForVerb(surveySpecSystemPrompt(), project.Language, db.VerbSurveySpec, "")

	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMsg},
	}, nil)
}

func buildSurveyUserMsg(doc, certifyFeedback, lastValidationFailure string) string {
	var sb strings.Builder
	if certifyFeedback != "" {
		sb.WriteString("## Rejection Feedback from Previous Attempt\n\n")
		sb.WriteString(certifyFeedback)
		sb.WriteString("\n\n")
	}
	if lastValidationFailure != "" {
		sb.WriteString("## Schema Error from Previous Attempt\n\n")
		sb.WriteString(lastValidationFailure)
		sb.WriteString("\n\n")
	}
	sb.WriteString("## Design Document\n\n")
	sb.WriteString(doc)
	return sb.String()
}

func (h *SurveySpec) Validate(raw string) (string, any) {
	var out SurveySpecOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if out.Module == "" {
		return "malformed: module is empty", nil
	}
	if out.Package == "" {
		return "malformed: package is empty", nil
	}
	if len(out.Files) == 0 {
		return "malformed: files array is empty", nil
	}
	for i, f := range out.Files {
		if f.Path == "" {
			return fmt.Sprintf("malformed: files[%d] missing path", i), nil
		}
		if f.Declarations == "" {
			return fmt.Sprintf("malformed: files[%d] (%s) missing declarations", i, f.Path), nil
		}
	}
	return "valid", out
}

func (h *SurveySpec) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'pending', ?, ?)`,
		job.ProjectID, db.VerbVerifyManifest, now, now)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", db.VerbVerifyManifest, err)
	}
	return nil
}
