package qualify

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ratchet/internal/ollama"
)

// RunQualifyModelMain is the entry point for `ratchet qualify-model`.
//
//	ratchet qualify-model \
//	  --corpus ~/Documents/ratchet-projects/qual-corpus-p48 \
//	  --verb REFINE_TESTS_CRITIQUE \
//	  --models qwen3:32b,qwen3.6:35b-a3b,mistral-small3.2:24b \
//	  --runs 3 --cases all \
//	  --out scratchpad/qual/CRITIQUE
//
// Sub-mode: `ratchet qualify-model scaffold-mutants --corpus ... --artifact ... --out ...`
// extracts the known-good impl files for every REFINE_TESTS_WRITE bead from the
// baseline artifact tarball into the mutant-fixtures tree.
func RunQualifyModelMain(args []string) {
	if len(args) > 0 && args[0] == "scaffold-mutants" {
		runScaffoldMutants(args[1:])
		return
	}
	if len(args) > 0 && args[0] == "regrade" {
		runRegrade(args[1:])
		return
	}

	fs := flag.NewFlagSet("qualify-model", flag.ExitOnError)
	corpus := fs.String("corpus", "", "capture-verb-io output dir (required)")
	reference := fs.String("reference", "", "final-state project DB for baseline verdicts (default <corpus>/corpus.db)")
	verb := fs.String("verb", "", "verb to qualify (required)")
	modelsArg := fs.String("models", "", "comma-separated candidate models (required)")
	runs := fs.Int("runs", 3, "replays per (model, case)")
	casesArg := fs.String("cases", "all", "comma-separated case IDs (b313-c1,...) or 'all'")
	ollamaURL := fs.String("ollama", "http://192.168.50.241:11434", "Ollama base URL")
	mutants := fs.String("mutants", "internal/qualify/testdata/qual-mutants", "WRITE mutant-fixtures root")
	outDir := fs.String("out", "", "report output dir (required)")
	workDir := fs.String("work", "", "per-run scratch dir (default <out>/work)")
	fidelityOnly := fs.Bool("fidelity-only", false, "replay each case once per model, assert prompt fidelity, skip grading")
	unload := fs.Bool("unload", true, "nudge each model out of remote VRAM when done")
	omitFormat := fs.Bool("omit-format", false, "drop the format grammar constraint on every model call (reasoning-model sweep)")
	thinkMode := fs.String("think", "", "override the think flag: '', 'on', or 'off'")
	_ = fs.Parse(args)

	if *corpus == "" || *verb == "" || *modelsArg == "" || *outDir == "" {
		slog.Error("qualify-model: --corpus, --verb, --models and --out are required")
		os.Exit(1)
	}
	if *reference == "" {
		*reference = filepath.Join(*corpus, "corpus.db")
	}
	if *workDir == "" {
		*workDir = filepath.Join(*outDir, "work")
	}
	models := splitCSV(*modelsArg)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		slog.Error("qualify-model: mkdir out", "error", err)
		os.Exit(1)
	}

	corp, err := LoadCorpus(*corpus)
	if err != nil {
		slog.Error("qualify-model: load corpus", "error", err)
		os.Exit(1)
	}
	cases, err := corp.Select(*verb, splitCSV(*casesArg))
	if err != nil {
		slog.Error("qualify-model: select cases", "error", err)
		os.Exit(1)
	}
	// Ordinal within same-bead cases of this verb (for ADJUDICATE baseline
	// alignment), and a display label that disambiguates when one bead has
	// several dispatches of the same verb (ADJUDICATE b314 -> b314.0, b314.1).
	beadCount := map[int64]int{}
	for _, c := range cases {
		if c.Meta.BeadID != nil {
			beadCount[*c.Meta.BeadID]++
		}
	}
	ord := map[int64]int{}
	ordinals := make([]int, len(cases))
	labels := make([]string, len(cases))
	for i, c := range cases {
		labels[i] = c.ID()
		if c.Meta.BeadID != nil {
			ordinals[i] = ord[*c.Meta.BeadID]
			if c.Meta.RefinementCycle == nil && beadCount[*c.Meta.BeadID] > 1 {
				labels[i] = fmt.Sprintf("%s.%d", c.ID(), ordinals[i])
			}
			ord[*c.Meta.BeadID]++
		} else {
			ordinals[i] = -1
		}
	}

	ref, err := OpenReferenceDB(*reference)
	if err != nil {
		slog.Error("qualify-model: open reference db", "error", err, "path", *reference)
		os.Exit(1)
	}
	defer ref.Close()

	slog.Info("qualify-model: start", "verb", *verb, "cases", len(cases), "models", models,
		"runs", *runs, "fidelity_only", *fidelityOnly)

	rp := NewReplayer(*ollamaURL, *workDir)
	cfg := "format=json think=default"
	if *omitFormat || *thinkMode != "" {
		var ov ollama.ChatOverride
		ov.ForceOmitFormat = *omitFormat
		switch *thinkMode {
		case "on":
			t := true
			ov.Think = &t
		case "off":
			t := false
			ov.Think = &t
		case "":
		default:
			slog.Error("qualify-model: --think must be '', 'on' or 'off'")
			os.Exit(1)
		}
		rp.Override, rp.HasOverride = ov, true
		fm := "json"
		if *omitFormat {
			fm = "omitted"
		}
		tm := "default"
		if *thinkMode != "" {
			tm = *thinkMode
		}
		cfg = fmt.Sprintf("format=%s think=%s", fm, tm)
	}
	_ = os.WriteFile(filepath.Join(*outDir, "config.txt"),
		[]byte(fmt.Sprintf("verb=%s\nmodels=%s\nruns=%d\ncases=%s\n%s\n", *verb, *modelsArg, *runs, *casesArg, cfg)), 0o644)
	ctx := context.Background()

	nRuns := *runs
	if *fidelityOnly {
		nRuns = 1
		rp.StopAfterCalls = 1
	}

	var summaries []ModelSummary
	fidLines := []string{"model\tcase\tchecked\tmatch\tdetail"}

	for _, model := range models {
		var results []ReplayResult
		var grades []RunGrade
		for ci, c := range cases {
			for run := 1; run <= nRuns; run++ {
				t0 := time.Now()
				res := rp.Replay(ctx, c, model, run)
				res.Ordinal = ordinals[ci]
				res.Case = labels[ci]
				results = append(results, res)

				fid := "n/a"
				if res.Fidelity.Checked {
					fid = fmt.Sprintf("match=%t", res.Fidelity.Match)
				}
				slog.Info("qualify-model: replay",
					"model", model, "case", labels[ci], "run", run,
					"wall", time.Since(t0).Round(time.Second),
					"validation", res.ValidationResult, "fidelity", fid,
					"run_err", errStr(res.RunErr))
				fidLines = append(fidLines, fmt.Sprintf("%s\t%s\t%t\t%t\t%s",
					model, labels[ci], res.Fidelity.Checked, res.Fidelity.Match, res.Fidelity.Detail))

				var g RunGrade
				if !*fidelityOnly {
					g = Grade(ctx, *verb, ref, absPath(*mutants), c, res)
				}
				grades = append(grades, g)
			}
		}
		if *unload {
			rp.Unload(model)
		}
		if !*fidelityOnly {
			s := Summarize(*verb, model, results, grades)
			summaries = append(summaries, s)
		}
	}

	writeLines(filepath.Join(*outDir, "fidelity.tsv"), fidLines)

	if *fidelityOnly {
		nMatch, nCheck := 0, 0
		for _, l := range fidLines[1:] {
			f := strings.Split(l, "\t")
			if f[2] == "true" {
				nCheck++
				if f[3] == "true" {
					nMatch++
				}
			}
		}
		fmt.Printf("\nfidelity: %d/%d checked prompts match the capture\n", nMatch, nCheck)
		fmt.Printf("detail: %s\n", filepath.Join(*outDir, "fidelity.tsv"))
		if nMatch != nCheck {
			os.Exit(1)
		}
		return
	}

	// Reports.
	tsv := []string{TSVHeader()}
	for _, s := range summaries {
		tsv = append(tsv, s.TSVRow())
	}
	writeLines(filepath.Join(*outDir, "summary.tsv"), tsv)

	table := Table(*verb+"  ["+cfg+"]", summaries)
	fmt.Println(table)
	_ = os.WriteFile(filepath.Join(*outDir, "table.txt"), []byte(table), 0o644)

	fmt.Printf("reports: %s\n", *outDir)
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func absPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func writeLines(path string, lines []string) {
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		slog.Warn("qualify-model: write", "path", path, "error", err)
	}
}
