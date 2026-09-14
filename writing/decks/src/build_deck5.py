import sys
sys.path.insert(0, "/private/tmp/claude-501/-Users-mike-Documents-GitHub-ratchet/f5519e05-1f50-4803-9a55-6fbc3753dac6/scratchpad")
from deck_common import *

DECK = 5
LABEL = "RATCHET · PART 5/5 · THE BURN-IN"
prs = new_deck()
TOTAL = 18

title_slide(prs, DECK, "The Burn-In",
    "The week the team stopped building it and just ran it, honestly",
    "Part 5 of 5 — the last piece. A deliberate stress-test of everything in the first "
    "four pieces: what it found, and what surprised even the people running it.",
    title_size=48, series_total=5)

# 2 the one rule
content_slide(prs, 2, TOTAL, LABEL, "The One Rule", "Nothing gets fixed while it's running",
    bullets=[
        "Every bug, stall, and escalation gets logged and the corpus moves on — recovered "
        "automatically if the framework's own machinery handles it, escalated if it doesn't.",
        "A framework nursed along by someone quietly intervening tells you nothing about how "
        "it behaves unattended — the only way it's ever actually going to run.",
        ("The corpus: ", "nine design docs, from small self-contained utilities to genuinely "
         "integrated systems, each run twice with tie-breakers on disagreement — twenty-three "
         "total project runs, ~74 hours of wall-clock time, mostly unattended."),
    ], bullet_size=18)

# 3 headline stat
stat_slide(prs, 3, TOTAL, LABEL, "The Headline Numbers", "14 / 23",
    "complete · 9 escalated (39%) · 0 showstoppers",
    "The number that matters more than the completion rate: zero repeated root causes. "
    "Every escalation, across all 23 runs, traced to a genuinely different mechanism — "
    "not the same bug wearing different clothes nine times.")

# 4 what the second number means
quote_slide(prs, 4, TOTAL, LABEL, "What \"Zero Repeated Root Causes\" Means",
    "A framework that fails the same way repeatedly has a bug. A framework that fails nine "
    "different ways, each caught and correctly routed to a human, is a framework whose "
    "failure-handling is doing its job.",
    "The right question isn't \"why did 39% need a person\" — it's \"did the 39% get caught "
    "cleanly, and did the other 61% actually deserve to finish.\" Both turned out to be yes.")

# 5 discipline
content_slide(prs, 5, TOTAL, LABEL, "The Discipline Underneath the Numbers", "Verify, don't infer",
    bullets=[
        "Every check-in: query the database fresh, don't assume yesterday's escalation list "
        "is still accurate.",
        "When something looks like a new escalation, verify it's genuinely new and genuinely "
        "blocking — a job-level escalation isn't the same as the whole project stopping.",
        "When a finding depends on what a model claimed happened, read the thing itself — the "
        "generated source, the raw completion, the database row — not the log's own summary.",
    ], bullet_size=18)

# 6 section: five stories
section_slide(prs, "A Tour of What Actually Broke",
    "Five stories, out of fourteen named findings",
    note="Chosen because each shows something different about how a fleet of small models "
         "actually fails — and, in two cases, how well the recovery layers catch it.")

# 7 coin flip
content_slide(prs, 7, TOTAL, LABEL, "Story 1", "The same prompt is a coin flip",
    bullets=[
        "A small TOML parser doc, run three times, byte-identical, same model doing DECOMPOSE. "
        "Twice it invented an extra bead for a paired round-trip invariant the doc's numbered "
        "list didn't call for. Once it explicitly weighed the same call and went the other way.",
        "Consequence wasn't symmetric: one run absorbed it as an extra stall, one run's invented "
        "bead collided by test-file name with a sibling's and escalated the whole project.",
        "The obvious fix — stop DECOMPOSE from ever adding an unlisted bead — turned out to be "
        "wrong: a dedicated test asserts supplementary beads are meant to be allowed. The real "
        "gap is narrower: nothing stops an invented bead's test from colliding by name.",
    ], bullet_size=15, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 8 truncation
content_slide(prs, 8, TOTAL, LABEL, "Story 2", "A spec that quietly stops mid-sentence",
    bullets=[
        "REVISE_PENDING, which lightly revises the next bead's spec after one succeeds, "
        "occasionally handed back a spec that just stopped — mid-token, pin block sheared off, "
        "with a completely normal \"done\" status.",
        "Not random: on one specific bead, across two separate runs of the same doc, the exact "
        "same truncation happened both times — identical character count, identical fragment.",
        "Both times the downstream stall triggered a mechanical rebuild that recovered cleanly — "
        "never visible as an escalation. Found only because someone diffed every revised spec "
        "for a length regression instead of trusting \"done\" meant complete.",
    ], bullet_size=15, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 9 the bug caught
content_slide(prs, 9, TOTAL, LABEL, "Story 3", "The bug the pipeline was built to catch — catching it",
    bullets=[
        "A fractal-rendering bead stalled reconciling its own implementation against a failing "
        "test. ADJUDICATE traced it to a different, already-succeeded bead: an unrequested "
        "\"smart\" optimization silently merged line segments the spec required kept separate.",
        "The renderer's own test never caught it — its only case was a single segment. The doc's "
        "correct multi-segment example existed, but lived under a different bead's section.",
        "Correctly routed to a full stop and human review — the review layer catching a real "
        "downstream defect its own author's tests had missed.",
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 10 natural experiment
quote_slide(prs, 10, TOTAL, LABEL, "Story 3 — The Natural Experiment",
    "Run two of the same doc, by pure chance of DECOMPOSE's stochastic summarization, worded "
    "the renderer bead's own spec with the missing rule. The wrong optimization was simply "
    "never invented.",
    "The multi-segment test still wasn't written either time — the coverage gap that let the "
    "bug through was still there. What changed was only whether the correct rule showed up in "
    "the one place a downstream model would actually read it. Confirmed by reading the literal "
    "generated source in both runs, line by line.")

# 11 section: judge-oscillation
section_slide(prs, "Story 4 — The Centerpiece",
    "One checkable fact, five rounds, four verbs",
    note="On the literal last bead of the entire 23-run corpus.")

# 12 the fact
content_slide(prs, 12, TOTAL, LABEL, "The Question", "Does the done-indicator render as \"true\", or as a CSS class?",
    bullets=[
        "A task's completion status either renders as the literal word \"true\" in the real "
        "HTML — or as a CSS class / checkmark, the way the design doc's illustrative example "
        "happened to phrase it.",
        "Only one of these matches the actual generated code. This single fact gets "
        "independently re-derived five times across four different verbs in one refinement chain.",
    ], bullet_size=19)

# 13 the tally
table_slide(prs, 13, TOTAL, LABEL, "The Chain, Cycle by Cycle", "Who got it right",
    headers=["Cycle", "Verb", "Verdict"],
    rows=[
        ["2", "ADJUDICATE", "Correct — traced the real server output directly."],
        ["3", "JUDGE", "Correct — adopted the verified fact."],
        ["4", "JUDGE", "WRONG — reversed cycle 3 back to the doc's illustrative phrasing."],
        ["4", "WRITE", "Visibly thrashes — re-derives the contradiction live, resolves nothing."],
        ["5", "CRITIQUE", "Correct — independently re-derives the fact a third time."],
        ["5", "JUDGE", "WRONG — overrides CRITIQUE's correct finding, approves the bad test."],
        ["—", "EXECUTE", "Correct — silently rewrites the assertion itself and ships it."],
    ], col_widths=[2, 3, 9], font_size=14, top=Inches(2.15))

# 14 final tally
stat_slide(prs, 14, TOTAL, LABEL, "Final Score", "1-for-3",
    "JUDGE was wrong twice on an already-settled question. Everyone else: 3-for-3.",
    "Zero escalation. Zero defect in the shipped code. About half an hour of extra churn on "
    "the very last bead the corpus had left to run. The layered-backstop architecture caught "
    "in the act of doing exactly what it's for.", color=STOP)

# 15 cost picture
content_slide(prs, 15, TOTAL, LABEL, "The Cost Picture", "32.4% of all compute, 78% zero findings",
    bullets=[
        "Across ~1,500 completed calls and ~80 hours of summed compute, CRITIQUE (reviewing a "
        "freshly-written test) is the single most expensive step — more than the code-writing step.",
        "A close read of one bead's four REFINE cycles: of ~4 distinct gaps CRITIQUE surfaced, "
        "at least 3 were checkable by a plain literal-string search against the bead's own spec.",
        "The 4th wasn't a real defect: CRITIQUE flagged the implementation for not existing yet "
        "— on a step that runs, by design, before any implementation exists. Recurred 3 times, independently.",
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 16 last check
content_slide(prs, 16, TOTAL, LABEL, "The Last Check", "Doubting the findings themselves",
    bullets=[
        "After all 23 runs and 14 findings were written up, every one was re-examined from "
        "scratch against raw primary sources — git diffs, DB rows, generated source — not the write-up's own prose.",
        "Most held up exactly as written. A few came back sharper: one escalation's root cause "
        "was correctly diagnosed but the framework's guidance for that case simply didn't exist "
        "yet; one suspected bug was a deliberately tested feature.",
        "Across all fourteen, not one finding was overturned. The same discipline the framework "
        "is built on, turned back on the exercise meant to validate it.",
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 17 bottom line
content_slide(prs, 17, TOTAL, LABEL, "Where This Leaves Things", "The freeze is safe to lift",
    bullets=[
        "The recovery machinery — stalls, escalations, mechanical re-checks — consistently "
        "recovers cleanly or correctly routes to a human, across every one of nine genuinely distinct failure shapes.",
        ("What changed: the priority order. ", "CRITIQUE's cost-and-reliability profile — 32% "
         "of all compute, mostly-mechanical yield — is the strongest, best-evidenced argument "
         "this exercise produced. It's next."),
    ], bullet_size=19)

closing_slide(prs, 18, TOTAL, LABEL,
    "None of this makes the models smarter.",
    "It makes their mistakes cheap to catch — cheap enough that the system survives being "
    "wrong about something five times in a row, on the very last bead, without anyone having "
    "to notice at all. That's the thread running through all five pieces.",
    next_label="End of series. Part 1: Ratchet · Part 2: Verbs and the Fleet · "
               "Part 3: Format, Think, Turn · Part 4: Write for Zero Domain Knowledge.")

# NOTE: TOTAL=20 but we only used 18 explicit slide numbers (1 title + 16 numbered + 1 closing).
# Corrected below before saving by rebuilding TOTAL to match actual count.
out = "/Users/mike/Documents/GitHub/ratchet/writing/decks/05-the-burn-in.pptx"
import os
os.makedirs(os.path.dirname(out), exist_ok=True)
prs.save(out)
print("Saved", out, "slides:", len(prs.slides._sldIdLst))
