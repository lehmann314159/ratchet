package report

import "testing"

// TestLastTestResultTruncatedNotFail reproduces a Stage 9 audit finding:
// lastTestResult treated any "Last run:" line that didn't end in "exit 0" as
// a definitive FAIL, including the truncated case (execution killed/timed
// out mid-command, so its exit criterion never actually finished running —
// trace.parseResultLines defaults ExitRaw to "(truncated)" in that case, per
// GenerateMechanicalFindings' "Last run: turn N, exit (truncated)" line).
// That mislabeled a killed-mid-test execution as a definitive test failure in
// the human-facing bead report, when the true state is unknown.
func TestLastTestResultTruncatedNotFail(t *testing.T) {
	findings := `Termination cause: timeout
Monitor fired: no

## Exit Criteria

1. go test ./... -run TestFoo
   Last run: turn 3, exit (truncated)
   (no output)
`
	got := lastTestResult(findings, []string{"go test ./... -run TestFoo"})
	if got != "unknown (truncated)" {
		t.Errorf("lastTestResult = %q, want %q", got, "unknown (truncated)")
	}
}

func TestLastTestResultPassAndFail(t *testing.T) {
	pass := `## Exit Criteria

1. go test ./...
   Last run: turn 2, exit 0
`
	if got := lastTestResult(pass, []string{"go test ./..."}); got != "PASS" {
		t.Errorf("PASS case: lastTestResult = %q, want PASS", got)
	}

	fail := `## Exit Criteria

1. go test ./...
   Last run: turn 2, exit exit status 1
`
	if got := lastTestResult(fail, []string{"go test ./..."}); got != "FAIL" {
		t.Errorf("FAIL case: lastTestResult = %q, want FAIL", got)
	}

	notRun := `## Exit Criteria

1. go test ./...
   Not run during this execution.
`
	if got := lastTestResult(notRun, []string{"go test ./..."}); got != "not run" {
		t.Errorf("not-run case: lastTestResult = %q, want %q", got, "not run")
	}
}
