package project

import (
	"strings"
	"testing"
)

func TestApplyGuidance_NoOpWhenNothingGiven(t *testing.T) {
	base := "implement the game"
	got, num, err := applyGuidance(base, "", 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("applyGuidance: %v", err)
	}
	if got != base {
		t.Errorf("full_text = %q, want unchanged %q", got, base)
	}
	if num != 0 {
		t.Errorf("newNoteNumber = %d, want 0", num)
	}
}

func TestApplyGuidance_AppendsFirstNoteAsNote1(t *testing.T) {
	base := "implement the game"
	got, num, err := applyGuidance(base, "watch out for off-by-one in the board index", 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("applyGuidance: %v", err)
	}
	if num != 1 {
		t.Errorf("newNoteNumber = %d, want 1", num)
	}
	if !strings.HasPrefix(got, base) {
		t.Errorf("expected base prose preserved verbatim at the start, got: %q", got)
	}
	if !strings.Contains(got, "### Note 1 — 2026-01-01T00:00:00Z") {
		t.Errorf("expected a Note 1 heading, got: %q", got)
	}
	if !strings.Contains(got, "watch out for off-by-one in the board index") {
		t.Errorf("expected note text present, got: %q", got)
	}
	if !strings.Contains(got, "Status: active") {
		t.Errorf("expected the new note marked active, got: %q", got)
	}
}

func TestApplyGuidance_SecondNoteAppendsWithoutTouchingFirst(t *testing.T) {
	base := "implement the game"
	afterFirst, _, err := applyGuidance(base, "first note", 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("applyGuidance (first): %v", err)
	}

	afterSecond, num, err := applyGuidance(afterFirst, "second note", 0, "2026-01-02T00:00:00Z")
	if err != nil {
		t.Fatalf("applyGuidance (second): %v", err)
	}
	if num != 2 {
		t.Errorf("newNoteNumber = %d, want 2", num)
	}
	if !strings.Contains(afterSecond, "### Note 1 — 2026-01-01T00:00:00Z") {
		t.Error("expected Note 1 to still be present")
	}
	if !strings.Contains(afterSecond, "first note") {
		t.Error("expected Note 1's text to still be present, unedited")
	}
	if !strings.Contains(afterSecond, "### Note 2 — 2026-01-02T00:00:00Z") {
		t.Error("expected Note 2 to be present")
	}

	base2, notes := parseGuidanceLog(afterSecond)
	if base2 != base {
		t.Errorf("base prose = %q, want %q (never touched by appends)", base2, base)
	}
	if len(notes) != 2 {
		t.Fatalf("parsed %d notes, want 2", len(notes))
	}
	if notes[0].Status != "active" {
		t.Errorf("Note 1 status = %q, want active (untouched by an unrelated append)", notes[0].Status)
	}
}

func TestApplyGuidance_SupersedeMarksOldNoteWithoutEditingItsText(t *testing.T) {
	afterFirst, _, err := applyGuidance("base prose", "original guidance", 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("applyGuidance (first): %v", err)
	}

	afterSecond, num, err := applyGuidance(afterFirst, "corrected guidance", 1, "2026-01-02T00:00:00Z")
	if err != nil {
		t.Fatalf("applyGuidance (supersede): %v", err)
	}
	if num != 2 {
		t.Errorf("newNoteNumber = %d, want 2", num)
	}

	_, notes := parseGuidanceLog(afterSecond)
	if len(notes) != 2 {
		t.Fatalf("parsed %d notes, want 2", len(notes))
	}
	if notes[0].Status != "superseded by Note 2" {
		t.Errorf("Note 1 status = %q, want %q", notes[0].Status, "superseded by Note 2")
	}
	if notes[0].Text != "original guidance" {
		t.Errorf("Note 1 text = %q, want unchanged %q (superseding must never edit the original text)", notes[0].Text, "original guidance")
	}
	if notes[1].Status != "active" {
		t.Errorf("Note 2 status = %q, want active", notes[1].Status)
	}
	if notes[1].Text != "corrected guidance" {
		t.Errorf("Note 2 text = %q, want %q", notes[1].Text, "corrected guidance")
	}
}

func TestApplyGuidance_RetractWithoutReplacementMarksRetracted(t *testing.T) {
	afterFirst, _, err := applyGuidance("base prose", "a note that turns out to be wrong", 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("applyGuidance (first): %v", err)
	}

	afterRetract, num, err := applyGuidance(afterFirst, "", 1, "2026-01-02T00:00:00Z")
	if err != nil {
		t.Fatalf("applyGuidance (retract): %v", err)
	}
	if num != 0 {
		t.Errorf("newNoteNumber = %d, want 0 (no new note added by a pure retraction)", num)
	}

	_, notes := parseGuidanceLog(afterRetract)
	if len(notes) != 1 {
		t.Fatalf("parsed %d notes, want 1", len(notes))
	}
	if notes[0].Status != "retracted" {
		t.Errorf("Note 1 status = %q, want retracted", notes[0].Status)
	}
	if notes[0].Text != "a note that turns out to be wrong" {
		t.Errorf("Note 1 text = %q, want unchanged", notes[0].Text)
	}
}

func TestApplyGuidance_SupersedesUnknownNoteErrors(t *testing.T) {
	_, _, err := applyGuidance("base prose", "some note", 99, "2026-01-01T00:00:00Z")
	if err == nil {
		t.Fatal("expected an error for --supersedes referencing a note that doesn't exist")
	}
}

func TestParseGuidanceLog_NoLogSectionReturnsFullTextAsBase(t *testing.T) {
	base, notes := parseGuidanceLog("just plain prose, never rewound")
	if base != "just plain prose, never rewound" {
		t.Errorf("base = %q, want the full input unchanged", base)
	}
	if notes != nil {
		t.Errorf("notes = %v, want nil", notes)
	}
}

func TestApplyGuidance_MultiParagraphNoteRoundTrips(t *testing.T) {
	note := "First paragraph of guidance.\n\nSecond paragraph with more detail."
	got, _, err := applyGuidance("base prose", note, 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("applyGuidance: %v", err)
	}
	_, notes := parseGuidanceLog(got)
	if len(notes) != 1 {
		t.Fatalf("parsed %d notes, want 1", len(notes))
	}
	if notes[0].Text != note {
		t.Errorf("round-tripped text = %q, want %q", notes[0].Text, note)
	}
}
