package verbs

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
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
		[]string{"calc_test.go"}, []string{"calc.go"}, []string{"TestAdd"}, "Add returns the sum of a and b.")

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
		[]string{"store_test.go"}, []string{"store.go"}, []string{"TestLookup"}, "Lookup returns the values for a key.")

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
		[]string{"reg_test.go"}, []string{"reg.go"}, []string{"TestNormalize"}, "Normalize trims and lowercases the input.")

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
		[]string{"parse_test.go"}, []string{"parse.go"}, []string{"TestParse"}, "Parse converts a decimal string to an int.")

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

// --- phase 3: spec cross-check + setup consistency ---

func hasSpecNote(notes []string, needle string) bool {
	for _, n := range notes {
		if strings.Contains(n, "appears nowhere in") && strings.Contains(n, needle) {
			return true
		}
	}
	return false
}

func TestCritiquePrepass_SpecCrossCheck_FlagsAbsentConstant(t *testing.T) {
	// The spec never names PUSH_CONST (nor the literal 10) — the test's asserted
	// mnemonic is invented. Emitted as a note, not a seed.
	spec := "Disassemble converts bytecode to text. Emit two-letter mnemonics: PC for a constant push, LD for a variable load, ST for a store."
	rep := runCritiquePrepass(context.Background(), fixtureDir(t, "spec_mismatch"),
		[]string{"dis_test.go"}, []string{"dis.go"}, []string{"TestDisassemble"}, spec)

	if !hasSpecNote(rep.Notes, "PUSH_CONST 10") {
		t.Fatalf("expected a spec cross-check note for the asserted \"PUSH_CONST 10\"; notes: %v", rep.Notes)
	}
	for _, s := range rep.Seeds {
		if s.Kind == "spec_contradiction" {
			t.Errorf("spec cross-check must be a note, not a seed: %+v", s)
		}
	}
}

func TestCritiquePrepass_SpecCrossCheck_NoFlagWhenSpecNamesTheParts(t *testing.T) {
	// The spec names PUSH_CONST and uses the literal 10; a test that composes
	// "PUSH_CONST 10" is not contradicting anything.
	spec := "Use the mnemonic PUSH_CONST for a constant push. For x=10 (fresh env) the output is PUSH_CONST then STORE."
	rep := runCritiquePrepass(context.Background(), fixtureDir(t, "spec_mismatch"),
		[]string{"dis_test.go"}, []string{"dis.go"}, []string{"TestDisassemble"}, spec)
	if hasSpecNote(rep.Notes, "PUSH_CONST 10") {
		t.Fatalf("did not expect a spec cross-check note when the spec names the parts: %v", rep.Notes)
	}
}

func TestCritiquePrepass_SetupInconsistency(t *testing.T) {
	spec := "Register adds a name to the environment's slot table."
	rep := runCritiquePrepass(context.Background(), fixtureDir(t, "setup_gap"),
		[]string{"env_test.go"}, []string{"env.go"}, []string{"TestRegister"}, spec)

	var got *critiqueSeed
	for i := range rep.Seeds {
		if rep.Seeds[i].Kind == "setup_inconsistency" {
			got = &rep.Seeds[i]
		}
	}
	if got == nil {
		t.Fatalf("expected a setup_inconsistency seed (NoSetup panics, WithSetup inits e.Slots); seeds: %+v", rep.Seeds)
	}
	if !strings.Contains(got.Subtest, "NoSetup") {
		t.Errorf("seed should name the failing subtest NoSetup; got %q", got.Subtest)
	}
	if !strings.Contains(got.Evidence, "WithSetup") {
		t.Errorf("evidence should name the sibling with the extra setup:\n%s", got.Evidence)
	}
}

// --- corpus-backed checks (skipped when the immutable qual corpus is absent) ---

func corpusRoot() string {
	if root := os.Getenv("RATCHET_QUAL_CORPUS"); root != "" {
		return root
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Documents", "ratchet-projects", "qual-corpus-p48")
}

func corpusCase(t *testing.T, dispatchDir string) (folder string, ok bool) {
	t.Helper()
	folder = filepath.Join(corpusRoot(), "verb-io", dispatchDir, "folder")
	if _, err := os.Stat(folder); err != nil {
		return "", false
	}
	return folder, true
}

// beadSpecText reads a bead's current-revision full_text (+ nothing else — the
// design-doc excerpt lives in the running verb, not the corpus) from the
// immutable corpus DB. Returns "" if the corpus or the bead is absent.
func beadSpecText(t *testing.T, root string, beadID int64) string {
	t.Helper()
	dbPath := filepath.Join(root, "corpus.db")
	if _, err := os.Stat(dbPath); err != nil {
		return ""
	}
	d, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open corpus db: %v", err)
	}
	defer d.Close()
	var full string
	err = d.QueryRowContext(context.Background(), `
		SELECT br.full_text FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID).Scan(&full)
	if err != nil {
		t.Fatalf("load bead %d spec: %v", beadID, err)
	}
	return full
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
		[]string{"compiler_test.go"}, []string{"compiler.go"}, []string{"TestCompiler"}, beadSpecText(t, corpusRoot(), 316))
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
		[]string{"parser_test.go"}, []string{"parser.go"}, []string{"TestParser"}, beadSpecText(t, corpusRoot(), 314))
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

// TestCritiquePrepass_Corpus_Profile dumps the pre-pass seed profile for every
// p48 CRITIQUE dispatch. Diagnostic only — run with RATCHET_PREPASS_PROFILE=1.
func TestCritiquePrepass_Corpus_Profile(t *testing.T) {
	if os.Getenv("RATCHET_PREPASS_PROFILE") == "" {
		t.Skip("set RATCHET_PREPASS_PROFILE=1 to run")
	}
	cases := []struct {
		dispatch, testFile, implFile, fn string
		bead                             int64
	}{
		{"007-REFINE_TESTS_CRITIQUE-p48-b313-c1", "lexer_test.go", "lexer.go", "TestLexer", 313},
		{"014-REFINE_TESTS_CRITIQUE-p48-b314-c1", "parser_test.go", "parser.go", "TestParser", 314},
		{"017-REFINE_TESTS_CRITIQUE-p48-b314-c2", "parser_test.go", "parser.go", "TestParser", 314},
		{"027-REFINE_TESTS_CRITIQUE-p48-b315-c1", "env_test.go", "env.go", "TestEnvironment", 315},
		{"034-REFINE_TESTS_CRITIQUE-p48-b316-c1", "compiler_test.go", "compiler.go", "TestCompiler", 316},
		{"037-REFINE_TESTS_CRITIQUE-p48-b316-c2", "compiler_test.go", "compiler.go", "TestCompiler", 316},
		{"044-REFINE_TESTS_CRITIQUE-p48-b317-c1", "vm_test.go", "vm.go", "TestVM", 317},
		{"051-REFINE_TESTS_CRITIQUE-p48-b318-c1", "handlers_test.go", "handlers.go", "TestHandlers", 318},
		{"062-REFINE_TESTS_CRITIQUE-p48-b320-c1", "integration_test.go", "", "TestPersistence", 320},
		{"065-REFINE_TESTS_CRITIQUE-p48-b320-c2", "integration_test.go", "", "TestPersistence", 320},
		{"072-REFINE_TESTS_CRITIQUE-p48-b321-c1", "integration_test.go", "", "TestRuntimeError", 321},
		{"075-REFINE_TESTS_CRITIQUE-p48-b321-c2", "integration_test.go", "", "TestRuntimeError", 321},
	}
	for _, tc := range cases {
		folder, ok := corpusCase(t, tc.dispatch)
		if !ok {
			t.Skip("qual corpus not present")
		}
		var impls []string
		if tc.implFile != "" {
			impls = []string{tc.implFile}
		}
		rep := runCritiquePrepass(context.Background(), folder,
			[]string{tc.testFile}, impls, []string{tc.fn}, beadSpecText(t, corpusRoot(), tc.bead))
		t.Logf("%s: compiled=%v ran=%v seeds=%d notes=%d", tc.dispatch, rep.Compiled, rep.Ran, len(rep.Seeds), len(rep.Notes))
		for _, s := range rep.Seeds {
			t.Logf("    seed[%s/%s] %s :: %s", s.Kind, s.Confidence, s.Subtest, firstNLines(s.Evidence, 1))
		}
		for _, n := range rep.Notes {
			t.Logf("    note: %s", firstNLines(n, 1))
		}
	}
}

func firstNLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " / ")
}

// TestCritiquePrepass_Corpus_GoodCases_NoNewSeeds is the phase-3 false-positive
// bar: on the CRITIQUE cases the incumbent (and JUDGE) both left clean, the
// static checks must add nothing. b317-c1 and b314-c2 are the labelled "good"
// cases from docs/fleet-qualification.md.
func TestCritiquePrepass_Corpus_GoodCases_NoNewSeeds(t *testing.T) {
	cases := []struct {
		dispatch string
		bead     int64
		testFile string
		implFile string
		fn       string
	}{
		{"044-REFINE_TESTS_CRITIQUE-p48-b317-c1", 317, "vm_test.go", "vm.go", "TestVM"},
		{"017-REFINE_TESTS_CRITIQUE-p48-b314-c2", 314, "parser_test.go", "parser.go", "TestParser"},
	}
	for _, tc := range cases {
		t.Run(tc.dispatch, func(t *testing.T) {
			folder, ok := corpusCase(t, tc.dispatch)
			if !ok {
				t.Skip("qual corpus not present")
			}
			rep := runCritiquePrepass(context.Background(), folder,
				[]string{tc.testFile}, []string{tc.implFile}, []string{tc.fn},
				beadSpecText(t, corpusRoot(), tc.bead))
			// Phase-1/2 seeds (a stub panic) are fine on a "good" case — the
			// test is correct, it just panics against the unimplemented stub.
			// The bar here is that the phase-3 static checks add nothing.
			for _, s := range rep.Seeds {
				if s.Kind == "setup_inconsistency" || s.Kind == "spec_contradiction" {
					t.Errorf("phase-3 false positive on a good case: %+v", s)
				}
			}
			if hasSpecNote(rep.Notes, "") {
				t.Logf("spec cross-check notes on %s (acceptable — nudges only): %v", tc.dispatch, rep.Notes)
			}
		})
	}
}
