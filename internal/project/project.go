// Package project implements the new-project command: creates a projects row,
// seeds the validated model fleet, and enqueues the first SURVEY_SPEC job.
package project

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"ratchet/internal/db"
)

// Params holds everything needed to create a new project.
type Params struct {
	Label                string
	FolderPath           string            // must exist; stored as absolute path
	DesignDocPath        string            // relative to FolderPath; file must exist
	MonitorOverride      string            // 'honor' | 'ignore' — seed value for DECOMPOSE_SPEC
	ExecutionBudget      int               // seconds — seed value for DECOMPOSE_SPEC
	MaxExecutionAttempts int               // cap on execute→adjudicate retries per bead
	Fleet                map[string]string // verb→model overrides; nil uses compiled-in defaults
	Language             string            // project language (default "go")
	PauseAfterReconcile  bool              // halt after RECONCILE converges; resume with resume-project
	PauseAfterVerb       string            // halt after this verb's forward-progress branch commits; "" disables
	PauseAfterBeadID     int64             // halt once this bead succeeds and REVISE_PENDING dispatches the next one; 0 disables
}

// pausableVerbs lists the verbs that actually check pause_after_verb (see
// shouldPauseAfterVerb's call sites in internal/verbs). Bead IDs are assigned
// by DECOMPOSE_SPEC (and are a global auto-increment across all projects, not
// scoped per-project), so --pause-after-bead can't be validated against real
// bead IDs at project-creation time — only --pause-after-verb is checked here.
var pausableVerbs = []string{
	db.VerbVerifyManifest,
	db.VerbCertifyManifest,
	db.VerbReconcileDecomposition,
	db.VerbAdjudicateNextExecution,
	db.VerbRefineTestsJudge,
}

// Create inserts a projects row, seeds the validated model fleet, and enqueues
// SURVEY_SPEC. Everything is atomic: either the project is fully initialised
// or nothing is written. Returns the new project ID.
//
// Preconditions (checked before writing anything):
//   - FolderPath must already exist (Mike creates and owns it)
//   - DesignDocPath must exist at FolderPath/DesignDocPath (Mike authors it)
func Create(ctx context.Context, d *db.DB, p Params) (int64, error) {
	if p.MonitorOverride != "honor" && p.MonitorOverride != "ignore" {
		return 0, fmt.Errorf("monitor-override must be 'honor' or 'ignore', got %q", p.MonitorOverride)
	}
	if p.ExecutionBudget <= 0 {
		return 0, fmt.Errorf("budget must be a positive number of seconds, got %d", p.ExecutionBudget)
	}
	if p.MaxExecutionAttempts <= 0 {
		return 0, fmt.Errorf("max-attempts must be a positive integer, got %d", p.MaxExecutionAttempts)
	}
	if p.Language == "" {
		p.Language = "go"
	}
	if p.PauseAfterVerb != "" {
		valid := false
		for _, v := range pausableVerbs {
			if p.PauseAfterVerb == v {
				valid = true
				break
			}
		}
		if !valid {
			return 0, fmt.Errorf("pause-after-verb must be one of %v, got %q", pausableVerbs, p.PauseAfterVerb)
		}
	}

	// Resolve to absolute path so traces and design-doc reads work regardless
	// of where the orchestrator is invoked from later.
	folderAbs, err := filepath.Abs(p.FolderPath)
	if err != nil {
		return 0, fmt.Errorf("resolve folder path: %w", err)
	}
	if _, err := os.Stat(folderAbs); os.IsNotExist(err) {
		return 0, fmt.Errorf("folder does not exist: %s", folderAbs)
	}

	designDocFull := filepath.Join(folderAbs, p.DesignDocPath)
	if _, err := os.Stat(designDocFull); os.IsNotExist(err) {
		return 0, fmt.Errorf("design doc not found: %s", designDocFull)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}

	pauseAfterReconcile := 0
	if p.PauseAfterReconcile {
		pauseAfterReconcile = 1
	}
	var pauseAfterVerb any
	if p.PauseAfterVerb != "" {
		pauseAfterVerb = p.PauseAfterVerb
	}
	var pauseAfterBeadID any
	if p.PauseAfterBeadID != 0 {
		pauseAfterBeadID = p.PauseAfterBeadID
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO projects
		  (label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, max_execution_attempts,
		   language, pause_after_reconcile, pause_after_verb, pause_after_bead_id,
		   created_at, updated_at)
		VALUES (?, ?, ?, 'active', ?, ?, 2, ?, ?, ?, ?, ?, ?, ?)`,
		p.Label, folderAbs, p.DesignDocPath,
		p.MonitorOverride, p.ExecutionBudget,
		p.MaxExecutionAttempts, p.Language, pauseAfterReconcile,
		pauseAfterVerb, pauseAfterBeadID, now, now)
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("insert project: %w", err)
	}
	projectID, _ := res.LastInsertId()

	var seedErr error
	if p.Fleet != nil {
		seedErr = db.SeedVerbModelAssignmentsFromFleet(ctx, tx, projectID, p.Fleet)
	} else {
		seedErr = db.SeedVerbModelAssignments(ctx, tx, projectID)
	}
	if seedErr != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("seed model assignments: %w", seedErr)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'pending', ?, ?)`,
		projectID, db.VerbSurveySpec, now, now); err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("enqueue %s: %w", db.VerbSurveySpec, err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return projectID, nil
}

// RunNewProjectMain is the entry point for the `ratchet new-project` subcommand.
func RunNewProjectMain(args []string) {
	flags := flag.NewFlagSet("new-project", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to SQLite database")
	label := flags.String("label", "", "project label (required)")
	folder := flags.String("folder", "", "project folder path — must exist and contain the design doc (required)")
	designDoc := flags.String("design-doc", "design_doc.md", "design doc filename, relative to --folder")
	monitorOverride := flags.String("monitor-override", "honor", "seed value for monitor_override on each Bead: honor or ignore")
	budget := flags.Int("budget", 900, "seed value for execution_budget on each Bead, in seconds")
	maxAttempts := flags.Int("max-attempts", 5, "maximum execute→adjudicate retries per Bead before escalation")
	fleetFile := flags.String("fleet", "", "path to a JSON file mapping verb names to model names (optional; omit to use compiled-in defaults)")
	language := flags.String("language", "go", "programming language for this project (default: go)")
	pauseAfterReconcile := flags.Bool("pause-after-reconcile", false, "halt after RECONCILE converges instead of starting bead execution; resume with resume-project")
	pauseAfterVerb := flags.String("pause-after-verb", "", fmt.Sprintf("halt after this verb's forward-progress branch commits; resume with resume-project. One of: %v", pausableVerbs))
	pauseAfterBead := flags.Int64("pause-after-bead", 0, "halt once this bead ID succeeds and REVISE_PENDING dispatches the next one; resume with resume-project. Bead IDs are assigned during decomposition, so this is only useful once you already know one (e.g. re-running a design doc you've decomposed before)")
	_ = flags.Parse(args)

	if *label == "" {
		slog.Error("new-project: --label is required")
		os.Exit(1)
	}
	if *folder == "" {
		slog.Error("new-project: --folder is required")
		os.Exit(1)
	}

	var fleet map[string]string
	if *fleetFile != "" {
		data, err := os.ReadFile(*fleetFile)
		if err != nil {
			slog.Error("new-project: read fleet file", "path", *fleetFile, "error", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(data, &fleet); err != nil {
			slog.Error("new-project: parse fleet file", "path", *fleetFile, "error", err)
			os.Exit(1)
		}
	}

	d, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("new-project: open db", "error", err)
		os.Exit(1)
	}
	defer d.Close()

	projectID, err := Create(context.Background(), d, Params{
		Label:                *label,
		FolderPath:           *folder,
		DesignDocPath:        *designDoc,
		MonitorOverride:      *monitorOverride,
		ExecutionBudget:      *budget,
		MaxExecutionAttempts: *maxAttempts,
		Fleet:                fleet,
		Language:             *language,
		PauseAfterReconcile:  *pauseAfterReconcile,
		PauseAfterVerb:       *pauseAfterVerb,
		PauseAfterBeadID:     *pauseAfterBead,
	})
	if err != nil {
		slog.Error("new-project: create failed", "error", err)
		os.Exit(1)
	}

	folderAbs, _ := filepath.Abs(*folder)
	fmt.Printf("project created\n")
	fmt.Printf("  id:         %d\n", projectID)
	fmt.Printf("  label:      %s\n", *label)
	fmt.Printf("  folder:     %s\n", folderAbs)
	fmt.Printf("  design doc: %s\n", filepath.Join(folderAbs, *designDoc))
	fmt.Printf("  language:   %s\n", *language)
	if *pauseAfterReconcile {
		fmt.Printf("  pause:      after RECONCILE (resume with: ratchet resume-project --db=%s --project=%d)\n", *dbPath, projectID)
	}
	if *pauseAfterVerb != "" {
		fmt.Printf("  pause:      after %s (resume with: ratchet resume-project --db=%s --project=%d)\n", *pauseAfterVerb, *dbPath, projectID)
	}
	if *pauseAfterBead != 0 {
		fmt.Printf("  pause:      after bead %d succeeds (resume with: ratchet resume-project --db=%s --project=%d)\n", *pauseAfterBead, *dbPath, projectID)
	}
	fmt.Printf("  SURVEY_SPEC job enqueued (status: pending)\n")
	fmt.Printf("\nstart the orchestrator:\n")
	fmt.Printf("  ratchet start --db=%s --ollama=... --addr=localhost:7474\n", *dbPath)
}
