package verbs

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
)

// detectLang returns the programming language for a project. It first checks
// the filesystem (reliable after the layout bead has run), then falls back to
// scanning outputFiles — the union of all bead output_files entries — which
// works before any bead has executed and go.mod / requirements.txt / etc. do
// not yet exist.
func detectLang(folderPath string, outputFiles []string) string {
	if lang := guidance.Detect(folderPath); lang != "" {
		return lang
	}
	for _, f := range outputFiles {
		switch {
		case strings.HasSuffix(f, ".go"):
			return "go"
		case strings.HasSuffix(f, ".py"):
			return "python"
		case strings.HasSuffix(f, ".rs") || f == "Cargo.toml":
			return "rust"
		case strings.HasSuffix(f, ".ts") || strings.HasSuffix(f, ".tsx"):
			return "typescript"
		case strings.HasSuffix(f, ".js") || strings.HasSuffix(f, ".jsx"):
			return "javascript"
		}
	}
	return ""
}

// beadOutputFiles flattens a slice of beadState into the union of all
// output_files entries, for passing to detectLang.
func beadOutputFiles(beads []beadState) []string {
	var files []string
	for _, b := range beads {
		files = append(files, b.OutputFiles...)
	}
	return files
}

// injectMechanicalFindings parses the raw AUDIT model output, appends any
// mechanical structural violations the model missed, and re-serializes. If raw
// is not valid JSON or no mechanical findings exist, it is returned unchanged.
func injectMechanicalFindings(raw, folderPath string, beads []beadState) string {
	lang := detectLang(folderPath, beadOutputFiles(beads))
	if lang == "" {
		return raw
	}

	var mechanical []AuditFinding
	switch lang {
	case "go":
		mechanical = goMechanicalBeadChecks(beads)
	}
	if len(mechanical) == 0 {
		return raw
	}

	var out AuditDecompositionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return raw // leave for Validate to reject
	}
	out.Findings = append(out.Findings, mechanical...)
	out.OverallVerdict = "issues_found"

	merged, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return string(merged)
}

// goMechanicalBeadChecks returns structural findings for Go projects that do
// not require model judgment: any bead with a "go test" exit criterion must
// own a *_test.go file.
func goMechanicalBeadChecks(beads []beadState) []AuditFinding {
	var findings []AuditFinding
	for _, b := range beads {
		for _, criterion := range b.ExitCriteria {
			if strings.Contains(criterion, "go test") && !hasTestGoFile(b.OutputFiles) {
				findings = append(findings, AuditFinding{
					BeadTitle: b.Title,
					Issue: fmt.Sprintf(
						"exit criterion %q runs go test but output_files contains no *_test.go file — "+
							"add the test file to output_files (e.g. game_test.go for game.go); "+
							"without an owned test file the command exits 0 with \"no test files\" "+
							"and verifies nothing (vacuous pass). Do not downgrade the criterion to "+
							"go build ./... — that removes the test goal from the executor.",
						criterion),
					DesignDocReference: "N/A — structural",
				})
				break // one finding per bead for this rule
			}
		}
	}
	return findings
}

// forwardFileReferenceChecks returns a human-readable violation message for
// each bead whose full_text or exit_criteria reference a subdirectory asset
// path (e.g. "templates/index.html") that only a LATER bead creates — i.e.
// the bead cannot structurally pass no matter how many times it is executed,
// because the file it depends on won't exist until after it runs. Checked
// directly against DECOMPOSE_SPEC's own output before any bead row is
// written, so DecomposeSpec.Commit can reject and retry with feedback
// instead of wasting an execute→adjudicate cycle that can never succeed.
//
// Restricted to paths containing "/" (subdirectory assets like templates or
// static files) rather than bare filenames: a bare name like "main.go" is a
// common word that shows up incidentally in unrelated prose constantly,
// while a distinctive subdirectory path is rarely mentioned except as a real
// dependency. This trades recall (same-directory forward references go
// undetected) for near-zero false positives.
//
// Real-world case this catches: checkers-v6 bead "http-handlers" called
// template.ParseFiles("templates/index.html", "templates/board.html") but
// those files were owned by the "templates" bead, ordered after it — three
// full execute cycles were spent on nil-pointer panics before the actual
// problem (bead ordering, not execution capability) was found.
func forwardFileReferenceChecks(beads []ParsedBead) []string {
	var violations []string
	for i, b := range beads {
		owned := map[string]bool{}
		for _, o := range beads[:i+1] {
			for _, f := range o.OutputFiles {
				owned[f] = true
			}
		}
		for j := i + 1; j < len(beads); j++ {
			for _, laterFile := range beads[j].OutputFiles {
				if !strings.Contains(laterFile, "/") || owned[laterFile] {
					continue
				}
				if strings.Contains(b.FullText, laterFile) || containsSubstring(b.ExitCriteria, laterFile) {
					violations = append(violations, fmt.Sprintf(
						"bead %q references %q, which is only created by the later bead %q — "+
							"reorder the decomposition so %q precedes %q, or move %q's file creation into %q.",
						b.Title, laterFile, beads[j].Title, beads[j].Title, b.Title, laterFile, b.Title))
					break
				}
			}
		}
	}
	return violations
}

// containsSubstring reports whether needle appears in any element of haystack.
func containsSubstring(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// applyMechanicalBeadFixes corrects structural violations in a ParsedBead before
// it is written to the DB, so the problem never reaches AUDIT or RECONCILE.
// Returns true if any fix was applied (caller may want to log this).
func applyMechanicalBeadFixes(lang string, bead *ParsedBead) bool {
	if lang != "go" {
		return false
	}
	return goFixBeadSpec(bead)
}

// ApplyMechanicalBeadFixes re-runs applyMechanicalBeadFixes for a caller
// outside this package (rewind-bead) that needs to heal a bead spec's
// exit_criteria inherited from a prior revision — including ones that predate
// a fix landing here, like the checkers-try-1 bead-684 and othello-fixture
// incidents — rather than carrying forward whatever was already stored.
func ApplyMechanicalBeadFixes(folderPath string, bead *ParsedBead) bool {
	return applyMechanicalBeadFixes(detectLang(folderPath, bead.OutputFiles), bead)
}

// goFixBeadSpec fixes Go-specific structural violations in-place:
//
//   - If a bead owns apiCheckTestFilename, strengthen any "go build ./..." exit
//     criterion with a grep check that verifies package-level blank-identifier
//     assertions. go build passes whether assertions are at file scope or inside
//     a function body; grep -q '^var _' enforces the structural requirement.
//   - Rewrite file-based go test invocations (go test ./foo_test.go -run TestFoo)
//     to package form (go test -run TestFoo .) — file-based invocations compile
//     in isolation and cannot see other package files.
//   - For criteria that target a specific test function with -run TestFoo, prepend
//     grep -q 'func TestFoo' file_test.go so the criterion fails hard when the
//     test function has not been written (instead of silently exiting 0 with
//     "no tests to run").
//   - If a bead has a "go test" exit criterion but no *_test.go in output_files,
//     and the bead owns non-test .go files: add a derived *_test.go to output_files
//     so the executor knows it must write tests (preserves goal visibility).
//   - If the bead has no .go files at all (content-only bead, e.g. HTML templates):
//     downgrade those criteria to "go build ./..." — the bead cannot own tests.
func goFixBeadSpec(bead *ParsedBead) bool {
	fixed := false

	// When apiCheckTestFilename is owned by this bead, ensure any "go build"
	// exit criterion is upgraded to "go test -c" so type errors inside
	// apiCheckTestFilename get caught by compilation (go build cannot compile
	// test files at all, so it silently misses them). This does NOT grep the
	// file's content — see stripAPICheckFileContentChecks below for why.
	if hasNamedFile(bead.OutputFiles, apiCheckTestFilename) {
		for i, c := range bead.ExitCriteria {
			result := c
			// Upgrade any go build form to go test -c. Check longest-first to avoid
			// partial matches (e.g. "go build ." matching inside "go build ./...").
			for _, old := range []string{"go build ./...", "go build .", "go build"} {
				if strings.Contains(result, old) {
					result = strings.Replace(result, old, "go test -c -o /dev/null ./...", 1)
					break
				}
			}
			if result != c {
				bead.ExitCriteria[i] = result
				fixed = true
			}
		}
	}

	// apiCheckTestFilename's content is fully deterministic: writeAPICheckTest
	// generates it from the SURVEY manifest's exported symbols, both at
	// scaffold time (scaffoldGoProject) and after every declare_success
	// (regenerateAPICheckTest) — no bead ever hand-authors it, and the agent
	// has no lasting way to affect it since regenerateAPICheckTest overwrites
	// it the moment any bead succeeds. A bead's exit criteria must never gate
	// on its content: the checkers-try-1 bead-684 incident traced back to
	// exactly this — a "must be present in do_not_use_this_test.go, must be
	// absent from game.go" pair (the natural way to express "move this
	// assertion out of the impl file") that a naive bare-grep file-assignment
	// heuristic collapsed onto the same file, producing a criterion that could
	// never be satisfied. Strip any grep clause (positive or negated) that
	// targets this file's content unconditionally, regardless of which verb
	// wrote it (DECOMPOSE, RECONCILE, or ADJUDICATE's execute_revised have all
	// been observed generating one).
	for i, c := range bead.ExitCriteria {
		if result, ok := stripAPICheckFileContentChecks(c); ok {
			bead.ExitCriteria[i] = result
			fixed = true
		}
	}

	// Escape stray asterisks in grep patterns. A Go pointer-receiver or
	// pointer-return signature (`func (g *Game) FindFlips`, `var _ func()
	// *Game = ...`) contains a literal `*`, but grep's default (POSIX basic)
	// regex treats an unescaped `*` as "repeat the preceding character," not a
	// literal asterisk — so `grep -q 'func (g *Game) FindFlips' game.go` can
	// never match, regardless of whether the method exists. Confirmed live:
	// the othello fixture's beads 670-673 (game-flips, game-valid-moves,
	// game-place-stone, game-state) all carry this exact unescaped form from
	// their original decomposition, making them unsatisfiable the moment any
	// othello-try-N clone reaches them. Runs on every grep clause regardless
	// of whether it already has a filename attached (unlike fixBareGrepFile),
	// since DECOMPOSE/RECONCILE generate these fully-qualified from the start.
	for i, c := range bead.ExitCriteria {
		if result, ok := escapeStrayGrepAsterisks(c); ok {
			bead.ExitCriteria[i] = result
			fixed = true
		}
	}

	// Fix a `go test -run` flag whose quoted value lists several test functions
	// separated by spaces instead of the regex alternation `|`. `go test -run`
	// takes a Go regexp: `-run 'TestA TestB'` matches neither (there is no space
	// in any test name and the space is not an operator), so the criterion
	// silently runs zero tests and exits 0 — a vacuous pass that the grep guards
	// on the same line do not catch. Confirmed live: exprvm-web-v4 bead 135's
	// RECONCILE_DECOMPOSITION consolidated two single-function criteria into one
	// `-run 'TestHandleIndex TestHandleEval'`, which vacuously passed for three
	// execution attempts. Runs regardless of a leading `grep -q` (addGrepGuard
	// skips guarded criteria, so it would never reach this otherwise).
	for i, c := range bead.ExitCriteria {
		if result, ok := fixRunFlagSeparator(c); ok {
			bead.ExitCriteria[i] = result
			fixed = true
		}
	}

	// Rewrite file-based go test forms to package form.
	for i, c := range bead.ExitCriteria {
		if converted, ok := fixFileBasedGoTest(c); ok {
			bead.ExitCriteria[i] = converted
			fixed = true
		}
	}

	// Add filename arguments to bare `grep -q 'func Foo'` calls that lack one.
	// Must run before addGrepGuard, which skips criteria already starting with grep.
	for i, c := range bead.ExitCriteria {
		if result, ok := fixBareGrepFile(c, bead.OutputFiles); ok {
			bead.ExitCriteria[i] = result
			fixed = true
		}
	}

	// Add grep guard for specific -run TestFoo criteria when the bead owns a
	// test file. This makes the criterion exit 1 when the test function has not
	// been written, instead of silently exiting 0 ("no tests to run"). A bead
	// that owns only apiCheckTestFilename (no behavioral test file) still takes
	// this path and returns below — it already got its own treatment in the
	// api-check block above, and must not fall through to the "derive a missing
	// test file" logic near the bottom of this function, which is for beads
	// that actually need a behavioral test file.
	if hasTestGoFile(bead.OutputFiles) || hasNamedFile(bead.OutputFiles, apiCheckTestFilename) {
		for i, c := range bead.ExitCriteria {
			if guarded, ok := addGrepGuard(c, bead.OutputFiles); ok {
				bead.ExitCriteria[i] = guarded
				fixed = true
			}
		}
		// Second pass: for go test criteria still lacking a -run flag after the
		// first pass (addGrepGuard was a no-op because there was no test name to
		// extract), add a broad vacuous-pass guard. This catches the case where
		// DECOMPOSE emits "go test -v ." without naming a specific test function —
		// the guard ensures the criterion exits 1 when no tests were written rather
		// than silently passing with "no tests to run". The DECOMPOSE prompt now
		// requires -run TestFoo for Go beads; this is defense-in-depth.
		for i, c := range bead.ExitCriteria {
			if !strings.Contains(c, "go test") || strings.HasPrefix(c, "grep -q") {
				continue
			}
			if extractRunTestName(c) != "" {
				continue // already has -run; guard applied in first pass
			}
			// testFileForName's fallback (used for a generic "Test" name that
			// never substring-matches a filename stem) always resolves to the
			// bead's first test file — if the bead owns more than one and the
			// missing test happens to land in a later one, that file's grep
			// guard would wrongly vacuous-pass. grep natively checks multiple
			// files and reports a match if any contains the pattern, so grep
			// every owned test file instead of picking just one.
			tfs := allTestFiles(bead.OutputFiles)
			if len(tfs) == 0 {
				continue
			}
			bead.ExitCriteria[i] = fmt.Sprintf("grep -q 'func Test' %s && %s", strings.Join(tfs, " "), c)
			fixed = true
		}
		return fixed // owns a test file — no further structural fix needed
	}

	hasGoTestCriterion := false
	for _, c := range bead.ExitCriteria {
		if strings.Contains(c, "go test") {
			hasGoTestCriterion = true
			break
		}
	}
	if !hasGoTestCriterion {
		return fixed
	}

	var goFiles []string
	for _, f := range bead.OutputFiles {
		if strings.HasSuffix(f, ".go") && !strings.HasSuffix(f, "_test.go") {
			goFiles = append(goFiles, f)
		}
	}

	if len(goFiles) == 0 {
		// Content-only bead (no .go files). Downgrading is the correct fallback.
		for i, c := range bead.ExitCriteria {
			if strings.Contains(c, "go test") {
				bead.ExitCriteria[i] = "go build ./..."
			}
		}
		return true
	}

	// Bead owns .go files: add the derived test file instead of downgrading.
	bead.OutputFiles = append(bead.OutputFiles, deriveTestFileName(bead.ExitCriteria, goFiles))
	// Run guard pass now that the test file is in output_files.
	for i, c := range bead.ExitCriteria {
		if guarded, ok := addGrepGuard(c, bead.OutputFiles); ok {
			bead.ExitCriteria[i] = guarded
			fixed = true
		}
	}
	return true
}

// fixFileBasedGoTest detects `go test ./foo_test.go [-run TestFoo]` and rewrites
// to package form `go test [-run TestFoo] .`. Returns the rewritten criterion and
// true if a rewrite occurred.
//
// Operates ONLY on the `&&`-separated clause that contains `go test`, never the
// whole criterion — a leading `grep -q 'func TestX' foo_test.go` guard also
// contains a `.go` token, and an earlier version of this function stripped it,
// leaving a bare `grep -q 'func TestX'` that reads stdin and always fails. That
// was masked by `fixBareGrepFile` re-adding a filename on the next pass (and
// could re-point the guard at the wrong file when a bead owns >1 test file).
func fixFileBasedGoTest(criterion string) (string, bool) {
	if !strings.Contains(criterion, "go test") {
		return criterion, false
	}
	clauses := strings.Split(criterion, "&&")
	changed := false
	for ci, clause := range clauses {
		if !strings.Contains(clause, "go test") || strings.Contains(clause, "go test -c") {
			// go test -c is a compile-only form, never file-based.
			continue
		}
		parts := strings.Fields(clause)
		var kept []string
		removed := false
		for _, p := range parts {
			// Drop a .go file path argument (not a flag, ends with .go).
			if !strings.HasPrefix(p, "-") && strings.HasSuffix(p, ".go") {
				removed = true
				continue
			}
			kept = append(kept, p)
		}
		if !removed {
			continue
		}
		hasSel := false
		for _, p := range kept {
			if p == "." || p == "./..." || (strings.HasPrefix(p, "./") && !strings.HasSuffix(p, ".go")) {
				hasSel = true
				break
			}
		}
		if !hasSel {
			kept = append(kept, ".")
		}
		// Preserve the original clause's surrounding spacing around " && ".
		lead, trail := leadingSpace(clause), trailingSpace(clause)
		clauses[ci] = lead + strings.Join(kept, " ") + trail
		changed = true
	}
	if !changed {
		return criterion, false
	}
	return strings.Join(clauses, "&&"), true
}

func leadingSpace(s string) string  { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }
func trailingSpace(s string) string { return s[len(strings.TrimRight(s, " \t")):] }

// addGrepGuard prepends `grep -q 'func TestFoo' file_test.go && ` to a go test
// criterion that targets a single simple test function name via -run. This makes
// the criterion exit 1 when the function has not been written rather than
// silently exiting 0 with "no tests to run". Returns the guarded criterion and
// true if a guard was added.
func addGrepGuard(criterion string, outputFiles []string) (string, bool) {
	if !strings.Contains(criterion, "go test") {
		return criterion, false
	}
	if strings.HasPrefix(criterion, "grep -q") {
		return criterion, false // already guarded
	}
	testName := extractRunTestName(criterion)
	if testName == "" || !isSimpleTestName(testName) {
		return criterion, false
	}
	tf := testFileForName(testName, outputFiles)
	if tf == "" {
		return criterion, false
	}
	return fmt.Sprintf("grep -q 'func %s' %s && %s", testName, tf, criterion), true
}

// allTestFiles returns every *_test.go file in outputFiles, excluding the
// mechanically-owned apiCheckTestFilename (which is never where a model
// writes behavioral test functions).
func allTestFiles(outputFiles []string) []string {
	var out []string
	for _, f := range outputFiles {
		if strings.HasSuffix(f, "_test.go") && filepath.Base(f) != apiCheckTestFilename {
			out = append(out, f)
		}
	}
	return out
}

// testFileForName returns the *_test.go file in outputFiles most likely to
// contain testName, by checking whether the file's base (without _test.go)
// appears as a substring of the lowercased test name. Falls back to the first
// *_test.go that is not apiCheckTestFilename.
func testFileForName(testName string, outputFiles []string) string {
	lower := strings.ToLower(testName)
	for _, f := range outputFiles {
		if !strings.HasSuffix(f, "_test.go") || filepath.Base(f) == apiCheckTestFilename {
			continue
		}
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(f), "_test.go"))
		if strings.Contains(lower, base) {
			return f
		}
	}
	for _, f := range outputFiles {
		if strings.HasSuffix(f, "_test.go") && filepath.Base(f) != apiCheckTestFilename {
			return f
		}
	}
	return ""
}

// isSimpleTestName returns true when name contains only letters, digits, and
// underscores — i.e. it is a plain function name rather than a -run regexp.
func isSimpleTestName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

// apiCheckTestFilePath returns the path of apiCheckTestFilename as listed in
// output_files, preserving any subdirectory prefix, or the bare filename as fallback.
func apiCheckTestFilePath(files []string) string {
	for _, f := range files {
		if filepath.Base(f) == apiCheckTestFilename {
			return f
		}
	}
	return apiCheckTestFilename
}

// deriveTestFileName picks the *_test.go filename to add when a bead has go
// test exit criteria but no test file. It tries to match a .go file whose base
// name appears as a substring of the test name from any -run= flag; falls back
// to the first .go file's _test.go.
func deriveTestFileName(exitCriteria, goFiles []string) string {
	for _, c := range exitCriteria {
		if !strings.Contains(c, "go test") {
			continue
		}
		testName := strings.ToLower(extractRunTestName(c))
		if testName == "" {
			continue
		}
		for _, gf := range goFiles {
			base := strings.ToLower(strings.TrimSuffix(filepath.Base(gf), ".go"))
			if strings.Contains(testName, base) {
				return filepath.Join(filepath.Dir(gf), base+"_test.go")
			}
		}
	}
	first := goFiles[0]
	base := strings.TrimSuffix(filepath.Base(first), ".go")
	return filepath.Join(filepath.Dir(first), base+"_test.go")
}

// runFlagQuotedValueRe captures a `-run` flag with a quoted value. Group 1 is
// the inner value between the quotes. Go's regexp (RE2) has no backreferences,
// so the closing quote is matched as "any quote char" rather than "the same
// one"; a mismatched pair is malformed input and harmless here. Both `-run 'x'`
// and `-run='x'` forms.
var runFlagQuotedValueRe = regexp.MustCompile(`-run=?\s*['"]([^'"]*)['"]`)

// allPlainTestNamesRe matches a string that is two or more plain top-level Go
// test-function names ("TestFoo") separated by single spaces — the exact shape
// a model produces when it means an alternation but writes spaces.
var allPlainTestNamesRe = regexp.MustCompile(`^Test\w+( Test\w+)+$`)

// fixRunFlagSeparator rewrites `go test -run 'TestA TestB'` to
// `go test -run 'TestA|TestB'`. It only touches a quoted value whose every
// space-separated token is a plain `TestXxx` name, so a legitimate regexp with
// an intentional space (`-run 'TestParse/two words'`) is left alone. Returns
// the rewritten criterion and true if a rewrite occurred.
func fixRunFlagSeparator(criterion string) (string, bool) {
	if !strings.Contains(criterion, "go test") {
		return criterion, false
	}
	m := runFlagQuotedValueRe.FindStringSubmatchIndex(criterion)
	if m == nil {
		return criterion, false
	}
	valStart, valEnd := m[2], m[3] // group 1 = inner value
	value := criterion[valStart:valEnd]
	if !strings.Contains(value, " ") || !allPlainTestNamesRe.MatchString(value) {
		return criterion, false
	}
	fixedValue := strings.ReplaceAll(value, " ", "|")
	return criterion[:valStart] + fixedValue + criterion[valEnd:], true
}

// extractRunTestName returns the value of the -run flag in a go test command,
// or "" if no -run flag is present. Handles both -run=TestFoo and -run TestFoo.
func extractRunTestName(criterion string) string {
	// Equals form: -run=TestFoo
	if idx := strings.Index(criterion, "-run="); idx >= 0 {
		rest := criterion[idx+len("-run="):]
		if i := strings.IndexAny(rest, " \t"); i >= 0 {
			return rest[:i]
		}
		return rest
	}
	// Space form: -run TestFoo
	if idx := strings.Index(criterion, "-run "); idx >= 0 {
		rest := strings.TrimLeft(criterion[idx+len("-run "):], " \t")
		if i := strings.IndexAny(rest, " \t"); i >= 0 {
			return rest[:i]
		}
		return rest
	}
	return ""
}

// hasTestGoFile reports whether files contains a _test.go file that a bead
// should go through REFINE_TESTS for. apiCheckTestFilename is excluded: it's
// mechanically regenerated from the SURVEY_SPEC manifest (see
// writeAPICheckTest) and never holds hand-written behavioral tests, so a bead
// whose only _test.go output is that file must skip straight to EXECUTE_BEAD.
func hasTestGoFile(files []string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") && filepath.Base(f) != apiCheckTestFilename {
			return true
		}
	}
	return false
}

func hasNamedFile(files []string, name string) bool {
	for _, f := range files {
		if filepath.Base(f) == name {
			return true
		}
	}
	return false
}

// parseGrepClause parses an " && "-split subcommand that is a `grep -q 'PATTERN'`
// (optionally negated with a leading "! "), whether or not it already has a
// file argument. file is "" for a bare clause. ok is false if the subcommand is
// not a grep -q at all.
func parseGrepClause(part string) (pattern string, negated bool, file string, ok bool) {
	const grepPrefix = "grep -q '"
	part = strings.TrimSpace(part)
	negated = strings.HasPrefix(part, "! ")
	body := strings.TrimPrefix(part, "! ")
	if !strings.HasPrefix(body, grepPrefix) {
		return "", false, "", false
	}
	after := body[len(grepPrefix):]
	closeIdx := strings.Index(after, "'")
	if closeIdx < 0 {
		return "", false, "", false
	}
	pattern = after[:closeIdx]
	rest := strings.TrimSpace(after[closeIdx+1:])
	// A shell connective means no file; otherwise the first token is the file.
	if rest != "" && !strings.HasPrefix(rest, "&&") && !strings.HasPrefix(rest, "||") && !strings.HasPrefix(rest, "|") {
		file = strings.Fields(rest)[0]
	}
	return pattern, negated, file, true
}

// fixBareGrepFile adds a filename argument to each `grep -q 'func Foo'`
// subcommand in criterion that is missing one — including a negated
// `! grep -q '...'` form (e.g. ADJUDICATE generating a "this assertion must
// no longer be present" check). Without an explicit file, `grep -q` reads an
// empty stdin, always finds no match, and the leading `!` then makes the
// subcommand vacuously exit 0 regardless of the real file content — the same
// "shell construct that cannot fail" class of bug that motivated
// execcheck.VerifyExitCriteria, except here it's baked into the criterion
// text itself, so re-running it mechanically doesn't help.
//
// Before assigning files, it also collapses a bare positive/negated pair that
// share the identical pattern text — e.g. `grep -q 'PATTERN' && ! grep -q
// 'PATTERN'` — down to just the negated clause. That pair is the natural way
// to express "this assertion must move out of the impl file" (present
// somewhere else, absent here); with no distinguishing information, naively
// assigning a file to both clauses independently sends them to the same file
// and produces a criterion that can never be satisfied (checkers-try-1 bead
// 684: both clauses resolved to game.go, requiring the identical line to be
// simultaneously present and absent in it). The positive half is dropped
// rather than routed elsewhere because the only place these compile-time
// assertions could legitimately be checked for presence is
// apiCheckTestFilename, whose content is deterministically generated and must
// never be exit-criteria-checked at all (see stripAPICheckFileContentChecks).
//
// The criterion is split on " && " to process each subcommand independently;
// results are rejoined. Function names beginning with "Test" are directed to
// the appropriate *_test.go via testFileForName; other function names (and
// non-func patterns like `var _ ...`) go to the first non-test .go file.
// Returns the fixed criterion and true if any change was made.
func fixBareGrepFile(criterion string, outputFiles []string) (string, bool) {
	parts := strings.Split(criterion, " && ")
	fixed := false

	resolveFile := func(pattern, existing string) string {
		if existing != "" {
			return existing
		}
		funcName := strings.TrimPrefix(pattern, "func ")
		if strings.HasPrefix(funcName, "Test") {
			return testFileForName(funcName, outputFiles)
		}
		return firstSourceGoFile(outputFiles)
	}

	// Pass 1: give every bare grep clause a file argument.
	for i, part := range parts {
		pattern, negated, file, ok := parseGrepClause(part)
		if !ok || file != "" {
			continue
		}
		resolved := resolveFile(pattern, file)
		if resolved == "" {
			continue
		}
		newBody := "grep -q '" + pattern + "' " + resolved
		if negated {
			newBody = "! " + newBody
		}
		parts[i] = newBody
		fixed = true
	}

	// Pass 2: collapse a positive/negated pair that share the identical pattern
	// AND the same file — "PATTERN present in F and absent from F" is always
	// unsatisfiable. The natural way a model expresses "move this assertion out
	// of F" (checkers-try-1 bead 684). Keep the negated half. This must compare
	// resolved files, not just bare-ness — the old version only caught bare/bare
	// pairs and silently relied on fixFileBasedGoTest incidentally stripping the
	// positive clause's file to make it bare.
	type key struct{ pattern, file string }
	negatedKeys := map[key]bool{}
	for _, part := range parts {
		if pattern, negated, file, ok := parseGrepClause(part); ok && negated {
			negatedKeys[key{pattern, file}] = true
		}
	}
	deduped := parts[:0]
	for _, part := range parts {
		if pattern, negated, file, ok := parseGrepClause(part); ok && !negated && negatedKeys[key{pattern, file}] {
			fixed = true
			continue
		}
		deduped = append(deduped, part)
	}
	parts = deduped

	if !fixed {
		return criterion, false
	}
	return strings.Join(parts, " && "), true
}

// stripAPICheckFileContentChecks removes any grep clause (positive or
// negated, bare or already file-qualified) that targets apiCheckTestFilename.
// That file's content is fully deterministic — writeAPICheckTest generates it
// from the SURVEY manifest's exported symbols, both at scaffold time
// (scaffoldGoProject) and after every declare_success
// (regenerateAPICheckTest) — so no bead's exit criteria should ever gate on
// it: doing so either duplicates guaranteed work, or (since
// regenerateAPICheckTest overwrites the file the moment any bead succeeds)
// checks content the agent has no lasting ability to affect. Leaves the
// criterion unchanged if stripping would remove every clause, since an empty
// exit criterion is a vacuous pass.
func stripAPICheckFileContentChecks(criterion string) (string, bool) {
	parts := strings.Split(criterion, " && ")
	kept := parts[:0]
	changed := false
	for _, part := range parts {
		body := strings.TrimPrefix(part, "! ")
		if strings.HasPrefix(body, "grep -q '") && strings.Contains(body, apiCheckTestFilename) {
			changed = true
			continue
		}
		kept = append(kept, part)
	}
	if !changed || len(kept) == 0 {
		return criterion, false
	}
	return strings.Join(kept, " && "), true
}

// grepPatternRe matches a `grep -q '...'` pattern body regardless of what
// precedes it (bare, negated, or already carrying a trailing filename) —
// unlike parseGrepClause, which is line-oriented rather than regex. Assumes the
// pattern itself never contains a literal single quote, consistent with
// every other grep-pattern helper in this file.
var grepPatternRe = regexp.MustCompile(`grep -q '([^']*)'`)

// escapeStrayGrepAsterisks escapes any unescaped `*` inside a grep -q
// pattern. grep's default POSIX basic regex treats a bare `*` as "repeat the
// preceding character," not a literal asterisk, so a pattern built directly
// from a Go signature — `func (g *Game) FindFlips`, `var _ func() *Game =
// NewGame` — silently never matches: a positive check can never pass, and a
// negated check vacuously always passes, regardless of the real file
// content. These patterns are always meant as literal substring checks
// against generated Go source, never real wildcard matching, so escaping
// every bare asterisk is always correct here.
func escapeStrayGrepAsterisks(criterion string) (string, bool) {
	changed := false
	result := grepPatternRe.ReplaceAllStringFunc(criterion, func(match string) string {
		sub := grepPatternRe.FindStringSubmatch(match)
		escaped := escapeBareAsterisks(sub[1])
		if escaped != sub[1] {
			changed = true
		}
		return "grep -q '" + escaped + "'"
	})
	if !changed {
		return criterion, false
	}
	return result, true
}

// escapeBareAsterisks inserts a backslash before every `*` not already
// preceded by one, leaving already-escaped `\*` sequences untouched.
func escapeBareAsterisks(pattern string) string {
	var b strings.Builder
	i := 0
	for i < len(pattern) {
		c := pattern[i]
		if c == '\\' && i+1 < len(pattern) {
			b.WriteByte(c)
			b.WriteByte(pattern[i+1])
			i += 2
			continue
		}
		if c == '*' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// firstSourceGoFile returns the first non-test .go file in outputFiles.
func firstSourceGoFile(outputFiles []string) string {
	for _, f := range outputFiles {
		if strings.HasSuffix(f, ".go") && !strings.HasSuffix(f, "_test.go") {
			return f
		}
	}
	return ""
}

var (
	decompositionPinStartRe = regexp.MustCompile(`^-\s+\*\*Pin\b`)
	decompositionPinBeadRe  = regexp.MustCompile("`([A-Za-z0-9_-]+)`\\s+bead\\b")
	topLevelListItemRe      = regexp.MustCompile(`^(-\s|\d+\.\s)`)
)

// extractMarkdownSection returns the body of a "## <heading>" section (up to
// but not including the next "## " heading, or end of file), or "" if the
// heading is not present. Line-oriented, matching the "## " convention used
// throughout docs/design_doc_guide.md and docs/design_doc_template.md.
func extractMarkdownSection(content, heading string) string {
	lines := strings.Split(content, "\n")
	target := "## " + heading
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == target {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return ""
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimRight(lines[i], " \t"), "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// extractDecompositionNotesPins parses every "- **Pin ... `<bead>` bead**: ..."
// bullet out of the design doc's "## Decomposition Notes" section, keyed by
// bead title (lowercased). A bullet may span multiple soft-wrapped
// continuation lines (2-space indented, markdown convention); capture ends
// at the next top-level list item (another "- " bullet or a numbered "N. "
// item) or a blank line, matching every real Decomposition Notes section in
// this repo's design docs (docs/fixture-design-docs/exprvm-web.md,
// docs/design-docs/connect-four-v1-design-doc.md).
func extractDecompositionNotesPins(designDoc string) map[string]string {
	section := extractMarkdownSection(designDoc, "Decomposition Notes")
	if section == "" {
		return nil
	}
	pins := map[string]string{}
	var current []string
	flush := func() {
		if len(current) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(current, "\n"))
		if m := decompositionPinBeadRe.FindStringSubmatch(text); m != nil {
			pins[strings.ToLower(m[1])] = text
		}
		current = nil
	}
	for _, line := range strings.Split(section, "\n") {
		switch {
		case decompositionPinStartRe.MatchString(line):
			flush()
			current = []string{line}
		case current == nil:
			// Not inside a pin bullet — ignore until the next "- **Pin" line.
		case strings.TrimSpace(line) == "" || topLevelListItemRe.MatchString(line):
			flush()
		default:
			current = append(current, line)
		}
	}
	flush()
	if len(pins) == 0 {
		return nil
	}
	return pins
}

// injectDecompositionNotesPin mechanically guarantees a bead's full_text
// contains the exact text of any Decomposition Notes "Pin ... to the X
// bead" bullet naming it, regardless of whether DECOMPOSE/RECONCILE's own
// transcription preserved it faithfully. The prompt asks the model to quote
// pins verbatim, but nothing previously enforced it — this closes that gap
// mechanically, the same way applyMechanicalBeadFixes closes prompt gaps for
// exit criteria.
//
// Motivating incident (exprvm-web-v2 bead 117, 2026-08-30): the design doc's
// precise three-way Bytecode-per-error-type rule (empty on a Compile error,
// populated on a Run error or success) got compressed by DECOMPOSE into an
// ambiguous "(if compiled)" clause. REFINE_TESTS_CRITIQUE and
// REFINE_TESTS_JUDGE never read the design doc at all — only the bead
// spec — so JUDGE endorsed a finding that contradicted the doc's actual,
// unambiguous rule. A verbatim pin for this bead would have given JUDGE the
// exact rule directly, regardless of how DECOMPOSE's own prose compressed
// it.
//
// pinAppendixHeader is the ratchet-authored sentence that prefixes an injected
// pin block. It is distinctive enough to locate a previously-injected appendix
// — including a copy a DECOMPOSE/RECONCILE model reproduced (often reflowed, or
// duplicated) in its own regenerated full_text — so injection can normalize to
// exactly one canonical copy.
const pinAppendixHeader = "Design doc Decomposition Notes pin for this bead " +
	"(verbatim, must be followed exactly, not re-derived or paraphrased):\n"

// injectDecompositionNotesPin normalizes bead.FullText so that, when a
// Decomposition Notes "Pin ... to the X bead" bullet names it, the text ends
// with exactly one canonical pin appendix — regardless of how many partial,
// reflowed, or duplicated copies a model left behind on a prior round.
//
// It strips everything from the first pinAppendixHeader onward, then appends
// one canonical block. This is deliberately not a presence check: exprvm-web-v4
// bead 135 accumulated three near-identical copies across DECOMPOSE + two
// RECONCILE rounds because the model reproduced the appendix in its output with
// a one-line wrap difference each time, which an exact-substring guard neither
// detected nor collapsed. Returns whether FullText changed.
func injectDecompositionNotesPin(bead *ParsedBead, pins map[string]string) bool {
	pin, ok := pins[strings.ToLower(bead.Title)]
	if !ok {
		return false
	}
	base := bead.FullText
	if idx := strings.Index(base, pinAppendixHeader); idx >= 0 {
		base = strings.TrimRight(base[:idx], " \t\n")
	}
	canonical := base + "\n\n" + pinAppendixHeader + pin
	if canonical == bead.FullText {
		return false
	}
	bead.FullText = canonical
	return true
}

// --- exit-criteria ↔ prose ↔ output_files consistency (audit 2026-08-30) ---
//
// applyMechanicalBeadFixes repairs the syntactically-broken subset of these
// invariants after the fact. The two functions below are *source-side* checks:
// they return human-readable violations so DECOMPOSE / RECONCILE / ADJUDICATE
// can reject a bead they just emitted and retry, rather than shipping an
// inconsistent bead downstream where REFINE_TESTS silently works around it (or
// doesn't). Structural only — every check is string/regex-decidable, no model
// judgment. Confirmed live: exprvm-web-v4/v5 bead 135/144, where RECONCILE
// consolidated two exit criteria into one `-run 'TestA TestB'` (zero tests) and
// invented a required function `TestHandlerRuntime` with no prose to support it.

// runFlagValueRe captures a `-run` flag's value: an unquoted token, or the
// contents of a single/double-quoted string. Group 1 = unquoted, group 2/3 =
// quoted contents.
var runFlagValueRe = regexp.MustCompile(`-run(?:=|\s+)(?:'([^']*)'|"([^"]*)"|(\S+))`)

// extractRunNames returns every top-level test-function name referenced by any
// `-run` flag in criterion. It splits an alternation (`A|B`) into individual
// names, strips anchor fragments (`^`, `$`), and takes only the top-level
// function name from a subtest path (`TestFoo/case` -> "TestFoo"). It does NOT
// split on spaces: a `-run` value with a space is a separate defect that
// checkBeadCriteriaConsistency's I6-residual check reports, and a legitimate
// subtest path can contain a space (`-run 'TestFoo/two words'`). A segment
// containing a space therefore yields no name here.
func extractRunNames(criterion string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range runFlagValueRe.FindAllStringSubmatch(criterion, -1) {
		val := m[1] + m[2] + m[3] // exactly one group is non-empty
		for _, seg := range strings.Split(val, "|") {
			seg = strings.Trim(seg, "^$()")
			if i := strings.IndexByte(seg, '/'); i >= 0 {
				seg = seg[:i]
			}
			seg = strings.TrimSpace(seg)
			if seg == "" || strings.ContainsAny(seg, " \t") {
				continue
			}
			if !seen[seg] {
				seen[seg] = true
				out = append(out, seg)
			}
		}
	}
	return out
}

// grepGuardFileRe captures a `grep -q 'func TestX' <file>` guard's function name
// (group 1) and file argument (group 2). The file is optional (a bare guard has
// none — fixBareGrepFile handles that separately).
var grepGuardFileRe = regexp.MustCompile(`grep\s+-q\s+['"]func\s+(Test\w+)['"](?:\s+(\S+\.go))?`)

// checkBeadCriteriaConsistency returns violations for beads whose exit_criteria
// are internally inconsistent or inconsistent with output_files, in ways a
// model retry can fix.
func checkBeadCriteriaConsistency(beads []ParsedBead) []string {
	var violations []string
	for _, b := range beads {
		ownedTestFiles := map[string]bool{}
		for _, f := range b.OutputFiles {
			if strings.HasSuffix(f, "_test.go") {
				ownedTestFiles[filepath.Base(f)] = true
			}
		}
		guardedFuncs := map[string]bool{}
		for _, name := range extractRequiredTestFuncs(b.ExitCriteria) {
			guardedFuncs[name] = true
		}

		for _, c := range b.ExitCriteria {
			// I7: every grep -q 'func TestX' <file> names a *_test.go this bead owns.
			for _, m := range grepGuardFileRe.FindAllStringSubmatch(c, -1) {
				file := m[2]
				if file == "" {
					continue // bare guard — fixBareGrepFile's problem
				}
				if strings.HasSuffix(file, "_test.go") && !ownedTestFiles[filepath.Base(file)] {
					violations = append(violations, fmt.Sprintf(
						"bead %q: exit criterion greps %q for a test function, but that file is not in this bead's output_files %v",
						b.Title, file, b.OutputFiles))
				}
			}
			// I6-residual: a -run value fixRunFlagSeparator could not normalize
			// (a space remained because a token was not a plain TestXxx name).
			for _, m := range runFlagValueRe.FindAllStringSubmatch(c, -1) {
				val := m[1] + m[2] + m[3]
				if strings.Contains(val, " ") {
					violations = append(violations, fmt.Sprintf(
						"bead %q: exit criterion has `-run %q` — `go test -run` takes a Go regexp; "+
							"separate multiple test names with `|`, not a space", b.Title, val))
				}
			}
			// I18: every -run name is established as a real required function —
			// by a grep guard (anywhere in this bead's criteria) or by the prose.
			for _, name := range extractRunNames(c) {
				if guardedFuncs[name] || strings.Contains(b.FullText, name) {
					continue
				}
				violations = append(violations, fmt.Sprintf(
					"bead %q: exit criterion runs `-run %s` but nothing establishes %s as a required test "+
						"function for this bead — no `grep -q 'func %s'` guard and no mention in the bead prose",
					b.Title, name, name, name))
			}
		}
	}
	return violations
}

// checkAddedRequiredFuncsHaveProse returns a violation for every test function
// that `after`'s exit_criteria newly require (versus `before`) but that
// `after.FullText` does not describe. This is the precise shape of the
// exprvm-web bead-135 orphan: RECONCILE replaced a curl check with a
// `grep -q 'func TestHandlerRuntime'` criterion and never added prose telling
// REFINE_TESTS what that function should test, so the model dropped it under
// turn pressure. Differential, so it never fires on a bead whose required
// functions are stable from DECOMPOSE.
func checkAddedRequiredFuncsHaveProse(before, after ParsedBead) []string {
	had := map[string]bool{}
	for _, n := range extractRequiredTestFuncs(before.ExitCriteria) {
		had[n] = true
	}
	var violations []string
	for _, n := range extractRequiredTestFuncs(after.ExitCriteria) {
		if had[n] || strings.Contains(after.FullText, n) {
			continue
		}
		violations = append(violations, fmt.Sprintf(
			"bead %q: your change adds a new required test function %s to the exit criteria, but the bead "+
				"prose does not describe it — add a sentence stating what %s must test, or do not add the criterion",
			after.Title, n, n))
	}
	return violations
}

// clonePB returns a deep copy of b (the two string slices are the only
// reference fields), so a caller can run applyMechanicalBeadFixes on the copy
// without mutating the model's original output.
func clonePB(b ParsedBead) ParsedBead {
	b.OutputFiles = append([]string(nil), b.OutputFiles...)
	b.ExitCriteria = append([]string(nil), b.ExitCriteria...)
	return b
}

// beadConsistencyViolations is the single source-side gate DECOMPOSE / RECONCILE
// / ADJUDICATE call before committing a bead they just emitted. It runs the
// mechanical-repair pass on copies of the proposed beads, then returns every
// consistency violation that survives repair — so the verb can reject-and-retry
// rather than shipping an inconsistent bead. `before` maps bead title to its
// prior stored spec, for the differential added-required-function check; pass
// nil when there is no prior state (DECOMPOSE).
func beadConsistencyViolations(lang string, proposed []ParsedBead, before map[string]ParsedBead) []string {
	repaired := make([]ParsedBead, len(proposed))
	for i, b := range proposed {
		repaired[i] = clonePB(b)
		applyMechanicalBeadFixes(lang, &repaired[i])
	}
	var v []string
	v = append(v, forwardFileReferenceChecks(repaired)...)
	v = append(v, checkBeadCriteriaConsistency(repaired)...)
	for _, r := range repaired {
		if b, ok := before[r.Title]; ok {
			v = append(v, checkAddedRequiredFuncsHaveProse(b, r)...)
		}
	}
	return v
}
