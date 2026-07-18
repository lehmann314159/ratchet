package verbs

import (
	"strings"
	"testing"
)

// TestBuildAuditUserMsg_InstructsOmittingConcededFindings reproduces the
// fractal-smoke-2 (project 105) incident: AUDIT round 2 conceded that its
// own Julia/Sierpinski file-sharing findings were now satisfied (worded as
// "now includes... satisfying the independence requirement") but still
// listed them as findings entries instead of omitting them. Complementary to
// ReconcileResponse.AlreadyAddressed (reconcile_decomposition.go): that field
// lets RECONCILE self-certify a repeat regardless of AUDIT's exact wording,
// but AUDIT re-listing an already-conceded finding at all is still worth
// preventing at the source — fewer spurious findings means fewer rounds
// where RECONCILE has to invoke AlreadyAddressed in the first place. The
// prompt must state unambiguously that a conceded finding is omitted
// entirely, not re-listed with acknowledgment language.
func TestBuildAuditUserMsg_InstructsOmittingConcededFindings(t *testing.T) {
	beads := []beadState{{Title: "B01", FullText: "do X", OutputFiles: []string{"a.go"}, ExitCriteria: []string{"go build ./..."}}}
	history := []debateRound{{
		RoundNumber:    1,
		CritiqueText:   `{"findings":[{"bead_title":"B01","issue":"shares a.go","design_doc_reference":"N/A"}],"overall_verdict":"issues_found"}`,
		Reconciliation: `{"responses":[{"bead_title":"B01","action":"agree_and_fix","reason":"added preservation instruction"}]}`,
		Outcome:        "disagreed_continuing",
	}}

	msg := buildAuditUserMsg("design doc text", beads, history)

	if !strings.Contains(msg, "## Previous Debate History") {
		t.Fatal("missing Previous Debate History section")
	}
	for _, want := range []string{
		"omit that finding from your findings array",
		"Do not include an entry that acknowledges or concedes the fix",
		"say nothing about that bead at all",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected prompt to contain %q\ngot:\n%s", want, msg)
		}
	}
}

// TestBuildAuditUserMsg_NoHistoryOmitsDebateSection is the companion
// non-regression case: on the first round (no history yet), the debate
// history section — and the new instruction — must not appear at all.
func TestBuildAuditUserMsg_NoHistoryOmitsDebateSection(t *testing.T) {
	beads := []beadState{{Title: "B01", FullText: "do X", OutputFiles: []string{"a.go"}, ExitCriteria: []string{"go build ./..."}}}

	msg := buildAuditUserMsg("design doc text", beads, nil)

	if strings.Contains(msg, "Previous Debate History") {
		t.Error("first-round message must not contain a debate history section")
	}
}
