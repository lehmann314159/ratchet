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
	"strconv"
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
//	                 setup inconsistency).
type critiqueSeedConfidence string

const (
	seedHigh          critiqueSeedConfidence = "high"
	seedStubExplained critiqueSeedConfidence = "stub_explained"
	seedReview        critiqueSeedConfidence = "review"
)

// critiqueSeed is one mechanically-detected concern handed to the model with
// its evidence.
type critiqueSeed struct {
	Kind       string // "compile_error" | "panic" | "hang" | "setup_inconsistency"
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
// output files; requiredFuncs are the Test* names the exit criteria pin;
// specText is the bead full_text plus any design-doc excerpt, used by the
// conservative spec cross-check.
func runCritiquePrepass(ctx context.Context, folderPath string, testFiles, currentImplFiles, requiredFuncs []string, specText string) critiquePrepassReport {
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
		for i := range rep.Subtests {
			if rep.Subtests[i].Name == culprit {
				rep.Subtests[i].Panicked = true
			}
		}
		subs = rep.Subtests
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

	// --- static checks (phase 3) ---
	//
	// Setup-consistency IS a seed: it is gated on an actual panic plus a
	// structural asymmetry between sibling subtests, so its precision is high.
	//
	// The spec cross-check is a NOTE, not a seed. On the validated p48 corpus it
	// caught none of the labelled defects (they are behavioral / over-spec, not
	// "asserted constant absent from the spec text") and produced a few
	// false positives on values the design document pins but the excerpt this
	// pass sees didn't happen to quote. A note nudges the model's attention
	// during its own spec-contradiction pass without forcing a verdict cycle.
	// See docs/critique-redesign.md.
	rep.Seeds = append(rep.Seeds, setupInconsistencySeeds(folderPath, testFiles, subs, curSymbolRe)...)
	rep.Notes = append(rep.Notes, specCrossCheckNotes(folderPath, testFiles, specText)...)

	return rep
}

// CritiquePrepassProfile re-runs the deterministic pre-pass against a folder
// and returns a compact "kind/confidence" list plus the note count. Harness
// helper for `ratchet qualify-model` grading — not used by the pipeline.
func CritiquePrepassProfile(ctx context.Context, folderPath string, testFiles, currentImplFiles, requiredFuncs []string, specText string) (seeds []string, notes int) {
	rep := runCritiquePrepass(ctx, folderPath, testFiles, currentImplFiles, requiredFuncs, specText)
	for _, s := range rep.Seeds {
		seeds = append(seeds, s.Kind+"/"+string(s.Confidence))
	}
	return seeds, len(rep.Notes)
}

// --- spec cross-check (conservative) ---

// wellKnownAssertString is the set of string literals that show up in assertion
// position across nearly every Go test and carry no spec meaning.
var wellKnownAssertString = map[string]bool{
	"true": true, "false": true, "nil": true, "error": true, "ok": true,
	"test": true, "GET": true, "POST": true, "PUT": true, "DELETE": true,
	"application/json": true, "text/plain": true, "text/html": true,
	"main": true, "localhost": true,
}

// specTokenRe splits a candidate string into comparable alphanumeric tokens.
var specTokenRe = regexp.MustCompile(`[A-Za-z0-9]+`)

// alphaTokenRe matches a run of >=3 letters — a candidate literal needs one to
// be considered a "named constant" rather than a computed number/output.
var alphaTokenRe = regexp.MustCompile(`[A-Za-z]{3,}`)

// normalizeForSpecMatch lowercases and keeps only alphanumerics and single
// spaces, so "PUSH_CONST 10" and "push const  10" compare equal.
func normalizeForSpecMatch(s string) string {
	return strings.Join(specTokenRe.FindAllString(strings.ToLower(s), -1), " ")
}

// specCrossCheckNotes lists string literals the test asserts as an exact
// expected value (an == / != operand, a strings.Contains/HasPrefix/HasSuffix
// argument, or an element of an expected/want composite literal) that appear
// NOWHERE in the bead spec or design-doc excerpt. Emitted as non-seed notes:
// conservative by construction, but on validated data a fair fraction are
// values the design document pins that this pass's spec text didn't quote, so
// they steer the model's own spec-contradiction pass rather than assert a
// defect.
func specCrossCheckNotes(folderPath string, testFiles []string, specText string) []string {
	if strings.TrimSpace(specText) == "" {
		return nil
	}
	normSpec := normalizeForSpecMatch(specText)
	var notes []string
	seen := map[string]bool{}
	for _, a := range assertedStringLiterals(folderPath, testFiles) {
		v := a.value
		if len(v) < 4 || !alphaTokenRe.MatchString(v) || seen[v] {
			continue // too short, or no >=3-letter run (a computed number/output, not a named constant)
		}
		if wellKnownAssertString[v] || wellKnownAssertString[strings.ToLower(v)] {
			continue
		}
		nv := normalizeForSpecMatch(v)
		if nv == "" || strings.Contains(normSpec, nv) {
			continue
		}
		// Token-wise fallback: every alphanumeric token of the literal is
		// somewhere in the spec (the spec names the parts, the test just
		// composes them) -> not a contradiction.
		allTokens := true
		for _, tok := range specTokenRe.FindAllString(strings.ToLower(v), -1) {
			if len(tok) >= 2 && !strings.Contains(normSpec, tok) {
				allTokens = false
				break
			}
		}
		if allTokens {
			continue
		}
		seen[v] = true
		where := ""
		if a.subtest != "" {
			where = " (" + a.subtest + ")"
		}
		notes = append(notes, fmt.Sprintf("the test asserts the exact string %q%s, which appears nowhere in "+
			"the bead spec or design-doc excerpt — confirm the spec pins it (even implicitly), or derive the "+
			"correct value from the spec's stated rule.", v, where))
		if len(notes) >= 5 {
			break
		}
	}
	return notes
}

type assertedStr struct {
	value   string
	subtest string
	line    int
}

// assertedStringLiterals walks testFiles' ASTs and collects string literals in
// assertion position.
func assertedStringLiterals(folderPath string, testFiles []string) []assertedStr {
	var out []assertedStr
	for _, f := range testFiles {
		src, err := os.ReadFile(filepath.Join(folderPath, f))
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "", src, 0)
		if err != nil {
			continue
		}
		// Track the nearest enclosing t.Run("name", ...) for attribution.
		var runStack []string
		var visit func(n ast.Node)
		record := func(expr ast.Expr) {
			bl, ok := expr.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return
			}
			s, uerr := strconv.Unquote(bl.Value)
			if uerr != nil {
				return
			}
			sub := ""
			if len(runStack) > 0 {
				sub = strings.Join(runStack, "/")
			}
			out = append(out, assertedStr{value: s, subtest: sub, line: fset.Position(bl.Pos()).Line})
		}
		visit = func(n ast.Node) {
			if n == nil {
				return
			}
			pushed := false
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					switch sel.Sel.Name {
					case "Run":
						if len(call.Args) > 0 {
							if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
								if name, e := strconv.Unquote(bl.Value); e == nil {
									runStack = append(runStack, name)
									pushed = true
								}
							}
						}
					case "Contains", "HasPrefix", "HasSuffix", "EqualFold", "Equal":
						for _, arg := range call.Args {
							record(arg)
						}
					}
				}
			}
			if bin, ok := n.(*ast.BinaryExpr); ok && (bin.Op == token.EQL || bin.Op == token.NEQ) {
				record(bin.X)
				record(bin.Y)
			}
			if as, ok := n.(*ast.AssignStmt); ok {
				expected := false
				for _, lhs := range as.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						l := strings.ToLower(id.Name)
						if strings.Contains(l, "expect") || strings.Contains(l, "want") {
							expected = true
						}
					}
				}
				if expected {
					for _, rhs := range as.Rhs {
						ast.Inspect(rhs, func(m ast.Node) bool {
							if bl, ok := m.(*ast.BasicLit); ok {
								record(bl)
							}
							return true
						})
					}
				}
			}
			ast.Inspect(n, func(c ast.Node) bool {
				if c == nil || c == n {
					return true
				}
				visit(c)
				return false
			})
			if pushed {
				runStack = runStack[:len(runStack)-1]
			}
		}
		for _, decl := range file.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Body != nil {
				visit(fd.Body)
			}
		}
	}
	return out
}

// --- setup consistency ---

// nilOrIndexFailRe matches a failure message that reads like a missing-setup
// symptom rather than a wrong expected value.
var nilOrIndexFailRe = regexp.MustCompile(`(?i)nil pointer|invalid memory|index out of range|nil map|slice bounds`)

// setupInconsistencySeeds flags the panic-before-assertion pattern: a subtest
// that panicked (or failed with a nil/index symptom) while a sibling subtest
// under the same Test function — one that exercises the same bead function —
// performs package-level state setup the failing one omits.
//
// Gated on an actual failure symptom (not just "A has more setup than B"):
// sibling subtests legitimately differ in setup (one registers a variable the
// code under test creates, another pre-registers one it needs), so setup
// asymmetry alone is far too noisy to seed.
func setupInconsistencySeeds(folderPath string, testFiles []string, subs []subtestOutcome, curSymRe *regexp.Regexp) []critiqueSeed {
	suspect := map[string]bool{}
	for _, s := range subs {
		if s.Panicked || (s.Action == "fail" && nilOrIndexFailRe.MatchString(s.Output)) {
			suspect[s.Name] = true
		}
	}
	if len(suspect) == 0 || curSymRe == nil {
		return nil
	}

	type stInfo struct {
		top      string
		name     string
		setupOps map[string]bool
		callsCur map[string]bool
	}
	var infos []stInfo
	for _, f := range testFiles {
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
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil || !strings.HasPrefix(fd.Name.Name, "Test") {
				continue
			}
			top := fd.Name.Name
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Run" || len(call.Args) < 2 {
					return true
				}
				bl, ok := call.Args[0].(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					return true
				}
				name, e := strconv.Unquote(bl.Value)
				if e != nil {
					return true
				}
				fn, ok := call.Args[1].(*ast.FuncLit)
				if !ok || fn.Body == nil {
					return true
				}
				info := stInfo{top: top, name: name, setupOps: map[string]bool{}, callsCur: map[string]bool{}}
				bodyStart := fset.Position(fn.Body.Pos()).Offset
				bodyEnd := fset.Position(fn.Body.End()).Offset
				if bodyStart >= 0 && bodyEnd <= len(src) {
					for _, m := range curSymRe.FindAllString(string(src[bodyStart:bodyEnd]), -1) {
						info.callsCur[m] = true
					}
				}
				ast.Inspect(fn.Body, func(m ast.Node) bool {
					as, ok := m.(*ast.AssignStmt)
					if !ok {
						return true
					}
					for _, lhs := range as.Lhs {
						switch e := lhs.(type) {
						case *ast.IndexExpr:
							info.setupOps[exprString(e.X)+"[]="] = true
						case *ast.SelectorExpr:
							info.setupOps[exprString(e)+"="] = true
						}
					}
					return true
				})
				infos = append(infos, info)
				return true
			})
		}
	}

	var seeds []critiqueSeed
	for _, b := range infos {
		full := b.top + "/" + b.name
		if !suspect[full] && !suspect[b.name] {
			continue
		}
		for _, a := range infos {
			if a.top != b.top || a.name == b.name {
				continue
			}
			if suspect[a.top+"/"+a.name] || suspect[a.name] {
				continue // both broken — not a useful contrast
			}
			if !shareKey(a.callsCur, b.callsCur) {
				continue
			}
			var missing []string
			for op := range a.setupOps {
				if !b.setupOps[op] {
					missing = append(missing, op)
				}
			}
			if len(missing) == 0 {
				continue
			}
			sort.Strings(missing)
			seeds = append(seeds, critiqueSeed{
				Kind: "setup_inconsistency", Subtest: full,
				Evidence: fmt.Sprintf("%s failed with a missing-setup symptom while its sibling %s/%s — which exercises the same function — performs setup this one omits: %s. Check whether %s makes an assumption about pre-existing state that it never establishes.",
					full, a.top, a.name, strings.Join(missing, ", "), b.name),
				Confidence: seedReview,
			})
			if len(seeds) >= 3 {
				return seeds
			}
			break
		}
	}
	return seeds
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.IndexExpr:
		return exprString(x.X) + "[]"
	}
	return "?"
}

func shareKey(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// formatCritiquePrepass renders the report as the "Mechanical Pre-Pass Results"
// section of the CRITIQUE user message: the raw compile/run outcome (always,
// as execution ground truth) followed by the classified seeds.
func formatCritiquePrepass(rep critiquePrepassReport) string {
	var b strings.Builder
	b.WriteString("## Mechanical Pre-Pass Results\n\n")
	b.WriteString("A deterministic pass compiled the package and ran the test file as it stands now " +
		"(test file present, THIS bead's implementation still scaffold stubs, prior beads' code real). " +
		"This is execution ground truth — what the tests actually did, not a prediction.\n\n")

	if !rep.Compiled {
		b.WriteString("### Compile: FAILED\n\n```\n" + strings.TrimSpace(rep.CompileOut) + "\n```\n\n")
	} else {
		b.WriteString("### Compile: ok\n\n")
		if rep.Ran {
			b.WriteString("### Test run against the scaffold stubs\n\n")
			if len(rep.Subtests) == 0 {
				b.WriteString("_the run reported no subtests_\n\n")
			} else {
				for _, s := range rep.Subtests {
					status := s.Action
					if status == "" {
						status = "did not finish"
					}
					if s.Panicked {
						status = "panic"
					}
					fmt.Fprintf(&b, "- `%s`: %s\n", s.Name, status)
				}
				b.WriteString("\nA failure here is EXPECTED wherever it is only the unimplemented stub " +
					"returning a zero value — that is not a test defect. A seed below is a failure the " +
					"pre-pass judged to be more than that.\n\n")
			}
		}
	}

	if len(rep.Seeds) == 0 {
		b.WriteString("### Seed findings: none\n\n" +
			"The pre-pass found nothing mechanically wrong. This does NOT mean the file is correct — " +
			"a plausible-but-wrong expected value, or an assertion stricter than the spec allows, is " +
			"invisible to a mechanical check. Do the spec-contradiction pass.\n\n")
	} else {
		fmt.Fprintf(&b, "### Seed findings: %d\n\n", len(rep.Seeds))
		for i, s := range rep.Seeds {
			fmt.Fprintf(&b, "**Seed %d — %s** (confidence: `%s`", i+1, s.Kind, s.Confidence)
			if s.Subtest != "" {
				b.WriteString(", " + s.Subtest)
			}
			b.WriteString(")\n\n```\n" + strings.TrimSpace(s.Evidence) + "\n```\n\n")
		}
	}

	if len(rep.Notes) > 0 {
		b.WriteString("### Other observations (not seeds — your judgement)\n\n")
		for _, n := range rep.Notes {
			b.WriteString("- " + strings.TrimSpace(n) + "\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
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
		n, _ := strconv.Atoi(m[2])
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
		// A struct render ("{Type:0 Value: Pos:0}") is what a scaffold stub
		// returns for a struct result — the fields are zero even though the
		// text isn't literally "0". Too ambiguous to call a real value.
		if strings.HasPrefix(v, "{") || strings.HasPrefix(v, "&{") {
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
