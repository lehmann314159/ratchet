package verbs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ratchet/internal/db"
)

// seedSurveyManifest inserts a completed, valid SURVEY_SPEC job+attempt so
// latestSurveyManifest (used by WriteScaffoldStubs) can find it.
func seedSurveyManifest(t *testing.T, d *db.DB, projectID int64, manifest SurveySpecOutput) {
	t.Helper()
	ctx := context.Background()
	res, err := d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'complete', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		projectID, db.VerbSurveySpec)
	if err != nil {
		t.Fatalf("seed handoff_jobs: %v", err)
	}
	jobID, _ := res.LastInsertId()

	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO handoff_attempts (job_id, attempt_number, raw_output, validation_result, created_at)
		VALUES (?, 1, ?, 'valid', '2026-01-01T00:00:00Z')`,
		jobID, string(raw)); err != nil {
		t.Fatalf("seed handoff_attempts: %v", err)
	}
}

// TestWriteScaffoldStubs_NonManifestFileIsDeletedNotSilentlySkipped
// reproduces the project-96/bead-617 incident: rewind-bead asked to reset an
// output file (templates/index.html) that SURVEY never scaffolded (only .go
// files are scaffolded). Before this fix, WriteScaffoldStubs silently did
// nothing for it — no write, no error, no deletion — while rewind.go still
// printed it under "impl files stubbed", falsely claiming a clean reset.
func TestWriteScaffoldStubs_NonManifestFileIsDeletedNotSilentlySkipped(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, 1, "scaffold-stubs-non-manifest")
	seedSurveyManifest(t, d, 1, SurveySpecOutput{
		Module: "example.com/m", Package: "main",
		Files: []SurveyManifestFile{{Path: "game.go", Declarations: "func NewGame() *Game { return &Game{} }\n\ntype Game struct{}\n"}},
	})

	dir := t.TempDir()
	// templates/index.html was written directly by a prior EXECUTE_BEAD run
	// (never part of the SURVEY manifest) and contains stale/broken content.
	writeGoFile(t, dir, "templates/index.html", "<html>broken</html>")
	writeGoFile(t, dir, "game.go", "package main\n\nfunc NewGame() *Game { panic(\"broken\") }\n")

	stubbed, deleted, err := WriteScaffoldStubs(context.Background(), d, 1, dir,
		[]string{"game.go", "templates/index.html"})
	if err != nil {
		t.Fatalf("WriteScaffoldStubs: %v", err)
	}

	if len(stubbed) != 1 || stubbed[0] != "game.go" {
		t.Errorf("expected game.go reported as stubbed, got %v", stubbed)
	}
	if len(deleted) != 1 || deleted[0] != "templates/index.html" {
		t.Errorf("expected templates/index.html reported as deleted, got %v", deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, "templates/index.html")); !os.IsNotExist(err) {
		t.Errorf("expected templates/index.html to actually be removed from disk, stat err = %v", err)
	}
}

func TestWriteScaffoldStubs_ManifestFileOverwrittenWithStub(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, 1, "scaffold-stubs-manifest")
	seedSurveyManifest(t, d, 1, SurveySpecOutput{
		Module: "example.com/m", Package: "main",
		Files: []SurveyManifestFile{{Path: "game.go", Declarations: "func NewGame() *Game { return &Game{} }\n\ntype Game struct{}\n"}},
	})

	dir := t.TempDir()
	writeGoFile(t, dir, "game.go", "package main\n\nfunc NewGame() *Game { panic(\"broken\") }\n")

	stubbed, deleted, err := WriteScaffoldStubs(context.Background(), d, 1, dir, []string{"game.go"})
	if err != nil {
		t.Fatalf("WriteScaffoldStubs: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("expected no deletions, got %v", deleted)
	}
	if len(stubbed) != 1 {
		t.Fatalf("expected game.go stubbed, got %v", stubbed)
	}
	content, err := os.ReadFile(filepath.Join(dir, "game.go"))
	if err != nil {
		t.Fatalf("read game.go: %v", err)
	}
	if got := string(content); got == "package main\n\nfunc NewGame() *Game { panic(\"broken\") }\n" {
		t.Errorf("game.go was not reset to its scaffold stub, still contains broken content")
	}
}

// TestWriteScaffoldStubs_GoModRegeneratedNotDeleted reproduces the
// checkers-try-1 bead-684 incident: rewind-bead deleted go.mod because it's
// never part of the SURVEY manifest's per-file declarations (scaffoldGoProject
// writes it mechanically from module+goVer, not from a manifest entry), even
// though it has a perfectly deterministic baseline. Deleting it left the
// package permanently uncompilable — REFINE_TESTS_WRITE's own compile check
// failed 3 times with "go.mod file not found" and escalated without ever
// reaching EXECUTE_BEAD.
func TestWriteScaffoldStubs_GoModRegeneratedNotDeleted(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, 1, "scaffold-stubs-gomod")
	seedSurveyManifest(t, d, 1, SurveySpecOutput{
		Module: "example.com/checkers", Package: "main",
		Files: []SurveyManifestFile{{Path: "game.go", Declarations: "func NewGame() *Game { return &Game{} }\n\ntype Game struct{}\n"}},
	})

	dir := t.TempDir()
	writeGoFile(t, dir, "game.go", "package main\n\nfunc NewGame() *Game { return nil }\n")
	// go.mod deliberately absent, matching rewind-bead's delete-then-restore flow.

	stubbed, deleted, err := WriteScaffoldStubs(context.Background(), d, 1, dir,
		[]string{"game.go", "go.mod"})
	if err != nil {
		t.Fatalf("WriteScaffoldStubs: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("expected go.mod not to be deleted, got deleted = %v", deleted)
	}
	found := false
	for _, f := range stubbed {
		if f == "go.mod" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected go.mod reported as stubbed, got %v", stubbed)
	}
	content, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatalf("expected go.mod to exist on disk: %v", err)
	}
	if !strings.Contains(string(content), "module example.com/checkers") {
		t.Errorf("go.mod content = %q, want it to declare the manifest's module", content)
	}
}

func TestWriteScaffoldStubs_MissingNonManifestFileIsNotAnError(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, 1, "scaffold-stubs-missing")
	seedSurveyManifest(t, d, 1, SurveySpecOutput{Module: "example.com/m", Package: "main"})

	dir := t.TempDir()
	// templates/index.html was never written at all — restoreMissingScaffolds
	// calls this path for files absent from disk.
	if _, _, err := WriteScaffoldStubs(context.Background(), d, 1, dir, []string{"templates/index.html"}); err != nil {
		t.Errorf("expected no error deleting an already-absent file, got %v", err)
	}
}

// TestWriteAPICheckTest_ProtectsExportedConstants reproduces the gap the
// audit found: every game project here declares an exported const enum
// (Color/Status/Move) with no regression protection, because
// exportedAssertions only ever handled token.VAR. This writes a real
// manifest file with an exported const block, an unexported const, and an
// exported function, then asserts on the actual generated file content that
// every exported const got a "_ = X" assertion and the unexported one did
// not (matching the existing var behavior for unexported identifiers).
func TestWriteAPICheckTest_ProtectsExportedConstants(t *testing.T) {
	dir := t.TempDir()
	files := []SurveyManifestFile{{
		Path: "game.go",
		Declarations: `type Color int

const (
	Black Color = 1
	White Color = 2
	unexportedHidden Color = 3
)

func NewGame() *Color { return nil }
`,
	}}
	if err := writeAPICheckTest("main", dir, files); err != nil {
		t.Fatalf("writeAPICheckTest: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, apiCheckTestFilename))
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	got := string(content)
	for _, want := range []string{"_ = Black", "_ = White", "_ = NewGame"} {
		if !strings.Contains(got, want) {
			t.Errorf("generated %s missing %q\ngot:\n%s", apiCheckTestFilename, want, got)
		}
	}
	if strings.Contains(got, "unexportedHidden") {
		t.Errorf("generated %s must not assert unexported identifiers\ngot:\n%s", apiCheckTestFilename, got)
	}
}
