package verbs

import (
	"os"
	"path/filepath"
	"testing"
)

// writeGoFile is a test helper: writes src to folder/name, creating parent dirs.
func writeGoFile(t *testing.T, folder, name, src string) {
	t.Helper()
	full := filepath.Join(folder, name)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(full, []byte(src), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestCheckStubPurity_PureStubsPass(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "game.go", `package main

func NewGame() *Game { return &Game{} }
func Score(a, b int) (int, int) { return 0, 0 }
func IsValid(s string) bool { return false }
`)
	manifest := &SurveySpecOutput{Files: []SurveyManifestFile{{Path: "game.go"}}}
	if got := checkStubPurity(dir, manifest); len(got) != 0 {
		t.Errorf("expected no violations for pure stubs, got %v", got)
	}
}

// TestCheckStubPurity_CatchesRealImplementation reproduces the exact defeat
// scenario the audit found: a model writes a fully working implementation
// (with real control flow) into SURVEY_SPEC's "declarations" field instead of
// a stub. Before this fix, StubPurityPass was hardcoded true and this would
// have been silently approved by CERTIFY.
func TestCheckStubPurity_CatchesRealImplementation(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "game.go", `package main

func Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
`)
	manifest := &SurveySpecOutput{Files: []SurveyManifestFile{{Path: "game.go"}}}
	got := checkStubPurity(dir, manifest)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 violation, got %v", got)
	}
}

func TestCheckStubPurity_EachBannedStatementKind(t *testing.T) {
	cases := map[string]string{
		"for":    `func F() { for i := 0; i < 1; i++ { _ = i } }`,
		"range":  `func F(xs []int) { for range xs { } }`,
		"switch": `func F(a int) int { switch a { case 1: return 1 }; return 0 }`,
		"select": `func F(ch chan int) { select {} }`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeGoFile(t, dir, "f.go", "package main\n\n"+body+"\n")
			manifest := &SurveySpecOutput{Files: []SurveyManifestFile{{Path: "f.go"}}}
			if got := checkStubPurity(dir, manifest); len(got) != 1 {
				t.Errorf("%s: expected 1 violation, got %v", name, got)
			}
		})
	}
}

func TestCheckStubPurity_SkipsAPICheckFile(t *testing.T) {
	dir := t.TempDir()
	// The generated api_check file is never model-authored declarations, and
	// isn't scaffolded via buildGoFile, so it must be excluded regardless of
	// content shape.
	writeGoFile(t, dir, apiCheckTestFilename, `package main

var (
	_ = NewGame
)
`)
	manifest := &SurveySpecOutput{Files: []SurveyManifestFile{{Path: apiCheckTestFilename}}}
	if got := checkStubPurity(dir, manifest); len(got) != 0 {
		t.Errorf("expected api check file to be skipped, got %v", got)
	}
}
