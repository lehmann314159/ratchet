// checkdesigndoc surfaces every worked-example block in a design doc's
// "Domain-Specific Test Scenarios" section alongside every "Pin ..." bullet
// in its "Decomposition Notes" section, so a worked example added without a
// matching pin is visible at a glance instead of relying on memory to catch
// it.
//
// This is not a pass/fail oracle: whether a given scenario is legitimately
// covered by an existing pin (including a pin that bundles several
// scenarios together, e.g. "the six scenarios listed verbatim above") is a
// judgment call this tool doesn't attempt to make. It exists because the
// actual failure mode observed live (connect-four-v5 bead 71, 2026-08-28)
// wasn't a borderline judgment call — a worked example was added and the
// matching pin was simply forgotten entirely, something that a human
// glancing at these two lists side by side would have caught immediately.
//
// Usage:
//
//	go run ./cmd/checkdesigndoc --doc path/to/design-doc.md
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

	scenariosSection := extractSection(content, "Domain-Specific Test Scenarios")
	decompNotesSection := extractSection(content, "Decomposition Notes")

	if scenariosSection == "" {
		fmt.Fprintln(os.Stderr, "checkdesigndoc: no \"## Domain-Specific Test Scenarios\" section found")
		os.Exit(1)
	}
	if decompNotesSection == "" {
		fmt.Fprintln(os.Stderr, "checkdesigndoc: no \"## Decomposition Notes\" section found")
		os.Exit(1)
	}

	scenarios := extractWorkedExamples(scenariosSection)
	pins := extractPins(decompNotesSection)

	fmt.Printf("Worked examples in Domain-Specific Test Scenarios (%d found):\n", len(scenarios))
	for _, s := range scenarios {
		fmt.Printf("  - %s\n", s)
	}
	fmt.Println()
	fmt.Printf("Pin bullets in Decomposition Notes (%d found):\n", len(pins))
	for _, p := range pins {
		fmt.Printf("  - %s\n", p)
	}
	fmt.Println()

	if len(pins) < len(scenarios) {
		fmt.Printf("WARNING: %d worked example(s) but only %d pin bullet(s) — a pin bullet may cover more\n"+
			"than one scenario (that's fine), but check the lists above by eye: every worked example\n"+
			"should be traceable to at least one pin, and a pin added without a genuinely new scenario\n"+
			"nearby is worth double-checking too.\n", len(scenarios), len(pins))
	} else {
		fmt.Println("Counts look consistent (pins >= worked examples). Still worth a quick eyeball —\n" +
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
	requiredRe := regexp.MustCompile(`(?m)^\*\*(Required worked example[^*]+)\*\*`)
	for _, m := range requiredRe.FindAllStringSubmatch(section, -1) {
		out = append(out, collapseWhitespace(m[1]))
	}
	return out
}

// extractPins finds top-level Decomposition Notes bullets whose bold
// lead-in starts with "Pin" (the doc's established convention for "this
// literal value/board/example must survive into a named bead's spec
// verbatim" — see design_doc_guide.md and every worked-example pin in this
// repo's design docs).
// collapseWhitespace joins a multi-line bold title (markdown wraps long
// lines) into a single display line.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func extractPins(section string) []string {
	var out []string
	pinRe := regexp.MustCompile(`(?m)^-\s+\*\*(Pin[^*]+)\*\*`)
	for _, m := range pinRe.FindAllStringSubmatch(section, -1) {
		out = append(out, collapseWhitespace(m[1]))
	}
	return out
}
