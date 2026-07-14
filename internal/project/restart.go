package project

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"ratchet/internal/db"
)

// RunRestartProjectMain is the entry point for the `ratchet restart-project`
// subcommand. It full-stops an existing project and creates a fresh one that
// inherits its design doc, fleet, and configuration — the sequence otherwise
// repeated by hand every time a project needs restarting after a design-doc
// correction or a framework fix (full-stop-project, mkdir, cp design_doc.md,
// new-project with the same --fleet/--budget/--max-attempts).
func RunRestartProjectMain(args []string) {
	flags := flag.NewFlagSet("restart-project", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to SQLite database")
	oldProjectID := flags.Int64("project", 0, "project ID to restart (required)")
	newLabel := flags.String("label", "", "label for the new project (required)")
	newFolder := flags.String("folder", "", "folder path for the new project — must not already exist (required)")
	designDocSrc := flags.String("design-doc", "", "path to a design doc to copy in (optional; defaults to copying the old project's own design doc unchanged)")
	fleetFile := flags.String("fleet", "", "path to a fleet JSON file (optional; defaults to reusing the old project's verb→model assignments)")
	monitorOverride := flags.String("monitor-override", "", "override monitor_override_default (optional; defaults to the old project's value)")
	budget := flags.Int("budget", 0, "override execution_budget_default in seconds (optional; defaults to the old project's value)")
	maxAttempts := flags.Int("max-attempts", 0, "override max_execution_attempts (optional; defaults to the old project's value)")
	language := flags.String("language", "", "override language (optional; defaults to the old project's value)")
	_ = flags.Parse(args)

	if *oldProjectID == 0 {
		slog.Error("restart-project: --project is required")
		os.Exit(1)
	}
	if *newLabel == "" {
		slog.Error("restart-project: --label is required")
		os.Exit(1)
	}
	if *newFolder == "" {
		slog.Error("restart-project: --folder is required")
		os.Exit(1)
	}

	d, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("restart-project: open db", "error", err)
		os.Exit(1)
	}
	defer d.Close()

	ctx := context.Background()

	var old struct {
		FolderPath           string
		DesignDocPath        string
		MonitorOverride      string
		ExecutionBudget      int
		MaxExecutionAttempts int
		Language             string
		PauseAfterReconcile  bool
		Status               string
	}
	if err := d.QueryRowContext(ctx, `
		SELECT folder_path, design_doc_path, monitor_override_default,
		       execution_budget_default, max_execution_attempts, language,
		       pause_after_reconcile, status
		FROM projects WHERE id = ?`, *oldProjectID,
	).Scan(&old.FolderPath, &old.DesignDocPath, &old.MonitorOverride,
		&old.ExecutionBudget, &old.MaxExecutionAttempts, &old.Language,
		&old.PauseAfterReconcile, &old.Status,
	); err != nil {
		slog.Error("restart-project: load old project", "id", *oldProjectID, "error", err)
		os.Exit(1)
	}
	if old.Status == "full_stopped" || old.Status == "complete" {
		slog.Error("restart-project: old project is already terminal", "id", *oldProjectID, "status", old.Status)
		os.Exit(1)
	}

	// Resolve config overrides against the old project's own values.
	monitorOverrideVal := old.MonitorOverride
	if *monitorOverride != "" {
		monitorOverrideVal = *monitorOverride
	}
	budgetVal := old.ExecutionBudget
	if *budget != 0 {
		budgetVal = *budget
	}
	maxAttemptsVal := old.MaxExecutionAttempts
	if *maxAttempts != 0 {
		maxAttemptsVal = *maxAttempts
	}
	languageVal := old.Language
	if *language != "" {
		languageVal = *language
	}

	// Fleet: reuse the old project's verb→model assignments unless --fleet overrides.
	var fleet map[string]string
	fleetSource := fmt.Sprintf("inherited from project %d", *oldProjectID)
	if *fleetFile != "" {
		data, err := os.ReadFile(*fleetFile)
		if err != nil {
			slog.Error("restart-project: read fleet file", "path", *fleetFile, "error", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(data, &fleet); err != nil {
			slog.Error("restart-project: parse fleet file", "path", *fleetFile, "error", err)
			os.Exit(1)
		}
		fleetSource = *fleetFile
	} else {
		fleet, err = loadProjectFleet(ctx, d, *oldProjectID)
		if err != nil {
			slog.Error("restart-project: load old project's fleet", "error", err)
			os.Exit(1)
		}
	}

	// Validate the new folder up front, before touching the old project.
	newFolderAbs, err := filepath.Abs(*newFolder)
	if err != nil {
		slog.Error("restart-project: resolve new folder path", "error", err)
		os.Exit(1)
	}
	if _, statErr := os.Stat(newFolderAbs); statErr == nil {
		slog.Error("restart-project: folder already exists", "folder", newFolderAbs)
		os.Exit(1)
	}

	oldFolderAbs, err := filepath.Abs(old.FolderPath)
	if err != nil {
		slog.Error("restart-project: resolve old folder path", "error", err)
		os.Exit(1)
	}
	docSrcPath := *designDocSrc
	if docSrcPath == "" {
		docSrcPath = filepath.Join(oldFolderAbs, old.DesignDocPath)
	}
	if _, statErr := os.Stat(docSrcPath); statErr != nil {
		slog.Error("restart-project: design doc source not found", "path", docSrcPath, "error", statErr)
		os.Exit(1)
	}

	// Everything validated — now full-stop the old project.
	oldLabel, beadsStopped, jobsCancelled, err := fullStopProject(ctx, d, *oldProjectID)
	if err != nil {
		slog.Error("restart-project: full-stop old project", "error", err)
		os.Exit(1)
	}

	// Create the new folder and copy the design doc into it.
	if err := os.MkdirAll(newFolderAbs, 0o755); err != nil {
		slog.Error("restart-project: create new folder", "error", err)
		os.Exit(1)
	}
	newDesignDocName := filepath.Base(old.DesignDocPath)
	newDesignDocPath := filepath.Join(newFolderAbs, newDesignDocName)
	if err := copyFile(docSrcPath, newDesignDocPath); err != nil {
		slog.Error("restart-project: copy design doc", "src", docSrcPath, "dst", newDesignDocPath, "error", err)
		os.Exit(1)
	}

	projectID, err := Create(ctx, d, Params{
		Label:                *newLabel,
		FolderPath:           newFolderAbs,
		DesignDocPath:        newDesignDocName,
		MonitorOverride:      monitorOverrideVal,
		ExecutionBudget:      budgetVal,
		MaxExecutionAttempts: maxAttemptsVal,
		Fleet:                fleet,
		Language:             languageVal,
		PauseAfterReconcile:  old.PauseAfterReconcile,
	})
	if err != nil {
		slog.Error("restart-project: create new project (old project is already full-stopped; folder is prepared at "+newFolderAbs+" — fix the issue and run new-project directly)", "error", err)
		os.Exit(1)
	}

	fmt.Printf("project restarted\n")
	fmt.Printf("  old id:           %d (%s) — full-stopped, %d beads stopped, %d jobs cancelled\n", *oldProjectID, oldLabel, beadsStopped, jobsCancelled)
	fmt.Printf("  new id:           %d\n", projectID)
	fmt.Printf("  new label:        %s\n", *newLabel)
	fmt.Printf("  new folder:       %s\n", newFolderAbs)
	fmt.Printf("  design doc from:  %s\n", docSrcPath)
	fmt.Printf("  fleet:            %s\n", fleetSource)
	fmt.Printf("  SURVEY_SPEC job enqueued (status: pending)\n")
	fmt.Printf("\nif you rebuilt the binary for this restart, restart the daemon so it picks up the new code.\n")
}

// loadProjectFleet returns the current verb→model assignments for a project,
// suitable for reuse as another project's Params.Fleet.
func loadProjectFleet(ctx context.Context, d *db.DB, projectID int64) (map[string]string, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT verb, model FROM verb_model_assignments WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fleet := make(map[string]string)
	for rows.Next() {
		var verb, model string
		if err := rows.Scan(&verb, &model); err != nil {
			return nil, err
		}
		fleet[verb] = model
	}
	return fleet, rows.Err()
}

// copyFile copies src to dst, which must not already exist.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
