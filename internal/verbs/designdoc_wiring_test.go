package verbs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadDesignDocExcerptForBead_EndToEnd exercises the full downstream path:
// a project row pointing at a folder with a real design_doc.md, through
// loadDesignDoc -> loadDesignDocSectionsForBead -> the authoritative-source
// header. This is the wiring REFINE_TESTS_{WRITE,CRITIQUE,JUDGE} and
// ADJUDICATE_NEXT_EXECUTION now share.
func TestLoadDesignDocExcerptForBead_EndToEnd(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "design_doc.md"), []byte(testDesignDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "parser.go"),
		[]byte("package main\n\ntype Parser struct{}\ntype Program struct{}\nfunc (p *Parser) ParseProgram() (*Program, error) { return nil, nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := d.ExecContext(ctx, `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (-1, 'fixture: design-doc excerpt wiring', ?, 'design_doc.md', 'active',
		        'honor', 300, 2, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		dir); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	bead := &beadState{BeadID: 1, OutputFiles: []string{"parser.go", "parser_test.go"}}
	out := loadDesignDocExcerptForBead(ctx, d, -1, bead)

	if out == "" {
		t.Fatal("expected a non-empty excerpt")
	}
	if !strings.Contains(out, "AUTHORITATIVE") {
		t.Error("excerpt missing the authoritative-source header")
	}
	// The rule DECOMPOSE would blur ("message names the offending token's Text,
	// never the whole Token struct") must survive into what the verb sees.
	if !strings.Contains(out, "offending token's `Text`") {
		t.Error("excerpt dropped the parser error-message rule from the Behavioral Specification")
	}
	if !strings.Contains(out, "Pin the token-text rule to the parser bead") {
		t.Error("excerpt dropped the Decomposition Notes pin")
	}
	// A bead whose project row is missing must degrade to "" rather than error.
	if got := loadDesignDocExcerptForBead(ctx, d, -999, bead); got != "" {
		t.Errorf("expected empty excerpt for an unknown project, got %q", got)
	}
}
