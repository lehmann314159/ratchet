package execution

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"ratchet/internal/ollama"
)

// TestCallMonitorModelCapsGenerationAndStaysResident locks in the two request
// knobs that keep MONITOR_EXECUTION from starving the concurrently-running
// EXECUTE_BEAD model: a small num_predict (its output is two short lines; an
// uncapped call once ran 4081 tokens over 11m39s under the format:"json"
// grammar, contending for the GPU the whole time — lsystem-demo-run bead 2)
// and keep_alive:-1 so the model isn't unloaded/reloaded every polling tick
// (Chat()'s default is unload-after-use).
func TestCallMonitorModelCapsGenerationAndStaysResident(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.Write([]byte(`{"message":{"role":"assistant","content":"DECISION: NO_FIRE\nREASON: fine"},"done":true}`))
	}))
	defer srv.Close()

	oc := ollama.New(srv.URL)
	if _, err := callMonitorModel(context.Background(), oc, "m", "[TURN 1] read_file x"); err != nil {
		t.Fatalf("callMonitorModel: %v", err)
	}
	if got := gotBody["keep_alive"]; got != float64(-1) {
		t.Errorf("keep_alive = %v, want -1", got)
	}
	opts, _ := gotBody["options"].(map[string]any)
	if got := opts["num_predict"]; got != float64(monitorNumPredict) {
		t.Errorf("options.num_predict = %v, want %d", got, monitorNumPredict)
	}
	if got := opts["num_ctx"]; got != float64(ollama.MonitorNumCtx) {
		t.Errorf("options.num_ctx = %v, want %d", got, ollama.MonitorNumCtx)
	}
}

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
