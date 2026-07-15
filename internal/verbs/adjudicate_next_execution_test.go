package verbs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyExitCriteriaMechanically(t *testing.T) {
	dir := t.TempDir()

	t.Run("all criteria pass", func(t *testing.T) {
		ok, detail := verifyExitCriteriaMechanically(context.Background(), dir, []string{"true", "echo hi"})
		if !ok {
			t.Errorf("expected pass, got failure detail: %q", detail)
		}
	})

	t.Run("a failing criterion is reported with output", func(t *testing.T) {
		ok, detail := verifyExitCriteriaMechanically(context.Background(), dir, []string{"echo Fail && false"})
		if ok {
			t.Fatal("expected the criterion to fail")
		}
		if detail == "" {
			t.Error("expected a non-empty failure detail")
		}
	})

	t.Run("no criteria vacuously passes", func(t *testing.T) {
		ok, _ := verifyExitCriteriaMechanically(context.Background(), dir, nil)
		if !ok {
			t.Error("expected an empty exit_criteria list to pass")
		}
	})

	t.Run("real-world checkers-v8 bead 627 case: block-style var assertions don't match the grep pattern", func(t *testing.T) {
		blockDir := t.TempDir()
		goMod := "module checkers\n\ngo 1.26\n"
		if err := os.WriteFile(filepath.Join(blockDir, "go.mod"), []byte(goMod), 0o644); err != nil {
			t.Fatal(err)
		}
		gameGo := "package main\n\nfunc NewGame() *int { return nil }\n"
		if err := os.WriteFile(filepath.Join(blockDir, "game.go"), []byte(gameGo), 0o644); err != nil {
			t.Fatal(err)
		}
		// Block-style var assertion — compiles fine, but no line starts with "var _".
		testGo := "package main\n\nvar (\n\t_ = NewGame\n)\n"
		if err := os.WriteFile(filepath.Join(blockDir, "do_not_use_this_test.go"), []byte(testGo), 0o644); err != nil {
			t.Fatal(err)
		}
		criteria := []string{"go test -c -o /dev/null ./... && grep -q '^var _' do_not_use_this_test.go"}
		ok, detail := verifyExitCriteriaMechanically(context.Background(), blockDir, criteria)
		if ok {
			t.Fatal("expected the literal grep pattern to fail against block-style var assertions")
		}
		if detail == "" {
			t.Error("expected a non-empty failure detail explaining the grep mismatch")
		}
	})
}
