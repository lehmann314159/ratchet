package execution

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBakeoffSpecs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.json")
	os.WriteFile(p, []byte(`{"full_text":"do it","output_files":["a.go"],"exit_criteria":["go test ./..."]}`), 0o644)

	specs, err := loadBakeoffSpecs("thin:" + p)
	if err != nil {
		t.Fatalf("loadBakeoffSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0].Label != "thin" || specs[0].FullText != "do it" || specs[0].OutputFiles[0] != "a.go" {
		t.Fatalf("unexpected parse: %+v", specs)
	}

	if _, err := loadBakeoffSpecs("noColon" + p); err == nil {
		t.Error("expected error for entry without label:path form")
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"output_files":["a.go"]}`), 0o644)
	if _, err := loadBakeoffSpecs("x:" + bad); err == nil {
		t.Error("expected error for spec missing full_text")
	}
}

func TestResetWorkDirExcludesTraces(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "work")
	os.WriteFile(filepath.Join(src, "keep.go"), []byte("package main"), 0o644)
	os.MkdirAll(filepath.Join(src, "traces", "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "traces", "sub", "t.log"), []byte("x"), 0o644)

	// Pre-existing content in dst must be wiped.
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "stale.go"), []byte("old"), 0o644)

	if err := resetWorkDir(src, dst); err != nil {
		t.Fatalf("resetWorkDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "keep.go")); err != nil {
		t.Error("keep.go should have been copied")
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.go")); !os.IsNotExist(err) {
		t.Error("stale.go should have been removed")
	}
	if _, err := os.Stat(filepath.Join(dst, "traces")); !os.IsNotExist(err) {
		t.Error("traces/ must be excluded from the copy")
	}
}

func TestRankResult(t *testing.T) {
	if rankResult(bakeoffResult{TestsPass: true}) <= rankResult(bakeoffResult{Compiles: true}) {
		t.Error("tests-pass must outrank compiles-only")
	}
	if rankResult(bakeoffResult{WriteCalls: 1}) <= rankResult(bakeoffResult{}) {
		t.Error("wrote-something must outrank wrote-nothing")
	}
}
