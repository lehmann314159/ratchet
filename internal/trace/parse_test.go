package trace

import (
	"testing"
)

func TestParse_BasicSuccess(t *testing.T) {
	input := `[TURN 1]
[tool: run_command map[command:go build ./...]]
[result]
exit: 0
[TURN 2]
[tool: run_command map[command:go test ./...]]
[result]
stdout:
ok  fib/fib  0.488s

exit: 0
[done — no further tool calls]
`
	pt := Parse([]byte(input))

	if pt.TerminationMarker != "success" {
		t.Errorf("TerminationMarker = %q, want %q", pt.TerminationMarker, "success")
	}
	if len(pt.Commands) != 2 {
		t.Fatalf("len(Commands) = %d, want 2", len(pt.Commands))
	}

	c0 := pt.Commands[0]
	if c0.Command != "go build ./..." {
		t.Errorf("Commands[0].Command = %q", c0.Command)
	}
	if c0.ExitCode != 0 {
		t.Errorf("Commands[0].ExitCode = %d, want 0", c0.ExitCode)
	}
	if c0.Turn != 1 {
		t.Errorf("Commands[0].Turn = %d, want 1", c0.Turn)
	}

	c1 := pt.Commands[1]
	if c1.Command != "go test ./..." {
		t.Errorf("Commands[1].Command = %q", c1.Command)
	}
	if c1.ExitCode != 0 {
		t.Errorf("Commands[1].ExitCode = %d", c1.ExitCode)
	}
	if c1.Stdout != "ok  fib/fib  0.488s" {
		t.Errorf("Commands[1].Stdout = %q", c1.Stdout)
	}
}

func TestParse_Failure(t *testing.T) {
	input := `[TURN 1]
[tool: run_command map[command:go build ./...]]
[result]
stderr:
pattern ./...: directory prefix . does not contain main module or its selected dependencies

exit: exit status 1
[terminated: timeout]
`
	pt := Parse([]byte(input))

	if pt.TerminationMarker != "timeout" {
		t.Errorf("TerminationMarker = %q, want %q", pt.TerminationMarker, "timeout")
	}
	if len(pt.Commands) != 1 {
		t.Fatalf("len(Commands) = %d, want 1", len(pt.Commands))
	}
	c := pt.Commands[0]
	if c.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", c.ExitCode)
	}
	if c.ExitRaw != "exit status 1" {
		t.Errorf("ExitRaw = %q", c.ExitRaw)
	}
	if c.Stderr == "" {
		t.Error("Stderr is empty")
	}
}

func TestParse_ModelTextIgnored(t *testing.T) {
	// Model text between [TURN] and [tool:] is ignored, even if it looks like a marker.
	input := `[TURN 1]
I will now run the build command. Here is my plan:
[tool: run_command map[command:go build ./...]] -- I might write this in my thinking
But actually let me proceed.
[tool: run_command map[command:go build ./...]]
[result]
exit: 0
[done — no further tool calls]
`
	pt := Parse([]byte(input))
	// The model text line "[tool: run_command map[command:go build ./...]] -- I might..."
	// should be treated as a tool call but with no following [result] before the next
	// tool call — finalize produces nothing (inResult is false).
	// Only the second tool call with a proper [result] should be captured.
	if len(pt.Commands) != 1 {
		t.Fatalf("len(Commands) = %d, want 1", len(pt.Commands))
	}
	if pt.Commands[0].ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", pt.Commands[0].ExitCode)
	}
}

func TestParse_WriteFileIgnored(t *testing.T) {
	input := `[TURN 1]
[tool: write_file map[content:package fib

func Fib(n int) (int, error) {
	return 0, nil
} path:fib.go]]
[result]
ok: wrote 196 bytes to fib.go
[TURN 2]
[tool: run_command map[command:go build ./...]]
[result]
exit: 0
[done — no further tool calls]
`
	pt := Parse([]byte(input))
	// write_file result should not be captured as a CommandResult
	if len(pt.Commands) != 1 {
		t.Fatalf("len(Commands) = %d, want 1", len(pt.Commands))
	}
	if pt.Commands[0].Command != "go build ./..." {
		t.Errorf("Commands[0].Command = %q", pt.Commands[0].Command)
	}
	// but it should be captured as a WriteFileResult
	if len(pt.WriteFiles) != 1 {
		t.Fatalf("len(WriteFiles) = %d, want 1", len(pt.WriteFiles))
	}
	wf := pt.WriteFiles[0]
	if wf.Turn != 1 {
		t.Errorf("WriteFiles[0].Turn = %d, want 1", wf.Turn)
	}
	if wf.Path != "fib.go" {
		t.Errorf("WriteFiles[0].Path = %q, want %q", wf.Path, "fib.go")
	}
	if !wf.Succeeded {
		t.Error("WriteFiles[0].Succeeded should be true")
	}
}

func TestParse_WriteFileNoPath(t *testing.T) {
	// Simulates the bug where model omits path argument.
	input := `[TURN 1]
[tool: write_file map[content:package main

func main() {}
]]
[result]
error: write_file requires a 'path' argument specifying the filename (e.g. path="game.go"); no path was provided
[TURN 2]
[tool: write_file map[content:package main

func main() {}
 path:main.go]]
[result]
ok: wrote 42 bytes to main.go
[done — no further tool calls]
`
	pt := Parse([]byte(input))
	if len(pt.WriteFiles) != 2 {
		t.Fatalf("len(WriteFiles) = %d, want 2", len(pt.WriteFiles))
	}
	// First: no path, failed
	if pt.WriteFiles[0].Path != "" {
		t.Errorf("WriteFiles[0].Path = %q, want empty", pt.WriteFiles[0].Path)
	}
	if pt.WriteFiles[0].Succeeded {
		t.Error("WriteFiles[0].Succeeded should be false")
	}
	// Second: has path, succeeded
	if pt.WriteFiles[1].Path != "main.go" {
		t.Errorf("WriteFiles[1].Path = %q, want %q", pt.WriteFiles[1].Path, "main.go")
	}
	if !pt.WriteFiles[1].Succeeded {
		t.Error("WriteFiles[1].Succeeded should be true")
	}
}

func TestExtractWritePath(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{" path:game.go]]", "game.go"},
		{"} path:fib.go]]", "fib.go"},
		{"return nil\n path:templates/board.html]]", "templates/board.html"},
		{"no path here]]", ""},
		{"]]", ""},
	}
	for _, tc := range cases {
		got := extractWritePath(tc.line)
		if got != tc.want {
			t.Errorf("extractWritePath(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestParse_Truncated(t *testing.T) {
	// Trace ends mid-result with no termination marker.
	input := `[TURN 1]
[tool: run_command map[command:go test ./...]]
[result]
stdout:
--- FAIL: TestFib (0.00s)
`
	pt := Parse([]byte(input))

	if pt.TerminationMarker != "" {
		t.Errorf("TerminationMarker = %q, want empty", pt.TerminationMarker)
	}
	if len(pt.Commands) != 1 {
		t.Fatalf("len(Commands) = %d, want 1", len(pt.Commands))
	}
	c := pt.Commands[0]
	if c.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 (truncated)", c.ExitCode)
	}
}

func TestParse_MultipleRunsOfSameCommand(t *testing.T) {
	input := `[TURN 1]
[tool: run_command map[command:go test ./...]]
[result]
stdout:
FAIL

exit: exit status 1
[TURN 2]
[tool: run_command map[command:go test ./...]]
[result]
stdout:
ok  fib/fib  0.123s

exit: 0
[done — no further tool calls]
`
	pt := Parse([]byte(input))

	if len(pt.Commands) != 2 {
		t.Fatalf("len(Commands) = %d, want 2", len(pt.Commands))
	}
	if pt.Commands[0].ExitCode != 1 {
		t.Errorf("Commands[0].ExitCode = %d, want 1", pt.Commands[0].ExitCode)
	}
	if pt.Commands[1].ExitCode != 0 {
		t.Errorf("Commands[1].ExitCode = %d, want 0", pt.Commands[1].ExitCode)
	}

	last := lastRunOf(pt.Commands, "go test ./...")
	if last == nil || last.ExitCode != 0 {
		t.Error("lastRunOf should return the last (successful) run")
	}
}

func TestParse_MonitorTerminated(t *testing.T) {
	input := `[TURN 1]
[tool: run_command map[command:ls]]
[result]
stdout:
fib.go

exit: 0
[TURN 2]
[terminated: monitor_terminated]
`
	pt := Parse([]byte(input))
	if pt.TerminationMarker != "monitor_terminated" {
		t.Errorf("TerminationMarker = %q", pt.TerminationMarker)
	}
}

func TestExtractRunCommand(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{`[tool: run_command map[command:go build ./...]]`, "go build ./..."},
		{`[tool: run_command map[command:go test ./... -v]]`, "go test ./... -v"},
		{`[tool: run_command map[command:ls -la]]`, "ls -la"},
	}
	for _, tc := range cases {
		got := extractRunCommand(tc.line)
		if got != tc.want {
			t.Errorf("extractRunCommand(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestGenerateMechanicalFindings_Basic(t *testing.T) {
	pt := ParsedTrace{
		TerminationMarker: "success",
		Commands: []CommandResult{
			{Turn: 1, Command: "go build ./...", ExitCode: 0, ExitRaw: "0"},
			{Turn: 2, Command: "go test ./...", ExitCode: 0, ExitRaw: "0", Stdout: "ok  fib/fib  0.488s"},
		},
	}
	fired := false
	findings := GenerateMechanicalFindings(
		pt, "success", &fired, nil,
		[]string{"go build ./...", "go test ./..."},
		"fib/fib.go: present (240 bytes)",
	)

	checks := []string{
		"Termination cause: success",
		"Monitor fired: no",
		"go build ./...",
		"→ exit 0",
		"go test ./...",
		"ok  fib/fib  0.488s",
		"## Exit Criteria",
		"## All Commands Run",
		"## Output Files",
		"fib/fib.go: present",
	}
	for _, want := range checks {
		if !contains(findings, want) {
			t.Errorf("findings missing %q\n---\n%s", want, findings)
		}
	}
}

func TestGenerateMechanicalFindings_CriterionNotRun(t *testing.T) {
	pt := ParsedTrace{
		TerminationMarker: "timeout",
		Commands: []CommandResult{
			{Turn: 1, Command: "ls", ExitCode: 0, ExitRaw: "0"},
		},
	}
	fired := false
	findings := GenerateMechanicalFindings(
		pt, "timeout", &fired, nil,
		[]string{"go build ./...", "go test ./..."},
		"fib/fib.go: missing",
	)

	if !contains(findings, "Not run during this execution") {
		t.Errorf("expected 'Not run during this execution' in findings\n---\n%s", findings)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
