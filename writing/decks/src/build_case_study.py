"""Build writing/decks/case-study-the-last-run.pptx — a standalone companion deck to
writing/case-study-the-last-run.md. Not part of the numbered 6-part series (no
"Part N of 6"), same reason docs/state-machine.pptx isn't: this tracks one specific
burn-in run's actual log, not a conceptual deep dive. Writes its own title/closing
slides for that reason, reusing the shared visual library for continuity.

Run: python3 writing/decks/src/build_case_study.py
"""
import sys
sys.path.insert(0, "/private/tmp/claude-501/-Users-mike-Documents-GitHub-ratchet/f5519e05-1f50-4803-9a55-6fbc3753dac6/scratchpad")
from deck_common import *
from deck_common import _set_run

LABEL = "RATCHET · CASE STUDY · THE LAST RUN"
prs = new_deck()
TOTAL = 18

# ---------------------------------------------------------------- 1: title (custom)
slide = add_slide(prs, bg=INK)
add_kicker(slide, "Ratchet · A Burn-In Run, Followed Step by Step", color=RGBColor(0x9A, 0xC7, 0xD3), top=Inches(2.35))
box, tf = add_textbox(slide, Inches(0.9), Inches(2.85), Inches(11.5), Inches(1.15))
p = tf.paragraphs[0]
p.line_spacing = 1.0
r = p.add_run()
_set_run(r, "The Last Run", size=54, color=WHITE, font=TITLE_FONT, bold=True)
box2, tf2 = add_textbox(slide, Inches(0.95), Inches(4.15), Inches(10.8), Inches(0.7))
p2 = tf2.paragraphs[0]
r2 = p2.add_run()
_set_run(r2, "One project, four beads, every verb call — not summarized",
          size=22, color=RGBColor(0xC9, 0xCD, 0xD2), font=BODY_FONT, italic=True)
box3, tf3 = add_textbox(slide, Inches(0.95), Inches(5.15), Inches(10.2), Inches(1.7))
p3 = tf3.paragraphs[0]
p3.line_spacing = 1.3
r3 = p3.add_run()
_set_run(r3, "A companion to the Ratchet article series — not a new part of it. This is the "
             "literal last of the burn-in's 23 project runs, walked through step by step "
             "instead of summarized, sourced directly from the live run log.",
          size=15, color=RGBColor(0x9A, 0x9F, 0xA6), font=BODY_FONT)
add_footer_dark(slide, 1, TOTAL, LABEL)

# ---------------------------------------------------------------- 2: why this run
content_slide(prs, 2, TOTAL, LABEL, "Why This Run", "The literal last of twenty-three",
    bullets=[
        "Seeded 13:50 MDT, the final day of the burn-in — project 23 of 23. Its last bead "
        "produced the single most dramatic finding of the entire corpus.",
        "Three of its four beads hit something genuinely interesting on the way. None of "
        "it turned into an escalation.",
        "That combination — friction that never becomes a fire — is what the rest of this "
        "deck tries to make concrete, one step at a time.",
    ], bullet_size=18)

# ---------------------------------------------------------------- 3: the project
content_slide(prs, 3, TOTAL, LABEL, "The Project", "A task list web server, four files, four beads",
    bullets=[
        "Add named tasks, mark them done, delete them. One HTML page, updated in place via "
        "HTMX. In-memory storage — the list resets on restart.",
        ("Four beads, in dependency order: ", "task-store (the data layer) → templates "
         "(HTML rendering) → http-handlers (the HTTP layer) → integration (wires the "
         "other three together, the one end-to-end test)."),
        "The design doc for this run was byte-identical to the one used for run 1 of the "
        "same project, three hours earlier the same day. Same prompt, same models, same "
        "everything — which is part of what makes comparing the two runs interesting.",
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# ---------------------------------------------------------------- 4: headline stat
stat_slide(prs, 4, TOTAL, LABEL, "Headline", "4 / 4",
    "beads succeeded · 0 escalations · ~2h58m wall time",
    "Two named findings absorbed along the way. Neither shows up in the project's final "
    "state — SELECT status FROM beads reads succeeded four times, same as a run where "
    "nothing interesting happened at all.")

# ---------------------------------------------------------------- 5: bootstrap
content_slide(prs, 5, TOTAL, LABEL, "Bootstrap", "Before any bead runs",
    bullets=[
        "SURVEY_SPEC → VERIFY_MANIFEST (model-free) → CERTIFY_MANIFEST → DECOMPOSE_SPEC "
        "produces the four beads above, in order.",
        ("AUDIT_DECOMPOSITION finds something: ", "the http-handlers bead's exit criteria "
         "only tested one of four handler functions."),
        ("The interesting part: ", "run 1 of this exact doc, three hours earlier, hit the "
         "same underlying gap — but AUDIT worded the complaint completely differently "
         "(\"no runtime test at all\" vs. \"doesn't cover the other three handlers\"). Same "
         "weak point in the doc, a fresh model call every time it's looked at.",),
        "RECONCILE_DECOMPOSITION fixes it: names all four handler test functions "
         "explicitly. agree_and_fix — the same resolution run 1 reached independently.",
    ], bullet_size=15, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# ---------------------------------------------------------------- 6: bead 1 header
section_slide(prs, "Bead 1 — task-store",
    "The data layer: add, toggle, delete, and a pinned rule — deleted IDs are never reused",
    note="One REFINE cycle. Unusually slow. Zero consequence.")

# ---------------------------------------------------------------- 7: bead 1 steps
content_slide(prs, 7, TOTAL, LABEL, "Bead 1 — task-store", "WRITE → CRITIQUE (24 minutes) → JUDGE → EXECUTE",
    bullets=[
        "WRITE produces a test against the pinned ID-reuse scenario. Compiles clean.",
        ("CRITIQUE spends about 24 minutes ", "re-deriving, by hand, what should happen to "
         "the internal counter across Add → Delete → Add. Its own run_go_snippet gives it "
         "an unexpected result; its reasoning trace visibly cycles through wrong guesses "
         "(\"maybe the problem is the mutex?\"), including one turn with no usable output at all."),
        ("Nothing intervenes, because nothing needs to. ", "A few turns later CRITIQUE "
         "correctly re-derives the logic on its own and returns all_correct: true. JUDGE "
         "approves; EXECUTE runs clean, first attempt; ADJUDICATE declares success."),
        "Not every stall is a bug, and not every slow cycle needs a mechanism to catch it.",
    ], bullet_size=14.5, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# ---------------------------------------------------------------- 8: bead 2 header
section_slide(prs, "Bead 2 — templates",
    "The bead this whole run was being watched for",
    note="Run 1's equivalent bead had a scaffold defect that cost about an hour and a "
         "quarter. Would it recur on a byte-identical doc? And if it did, would it cost "
         "the same again?")

# ---------------------------------------------------------------- 9: bead 2 pt1 — scaffold gap + wrong claim
content_slide(prs, 9, TOTAL, LABEL, "Bead 2 — The Scaffold Gap, Confirmed", "2 for 2, before REFINE even starts",
    bullets=[
        ("Reading the freshly-generated scaffold directly settles it: ", "RenderTaskList is "
         "missing again — the same function DECOMPOSE omitted from this exact file on run 1. "
         "Structurally identical gap. Not a one-off; a reproducible defect in how this file "
         "gets scaffolded."),
        ("CRITIQUE cycle 1: ", "one legitimate finding (an over-specified test), and one "
         "confidently wrong one — that html/template will HTML-escape a literal <script> tag. "
         "It won't; a throwaway Go snippet reproducing the exact case confirms the tag comes "
         "through unescaped."),
        ("It catches itself. ", "A transient malformed-JSON retry buys it a second attempt, "
         "and on that attempt CRITIQUE tests its own claim empirically instead of asserting "
         "it from memory — and drops it. JUDGE never sees the wrong claim at all."),
    ], bullet_size=14, top_bullets=Inches(2.0), bullets_height=Inches(4.8))

# ---------------------------------------------------------------- 10: bead 2 pt2 — the wrong claim comes back, resolution
content_slide(prs, 10, TOTAL, LABEL, "Bead 2 — The Same Wrong Claim, Twice", "Same defect. Different consequence.",
    bullets=[
        ("CRITIQUE cycle 2: ", "the escaping claim comes back — independently re-derived "
         "from a fresh context, no memory of cycle 1 correcting it — and this time applied "
         "to three assertions instead of one. A different transient retry catches it again; "
         "the retry attempt correctly re-derives \"the test is correct\" before landing."),
        ("This isn't a mistake CRITIQUE learned from. ", "It's a belief about one stdlib "
         "fact that this model can independently regenerate wrong, from nothing, on a fresh "
         "pass, no matter how confidently the last pass corrected it."),
        ("EXECUTE runs clean. ", "Reading the finished file directly: RenderTaskList is "
         "present and correct — EXECUTE filled the scaffold gap from the full design doc, "
         "same as run 1. But run 1's REFINE hit a costly pileup because CRITIQUE happened to "
         "flag the missing function by name. This run's CRITIQUE never once raised that "
         "specific complaint. Same defect, twice. Same cost, only once."),
    ], bullet_size=13.5, top_bullets=Inches(2.0), bullets_height=Inches(4.8))

# ---------------------------------------------------------------- 11: bead 3
content_slide(prs, 11, TOTAL, LABEL, "Bead 3 — http-handlers", "One cycle. Eighteen minutes. The fastest bead in the run.",
    bullets=[
        "WRITE tests all four handlers, per the exit criteria RECONCILE corrected at "
        "bootstrap.",
        "CRITIQUE wants an un-pinned \"Done\" status marker asserted — one the design doc "
        "only ever gave as an illustrative example.",
        ("JUDGE approves the test and rejects the finding in the same sentence: ", "\"critique "
         "findings demanding un-pinned Done status assertions are invalid "
         "over-specifications.\" The review layer working exactly as intended, in one move."),
        "A quiet contrast with what happens to the doc's identical \"Done status\" ambiguity "
        "one bead later.",
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# ---------------------------------------------------------------- 12: bead 4 header
section_slide(prs, "Bead 4 — integration",
    "The last bead of the entire 23-run corpus",
    note="Does the done-indicator render as the literal word \"true\", or as a CSS class — "
         "the way the doc's illustrative example happened to phrase it? One objectively "
         "checkable fact, independently re-derived five times, by four different verbs.")

# ---------------------------------------------------------------- 13: the fact
content_slide(prs, 13, TOTAL, LABEL, "The Question at the Center", "Only one answer matches the real code",
    bullets=[
        "The real server renders the boolean field as the literal string \"true\". The "
        "design doc's own example illustrates it as a CSS class or a checkmark instead — "
        "never claiming that's required, but never ruled out either.",
        "Five independent re-derivations of this single fact follow, across four verbs, "
        "before the bead is done.",
    ], bullet_size=19)

# ---------------------------------------------------------------- 14: the chain, cycle by cycle
table_slide(prs, 14, TOTAL, LABEL, "The Chain, Step by Step", "Who got it right",
    headers=["Cycle", "Verb", "What happened"],
    rows=[
        ["1–2", "WRITE → JUDGE", "Two normal settling cycles — a real over-specification "
         "fixed, a global-state test properly refactored."],
        ["2", "EXECUTE", "Stalls — not mechanically. The test checks for \"done\"; the real "
         "render is \"true\"."],
        ["—", "ADJUDICATE", "Correct — runs its own snippet against the real response, "
         "diagnoses the fix directly."],
        ["3", "JUDGE", "Correct — adopts the verified fix."],
        ["4", "JUDGE", "WRONG — reverses cycle 3, back to the doc's illustrative phrasing."],
        ["4", "WRITE", "Visibly thrashes — re-derives the contradiction live, ships a hedge "
         "that tests nothing."],
        ["5", "CRITIQUE", "Correct — independently re-derives the fact a third time, "
         "unprompted."],
        ["5", "JUDGE", "WRONG — overrides CRITIQUE's correct finding, calls it "
         "\"over-literal.\""],
        ["—", "EXECUTE", "Correct — rewrites the assertion itself before running it, and "
         "ships it."],
    ], col_widths=[2, 3, 9], font_size=13, top=Inches(2.15))

# ---------------------------------------------------------------- 15: final tally
stat_slide(prs, 15, TOTAL, LABEL, "Final Tally", "1-for-3",
    "JUDGE, on an already-settled question. Everyone else who touched it: 3-for-3.",
    "Five REFINE cycles, about half an hour of extra churn, on the very last bead the "
    "entire corpus had left to run. Zero escalation. Zero defect in the shipped code.",
    color=STOP)

# ---------------------------------------------------------------- 16: what this run adds up to
content_slide(prs, 16, TOTAL, LABEL, "What One Run Adds Up To", "Friction survives in the log, not the outcome",
    bullets=[
        "Four beads. Three hit something worth stopping for — a slow but self-correcting "
        "model, a scaffold defect that cost nothing the second time, a verb repeatedly "
        "wrong about a fact three others had already nailed down.",
        "None of it needed a human. The project's own final state doesn't hint that any of "
        "it happened.",
        "That's arguably the point of a system built this way: not the absence of "
        "mistakes, but mistakes that never got the chance to become anyone's problem.",
    ], bullet_size=18)

# ---------------------------------------------------------------- 17: source note
content_slide(prs, 17, TOTAL, LABEL, "A Note on Sourcing", "Read from the record, not reconstructed",
    bullets=[
        "Every step in this deck — job timestamps, verdict text, file contents — is read "
        "directly from the burn-in's live run log for tasklist run 2 (project id 24), not "
        "reconstructed from the summary-level account in the main series.",
        "For the surrounding context — what the burn-in was, and how the layered review "
        "architecture in this story got built — see the Ratchet article series.",
    ], bullet_size=18)

# ---------------------------------------------------------------- 18: closing (custom)
slide = add_slide(prs, bg=INK)
add_kicker(slide, "ONE RUN, FOUR BEADS", color=RGBColor(0x9A, 0xC7, 0xD3), top=Inches(1.8))
box, tf = add_textbox(slide, Inches(0.9), Inches(2.3), Inches(11.3), Inches(1.6))
p = tf.paragraphs[0]
p.line_spacing = 1.15
r = p.add_run()
_set_run(r, "succeeded, succeeded, succeeded, succeeded.", size=34, color=WHITE, font=MONO_FONT, bold=True)
box2, tf2 = add_textbox(slide, Inches(0.95), Inches(3.75), Inches(10.6), Inches(2.5))
p2 = tf2.paragraphs[0]
p2.line_spacing = 1.35
r2 = p2.add_run()
_set_run(r2, "Same as a run where nothing interesting happened at all. Everything in this "
             "deck is the difference between those two sentences — and the reason a "
             "framework built on small, unreliable models can still tell them apart.",
          size=17, color=RGBColor(0xC9, 0xCD, 0xD2), font=BODY_FONT)
add_footer_dark(slide, 18, TOTAL, LABEL)

out = "/Users/mike/Documents/GitHub/ratchet/writing/decks/case-study-the-last-run.pptx"
import os
os.makedirs(os.path.dirname(out), exist_ok=True)
prs.save(out)
print("Saved", out, "slides:", len(prs.slides._sldIdLst))
