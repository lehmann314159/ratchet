// Package capture records the exact inputs to each verb dispatch — the DB
// state, the project folder, the job row, and every model call — so the
// model-qualification harness (cmd/qualify-model) can replay a verb's real
// Run() against a candidate model without a live project.
//
// It is opt-in: the orchestrator installs a *Capturer via
// orchestrator.WithCapturer only when `ratchet start --capture-verb-io <dir>`
// is passed. With no Capturer installed the instrumentation is inert.
//
// Layout, one directory per non-EXECUTE_BEAD verb dispatch:
//
//	<dir>/001-REFINE_TESTS_CRITIQUE-p47-b305-c1/
//	    meta.json        job row + project fields + timestamps
//	    db.sqlite        VACUUM INTO copy of the whole DB at dispatch time
//	    folder/          copy of the project folder (minus traces/)
//	    call-001.json    ollama.CallRecord for the verb's 1st model call
//	    call-002.json    ... 2nd ... (tool-loop turns are separate calls)
package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

// Capturer writes one directory per verb dispatch. Safe for the orchestrator's
// single-dispatch-at-a-time loop; RecordCall additionally takes a lock so a
// stray concurrent caller can't corrupt the counter.
type Capturer struct {
	root string

	mu  sync.Mutex
	seq int
	cur *dispatch // nil between dispatches
}

type dispatch struct {
	dir     string
	callSeq int
}

// New creates (or reuses) the capture root directory.
func New(dir string) (*Capturer, error) {
	if dir == "" {
		return nil, fmt.Errorf("capture dir is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("capture: mkdir %s: %w", dir, err)
	}
	return &Capturer{root: dir}, nil
}

// meta is the JSON written to meta.json — everything the harness needs to
// rebuild the *db.HandoffJob and locate the folder/DB.
type meta struct {
	Seq             int       `json:"seq"`
	CapturedAt      time.Time `json:"captured_at"`
	Verb            string    `json:"verb"`
	JobID           int64     `json:"job_id"`
	ProjectID       int64     `json:"project_id"`
	BeadID          *int64    `json:"bead_id"`
	RefinementCycle *int64    `json:"refinement_cycle_id"`
	JobStatus       string    `json:"job_status"`
	JobCreatedAt    time.Time `json:"job_created_at"`
	JobUpdatedAt    time.Time `json:"job_updated_at"`
	ProjectLabel    string    `json:"project_label"`
	FolderPath      string    `json:"folder_path"`
	DesignDocPath   string    `json:"design_doc_path"`
	Language        string    `json:"language"`
}

// BeginDispatch snapshots the DB + folder + job for one verb dispatch and
// returns a context carrying the per-call recorder plus a finalize func the
// caller must defer. Any error is returned without side effects on the
// Capturer's state — the caller should log and proceed uninstrumented rather
// than abort the run.
func (c *Capturer) BeginDispatch(ctx context.Context, d *db.DB, project *db.Project, job *db.HandoffJob) (context.Context, func(), error) {
	c.mu.Lock()
	c.seq++
	seq := c.seq
	c.mu.Unlock()

	var beadID *int64
	if job.BeadID.Valid {
		beadID = &job.BeadID.Int64
	}
	var cycle *int64
	if job.RefinementCycleID.Valid {
		cycle = &job.RefinementCycleID.Int64
	}

	name := fmt.Sprintf("%03d-%s-p%d", seq, job.Verb, job.ProjectID)
	if beadID != nil {
		name += fmt.Sprintf("-b%d", *beadID)
	}
	if cycle != nil {
		name += fmt.Sprintf("-c%d", *cycle)
	}
	dir := filepath.Join(c.root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ctx, func() {}, fmt.Errorf("capture: mkdir dispatch dir: %w", err)
	}

	// Whole-DB copy. VACUUM INTO produces a clean, defragmented standalone file
	// and requires the target not to exist (dir is fresh).
	if _, err := d.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(dir, "db.sqlite")); err != nil {
		return ctx, func() {}, fmt.Errorf("capture: VACUUM INTO: %w", err)
	}

	if err := copyTree(project.FolderPath, filepath.Join(dir, "folder"), map[string]bool{"traces": true}); err != nil {
		return ctx, func() {}, fmt.Errorf("capture: copy folder: %w", err)
	}

	m := meta{
		Seq: seq, CapturedAt: time.Now().UTC(), Verb: job.Verb,
		JobID: job.ID, ProjectID: job.ProjectID, BeadID: beadID, RefinementCycle: cycle,
		JobStatus: job.Status, JobCreatedAt: job.CreatedAt, JobUpdatedAt: job.UpdatedAt,
		ProjectLabel: project.Label, FolderPath: project.FolderPath,
		DesignDocPath: project.DesignDocPath, Language: project.Language,
	}
	if err := writeJSON(filepath.Join(dir, "meta.json"), m); err != nil {
		return ctx, func() {}, fmt.Errorf("capture: write meta: %w", err)
	}

	c.mu.Lock()
	c.cur = &dispatch{dir: dir}
	c.mu.Unlock()

	finalize := func() {
		c.mu.Lock()
		c.cur = nil
		c.mu.Unlock()
	}
	return ollama.WithRecorder(ctx, c), finalize, nil
}

// RecordCall implements ollama.CallRecorder. One JSON file per model call,
// numbered within the current dispatch. Calls outside a dispatch window (no
// current dispatch) are dropped.
func (c *Capturer) RecordCall(rec ollama.CallRecord) {
	c.mu.Lock()
	cur := c.cur
	if cur == nil {
		c.mu.Unlock()
		return
	}
	cur.callSeq++
	n := cur.callSeq
	dir := cur.dir
	c.mu.Unlock()

	rec.Turn = n
	path := filepath.Join(dir, fmt.Sprintf("call-%03d.json", n))
	if err := writeJSON(path, rec); err != nil {
		// Best-effort: a lost call record shouldn't take down a 9-hour run.
		fmt.Fprintf(os.Stderr, "capture: write %s: %v\n", path, err)
	}
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// copyTree copies src into dst recursively, skipping any top-level directory
// name in skipTop (matched only at the root, so a nested "traces" dir would
// still copy — none exists in a project folder).
func copyTree(src, dst string, skipTop map[string]bool) error {
	return filepath.WalkDir(src, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if e.IsDir() {
			if skipTop[rel] {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !e.Type().IsRegular() {
			return nil // skip symlinks/sockets/etc.
		}
		return copyFile(path, filepath.Join(dst, rel))
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
