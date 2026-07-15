package verbs

import (
	"encoding/json"
	"fmt"
	"path/filepath"
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

	// When apiCheckTestFilename is owned by this bead, ensure the exit criterion
	// uses go test -c (not go build) and carries the grep guard for package-scope
	// var_ assertions. go build cannot compile test files, so type errors in
	// apiCheckTestFilename are silently missed. go test -c compiles all *_test.go
	// files and exits 0/1 without executing any tests.
	if hasNamedFile(bead.OutputFiles, apiCheckTestFilename) {
		apiPath := apiCheckTestFilePath(bead.OutputFiles)
		grepSuffix := " && grep -q '^var _' " + apiPath
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
			// Add grep guard for package-scope var_ assertion check.
			if strings.Contains(result, "go test") && !strings.Contains(result, "grep -q '^var _'") {
				result += grepSuffix
			}
			if result != c {
				bead.ExitCriteria[i] = result
				fixed = true
			}
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
			tf := testFileForName("Test", bead.OutputFiles)
			if tf == "" {
				continue
			}
			bead.ExitCriteria[i] = fmt.Sprintf("grep -q 'func Test' %s && %s", tf, c)
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
func fixFileBasedGoTest(criterion string) (string, bool) {
	if !strings.Contains(criterion, "go test") {
		return criterion, false
	}
	// The compile-only form (go test -c) is never file-based and may be part of
	// a compound criterion whose subsequent stages contain .go file paths (e.g.
	// grep arguments). Skip it entirely to avoid stripping those paths.
	if strings.Contains(criterion, "go test -c") {
		return criterion, false
	}
	parts := strings.Fields(criterion)
	var kept []string
	removed := false
	for _, p := range parts {
		// Drop any .go file path argument (not a flag, ends with .go).
		if !strings.HasPrefix(p, "-") && strings.HasSuffix(p, ".go") {
			removed = true
			continue
		}
		kept = append(kept, p)
	}
	if !removed {
		return criterion, false
	}
	// Add "." as package selector if no selector is present.
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
	return strings.Join(kept, " "), true
}

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

// fixBareGrepFile adds a filename argument to each `grep -q 'func Foo'`
// subcommand in criterion that is missing one. The criterion is split on " && "
// to process each subcommand independently; results are rejoined. Function
// names beginning with "Test" are directed to the appropriate *_test.go via
// testFileForName; other function names go to the first non-test .go file.
// Returns the fixed criterion and true if any change was made.
func fixBareGrepFile(criterion string, outputFiles []string) (string, bool) {
	const grepPrefix = "grep -q '"
	parts := strings.Split(criterion, " && ")
	fixed := false
	for i, part := range parts {
		if !strings.HasPrefix(part, grepPrefix) {
			continue
		}
		after := part[len(grepPrefix):]
		closeIdx := strings.Index(after, "'")
		if closeIdx < 0 {
			continue
		}
		pattern := after[:closeIdx]
		afterClose := strings.TrimLeft(after[closeIdx+1:], " \t")
		// A filename is absent when nothing follows the closing quote, or when
		// only a shell connective (&&, ||, |) follows.
		if afterClose != "" &&
			!strings.HasPrefix(afterClose, "&&") &&
			!strings.HasPrefix(afterClose, "||") &&
			!strings.HasPrefix(afterClose, "|") {
			continue // already has a filename argument
		}
		funcName := strings.TrimPrefix(pattern, "func ")
		var file string
		if strings.HasPrefix(funcName, "Test") {
			file = testFileForName(funcName, outputFiles)
		} else {
			file = firstSourceGoFile(outputFiles)
		}
		if file == "" {
			continue
		}
		parts[i] = grepPrefix + pattern + "' " + file
		fixed = true
	}
	if !fixed {
		return criterion, false
	}
	return strings.Join(parts, " && "), true
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

// --- RECONCILE tie-break: AUDIT repeating an already-disputed finding ---

// normalizeFindingText canonicalizes an AUDIT finding's issue text for
// verbatim-repeat comparison across rounds: lowercases, folds every quote
// variant (straight/smart, single/double) to one canonical character, and
// collapses whitespace runs. Folding single and double quotes together is
// deliberate, not just cosmetic-unicode handling: the observed real-world
// case (checkers-v8, project 98, round 1→2) had AUDIT re-quote the identical
// embedded shell command with ASCII "..." in one round and ASCII '...' in the
// next, which a smart-quote-only normalization would miss. This is deliberately
// crude (not semantic similarity) — the goal is only to catch a model
// re-emitting the same finding with cosmetic differences, not to judge
// whether two different findings are "similar."
func normalizeFindingText(s string) string {
	replacer := strings.NewReplacer(
		"'", "'", "‘", "'", "’", "'",
		"\"", "'", "“", "'", "”", "'",
	)
	s = replacer.Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

// findingsByBead parses a raw AUDIT_DECOMPOSITION critique (possibly
// markdown-fenced) into a map from bead_title to its normalized finding issue
// texts. Returns nil if the critique does not parse as AuditDecompositionOutput
// JSON — expected for 'redecompose' rows, whose critique_text is mechanical
// prose (forwardFileReferenceChecks output), not a model critique.
func findingsByBead(critiqueRaw string) map[string][]string {
	var out AuditDecompositionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(critiqueRaw)), &out); err != nil {
		return nil
	}
	byBead := map[string][]string{}
	for _, f := range out.Findings {
		byBead[f.BeadTitle] = append(byBead[f.BeadTitle], normalizeFindingText(f.Issue))
	}
	return byBead
}

// isRepeatDisagreement reports whether every current-round finding AUDIT
// raised about beadTitle was already raised — verbatim, per normalizeFindingText
// — in some earlier round where RECONCILE disagreed with it. When true, AUDIT
// has re-raised the same complaint without engaging with RECONCILE's prior
// rebuttal: the "no new argument" case that lets RECONCILE's disagreement
// stand (see ReconcileDecomposition.Commit's convergence comparator) instead
// of burning another round or escalating on an unchanged disagreement.
func isRepeatDisagreement(beadTitle string, currentFindings map[string][]string, history []debateRound) bool {
	current := currentFindings[beadTitle]
	if len(current) == 0 {
		return false
	}
	for _, round := range history {
		if round.Outcome == "redecompose" {
			continue
		}
		var recon ReconcileDecompositionOutput
		if err := json.Unmarshal([]byte(ollama.ExtractJSON(round.Reconciliation)), &recon); err != nil {
			continue
		}
		disagreed := false
		for _, r := range recon.Responses {
			if r.BeadTitle == beadTitle && r.Action == "disagree" {
				disagreed = true
				break
			}
		}
		if !disagreed {
			continue
		}
		prior := findingsByBead(round.CritiqueText)
		if prior == nil {
			continue
		}
		priorSet := map[string]bool{}
		for _, s := range prior[beadTitle] {
			priorSet[s] = true
		}
		allSeen := true
		for _, s := range current {
			if !priorSet[s] {
				allSeen = false
				break
			}
		}
		if allSeen {
			return true
		}
	}
	return false
}
