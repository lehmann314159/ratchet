package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ratchet/internal/db"
)

// --- execWorkspace unit tests ---

func TestNewExecWorkspace_SeedsFolderExcludingTraces(t *testing.T) {
	live := t.TempDir()
	mustWrite(t, filepath.Join(live, "game.go"), "package main\n")
	mustWrite(t, filepath.Join(live, "go.mod"), "module x\n\ngo 1.21\n")
	if err := os.MkdirAll(filepath.Join(live, "traces"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(live, "traces", "bead-1-attempt-1.log"), "log data\n")

	ws, err := newExecWorkspace(live)
	if err != nil {
		t.Fatalf("newExecWorkspace: %v", err)
	}
	defer ws.cleanup()

	if _, err := os.Stat(filepath.Join(ws.dir, "game.go")); err != nil {
		t.Errorf("game.go should be seeded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.dir, "traces")); !os.IsNotExist(err) {
		t.Errorf("traces/ must be excluded from the seed, stat err = %v", err)
	}
	if !ws.seedSet["game.go"] || !ws.seedSet["go.mod"] {
		t.Errorf("seedSet missing entries: %v", ws.seedSet)
	}
	if ws.seedSet[filepath.Join("traces", "bead-1-attempt-1.log")] {
		t.Errorf("seedSet must not include traces/: %v", ws.seedSet)
	}
}

func TestExecWorkspace_CopyBack(t *testing.T) {
	live := t.TempDir()
	mustWrite(t, filepath.Join(live, "game.go"), "package main // stub\n")
	mustWrite(t, filepath.Join(live, "sibling.go"), "package main // owned by another bead\n")

	ws, err := newExecWorkspace(live)
	if err != nil {
		t.Fatalf("newExecWorkspace: %v", err)
	}
	defer ws.cleanup()

	// Model writes: a legit output file, a sibling it does not own, and a stray.
	mustWrite(t, filepath.Join(ws.dir, "game.go"), "package main // real impl\n")
	mustWrite(t, filepath.Join(ws.dir, "sibling.go"), "package main // CORRUPTED\n")
	mustWrite(t, filepath.Join(ws.dir, "scratch.go"), "package main\nfunc main() {}\n")

	discarded, err := ws.copyBack([]string{"game.go"})
	if err != nil {
		t.Fatalf("copyBack: %v", err)
	}

	if got := readFile(t, filepath.Join(live, "game.go")); got != "package main // real impl\n" {
		t.Errorf("game.go not copied back: %q", got)
	}
	if got := readFile(t, filepath.Join(live, "sibling.go")); got != "package main // owned by another bead\n" {
		t.Errorf("sibling.go must be unmodified in the live folder, got: %q", got)
	}
	if _, err := os.Stat(filepath.Join(live, "scratch.go")); !os.IsNotExist(err) {
		t.Errorf("scratch.go must not reach the live folder, stat err = %v", err)
	}
	if want := []string{"scratch.go", "sibling.go"}; !equalStrings(discarded, want) {
		t.Errorf("discarded = %v, want %v", discarded, want)
	}

	line := discardedFilesTraceLine(discarded)
	if !strings.Contains(line, "scratch.go") || !strings.Contains(line, "sibling.go") || !strings.HasPrefix(line, "[workspace] discarded 2 file(s)") {
		t.Errorf("unexpected trace line: %q", line)
	}
}

func TestExecWorkspace_CopyBack_NothingDiscardedWhenClean(t *testing.T) {
	live := t.TempDir()
	mustWrite(t, filepath.Join(live, "game.go"), "package main\n")

	ws, err := newExecWorkspace(live)
	if err != nil {
		t.Fatalf("newExecWorkspace: %v", err)
	}
	defer ws.cleanup()
	mustWrite(t, filepath.Join(ws.dir, "game.go"), "package main // impl\n")

	discarded, err := ws.copyBack([]string{"game.go"})
	if err != nil {
		t.Fatalf("copyBack: %v", err)
	}
	if len(discarded) != 0 {
		t.Errorf("discarded = %v, want empty", discarded)
	}
	if discardedFilesTraceLine(discarded) != "" {
		t.Errorf("expected empty trace line")
	}
}

// --- end-to-end through runExecuteBeadReal ---

// TestRunExecuteBeadReal_StrayFileIsDiscardedAndNextAttemptIsClean is the
// baseline-13 bead-319 regression: EXECUTE writes a scratch `package main`
// program alongside its real output file. The stray must never reach the
// project folder, and a subsequent attempt must seed a workspace with no trace
// of it.
func TestRunExecuteBeadReal_StrayFileIsDiscardedAndNextAttemptIsClean(t *testing.T) {
	var turn atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch turn.Add(1) {
		case 1:
			writeToolCalls(w,
				toolCall("write_file", map[string]any{"path": "game.go", "content": "package main\n\nfunc Play() {}\n"}),
				toolCall("write_file", map[string]any{"path": "test_escape.go", "content": "package main\n\nfunc main() { println(1 + 1) }\n"}),
			)
		default:
			writeDone(w) // no tool calls -> exit criteria check -> success
		}
	}))
	defer srv.Close()

	d := openTestDB(t)
	folder := t.TempDir()
	seedRunProject(t, d, folder)
	fullText := `{"title":"B01","full_text":"spec","output_files":["game.go"],"exit_criteria":["test -f game.go"]}`
	execID := seedRunExecution(t, d, folder, fullText)

	if err := runExecuteBeadReal(d, execID, srv.URL); err != nil {
		t.Fatalf("runExecuteBeadReal: %v", err)
	}

	if _, err := os.Stat(filepath.Join(folder, "game.go")); err != nil {
		t.Errorf("game.go must be copied back: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "test_escape.go")); !os.IsNotExist(err) {
		t.Errorf("test_escape.go (a stray package-main file) must NOT reach the project folder")
	}
	if cause := terminationCause(t, d, execID); cause != "success" {
		t.Errorf("termination_cause = %q, want success", cause)
	}
	trace := readFile(t, traceForExec(t, d, execID))
	if !strings.Contains(trace, "[workspace] discarded 1 file(s)") || !strings.Contains(trace, "test_escape.go") {
		t.Errorf("trace missing the discarded-file note:\n%s", trace)
	}

	// A fresh workspace for the next attempt seeds only committed state.
	ws, err := newExecWorkspace(folder)
	if err != nil {
		t.Fatalf("newExecWorkspace (attempt 2): %v", err)
	}
	defer ws.cleanup()
	if ws.seedSet["test_escape.go"] {
		t.Errorf("next attempt's workspace still contains the stray file")
	}
	if !ws.seedSet["game.go"] {
		t.Errorf("next attempt's workspace lost the real output file")
	}
}

// TestRunExecuteBeadReal_PartialProgressSurvivesStall: a model that writes one
// file then stops making progress (only read_file calls) is walled — the one
// finalize directive is injected, then the attempt ends as 'stalled' — and the
// partial file it did write is still copied back (PR #7 flush on every path).
func TestRunExecuteBeadReal_PartialProgressSurvivesStall(t *testing.T) {
	withTestExecBudget(t, 30*time.Millisecond)
	var turn atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if turn.Add(1) == 1 {
			writeToolCalls(w, toolCall("write_file", map[string]any{
				"path": "game.go", "content": "package main\n\nfunc Play() { /* partial */ }\n",
			}))
			return
		}
		time.Sleep(45 * time.Millisecond) // exceed the checkpoint interval
		writeToolCalls(w, toolCall("read_file", map[string]any{"path": "game.go"}))
	}))
	defer srv.Close()

	d := openTestDB(t)
	folder := t.TempDir()
	seedRunProject(t, d, folder)
	fullText := `{"title":"B01","full_text":"spec","output_files":["game.go"],"exit_criteria":["test -f game.go && grep -q real game.go"]}`
	execID := seedRunExecutionBudget(t, d, folder, fullText, 1)

	if err := runExecuteBeadReal(d, execID, srv.URL); err != nil {
		t.Fatalf("runExecuteBeadReal: %v", err)
	}

	if cause := terminationCause(t, d, execID); cause != "stalled" {
		t.Errorf("termination_cause = %q, want stalled", cause)
	}
	tr := readFile(t, traceForExec(t, d, execID))
	if !strings.Contains(tr, "requesting graceful finalize") {
		t.Errorf("trace missing the graceful-finalize directive:\n%s", tr)
	}
	got := readFile(t, filepath.Join(folder, "game.go"))
	if !strings.Contains(got, "partial") {
		t.Errorf("partial progress lost — game.go = %q", got)
	}
}

// TestRunExecuteBeadReal_StallToFinalizeToStalled: a model that only ever calls
// read_file (never a productive write) is walled at the first budget checkpoint
// with no progress, gets exactly one finalize directive, and terminates
// 'stalled' — not 'success', not 'no_write'.
func TestRunExecuteBeadReal_StallToFinalizeToStalled(t *testing.T) {
	withTestExecBudget(t, 30*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		time.Sleep(45 * time.Millisecond)
		writeToolCalls(w, toolCall("read_file", map[string]any{"path": "game.go"}))
	}))
	defer srv.Close()

	d := openTestDB(t)
	folder := t.TempDir()
	seedRunProject(t, d, folder)
	fullText := `{"title":"B01","full_text":"spec","output_files":["game.go"],"exit_criteria":["test -f game.go"]}`
	execID := seedRunExecutionBudget(t, d, folder, fullText, 1)

	if err := runExecuteBeadReal(d, execID, srv.URL); err != nil {
		t.Fatalf("runExecuteBeadReal: %v", err)
	}

	if cause := terminationCause(t, d, execID); cause != "stalled" {
		t.Errorf("termination_cause = %q, want stalled", cause)
	}
	tr := readFile(t, traceForExec(t, d, execID))
	if !strings.Contains(tr, "requesting graceful finalize") {
		t.Errorf("trace missing finalize directive:\n%s", tr)
	}
	if strings.Count(tr, "requesting graceful finalize") != 1 {
		t.Errorf("finalize directive should be injected exactly once, trace:\n%s", tr)
	}
}

// TestRunExecuteBeadReal_ReasoningSpiralIsStalled: two consecutive turns that
// hit the per-turn token cap mid-think (done_reason "length", no tool call, no
// content) trip the spiral predicate → finalize → stalled, regardless of
// wall-clock.
func TestRunExecuteBeadReal_ReasoningSpiralIsStalled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeLengthCapEmpty(w)
	}))
	defer srv.Close()

	d := openTestDB(t)
	folder := t.TempDir()
	seedRunProject(t, d, folder)
	fullText := `{"title":"B01","full_text":"spec","output_files":["game.go"],"exit_criteria":["test -f game.go"]}`
	execID := seedRunExecution(t, d, folder, fullText) // default budget — spiral fires on turn count, not wall

	if err := runExecuteBeadReal(d, execID, srv.URL); err != nil {
		t.Fatalf("runExecuteBeadReal: %v", err)
	}

	if cause := terminationCause(t, d, execID); cause != "stalled" {
		t.Errorf("termination_cause = %q, want stalled", cause)
	}
	tr := readFile(t, traceForExec(t, d, execID))
	if !strings.Contains(tr, "reasoning spiral") {
		t.Errorf("trace should name the reasoning-spiral stall reason:\n%s", tr)
	}
}

// TestRunExecuteBeadReal_SteadyProgressIsNotStalled: a model writing a
// materially-changed output file every turn is never walled — it extends past
// budget checkpoints and finishes 'success'.
func TestRunExecuteBeadReal_SteadyProgressIsNotStalled(t *testing.T) {
	withTestExecBudget(t, 30*time.Millisecond)
	var turn atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := turn.Add(1)
		if n <= 5 {
			time.Sleep(45 * time.Millisecond)
			writeToolCalls(w, toolCall("write_file", map[string]any{
				"path":    "game.go",
				"content": fmt.Sprintf("package main\n\n// revision %d\nfunc Play() {}\n", n),
			}))
			return
		}
		writeDone(w)
	}))
	defer srv.Close()

	d := openTestDB(t)
	folder := t.TempDir()
	seedRunProject(t, d, folder)
	fullText := `{"title":"B01","full_text":"spec","output_files":["game.go"],"exit_criteria":["test -f game.go"]}`
	execID := seedRunExecutionBudget(t, d, folder, fullText, 1)

	if err := runExecuteBeadReal(d, execID, srv.URL); err != nil {
		t.Fatalf("runExecuteBeadReal: %v", err)
	}

	if cause := terminationCause(t, d, execID); cause != "success" {
		t.Errorf("termination_cause = %q, want success", cause)
	}
	tr := readFile(t, traceForExec(t, d, execID))
	if strings.Contains(tr, "requesting graceful finalize") {
		t.Errorf("a steadily-progressing model must not get a finalize directive:\n%s", tr)
	}
	if !strings.Contains(tr, "forward progress detected, extending") {
		t.Errorf("expected at least one budget-checkpoint extension:\n%s", tr)
	}
}

// --- helpers ---

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func toolCall(name string, args map[string]any) map[string]any {
	return map[string]any{"function": map[string]any{"name": name, "arguments": args}}
}

func writeToolCalls(w http.ResponseWriter, calls ...map[string]any) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message": map[string]any{"role": "assistant", "content": "", "tool_calls": calls},
		"done":    true,
	})
}

func writeDone(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message": map[string]any{"role": "assistant", "content": ""},
		"done":    true,
	})
}

// writeLengthCapEmpty simulates a turn that hit the per-turn token cap while
// still reasoning: no content, no tool call, done_reason "length".
func writeLengthCapEmpty(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message":     map[string]any{"role": "assistant", "content": ""},
		"done":        true,
		"done_reason": "length",
	})
}

// withTestExecBudget overrides runExecuteBeadReal's wall-clock budget interval
// for the duration of one test so the stall / checkpoint paths are reachable in
// milliseconds.
func withTestExecBudget(t *testing.T, d time.Duration) {
	t.Helper()
	old := testExecBudget
	testExecBudget = d
	t.Cleanup(func() { testExecBudget = old })
}

func seedRunProject(t *testing.T, d *db.DB, folder string) {
	t.Helper()
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (-1, 'fixture: workspace test', ?, 'design.md',
		        'active', 'honor', 300, 2,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, folder); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (-1, 'EXECUTE_BEAD', 'stub-model')`); err != nil {
		t.Fatalf("seed model assignment: %v", err)
	}
}

func seedRunExecution(t *testing.T, d *db.DB, folder, fullText string) int64 {
	t.Helper()
	return seedRunExecutionBudget(t, d, folder, fullText, 300)
}

func seedRunExecutionBudget(t *testing.T, d *db.DB, folder, fullText string, budget int) int64 {
	t.Helper()
	ctx := context.Background()
	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status, current_revision_id) VALUES (-1, 'executing', NULL)`)
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	beadID, _ := res.LastInsertId()
	res, err = d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text,
		   execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (-1, ?, 1, ?, ?, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		beadID, fullText, budget)
	if err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	revID, _ := res.LastInsertId()
	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
		t.Fatalf("point bead at revision: %v", err)
	}
	tracePath := filepath.Join(folder, "traces", "bead-x-attempt.log")
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o755); err != nil {
		t.Fatal(err)
	}
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
	return execID
}

func terminationCause(t *testing.T, d *db.DB, execID int64) string {
	t.Helper()
	var cause string
	if err := d.QueryRowContext(context.Background(),
		`SELECT termination_cause FROM executions WHERE id = ?`, execID).Scan(&cause); err != nil {
		t.Fatalf("query termination_cause: %v", err)
	}
	return cause
}

func traceForExec(t *testing.T, d *db.DB, execID int64) string {
	t.Helper()
	var p string
	if err := d.QueryRowContext(context.Background(),
		`SELECT trace_path FROM executions WHERE id = ?`, execID).Scan(&p); err != nil {
		t.Fatalf("query trace_path: %v", err)
	}
	return p
}
