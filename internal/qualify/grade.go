package qualify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ratchet/internal/splice"
	"ratchet/internal/verbs"
)

// RunGrade is one verb-specific score row for a single replay run.
type RunGrade struct {
	// Pass is the headline pass/fail against this verb's rubric.
	Pass bool
	// Partial is true when the rubric could only be partly evaluated (e.g. no
	// mutant fixtures for a WRITE bead).
	Partial bool
	// Cols are verb-specific columns surfaced in the report, in a stable key set
	// per verb.
	Cols map[string]string
	Note string
}

func col(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// Grade dispatches to the per-verb grader.
func Grade(ctx context.Context, verb string, ref *ReferenceDB, mutantRoot string, c Case, res ReplayResult) RunGrade {
	if res.RunErr != nil {
		return RunGrade{Note: "run error: " + res.RunErr.Error()}
	}
	switch verb {
	case "REFINE_TESTS_WRITE":
		return gradeWrite(ctx, mutantRoot, c, res)
	case "REFINE_TESTS_CRITIQUE":
		return gradeCritique(ctx, ref, c, res)
	case "REFINE_TESTS_JUDGE":
		return gradeJudge(ctx, ref, c, res)
	case "ADJUDICATE_NEXT_EXECUTION":
		return gradeAdjudicate(ctx, ref, c, res)
	default:
		return RunGrade{Note: "no grader for verb " + verb}
	}
}

// ---------- REFINE_TESTS_WRITE: mutation-style ----------

func gradeWrite(ctx context.Context, mutantRoot string, c Case, res ReplayResult) RunGrade {
	beadID := *c.Meta.BeadID
	spec, err := caseBeadSpec(ctx, filepath.Join(c.Dir, "db.sqlite"), beadID)
	if err != nil {
		return RunGrade{Note: err.Error()}
	}
	// The per-run folder the replayed Run() wrote into.
	folder := filepath.Join(res.RunDir, "folder")
	testFiles := spec.TestFiles()
	required := spec.RequiredTestFuncs()

	// Coverage: fraction of required Test funcs now present on disk.
	present := 0
	var missing []string
	for _, fn := range required {
		found := false
		for _, tf := range testFiles {
			src, rerr := os.ReadFile(filepath.Join(folder, tf))
			if rerr != nil {
				continue
			}
			if fm, ferr := splice.FuncMap(string(src)); ferr == nil {
				if _, ok := fm[fn]; ok {
					found = true
					break
				}
			}
		}
		if found {
			present++
		} else {
			missing = append(missing, fn)
		}
	}
	coverage := "n/a"
	if len(required) > 0 {
		coverage = fmt.Sprintf("%d/%d", present, len(required))
	}

	// Compile check: the generated test file must build against the package.
	compiles := runGo(folder, 8*time.Minute, "test", "-c", "-o", os.DevNull, ".") == nil

	beadDir := filepath.Join(mutantRoot, fmt.Sprintf("b%d", beadID))
	variants, verr := loadVariants(beadDir, spec.ImplFiles())
	if verr != nil || len(variants) == 0 {
		return RunGrade{
			Pass:    compiles,
			Partial: true,
			Cols: col("compiles", b2s(compiles), "passes_good", "?", "kills_mutant", "?",
				"coverage", coverage),
			Note: fmt.Sprintf("no mutant fixtures at %s — compile+coverage only%s", beadDir, missNote(missing)),
		}
	}

	runFuncs := strings.Join(required, "|")
	if runFuncs == "" {
		runFuncs = "Test"
	}
	gradeRoot := filepath.Join(res.RunDir, "grade")
	_ = os.MkdirAll(gradeRoot, 0o755)

	passesGood := false
	mutantsKilled := 0
	var detail []string
	for _, v := range variants {
		w := filepath.Join(gradeRoot, sanitize(v.name))
		if err := copyTree(folder, w); err != nil {
			detail = append(detail, v.name+":copyerr")
			continue
		}
		for rel, srcPath := range v.files {
			if err := copyFile(srcPath, filepath.Join(w, rel)); err != nil {
				detail = append(detail, v.name+":overlayerr")
			}
		}
		out, testErr := runGoOut(w, 8*time.Minute, "test", "-run", runFuncs, "-count=1", "./...")
		_ = os.WriteFile(filepath.Join(gradeRoot, sanitize(v.name)+".log"), []byte(out), 0o644)
		pass := testErr == nil
		detail = append(detail, fmt.Sprintf("%s=%s", v.name, map[bool]string{true: "pass", false: "fail"}[pass]))
		if v.good {
			passesGood = pass
		} else if !pass {
			mutantsKilled++
		}
	}
	nMutants := len(variants) - 1

	// kills_mutant is only meaningful when the good impl passes — otherwise every
	// variant fails trivially.
	killsCol := "n/a"
	if passesGood {
		killsCol = fmt.Sprintf("%d/%d", mutantsKilled, nMutants)
	}
	note := strings.Join(detail, " ") + missNote(missing)
	if !passesGood {
		note = "TEST FAILS vs known-good impl — " + note
	}

	pass := compiles && passesGood && mutantsKilled >= 1
	return RunGrade{
		Pass: pass,
		Cols: col("compiles", b2s(compiles), "passes_good", b2s(passesGood),
			"kills_mutant", killsCol, "coverage", coverage),
		Note: note,
	}
}

func missNote(missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	return " missing:" + strings.Join(missing, ",")
}

type implVariant struct {
	name  string
	good  bool
	files map[string]string // package-relative path -> absolute source path
}

// loadVariants reads b<bead>/{good,m*}/ subdirs. Each must contain exactly the
// impl files the bead produces.
func loadVariants(beadDir string, implFiles []string) ([]implVariant, error) {
	entries, err := os.ReadDir(beadDir)
	if err != nil {
		return nil, err
	}
	var out []implVariant
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		v := implVariant{name: e.Name(), good: e.Name() == "good", files: map[string]string{}}
		for _, rel := range implFiles {
			p := filepath.Join(beadDir, e.Name(), filepath.Base(rel))
			if _, serr := os.Stat(p); serr != nil {
				return nil, fmt.Errorf("%s/%s: missing %s", beadDir, e.Name(), filepath.Base(rel))
			}
			v.files[rel] = p
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// ---------- REFINE_TESTS_CRITIQUE: labelled catch/false-positive ----------

func gradeCritique(ctx context.Context, ref *ReferenceDB, c Case, res ReplayResult) RunGrade {
	label, err := ref.CritiqueLabel(ctx, *c.Meta.BeadID, cycleOf(c))
	if err != nil {
		return RunGrade{Note: err.Error()}
	}
	if res.ValidationResult != "valid" {
		return RunGrade{Cols: col("label", label, "verdict", "malformed", "flag", "?"),
			Note: res.ValidationResult}
	}
	out := res.Parsed.(verbs.RefineTestsCritiqueOutput)
	// Key on all_correct only — the hard "this test has a real defect" verdict.
	// Findings alone are noisy: the real baseline CRITIQUE routinely lists minor
	// findings on a test the JUDGE then approves, so "any finding == flagged"
	// would score the incumbent itself as a false positive.
	flagged := !out.AllCorrect
	// good -> should NOT flag (flagged == false-positive)
	// bad  -> should flag     (not flagged == miss)
	var pass bool
	var kind string
	switch label {
	case "good":
		pass = !flagged
		kind = map[bool]string{true: "false_positive", false: "correct_clear"}[flagged]
	default: // bad
		pass = flagged
		kind = map[bool]string{true: "caught", false: "missed"}[flagged]
	}
	return RunGrade{
		Pass: pass,
		Cols: col("label", label, "all_correct", b2s(out.AllCorrect),
			"findings", fmt.Sprintf("%d", len(out.Findings)), "outcome", kind),
		Note: firstLine(out.Summary),
	}
}

// ---------- REFINE_TESTS_JUDGE: verdict agreement ----------

func gradeJudge(ctx context.Context, ref *ReferenceDB, c Case, res ReplayResult) RunGrade {
	want, err := ref.JudgeDecision(ctx, *c.Meta.BeadID, cycleOf(c))
	if err != nil {
		return RunGrade{Note: err.Error()}
	}
	if res.ValidationResult != "valid" {
		return RunGrade{Cols: col("baseline", want, "verdict", "malformed", "agree", "false"),
			Note: res.ValidationResult}
	}
	out := res.Parsed.(verbs.RefineTestsJudgeOutput)
	agree := out.Decision == want
	return RunGrade{
		Pass: agree,
		Cols: col("baseline", want, "verdict", out.Decision, "agree", b2s(agree)),
		Note: firstLine(out.Summary),
	}
}

// ---------- ADJUDICATE_NEXT_EXECUTION: verdict agreement + spiral ----------

func gradeAdjudicate(ctx context.Context, ref *ReferenceDB, c Case, res ReplayResult) RunGrade {
	refs, err := ref.AdjudicationDecisions(ctx, *c.Meta.BeadID)
	if err != nil {
		return RunGrade{Note: err.Error()}
	}
	// Which ADJUDICATE dispatch for this bead is this? Ordinal by seq among the
	// corpus's ADJUDICATE cases for the same bead is baked into the case list;
	// the caller passes it via res.Ordinal.
	idx := res.Ordinal
	want := "?"
	if idx >= 0 && idx < len(refs) {
		want = refs[idx].Decision
	}
	dead := res.deadTurns()
	if res.ValidationResult != "valid" {
		return RunGrade{Cols: col("baseline", want, "verdict", "malformed", "agree", "false",
			"dead_turns", fmt.Sprintf("%d", dead)), Note: res.ValidationResult}
	}
	out := res.Parsed.(verbs.AdjudicateNextExecutionOutput)
	agree := want != "?" && out.Decision == want
	return RunGrade{
		Pass: agree,
		Cols: col("baseline", want, "verdict", out.Decision, "agree", b2s(agree),
			"dead_turns", fmt.Sprintf("%d", dead)),
		Note: firstLine(out.Reasoning),
	}
}

// ---------- helpers ----------

func cycleOf(c Case) int64 {
	if c.Meta.RefinementCycle != nil {
		return *c.Meta.RefinementCycle
	}
	return 1
}

func runGo(dir string, timeout time.Duration, args ...string) error {
	_, err := runGoOut(dir, timeout, args...)
	return err
}

func runGoOut(dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.Join(args, " ") + "\n" + string(out), err
}

func b2s(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}
