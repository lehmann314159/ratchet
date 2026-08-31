package verbs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectStubFuncs_FloatZeroValue reproduces a gap found while auditing
// the fractal-smoke stress test: isZeroValueExpr recognized "0" (token.INT)
// as a stub return but not "0.0"/"0."/".0" (token.FLOAT), so a genuinely
// unimplemented float-returning stub like `func Scale() float64 { return
// 0.0 }` slipped past detectStubFuncs — no "[Stub implementation]" nudge
// ever reached ADJUDICATE for it. Covers both the previously-missed float
// zero forms and a non-zero float, which must NOT be flagged.
func TestDetectStubFuncs_FloatZeroValue(t *testing.T) {
	dir := t.TempDir()
	src := `package main

func ZeroDotZero() float64 { return 0.0 }
func ZeroDot() float64 { return 0. }
func DotZero() float64 { return .0 }
func NonZero() float64 { return 0.5 }
func RealWork() float64 { x := 2.0; return x * x }
`
	path := filepath.Join(dir, "stubs.go")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	stubs := detectStubFuncs(path, "stubs.go")

	want := map[string]bool{"ZeroDotZero": true, "ZeroDot": true, "DotZero": true}
	got := map[string]bool{}
	for _, s := range stubs {
		got[s] = true
	}
	for name := range want {
		found := false
		for s := range got {
			if len(s) >= len(name) && s[:len(name)] == name {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %s to be detected as a float-zero-value stub; got %v", name, stubs)
		}
	}
	for _, notWant := range []string{"NonZero", "RealWork"} {
		for s := range got {
			if len(s) >= len(notWant) && s[:len(notWant)] == notWant {
				t.Errorf("%s must not be flagged as a stub (not a zero-value return): %v", notWant, stubs)
			}
		}
	}
}
