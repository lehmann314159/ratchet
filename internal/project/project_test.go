package project_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"ratchet/internal/db"
	"ratchet/internal/project"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// seedFolder creates a temp folder with a design doc and returns the folder path.
func seedFolder(t *testing.T) (folder, designDoc string) {
	t.Helper()
	folder = t.TempDir()
	designDoc = "design_doc.md"
	if err := os.WriteFile(filepath.Join(folder, designDoc), []byte("# Test Design Doc\n"), 0o644); err != nil {
		t.Fatalf("write design doc: %v", err)
	}
	return folder, designDoc
}

func TestCreateHappyPath(t *testing.T) {
	d := openTestDB(t)
	folder, designDoc := seedFolder(t)
	ctx := context.Background()

	projectID, err := project.Create(ctx, d, project.Params{
		Label:           "test project",
		FolderPath:      folder,
		DesignDocPath:   designDoc,
		MonitorOverride: "honor",
		ExecutionBudget: 300,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if projectID <= 0 {
		t.Errorf("projectID = %d, want > 0", projectID)
	}

	// Project row written correctly.
	var label, status, monitorOverride, folderPath, docPath string
	var budget, roundCap int
	if err := d.QueryRowContext(ctx,
		`SELECT label, status, monitor_override_default, execution_budget_default,
		        audit_reconcile_round_cap, folder_path, design_doc_path
		 FROM projects WHERE id = ?`, projectID,
	).Scan(&label, &status, &monitorOverride, &budget, &roundCap, &folderPath, &docPath); err != nil {
		t.Fatalf("read project row: %v", err)
	}
	if label != "test project" {
		t.Errorf("label = %q", label)
	}
	if status != "active" {
		t.Errorf("status = %q, want active", status)
	}
	if monitorOverride != "honor" {
		t.Errorf("monitor_override_default = %q, want honor", monitorOverride)
	}
	if budget != 300 {
		t.Errorf("execution_budget_default = %d, want 300", budget)
	}
	if roundCap != 2 {
		t.Errorf("audit_reconcile_round_cap = %d, want 2", roundCap)
	}
	// folder_path stored as absolute.
	if !filepath.IsAbs(folderPath) {
		t.Errorf("folder_path %q not absolute", folderPath)
	}
	if docPath != designDoc {
		t.Errorf("design_doc_path = %q, want %q", docPath, designDoc)
	}

	// Model fleet seeded: 7 assignments (EXECUTE_BEAD has no model).
	var assignmentCount int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM verb_model_assignments WHERE project_id = ?`, projectID,
	).Scan(&assignmentCount); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if assignmentCount != 7 {
		t.Errorf("verb_model_assignments = %d, want 7", assignmentCount)
	}

	// DECOMPOSE_SPEC and RECONCILE_DECOMPOSITION share a model.
	var decomposeModel, reconcileModel string
	_ = d.QueryRowContext(ctx, `SELECT model FROM verb_model_assignments WHERE project_id=? AND verb='DECOMPOSE_SPEC'`, projectID).Scan(&decomposeModel)
	_ = d.QueryRowContext(ctx, `SELECT model FROM verb_model_assignments WHERE project_id=? AND verb='RECONCILE_DECOMPOSITION'`, projectID).Scan(&reconcileModel)
	if decomposeModel != reconcileModel {
		t.Errorf("DECOMPOSE model %q != RECONCILE model %q (must share)", decomposeModel, reconcileModel)
	}

	// AUDIT_DECOMPOSITION uses a different model from DECOMPOSE_SPEC.
	var auditModel string
	_ = d.QueryRowContext(ctx, `SELECT model FROM verb_model_assignments WHERE project_id=? AND verb='AUDIT_DECOMPOSITION'`, projectID).Scan(&auditModel)
	if auditModel == decomposeModel {
		t.Errorf("AUDIT model %q == DECOMPOSE model %q (must differ)", auditModel, decomposeModel)
	}

	// DECOMPOSE_SPEC job enqueued with status=pending.
	var verb, jobStatus string
	if err := d.QueryRowContext(ctx,
		`SELECT verb, status FROM handoff_jobs WHERE project_id = ?`, projectID,
	).Scan(&verb, &jobStatus); err != nil {
		t.Fatalf("read handoff_job: %v", err)
	}
	if verb != db.VerbDecomposeSpec {
		t.Errorf("job verb = %q, want DECOMPOSE_SPEC", verb)
	}
	if jobStatus != "pending" {
		t.Errorf("job status = %q, want pending", jobStatus)
	}
}

func TestCreateFolderNotExist(t *testing.T) {
	d := openTestDB(t)
	_, err := project.Create(context.Background(), d, project.Params{
		Label:           "x",
		FolderPath:      "/does/not/exist/at/all",
		DesignDocPath:   "design_doc.md",
		MonitorOverride: "honor",
		ExecutionBudget: 300,
	})
	if err == nil {
		t.Error("expected error when folder does not exist")
	}
}

func TestCreateDesignDocMissing(t *testing.T) {
	d := openTestDB(t)
	folder := t.TempDir() // folder exists but no design doc
	_, err := project.Create(context.Background(), d, project.Params{
		Label:           "x",
		FolderPath:      folder,
		DesignDocPath:   "design_doc.md",
		MonitorOverride: "honor",
		ExecutionBudget: 300,
	})
	if err == nil {
		t.Error("expected error when design doc is missing")
	}
}

func TestCreateInvalidMonitorOverride(t *testing.T) {
	d := openTestDB(t)
	folder, designDoc := seedFolder(t)
	_, err := project.Create(context.Background(), d, project.Params{
		Label:           "x",
		FolderPath:      folder,
		DesignDocPath:   designDoc,
		MonitorOverride: "maybe",
		ExecutionBudget: 300,
	})
	if err == nil {
		t.Error("expected error for invalid monitor-override value")
	}
}

func TestCreateInvalidBudget(t *testing.T) {
	d := openTestDB(t)
	folder, designDoc := seedFolder(t)
	_, err := project.Create(context.Background(), d, project.Params{
		Label:           "x",
		FolderPath:      folder,
		DesignDocPath:   designDoc,
		MonitorOverride: "honor",
		ExecutionBudget: 0,
	})
	if err == nil {
		t.Error("expected error for zero budget")
	}
}

func TestCreateFolderPathStoredAbsolute(t *testing.T) {
	// Passing a relative path — stored value must be absolute.
	d := openTestDB(t)
	folder, designDoc := seedFolder(t)
	ctx := context.Background()

	// Change to the folder's parent so we can pass a relative path.
	orig, _ := os.Getwd()
	_ = os.Chdir(filepath.Dir(folder))
	defer os.Chdir(orig)

	relFolder := filepath.Base(folder)
	projectID, err := project.Create(ctx, d, project.Params{
		Label:           "relative path test",
		FolderPath:      relFolder,
		DesignDocPath:   designDoc,
		MonitorOverride: "honor",
		ExecutionBudget: 60,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var storedPath string
	_ = d.QueryRowContext(ctx,
		`SELECT folder_path FROM projects WHERE id = ?`, projectID).Scan(&storedPath)
	if !filepath.IsAbs(storedPath) {
		t.Errorf("folder_path %q stored as relative, want absolute", storedPath)
	}
}

func TestCreateIgnoreMonitorOverride(t *testing.T) {
	d := openTestDB(t)
	folder, designDoc := seedFolder(t)
	ctx := context.Background()

	projectID, err := project.Create(ctx, d, project.Params{
		Label:           "ignore override test",
		FolderPath:      folder,
		DesignDocPath:   designDoc,
		MonitorOverride: "ignore",
		ExecutionBudget: 120,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var override string
	_ = d.QueryRowContext(ctx,
		`SELECT monitor_override_default FROM projects WHERE id = ?`, projectID).Scan(&override)
	if override != "ignore" {
		t.Errorf("monitor_override_default = %q, want ignore", override)
	}
}
