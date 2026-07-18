package execution

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ratchet/internal/db"
)

// TestRunExecuteBeadReal_NoWriteAfterWarningIsLabeledNoWrite reproduces the
// Stage 4 audit's "confirmed cosmetic" no-write-warning finding: once the
// no-write warning fires (turn 1: zero tool calls, expectedFiles non-empty)
// and the model still produces zero tool calls on the very next turn, the
// run used to fall through to termination_cause='success' — indistinguishable
// from a normal completion even though nothing was ever written. Verifies it
// now writes 'no_write' instead.
func TestRunExecuteBeadReal_NoWriteAfterWarningIsLabeledNoWrite(t *testing.T) {
	// Every /api/chat call returns an immediately-done, empty-content,
	// zero-tool-call response — simulating a model that produces prose (or
	// nothing) instead of ever calling write_file, on every turn.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"role": "assistant", "content": ""},
			"done":    true,
		})
	}))
	defer srv.Close()

	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()

	folder := t.TempDir()
	if _, err := d.ExecContext(ctx, `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (-1, 'fixture: no-write test', ?, 'design.md',
		        'active', 'honor', 300, 2,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, folder); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (-1, 'EXECUTE_BEAD', 'stub-model')`); err != nil {
		t.Fatalf("seed model assignment: %v", err)
	}

	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status, current_revision_id) VALUES (-1, 'executing', NULL)`)
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	beadID, _ := res.LastInsertId()

	fullText := `{"title":"B01","full_text":"spec","output_files":["game.go"],"exit_criteria":["go build ./..."]}`
	res, err = d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text,
		   execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (-1, ?, 1, ?, 300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		beadID, fullText)
	if err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	revID, _ := res.LastInsertId()
	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
		t.Fatalf("point bead at revision: %v", err)
	}

	tracePath := filepath.Join(folder, "trace.log")
	if err := os.WriteFile(tracePath, nil, 0o644); err != nil {
		t.Fatalf("create trace file: %v", err)
	}
	res, err = d.ExecContext(ctx, `
		INSERT INTO executions
		  (project_id, bead_id, bead_revision_id, trace_path,
		   monitor_honored, started_at)
		VALUES (-1, ?, ?, ?, 1, '2026-01-01T00:00:00Z')`,
		beadID, revID, tracePath)
	if err != nil {
		t.Fatalf("seed execution: %v", err)
	}
	execID, _ := res.LastInsertId()

	if err := runExecuteBeadReal(d, execID, srv.URL); err != nil {
		t.Fatalf("runExecuteBeadReal: %v", err)
	}

	var cause string
	if err := d.QueryRowContext(ctx,
		`SELECT termination_cause FROM executions WHERE id = ?`, execID,
	).Scan(&cause); err != nil {
		t.Fatalf("query termination_cause: %v", err)
	}
	if cause != "no_write" {
		t.Errorf("termination_cause = %q, want %q", cause, "no_write")
	}
}

// TestRunExecuteBeadReal_ExitCriteriaAlreadyPassingIsSuccessNotNoWrite covers
// the case that the no-write warning above cannot distinguish on its own: a
// prior attempt already finished the bead (all output_files correct on disk,
// exit_criteria mechanically passing), so a model that makes zero write_file
// calls this turn is correctly recognizing there is nothing left to do, not
// dodging the write tool. Before this fix, this attempt would have been
// forced through the no-write warning and eventually labeled 'no_write' or
// 'success' only by accident of the model happening to touch a file.
func TestRunExecuteBeadReal_ExitCriteriaAlreadyPassingIsSuccessNotNoWrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"role": "assistant", "content": ""},
			"done":    true,
		})
	}))
	defer srv.Close()

	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()

	folder := t.TempDir()
	if _, err := d.ExecContext(ctx, `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (-1, 'fixture: no-write test', ?, 'design.md',
		        'active', 'honor', 300, 2,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, folder); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (-1, 'EXECUTE_BEAD', 'stub-model')`); err != nil {
		t.Fatalf("seed model assignment: %v", err)
	}

	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status, current_revision_id) VALUES (-1, 'executing', NULL)`)
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	beadID, _ := res.LastInsertId()

	// game.go already exists and the exit criterion already passes — this
	// simulates a prior attempt having already finished the job.
	if err := os.WriteFile(filepath.Join(folder, "game.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("seed game.go: %v", err)
	}
	fullText := `{"title":"B01","full_text":"spec","output_files":["game.go"],"exit_criteria":["test -f game.go"]}`
	res, err = d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text,
		   execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (-1, ?, 1, ?, 300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		beadID, fullText)
	if err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	revID, _ := res.LastInsertId()
	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
		t.Fatalf("point bead at revision: %v", err)
	}

	tracePath := filepath.Join(folder, "trace.log")
	if err := os.WriteFile(tracePath, nil, 0o644); err != nil {
		t.Fatalf("create trace file: %v", err)
	}
	res, err = d.ExecContext(ctx, `
		INSERT INTO executions
		  (project_id, bead_id, bead_revision_id, trace_path,
		   monitor_honored, started_at)
		VALUES (-1, ?, ?, ?, 1, '2026-01-01T00:00:00Z')`,
		beadID, revID, tracePath)
	if err != nil {
		t.Fatalf("seed execution: %v", err)
	}
	execID, _ := res.LastInsertId()

	if err := runExecuteBeadReal(d, execID, srv.URL); err != nil {
		t.Fatalf("runExecuteBeadReal: %v", err)
	}

	var cause string
	if err := d.QueryRowContext(ctx,
		`SELECT termination_cause FROM executions WHERE id = ?`, execID,
	).Scan(&cause); err != nil {
		t.Fatalf("query termination_cause: %v", err)
	}
	if cause != "success" {
		t.Errorf("termination_cause = %q, want %q", cause, "success")
	}

	trace, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	if !strings.Contains(string(trace), "exit criteria already satisfied") {
		t.Errorf("expected trace to record the mechanical short-circuit, got:\n%s", trace)
	}
	if strings.Contains(string(trace), "no-write warning") {
		t.Errorf("expected the no-write warning to be skipped entirely, got:\n%s", trace)
	}
}
