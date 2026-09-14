import sys
sys.path.insert(0, "/private/tmp/claude-501/-Users-mike-Documents-GitHub-ratchet/f5519e05-1f50-4803-9a55-6fbc3753dac6/scratchpad")
from deck_common import *

DECK = 4
LABEL = "RATCHET · PART 4/6 · ZERO DOMAIN KNOWLEDGE"
prs = new_deck()
TOTAL = 17

title_slide(prs, DECK, "Write for Zero Domain Knowledge",
    "Writing a spec precise enough for a model that won't fill your gaps kindly",
    "Part 4 of 6. The single hardest skill in the whole system: removing every gap a small "
    "model could misinterpret, before it ever gets the chance to guess.",
    title_size=40, series_total=6)

# 2 the test
quote_slide(prs, 2, TOTAL, LABEL, "The Test",
    "Does a sentence, read literally, pin down exactly one value or behavior — or is it "
    "compatible with two or more implementations a careful, literal reader could each defend?",
    "Nobody has to be wrong for a spec to be dangerously unclear. Almost by construction, the "
    "author already has one reading in mind and cannot see the other one.")

# 3 field guide intro
content_slide(prs, 3, TOTAL, LABEL, "A Field Guide to the Gaps", "18 classes, each earned by an actual incident",
    bullets=[
        ("Geometry & arithmetic ", "want the computed number, not the formula. \"Moves toward "
         "lower row indices\" isn't precise until Δrow/Δcol are stated."),
        ("Ownership ", "needs field-level precision — independent copy, or shared reference? "
         "Judgment-only; no keyword scan resolves it."),
        "Both generalize past their origin domain — arithmetic went from board-game geometry "
        "to an FNV-1a hash worked all the way through.",
    ], bullet_size=18)

# 4 construction form story pt1
content_slide(prs, 4, TOTAL, LABEL, "The Stickiest Class", "How a value gets constructed, not just its type",
    bullets=[
        "A VM's AST nodes were shown as value literals throughout. The scaffold's interface "
        "was satisfied by both a value and a pointer — nothing forced either choice.",
        "One bead: the parser picked pointers, the test (same doc) picked values. Days later, "
        "a different bead hit the identical defect independently.",
        "Two independent beads, two independent models, one un-pinned decision.",
    ], bullet_size=18)

# 5 construction form pt2
quote_slide(prs, 5, TOTAL, LABEL, "The Stickiest Class — cont.",
    "Even the hand-verified \"known-good\" reference implementation, built later to grade "
    "candidate models, had the same pointer/value inconsistency baked in.",
    "Nobody wrote it carelessly — the ambiguity in the original spec was load-bearing enough "
    "that a deliberately-careful reference implementation reproduced it anyway. The fix wasn't "
    "\"be more careful\" — it was a mechanical check flagging any shared type crossing a bead "
    "boundary with no explicit pointer-or-value statement.")

# 6 standard rules class
content_slide(prs, 6, TOTAL, LABEL, "The Sharpest Finding", "\"Standard movement rules\" — zero content, and invisible",
    bullets=[
        "A chess doc described knight/bishop/rook/queen movement as \"standard\" — a category "
        "name standing in for the rule. An independent reviewer caught six other real problems "
        "in the same pass and never flagged this line.",
        "A reviewer who shares the author's domain fluency has the identical blind spot, for "
        "the identical reason the author didn't notice writing it.",
        "Confirmed, not assumed: after the doc was rewritten with explicit rank/file deltas, a "
        "second independent review raised zero findings on that section.",
    ], bullet_size=17)

# 7 standard rules conclusion
quote_slide(prs, 7, TOTAL, LABEL, "The Sharpest Finding — the consequence",
    "For this one class, judgment review is not a substitute for a mechanical check. Full stop.",
    "The only reliable catch is a keyword scan — grep for standard/normal/usual/conventional "
    "next to a rules-or-procedure noun — because the whole failure mode is that a fluent "
    "reader, human or model, won't consciously notice anything is missing.", quote_color=ACCENT)

# 8 guard precedence
content_slide(prs, 8, TOTAL, LABEL, "Newest Addition", "Guard precedence: which error wins?",
    bullets=[
        "When more than one validation check can fail on the same input, and each maps to a "
        "different error, the check order has to be stated explicitly.",
        "Every individual guard statement is correct in isolation — they're just jointly "
        "silent about precedence. A worked example exercising each guard alone never surfaces the overlap.",
        "Independently flagged by three of four fresh reviewers, across three different design "
        "docs, in the same review batch — about as strong a signal as this catalogue gets.",
    ], bullet_size=17)

# 9 tooling draft-design-doc
content_slide(prs, 9, TOTAL, LABEL, "The Tooling", "draft-design-doc — three hard rules",
    bullets=[
        ("Every worked example is script-verified. ", "A throwaway script, run in-session, "
         "cited inline. No citation = compliance failure, not a style nit."),
        ("Gaps are surfaced, never silently filled. ", "Anything the template requires but the "
         "prose doesn't pin goes to a standing Open Questions section."),
        ("Load-bearing literals get an explicit pin. ", "A bullet naming the exact bead and "
         "value, generated automatically rather than hoped-for."),
    ], bullet_size=18)

# 10 tooling checkdesigndoc
content_slide(prs, 10, TOTAL, LABEL, "The Tooling", "checkdesigndoc — an over-flagging report, tuned hard",
    bullets=[
        "A mechanical scanner for the keyword-detectable classes. Explicitly a report, never "
        "a pass/fail gate.",
        "First version of the arithmetic check flagged every Go pointer type (g *Game reads "
        "like arithmetic to a naive regex) — rebuilt to trigger only on real arithmetic "
        "language: \"sum of,\" \"the formula,\" \"FNV.\"",
        "\"Above\" and \"below\" turned out to almost always be ordinary cross-references, not "
        "geometry — dropped from the trigger list entirely.",
        "Tuned against every real doc in the project's history: catches every known-bad bead, "
        "zero false positives on the clean ones.",
    ], bullet_size=15, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 11 tooling check-design-doc
content_slide(prs, 11, TOTAL, LABEL, "The Tooling", "check-design-doc — mirrors DECOMPOSE → AUDIT → RECONCILE",
    bullets=[
        "Runs the mechanical scan first, then hands the raw doc — not the mechanical report — "
        "to an independent review pass, so it forms its own findings first.",
        "Reconciles both: anything mechanically flagged but not addressed is carried forward, "
        "never silently dropped. The one class judgment can't catch is always carried forward.",
        "Every open item gets an explicit resolution — accepted, rewritten, or affirmatively "
        "waived with the reason recorded inline — before a doc is clear to hand to the pipeline.",
    ], bullet_size=17)

# 12 bead sizing
content_slide(prs, 12, TOTAL, LABEL, "Sizing a Bead", "A human judgment, on purpose",
    bullets=[
        "Line count is the wrong metric — a 230-line bead finished clean every time; a "
        "150-line bead escalated across five separate runs.",
        "The real signal: responsibility count AND integration tightness, together. Either alone produced false alarms.",
        "Letting the planning model decide bead splits failed structurally: it can't reliably "
        "estimate whether the execution model will spiral, any more than it knows its own "
        "reasoning has stalled.",
        "The same doc, run twice, produced two different bead counts by chance — merge spiraled "
        "for 3 cycles, split finished clean on the first pass.",
    ], bullet_size=15, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 13 bead sizing resolution
quote_slide(prs, 13, TOTAL, LABEL, "Sizing a Bead — the fix",
    "The numbered bead list in a human-authored design doc is now authoritative. The planning "
    "step transcribes it faithfully; it doesn't get to exercise discretion over it.",
    "A lint (size × integration, cleared by a one-line rationale) flags likely trouble before "
    "a project starts. Calibrated against the project's history: catches every case known to "
    "cause trouble, flags nothing known to run clean.")

# 14 precision survives rewriting
content_slide(prs, 14, TOTAL, LABEL, "Precision Has to Survive Being Rewritten", "A pin, once placed, can still vanish",
    bullets=[
        "If a doc pins a value to bead \"compiler\" and a later step renames or splits it, the "
        "name-based lookup silently finds nothing — no test fails, no warning fires.",
        "A bullet naming two beads at once only ever attached to the first name a regex matched.",
        "Several later steps rewrite a bead's spec during normal recovery — each one a place a "
        "verbatim pin could quietly vanish unless the injection step reruns explicitly, at every one of them.",
    ], bullet_size=17)

# 15 the fix philosophy
quote_slide(prs, 15, TOTAL, LABEL, "The Fix, Restated",
    "A good decision made once isn't enough if nothing re-verifies it survived everything "
    "that happened afterward.",
    "The same principle as the mechanical gates in the architecture piece, applied to prose "
    "instead of code.")

# 16 what this buys/costs
content_slide(prs, 16, TOTAL, LABEL, "What This Buys, and What It Costs", "Precision replaces judgment",
    bullets=[
        "With a frontier model, ambiguity gets absorbed by judgment, quietly, mostly correctly.",
        "With a fleet of small local models, judgment isn't a resource you get to spend — so "
        "precision has to do the job instead.",
        "The honest cost: writing this way is slower. Every worked example run through a "
        "script, every gap surfaced, every cross-bead value traced to confirm it's pinned somewhere.",
    ], bullet_size=18)

closing_slide(prs, 17, TOTAL, LABEL,
    "Precision has to be built, checked, and re-verified —",
    "the same way any other part of a reliable system is. That's the whole trade this piece "
    "has been describing.",
    next_label="Draft, Check, Sign Off — the workflow that turns rough prose into a design "
               "doc the pipeline is allowed to trust.")

out = "/Users/mike/Documents/GitHub/ratchet/writing/decks/04-write-for-zero-domain-knowledge.pptx"
import os
os.makedirs(os.path.dirname(out), exist_ok=True)
prs.save(out)
print("Saved", out, "slides:", len(prs.slides._sldIdLst))
