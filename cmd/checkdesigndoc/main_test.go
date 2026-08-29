package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allInScope is an inScope predicate that accepts every offset — used when a
// test snippet has no section headings.
func allInScope(int) bool { return true }

func TestClass1_flagsBareDirectionLanguage(t *testing.T) {
	doc := "## Behavioral Specification\n\nThe knight moves diagonally to reach the far corner.\n"
	got := scanClass1(stripFences(doc), allInScope)
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(got), got)
	}
}

func TestClass1_clearedByWorkedDelta(t *testing.T) {
	doc := "## Behavioral Specification\n\nThe bishop moves diagonally: c1 -> h6, Δfile=5, Δrank=5.\n"
	if got := scanClass1(stripFences(doc), allInScope); len(got) != 0 {
		t.Fatalf("want 0 findings (Δ present), got %d: %+v", len(got), got)
	}
}

func TestClass1_ignoresCrossReferencePhrases(t *testing.T) {
	doc := "## Behavioral Specification\n\nSee the rule below; the value above is authoritative.\n"
	if got := scanClass1(stripFences(doc), allInScope); len(got) != 0 {
		t.Fatalf("bare above/below must not flag, got %+v", got)
	}
}

func TestClass1_ignoresOverviewProse(t *testing.T) {
	doc := "## Overview\n\nStones connect orthogonally and never diagonally.\n"
	st := stripFences(doc)
	secs := topSections(st)
	inScope := func(i int) bool { return scanSectionTitles[sectionTitleAt(secs, i)] }
	if got := scanClass1(st, inScope); len(got) != 0 {
		t.Fatalf("Overview is out of scope, got %+v", got)
	}
}

func TestClass2_flagsFormulaWithoutValue(t *testing.T) {
	doc := "## Behavioral Specification\n\nThe partition is the FNV-1a hash of the key mod the partition count.\n"
	if got := scanClass2(stripFences(doc), allInScope); len(got) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(got), got)
	}
}

func TestClass2_clearedByComputedValue(t *testing.T) {
	doc := "## Behavioral Specification\n\nhash(\"order-1\") mod 3 = 1, so it routes to partition 1.\n"
	if got := scanClass2(stripFences(doc), allInScope); len(got) != 0 {
		t.Fatalf("want 0 findings (value present), got %d: %+v", len(got), got)
	}
}

func TestClass2_doesNotFlagGoPointerTypes(t *testing.T) {
	doc := "## Data Types and Function Signatures\n\n**`Compile(prog *Program) (*CompiledProgram, error)`** translates the AST.\n"
	// Section not in scanSectionTitles, but test the scanner directly with allInScope
	// to prove the pointer-star does not trigger the arithmetic heuristic.
	if got := scanClass2(stripFences(doc), allInScope); len(got) != 0 {
		t.Fatalf("Go pointer types must not flag as arithmetic, got %+v", got)
	}
}

func TestClass6_flagsSymbolicReference(t *testing.T) {
	doc := "## Behavioral Specification\n\nWhite is the Color value as defined above.\n"
	if got := scanClass6(stripFences(doc), allInScope); len(got) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(got), got)
	}
}

func TestClass6_clearedByInlineLiteral(t *testing.T) {
	doc := "## Behavioral Specification\n\nWhite is `Color(-1)`, as defined above.\n"
	if got := scanClass6(stripFences(doc), allInScope); len(got) != 0 {
		t.Fatalf("inline literal on same line clears the finding, got %+v", got)
	}
}

func TestClass7_flagsMissingPackageMain(t *testing.T) {
	doc := "## Architecture\n\n- `main.go` contains `var game *Game` and `func main()`.\n"
	if got := scanClass7(doc); len(got) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(got), got)
	}
}

func TestClass7_clearedByPackageMainDeclaration(t *testing.T) {
	doc := "## Architecture\n\nAll `.go` files use `package main`.\n\n- `main.go` contains `func main()`.\n"
	if got := scanClass7(doc); len(got) != 0 {
		t.Fatalf("package main present, want 0 findings, got %+v", got)
	}
}

func TestClass17_flagsSelfDescribingCategoryPhrase(t *testing.T) {
	doc := "Knight, Bishop, Rook, Queen, King: standard movement rules.\n"
	if got := scanClass17(doc); len(got) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(got), got)
	}
}

func TestClass17_ignoresDistantAdjectiveNoun(t *testing.T) {
	doc := "NewGame() returns the standard starting position with White to move first.\n"
	if got := scanClass17(doc); len(got) != 0 {
		t.Fatalf("adjective too far from noun, want 0 findings, got %+v", got)
	}
}

func TestStripFences_preservesLineNumbers(t *testing.T) {
	doc := "line1\n```\nfenced diagonally\n```\nafter the fence, moves diagonally\n"
	st := stripFences(doc)
	if strings.Count(st, "\n") != strings.Count(doc, "\n") {
		t.Fatalf("line count changed: %d -> %d", strings.Count(doc, "\n"), strings.Count(st, "\n"))
	}
	got := scanClass1(st, allInScope)
	if len(got) != 1 {
		t.Fatalf("want 1 finding (only the non-fenced line), got %d: %+v", len(got), got)
	}
	if got[0].line != 5 {
		t.Fatalf("want finding on line 5, got line %d", got[0].line)
	}
}

// TestFixtureDocs_regressionAnchors guards the tuned regexes against drift:
// the fixed chess fixture must be clean, and every other fixture must stay at
// or below its current flag count so a future regex change that reintroduces
// noise fails here.
func TestFixtureDocs_regressionAnchors(t *testing.T) {
	maxFlags := map[string]int{
		"checkers.md":   4,
		"chess.md":      0,
		"exprvm.md":     1,
		"exprvm-web.md": 1,
		"fractal.md":    0,
		"goban.md":      1,
		"kafka-sim.md":  4,
		"othello.md":    0,
		"tasklist.md":   0,
	}
	dir := filepath.Join("..", "..", "docs", "fixture-design-docs")
	for name, max := range maxFlags {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		content := string(data)
		st := stripFences(content)
		secs := topSections(st)
		inScope := func(i int) bool { return scanSectionTitles[sectionTitleAt(secs, i)] }
		n := len(scanClass1(st, inScope)) + len(scanClass2(st, inScope)) +
			len(scanClass6(st, inScope)) + len(scanClass7(content)) + len(scanClass17(st))
		if n > max {
			t.Errorf("%s: %d flags, want <= %d (regex may have regressed toward noise)", name, n, max)
		}
	}
}

func TestPins_countsWorkedExamplesAndPins(t *testing.T) {
	doc := `## Domain-Specific Test Scenarios

1. **Horizontal win:** four in a row.
2. **Vertical win:** four in a column.

## Decomposition Notes

- **Pin the two win scenarios into the check-winner bead.**
`
	scenarios := extractWorkedExamples(extractSection(doc, "Domain-Specific Test Scenarios"))
	pins := extractPins(extractSection(doc, "Decomposition Notes"))
	if len(scenarios) != 2 {
		t.Fatalf("want 2 worked examples, got %d: %v", len(scenarios), scenarios)
	}
	if len(pins) != 1 {
		t.Fatalf("want 1 pin, got %d: %v", len(pins), pins)
	}
}
