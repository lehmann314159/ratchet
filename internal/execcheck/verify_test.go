package execcheck

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// goMainModule writes a minimal buildable `package main` module into dir and
// returns the binary name `go build .` produces (the last element of the module
// path, not the filesystem dir).
func goMainModule(t *testing.T, dir string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module app\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return "app"
}

func TestVerifyExitCriteriaIsolated(t *testing.T) {
	t.Run("go build . does not litter the real folder", func(t *testing.T) {
		dir := t.TempDir()
		bin := goMainModule(t, dir)

		ok, detail := VerifyExitCriteriaIsolated(context.Background(), dir, []string{"go build ."})
		if !ok {
			t.Fatalf("expected the build criterion to pass, got: %s", detail)
		}
		if _, err := os.Stat(filepath.Join(dir, bin)); !os.IsNotExist(err) {
			t.Errorf("isolated verify left a %q binary in the real folder (err=%v)", bin, err)
		}

		// Control: the plain in-place verify DOES litter — proves the test bites.
		ctrl := t.TempDir()
		cbin := goMainModule(t, ctrl)
		if ok, detail := VerifyExitCriteria(context.Background(), ctrl, []string{"go build ."}); !ok {
			t.Fatalf("control build failed: %s", detail)
		}
		if _, err := os.Stat(filepath.Join(ctrl, cbin)); err != nil {
			t.Fatalf("control: expected in-place verify to leave a binary, but none found: %v", err)
		}
	})

	t.Run("same verdict as VerifyExitCriteria", func(t *testing.T) {
		dir := t.TempDir()
		if ok, _ := VerifyExitCriteriaIsolated(context.Background(), dir, []string{"true", "echo hi"}); !ok {
			t.Error("expected passing criteria to pass")
		}
		if ok, detail := VerifyExitCriteriaIsolated(context.Background(), dir, []string{"echo boom && false"}); ok || detail == "" {
			t.Errorf("expected failing criteria to fail with detail, got ok=%v detail=%q", ok, detail)
		}
		if ok, _ := VerifyExitCriteriaIsolated(context.Background(), dir, nil); !ok {
			t.Error("expected empty criteria to vacuously pass")
		}
	})

	t.Run("criterion reads a copied sibling file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("1.2.3\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if ok, detail := VerifyExitCriteriaIsolated(context.Background(), dir, []string{"grep -q 1.2.3 VERSION"}); !ok {
			t.Errorf("expected the criterion to see the copied file, got: %s", detail)
		}
	})

	t.Run("falls back rather than panicking when the folder is unreadable", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		// Copy fails → fallback to in-place verify against a missing dir → not ok,
		// but it must return, not panic.
		if ok, _ := VerifyExitCriteriaIsolated(context.Background(), missing, []string{"true"}); ok {
			t.Error("expected verify against a missing folder to fail")
		}
	})
}

func TestVerifyExitCriteria(t *testing.T) {
	dir := t.TempDir()

	t.Run("all criteria pass", func(t *testing.T) {
		ok, detail := VerifyExitCriteria(context.Background(), dir, []string{"true", "echo hi"})
		if !ok {
			t.Errorf("expected pass, got failure detail: %q", detail)
		}
	})

	t.Run("a failing criterion is reported with output", func(t *testing.T) {
		ok, detail := VerifyExitCriteria(context.Background(), dir, []string{"echo Fail && false"})
		if ok {
			t.Fatal("expected the criterion to fail")
		}
		if detail == "" {
			t.Error("expected a non-empty failure detail")
		}
	})

	t.Run("no criteria vacuously passes", func(t *testing.T) {
		ok, _ := VerifyExitCriteria(context.Background(), dir, nil)
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
		ok, detail := VerifyExitCriteria(context.Background(), blockDir, criteria)
		if ok {
			t.Fatal("expected the literal grep pattern to fail against block-style var assertions")
		}
		if detail == "" {
			t.Error("expected a non-empty failure detail explaining the grep mismatch")
		}
	})
}