package verbs

import (
	"strings"
	"testing"
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

	t.Run("layout bead with do_not_use_this_test.go — go build upgraded to go test -c and grep guard added", func(t *testing.T) {
		b := &ParsedBead{
			OutputFiles:  []string{"game.go", "ai.go", "main.go", "do_not_use_this_test.go"},
			ExitCriteria: []string{"go build ./..."},
		}
		fixed := goFixBeadSpec(b)
		if !fixed {
			t.Fatal("expected fix to be applied")
		}
		want := "go test -c -o /dev/null ./... && grep -q '^var _' do_not_use_this_test.go"
		if b.ExitCriteria[0] != want {
			t.Errorf("criterion = %q, want %q", b.ExitCriteria[0], want)
		}
	})

	t.Run("layout bead with do_not_use_this_test.go — go build+grep upgraded, no double grep", func(t *testing.T) {
		// Transition case: a criterion that already had the grep guard from a prior
		// fix pass but still uses go build. Should upgrade go build without adding
		// a second grep guard.
		b := &ParsedBead{
			OutputFiles:  []string{"game.go", "do_not_use_this_test.go"},
			ExitCriteria: []string{"go build ./... && grep -q '^var _' do_not_use_this_test.go"},
		}
		fixed := goFixBeadSpec(b)
		if !fixed {
			t.Fatal("expected go build to be upgraded even when grep guard already present")
		}
		want := "go test -c -o /dev/null ./... && grep -q '^var _' do_not_use_this_test.go"
		if b.ExitCriteria[0] != want {
			t.Errorf("criterion = %q, want %q", b.ExitCriteria[0], want)
		}
	})

	t.Run("layout bead with do_not_use_this_test.go — already correct form, idempotent", func(t *testing.T) {
		b := &ParsedBead{
			OutputFiles:  []string{"game.go", "do_not_use_this_test.go"},
			ExitCriteria: []string{"go test -c -o /dev/null ./... && grep -q '^var _' do_not_use_this_test.go"},
		}
		fixed := goFixBeadSpec(b)
		if fixed {
			t.Fatal("expected no fix when criterion is already in correct form")
		}
		want := "go test -c -o /dev/null ./... && grep -q '^var _' do_not_use_this_test.go"
		if b.ExitCriteria[0] != want {
			t.Errorf("criterion should be unchanged, got %q", b.ExitCriteria[0])
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
