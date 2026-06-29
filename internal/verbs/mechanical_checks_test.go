package verbs

import (
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
		{"go test file", []string{"api_check_test.go"}, "go"},
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

	t.Run("with test file already present — no fix", func(t *testing.T) {
		b := &ParsedBead{
			OutputFiles:  []string{"game.go", "game_test.go"},
			ExitCriteria: []string{"go test -v . -run=TestFoo"},
		}
		fixed := goFixBeadSpec(b)
		if fixed {
			t.Fatal("expected no fix when test file is present")
		}
		if b.ExitCriteria[0] != "go test -v . -run=TestFoo" {
			t.Errorf("criterion should be unchanged, got %q", b.ExitCriteria[0])
		}
	})

	t.Run("non-go-test criterion — no fix", func(t *testing.T) {
		b := &ParsedBead{
			OutputFiles:  []string{"main.go"},
			ExitCriteria: []string{"go build ./..."},
		}
		fixed := goFixBeadSpec(b)
		if fixed {
			t.Fatal("expected no fix for go build criterion without api_check_test.go")
		}
	})

	t.Run("layout bead with api_check_test.go — grep suffix added to go build", func(t *testing.T) {
		b := &ParsedBead{
			OutputFiles:  []string{"game.go", "ai.go", "main.go", "api_check_test.go"},
			ExitCriteria: []string{"go build ./..."},
		}
		fixed := goFixBeadSpec(b)
		if !fixed {
			t.Fatal("expected fix to be applied")
		}
		want := "go build ./... && grep -q '^var _' api_check_test.go"
		if b.ExitCriteria[0] != want {
			t.Errorf("criterion = %q, want %q", b.ExitCriteria[0], want)
		}
	})

	t.Run("layout bead with api_check_test.go — no double-add if grep already present", func(t *testing.T) {
		b := &ParsedBead{
			OutputFiles:  []string{"game.go", "api_check_test.go"},
			ExitCriteria: []string{"go build ./... && grep -q '^var _' api_check_test.go"},
		}
		fixed := goFixBeadSpec(b)
		if fixed {
			t.Fatal("expected no fix when grep already present")
		}
		want := "go build ./... && grep -q '^var _' api_check_test.go"
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
