package verbs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A compact doc with the real heading structure checkdesigndoc enforces.
const testDesignDoc = `# Widget — Design Document

## Overview

Irrelevant prose that should never appear in an excerpt.

## Data Types and Function Signatures

` + "```go" + `
type Token struct { Type int; Text string }
` + "```" + `

## Behavioral Specification

**` + "`" + `(*Parser).ParseProgram() (*Program, error)` + "`" + `** — parses the
submission. On an unexpected token it returns an error whose message names the
offending token's ` + "`Text`" + `, never the whole ` + "`Token`" + ` struct.

**Left-associativity, precedence, and unary minus** — a cross-cutting rule that
names no single function and must reach every bead.

**` + "`" + `HandleEval(w http.ResponseWriter, r *http.Request)` + "`" + `** — serves
POST /eval. Calls ParseProgram then Compile.

## Domain-Specific Test Scenarios

1. **Left-associativity**: ` + "`\"10-3-2\"`" + ` -> ` + "`\"5\"`" + `.
2. **Unexpected token**: ` + "`\"2+\"`" + ` -> Err is non-empty (message names the token text).

## Cross-Bead Contracts

### parser -> compiler (format)

- **producer**: parser (parser.go)
- **consumer**: compiler (compiler.go)
- **interface**: ` + "`*Program`" + `

### handlers -> templates (data-shape)

- **producer**: handlers (handlers.go)
- **consumer**: templates (templates.go)
- **interface**: ` + "`PageData`" + `

## Decomposition Notes

- **Pin the token-text rule to the parser bead**: the factor error message is
  exactly ` + "`unexpected token: <text>`" + `.
`

func writeBeadFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestLoadDesignDocSectionsForBead_ParserBead(t *testing.T) {
	dir := writeBeadFiles(t, map[string]string{
		"parser.go": "package main\n\nfunc NewParser(s string) *Parser { return nil }\nfunc (p *Parser) ParseProgram() (*Program, error) { return nil, nil }\ntype Parser struct{}\ntype Program struct{}\n",
	})
	bead := &beadState{BeadID: 1, OutputFiles: []string{"parser.go", "parser_test.go"}}

	out := loadDesignDocSectionsForBead(testDesignDoc, dir, bead)

	if out == "" {
		t.Fatal("expected a non-empty excerpt")
	}
	// Always-included sections present.
	for _, want := range []string{"Domain-Specific Test Scenarios", "Decomposition Notes", "Data Types and Function Signatures"} {
		if !strings.Contains(out, want) {
			t.Errorf("excerpt missing always-included section %q", want)
		}
	}
	// Overview never leaks.
	if strings.Contains(out, "Irrelevant prose") {
		t.Error("excerpt leaked the Overview section")
	}
	// The parser cross-bead contract is kept; the handlers one is dropped.
	if !strings.Contains(out, "parser -> compiler") {
		t.Error("excerpt dropped the parser -> compiler contract")
	}
	if strings.Contains(out, "handlers -> templates") {
		t.Error("excerpt kept an unrelated contract (handlers -> templates)")
	}
	// Behavioral: the ParseProgram block and the cross-cutting rule are kept;
	// the HandleEval block is dropped (small doc still fits budget here, so the
	// whole section is included — adjust if the budget shrinks).
	if !strings.Contains(out, "ParseProgram") {
		t.Error("excerpt dropped the ParseProgram behavioral block")
	}
}

func TestLoadDesignDocSectionsForBead_BehavioralFilteringUnderBudget(t *testing.T) {
	// Force the filtered path by inflating the Behavioral Specification past the
	// budget with filler bold blocks that name no bead symbol.
	var filler strings.Builder
	filler.WriteString(testDesignDoc)
	filler.WriteString("\n")
	// Append filler to the Behavioral Specification by rewriting: simplest is to
	// just append a huge unrelated bold block before the next "## " heading.
	doc := strings.Replace(testDesignDoc,
		"## Domain-Specific Test Scenarios",
		"**`UnrelatedThing()`** — "+strings.Repeat("padding padding padding ", 2000)+"\n\n## Domain-Specific Test Scenarios",
		1)

	dir := writeBeadFiles(t, map[string]string{
		"parser.go": "package main\n\nfunc (p *Parser) ParseProgram() (*Program, error) { return nil, nil }\ntype Parser struct{}\ntype Program struct{}\n",
	})
	bead := &beadState{BeadID: 1, OutputFiles: []string{"parser.go"}}

	out := loadDesignDocSectionsForBead(doc, dir, bead)

	if strings.Contains(out, "UnrelatedThing") {
		t.Error("filtered behavioral path kept an unrelated function block")
	}
	if !strings.Contains(out, "ParseProgram") {
		t.Error("filtered behavioral path dropped the relevant ParseProgram block")
	}
	if !strings.Contains(out, "Left-associativity, precedence, and unary minus") {
		t.Error("filtered behavioral path dropped the cross-cutting rule block")
	}
	if len(out) > designDocExcerptBudget+200 {
		t.Errorf("excerpt %d bytes exceeds budget %d", len(out), designDocExcerptBudget)
	}
}

func TestLoadDesignDocSectionsForBead_NoStructure(t *testing.T) {
	dir := writeBeadFiles(t, map[string]string{"parser.go": "package main\n"})
	bead := &beadState{BeadID: 1, OutputFiles: []string{"parser.go"}}

	if out := loadDesignDocSectionsForBead("just some freeform notes, no headings", dir, bead); out != "" {
		t.Errorf("expected empty excerpt for an unstructured doc, got %q", out)
	}
	if out := loadDesignDocSectionsForBead("", dir, bead); out != "" {
		t.Errorf("expected empty excerpt for an empty doc, got %q", out)
	}
}

func TestGoSymbols(t *testing.T) {
	syms := goSymbols("package main\n\ntype Foo struct{}\nconst Bar = 1\nfunc Baz() {}\nfunc (f Foo) Qux() {}\n")
	got := map[string]bool{}
	for _, s := range syms {
		got[s] = true
	}
	for _, want := range []string{"Foo", "Bar", "Baz", "Qux"} {
		if !got[want] {
			t.Errorf("goSymbols missing %q (got %v)", want, syms)
		}
	}
}
