package qualify

import (
	"testing"

	"ratchet/internal/ollama"
)

func TestBeadSpecParsing(t *testing.T) {
	s := BeadSpec{
		OutputFiles: []string{"lexer.go", "lexer_test.go", "api_check_test.go"},
		ExitCriteria: []string{
			"grep -q 'func TestLexer' lexer_test.go",
			`go test -run TestLexer ./...`,
			"grep -q \"func TestLexerErrors\" lexer_test.go",
		},
	}
	if got := s.ImplFiles(); len(got) != 1 || got[0] != "lexer.go" {
		t.Fatalf("ImplFiles = %v", got)
	}
	if got := s.TestFiles(); len(got) != 1 || got[0] != "lexer_test.go" {
		t.Fatalf("TestFiles = %v", got)
	}
	req := s.RequiredTestFuncs()
	if len(req) != 2 || req[0] != "TestLexer" || req[1] != "TestLexerErrors" {
		t.Fatalf("RequiredTestFuncs = %v", req)
	}
}

func TestPercentileMedian(t *testing.T) {
	xs := []float64{5, 1, 3, 2, 4}
	if m := median(xs); m != 3 {
		t.Errorf("median = %v want 3", m)
	}
	if p := percentile(xs, 0.5); p != 3 {
		t.Errorf("p50 = %v want 3", p)
	}
	if p := percentile(xs, 1); p != 5 {
		t.Errorf("p100 = %v want 5", p)
	}
	if median(nil) != 0 || percentile(nil, 0.9) != 0 {
		t.Errorf("empty input should be 0")
	}
}

func TestCorpusSelect(t *testing.T) {
	b313, b314 := int64(313), int64(314)
	c := &Corpus{Cases: []Case{
		{Name: "006-W", Meta: Meta{Seq: 6, Verb: "REFINE_TESTS_WRITE", BeadID: &b313, RefinementCycle: ptr(1)}},
		{Name: "007-C", Meta: Meta{Seq: 7, Verb: "REFINE_TESTS_CRITIQUE", BeadID: &b313, RefinementCycle: ptr(1)}},
		{Name: "013-W", Meta: Meta{Seq: 13, Verb: "REFINE_TESTS_WRITE", BeadID: &b314, RefinementCycle: ptr(1)}},
	}}
	all, err := c.Select("REFINE_TESTS_WRITE", nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("select all: %v n=%d", err, len(all))
	}
	one, err := c.Select("REFINE_TESTS_WRITE", []string{"b314-c1"})
	if err != nil || len(one) != 1 || one[0].Name != "013-W" {
		t.Fatalf("select b314-c1: %v %+v", err, one)
	}
	if _, err := c.Select("REFINE_TESTS_WRITE", []string{"b999-c1"}); err == nil {
		t.Fatal("expected error for missing case")
	}
}

func TestDeadTurnDetection(t *testing.T) {
	r := ReplayResult{Calls: []ollama.CallRecord{
		{DoneReason: "length", Response: ollama.Message{}},                                  // dead
		{DoneReason: "stop", Response: ollama.Message{Content: "ok"}},                        // fine
		{DoneReason: "length", Response: ollama.Message{Content: "partial"}},                 // not dead (has content)
		{DoneReason: "length", Response: ollama.Message{ToolCalls: []ollama.ToolCall{{}}}},   // not dead (tool call)
	}}
	if got := r.deadTurns(); got != 1 {
		t.Fatalf("deadTurns = %d want 1", got)
	}
}

func ptr(i int64) *int64 { return &i }
