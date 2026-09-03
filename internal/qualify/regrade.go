package qualify

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"ratchet/internal/ollama"
	"ratchet/internal/verbs"
)

// runRegrade re-runs Validate + the graders + Summarize over the persisted
// result.json files under a matrix section dir — no model calls. Use it after a
// grader / labelling fix so a corpus of expensive replays can be re-scored for
// free. Rewrites table.txt + summary.tsv in place (keeps the originals as
// table.pre-regrade.txt).
func runRegrade(args []string) {
	fs := flag.NewFlagSet("qualify-model regrade", flag.ExitOnError)
	sectionDir := fs.String("dir", "", "matrix section out dir (has work/**/result.json) (required)")
	corpusDir := fs.String("corpus", "", "capture corpus dir (required — WRITE grader needs bead specs)")
	reference := fs.String("reference", "", "final-state project DB (default <corpus>/corpus.db)")
	mutants := fs.String("mutants", "internal/qualify/testdata/qual-mutants", "WRITE mutant-fixtures root")
	_ = fs.Parse(args)
	if *sectionDir == "" || *corpusDir == "" {
		slog.Error("regrade: --dir and --corpus are required")
		os.Exit(1)
	}
	if *reference == "" {
		*reference = filepath.Join(*corpusDir, "corpus.db")
	}

	corp, err := LoadCorpus(*corpusDir)
	if err != nil {
		slog.Error("regrade: load corpus", "error", err)
		os.Exit(1)
	}
	byName := map[string]Case{}
	for _, c := range corp.Cases {
		byName[c.Name] = c
	}
	ref, err := OpenReferenceDB(*reference)
	if err != nil {
		slog.Error("regrade: open reference", "error", err)
		os.Exit(1)
	}
	defer ref.Close()

	var recs []resultRecord
	var dirs []string
	seen := map[string]bool{}
	_ = filepath.WalkDir(*sectionDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		var rr resultRecord
		switch d.Name() {
		case "result.json":
			b, rerr := os.ReadFile(p)
			if rerr != nil || json.Unmarshal(b, &rr) != nil {
				return nil
			}
		case "replay.txt":
			if seen[filepath.Dir(p)] {
				return nil // result.json already covered this dir
			}
			parsed, perr := parseReplayTxt(p, byName)
			if perr != nil {
				slog.Warn("regrade: parse replay.txt", "path", p, "error", perr)
				return nil
			}
			rr = parsed
		default:
			return nil
		}
		recs = append(recs, rr)
		dirs = append(dirs, filepath.Dir(p))
		seen[filepath.Dir(p)] = true
		return nil
	})
	if len(recs) == 0 {
		slog.Error("regrade: no result.json / replay.txt under dir", "dir", *sectionDir)
		os.Exit(1)
	}

	ctx := context.Background()
	verb := recs[0].Verb
	handler := verbs.All("")[verb]

	perModel := map[string][]ReplayResult{}
	perModelG := map[string][]RunGrade{}
	var order []string

	for i, rr := range recs {
		c, ok := byName[rr.CaseName]
		if !ok {
			slog.Warn("regrade: case not in corpus", "case", rr.CaseName)
			continue
		}
		res := ReplayResult{
			Case: rr.Case, Model: rr.Model, Run: rr.Run, Ordinal: rr.Ordinal,
			RunDir: dirs[i], RawOutput: rr.Raw, Wall: time.Duration(rr.WallMS) * time.Millisecond,
			ValidationResult: rr.Validation, Fidelity: rr.Fidelity,
		}
		if rr.RunErr != "" {
			res.RunErr = errors.New(rr.RunErr)
		}
		for _, cl := range rr.Calls {
			res.Calls = append(res.Calls, ollama.CallRecord{
				DoneReason: cl.DoneReason,
				Stats: ollama.Stats{
					EvalCount: cl.EvalCount, EvalDuration: cl.EvalDuration,
					PromptEvalCount: cl.PromptEvalCount, PromptEvalDuration: cl.PromptEvalDuration,
					TotalDuration: cl.TotalDuration, LoadDuration: cl.LoadDuration,
				},
				Response:   ollama.Message{Content: pad(cl.ContentLen), Thinking: pad(cl.ThinkingLen), ToolCalls: make([]ollama.ToolCall, cl.ToolCalls)},
				WallMillis: cl.WallMS,
			})
		}
		if res.RunErr == nil && handler != nil {
			res.ValidationResult, res.Parsed = handler.Validate(rr.Raw)
		}
		g := Grade(ctx, verb, ref, absPath(*mutants), c, res)
		if _, seen := perModel[rr.Model]; !seen {
			order = append(order, rr.Model)
		}
		perModel[rr.Model] = append(perModel[rr.Model], res)
		perModelG[rr.Model] = append(perModelG[rr.Model], g)
	}

	var sums []ModelSummary
	for _, m := range order {
		sums = append(sums, Summarize(verb, m, perModel[m], perModelG[m]))
	}
	sort.Slice(sums, func(i, j int) bool { return sums[i].Model < sums[j].Model })

	table := Table(verb+"  [regraded]", sums)
	fmt.Println(table)
	if orig, e := os.ReadFile(filepath.Join(*sectionDir, "table.txt")); e == nil {
		_ = os.WriteFile(filepath.Join(*sectionDir, "table.pre-regrade.txt"), orig, 0o644)
	}
	_ = os.WriteFile(filepath.Join(*sectionDir, "table.txt"), []byte(table), 0o644)
	tsv := []string{TSVHeader()}
	for _, s := range sums {
		tsv = append(tsv, s.TSVRow())
	}
	writeLines(filepath.Join(*sectionDir, "summary.tsv"), tsv)
	fmt.Printf("regraded %d results in %s\n", len(recs), *sectionDir)
}

// parseReplayTxt reconstructs a resultRecord from a writeTrace()-format
// replay.txt (the fallback for replays captured before result.json existed).
// The case dir name (…/work/<Case.Name>/<model>/run-N/replay.txt) keys the
// corpus lookup for bead/cycle/verb.
func parseReplayTxt(path string, byName map[string]Case) (resultRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return resultRecord{}, err
	}
	text := string(b)

	// …/work/<caseName>/<model>/run-N/replay.txt
	rel := path
	if i := strings.Index(path, string(os.PathSeparator)+"work"+string(os.PathSeparator)); i >= 0 {
		rel = path[i+len("/work/"):]
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) < 4 {
		return resultRecord{}, fmt.Errorf("unexpected path layout: %s", rel)
	}
	caseName := parts[0]
	c, ok := byName[caseName]
	if !ok {
		return resultRecord{}, fmt.Errorf("case %q not in corpus", caseName)
	}

	rr := resultRecord{
		CaseName: caseName, Verb: c.Meta.Verb, Case: c.ID(),
		BeadID: c.Meta.BeadID, Cycle: c.Meta.RefinementCycle, Ordinal: -1,
	}
	if m := reField.FindStringSubmatch(text); m != nil {
		rr.Model = m[1]
		rr.Run = atoiSafe(m[2])
	}
	if m := reWall.FindStringSubmatch(text); m != nil {
		if d, e := time.ParseDuration(m[1]); e == nil {
			rr.WallMS = d.Milliseconds()
		}
	}
	if m := reValidation.FindStringSubmatch(text); m != nil {
		rr.Validation = m[1]
	}
	if m := reRunErr.FindStringSubmatch(text); m != nil {
		rr.RunErr = strings.TrimSpace(m[1])
	}
	rr.Fidelity = FidelityResult{Checked: true, Match: strings.Contains(text, "fidelity_match=true")}

	for _, m := range reCall.FindAllStringSubmatch(text, -1) {
		cl := callLite{
			DoneReason: m[1], EvalCount: int64(atoiSafe(m[2])),
			ThinkingLen: atoiSafe(m[3]), ContentLen: atoiSafe(m[4]), ToolCalls: atoiSafe(m[5]),
		}
		// new trace format also carries per-call durations
		if len(m) > 8 && m[6] != "" {
			cl.TotalDuration = int64(atoiSafe(m[6]))
			cl.PromptEvalDuration = int64(atoiSafe(m[7]))
			cl.EvalDuration = int64(atoiSafe(m[8]))
		}
		rr.Calls = append(rr.Calls, cl)
	}
	// Distribute the trace's aggregate gen-token count + tok/s across the calls
	// so Summarize's per-call sums land on the right totals (replay.txt has no
	// per-call eval_duration).
	if m := reAgg.FindStringSubmatch(text); m != nil && len(rr.Calls) > 0 {
		genTok, tokPerSec := int64(atoiSafe(m[1])), parseFloat(m[2])
		var ns int64
		if tokPerSec > 0 {
			ns = int64(float64(genTok) / tokPerSec * 1e9)
		}
		per := genTok / int64(len(rr.Calls))
		perNS := ns / int64(len(rr.Calls))
		for i := range rr.Calls {
			rr.Calls[i].EvalCount = per
			rr.Calls[i].EvalDuration = perNS
		}
	}
	if i := strings.Index(text, "=== raw output ===\n"); i >= 0 {
		rr.Raw = strings.TrimRight(text[i+len("=== raw output ===\n"):], "\n")
	}
	return rr, nil
}

var (
	reField      = regexp.MustCompile(`case=\S+ model=(\S+) run=(\d+)`)
	reWall       = regexp.MustCompile(`wall=(\S+) validation=`)
	reValidation = regexp.MustCompile(`validation="([^"]*)"`)
	reRunErr     = regexp.MustCompile(`RUN ERROR: (.+)`)
	reCall       = regexp.MustCompile(`--- call \d+: done="([^"]*)" eval=(\d+) thinking=(\d+) content=(\d+) tools=(\d+)(?: total_dur=(\d+) prompt_eval_dur=(\d+) eval_dur=(\d+))?`)
	reAgg        = regexp.MustCompile(`calls=\d+ (?:gen_tok|answer_tok)=(\d+) prompt_tok=\d+ (?:tok/s|answer_tok/s)=([\d.]+)`)
)

func parseFloat(s string) float64 {
	var whole, frac float64
	var div float64 = 1
	dot := false
	for _, r := range s {
		switch {
		case r == '.':
			dot = true
		case r >= '0' && r <= '9':
			if dot {
				div *= 10
				frac += float64(r-'0') / div
			} else {
				whole = whole*10 + float64(r-'0')
			}
		default:
			return whole + frac
		}
	}
	return whole + frac
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// pad returns a string of n 'x' bytes — the graders only ever check len() of
// Content/Thinking, never the bytes.
func pad(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
