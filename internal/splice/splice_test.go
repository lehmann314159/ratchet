package splice

import (
	"strings"
	"testing"
)

// TestSharedFileClobber reproduces the checkers-v8 (project 98) bead 628/629
// incident: bead A's REFINE_TESTS_WRITE assembles a fresh shared test file
// containing TestFoo. Bead B then runs its own cycle-1 REFINE_TESTS_WRITE
// against the same file path and writes only TestBar. The fix in
// RefineTestsWrite.Run (internal/verbs/refine_tests.go) must splice TestBar
// onto bead A's existing content — via splice.Replace, since the target file
// is non-empty — rather than calling Assemble with only TestBar and silently
// discarding TestFoo. This test exercises that splice.Replace call directly.
func TestSharedFileClobber(t *testing.T) {
	// Bead A: fresh file, assembled from scratch (originalSrc == "" case).
	fooBody := `func TestFoo(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("math is broken")
	}
}`
	afterBeadA, err := Assemble("checkers", []string{fooBody})
	if err != nil {
		t.Fatalf("Assemble (bead A): %v", err)
	}
	if !strings.Contains(afterBeadA, "func TestFoo") {
		t.Fatalf("bead A output missing TestFoo:\n%s", afterBeadA)
	}

	// Bead B: cycle-1 write against the same path, which now already holds
	// bead A's content. originalSrc is non-empty, so the fixed code path
	// splices via Replace instead of re-Assembling from only bead B's funcs.
	barBody := `func TestBar(t *testing.T) {
	if 2+2 != 4 {
		t.Fatal("math is broken")
	}
}`
	afterBeadB, err := Replace(afterBeadA, "TestBar", barBody)
	if err != nil {
		t.Fatalf("Replace (bead B): %v", err)
	}

	if !strings.Contains(afterBeadB, "func TestFoo") {
		t.Fatalf("bead B's write clobbered bead A's TestFoo — file now:\n%s", afterBeadB)
	}
	if !strings.Contains(afterBeadB, "func TestBar") {
		t.Fatalf("bead B's own TestBar missing from result:\n%s", afterBeadB)
	}

	funcs, err := FuncMap(afterBeadB)
	if err != nil {
		t.Fatalf("FuncMap on final content: %v", err)
	}
	if _, ok := funcs["TestFoo"]; !ok {
		t.Error("TestFoo not present in final parsed function set")
	}
	if _, ok := funcs["TestBar"]; !ok {
		t.Error("TestBar not present in final parsed function set")
	}
}

// TestAssembleDiscardsOnlyWhenCallerPassesNoPriorContent documents the
// invariant the refine_tests.go fix depends on: Assemble always builds a file
// from exactly the funcs it's given, with no memory of anything previously on
// disk. It is only safe to call when the caller has confirmed no prior
// content exists at the target path (originalSrc == "") — otherwise Replace
// (which preserves anything not explicitly named) must be used instead, as in
// TestSharedFileClobber above.
func TestAssembleDiscardsOnlyWhenCallerPassesNoPriorContent(t *testing.T) {
	fooBody := `func TestFoo(t *testing.T) {}`
	first, err := Assemble("checkers", []string{fooBody})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	barBody := `func TestBar(t *testing.T) {}`
	second, err := Assemble("checkers", []string{barBody})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	if strings.Contains(second, "TestFoo") {
		t.Fatal("expected Assemble to have no memory of prior content")
	}
	_ = first
}

// TestReplaceSyncsImports confirms Replace keeps the import block in sync
// with whatever the swapped-in function body actually needs, in both
// directions: adding a newly-needed import, and dropping one that's no
// longer used once the old body is gone. Replace previously left the import
// block untouched, so a revision introducing (or removing) a package
// dependency produced a file that failed to compile with no way for the
// model to fix it (write_function only supplies function bodies, never
// imports).
func TestReplaceSyncsImports(t *testing.T) {
	fooBody := `func TestFoo(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("bad")
	}
}`
	assembled, err := Assemble("pkg", []string{fooBody})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if strings.Contains(assembled, `"sort"`) {
		t.Fatal("sort should not be imported yet")
	}

	revised := `func TestFoo(t *testing.T) {
	s := []int{3, 1, 2}
	sort.Ints(s)
	if s[0] != 1 {
		t.Fatal("bad")
	}
}`
	result, err := Replace(assembled, "TestFoo", revised)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if !strings.Contains(result, `"sort"`) {
		t.Fatalf("expected sort import to be added:\n%s", result)
	}

	// Revise again, dropping the sort usage — the now-stale import must go too.
	final := `func TestFoo(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("bad")
	}
}`
	result2, err := Replace(result, "TestFoo", final)
	if err != nil {
		t.Fatalf("Replace 2: %v", err)
	}
	if strings.Contains(result2, `"sort"`) {
		t.Fatalf("expected stale sort import to be removed:\n%s", result2)
	}
}
