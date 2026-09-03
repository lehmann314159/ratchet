// Package qualify implements `ratchet qualify-model`: the REFINE_TESTS +
// ADJUDICATE verb bakeoff harness. It replays a captured verb dispatch (the
// output of `ratchet start --capture-verb-io`) against a candidate model by
// calling the verb's real Run(), never Commit(), against a per-run copy of the
// captured whole-DB snapshot.
//
// Route (differs from the original plan sketch, which predates the capture
// layer landing on whole-DB snapshots):
//   - Each captured dispatch dir holds db.sqlite — a VACUUM INTO copy of the
//     entire DB at dispatch time. The harness copies that file per run, patches
//     the verb_model_assignments row + projects.folder_path, loads the
//     *db.HandoffJob by meta.json's job_id, and calls handler.Run. No
//     fresh-project reconstruction, no rows.sql.
//   - Ollama timing/token stats are read from the ollama.CallRecorder hook
//     (already parsed into ollama.CallRecord by the client) — no client
//     return-path change. The harness installs its own recorder on the replay
//     context and grades from the collected records plus overall wall-clock.
package qualify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ratchet/internal/ollama"
)

// Meta mirrors the JSON written by internal/capture to meta.json.
type Meta struct {
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

// Case is one captured verb dispatch: a directory with meta.json, db.sqlite,
// folder/, and call-NNN.json records.
type Case struct {
	// Name is the dispatch dir basename, e.g. "007-REFINE_TESTS_CRITIQUE-p48-b313-c1".
	Name string
	Dir  string
	Meta Meta
	// Calls are the captured model calls in order (call-001.json first). Present
	// only after LoadCalls.
	Calls []ollama.CallRecord
}

// ID is the short case identifier used on the command line: "b313-c1" for a
// refinement case, "b313" for a bead-scoped case, or the dispatch seq for a
// project-scoped verb.
func (c Case) ID() string {
	if c.Meta.BeadID != nil {
		s := fmt.Sprintf("b%d", *c.Meta.BeadID)
		if c.Meta.RefinementCycle != nil {
			s += fmt.Sprintf("-c%d", *c.Meta.RefinementCycle)
		}
		return s
	}
	return strconv.Itoa(c.Meta.Seq)
}

// LoadCalls reads every call-NNN.json in the case dir into c.Calls, ordered by
// file name (which is the call sequence).
func (c *Case) LoadCalls() error {
	matches, err := filepath.Glob(filepath.Join(c.Dir, "call-*.json"))
	if err != nil {
		return err
	}
	sort.Strings(matches)
	c.Calls = c.Calls[:0]
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			return fmt.Errorf("read %s: %w", m, err)
		}
		var rec ollama.CallRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			return fmt.Errorf("parse %s: %w", m, err)
		}
		c.Calls = append(c.Calls, rec)
	}
	if len(c.Calls) == 0 {
		return fmt.Errorf("case %s: no call-NNN.json records", c.Name)
	}
	return nil
}

// Corpus is a --capture-verb-io output directory.
type Corpus struct {
	Root  string
	Cases []Case
}

// LoadCorpus discovers every dispatch dir under root that has a readable
// meta.json + db.sqlite.
func LoadCorpus(root string) (*Corpus, error) {
	verbIO := root
	if fi, err := os.Stat(filepath.Join(root, "verb-io")); err == nil && fi.IsDir() {
		verbIO = filepath.Join(root, "verb-io")
	}
	entries, err := os.ReadDir(verbIO)
	if err != nil {
		return nil, fmt.Errorf("read corpus %s: %w", verbIO, err)
	}
	c := &Corpus{Root: verbIO}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(verbIO, e.Name())
		mb, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			continue // not a dispatch dir
		}
		var m Meta
		if err := json.Unmarshal(mb, &m); err != nil {
			return nil, fmt.Errorf("%s/meta.json: %w", e.Name(), err)
		}
		if _, err := os.Stat(filepath.Join(dir, "db.sqlite")); err != nil {
			return nil, fmt.Errorf("%s: missing db.sqlite", e.Name())
		}
		c.Cases = append(c.Cases, Case{Name: e.Name(), Dir: dir, Meta: m})
	}
	sort.Slice(c.Cases, func(i, j int) bool { return c.Cases[i].Meta.Seq < c.Cases[j].Meta.Seq })
	return c, nil
}

// Select returns the cases for verb whose ID() is in ids. ids=="all" (or empty)
// returns every case for that verb.
func (c *Corpus) Select(verb string, ids []string) ([]Case, error) {
	want := map[string]bool{}
	all := len(ids) == 0
	for _, id := range ids {
		if id == "all" {
			all = true
		}
		want[id] = true
	}
	var out []Case
	seen := map[string]bool{}
	for _, cs := range c.Cases {
		if cs.Meta.Verb != verb {
			continue
		}
		if all || want[cs.ID()] {
			out = append(out, cs)
			seen[cs.ID()] = true
		}
	}
	if !all {
		var missing []string
		for _, id := range ids {
			if id != "all" && !seen[id] {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("verb %s: no such case(s): %s", verb, strings.Join(missing, ", "))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("verb %s: no cases in corpus", verb)
	}
	return out, nil
}
