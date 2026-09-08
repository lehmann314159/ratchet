// checkdesigndoc runs report-style checks over a ratchet design doc. Nothing
// it prints is a pass/fail oracle — every finding is a site a human should
// look at, and a clean report is not a guarantee.
//
// Two checks, selected with --checks (default "all"):
//
//   - pins: lists every worked-example block in "Domain-Specific Test
//     Scenarios" next to every "Pin ..." bullet in "Decomposition Notes", so a
//     worked example added without a matching pin is visible at a glance
//     instead of relying on memory to catch it. Motivated by connect-four-v5
//     bead 71 (2026-08-28): a worked example was added and the matching pin was
//     simply forgotten.
//
//   - bead-size: flags a "## Decomposition Notes" bead that is both large (owns
//     >=4 functions) and heavily integrated (>=3 prior-bead dependencies or >=3
//     Cross-Bead Contracts) — the surface area the EXECUTE model spirals on.
//     Motivated by the lsystem `grammar` monolith (escalated five from-scratch
//     runs; hand-split into three expr-sized sub-beads that all ran clean). See
//     beadsize.go and docs/decomposition-framework-plan.md. Report-only;
//     cleared by a "sizing rationale:" note in the bullet.
//
//   - ambiguity: a first-pass mechanical scan for the design-doc ambiguity
//     classes that docs/design_doc_ambiguity_checklist.md marks as
//     mechanically detectable (1 directional/geometric, 2 spec-derived
//     arithmetic, 6 concrete-literals-over-symbolic-refs, 7 package/entry-point
//     declaration, 17 self-describing category phrase). Tuned to over-flag: a
//     flagged site still needs a judgment call, and the judgment pass
//     (.claude/skills/design-doc-ambiguity-check.md) remains the real check for
//     every class except 17 (which judgment review provably cannot catch — see
//     the checklist).
//
// Usage:
//
//	go run ./cmd/checkdesigndoc --doc path/to/design-doc.md
//	go run ./cmd/checkdesigndoc --doc path/to/design-doc.md --checks=ambiguity
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
)

func main() {
	docPath := flag.String("doc", "", "path to the design doc markdown file (required)")
	checks := flag.String("checks", "all", "comma-separated checks to run: pins, ambiguity, construction-form, bead-size, all")
	flag.Parse()
	if *docPath == "" {
		fmt.Fprintln(os.Stderr, "checkdesigndoc: --doc is required")
		os.Exit(2)
	}

	data, err := os.ReadFile(*docPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "checkdesigndoc: read %s: %v\n", *docPath, err)
		os.Exit(1)
	}
	content := string(data)

	runPins, runAmbiguity, runConstructionForm, runBeadSize := false, false, false, false
	for _, c := range strings.Split(*checks, ",") {
		switch strings.TrimSpace(c) {
		case "all":
			runPins, runAmbiguity, runConstructionForm, runBeadSize = true, true, true, true
		case "pins":
			runPins = true
		case "ambiguity":
			runAmbiguity = true
		case "construction-form":
			runConstructionForm = true
		case "bead-size":
			runBeadSize = true
		case "":
		default:
			fmt.Fprintf(os.Stderr, "checkdesigndoc: unknown check %q (want: pins, ambiguity, construction-form, bead-size, all)\n", c)
			os.Exit(2)
		}
	}

	printed := false
	sep := func() {
		if printed {
			fmt.Fprintln(os.Stdout)
		}
		printed = true
	}
	if runAmbiguity {
		sep()
		reportAmbiguity(os.Stdout, *docPath, content)
	}
	if runConstructionForm {
		sep()
		reportConstructionForm(os.Stdout, *docPath, content)
	}
	if runBeadSize {
		sep()
		reportBeadSize(os.Stdout, *docPath, content)
	}
	if runPins {
		sep()
		reportPins(os.Stdout, content)
	}
}

// ---------------------------------------------------------------------------
// pins check
// ---------------------------------------------------------------------------

func reportPins(w *os.File, content string) {
	scenariosSection := extractSection(content, "Domain-Specific Test Scenarios")
	if scenariosSection == "" {
		scenariosSection = extractSection(content, "Required Test Scenarios")
	}
	decompNotesSection := extractSection(content, "Decomposition Notes")

	fmt.Fprintln(w, "== pins ==")
	if scenariosSection == "" {
		fmt.Fprintln(w, "SKIPPED: no \"## Domain-Specific Test Scenarios\" (or \"## Required Test Scenarios\") section.")
		return
	}
	if decompNotesSection == "" {
		fmt.Fprintln(w, "SKIPPED: no \"## Decomposition Notes\" section.")
		return
	}

	scenarios := extractWorkedExamples(scenariosSection)
	pins := extractPins(decompNotesSection)

	fmt.Fprintf(w, "Worked examples in Domain-Specific Test Scenarios (%d found):\n", len(scenarios))
	for _, s := range scenarios {
		fmt.Fprintf(w, "  - %s\n", s)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Pin bullets in Decomposition Notes (%d found):\n", len(pins))
	for _, p := range pins {
		fmt.Fprintf(w, "  - %s\n", p)
	}
	fmt.Fprintln(w)

	if len(pins) < len(scenarios) {
		fmt.Fprintf(w, "WARNING: %d worked example(s) but only %d pin bullet(s) — a pin bullet may cover more\n"+
			"than one scenario (that's fine), but check the lists above by eye: every worked example\n"+
			"should be traceable to at least one pin, and a pin added without a genuinely new scenario\n"+
			"nearby is worth double-checking too.\n", len(scenarios), len(pins))
	} else {
		fmt.Fprintln(w, "Counts look consistent (pins >= worked examples). Still worth a quick eyeball —\n"+
			"this tool counts, it doesn't verify each pin actually names the right scenario.")
	}
}

// extractSection returns the body text of a "## <title>" markdown section,
// up to (but not including) the next "## " heading, or the end of the file.
func extractSection(content, title string) string {
	headingRe := regexp.MustCompile(`(?m)^## ` + regexp.QuoteMeta(title) + `\s*$`)
	loc := headingRe.FindStringIndex(content)
	if loc == nil {
		return ""
	}
	rest := content[loc[1]:]
	nextRe := regexp.MustCompile(`(?m)^## `)
	if nextLoc := nextRe.FindStringIndex(rest); nextLoc != nil {
		return rest[:nextLoc[0]]
	}
	return rest
}

// extractWorkedExamples finds two shapes of worked-example block within a
// Domain-Specific Test Scenarios section: numbered list items with a bold
// lead-in ("1. **Horizontal win:** ...") and "**Required worked example for
// ...**" paragraphs (the ai bead's evaluate() case doesn't fit the numbered
// list shape used for CheckWinner/draw scenarios).
func extractWorkedExamples(section string) []string {
	var out []string
	numberedRe := regexp.MustCompile(`(?m)^\d+\.\s+\*\*(?s:(.+?))\*\*`)
	for _, m := range numberedRe.FindAllStringSubmatch(section, -1) {
		out = append(out, collapseWhitespace(m[1]))
	}
	// Bold paragraph lead-ins used as scenario headers by draft-design-doc and
	// several hand-written docs: "**Required worked example for ...**",
	// "**Scenario A — ...:**", "**Worked example: ...**".
	leadInRe := regexp.MustCompile(`(?mi)^\*\*((?:Required worked example|Scenario [A-Z0-9]|Worked example)[^*]*)\*\*`)
	for _, m := range leadInRe.FindAllStringSubmatch(section, -1) {
		out = append(out, collapseWhitespace(m[1]))
	}
	return out
}

// extractPins finds top-level Decomposition Notes bullets whose bold lead-in
// starts with "Pin" (the doc's established convention for "this literal
// value/board/example must survive into a named bead's spec verbatim" — see
// design_doc_guide.md and every worked-example pin in this repo's design docs).
func extractPins(section string) []string {
	var out []string
	pinRe := regexp.MustCompile(`(?m)^-\s+\*\*(Pin[^*]+)\*\*`)
	for _, m := range pinRe.FindAllStringSubmatch(section, -1) {
		out = append(out, collapseWhitespace(m[1]))
	}
	return out
}

// collapseWhitespace joins a multi-line bold title (markdown wraps long lines)
// into a single display line.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ---------------------------------------------------------------------------
// construction-form check
// ---------------------------------------------------------------------------
//
// A Cross-Bead Contract that hands a slice of a polymorphic type ([]Stmt, []Expr,
// []Shape — a named type that is never constructed directly, only via ≥2 concrete
// brace-literal variants) across a bead boundary, without stating whether the
// elements are struct values or pointers, is the b314 defect: exprvm-web's
// `parser → compiler` contract lists `[]Stmt` with `AssignStmt{…}` /
// `PrintStmt{…}` / `ExprStmt{…}` all as value-form literals but never says
// value-not-pointer. The scaffold used value receivers, so `T` and `*T` both
// satisfy the interface; the impl picked `*ExprStmt`, the test picked `ExprStmt`,
// and REFINE_TESTS burned a re_refine on it — twice in one run (b314 and the
// b316 replay). This flags the site so the doc gets a value/pointer pin (a
// hand-off to design-doc precision guidance, which covers the wording).

var (
	// contractSubheadingRe splits the Cross-Bead Contracts section into its
	// "### <producer> → <consumer>" blocks.
	contractSubheadingRe = regexp.MustCompile(`(?m)^### .+$`)
	// polymorphicEnumRe matches the "`Name` is `A{…}`, `B{…}`, or `C{…}`" shape a
	// contract uses to enumerate an interface/sum type's concrete variants —
	// exprvm-web's "where `Stmt` is `AssignStmt{Name string, Value Expr}`,
	// `PrintStmt{Value Expr}`, or `ExprStmt{Value Expr}`". The capture is the
	// polymorphic type name; the trailing brace-literal count is checked
	// separately so a two-line soft wrap doesn't defeat the match.
	polymorphicEnumRe = regexp.MustCompile("`([A-Z]\\w*)`\\s+is\\s+`[A-Z]\\w*\\{")
)

func reportConstructionForm(w *os.File, path, content string) {
	fmt.Fprintln(w, "== construction-form ==")
	fmt.Fprintln(w, "Cross-Bead Contracts that enumerate a polymorphic type's concrete variants")
	fmt.Fprintln(w, "(\"`Stmt` is `AssignStmt{…}`, `PrintStmt{…}`, or `ExprStmt{…}`\") without saying")
	fmt.Fprintln(w, "whether instances are struct values or pointers. Each hit needs a value/pointer")
	fmt.Fprintln(w, "line in the contract (or a Decomposition Notes pin). See the b314 incident.")
	fmt.Fprintln(w)

	if extractSection(content, "Cross-Bead Contracts") == "" {
		fmt.Fprintln(w, "SKIPPED: no \"## Cross-Bead Contracts\" section.")
		return
	}
	findings := scanConstructionForm(content)
	if len(findings) == 0 {
		fmt.Fprintln(w, "0 site(s) flagged.")
		return
	}
	for _, f := range findings {
		fmt.Fprintf(w, "  %s:%d  %s\n", path, f.line, f.quote)
	}
	fmt.Fprintf(w, "\n%d site(s) flagged.\n", len(findings))
}

// scanConstructionForm returns one finding per polymorphic type enumerated in a
// Cross-Bead Contract block that has no value-vs-pointer statement.
func scanConstructionForm(content string) []finding {
	section := extractSection(content, "Cross-Bead Contracts")
	if section == "" {
		return nil
	}
	sectionStart := strings.Index(content, section)

	var findings []finding
	for _, blk := range splitContractBlocks(section) {
		body := blk.text
		if constructionFormDisambiguated(body) {
			continue
		}
		seen := map[string]bool{}
		for _, m := range polymorphicEnumRe.FindAllStringSubmatchIndex(body, -1) {
			name := body[m[2]:m[3]]
			if seen[name] {
				continue
			}
			// Require at least two distinct brace-literal variants in the ~240
			// chars after "`Name` is " — the enumeration, not a one-off.
			tail := body[m[0]:min(len(body), m[0]+240)]
			variants := map[string]bool{}
			for _, v := range braceLiteralNameRe.FindAllStringSubmatch(tail, -1) {
				if v[1] != name {
					variants[v[1]] = true
				}
			}
			if len(variants) < 2 {
				continue
			}
			seen[name] = true
			idx := sectionStart + blk.offset + m[0]
			findings = append(findings, finding{
				line:  lineNumber(content, idx),
				quote: fmt.Sprintf("%s — `%s` (polymorphic, %d+ variants) has no value/pointer statement", strings.TrimSpace(blk.heading), name, len(variants)),
			})
		}
	}
	return findings
}

var braceLiteralNameRe = regexp.MustCompile(`([A-Z]\w*)\{`)

type contractBlock struct {
	heading string
	text    string
	offset  int // byte offset of text within the Cross-Bead Contracts section
}

// splitContractBlocks divides the Cross-Bead Contracts section body into its
// "### " subsections. A leading preamble before the first "### " is ignored.
func splitContractBlocks(section string) []contractBlock {
	locs := contractSubheadingRe.FindAllStringIndex(section, -1)
	var blocks []contractBlock
	for i, m := range locs {
		end := len(section)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		headingLine := section[m[0]:m[1]]
		blocks = append(blocks, contractBlock{
			heading: strings.TrimPrefix(headingLine, "### "),
			text:    section[m[0]:end],
			offset:  m[0],
		})
	}
	return blocks
}

// constructionFormDisambiguated reports whether a contract block already says,
// somewhere, whether its polymorphic-slice elements are values or pointers.
func constructionFormDisambiguated(block string) bool {
	// Phrases that state value-vs-pointer. Deliberately NOT a bare "value" —
	// "Value Expr" is a common field name in exactly these contracts.
	if regexp.MustCompile(`(?i)\bpointers?\b|\bby value\b|\bas values?\b|\bvalue type\b|\bvalue semantics\b|\bnon-pointer\b`).MatchString(block) {
		return true
	}
	// A "&Name{" construction example, or a "[]*Name" slice-of-pointer element
	// type, anywhere in the block resolves it. (A bare "*Program{" does NOT —
	// that disambiguates Program, not the polymorphic Stmt/Expr elements.)
	if regexp.MustCompile(`&[A-Z]\w*\{|\[\]\*[A-Z]\w*`).MatchString(block) {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// ambiguity check
// ---------------------------------------------------------------------------

// scanSectionTitles are the top-level sections whose bodies classes 1, 2 and 6
// scan. Overview prose is deliberately excluded — it is allowed to be loose.
// Classes 7 and 17 scan the whole document instead.
var scanSectionTitles = map[string]bool{
	"Architecture":                   true,
	"Behavioral Specification":       true,
	"Domain-Specific Test Scenarios": true,
	"Required Test Scenarios":        true,
	"Cross-Bead Contracts":           true,
}

type finding struct {
	line  int
	quote string
	note  string
}

func reportAmbiguity(w *os.File, path, content string) {
	fmt.Fprintln(w, "== ambiguity ==")
	fmt.Fprintln(w, "First-pass mechanical scan. Every hit needs a human judgment call; a clean")
	fmt.Fprintln(w, "scan is not a guarantee. See docs/design_doc_ambiguity_checklist.md.")
	fmt.Fprintln(w)

	// Classes 1/2/6/17 scan with fenced code blocks blanked out (line numbers
	// preserved). Class 7 scans the raw doc so a `package main` inside an
	// example block still counts.
	scanText := stripFences(content)
	secs := topSections(scanText)
	inScope := func(idx int) bool { return scanSectionTitles[sectionTitleAt(secs, idx)] }

	class1 := scanClass1(scanText, inScope)
	class2 := scanClass2(scanText, inScope)
	class6 := scanClass6(scanText, inScope)
	class7 := scanClass7(content)
	class17 := scanClass17(scanText)

	printClass(w, path, "Class 1", "directional / geometric relationships",
		"relative-direction language with no worked Δrow/Δcol (or a1->b2 style) example in the same paragraph",
		class1)
	printClass(w, path, "Class 2", "spec-derived arithmetic",
		"a formula or numeric constraint stated with no computed example value in the same paragraph",
		class2)
	printClass(w, path, "Class 6", "concrete literals over symbolic references",
		"a value pointed at by name rather than inlined as a literal on the same line",
		class6)
	printClass(w, path, "Class 7", "package / entry-point declaration",
		"main.go is referenced but no literal `package main` declaration appears anywhere in the doc",
		class7)
	printClass(w, path, "Class 17", "self-describing category phrase",
		"a phrase naming a category as if the name were the spec ('standard movement rules'). "+
			"JUDGMENT REVIEW WILL NOT CATCH THIS CLASS — a same-fluency reviewer has the author's "+
			"blind spot. Each hit must be replaced with the explicit rules it names, or affirmatively "+
			"justified as fully standard for this domain",
		class17)

	total := len(class1) + len(class2) + len(class6) + len(class7) + len(class17)
	fmt.Fprintf(w, "%d site(s) flagged across all classes.\n", total)
}

func printClass(w *os.File, path, label, name, meaning string, fs []finding) {
	fmt.Fprintf(w, "%s (%s): %d site(s) flagged\n", label, name, len(fs))
	if len(fs) > 0 {
		fmt.Fprintf(w, "  meaning: %s\n", meaning)
	}
	for _, f := range fs {
		fmt.Fprintf(w, "  %s:%d  %s\n", path, f.line, f.quote)
		if f.note != "" {
			fmt.Fprintf(w, "         (%s)\n", f.note)
		}
	}
	fmt.Fprintln(w)
}

// --- section machinery -----------------------------------------------------

type section struct {
	title      string
	start, end int // byte offsets into content; [start,end) is the body
}

// topSections splits the document on "## " headings. Each section's body runs
// from the end of its heading line to the start of the next "## " heading
// (or EOF), so it includes any "### " subsections.
func topSections(content string) []section {
	re := regexp.MustCompile(`(?m)^## (.+?)[ \t]*$`)
	locs := re.FindAllStringSubmatchIndex(content, -1)
	var secs []section
	for i, m := range locs {
		title := strings.TrimSpace(content[m[2]:m[3]])
		bodyStart := m[1]
		end := len(content)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		secs = append(secs, section{title, bodyStart, end})
	}
	return secs
}

func sectionTitleAt(secs []section, idx int) string {
	for _, s := range secs {
		if idx >= s.start && idx < s.end {
			return s.title
		}
	}
	return ""
}

// lineNumber returns the 1-based line number of the byte at idx.
func lineNumber(content string, idx int) int {
	if idx > len(content) {
		idx = len(content)
	}
	return 1 + strings.Count(content[:idx], "\n")
}

// paragraphBlock returns the run of consecutive non-blank lines containing the
// byte at idx — the tool's approximation of "the same paragraph or list item".
func paragraphBlock(content string, idx int) string {
	start := idx
	for start > 0 {
		nl := strings.LastIndexByte(content[:start], '\n')
		if nl < 0 {
			start = 0
			break
		}
		// line is content[nl+1 : lineEnd]; check the line before this one
		prevNL := strings.LastIndexByte(content[:nl], '\n')
		lineStart := prevNL + 1
		if strings.TrimSpace(content[lineStart:nl]) == "" {
			start = nl + 1
			break
		}
		start = lineStart
	}
	end := idx
	for end < len(content) {
		nl := strings.IndexByte(content[end:], '\n')
		if nl < 0 {
			end = len(content)
			break
		}
		lineEnd := end + nl
		nextNL := strings.IndexByte(content[lineEnd+1:], '\n')
		var nextLineEnd int
		if nextNL < 0 {
			nextLineEnd = len(content)
		} else {
			nextLineEnd = lineEnd + 1 + nextNL
		}
		if strings.TrimSpace(content[lineEnd+1:nextLineEnd]) == "" {
			end = lineEnd
			break
		}
		end = lineEnd + 1
	}
	return content[start:end]
}

func lineAt(content string, idx int) string {
	start := strings.LastIndexByte(content[:idx], '\n') + 1
	end := strings.IndexByte(content[idx:], '\n')
	if end < 0 {
		return content[start:]
	}
	return content[start : idx+end]
}

func quoteLine(content string, idx int) string {
	return collapseWhitespace(lineAt(content, idx))
}

// --- class scanners ------------------------------------------------------

var (
	// fenceRe matches a ```-delimited code block. stripFences blanks these
	// out (keeping the line count) before classes 1/2/6/17 scan, so directory
	// trees and Go signature blocks don't generate noise.
	fenceRe = regexp.MustCompile("(?ms)^```.*?^```[ \\t]*$")

	// Class 1 — relative-direction / geometric language. Deliberately narrow:
	// bare "above"/"below"/"left of" are excluded because in these docs they
	// are almost always cross-references ("see rule below"), not geometry.
	class1Trigger = regexp.MustCompile(`(?i)\b(diagonal|diagonally|orthogonal|orthogonally|toward|towards|forwards?|backwards?|clockwise|counter-?clockwise)\b|\b(up|down) the board\b|\brow (above|below)\b|\bcolumn to the (left|right)\b`)
	class1Clear   = regexp.MustCompile(`(?i)Δ\s*(row|col|rank|file)|\bd[rc]\s*[=:]\s*[+-]?\d|\(\s*[+-]?\d+\s*,\s*[+-]?\d+\s*\)|\{\s*[+-]?\d+\s*,\s*[+-]?\d+\s*\}|\b[a-h][1-8]\s*(→|->|to)\s*[a-h][1-8]`)

	// Class 2 — a formula / numeric constraint stated without a computed value.
	// Keyword-only: an arithmetic-operator heuristic flagged every Go pointer
	// type (`g *Game`) and was dropped.
	// `\bmod ` (with the trailing space) not `\bmod\b` — the latter matches `go.mod`
	// in every Architecture section that lists build files.
	class2Trigger = regexp.MustCompile(`(?i)\b(sum of|product of|divided by|modulo|floor\(|ceil\()|\bmod \S|\bFNV\b|\bhash of\b|\bthe formula\b|\bnumber of\b[^.\n]{0,50}\bis\b`)
	class2Clear   = regexp.MustCompile(`(?i)=\s*[+-]?\d|\be\.g\.,?\s*[+-]?\d|→\s*[+-]?\d|\bis\s+[+-]?\d+\b|partitions?\s+[+-]?\d|\b[+-]?\d+\s*(bytes?|elements?|cells?|rows?|columns?|pixels?|entries|players|stones?|pieces?|partitions?|slots?)\b`)

	// Class 6 — a value pointed at by name rather than inlined. Clearing is
	// same-line only ("in the same sentence" per the checklist).
	class6Trigger = regexp.MustCompile(`(?i)\buse the \w+ (type|constant|value|enum) (from|defined in|in) \w|\bas defined (above|below|elsewhere)\b|\bsee\b[^.\n]{0,40}\bfor the (value|values|definition)\b|\bthe standard value\b`)
	class6Clear   = regexp.MustCompile("`[^`]+`" + `|=\s*\S|\b\w+\([+-]?\d+\)|\b[+-]?\d+\b`)

	// Class 7 — main.go referenced with no literal `package main` anywhere.
	// Runs on the raw doc (not fence-stripped): an example block may carry it.
	class7File  = regexp.MustCompile(`\bmain\.go\b`)
	class7Clear = regexp.MustCompile(`(?i)package main`)

	// Class 17 — a category name used as if it were the spec. Adjective must
	// sit within two words of the noun ("standard movement rules"), else
	// "standard starting position with White to move" false-positives.
	class17Trigger = regexp.MustCompile(`(?i)\b(standard|normal|usual|conventional|typical)\s+(\w+\s+){0,2}(rules?|movement|moves?|behaviou?r|procedure|semantics|logic|betting|scoring|tie-?break\w*)\b|\b(movement|betting|scoring|tie-?break\w*)\s+(\w+\s+){0,2}(are|is)\s+(standard|normal|usual|conventional|typical)\b`)
)

// stripFences replaces every ```-fenced block with the same number of newlines,
// so byte offsets change but line numbers reported to the user stay accurate.
func stripFences(content string) string {
	return fenceRe.ReplaceAllStringFunc(content, func(block string) string {
		return strings.Repeat("\n", strings.Count(block, "\n"))
	})
}

func scanTriggerWithClear(content string, trigger, clear *regexp.Regexp, inScope func(int) bool, blockLevel bool) []finding {
	var out []finding
	for _, m := range trigger.FindAllStringIndex(content, -1) {
		idx := m[0]
		if inScope != nil && !inScope(idx) {
			continue
		}
		var context string
		if blockLevel {
			context = paragraphBlock(content, idx)
		} else {
			context = lineAt(content, idx)
		}
		if clear.MatchString(context) {
			continue
		}
		out = append(out, finding{line: lineNumber(content, idx), quote: quoteLine(content, idx)})
	}
	return dedupeByLine(out)
}

func scanClass1(content string, inScope func(int) bool) []finding {
	return scanTriggerWithClear(content, class1Trigger, class1Clear, inScope, true)
}

func scanClass2(content string, inScope func(int) bool) []finding {
	return scanTriggerWithClear(content, class2Trigger, class2Clear, inScope, true)
}

func scanClass6(content string, inScope func(int) bool) []finding {
	return scanTriggerWithClear(content, class6Trigger, class6Clear, inScope, false)
}

func scanClass7(content string) []finding {
	loc := class7File.FindStringIndex(content)
	if loc == nil {
		return nil
	}
	if class7Clear.MatchString(content) {
		return nil
	}
	return []finding{{
		line:  lineNumber(content, loc[0]),
		quote: quoteLine(content, loc[0]),
		note:  "no `package main` declaration found anywhere in the doc; `go build` gives no signal if a different package name is used",
	}}
}

func scanClass17(content string) []finding {
	var out []finding
	for _, m := range class17Trigger.FindAllStringIndex(content, -1) {
		out = append(out, finding{line: lineNumber(content, m[0]), quote: quoteLine(content, m[0])})
	}
	return dedupeByLine(out)
}

func dedupeByLine(fs []finding) []finding {
	seen := map[int]bool{}
	var out []finding
	for _, f := range fs {
		if seen[f.line] {
			continue
		}
		seen[f.line] = true
		out = append(out, f)
	}
	return out
}
