package verbs

import (
	"strings"
	"testing"
)

func TestExtractFailureSignals(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{
			"RECURRING (2 attempts): TestFib fails — got 0, want 1",
			[]string{"FAIL: TestFib"},
		},
		{
			"RECURRING: TestEncode and TestDecode both fail",
			[]string{"FAIL: TestEncode", "FAIL: TestDecode"},
		},
		{
			"RECURRING: undefined: Fib persists across attempts",
			[]string{"undefined: Fib"},
		},
		{
			"RECURRING: compilation error — undefined: NewEncoder",
			[]string{"undefined: NewEncoder"},
		},
		{
			"RECURRING: TestFib fails with undefined: Fib",
			[]string{"FAIL: TestFib", "undefined: Fib"},
		},
		{
			// Prose-only — no extractable signals
			"RECURRING: compilation errors persist",
			nil,
		},
		{
			// Not a RECURRING line — signals still extracted (caller checks RECURRING)
			"NEW: TestFoo fails on first attempt",
			[]string{"FAIL: TestFoo"},
		},
	}

	for _, tc := range cases {
		got := extractFailureSignals(tc.line)
		if len(got) != len(tc.want) {
			t.Errorf("extractFailureSignals(%q)\n  got  %v\n  want %v", tc.line, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("extractFailureSignals(%q)[%d] = %q, want %q", tc.line, i, got[i], tc.want[i])
			}
		}
	}
}

func TestInjectResolvedTags(t *testing.T) {
	passingFindings := "Termination cause: success\n\n## Exit Criteria\n\n1. go test ./...\n   Last run: turn 8, exit 0\n   stdout: ok  fib  0.488s\n"
	failingFindings := "Termination cause: success\n\n## Exit Criteria\n\n1. go test ./...\n   Last run: turn 8, exit 1\n   stdout: --- FAIL: TestFib (0.00s)\n       got 0, want 1\n"
	undefinedFindings := "Termination cause: success\n\n## Exit Criteria\n\n1. go build ./...\n   Last run: turn 3, exit 1\n   stderr: ./fib.go:5:6: undefined: Fib\n"

	t.Run("resolves_test_failure_when_tests_pass", func(t *testing.T) {
		compressed := "Attempt 1: NEW: TestFib fails.\nAttempt 2: RECURRING (1 prior): TestFib fails — got 0."
		got := injectResolvedTags(compressed, passingFindings)
		if !strings.Contains(got, "RESOLVED") {
			t.Errorf("expected RESOLVED tag; got:\n%s", got)
		}
		if strings.Contains(got, "RECURRING (1 prior): TestFib fails — got 0. [RESOLVED") {
			// correct form
		}
	})

	t.Run("keeps_recurring_when_test_still_fails", func(t *testing.T) {
		compressed := "Attempt 1: NEW: TestFib fails.\nAttempt 2: RECURRING (1 prior): TestFib fails."
		got := injectResolvedTags(compressed, failingFindings)
		if strings.Contains(got, "RESOLVED") {
			t.Errorf("should not inject RESOLVED when test still failing; got:\n%s", got)
		}
	})

	t.Run("resolves_undefined_symbol_when_fixed", func(t *testing.T) {
		compressed := "RECURRING (2 prior): undefined: Fib — symbol missing."
		got := injectResolvedTags(compressed, passingFindings)
		if !strings.Contains(got, "RESOLVED") {
			t.Errorf("expected RESOLVED for resolved undefined symbol; got:\n%s", got)
		}
	})

	t.Run("keeps_undefined_symbol_when_still_missing", func(t *testing.T) {
		compressed := "RECURRING (2 prior): undefined: Fib — symbol missing."
		got := injectResolvedTags(compressed, undefinedFindings)
		if strings.Contains(got, "RESOLVED") {
			t.Errorf("should not inject RESOLVED when symbol still undefined; got:\n%s", got)
		}
	})

	t.Run("prose_only_recurring_left_unchanged", func(t *testing.T) {
		compressed := "RECURRING: compilation errors persist across attempts."
		got := injectResolvedTags(compressed, passingFindings)
		if got != compressed {
			t.Errorf("prose-only RECURRING should be unchanged; got:\n%s", got)
		}
	})

	t.Run("already_resolved_not_double_tagged", func(t *testing.T) {
		compressed := "RECURRING (2 prior): TestFib fails. [RESOLVED — absent from latest attempt]"
		got := injectResolvedTags(compressed, passingFindings)
		if strings.Count(got, "RESOLVED") != 1 {
			t.Errorf("expected exactly one RESOLVED tag; got:\n%s", got)
		}
	})

	t.Run("new_tag_not_touched", func(t *testing.T) {
		compressed := "NEW: TestFoo fails on first attempt."
		got := injectResolvedTags(compressed, passingFindings)
		// NEW lines don't contain RECURRING so they are never touched
		if got != compressed {
			t.Errorf("NEW-tagged line should be unchanged; got:\n%s", got)
		}
	})

	t.Run("multiline_only_recurring_lines_modified", func(t *testing.T) {
		compressed := "Attempt 1: NEW: TestFib fails.\nAttempt 2: RECURRING (1 prior): TestFib fails.\nSome other note."
		got := injectResolvedTags(compressed, passingFindings)
		lines := strings.Split(got, "\n")
		if strings.Contains(lines[0], "RESOLVED") {
			t.Error("first line (NEW) should not be tagged RESOLVED")
		}
		if !strings.Contains(lines[1], "RESOLVED") {
			t.Error("second line (RECURRING) should be tagged RESOLVED")
		}
		if strings.Contains(lines[2], "RESOLVED") {
			t.Error("third line (note) should not be tagged RESOLVED")
		}
	})
}
