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

func TestIsRepeatDisagreement(t *testing.T) {
	// Modeled on the checkers-v8 (project 98) round 1→2 escalation: AUDIT
	// re-raised an identical http-handlers finding (only quote style/case
	// differed) that RECONCILE had already disputed in round 1.
	priorCritique := `{"findings":[{"bead_title":"http-handlers","issue":"Depends on templates/index.html and templates/board.html created by the later 'templates' bead."}],"overall_verdict":"issues_found"}`
	priorReconciliation := `{"responses":[{"bead_title":"http-handlers","action":"disagree","reason":"templates already precedes http-handlers"}]}`
	history := []debateRound{
		{RoundNumber: 1, CritiqueText: priorCritique, Reconciliation: priorReconciliation, Outcome: "disagreed_continuing"},
	}

	t.Run("verbatim repeat (cosmetic quote/case differences only) is a repeat", func(t *testing.T) {
		currentCritique := `{"findings":[{"bead_title":"http-handlers","issue":"Depends on templates/index.html and templates/board.html created by the later \"templates\" bead."}],"overall_verdict":"issues_found"}`
		current := findingsByBead(currentCritique)
		if !isRepeatDisagreement("http-handlers", current, history) {
			t.Error("expected a cosmetically-reworded repeat of an already-disputed finding to be detected as a repeat")
		}
	})

	t.Run("genuinely new finding is not a repeat", func(t *testing.T) {
		currentCritique := `{"findings":[{"bead_title":"http-handlers","issue":"HandleMove does not validate that from/to coordinates are on the board."}],"overall_verdict":"issues_found"}`
		current := findingsByBead(currentCritique)
		if isRepeatDisagreement("http-handlers", current, history) {
			t.Error("a substantively new finding must not be treated as a repeat")
		}
	})

	t.Run("bead with no prior disagreement is not a repeat", func(t *testing.T) {
		currentCritique := `{"findings":[{"bead_title":"layout","issue":"output_files contains no *_test.go file."}],"overall_verdict":"issues_found"}`
		current := findingsByBead(currentCritique)
		if isRepeatDisagreement("layout", current, history) {
			t.Error("a bead never previously disagreed-with must not be treated as a repeat")
		}
	})

	t.Run("prior round where RECONCILE agreed_and_fixed is not a repeat source", func(t *testing.T) {
		agreedHistory := []debateRound{
			{
				RoundNumber:    1,
				CritiqueText:   priorCritique,
				Reconciliation: `{"responses":[{"bead_title":"http-handlers","action":"agree_and_fix","reason":"fixed","updated_bead":{"title":"http-handlers","full_text":"x","execution_budget":0,"monitor_override":"honor","output_files":["a.go"],"exit_criteria":["go build ./..."]}}]}`,
				Outcome:        "disagreed_continuing",
			},
		}
		current := findingsByBead(priorCritique)
		if isRepeatDisagreement("http-handlers", current, agreedHistory) {
			t.Error("agree_and_fix in the prior round means there was no dispute to repeat")
		}
	})

	t.Run("redecompose rows are skipped as non-critique history", func(t *testing.T) {
		redecomposeHistory := []debateRound{
			{RoundNumber: 1, CritiqueText: "Bead ordering violations (structural, mechanically detected...)", Reconciliation: "", Outcome: "redecompose"},
		}
		current := findingsByBead(priorCritique)
		if isRepeatDisagreement("http-handlers", current, redecomposeHistory) {
			t.Error("a redecompose row's mechanical prose must not be parsed as a matching critique")
		}
	})
}

func TestNormalizeFindingText(t *testing.T) {
	a := normalizeFindingText(`Depends on templates/index.html — the 'templates' bead.`)
	b := normalizeFindingText(`depends on templates/index.html — the "templates" bead.`)
	if a != b {
		t.Errorf("expected quote-style and case differences to normalize equal, got %q vs %q", a, b)
	}
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
