package verbs

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The REFINE_TESTS_CRITIQUE mechanical pre-pass.
//
// At CRITIQUE dispatch the folder is in a fixed state: the test file (from
// WRITE) is present, this bead's own impl files are scaffold stubs, prior
// beads' impls are real, and the package compiles. A corpus analysis of real
// CRITIQUE catches (docs/critique-redesign.md) found ~2/3 are mechanically or
// execution-detectable — the model was slowly rediscovering, through a forced
// 6-turn snippet loop, things a deterministic pass surfaces in seconds.
//
// The pre-pass compiles and runs the test file as-is and classifies each
// failure as expected-stub-behavior vs a real test defect, emitting "seed"
// findings with evidence attached only for the latter. The reshaped model turn
// then confirms/refutes each seed and does one focused pass for the
// spec-contradiction residual the checks can't see.

// critiqueSeedConfidence tunes how the reshaped prompt presents a seed.
//
//	high           — mechanically certain the test is defective (a compile
//	                 error located in the locked test file).
//	stub_explained — a real symptom (a panic) whose proximate cause is this
//	                 bead's own unimplemented stub returning a zero value; the
//	                 model must judge whether the test ALSO makes an assumption
//	                 a correct implementation would violate.
//	review         — the pre-pass found something worth the model's attention
//	                 but cannot itself decide (a panic in prior-bead code, a
//	                 spec-cross-check flag, a setup inconsistency).
type critiqueSeedConfidence string

const (
	seedHigh          critiqueSeedConfidence = "high"
	seedStubExplained critiqueSeedConfidence = "stub_explained"
	seedReview        critiqueSeedConfidence = "review"
)

// critiqueSeed is one mechanically-detected concern handed to the model with
// its evidence.
type critiqueSeed struct {
	Kind       string // "compile_error" | "panic" | "hang" | "spec_contradiction" | "setup_inconsistency"
	Subtest    string // "TestParser/ExactlyOneStatement/OnlyNewline"; "" for file-level
	Evidence   string // the compile-error line / panic message + stack + reasoning
	Confidence critiqueSeedConfidence
}

// subtestOutcome is one row of the `go test -json` run.
type subtestOutcome struct {
	Name     string // full "TestX/sub/sub"
	Action   string // "pass" | "fail" | "" (never reached a terminal action — e.g. the process panicked mid-run)
	Panicked bool
	Output   string
}

// critiquePrepassReport is the full result of the pre-pass: the raw
// compile/run outcome (always surfaced to the model as execution ground truth)
// plus the classified seeds.
type critiquePrepassReport struct {
	Compiled   bool
	CompileOut string
	Ran        bool
	Subtests   []subtestOutcome
	Seeds      []critiqueSeed
	Notes      []string // observations that are not seeds (a failure with a non-zero "got", etc.)
}

// critiquePrepassRunTimeout bounds the pre-pass's `go test` invocation. A var,
// not a const, so tests can shrink it.
var critiquePrepassRunTimeout = 90 * time.Second

// compileLineRe captures file, line, col, message from a Go build-error line
// ("./foo_test.go:12:3: undefined: Bar" or "foo_test.go:12:3: ...").
var compileLineRe = regexp.MustCompile(`^(?:\./)?(\S+?\.go):(\d+):(\d+):\s*(.*)$`)

// stackFrameRe captures an absolute "\t/path/to/file.go:123 +0x..." stack frame.
var stackFrameRe = regexp.MustCompile(`^\s+(/\S+\.go):(\d+)(?:\s|$)`)

// runCritiquePrepass compiles and runs the test file against the folder as it
// exists at CRITIQUE dispatch and returns the classified report. It never
// mutates the folder. currentImplFiles are this bead's (stubbed) non-test
// output files; requiredFuncs are the Test* names the exit criteria pin.
func runCritiquePrepass(ctx context.Context, folderPath string, testFiles, currentImplFiles, requiredFuncs []string) critiquePrepassReport {
	var rep critiquePrepassReport

	testBase := baseSet(testFiles)
	curImplBase := baseSet(currentImplFiles)
	priorImplBase := map[string]bool{}
	if entries, err := os.ReadDir(folderPath); err == nil {
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
				continue
			}
			if !curImplBase[n] {
				priorImplBase[n] = true
			}
		}
	}
	curSymbolRe := exportedSymbolRe(folderPath, currentImplFiles)

	// --- compile ---
	ok, out := runCompile(ctx, folderPath)
	rep.Compiled, rep.CompileOut = ok, out
	if !ok {
		for _, line := range strings.Split(out, "\n") {
			m := compileLineRe.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			if testBase[filepath.Base(m[1])] {
				rep.Seeds = append(rep.Seeds, critiqueSeed{
					Kind: "compile_error", Subtest: "",
					Evidence: strings.TrimSpace(line), Confidence: seedHigh,
				})
			}
		}
		if len(rep.Seeds) == 0 {
			rep.Notes = append(rep.Notes,
				"the package does not compile, but no error is located in the test file:\n"+out)
		}
		return rep
	}

	// --- run ---
	runRe := "."
	if len(requiredFuncs) > 0 {
		esc := make([]string, len(requiredFuncs))
		for i, f := range requiredFuncs {
			esc[i] = regexp.QuoteMeta(f)
		}
		runRe = "^(" + strings.Join(esc, "|") + ")$"
	}
	rctx, cancel := context.WithTimeout(ctx, critiquePrepassRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, "go", "test", "-json", "-run", runRe, "-count=1", "-timeout", "60s", ".")
	cmd.Dir = folderPath
	runOut, _ := cmd.CombinedOutput()
	rep.Ran = true

	subs, events := parseGoTestJSON(runOut)
	rep.Subtests = subs

	if msg, stack, culprit, kind, found := findTestPanic(events, subs); found {
		frameFile, frameLine := firstProjectFrame(stack, folderPath)
		conf := seedReview
		reason := ""
		switch {
		case kind == "hang":
			conf = seedReview
			reason = " A correct implementation of this bead's stub would not, on its own, cause the test to hang — check for an unbounded loop or a missing channel send/close in the test's own setup."
		case culprit != "" && curSymbolRe != nil && subtestBodyReferences(folderPath, testFiles, culprit, curSymbolRe):
			conf = seedStubExplained
			reason = " The panicking value flows from this bead's own code, which is currently an unimplemented stub returning a zero value. Judge whether the test ALSO makes an assumption a correct implementation would still violate (a genuine defect), or whether it is fine once the stub is implemented."
		case frameFile != "" && curImplBase[filepath.Base(frameFile)]:
			conf = seedStubExplained
			reason = " The fault is inside this bead's own stub."
		case frameFile != "" && priorImplBase[filepath.Base(frameFile)]:
			conf = seedReview
			reason = " The fault is inside already-implemented code from a prior bead — the test may be driving it into a state a correct implementation of THIS bead would not produce."
		case frameFile != "" && testBase[filepath.Base(frameFile)]:
			conf = seedReview
			reason = " The fault is in the test file's own logic (not a call into implementation code)."
		}
		loc := ""
		if frameFile != "" {
			loc = fmt.Sprintf(" (%s:%d)", filepath.Base(frameFile), frameLine)
		}
		seedKind := "panic"
		if kind == "hang" {
			seedKind = "hang"
		}
		rep.Seeds = append(rep.Seeds, critiqueSeed{
			Kind: seedKind, Subtest: culprit,
			Evidence:   strings.TrimSpace(msg) + loc + "\n" + indentLines(stack, "    ") + reason,
			Confidence: conf,
		})
	}

	// Non-seed notes: a failing subtest whose "got" value is demonstrably NOT a
	// stub zero value is worth the model's eye but is not asserted as a defect.
	for _, s := range subs {
		if s.Action != "fail" || s.Panicked {
			continue
		}
		if got, ok := nonZeroGot(s.Output); ok {
			rep.Notes = append(rep.Notes, fmt.Sprintf(
				"%s fails against the stub with a non-zero actual value (got %s) — not explained by an unimplemented stub returning a zero value; worth checking the expected value.",
				s.Name, got))
		}
	}

	return rep
}

// --- go test -json parsing ---

type testEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
	Output string `json:"Output"`
}

// parseGoTestJSON folds the -json event stream into per-subtest outcomes and
// returns the raw ordered events (for panic attribution). Non-JSON lines
// (which `go test` emits on a build failure or a hard crash) are captured as
// synthetic package-level output events so panic detection still sees them.
func parseGoTestJSON(raw []byte) ([]subtestOutcome, []testEvent) {
	agg := map[string]*subtestOutcome{}
	var order []string
	var events []testEvent
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		var e testEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			events = append(events, testEvent{Action: "output", Output: line + "\n"})
			continue
		}
		events = append(events, e)
		if e.Test == "" {
			continue
		}
		o := agg[e.Test]
		if o == nil {
			o = &subtestOutcome{Name: e.Test}
			agg[e.Test] = o
			order = append(order, e.Test)
		}
		switch e.Action {
		case "output":
			o.Output += e.Output
		case "pass", "fail", "skip":
			o.Action = e.Action
		}
	}
	out := make([]subtestOutcome, 0, len(order))
	for _, name := range order {
		out = append(out, *agg[name])
	}
	return out, events
}

// findTestPanic scans the event stream in order for a panic or a timeout. It
// returns the panic message line, the trailing stack text, the culprit subtest
// (the deepest test that had started but never reached a terminal action), and
// kind ("panic" or "hang").
func findTestPanic(events []testEvent, subs []subtestOutcome) (msg, stack, culprit, kind string, found bool) {
	terminal := map[string]bool{}
	started := map[string]bool{}
	for _, s := range subs {
		if s.Action != "" {
			terminal[s.Name] = true
		}
	}

	var lastRunning string
	var collecting bool
	var b strings.Builder
	for _, e := range events {
		if e.Action == "run" && e.Test != "" {
			started[e.Test] = true
			lastRunning = e.Test
		}
		if e.Action != "output" {
			continue
		}
		line := e.Output
		if !collecting {
			trimmed := strings.TrimLeft(line, " \t")
			switch {
			case strings.HasPrefix(trimmed, "panic: test timed out"):
				kind, found, collecting = "hang", true, true
				msg = strings.TrimRight(trimmed, "\n")
				culprit = deepestUnfinished(started, terminal, lastRunning)
			case strings.HasPrefix(trimmed, "panic:"):
				kind, found, collecting = "panic", true, true
				msg = strings.TrimRight(trimmed, "\n")
				culprit = deepestUnfinished(started, terminal, lastRunning)
			}
			continue
		}
		// collecting the stack
		if strings.HasPrefix(line, "=== ") || strings.HasPrefix(line, "--- ") {
			break
		}
		b.WriteString(line)
		if b.Len() > 8000 {
			break
		}
	}
	stack = b.String()
	return
}

// deepestUnfinished picks the most specific test that started and never
// finished; falls back to the last test seen running.
func deepestUnfinished(started, terminal map[string]bool, lastRunning string) string {
	best := ""
	for name := range started {
		if terminal[name] {
			continue
		}
		if len(name) > len(best) {
			best = name
		}
	}
	if best != "" {
		return best
	}
	return lastRunning
}

// firstProjectFrame returns the base filename and line of the first stack frame
// whose file lives directly in folderPath.
func firstProjectFrame(stack, folderPath string) (string, int) {
	for _, line := range strings.Split(stack, "\n") {
		m := stackFrameRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if filepath.Dir(m[1]) != folderPath {
			continue
		}
		n, _ := parseIntSafe(m[2])
		return m[1], n
	}
	return "", 0
}

// --- classification helpers ---

// nonZeroGotRe pulls the actual value out of common failure phrasings.
var nonZeroGotRe = regexp.MustCompile(`(?i)\bgot[:= ]+("[^"]*"|\S+)`)

// zeroLiteral is the set of rendered zero values a scaffold stub produces.
var zeroLiteral = map[string]bool{
	"0": true, "0.0": true, `""`: true, "''": true, "[]": true, "{}": true,
	"<nil>": true, "nil": true, "false": true, "map[]": true, "0x0": true,
}

// nonZeroGot reports the actual value from a failure message when it can be
// identified AND is not a zero value. Conservative: if no "got" token can be
// found, it returns ok=false (treated as stub behavior, stays silent).
func nonZeroGot(output string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		m := nonZeroGotRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v := strings.TrimRight(strings.TrimSpace(m[1]), ".,;:)")
		if v == "" || zeroLiteral[v] {
			return "", false
		}
		// A bare "got []int{}" / "got []string(nil)" style — treat empty-ish.
		if strings.HasSuffix(v, "(nil)") || strings.HasSuffix(v, "{}") {
			return "", false
		}
		return v, true
	}
	return "", false
}

// exportedSymbolRe builds a \b(Sym1|Sym2|...)\b matcher over every exported
// func / method / type / var / const declared in implFiles. nil if none.
func exportedSymbolRe(folderPath string, implFiles []string) *regexp.Regexp {
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if n == "" || !isExportedIdent(n) || seen[n] {
			return
		}
		seen[n] = true
		names = append(names, regexp.QuoteMeta(n))
	}
	for _, f := range implFiles {
		src, err := os.ReadFile(filepath.Join(folderPath, f))
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "", src, 0)
		if err != nil {
			continue
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				add(d.Name.Name)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						add(s.Name.Name)
					case *ast.ValueSpec:
						for _, n := range s.Names {
							add(n.Name)
						}
					}
				}
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return regexp.MustCompile(`\b(` + strings.Join(names, "|") + `)\b`)
}

// subtestBodyReferences reports whether the top-level Test function enclosing
// culprit (its first "/"-segment) mentions any symbol matched by symRe.
func subtestBodyReferences(folderPath string, testFiles []string, culprit string, symRe *regexp.Regexp) bool {
	top := culprit
	if i := strings.IndexByte(top, '/'); i >= 0 {
		top = top[:i]
	}
	for _, f := range testFiles {
		src, err := os.ReadFile(filepath.Join(folderPath, f))
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "", src, 0)
		if err != nil {
			// Can't parse — fall back to a whole-file scan.
			if symRe.Match(src) {
				return true
			}
			continue
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name.Name != top || fd.Body == nil {
				continue
			}
			start := fset.Position(fd.Body.Pos()).Offset
			end := fset.Position(fd.Body.End()).Offset
			if start >= 0 && end <= len(src) && symRe.MatchString(string(src[start:end])) {
				return true
			}
		}
	}
	return false
}

// --- small utilities ---

func baseSet(paths []string) map[string]bool {
	m := make(map[string]bool, len(paths))
	for _, p := range paths {
		m[filepath.Base(p)] = true
	}
	return m
}

func indentLines(s, prefix string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func parseIntSafe(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n, fmt.Errorf("not an int: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
