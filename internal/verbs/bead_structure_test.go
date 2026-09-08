package verbs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const structDocEmDash = `## Decomposition Notes

**Bead dependency order (do not reorder):**

1. **core** — ` + "`Thing`" + `, ` + "`MakeThing`" + `. No dependencies. Owns ` + "`core.go`" + `.
2. **handlers** — the HTTP handlers. Depends on bead 1. Owns ` + "`handlers.go`" + `.
3. **templates** — ` + "`InitTemplates`" + `. Depends on bead 1. Owns ` + "`templates.go`" + `.
4. **cli** (main.go): wires the mux. Depends on bead 2.
5. **integration** — one bounded httptest scenario.

**Pins:**

- **Pin — something to the ` + "`core`" + ` bead:** value.
`

func TestParseDecompositionNotesBeadList(t *testing.T) {
	got := parseDecompositionNotesBeadList(structDocEmDash)
	want := []structDocBead{
		{"core", "core.go"},
		{"handlers", "handlers.go"},
		{"templates", "templates.go"},
		{"cli", "main.go"}, // from "(main.go)"
		{"integration", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d beads, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bead[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseDecompositionNotesBeadList_ColonFormat(t *testing.T) {
	doc := "## Decomposition Notes\n\n" +
		"1. **lexer**: `Token`, `NewLexer`. No dependencies.\n" +
		"2. **parser**: AST types, `NewParser`. Calls bead 1.\n"
	got := parseDecompositionNotesBeadList(doc)
	if len(got) != 2 || got[0].title != "lexer" || got[1].title != "parser" {
		t.Fatalf("colon format not parsed: %+v", got)
	}
}

func TestParseDecompositionNotesBeadList_StopsAtNonContiguousProse(t *testing.T) {
	doc := "## Decomposition Notes\n\n" +
		"1. **lexer**: `NewLexer`. Owns `lexer.go`.\n" +
		"2. **parser**: `NewParser`. Owns `parser.go`.\n\n" +
		"**Integration scenarios:**\n\n" +
		"1. Submit `\"x=5\"` and assert the response contains `5`.\n" +
		"2. Submit `\"1/0\"` and assert an error.\n"
	got := parseDecompositionNotesBeadList(doc)
	if len(got) != 2 || got[0].title != "lexer" || got[1].title != "parser" {
		t.Fatalf("stray numbered prose leaked into bead list: %+v", got)
	}
}

func TestParseDecompositionNotesBeadList_NoNumberedList(t *testing.T) {
	doc := "## Decomposition Notes\n\n- **Pin X to the `game` bead.**\n- **Sequencing:** templates before handlers.\n"
	if got := parseDecompositionNotesBeadList(doc); got != nil {
		t.Errorf("prose-only Decomposition Notes must yield nil, got %+v", got)
	}
}

func bead(title string, files ...string) ParsedBead {
	return ParsedBead{Title: title, OutputFiles: files}
}

func TestBeadStructureViolations_NoListIsSkip(t *testing.T) {
	doc := "## Decomposition Notes\n\n- **Sequencing:** templates before handlers.\n"
	if v := beadStructureViolations(doc, []ParsedBead{bead("anything", "x.go")}); v != nil {
		t.Errorf("no numbered list -> no violations, got %v", v)
	}
}

func TestBeadStructureViolations_HappyPath(t *testing.T) {
	proposed := []ParsedBead{
		bead("core", "core.go", "core_test.go"),
		bead("handlers", "handlers.go", "handlers_test.go"),
		bead("templates", "templates.go", "templates_test.go"),
		bead("main", "main.go"), // benign rename of "cli"
		bead("integration", "integration_test.go"),
	}
	if v := beadStructureViolations(structDocEmDash, proposed); len(v) != 0 {
		t.Errorf("clean 1:1 decomposition (with cli->main rename) must not violate, got:\n%s", strings.Join(v, "\n"))
	}
}

func TestBeadStructureViolations_MergeFlagged(t *testing.T) {
	// The exprvm-web baseline-14 shape: handlers + templates folded into one bead.
	proposed := []ParsedBead{
		bead("core", "core.go"),
		bead("handlers-templates", "handlers.go", "templates.go"),
		bead("cli", "main.go"),
	}
	v := beadStructureViolations(structDocEmDash, proposed)
	if len(v) == 0 {
		t.Fatal("merge of handlers+templates must be flagged")
	}
	joined := strings.Join(v, "\n")
	if !strings.Contains(joined, "handlers-templates") ||
		!strings.Contains(joined, "handlers") || !strings.Contains(joined, "templates") {
		t.Errorf("merge violation should name the merged bead and both listed beads:\n%s", joined)
	}
}

func TestBeadStructureViolations_DropFlagged(t *testing.T) {
	proposed := []ParsedBead{
		bead("core", "core.go"),
		bead("handlers", "handlers.go"),
		// templates dropped entirely
		bead("cli", "main.go"),
	}
	v := beadStructureViolations(structDocEmDash, proposed)
	if len(v) != 1 || !strings.Contains(v[0], `"templates"`) {
		t.Errorf("dropped `templates` bead must be the single violation, got: %v", v)
	}
}

func TestBeadStructureViolations_IntegrationBeadNotRequired(t *testing.T) {
	// "integration" listed but no proposed bead covers it -> not a violation.
	proposed := []ParsedBead{
		bead("core", "core.go"),
		bead("handlers", "handlers.go"),
		bead("templates", "templates.go"),
		bead("cli", "main.go"),
	}
	if v := beadStructureViolations(structDocEmDash, proposed); len(v) != 0 {
		t.Errorf("missing integration bead must not violate, got: %v", v)
	}
}

func TestBeadStructureViolations_ExtraBeadAllowed(t *testing.T) {
	proposed := []ParsedBead{
		bead("core", "core.go"),
		bead("handlers", "handlers.go"),
		bead("templates", "templates.go"),
		bead("main", "main.go"),
		bead("integration", "integration_test.go"),
		bead("error-path-integration", "error_integration_test.go"), // extra, test-only
	}
	if v := beadStructureViolations(structDocEmDash, proposed); len(v) != 0 {
		t.Errorf("an extra integration bead must not violate, got: %v", v)
	}
}

// TestBeadStructureViolations_RealDocsClean is the false-positive baseline:
// for every design doc with a numbered bead list, the natural 1:1
// decomposition (one bead per listed entry, owning that entry's file) must
// produce zero structure violations.
func TestBeadStructureViolations_RealDocsClean(t *testing.T) {
	roots := []string{"../../docs/design-docs", "../../docs/fixture-design-docs"}
	seen := 0
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			path := filepath.Join(root, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			docBeads := parseDecompositionNotesBeadList(string(data))
			if len(docBeads) < 2 {
				continue // no numbered list — check is skipped for this doc
			}
			seen++
			var proposed []ParsedBead
			for _, db := range docBeads {
				files := []string{}
				if db.file != "" {
					files = append(files, db.file)
				} else {
					files = append(files, strings.ReplaceAll(strings.ToLower(db.title), "+", "_")+".go")
				}
				proposed = append(proposed, bead(db.title, files...))
			}
			if v := beadStructureViolations(string(data), proposed); len(v) != 0 {
				t.Errorf("%s: natural 1:1 decomposition flagged:\n%s", e.Name(), strings.Join(v, "\n"))
			}
		}
	}
	if seen == 0 {
		t.Fatal("no docs with a numbered bead list found — test is vacuous")
	}
}
