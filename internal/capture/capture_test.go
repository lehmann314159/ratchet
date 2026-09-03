package capture

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

func TestNewRejectsEmptyDir(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("New(\"\") should error")
	}
}

func TestBeginDispatchSnapshotsEverything(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	// Project folder: two real files plus a traces/ dir that must be skipped.
	folder := t.TempDir()
	mustWrite(t, filepath.Join(folder, "design_doc.md"), "# doc\n")
	mustWrite(t, filepath.Join(folder, "parser.go"), "package main\n")
	mustWrite(t, filepath.Join(folder, "traces", "big.log"), "lots of bytes\n")

	root := filepath.Join(t.TempDir(), "cap")
	c, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	project := &db.Project{ID: 47, Label: "exprvm-web-baseline-7", FolderPath: folder, DesignDocPath: "design_doc.md", Language: "go"}
	job := &db.HandoffJob{
		ID: 1901, ProjectID: 47, Verb: db.VerbRefineTestsCritique,
		BeadID:            sql.NullInt64{Int64: 305, Valid: true},
		RefinementCycleID: sql.NullInt64{Int64: 1, Valid: true},
		Status:            "running", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	ctx, finalize, err := c.BeginDispatch(context.Background(), d, project, job)
	if err != nil {
		t.Fatalf("BeginDispatch: %v", err)
	}

	dispDir := filepath.Join(root, "001-REFINE_TESTS_CRITIQUE-p47-b305-c1")
	if _, err := os.Stat(dispDir); err != nil {
		t.Fatalf("dispatch dir not created: %v", err)
	}

	// meta.json round-trips the job identity.
	var m meta
	readJSON(t, filepath.Join(dispDir, "meta.json"), &m)
	if m.Verb != db.VerbRefineTestsCritique || m.JobID != 1901 || m.ProjectID != 47 {
		t.Fatalf("meta mismatch: %+v", m)
	}
	if m.BeadID == nil || *m.BeadID != 305 || m.RefinementCycle == nil || *m.RefinementCycle != 1 {
		t.Fatalf("meta bead/cycle wrong: %+v", m)
	}

	// db.sqlite is a real, openable SQLite database.
	snapDB, err := sql.Open("sqlite", filepath.Join(dispDir, "db.sqlite"))
	if err != nil {
		t.Fatalf("open snapshot db: %v", err)
	}
	defer snapDB.Close()
	if err := snapDB.Ping(); err != nil {
		t.Fatalf("snapshot db ping: %v", err)
	}
	var n int
	if err := snapDB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='beads'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("snapshot db missing schema: n=%d err=%v", n, err)
	}

	// folder/ copied, traces/ skipped.
	if _, err := os.Stat(filepath.Join(dispDir, "folder", "parser.go")); err != nil {
		t.Fatalf("folder/parser.go not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dispDir, "folder", "traces")); !os.IsNotExist(err) {
		t.Fatalf("traces/ should have been skipped, stat err = %v", err)
	}

	// The returned context carries the recorder.
	if ollama.RecorderInstalled(ctx) != true {
		t.Fatal("BeginDispatch context should carry an ollama recorder")
	}

	// RecordCall numbers calls within the dispatch.
	c.RecordCall(ollama.CallRecord{Kind: "tools", Model: "qwen3:32b"})
	c.RecordCall(ollama.CallRecord{Kind: "tools", Model: "qwen3:32b"})
	var rec ollama.CallRecord
	readJSON(t, filepath.Join(dispDir, "call-002.json"), &rec)
	if rec.Turn != 2 || rec.Model != "qwen3:32b" {
		t.Fatalf("call-002 wrong: %+v", rec)
	}

	// After finalize, calls are dropped.
	finalize()
	c.RecordCall(ollama.CallRecord{Kind: "tools", Model: "x"})
	if _, err := os.Stat(filepath.Join(dispDir, "call-003.json")); !os.IsNotExist(err) {
		t.Fatalf("call after finalize should be dropped, err = %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
}
