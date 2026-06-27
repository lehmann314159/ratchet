// Package project implements the new-project command: creates a projects row,
// seeds the validated model fleet, and enqueues the first DECOMPOSE_SPEC job.
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
	FolderPath           string         // must exist; stored as absolute path
	DesignDocPath        string         // relative to FolderPath; file must exist
	MonitorOverride      string         // 'honor' | 'ignore' — seed value for DECOMPOSE_SPEC
	ExecutionBudget      int            // seconds — seed value for DECOMPOSE_SPEC
	MaxExecutionAttempts int            // cap on execute→adjudicate retries per bead
	Fleet                map[string]string // verb→model overrides; nil uses compiled-in defaults
}

// Create inserts a projects row, seeds the validated model fleet, and enqueues
// DECOMPOSE_SPEC. Everything is atomic: either the project is fully initialised
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

	res, err := tx.ExecContext(ctx, `
		INSERT INTO projects
		  (label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, max_execution_attempts,
		   created_at, updated_at)
		VALUES (?, ?, ?, 'active', ?, ?, 2, ?, ?, ?)`,
		p.Label, folderAbs, p.DesignDocPath,
		p.MonitorOverride, p.ExecutionBudget,
		p.MaxExecutionAttempts, now, now)
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
		VALUES (?, 'DECOMPOSE_SPEC', NULL, 'pending', ?, ?)`,
		projectID, now, now); err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("enqueue DECOMPOSE_SPEC: %w", err)
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
	fmt.Printf("  DECOMPOSE_SPEC job enqueued (status: pending)\n")
	fmt.Printf("\nstart the orchestrator:\n")
	fmt.Printf("  ratchet --db=%s\n", *dbPath)
}
