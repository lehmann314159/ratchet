package execution

import "testing"

// TestMechanicalLoopPatternCheck covers the two "Explicit loop patterns"
// rules documented in monitorSystemPrompt. Before this check existed, both
// rules were enforced purely by the MONITOR model reading the rule out of the
// prompt against raw trace text, with no mechanical backstop — a weaker model
// could miss an instance the rule was written to catch (observed live:
// checkers-v8 bead 627 attempt 2, a repeated identical self-check command
// with no intervening write did not fire MONITOR).
func TestMechanicalLoopPatternCheck(t *testing.T) {
	tests := []struct {
		name      string
		trace     string
		wantFired bool
	}{
		{
			name: "identical run_command output twice, no intervening write — FIRE",
			trace: `[TURN 1]
[tool: run_command map[command:grep -q Foo x.go && echo Pass || echo Fail]]
[result]
stdout:
Fail

exit: 0
[TURN 2]
[tool: run_command map[command:grep -q Foo x.go && echo Pass || echo Fail]]
[result]
stdout:
Fail

exit: 0
`,
			wantFired: true,
		},
		{
			name: "same command, different output — no fire",
			trace: `[TURN 1]
[tool: run_command map[command:go test ./...]]
[result]
stdout:
FAIL: TestFoo

exit: exit status 1
[TURN 2]
[tool: run_command map[command:go test ./...]]
[result]
stdout:
ok

exit: 0
`,
			wantFired: false,
		},
		{
			name: "identical command output twice, but a write_file happened in between — no fire",
			trace: `[TURN 1]
[tool: run_command map[command:go test ./...]]
[result]
stdout:
FAIL: TestFoo

exit: exit status 1
[TURN 2]
[tool: write_file map[content:package main
path:x.go]]
[result]
ok: wrote 20 bytes to x.go
[TURN 3]
[tool: run_command map[command:go test ./...]]
[result]
stdout:
FAIL: TestFoo

exit: exit status 1
`,
			wantFired: false,
		},
		{
			name: "missing-path error appears twice — FIRE",
			trace: `[TURN 1]
[tool: write_file map[content:package main]]
[result]
error: write_file requires a 'path' argument specifying the filename (e.g. path="game.go"); no path was provided
[TURN 2]
[tool: write_file map[content:package main]]
[result]
error: write_file requires a 'path' argument specifying the filename (e.g. path="game.go"); no path was provided
`,
			wantFired: true,
		},
		{
			name: "missing-path error appears once — no fire",
			trace: `[TURN 1]
[tool: write_file map[content:package main]]
[result]
error: write_file requires a 'path' argument specifying the filename (e.g. path="game.go"); no path was provided
[TURN 2]
[tool: write_file map[content:package main
path:x.go]]
[result]
ok: wrote 20 bytes to x.go
`,
			wantFired: false,
		},
		{
			name: "empty trace — no fire",
			trace: "",
			wantFired: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason := mechanicalLoopPatternCheck(tc.trace)
			fired := reason != ""
			if fired != tc.wantFired {
				t.Errorf("mechanicalLoopPatternCheck() fired=%v (reason=%q), want fired=%v", fired, reason, tc.wantFired)
			}
		})
	}
}
