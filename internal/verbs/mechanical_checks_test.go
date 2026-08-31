package verbs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ratchet/internal/execcheck"
)

func TestDetectLang(t *testing.T) {
	// folderPath is empty for all cases — forces the output_files fallback.
	cases := []struct {
		name        string
		outputFiles []string
		want        string
	}{
		{"go file", []string{"main.go", "game.go"}, "go"},
		{"go test file", []string{"do_not_use_this_test.go"}, "go"},
		{"python", []string{"app.py", "requirements.txt"}, "python"},
		{"rust", []string{"src/main.rs"}, "rust"},
		{"cargo toml", []string{"Cargo.toml"}, "rust"},
		{"typescript", []string{"index.tsx"}, "typescript"},
		{"javascript", []string{"index.jsx"}, "javascript"},
		{"unknown", []string{"README.md", "Makefile"}, ""},
		{"empty", []string{}, ""},
		{"mixed picks first match", []string{"templates/index.html", "main.go"}, "go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectLang("", tc.outputFiles)
			if got != tc.want {
				t.Errorf("detectLang(%v) = %q, want %q", tc.outputFiles, got, tc.want)
			}
		})
	}
}

func TestHasTestGoFile(t *testing.T) {
	t.Run("only do_not_use_this_test.go — not a real test file, returns false", func(t *testing.T) {
		if hasTestGoFile([]string{"game.go", "ai.go", "do_not_use_this_test.go"}) {
			t.Error("expected false: apiCheckTestFilename must not trigger REFINE_TESTS on its own")
		}
	})

	t.Run("real behavioral test file alongside it — returns true", func(t *testing.T) {
		if !hasTestGoFile([]string{"game.go", "do_not_use_this_test.go", "game_test.go"}) {
			t.Error("expected true: a genuine behavioral test file is present")
		}
	})

	t.Run("no test files at all — returns false", func(t *testing.T) {
		if hasTestGoFile([]string{"game.go", "ai.go"}) {
			t.Error("expected false: no _test.go files present")
		}
	})
}

func TestGoFixBeadSpec(t *testing.T) {
	t.Run("has go file — test file added to output_files, criteria unchanged", func(t *testing.T) {
		b := &ParsedBead{
			OutputFiles:  []string{"game.go"},
			ExitCriteria: []string{"go test -v . -run=TestFoo", "go test -v . -run=TestBar"},
		}
		fixed := goFixBeadSpec(b)
		if !fixed {
			t.Fatal("expected fix to be applied")
		}
		if !hasTestGoFile(b.OutputFiles) {
			t.Errorf("expected a *_test.go to be added to output_files, got %v", b.OutputFiles)
		}
		for _, c := range b.ExitCriteria {
			if c == "go build ./..." {
				t.Errorf("criteria should not be downgraded when .go files are present, got %q", c)
			}
		}
	})

	t.Run("no go files at all — criteria downgraded to go build (content-only bead)", func(t *testing.T) {
		b := &ParsedBead{
			OutputFiles:  []string{"templates/index.html"},
			ExitCriteria: []string{"go test -v ./... -run=TestTemplateParsing"},
		}
		fixed := goFixBeadSpec(b)
		if !fixed {
			t.Fatal("expected fix to be applied")
		}
		if b.ExitCriteria[0] != "go build ./..." {
			t.Errorf("expected criterion to be downgraded to go build ./..., got %q", b.ExitCriteria[0])
		}
		if hasTestGoFile(b.OutputFiles) {
			t.Errorf("no test file should be added when there are no .go files, got %v", b.OutputFiles)
		}
	})

	t.Run("with test file present — adds grep guard for -run TestFoo", func(t *testing.T) {
		b := &ParsedBead{
			OutputFiles:  []string{"game.go", "game_test.go"},
			ExitCriteria: []string{"go test -v . -run=TestFoo"},
		}
		fixed := goFixBeadSpec(b)
		if !fixed {
			t.Fatal("expected grep guard to be added")
		}
		want := "grep -q 'func TestFoo' game_test.go && go test -v . -run=TestFoo"
		if b.ExitCriteria[0] != want {
			t.Errorf("criterion = %q, want %q", b.ExitCriteria[0], want)
		}
	})

	t.Run("non-go-test criterion — no fix", func(t *testing.T) {
		b := &ParsedBead{
			OutputFiles:  []string{"main.go"},
			ExitCriteria: []string{"go build ./..."},
		}
		fixed := goFixBeadSpec(b)
		if fixed {
			t.Fatal("expected no fix for go build criterion without do_not_use_this_test.go")
		}
	})

	t.Run("layout bead with do_not_use_this_test.go — go build upgraded to go test -c, no content-check added", func(t *testing.T) {
		b := &ParsedBead{
			OutputFiles:  []string{"game.go", "ai.go", "main.go", "do_not_use_this_test.go"},
			ExitCriteria: []string{"go build ./..."},
		}
		fixed := goFixBeadSpec(b)
		if !fixed {
			t.Fatal("expected fix to be applied")
		}
		// No "grep -q ... do_not_use_this_test.go" is appended: that file's
		// content is deterministically generated (writeAPICheckTest) and must
		// never be exit-criteria-checked — see stripAPICheckFileContentChecks.
		want := "go test -c -o /dev/null ./..."
		if b.ExitCriteria[0] != want {
			t.Errorf("criterion = %q, want %q", b.ExitCriteria[0], want)
		}
	})

	t.Run("layout bead with do_not_use_this_test.go — inherited content-check clause stripped, go build still upgraded", func(t *testing.T) {
		// A criterion inherited from an older revision (or an earlier verb)
		// that already carries a do_not_use_this_test.go content-check gets
		// that clause stripped unconditionally, regardless of source — the
		// checkers fixture's own bead 636 revision 1 had exactly this shape.
		b := &ParsedBead{
			OutputFiles:  []string{"game.go", "do_not_use_this_test.go"},
			ExitCriteria: []string{"go build ./... && grep -q '^var _' do_not_use_this_test.go"},
		}
		fixed := goFixBeadSpec(b)
		if !fixed {
			t.Fatal("expected go build upgrade and content-check stripping to be applied")
		}
		want := "go test -c -o /dev/null ./..."
		if b.ExitCriteria[0] != want {
			t.Errorf("criterion = %q, want %q", b.ExitCriteria[0], want)
		}
	})

	t.Run("owns two test files, no -run flag — vacuous-pass guard greps all owned test files, not just the first", func(t *testing.T) {
		// Reproduces the Stage 2 audit finding: testFileForName's fallback for
		// a generic "Test" name always resolves to the first *_test.go file,
		// so a bead owning two test files could grep only the first — a false
		// "nothing written" verdict if the model wrote real tests to the
		// second file instead.
		b := &ParsedBead{
			OutputFiles:  []string{"game.go", "game_test.go", "ai_test.go"},
			ExitCriteria: []string{"go test -v ."},
		}
		fixed := goFixBeadSpec(b)
		if !fixed {
			t.Fatal("expected the vacuous-pass guard to be added")
		}
		want := "grep -q 'func Test' game_test.go ai_test.go && go test -v ."
		if b.ExitCriteria[0] != want {
			t.Errorf("criterion = %q, want %q", b.ExitCriteria[0], want)
		}
	})

	t.Run("layout bead with do_not_use_this_test.go — already correct form, idempotent", func(t *testing.T) {
		b := &ParsedBead{
			OutputFiles:  []string{"game.go", "do_not_use_this_test.go"},
			ExitCriteria: []string{"go test -c -o /dev/null ./..."},
		}
		fixed := goFixBeadSpec(b)
		if fixed {
			t.Fatal("expected no fix when criterion is already in correct form")
		}
		want := "go test -c -o /dev/null ./..."
		if b.ExitCriteria[0] != want {
			t.Errorf("criterion should be unchanged, got %q", b.ExitCriteria[0])
		}
	})

	t.Run("real-world checkers-try-1 bead 684 case: positive/negated duplicate pattern collapses to a satisfiable criterion", func(t *testing.T) {
		// This is the actual criterion ADJUDICATE stored on bead 684's revision
		// 12 (checkers-try-1, project 100), reproduced byte-for-byte: it wanted
		// to verify a compile-time assertion moved out of game.go. The bare
		// positive clause (intended to confirm presence somewhere — the only
		// candidate is apiCheckTestFilename, which must never be content-checked,
		// see stripAPICheckFileContentChecks) and the bare negated clause (must
		// be absent from game.go) share identical pattern text. The old fixBareGrepFile
		// routed both to game.go independently, producing "must be present in
		// game.go AND absent from game.go" — impossible to ever satisfy, so the
		// bead retried forever. The fix drops the positive duplicate and keeps
		// only the negated one. Also picks up escapeStrayGrepAsterisks along the
		// way: the surviving negated clause's bare `*` gets escaped to `\*`,
		// since an unescaped `*` in POSIX basic regex would otherwise make it
		// vacuously always-true regardless of game.go's real content (the same
		// bug confirmed separately on the othello fixture).
		b := &ParsedBead{
			OutputFiles: []string{"game.go", "ai.go", "handlers.go", "main.go", "go.mod", "layout_test.go"},
			ExitCriteria: []string{
				"grep -q 'func TestLayout' layout_test.go && grep -q 'var _ func() *Game = NewGame' game.go && ! grep -q 'var _ func() *Game = NewGame' && go test -v . -run TestLayout",
			},
		}
		fixed := goFixBeadSpec(b)
		if !fixed {
			t.Fatal("expected the duplicate pattern to be collapsed")
		}
		want := "grep -q 'func TestLayout' layout_test.go && ! grep -q 'var _ func() \\*Game = NewGame' game.go && go test -v . -run TestLayout"
		if b.ExitCriteria[0] != want {
			t.Errorf("criterion = %q, want %q", b.ExitCriteria[0], want)
		}
	})
}

func TestFixBareGrepFile_Negated(t *testing.T) {
	t.Run("negated bare grep gets a filename, prefix preserved", func(t *testing.T) {
		result, ok := fixBareGrepFile("! grep -q 'var _ func() *Game = NewGame'", []string{"game.go", "game_test.go"})
		if !ok {
			t.Fatal("expected a fix")
		}
		want := "! grep -q 'var _ func() *Game = NewGame' game.go"
		if result != want {
			t.Errorf("result = %q, want %q", result, want)
		}
	})

	t.Run("negated grep that already has a filename is left alone", func(t *testing.T) {
		criterion := "! grep -q 'var _ func() *Game = NewGame' game.go"
		result, ok := fixBareGrepFile(criterion, []string{"game.go"})
		if ok {
			t.Errorf("expected no fix, got %q", result)
		}
	})

	t.Run("file-qualified positive collapses against a bare negated of the same pattern+file", func(t *testing.T) {
		// The checkers-684 shape without relying on fixFileBasedGoTest to first
		// strip the positive clause's file: pass 1 resolves the bare negated to
		// game.go, pass 2 then sees both target game.go and drops the positive.
		criterion := "grep -q 'PAT' game.go && ! grep -q 'PAT'"
		result, ok := fixBareGrepFile(criterion, []string{"game.go", "ai.go"})
		if !ok {
			t.Fatal("expected a fix")
		}
		want := "! grep -q 'PAT' game.go"
		if result != want {
			t.Errorf("result = %q, want %q", result, want)
		}
	})

	t.Run("positive and negated of the same pattern but DIFFERENT files are both kept", func(t *testing.T) {
		criterion := "grep -q 'PAT' a.go && ! grep -q 'PAT' b.go"
		result, ok := fixBareGrepFile(criterion, []string{"a.go", "b.go"})
		if ok {
			t.Errorf("expected no fix (both files explicit, no contradiction), got %q", result)
		}
		_ = result
	})
}

func TestFixFileBasedGoTest_PreservesGrepGuardFile(t *testing.T) {
	// The bug: an earlier version ran strings.Fields over the WHOLE compound
	// criterion and dropped every .go token — including handlers_test.go from a
	// leading grep guard — leaving a bare `grep -q '...'` that reads stdin and
	// always fails. It only operates on the `go test` clause now.
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{
			name: "grep guard .go arg is preserved; go test clause is untouched (already package form)",
			in:   "grep -q 'func TestFoo' foo_test.go && go test -v -run TestFoo ./...",
			want: "grep -q 'func TestFoo' foo_test.go && go test -v -run TestFoo ./...",
			ok:   false,
		},
		{
			name: "file-based go test clause is rewritten; grep guard file kept",
			in:   "grep -q 'func TestFoo' foo_test.go && go test ./foo_test.go -run TestFoo",
			want: "grep -q 'func TestFoo' foo_test.go && go test -run TestFoo .",
			ok:   true,
		},
		{
			name: "bare file-based go test still rewritten",
			in:   "go test ./foo_test.go -run TestFoo",
			want: "go test -run TestFoo .",
			ok:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := fixFileBasedGoTest(tc.in)
			if ok != tc.ok {
				t.Errorf("ok = %v, want %v", ok, tc.ok)
			}
			if strings.TrimSpace(got) != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestDeriveTestFileName(t *testing.T) {
	cases := []struct {
		name         string
		exitCriteria []string
		goFiles      []string
		want         string
	}{
		{
			name:         "run flag matches go file base",
			exitCriteria: []string{"go test -v . -run=TestGameLogic"},
			goFiles:      []string{"game.go"},
			want:         "game_test.go",
		},
		{
			name:         "run flag matches one of several go files by base name substring",
			exitCriteria: []string{"go test -v . -run=TestGameApplyMove"},
			goFiles:      []string{"main.go", "game.go", "ai.go"},
			want:         "game_test.go",
		},
		{
			name:         "no run flag — falls back to first go file",
			exitCriteria: []string{"go test ./..."},
			goFiles:      []string{"encode.go", "decode.go"},
			want:         "encode_test.go",
		},
		{
			name:         "run flag no match — falls back to first go file",
			exitCriteria: []string{"go test -v . -run=TestSomethingUnrelated"},
			goFiles:      []string{"widget.go"},
			want:         "widget_test.go",
		},
		{
			name:         "file in subdirectory preserves directory",
			exitCriteria: []string{"go test ./..."},
			goFiles:      []string{"internal/store/store.go"},
			want:         "internal/store/store_test.go",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveTestFileName(tc.exitCriteria, tc.goFiles)
			if got != tc.want {
				t.Errorf("deriveTestFileName(%v, %v) = %q, want %q", tc.exitCriteria, tc.goFiles, got, tc.want)
			}
		})
	}
}

func TestForwardFileReferenceChecks(t *testing.T) {
	t.Run("checkers-v6 bug: earlier bead references a later bead's subdirectory asset", func(t *testing.T) {
		beads := []ParsedBead{
			{
				Title:        "http-handlers",
				FullText:     `Implement InitServer: templates, err = template.ParseFiles("templates/index.html", "templates/board.html")`,
				OutputFiles:  []string{"handlers.go", "main.go"},
				ExitCriteria: []string{"go test -run TestHandlers ."},
			},
			{
				Title:        "templates",
				FullText:     "Create the HTML templates.",
				OutputFiles:  []string{"templates/index.html", "templates/board.html"},
				ExitCriteria: []string{"go build ./..."},
			},
		}
		got := forwardFileReferenceChecks(beads)
		if len(got) != 1 {
			t.Fatalf("expected 1 violation, got %d: %v", len(got), got)
		}
		if !strings.Contains(got[0], "http-handlers") || !strings.Contains(got[0], "templates/index.html") || !strings.Contains(got[0], "templates") {
			t.Errorf("violation message missing expected content: %q", got[0])
		}
	})

	t.Run("correct order — templates bead first, no violation", func(t *testing.T) {
		beads := []ParsedBead{
			{
				Title:        "templates",
				FullText:     "Create the HTML templates.",
				OutputFiles:  []string{"templates/index.html", "templates/board.html"},
				ExitCriteria: []string{"go build ./..."},
			},
			{
				Title:        "http-handlers",
				FullText:     `Implement InitServer: templates, err = template.ParseFiles("templates/index.html", "templates/board.html")`,
				OutputFiles:  []string{"handlers.go", "main.go"},
				ExitCriteria: []string{"go test -run TestHandlers ."},
			},
		}
		got := forwardFileReferenceChecks(beads)
		if len(got) != 0 {
			t.Errorf("expected no violations when the dependency bead runs first, got %v", got)
		}
	})

	t.Run("bare same-level filename reference is not flagged (avoids false positives)", func(t *testing.T) {
		beads := []ParsedBead{
			{
				Title:        "handlers",
				FullText:     "As defined in main.go, the server starts on port 8080.",
				OutputFiles:  []string{"handlers.go"},
				ExitCriteria: []string{"go build ./..."},
			},
			{
				Title:        "main",
				FullText:     "Wire up the server.",
				OutputFiles:  []string{"main.go"},
				ExitCriteria: []string{"go build ./..."},
			},
		}
		got := forwardFileReferenceChecks(beads)
		if len(got) != 0 {
			t.Errorf("bare filenames like main.go should not trigger the check, got %v", got)
		}
	})

	t.Run("reference to an earlier or own bead's file is not flagged", func(t *testing.T) {
		beads := []ParsedBead{
			{
				Title:        "templates",
				FullText:     "Create templates/index.html.",
				OutputFiles:  []string{"templates/index.html"},
				ExitCriteria: []string{"go build ./..."},
			},
			{
				Title:        "handlers",
				FullText:     `Uses templates/index.html from the previous bead.`,
				OutputFiles:  []string{"handlers.go"},
				ExitCriteria: []string{"go build ./..."},
			},
		}
		got := forwardFileReferenceChecks(beads)
		if len(got) != 0 {
			t.Errorf("referencing an earlier bead's own file should not be flagged, got %v", got)
		}
	})

	t.Run("no beads reference any later file — no violations", func(t *testing.T) {
		beads := []ParsedBead{
			{Title: "a", FullText: "stuff", OutputFiles: []string{"a.go"}, ExitCriteria: []string{"go build ./..."}},
			{Title: "b", FullText: "other stuff", OutputFiles: []string{"static/style.css"}, ExitCriteria: []string{"go build ./..."}},
		}
		got := forwardFileReferenceChecks(beads)
		if len(got) != 0 {
			t.Errorf("expected no violations, got %v", got)
		}
	})
}

func TestExtractRunTestName(t *testing.T) {
	cases := []struct {
		criterion string
		want      string
	}{
		{"go test -v . -run=TestFoo", "TestFoo"},
		{"go test -v ./... -run=TestApplyMove", "TestApplyMove"},
		{"go test ./...", ""},
		{"go build ./...", ""},
		{"go test -v . -run=TestFoo -count=1", "TestFoo"},
	}
	for _, tc := range cases {
		got := extractRunTestName(tc.criterion)
		if got != tc.want {
			t.Errorf("extractRunTestName(%q) = %q, want %q", tc.criterion, got, tc.want)
		}
	}
}

func TestFixRunFlagSeparator(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		want      string
		wantFixed bool
	}{
		{
			name:      "bead 135: space-separated names in quoted -run value",
			in:        "grep -q 'func TestHandleIndex' handlers_test.go && grep -q 'func TestHandleEval' handlers_test.go && go test -v -run 'TestHandleIndex TestHandleEval' ./...",
			want:      "grep -q 'func TestHandleIndex' handlers_test.go && grep -q 'func TestHandleEval' handlers_test.go && go test -v -run 'TestHandleIndex|TestHandleEval' ./...",
			wantFixed: true,
		},
		{
			name:      "three names, double quotes",
			in:        `go test -run "TestA TestB TestC" ./...`,
			want:      `go test -run "TestA|TestB|TestC" ./...`,
			wantFixed: true,
		},
		{
			name:      "equals form",
			in:        "go test -run='TestFoo TestBar' .",
			want:      "go test -run='TestFoo|TestBar' .",
			wantFixed: true,
		},
		{
			name:      "already correct alternation — untouched",
			in:        "go test -run 'TestHandleIndex|TestHandleEval' ./...",
			want:      "go test -run 'TestHandleIndex|TestHandleEval' ./...",
			wantFixed: false,
		},
		{
			name:      "single quoted name — untouched",
			in:        "go test -run 'TestFoo' ./...",
			want:      "go test -run 'TestFoo' ./...",
			wantFixed: false,
		},
		{
			name:      "unquoted single name — untouched",
			in:        "go test -v . -run TestFoo",
			want:      "go test -v . -run TestFoo",
			wantFixed: false,
		},
		{
			name:      "intentional space in a subtest regexp — untouched (not all tokens are TestXxx)",
			in:        "go test -run 'TestParse/two words' .",
			want:      "go test -run 'TestParse/two words' .",
			wantFixed: false,
		},
		{
			name:      "not a go test criterion — untouched",
			in:        "grep -q 'func Foo bar' foo.go",
			want:      "grep -q 'func Foo bar' foo.go",
			wantFixed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, fixed := fixRunFlagSeparator(tc.in)
			if fixed != tc.wantFixed {
				t.Errorf("fixed = %v, want %v", fixed, tc.wantFixed)
			}
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestGoFixBeadSpec_Bead135RunFlag reproduces the exprvm-web-v4 bead 135
// criterion end-to-end through goFixBeadSpec: the criterion already carries
// both grep guards (so addGrepGuard skips it), but its -run value is
// space-separated and would vacuously pass. The full fix pipeline must still
// repair the separator.
func TestGoFixBeadSpec_Bead135RunFlag(t *testing.T) {
	b := &ParsedBead{
		OutputFiles: []string{"handlers.go", "templates.go", "handlers_test.go"},
		ExitCriteria: []string{
			"grep -q 'func TestHandleIndex' handlers_test.go && grep -q 'func TestHandleEval' handlers_test.go && go test -v -run 'TestHandleIndex TestHandleEval' ./...",
			"grep -q 'func TestHandlerRuntime' handlers_test.go && go test -v -run TestHandlerRuntime ./...",
		},
	}
	if !goFixBeadSpec(b) {
		t.Fatal("expected a fix")
	}
	want := "grep -q 'func TestHandleIndex' handlers_test.go && grep -q 'func TestHandleEval' handlers_test.go && go test -v -run 'TestHandleIndex|TestHandleEval' ./..."
	if b.ExitCriteria[0] != want {
		t.Errorf("criterion[0] = %q\nwant %q", b.ExitCriteria[0], want)
	}
	if b.ExitCriteria[1] != "grep -q 'func TestHandlerRuntime' handlers_test.go && go test -v -run TestHandlerRuntime ./..." {
		t.Errorf("criterion[1] should be unchanged, got %q", b.ExitCriteria[1])
	}
	// Idempotent in effect: a second pass must not alter the (already-fixed)
	// criteria text. (goFixBeadSpec's return bool is not a reliable "changed"
	// signal for grep-guarded criteria — fixFileBasedGoTest strips the grep
	// guard's .go arg and fixBareGrepFile re-adds it, a benign round-trip that
	// predates this fix — so assert on the text, not the bool.)
	before := append([]string(nil), b.ExitCriteria...)
	goFixBeadSpec(b)
	for i := range before {
		if b.ExitCriteria[i] != before[i] {
			t.Errorf("second pass changed criterion[%d]:\n before %q\n after  %q", i, before[i], b.ExitCriteria[i])
		}
	}
}

// TestBead684CriterionSatisfiability actually runs the bead-684 exit
// criteria against real files on disk via execcheck.VerifyExitCriteria — the
// same mechanical gate ADJUDICATE's declare_success path uses — rather than
// just comparing generated strings. String comparison alone is exactly what
// let the original bug through: the prior version of this test file asserted
// a `want` value that was itself the impossible, self-contradictory
// criterion (see mechanical_checks_test.go git history), because it only
// checked "was a filename added," never "can this criterion ever pass."
//
// It builds a minimal real Go package in two states — "assertion still in
// game.go" (not yet fixed) and "assertion removed" (fixed) — and checks:
//   - the OLD broken criterion (hardcoded, byte-for-byte what was actually
//     stored on checkers-try-1 bead 684 revision 12) fails in both states,
//     proving it was never satisfiable regardless of what the agent did;
//   - the NEW criterion (produced by goFixBeadSpec from the same raw model
//     output) fails in the unfixed state and passes in the fixed state,
//     proving it is both correct and actually satisfiable.
func TestBead684CriterionSatisfiability(t *testing.T) {
	const goMod = "module scratchgame\n\ngo 1.21\n"
	const layoutTest = `package main

import "testing"

func TestLayout(t *testing.T) {}
`
	const gameGoWithAssertion = `package main

type Game struct{}

func NewGame() *Game { return &Game{} }

var _ func() *Game = NewGame
`
	const gameGoFixed = `package main

type Game struct{}

func NewGame() *Game { return &Game{} }
`

	writeState := func(t *testing.T, gameGo string) string {
		t.Helper()
		dir := t.TempDir()
		files := map[string]string{
			"go.mod":         goMod,
			"game.go":        gameGo,
			"layout_test.go": layoutTest,
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
		return dir
	}

	// Byte-exact copy of what was actually stored on checkers-try-1 bead 684
	// revision 12 (project 100) — including the backslash-escaped asterisk the
	// model itself used, confirmed against the live DB rather than assumed.
	oldBrokenCriterion := "grep -q 'var _ func() \\*Game = NewGame' game.go && " +
		"! grep -q 'var _ func() \\*Game = NewGame' game.go && go test -v . -run TestLayout && grep -q '^var _' game.go"

	rawModelCriterion := "grep -q 'func TestLayout' layout_test.go && " +
		"grep -q 'var _ func() \\*Game = NewGame' && " +
		"! grep -q 'var _ func() \\*Game = NewGame' && go test -v . -run TestLayout"
	bead := &ParsedBead{
		OutputFiles:  []string{"game.go", "ai.go", "handlers.go", "main.go", "go.mod", "layout_test.go"},
		ExitCriteria: []string{rawModelCriterion},
	}
	goFixBeadSpec(bead)
	newCriterion := bead.ExitCriteria[0]

	for _, state := range []struct {
		name   string
		gameGo string
		fixed  bool
	}{
		{"assertion still in game.go (unfixed)", gameGoWithAssertion, false},
		{"assertion removed from game.go (fixed)", gameGoFixed, true},
	} {
		t.Run(state.name, func(t *testing.T) {
			dir := writeState(t, state.gameGo)

			if ok, detail := execcheck.VerifyExitCriteria(context.Background(), dir, []string{oldBrokenCriterion}); ok {
				t.Errorf("old criterion should never be satisfiable, but passed (detail: %s)", detail)
			}

			ok, detail := execcheck.VerifyExitCriteria(context.Background(), dir, []string{newCriterion})
			if ok != state.fixed {
				t.Errorf("new criterion satisfiability = %v, want %v (detail: %s)", ok, state.fixed, detail)
			}
		})
	}
}

// TestOthelloFixtureAsteriskBug is the real criterion stored on the othello
// fixture (project -4, bead 670, "game-flips") — confirmed via a live DB
// audit to be byte-for-byte what DECOMPOSE_SPEC/RECONCILE_DECOMPOSITION
// generated. It checks, end-to-end, that the unescaped form can never match a
// real Go file even when the method genuinely exists, and that the escaped
// form goFixBeadSpec now produces can.
func TestOthelloFixtureAsteriskBug(t *testing.T) {
	const goMod = "module scratchothello\n\ngo 1.21\n"
	const gameGo = `package main

type Game struct{}

func (g *Game) FindFlips() {}
`
	const gameTest = `package main

import "testing"

func TestFindFlips(t *testing.T) {}
`
	dir := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":       goMod,
		"game.go":      gameGo,
		"game_test.go": gameTest,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// Byte-exact copy of bead 670's stored exit criterion (revision 1257).
	unescaped := "grep -q 'func (g *Game) FindFlips' game.go && go test -run TestFindFlips ."
	if ok, _ := execcheck.VerifyExitCriteria(context.Background(), dir, []string{unescaped}); ok {
		t.Error("unescaped criterion should never match a real pointer-receiver signature, but it passed")
	}

	b := &ParsedBead{
		OutputFiles:  []string{"game.go", "game_test.go"},
		ExitCriteria: []string{unescaped},
	}
	if !goFixBeadSpec(b) {
		t.Fatal("expected the asterisk to be escaped")
	}
	if ok, detail := execcheck.VerifyExitCriteria(context.Background(), dir, []string{b.ExitCriteria[0]}); !ok {
		t.Errorf("escaped criterion should match the real method, but failed: %s", detail)
	}
}

// Verbatim from docs/fixture-design-docs/exprvm-web.md's Decomposition Notes,
// as of the 2026-08-29 fixup (commit 7b87ee1) — two back-to-back pin bullets
// with no blank line between them, each spanning multiple soft-wrapped lines.
const exprvmWebDecompositionNotes = `## Decomposition Notes

**Critical dependency chain — do not reorder:**

1. **lexer**: ` + "`Token`" + `, ` + "`TokenType`" + `, ` + "`NewLexer`" + `, ` + "`(*Lexer).Next`" + `. No
   dependencies on any other bead.

- **Pin the exact disassembly strings to the ` + "`compiler`" + ` bead**: for ` + "`\"x=10\"`" + `
  (fresh ` + "`Environment`" + `), ` + "`Disassemble`" + ` must return exactly
  ` + "`[\"PUSH_CONST 10\", \"STORE x\"]`" + `; for ` + "`\"print(x+1)\"`" + ` (same, now-populated
  ` + "`Environment`" + `), exactly ` + "`[\"LOAD x\", \"PUSH_CONST 1\", \"ADD\", \"PRINT\"]`" + ` — no
  trailing operand on ` + "`ADD`/`PRINT`" + `. Do not rely on the general
  opcode-to-mnemonic table alone and leave ` + "`REFINE_TESTS_WRITE`" + ` to re-derive
  the operand-omission-on-no-operand-opcodes rule itself.
- **Pin the exact division sign combinations to the ` + "`vm`" + ` bead**: ` + "`7/2 = 3`" + `,
  ` + "`-7/2 = -3`" + ` (NOT ` + "`-4`" + `), ` + "`7/-2 = -3`" + ` (NOT ` + "`-4`" + `), ` + "`-7/-2 = 3`" + `. Do not rely on
  the general "truncates toward zero" rule alone and leave
  ` + "`REFINE_TESTS_WRITE`" + ` to re-derive the negative-operand cases itself.

**Integration bead scenarios** (bounded — one fixed scenario each):
- Using ` + "`httptest.NewServer`" + `, ` + "`POST /eval`" + ` with input=x=5.
`

func TestExtractDecompositionNotesPins(t *testing.T) {
	pins := extractDecompositionNotesPins(exprvmWebDecompositionNotes)
	if len(pins) != 2 {
		t.Fatalf("expected 2 pins, got %d: %v", len(pins), pins)
	}
	compiler, ok := pins["compiler"]
	if !ok {
		t.Fatal("expected a pin keyed by \"compiler\"")
	}
	if !strings.HasPrefix(compiler, "- **Pin the exact disassembly strings") {
		t.Errorf("compiler pin text wrong start: %q", compiler)
	}
	if !strings.Contains(compiler, "PUSH_CONST 10") {
		t.Errorf("compiler pin missing its own content — bullet-boundary detection likely wrong: %q", compiler)
	}
	if strings.Contains(compiler, "division sign combinations") {
		t.Errorf("compiler pin bled into the vm pin — bullet-boundary detection is wrong: %q", compiler)
	}

	vm, ok := pins["vm"]
	if !ok {
		t.Fatal("expected a pin keyed by \"vm\"")
	}
	if !strings.Contains(vm, "-7/2 = -3") {
		t.Errorf("vm pin missing its own content: %q", vm)
	}
	if strings.Contains(vm, "disassembly strings") || strings.Contains(vm, "httptest.NewServer") {
		t.Errorf("vm pin captured neighboring content — bullet-boundary detection is wrong: %q", vm)
	}
}

func TestExtractDecompositionNotesPins_ParentheticalCaveat(t *testing.T) {
	// Verbatim shape from docs/design-docs/connect-four-v1-design-doc.md: the
	// bead-name backtick is followed by a parenthetical aside before the
	// closing "**:" rather than immediately by it.
	doc := "## Decomposition Notes\n\n" +
		"- **Pin exact literal values to the `ai` bead** (whichever bead ends up\n" +
		"  implementing `ai.go`): `SearchDepth = 4`.\n"
	pins := extractDecompositionNotesPins(doc)
	ai, ok := pins["ai"]
	if !ok {
		t.Fatalf("expected a pin keyed by \"ai\", got %v", pins)
	}
	if !strings.Contains(ai, "SearchDepth = 4") {
		t.Errorf("ai pin missing its own content: %q", ai)
	}
}

func TestExtractDecompositionNotesPins_NoSection(t *testing.T) {
	if pins := extractDecompositionNotesPins("## Overview\n\nNo notes here.\n"); pins != nil {
		t.Errorf("expected nil for a doc with no Decomposition Notes section, got %v", pins)
	}
}

// TestInjectDecompositionNotesPin_MotivatingBug reproduces the exact failure
// mode found live in exprvm-web-v2 bead 117 (2026-08-30): the design doc's
// precise three-way Bytecode-per-error-type rule got compressed by DECOMPOSE
// into an ambiguous "(if compiled)" clause, and REFINE_TESTS_JUDGE — which
// never reads the design doc, only the bead spec — endorsed a CRITIQUE
// finding that contradicted the doc's actual rule as a result. A pin for
// this bead, mechanically injected, gives JUDGE the exact rule regardless of
// how DECOMPOSE's own prose compressed it.
func TestInjectDecompositionNotesPin_MotivatingBug(t *testing.T) {
	doc := "## Decomposition Notes\n\n" +
		"- **Pin the exact Bytecode-by-error-type rule to the `handlers-templates` bead**:\n" +
		"  on a Compile error, `Bytecode` stays `\"\"` (nothing was compiled — `Disassemble`\n" +
		"  never runs); on a Run error or success, `Bytecode` is always `bytecodeText`\n" +
		"  (`Disassemble` already ran before `Run` was ever called). There is no\n" +
		"  \"partial instructions\" case.\n"
	pins := extractDecompositionNotesPins(doc)

	bead := &ParsedBead{
		Title:    "handlers-templates",
		FullText: "On any error in the pipeline, append HistoryEntry with appropriate Err and Bytecode (if compiled).",
	}
	if !injectDecompositionNotesPin(bead, pins) {
		t.Fatal("expected the pin to be injected")
	}
	if !strings.Contains(bead.FullText, "Bytecode-by-error-type rule") || !strings.Contains(bead.FullText, "bytecodeText") {
		t.Errorf("bead full_text missing the injected pin: %q", bead.FullText)
	}

	// Idempotent: calling again (as RECONCILE would on a later round) must
	// not duplicate the pin text.
	before := bead.FullText
	if injectDecompositionNotesPin(bead, pins) {
		t.Error("expected no change on second call — pin already present")
	}
	if bead.FullText != before {
		t.Errorf("full_text changed on second call:\nbefore: %q\nafter:  %q", before, bead.FullText)
	}
}

// TestInjectDecompositionNotesPin_CollapsesReflowedCopies reproduces
// exprvm-web-v4 bead 135 (2026-08-30): DECOMPOSE injected the pin appendix,
// then across two RECONCILE rounds the model regenerated full_text with the
// appendix reproduced — twice verbatim, once with a one-line wrap difference —
// so the bead spec carried three near-identical copies. injection must
// normalize any such accumulation back to exactly one canonical block.
func TestInjectDecompositionNotesPin_CollapsesReflowedCopies(t *testing.T) {
	doc := "## Decomposition Notes\n\n" +
		"- **Pin the exact Bytecode-by-error-type rule to the `handlers-templates` bead**:\n" +
		"  on a Compile error, `Bytecode` stays `\"\"`; on a Run error or success it is\n" +
		"  always `bytecodeText`.\n"
	pins := extractDecompositionNotesPins(doc)

	// A bead spec that already carries the appendix three times: two verbatim,
	// one with the bullet's second line wrapped differently.
	pin := pins["handlers-templates"]
	reflowed := strings.Replace(pin, "on a Run error or success it is\n  always", "on a Run error or\n  success it is always", 1)
	spec := "Implement the web layer.\n\n" +
		"\n\n" + pinAppendixHeader + pin +
		"\n\n" + pinAppendixHeader + pin +
		"\n\n" + pinAppendixHeader + reflowed

	bead := &ParsedBead{Title: "handlers-templates", FullText: spec}

	if !injectDecompositionNotesPin(bead, pins) {
		t.Fatal("expected normalization to report a change")
	}
	if n := strings.Count(bead.FullText, pinAppendixHeader); n != 1 {
		t.Fatalf("expected exactly one pin appendix after normalization, got %d:\n%s", n, bead.FullText)
	}
	if !strings.HasPrefix(bead.FullText, "Implement the web layer.") {
		t.Errorf("normalization dropped the real spec prose: %q", bead.FullText)
	}
	if !strings.Contains(bead.FullText, pin) {
		t.Errorf("normalized appendix is not the canonical pin text: %q", bead.FullText)
	}
	// Second call is now a no-op.
	if injectDecompositionNotesPin(bead, pins) {
		t.Error("expected no change on a second normalization pass")
	}
}

func TestInjectDecompositionNotesPin_NoMatch(t *testing.T) {
	pins := extractDecompositionNotesPins(exprvmWebDecompositionNotes)
	bead := &ParsedBead{Title: "main", FullText: "wires everything together"}
	if injectDecompositionNotesPin(bead, pins) {
		t.Error("expected no injection for a bead with no matching pin")
	}
	if bead.FullText != "wires everything together" {
		t.Errorf("full_text should be unchanged, got %q", bead.FullText)
	}
}

func TestExtractRunNames(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"go test -run TestFoo ./...", []string{"TestFoo"}},
		{"go test -run=TestFoo .", []string{"TestFoo"}},
		{"go test -v -run 'TestA|TestB' ./...", []string{"TestA", "TestB"}},
		{"go test -run 'TestA TestB' ./...", nil}, // accidental space form — I6-residual reports it, not this
		{"go test -run '^TestFoo$' .", []string{"TestFoo"}},
		{"go test -run 'TestFoo/sub case' .", []string{"TestFoo"}},
		{"go test ./...", nil},
	}
	for _, tc := range cases {
		got := extractRunNames(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("extractRunNames(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("extractRunNames(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestCheckBeadCriteriaConsistency(t *testing.T) {
	t.Run("clean bead — no violations", func(t *testing.T) {
		b := ParsedBead{
			Title:        "compiler",
			FullText:     "Implement Compile and Disassemble. Test with TestCompile and TestDisassemble.",
			OutputFiles:  []string{"compiler.go", "compiler_test.go"},
			ExitCriteria: []string{"grep -q 'func TestCompile' compiler_test.go && go test -run TestCompile ./..."},
		}
		if v := checkBeadCriteriaConsistency([]ParsedBead{b}); len(v) != 0 {
			t.Errorf("expected no violations, got %v", v)
		}
	})

	t.Run("orphan -run name — no guard, not in prose", func(t *testing.T) {
		b := ParsedBead{
			Title:        "handlers-templates",
			FullText:     "Implement HandleIndex and HandleEval.",
			OutputFiles:  []string{"handlers.go", "handlers_test.go"},
			ExitCriteria: []string{"go test -v -run TestHandlerRuntime ./..."},
		}
		v := checkBeadCriteriaConsistency([]ParsedBead{b})
		if len(v) == 0 || !strings.Contains(v[0], "TestHandlerRuntime") {
			t.Errorf("expected a violation naming TestHandlerRuntime, got %v", v)
		}
	})

	t.Run("space-separated -run value still present", func(t *testing.T) {
		b := ParsedBead{
			Title:        "x",
			FullText:     "TestA and TestB",
			OutputFiles:  []string{"x.go", "x_test.go"},
			ExitCriteria: []string{"grep -q 'func TestA' x_test.go && grep -q 'func TestB' x_test.go && go test -run 'TestA TestB' ./..."},
		}
		v := checkBeadCriteriaConsistency([]ParsedBead{b})
		if len(v) == 0 {
			t.Fatalf("expected a violation for the space-separated -run value")
		}
	})

	t.Run("grep guard names a test file the bead does not own", func(t *testing.T) {
		b := ParsedBead{
			Title:        "handlers",
			FullText:     "TestHandleIndex",
			OutputFiles:  []string{"handlers.go", "handlers_test.go"},
			ExitCriteria: []string{"grep -q 'func TestHandleIndex' other_test.go && go test -run TestHandleIndex ./..."},
		}
		v := checkBeadCriteriaConsistency([]ParsedBead{b})
		if len(v) == 0 || !strings.Contains(v[0], "other_test.go") {
			t.Errorf("expected a violation naming other_test.go, got %v", v)
		}
	})
}

func TestCheckAddedRequiredFuncsHaveProse(t *testing.T) {
	before := ParsedBead{
		Title:        "handlers-templates",
		FullText:     "Implement HandleIndex and HandleEval.",
		ExitCriteria: []string{"grep -q 'func TestHandleIndex' handlers_test.go && go test -run TestHandleIndex ./..."},
	}

	t.Run("RECONCILE adds an orphan required function", func(t *testing.T) {
		after := ParsedBead{
			Title:    "handlers-templates",
			FullText: "Implement HandleIndex and HandleEval.", // unchanged — no prose for TestHandlerRuntime
			ExitCriteria: []string{
				"grep -q 'func TestHandleIndex' handlers_test.go && go test -run TestHandleIndex ./...",
				"grep -q 'func TestHandlerRuntime' handlers_test.go && go test -run TestHandlerRuntime ./...",
			},
		}
		v := checkAddedRequiredFuncsHaveProse(before, after)
		if len(v) != 1 || !strings.Contains(v[0], "TestHandlerRuntime") {
			t.Errorf("expected one violation naming TestHandlerRuntime, got %v", v)
		}
	})

	t.Run("RECONCILE adds a required function AND describes it — no violation", func(t *testing.T) {
		after := ParsedBead{
			Title:    "handlers-templates",
			FullText: "Implement HandleIndex and HandleEval. Also write TestHandlerRuntime, an integration test using httptest.NewServer.",
			ExitCriteria: []string{
				"grep -q 'func TestHandleIndex' handlers_test.go && go test -run TestHandleIndex ./...",
				"grep -q 'func TestHandlerRuntime' handlers_test.go && go test -run TestHandlerRuntime ./...",
			},
		}
		if v := checkAddedRequiredFuncsHaveProse(before, after); len(v) != 0 {
			t.Errorf("expected no violation, got %v", v)
		}
	})

	t.Run("no new required functions — no violation even if prose is silent on test names", func(t *testing.T) {
		after := before
		after.FullText = "Reworded prose that still names no test functions."
		if v := checkAddedRequiredFuncsHaveProse(before, after); len(v) != 0 {
			t.Errorf("expected no violation for a stable required-func set, got %v", v)
		}
	})
}
