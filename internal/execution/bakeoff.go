package execution

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
)

// RunExecBakeoffMain is the entry point for `ratchet exec-bakeoff`: replay one
// EXECUTE_BEAD tool-loop against a list of candidate models on identical inputs
// and report which produced compiling, test-passing code and how fast.
//
// It is a qualification harness, not part of the pipeline: no DB, no monitor, no
// termination_cause writes. Each (spec, model) run gets its own copy of the
// template folder so runs never contaminate each other. Models are exercised
// sequentially; the box loads one at a time.
//
// Example:
//
//	ratchet exec-bakeoff \
//	  --template-dir=/path/to/project-folder \
//	  --specs=thin:thin.json,prescriptive:presc.json \
//	  --models=gemma4:31b,muse-glimmer:30b-q8_0-dflash,glm-4.7-flash \
//	  --budget=1800 --out=/tmp/bakeoff
//
// Each spec JSON file is {"full_text": "...", "output_files": [...], "exit_criteria": [...]}.
func RunExecBakeoffMain(args []string) {
	flags := flag.NewFlagSet("exec-bakeoff", flag.ExitOnError)
	templateDir := flags.String("template-dir", "", "clean project folder to copy per run (required)")
	specsArg := flags.String("specs", "", "comma-separated label:path.json entries (required)")
	modelsArg := flags.String("models", "", "comma-separated model names (required)")
	budget := flags.Int("budget", 1800, "per-run execution budget in seconds")
	ollamaURL := flags.String("ollama", "http://192.168.50.241:11434", "Ollama base URL")
	outDir := flags.String("out", "", "output directory for per-run folders + results.tsv (required)")
	testCmd := flags.String("test-cmd", "go test -run TestParser ./...", "command run in the work dir to check output")
	_ = flags.Parse(args)

	if *templateDir == "" || *specsArg == "" || *modelsArg == "" || *outDir == "" {
		slog.Error("exec-bakeoff: --template-dir, --specs, --models and --out are all required")
		os.Exit(1)
	}

	specs, err := loadBakeoffSpecs(*specsArg)
	if err != nil {
		slog.Error("exec-bakeoff: load specs", "error", err)
		os.Exit(1)
	}
	models := splitCSV(*modelsArg)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		slog.Error("exec-bakeoff: mkdir out", "error", err)
		os.Exit(1)
	}
	resultsPath := filepath.Join(*outDir, "results.tsv")
	rf, err := os.Create(resultsPath)
	if err != nil {
		slog.Error("exec-bakeoff: create results.tsv", "error", err)
		os.Exit(1)
	}
	fmt.Fprintln(rf, strings.Join([]string{
		"spec", "model", "termination", "wall_s", "first_write_s", "turns",
		"write_calls", "thinking_chars", "content_chars", "compiles", "tests_pass",
	}, "\t"))

	oc := ollama.NewUnbounded(*ollamaURL)
	var results []bakeoffResult

	for _, sp := range specs {
		for _, model := range models {
			runDir := filepath.Join(*outDir, sanitize(sp.Label), sanitize(model))
			slog.Info("exec-bakeoff: run", "spec", sp.Label, "model", model, "dir", runDir)

			if err := resetWorkDir(*templateDir, runDir); err != nil {
				slog.Error("exec-bakeoff: reset work dir", "error", err, "model", model)
				continue
			}
			tracePath := filepath.Join(runDir, "_bakeoff_trace.log")

			res := bakeoffResult{Spec: sp.Label, Model: model}
			r, runErr := bakeoffRunOne(context.Background(), oc, model, sp, runDir, tracePath, *budget)
			if runErr != nil {
				res.Termination = "error: " + runErr.Error()
			} else {
				res = r
				res.Spec, res.Model = sp.Label, model
			}

			// The trace file lives inside runDir; move it out before go test so
			// it isn't picked up as a stray package or counted by the tooling.
			_ = os.Rename(tracePath, filepath.Join(*outDir, sanitize(sp.Label)+"_"+sanitize(model)+"_trace.log"))

			res.Compiles = runShell(runDir, "go build ./...") == nil
			if res.Compiles {
				res.TestsPass = runShell(runDir, *testCmd) == nil
			}

			results = append(results, res)
			fmt.Fprintln(rf, res.tsv())
			rf.Sync()
			printBakeoffRow(res)

			// Nudge the remote model out of VRAM so the next one loads clean.
			unloadModel(*ollamaURL, model)
		}
	}
	rf.Close()

	fmt.Println()
	printBakeoffTable(results)
	fmt.Printf("\nresults: %s\n", resultsPath)
}

type bakeoffSpec struct {
	Label        string
	FullText     string   `json:"full_text"`
	OutputFiles  []string `json:"output_files"`
	ExitCriteria []string `json:"exit_criteria"`
}

type bakeoffResult struct {
	Spec           string
	Model          string
	Termination    string // success | timeout | no_write | error: ...
	WallSecs       float64
	FirstWriteSecs float64 // -1 if the model never called write_file
	Turns          int
	WriteCalls     int
	ThinkingChars  int
	ContentChars   int
	Compiles       bool
	TestsPass      bool
}

func (r bakeoffResult) tsv() string {
	fw := "-"
	if r.FirstWriteSecs >= 0 {
		fw = fmt.Sprintf("%.0f", r.FirstWriteSecs)
	}
	return strings.Join([]string{
		r.Spec, r.Model, r.Termination,
		fmt.Sprintf("%.0f", r.WallSecs), fw,
		fmt.Sprintf("%d", r.Turns), fmt.Sprintf("%d", r.WriteCalls),
		fmt.Sprintf("%d", r.ThinkingChars), fmt.Sprintf("%d", r.ContentChars),
		fmt.Sprintf("%t", r.Compiles), fmt.Sprintf("%t", r.TestsPass),
	}, "\t")
}

// bakeoffRunOne runs the EXECUTE_BEAD tool-loop once. It mirrors
// runExecuteBeadReal's turn loop (same system prompt, same tools, same
// OmitFormat, same no-write / missing-path corrective injections, same soft
// budget stop) but strips everything pipeline-specific: no DB, no monitor, no
// SIGTERM handling, no prior-attempt history.
func bakeoffRunOne(ctx context.Context, oc *ollama.Client, model string, sp bakeoffSpec, folderPath, tracePath string, budget int) (bakeoffResult, error) {
	traceFile, err := os.Create(tracePath)
	if err != nil {
		return bakeoffResult{}, fmt.Errorf("create trace: %w", err)
	}
	defer traceFile.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	softStopCh := make(chan struct{})
	budgetTimer := time.NewTimer(time.Duration(budget) * time.Second)
	defer budgetTimer.Stop()
	go func() {
		select {
		case <-budgetTimer.C:
			close(softStopCh)
			time.AfterFunc(writeGracePeriod, cancel)
		case <-ctx.Done():
		}
	}()

	// Which files the model is told to write this attempt (tests-locked =>
	// implementation files only), matching runExecuteBeadReal.
	var expectedFiles []string
	testFirst := isTestFirstMode(folderPath, sp.OutputFiles)
	testsLocked := !testFirst && isTestsLockedMode(folderPath, sp.OutputFiles)
	switch {
	case testFirst:
		for _, f := range sp.OutputFiles {
			if strings.HasSuffix(f, "_test.go") {
				expectedFiles = append(expectedFiles, f)
			}
		}
	case testsLocked:
		for _, f := range sp.OutputFiles {
			if !strings.HasSuffix(f, "_test.go") {
				expectedFiles = append(expectedFiles, f)
			}
		}
	default:
		expectedFiles = sp.OutputFiles
	}

	tools := toolDefinitions()
	execOpts := &ollama.Options{OmitFormat: true}
	messages := []ollama.Message{
		{Role: "system", Content: guidance.InjectForVerbPath(executeBeadSystemPrompt, folderPath, "EXECUTE_BEAD", "")},
		{Role: "user", Content: buildBeadUserMsg(sp.FullText, sp.OutputFiles, sp.ExitCriteria,
			loadContextFiles(folderPath, sp.OutputFiles), "", "", folderPath)},
	}

	res := bakeoffResult{FirstWriteSecs: -1}
	start := time.Now()
	var stubWarned, missingPathWarned bool

	for turn := 1; ; turn++ {
		res.Turns = turn
		writeLine(traceFile, fmt.Sprintf("[TURN %d]", turn))

		msg, err := oc.ChatWithTools(ctx, model, messages, tools, execOpts, traceFile)
		if err != nil {
			select {
			case <-softStopCh:
				res.Termination = "timeout"
				res.WallSecs = time.Since(start).Seconds()
				return res, nil
			default:
			}
			return res, fmt.Errorf("model call: %w", err)
		}
		messages = append(messages, msg)
		res.ThinkingChars += len(msg.Thinking)
		res.ContentChars += len(msg.Content)

		select {
		case <-softStopCh:
			writeLine(traceFile, "[terminated: timeout]")
			res.Termination = "timeout"
			res.WallSecs = time.Since(start).Seconds()
			return res, nil
		default:
		}

		if len(msg.ToolCalls) == 0 {
			if !stubWarned && res.WriteCalls == 0 && len(expectedFiles) > 0 {
				stubWarned = true
				writeLine(traceFile, "[injected: no-write warning]")
				messages = append(messages, ollama.Message{Role: "user", Content: buildNoWriteWarning(expectedFiles)})
				continue
			}
			if stubWarned && res.WriteCalls == 0 {
				writeLine(traceFile, "[done — no writes after warning]")
				res.Termination = "no_write"
				res.WallSecs = time.Since(start).Seconds()
				return res, nil
			}
			writeLine(traceFile, "[done — no further tool calls]")
			res.Termination = "success"
			res.WallSecs = time.Since(start).Seconds()
			return res, nil
		}

		var missingPath bool
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name == "write_file" {
				res.WriteCalls++
				if res.FirstWriteSecs < 0 {
					res.FirstWriteSecs = time.Since(start).Seconds()
				}
			}
			writeLine(traceFile, fmt.Sprintf("[tool: %s %v]", tc.Function.Name, tc.Function.Arguments))
			result := executeTool(ctx, tc, folderPath)
			writeLine(traceFile, fmt.Sprintf("[result]\n%s", result))
			if tc.Function.Name == "write_file" && strings.Contains(result, "write_file requires a 'path' argument") {
				missingPath = true
			}
			messages = append(messages, ollama.Message{Role: "tool", Content: result})
		}
		if missingPath && !missingPathWarned {
			missingPathWarned = true
			writeLine(traceFile, "[injected: missing write_file path]")
			messages = append(messages, ollama.Message{Role: "user", Content: buildMissingPathWarning(expectedFiles)})
		}

		select {
		case <-softStopCh:
			writeLine(traceFile, "[terminated: timeout]")
			res.Termination = "timeout"
			res.WallSecs = time.Since(start).Seconds()
			return res, nil
		default:
		}
	}
}

func loadBakeoffSpecs(arg string) ([]bakeoffSpec, error) {
	var out []bakeoffSpec
	for _, entry := range splitCSV(arg) {
		label, path, ok := strings.Cut(entry, ":")
		if !ok {
			return nil, fmt.Errorf("spec entry %q must be label:path", entry)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var sp bakeoffSpec
		if err := json.Unmarshal(data, &sp); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if sp.FullText == "" || len(sp.OutputFiles) == 0 {
			return nil, fmt.Errorf("%s: full_text and output_files are required", path)
		}
		sp.Label = label
		out = append(out, sp)
	}
	return out, nil
}

// resetWorkDir removes dst and re-copies src into it, minus the traces/ subtree
// and any *_bakeoff_trace.log — so every run starts from an identical tree.
func resetWorkDir(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if strings.HasPrefix(rel, "traces"+string(os.PathSeparator)) || rel == "traces" {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

func runShell(dir, command string) error {
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Info("exec-bakeoff: check failed", "dir", filepath.Base(dir), "cmd", command,
			"tail", lastN(string(out), 400))
	}
	return err
}

// unloadModel asks the remote Ollama to drop a model from memory so the next
// candidate loads without competing for VRAM. Best-effort.
func unloadModel(baseURL, model string) {
	body := fmt.Sprintf(`{"model":%q,"keep_alive":0}`, model)
	cmd := exec.Command("curl", "-s", "-m", "20", "-XPOST", baseURL+"/api/generate", "-d", body)
	_ = cmd.Run()
	time.Sleep(2 * time.Second)
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func sanitize(s string) string {
	r := strings.NewReplacer("/", "_", ":", "_", " ", "_")
	return r.Replace(s)
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

func printBakeoffRow(r bakeoffResult) {
	fw := "never"
	if r.FirstWriteSecs >= 0 {
		fw = fmt.Sprintf("%.0fs", r.FirstWriteSecs)
	}
	slog.Info("exec-bakeoff: done",
		"spec", r.Spec, "model", r.Model, "termination", r.Termination,
		"wall", fmt.Sprintf("%.0fs", r.WallSecs), "first_write", fw,
		"turns", r.Turns, "writes", r.WriteCalls,
		"thinking_chars", r.ThinkingChars, "compiles", r.Compiles, "tests_pass", r.TestsPass)
}

func printBakeoffTable(results []bakeoffResult) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Spec != results[j].Spec {
			return results[i].Spec < results[j].Spec
		}
		// pass first, then compiles, then by wall time
		si, sj := rankResult(results[i]), rankResult(results[j])
		if si != sj {
			return si > sj
		}
		return results[i].WallSecs < results[j].WallSecs
	})
	fmt.Printf("%-14s %-32s %-12s %8s %10s %6s %7s %10s %8s %9s\n",
		"SPEC", "MODEL", "TERMINATION", "WALL", "1ST-WRITE", "TURNS", "WRITES", "THINKING", "COMPILE", "TESTS")
	for _, r := range results {
		fw := "never"
		if r.FirstWriteSecs >= 0 {
			fw = fmt.Sprintf("%.0fs", r.FirstWriteSecs)
		}
		fmt.Printf("%-14s %-32s %-12s %7.0fs %10s %6d %7d %10d %8t %9t\n",
			r.Spec, truncate(r.Model, 32), truncate(r.Termination, 12), r.WallSecs, fw,
			r.Turns, r.WriteCalls, r.ThinkingChars, r.Compiles, r.TestsPass)
	}
}

func rankResult(r bakeoffResult) int {
	switch {
	case r.TestsPass:
		return 3
	case r.Compiles:
		return 2
	case r.WriteCalls > 0:
		return 1
	default:
		return 0
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
