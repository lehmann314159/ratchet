package verbs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureDir(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "prepass", name))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// TestCritiquePrepass_CleanTestAgainstStub_NoSeeds is the phase-1 gate: a
// correct test whose only "failures" are the scaffold stub returning zero
// values must produce no seeds (matching the baseline CRITIQUE's all_correct).
func TestCritiquePrepass_CleanTestAgainstStub_NoSeeds(t *testing.T) {
	rep := runCritiquePrepass(context.Background(), fixtureDir(t, "clean"),
		[]string{"calc_test.go"}, []string{"calc.go"}, []string{"TestAdd"})

	if !rep.Compiled {
		t.Fatalf("expected fixture to compile; compile out:\n%s", rep.CompileOut)
	}
	if !rep.Ran {
		t.Fatal("expected the pre-pass to run the tests")
	}
	if len(rep.Seeds) != 0 {
		t.Fatalf("expected 0 seeds on a clean-against-stub test, got %d: %+v", len(rep.Seeds), rep.Seeds)
	}
	if len(rep.Notes) != 0 {
		t.Fatalf("expected 0 notes (all failures are stub zero-values), got: %v", rep.Notes)
	}
}

// TestCritiquePrepass_PanicThroughStub emits a panic seed and tags it
// stub_explained because the panicking value flows from this bead's own stub.
func TestCritiquePrepass_PanicThroughStub(t *testing.T) {
	rep := runCritiquePrepass(context.Background(), fixtureDir(t, "panic_stub"),
		[]string{"store_test.go"}, []string{"store.go"}, []string{"TestLookup"})

	if len(rep.Seeds) != 1 {
		t.Fatalf("expected exactly 1 seed, got %d: %+v", len(rep.Seeds), rep.Seeds)
	}
	s := rep.Seeds[0]
	if s.Kind != "panic" {
		t.Errorf("kind = %q, want panic", s.Kind)
	}
	if s.Confidence != seedStubExplained {
		t.Errorf("confidence = %q, want stub_explained", s.Confidence)
	}
	if !strings.Contains(s.Subtest, "TestLookup") {
		t.Errorf("subtest = %q, want it to name TestLookup", s.Subtest)
	}
	if !strings.Contains(s.Evidence, "index out of range") {
		t.Errorf("evidence missing the panic message:\n%s", s.Evidence)
	}
	if !strings.Contains(s.Evidence, "store_test.go:") {
		t.Errorf("evidence missing the fault location:\n%s", s.Evidence)
	}
}

// TestCritiquePrepass_PanicInTestLogic tags a panic that does not route through
// this bead's code as review (the test's own setup is suspect).
func TestCritiquePrepass_PanicInTestLogic(t *testing.T) {
	rep := runCritiquePrepass(context.Background(), fixtureDir(t, "panic_testlogic"),
		[]string{"reg_test.go"}, []string{"reg.go"}, []string{"TestNormalize"})

	if len(rep.Seeds) != 1 {
		t.Fatalf("expected exactly 1 seed, got %d: %+v", len(rep.Seeds), rep.Seeds)
	}
	s := rep.Seeds[0]
	if s.Kind != "panic" {
		t.Errorf("kind = %q, want panic", s.Kind)
	}
	if s.Confidence != seedReview {
		t.Errorf("confidence = %q, want review", s.Confidence)
	}
	if !strings.Contains(s.Evidence, "nil map") {
		t.Errorf("evidence missing the panic message:\n%s", s.Evidence)
	}
}

// TestCritiquePrepass_CompileErrorInTestFile is a high-confidence seed.
func TestCritiquePrepass_CompileErrorInTestFile(t *testing.T) {
	rep := runCritiquePrepass(context.Background(), fixtureDir(t, "compile_err"),
		[]string{"parse_test.go"}, []string{"parse.go"}, []string{"TestParse"})

	if rep.Compiled {
		t.Fatal("expected the fixture NOT to compile")
	}
	if rep.Ran {
		t.Fatal("expected the pre-pass to skip the run step on a compile failure")
	}
	if len(rep.Seeds) == 0 {
		t.Fatal("expected at least one compile_error seed")
	}
	for _, s := range rep.Seeds {
		if s.Kind != "compile_error" || s.Confidence != seedHigh {
			t.Errorf("got kind=%q confidence=%q, want compile_error/high", s.Kind, s.Confidence)
		}
		if !strings.Contains(s.Evidence, "parse_test.go:") {
			t.Errorf("evidence should point at the test file:\n%s", s.Evidence)
		}
	}
}

// --- corpus-backed checks (skipped when the immutable qual corpus is absent) ---

func corpusCase(t *testing.T, dispatchDir string) (folder string, ok bool) {
	t.Helper()
	root := os.Getenv("RATCHET_QUAL_CORPUS")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, "Documents", "ratchet-projects", "qual-corpus-p48")
	}
	folder = filepath.Join(root, "verb-io", dispatchDir, "folder")
	if _, err := os.Stat(folder); err != nil {
		return "", false
	}
	return folder, true
}

// TestCritiquePrepass_Corpus_b316c1_NoSeeds — the doc's phase-1 gate on real
// data: scaffold Compile(){return nil,nil} → every subtest fails "expected N,
// got 0", no panics, compiles clean → the pre-pass stays quiet.
func TestCritiquePrepass_Corpus_b316c1_NoSeeds(t *testing.T) {
	folder, ok := corpusCase(t, "034-REFINE_TESTS_CRITIQUE-p48-b316-c1")
	if !ok {
		t.Skip("qual corpus not present")
	}
	rep := runCritiquePrepass(context.Background(), folder,
		[]string{"compiler_test.go"}, []string{"compiler.go"}, []string{"TestCompiler"})
	if !rep.Compiled || !rep.Ran {
		t.Fatalf("compiled=%v ran=%v compileOut:\n%s", rep.Compiled, rep.Ran, rep.CompileOut)
	}
	if len(rep.Seeds) != 0 {
		t.Fatalf("expected 0 seeds on b316-c1, got %d: %+v", len(rep.Seeds), rep.Seeds)
	}
}

// TestCritiquePrepass_Corpus_b314c1_StubPanics — ParseProgram(){return nil,nil}
// → the AST-walking subtests nil-deref. All panic seeds must be stub_explained
// (the tests are fine once the parser is implemented; the real defect there is
// a spec-interpretation one the model still has to find).
func TestCritiquePrepass_Corpus_b314c1_StubPanics(t *testing.T) {
	folder, ok := corpusCase(t, "014-REFINE_TESTS_CRITIQUE-p48-b314-c1")
	if !ok {
		t.Skip("qual corpus not present")
	}
	rep := runCritiquePrepass(context.Background(), folder,
		[]string{"parser_test.go"}, []string{"parser.go"}, []string{"TestParser"})
	if !rep.Compiled {
		t.Fatalf("expected compile; out:\n%s", rep.CompileOut)
	}
	sawPanic := false
	for _, s := range rep.Seeds {
		if s.Kind != "panic" {
			continue
		}
		sawPanic = true
		if s.Confidence != seedStubExplained {
			t.Errorf("panic seed %q: confidence %q, want stub_explained", s.Subtest, s.Confidence)
		}
	}
	if !sawPanic {
		t.Fatalf("expected at least one panic seed from the nil ParseProgram stub; seeds: %+v", rep.Seeds)
	}
}
