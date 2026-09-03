package qualify

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
	"ratchet/internal/verbs"
)

// recorder collects the CallRecord for every model call the replayed Run()
// makes. It satisfies ollama.CallRecorder.
type recorder struct {
	mu    sync.Mutex
	calls []ollama.CallRecord
	// stopAfter, when > 0, cancels the run context once that many calls have
	// been recorded — used by fidelity-only mode to avoid running a full
	// multi-minute tool loop just to compare the first prompt.
	stopAfter int
	cancel    context.CancelFunc
}

func (r *recorder) RecordCall(rec ollama.CallRecord) {
	r.mu.Lock()
	r.calls = append(r.calls, rec)
	n := len(r.calls)
	r.mu.Unlock()
	if r.stopAfter > 0 && n >= r.stopAfter && r.cancel != nil {
		r.cancel()
	}
}

func (r *recorder) snapshot() []ollama.CallRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ollama.CallRecord, len(r.calls))
	copy(out, r.calls)
	return out
}

// ReplayResult is the raw outcome of one Run() replay, before per-verb grading.
type ReplayResult struct {
	Case  string
	Model string
	Run   int
	// Ordinal is this case's 0-based position among the same-bead cases of the
	// same verb, in seq order — used to line an ADJUDICATE replay up with the
	// right baseline adjudications row. -1 when not applicable.
	Ordinal int
	// RunDir is the per-run scratch directory (holds folder/, run.db, replay.txt).
	RunDir string

	// RunErr is a Run() infrastructure error (network/DB/precondition) — not a
	// malformed model output. Non-nil RunErr means the rest is zero.
	RunErr error

	RawOutput        string
	ValidationResult string // "valid" or "malformed: ..."
	Parsed           any    // typed per verb; nil unless valid

	Wall     time.Duration
	Calls    []ollama.CallRecord
	Fidelity FidelityResult
}

// FidelityResult reports whether the freshly-built prompt of the replay's first
// model call matches the captured one (modulo model name).
type FidelityResult struct {
	Checked bool
	Match   bool
	Detail  string
}

// firstTryValid reports whether Validate accepted the output with no in-verb
// retry (single accepted final call). For tool-loop verbs "no retry" means the
// loop terminated on its own rather than exhausting its turn budget.
func (r ReplayResult) FirstTryValid() bool {
	return r.RunErr == nil && r.ValidationResult == "valid"
}

// deadTurns counts turns with done_reason=="length" and no content and no tool
// call — the ADJUDICATE spiral signature.
func (r ReplayResult) deadTurns() int {
	n := 0
	for _, c := range r.Calls {
		if c.DoneReason != "length" {
			continue
		}
		if len(c.Response.ToolCalls) == 0 && c.Response.Content == "" {
			n++
		}
	}
	return n
}

func (r ReplayResult) genTokens() int64 {
	var n int64
	for _, c := range r.Calls {
		n += c.Stats.EvalCount
	}
	return n
}

func (r ReplayResult) promptTokens() int64 {
	var n int64
	for _, c := range r.Calls {
		n += c.Stats.PromptEvalCount
	}
	return n
}

// answerTokPerSec is *answer* tokens over answer-generation nanoseconds. Ollama's
// eval_count/eval_duration cover only the final content, NOT a reasoning model's
// thinking stream (which is generated at the same rate but reported nowhere in
// the token counters — it only shows up in total_duration). So this is
// "throughput of the visible answer", not the model's true generation rate.
func (r ReplayResult) answerTokPerSec() float64 {
	var tok, ns int64
	for _, c := range r.Calls {
		tok += c.Stats.EvalCount
		ns += c.Stats.EvalDuration
	}
	if ns == 0 {
		return 0
	}
	return float64(tok) / (float64(ns) / 1e9)
}

// thinkingSecs estimates total time spent generating the (uncounted) reasoning
// stream: total_duration − prompt_eval_duration − eval_duration, summed across
// calls. Zero when total_duration wasn't captured (pre-fix result.json / older
// replay.txt) — callers should fall back to the thinking char count.
func (r ReplayResult) thinkingSecs() float64 {
	var ns int64
	for _, c := range r.Calls {
		if c.Stats.TotalDuration <= 0 {
			continue
		}
		d := c.Stats.TotalDuration - c.Stats.PromptEvalDuration - c.Stats.EvalDuration - c.Stats.LoadDuration
		if d > 0 {
			ns += d
		}
	}
	return float64(ns) / 1e9
}

// thinkingChars is the fallback signal when total_duration is absent.
func (r ReplayResult) thinkingChars() int {
	n := 0
	for _, c := range r.Calls {
		n += len(c.Response.Thinking)
	}
	return n
}

// Replayer holds the fixed configuration for a batch of replays.
type Replayer struct {
	OllamaURL string
	WorkRoot  string // per-run scratch (dbs + folders) live here

	// StopAfterCalls > 0 aborts each Run() once that many model calls have been
	// made (fidelity-only mode). The resulting RunErr is expected and ignored
	// by the caller.
	StopAfterCalls int

	// Override, when set, is installed on every replay's context so the ollama
	// client applies it on top of the verb's hard-coded per-call options
	// (grammar-constraint / think sweep). Zero value = no override installed.
	Override    ollama.ChatOverride
	HasOverride bool

	// PerRunCeiling hard-caps one Run() (the harness uses the unbounded ollama
	// client, so a genuinely stuck reasoning stream would otherwise hang the
	// batch). A run that hits this is recorded with RunErr = context deadline.
	// Zero => 30m.
	PerRunCeiling time.Duration

	oc     *ollama.Client
	warmed map[string]bool
	warmMu sync.Mutex
}

func NewReplayer(ollamaURL, workRoot string) *Replayer {
	return &Replayer{
		OllamaURL: ollamaURL,
		WorkRoot:  workRoot,
		oc:        ollama.NewUnbounded(ollamaURL),
		warmed:    map[string]bool{},
	}
}

func (rp *Replayer) warmup(ctx context.Context, model string) error {
	rp.warmMu.Lock()
	done := rp.warmed[model]
	rp.warmMu.Unlock()
	if done {
		return nil
	}
	if err := rp.oc.Warmup(ctx, model); err != nil {
		return err
	}
	rp.warmMu.Lock()
	rp.warmed[model] = true
	rp.warmMu.Unlock()
	return nil
}

// Unload nudges a model out of the remote VRAM so the next candidate loads clean.
func (rp *Replayer) Unload(model string) { unloadModel(rp.OllamaURL, model) }

// Replay runs handler.Run() once for (case, model, run). It:
//  1. copies the case's db.sqlite to a per-run file and its folder/ to a per-run dir,
//  2. patches verb_model_assignments(project, verb, model) and projects.folder_path,
//  3. reconstructs the *db.HandoffJob from meta.json's job_id,
//  4. installs a recorder on ctx and calls Run() then Validate(),
//  5. never calls Commit; discards the per-run DB.
func (rp *Replayer) Replay(ctx context.Context, c Case, model string, run int) ReplayResult {
	res := ReplayResult{Case: c.ID(), Model: model, Run: run, Ordinal: -1}

	runDir := filepath.Join(rp.WorkRoot, sanitize(c.Name), sanitize(model), fmt.Sprintf("run-%d", run))
	res.RunDir = runDir
	if err := os.RemoveAll(runDir); err != nil {
		res.RunErr = fmt.Errorf("clean run dir: %w", err)
		return res
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		res.RunErr = err
		return res
	}

	runDBPath := filepath.Join(runDir, "run.db")
	if err := copyFile(filepath.Join(c.Dir, "db.sqlite"), runDBPath); err != nil {
		res.RunErr = fmt.Errorf("copy db.sqlite: %w", err)
		return res
	}
	_ = os.Chmod(runDBPath, 0o644)

	folderDir := filepath.Join(runDir, "folder")
	if err := copyTree(filepath.Join(c.Dir, "folder"), folderDir); err != nil {
		res.RunErr = fmt.Errorf("copy folder: %w", err)
		return res
	}

	d, err := db.Open(runDBPath)
	if err != nil {
		res.RunErr = fmt.Errorf("open run db: %w", err)
		return res
	}
	defer d.Close()

	m := c.Meta
	if _, err := d.ExecContext(ctx,
		`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (?, ?, ?)
		 ON CONFLICT(project_id, verb) DO UPDATE SET model = excluded.model`,
		m.ProjectID, m.Verb, model); err != nil {
		res.RunErr = fmt.Errorf("patch verb_model_assignments: %w", err)
		return res
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET folder_path = ? WHERE id = ?`, folderDir, m.ProjectID); err != nil {
		res.RunErr = fmt.Errorf("patch folder_path: %w", err)
		return res
	}

	job, err := loadJob(ctx, d, m.JobID)
	if err != nil {
		res.RunErr = err
		return res
	}

	handler, ok := verbs.All(rp.OllamaURL)[m.Verb]
	if !ok {
		res.RunErr = fmt.Errorf("no handler for verb %s", m.Verb)
		return res
	}

	if err := rp.warmup(ctx, model); err != nil {
		res.RunErr = fmt.Errorf("warmup %s: %w", model, err)
		return res
	}

	ceiling := rp.PerRunCeiling
	if ceiling <= 0 {
		ceiling = 30 * time.Minute
	}
	ctx, ceilCancel := context.WithTimeout(ctx, ceiling)
	defer ceilCancel()

	rec := &recorder{stopAfter: rp.StopAfterCalls}
	runCtx := ctx
	if rp.StopAfterCalls > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithCancel(ctx)
		defer cancel()
		rec.cancel = cancel
	}
	if rp.HasOverride {
		runCtx = ollama.WithChatOverride(runCtx, rp.Override)
	}
	runCtx = ollama.WithRecorder(runCtx, rec)

	start := time.Now()
	raw, runErr := handler.Run(runCtx, d, rp.oc, job)
	res.Wall = time.Since(start)
	res.Calls = rec.snapshot()

	// Fidelity is checkable as soon as the first call is recorded, even if the
	// run was deliberately aborted (StopAfterCalls) or errored later.
	if len(res.Calls) > 0 {
		res.Fidelity = checkFidelity(c, res.Calls)
	}

	if runErr != nil {
		if rp.StopAfterCalls > 0 && len(res.Calls) >= rp.StopAfterCalls {
			return res // expected abort; RunErr left nil
		}
		res.RunErr = runErr
		return res
	}
	res.RawOutput = raw
	res.ValidationResult, res.Parsed = handler.Validate(raw)

	// Persist the trace + a machine-readable result for post-hoc re-grading
	// (so a labelling fix doesn't require re-running the model).
	writeTrace(runDir, res)
	writeResultRecord(runDir, c, res)
	return res
}

// resultRecord is the persisted, re-gradable form of a ReplayResult: enough to
// re-run Validate + the graders without the model. The per-run folder/ +
// grade/ dirs stay on disk alongside it for the WRITE grader.
type resultRecord struct {
	Case       string         `json:"case"`
	CaseName   string         `json:"case_name"`
	Verb       string         `json:"verb"`
	Model      string         `json:"model"`
	Run        int            `json:"run"`
	Ordinal    int            `json:"ordinal"`
	BeadID     *int64         `json:"bead_id"`
	Cycle      *int64         `json:"cycle_id"`
	WallMS     int64          `json:"wall_ms"`
	RunErr     string         `json:"run_err,omitempty"`
	Raw        string         `json:"raw_output"`
	Validation string         `json:"validation_result"`
	Fidelity   FidelityResult `json:"fidelity"`
	Calls      []callLite     `json:"calls"`
}

type callLite struct {
	DoneReason         string `json:"done_reason"`
	EvalCount          int64  `json:"eval_count"`
	EvalDuration       int64  `json:"eval_duration"`
	PromptEvalCount    int64  `json:"prompt_eval_count"`
	PromptEvalDuration int64  `json:"prompt_eval_duration"`
	TotalDuration      int64  `json:"total_duration"`
	LoadDuration       int64  `json:"load_duration"`
	ContentLen         int    `json:"content_len"`
	ThinkingLen        int    `json:"thinking_len"`
	ToolCalls          int    `json:"tool_calls"`
	WallMS             int64  `json:"wall_ms"`
}

func writeResultRecord(runDir string, c Case, res ReplayResult) {
	rr := resultRecord{
		Case: res.Case, CaseName: c.Name, Verb: c.Meta.Verb, Model: res.Model,
		Run: res.Run, Ordinal: res.Ordinal, BeadID: c.Meta.BeadID, Cycle: c.Meta.RefinementCycle,
		WallMS: res.Wall.Milliseconds(), Raw: res.RawOutput,
		Validation: res.ValidationResult, Fidelity: res.Fidelity,
	}
	if res.RunErr != nil {
		rr.RunErr = res.RunErr.Error()
	}
	for _, cl := range res.Calls {
		rr.Calls = append(rr.Calls, callLite{
			DoneReason: cl.DoneReason, EvalCount: cl.Stats.EvalCount,
			EvalDuration: cl.Stats.EvalDuration, PromptEvalCount: cl.Stats.PromptEvalCount,
			PromptEvalDuration: cl.Stats.PromptEvalDuration, TotalDuration: cl.Stats.TotalDuration,
			LoadDuration: cl.Stats.LoadDuration,
			ContentLen:   len(cl.Response.Content), ThinkingLen: len(cl.Response.Thinking),
			ToolCalls: len(cl.Response.ToolCalls), WallMS: cl.WallMillis,
		})
	}
	b, _ := json.MarshalIndent(rr, "", "  ")
	_ = os.WriteFile(filepath.Join(runDir, "result.json"), b, 0o644)
}

func loadJob(ctx context.Context, d *db.DB, jobID int64) (*db.HandoffJob, error) {
	j := &db.HandoffJob{}
	var createdAt, updatedAt string
	err := d.QueryRowContext(ctx,
		`SELECT id, project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at
		 FROM handoff_jobs WHERE id = ?`, jobID).
		Scan(&j.ID, &j.ProjectID, &j.Verb, &j.BeadID, &j.Status, &j.RefinementCycleID, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("job %d not in captured db.sqlite", jobID)
	}
	if err != nil {
		return nil, fmt.Errorf("load job %d: %w", jobID, err)
	}
	j.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	j.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	// The capture snapshots the job as 'running'; Run() doesn't care about
	// status, but normalize so nothing downstream trips on it.
	j.Status = "running"
	return j, nil
}

// checkFidelity compares the replay's first model call's messages against the
// captured call-001.json (modulo the model name, which lives outside messages).
func checkFidelity(c Case, replayCalls []ollama.CallRecord) FidelityResult {
	if len(replayCalls) == 0 {
		return FidelityResult{Checked: false, Detail: "replay made no model calls"}
	}
	cc := c
	if err := cc.LoadCalls(); err != nil {
		return FidelityResult{Checked: false, Detail: "load captured calls: " + err.Error()}
	}
	want := cc.Calls[0].Messages
	got := replayCalls[0].Messages
	if len(want) != len(got) {
		return FidelityResult{Checked: true, Match: false,
			Detail: fmt.Sprintf("message count: captured %d, replay %d", len(want), len(got))}
	}
	for i := range want {
		if want[i].Role != got[i].Role {
			return FidelityResult{Checked: true, Match: false,
				Detail: fmt.Sprintf("message %d role: captured %q, replay %q", i, want[i].Role, got[i].Role)}
		}
		if want[i].Content != got[i].Content {
			return FidelityResult{Checked: true, Match: false,
				Detail: fmt.Sprintf("message %d content differs (captured %d bytes, replay %d bytes); first diff at %s",
					i, len(want[i].Content), len(got[i].Content), firstDiff(want[i].Content, got[i].Content))}
		}
	}
	return FidelityResult{Checked: true, Match: true}
}

func firstDiff(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			lo := i - 40
			if lo < 0 {
				lo = 0
			}
			hi := i + 40
			if hi > n {
				hi = n
			}
			return fmt.Sprintf("byte %d\n  captured: ...%q...\n  replay:   ...%q...", i, a[lo:hi], b[lo:hi])
		}
	}
	return fmt.Sprintf("byte %d (one is a prefix of the other)", n)
}

func writeTrace(runDir string, res ReplayResult) {
	f, err := os.Create(filepath.Join(runDir, "replay.txt"))
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "case=%s model=%s run=%d\n", res.Case, res.Model, res.Run)
	fmt.Fprintf(f, "wall=%s validation=%q fidelity_match=%t %s\n",
		res.Wall.Round(time.Millisecond), res.ValidationResult, res.Fidelity.Match, res.Fidelity.Detail)
	fmt.Fprintf(f, "calls=%d answer_tok=%d prompt_tok=%d answer_tok/s=%.1f thinking_s=%.0f thinking_chars=%d dead_turns=%d\n",
		len(res.Calls), res.genTokens(), res.promptTokens(), res.answerTokPerSec(),
		res.thinkingSecs(), res.thinkingChars(), res.deadTurns())
	for i, c := range res.Calls {
		think := c.Stats.TotalDuration - c.Stats.PromptEvalDuration - c.Stats.EvalDuration - c.Stats.LoadDuration
		fmt.Fprintf(f, "\n--- call %d: done=%q eval=%d thinking=%d content=%d tools=%d total_dur=%d prompt_eval_dur=%d eval_dur=%d think_s=%.0f ---\n",
			i+1, c.DoneReason, c.Stats.EvalCount, len(c.Response.Thinking), len(c.Response.Content), len(c.Response.ToolCalls),
			c.Stats.TotalDuration, c.Stats.PromptEvalDuration, c.Stats.EvalDuration, float64(think)/1e9)
	}
	fmt.Fprintf(f, "\n=== raw output ===\n%s\n", res.RawOutput)
}
