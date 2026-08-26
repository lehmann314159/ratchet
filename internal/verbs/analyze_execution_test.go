package verbs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckOutputFiles_InlinesContent reproduces the exprvm-v1 bead-22
// incident: ADJUDICATE_NEXT_EXECUTION claimed a fully-correct one-line
// NewVM() was "still a stub" for four rounds running, because nothing in its
// input ever showed it the actual file content — checkOutputFiles reported
// only a byte count. This asserts the fix: the real source text must be
// present in the returned findings, not just size, so a false claim about
// specific code content is falsifiable against ground truth.
func TestCheckOutputFiles_InlinesContent(t *testing.T) {
	dir := t.TempDir()
	src := "package main\n\ntype VM struct{}\n\nfunc NewVM() *VM {\n\treturn &VM{}\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "vm.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	testSrc := "package main\n\nimport \"testing\"\n\nfunc TestRun(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(dir, "vm_test.go"), []byte(testSrc), 0644); err != nil {
		t.Fatal(err)
	}

	specJSON, err := json.Marshal(struct {
		OutputFiles []string `json:"output_files"`
	}{OutputFiles: []string{"vm.go", "vm_test.go"}})
	if err != nil {
		t.Fatal(err)
	}

	got := checkOutputFiles(string(specJSON), dir)

	if !strings.Contains(got, "func NewVM() *VM") || !strings.Contains(got, "return &VM{}") {
		t.Errorf("checkOutputFiles did not inline vm.go's real content; got:\n%s", got)
	}
	if !strings.Contains(got, "func TestRun(t *testing.T)") {
		t.Errorf("checkOutputFiles did not inline vm_test.go's real content; got:\n%s", got)
	}
	if !strings.Contains(got, "vm_test.go: present") || !strings.Contains(got, "1 test function(s)") {
		t.Errorf("checkOutputFiles dropped the existing test-function-count annotation; got:\n%s", got)
	}
}

// TestCheckOutputFiles_MissingFile confirms the pre-existing missing-file
// behavior is unchanged by the content-inlining addition.
func TestCheckOutputFiles_MissingFile(t *testing.T) {
	dir := t.TempDir()
	specJSON, err := json.Marshal(struct {
		OutputFiles []string `json:"output_files"`
	}{OutputFiles: []string{"nonexistent.go"}})
	if err != nil {
		t.Fatal(err)
	}

	got := checkOutputFiles(string(specJSON), dir)
	if got != "nonexistent.go: missing" {
		t.Errorf("expected missing-file status unchanged, got %q", got)
	}
}

// TestCheckOutputFiles_OversizedFileOmitsContent confirms the size cap
// protects against inlining an unexpectedly large file (a bead that violated
// DECOMPOSE's ~200-line convention) rather than silently blowing up context.
func TestCheckOutputFiles_OversizedFileOmitsContent(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", maxInlineFileBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(big), 0644); err != nil {
		t.Fatal(err)
	}

	specJSON, err := json.Marshal(struct {
		OutputFiles []string `json:"output_files"`
	}{OutputFiles: []string{"big.go"}})
	if err != nil {
		t.Fatal(err)
	}

	got := checkOutputFiles(string(specJSON), dir)
	if !strings.Contains(got, "content omitted") {
		t.Errorf("expected oversized file's content to be omitted, got:\n%s", got)
	}
	if strings.Contains(got, big) {
		t.Errorf("oversized file's content should not have been inlined")
	}
}
