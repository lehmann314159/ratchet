import sys
sys.path.insert(0, "/private/tmp/claude-501/-Users-mike-Documents-GitHub-ratchet/f5519e05-1f50-4803-9a55-6fbc3753dac6/scratchpad")
from deck_common import *

DECK = 2
LABEL = "RATCHET · PART 2/4 · VERBS & THE FLEET"
prs = new_deck()
TOTAL = 29

title_slide(prs, DECK, "Verbs and the Fleet",
    "The architecture, in the detail that lets you argue with it",
    "Part 2 of 4. Quick recap: a design doc gets broken into beads — small, independently "
    "verifiable units of work — and each one is driven through a fixed sequence of narrow "
    "model calls called verbs. This piece is about how that actually works.")

# 2
content_slide(prs, 2, TOTAL, LABEL, "Foundations", "The database is the ground truth",
    bullets=[
        "Ratchet is not a long-running agent with state in memory — it's a SQLite database "
        "and a daemon that repeatedly asks it \"what's the next job to run?\"",
        "Every project, bead, model call, and decision lives in a row: projects, beads, "
        "bead_revisions, handoff_jobs, executions, adjudications, test_refinements.",
        ("This is what makes two things possible: ", "an operator recovers a stuck project "
         "by querying a table instead of reading scrollback, and a benchmarking harness can "
         "replay a real historical decision — because \"what happened\" is a row, not a memory "
         "that evaporated when the process exited."),
    ], bullet_size=18)

# 3 four state machines overview
content_slide(prs, 3, TOTAL, LABEL, "Control Flow", "Four state machines, nested",
    bullets=[
        ("1. Project status — ", "active, paused, full_stopped, complete, or fixture."),
        ("2. Bootstrap — ", "runs once, before any bead executes: survey, decompose, "
         "audit the plan."),
        ("3. Per-bead pipeline — ", "where most of the complexity lives: write, review, "
         "execute, adjudicate."),
        ("4. Generic job status — ", "underneath everything: pending → running → complete, "
         "with retry and escalation."),
    ], bullet_size=19)

# ============================================================ 4 — diagram: project status
slide = diagram_header(prs, 4, TOTAL, LABEL, "Diagram 1 of 4", "Project status",
    caption="Most of a project's life is spent active. paused and fixture are recoverable; complete and full_stopped are not.")
active = add_node(slide, Inches(2.5), Inches(3.85), Inches(2.3), Inches(0.75), "active")
paused = add_node(slide, Inches(2.5), Inches(1.95), Inches(2.3), Inches(0.7), "paused", fill=SURFACE2, border=INK_DIM)
complete = add_node(slide, Inches(9.8), Inches(2.55), Inches(2.5), Inches(0.75), "complete", fill=TERMINAL_OK, border=OK)
full_stopped = add_node(slide, Inches(9.8), Inches(5.05), Inches(2.5), Inches(0.75), "full_stopped", fill=TERMINAL_STOP, border=STOP)
fixture = add_node(slide, Inches(2.5), Inches(5.65), Inches(2.5), Inches(0.75), "fixture", fill=SURFACE2, border=INK_DIM)
add_edge_xy(slide, Inches(0.5), Inches(4.225), Inches(2.5), Inches(4.225))
add_edge(slide, active, 0, paused, 2, label="pause ⇄ resume", elbow=False)
add_edge(slide, active, 1, complete, 3, label="last bead succeeds", elbow=False)
add_edge(slide, active, 2, full_stopped, 0, label="recovery exhausted", elbow=True)
add_edge(slide, active, 3, fixture, 1, label="saved as fixture", elbow=False)

# 5 bootstrap
content_slide(prs, 5, TOTAL, LABEL, "Bootstrap", "Runs once, before bead 1",
    bullets=[
        "SURVEY_SPEC → VERIFY_MANIFEST → CERTIFY_MANIFEST → DECOMPOSE_SPEC → "
        "AUDIT_DECOMPOSITION → (RECONCILE_DECOMPOSITION if needed) → DECOMPOSITION_APPROVED.",
        ("VERIFY_MANIFEST is model-free. ", "A mechanical check, not a model call — not "
         "everything needs a model."),
        ("AUDIT_DECOMPOSITION is a second model reviewing the plan. ", "Before any code is "
         "written: does the decomposition cover the doc, is it correctly ordered? "
         "RECONCILE negotiates a fix, capped at two rounds before it escalates."),
        "Both retry loops are governed by a mechanical structural check, not a model's opinion — "
        "three chances each before full-stop or escalation.",
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# ============================================================ 6 — diagram: bootstrap
slide = diagram_header(prs, 6, TOTAL, LABEL, "Diagram 2 of 4", "Bootstrap",
    caption="Two independent, mechanically-governed retry loops — neither is a model's judgment call.")
survey = add_node(slide, Inches(0.35), Inches(3.6), Inches(1.75), Inches(0.8), "SURVEY_SPEC", font_size=10)
verify = add_node(slide, Inches(2.25), Inches(3.6), Inches(1.85), Inches(0.8), "VERIFY_MANIFEST", font_size=10)
certify = add_node(slide, Inches(4.25), Inches(3.6), Inches(1.85), Inches(0.8), "CERTIFY_MANIFEST", font_size=10)
decompose = add_node(slide, Inches(6.25), Inches(3.6), Inches(1.8), Inches(0.8), "DECOMPOSE_SPEC", font_size=10)
audit = add_node(slide, Inches(8.2), Inches(3.6), Inches(2.05), Inches(0.8), "AUDIT_DECOMPOSITION", font_size=9.5)
approved = add_node(slide, Inches(10.4), Inches(3.6), Inches(2.35), Inches(0.8), "DECOMPOSITION_APPROVED", fill=TERMINAL_OK, border=OK, font_size=9)
reconcile = add_node(slide, Inches(8.2), Inches(5.55), Inches(2.05), Inches(0.8), "RECONCILE_DECOMPOSITION", font_size=8.5)
add_edge(slide, survey, 1, verify, 3, elbow=False)
add_edge(slide, verify, 1, certify, 3, elbow=False)
add_edge(slide, certify, 1, decompose, 3, label="approved", elbow=False)
add_edge(slide, certify, 0, survey, 0, label="rejected (cap 5)", elbow=True)
add_edge(slide, decompose, 1, audit, 3, elbow=False)
add_edge(slide, audit, 1, approved, 3, label="no issues", elbow=False)
add_edge(slide, audit, 2, reconcile, 0, label="issues found", elbow=False)
add_edge(slide, reconcile, 3, audit, 3, label="disagree, retry (cap 2)", elbow=True, label_nudge=(0, 0.35))
add_edge(slide, reconcile, 1, approved, 2, label="converged", elbow=True)

# 7 per-bead pipeline
content_slide(prs, 7, TOTAL, LABEL, "The Per-Bead Pipeline", "Write → review → execute → decide",
    bullets=[
        "WRITE (compiles) → CRITIQUE → JUDGE → [approved] → EXECUTE → ANALYZE → COMPRESS → ADJUDICATE.",
        ("ADJUDICATE's menu: ", "execute as-is, execute revised, reject the test, re-refine "
         "(back to review), declare success, or full-stop."),
        ("test_reject only exists in \"test-first\" mode. ", "In the common REFINE_TESTS mode, "
         "a genuinely bad test can never be silently patched around — it goes back through "
         "re_refine into the review chain."),
        ("re_refine bypasses the attempt cap on purpose. ", "\"The test was wrong\" is "
         "accounted differently than \"the model couldn't implement a correct test.\""),
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# ============================================================ 8 — diagram: per-bead pipeline
slide = diagram_header(prs, 8, TOTAL, LABEL, "Diagram 3 of 4", "The per-bead pipeline",
    caption="Where most of the complexity lives. Two loops feed back in — one above the spine, one below.")
write = add_node(slide, Inches(0.4), Inches(3.6), Inches(1.55), Inches(0.8), "WRITE", font_size=10)
critique = add_node(slide, Inches(2.1), Inches(3.6), Inches(1.55), Inches(0.8), "CRITIQUE", font_size=10)
judge = add_node(slide, Inches(3.8), Inches(3.6), Inches(1.4), Inches(0.8), "JUDGE", font_size=10)
execute = add_node(slide, Inches(5.35), Inches(3.6), Inches(1.55), Inches(0.8), "EXECUTE", font_size=10)
analyze = add_node(slide, Inches(7.05), Inches(3.6), Inches(1.55), Inches(0.8), "ANALYZE", font_size=9.5)
compress = add_node(slide, Inches(8.75), Inches(3.6), Inches(1.65), Inches(0.8), "COMPRESS", font_size=9.5)
adjudicate = add_node(slide, Inches(10.55), Inches(3.6), Inches(1.9), Inches(0.8), "ADJUDICATE", font_size=9.5)
done = add_node(slide, Inches(10.0), Inches(5.7), Inches(1.35), Inches(0.55), "done", fill=TERMINAL_OK, border=OK, font_size=9)
stopped = add_node(slide, Inches(11.55), Inches(5.7), Inches(1.7), Inches(0.55), "full_stop", fill=TERMINAL_STOP, border=STOP, font_size=9)
add_edge(slide, write, 1, critique, 3, label="compiles", elbow=False)
add_edge(slide, critique, 1, judge, 3, elbow=False)
add_edge(slide, judge, 0, write, 0, label="revise (≤5)", elbow=True)
add_edge(slide, judge, 1, execute, 3, label="approved", elbow=False)
add_edge(slide, execute, 1, analyze, 3, elbow=False)
add_edge(slide, analyze, 1, compress, 3, elbow=False)
add_edge(slide, compress, 1, adjudicate, 3, elbow=False)
add_edge(slide, adjudicate, 0, execute, 0, label="retry, as-is or revised", elbow=True)
add_edge(slide, adjudicate, 2, judge, 2, label="re_refine — test was wrong", elbow=True)
add_edge(slide, adjudicate, 2, done, 0, label="declare_success", elbow=True)
add_edge(slide, adjudicate, 1, stopped, 0, label="full_stop", elbow=True)

# 9 job status
content_slide(prs, 9, TOTAL, LABEL, "Job Status", "Underneath everything",
    bullets=[
        "Every verb call: pending → running → complete, with failed_retry (2 strikes, flat "
        "across every verb) and escalated as the only other ways out.",
        ("EXECUTE_BEAD is the exception. ", "It runs as its own supervised subprocess with "
         "its own retry accounting, not the generic path."),
    ], bullet_size=19)

# ============================================================ 10 — diagram: job status
slide = diagram_header(prs, 10, TOTAL, LABEL, "Diagram 4 of 4", "Generic job status",
    caption="Underneath every verb above, except EXECUTE_BEAD, which runs its own supervised loop instead.")
pending = add_node(slide, Inches(1.5), Inches(3.6), Inches(2.0), Inches(0.75), "pending")
running = add_node(slide, Inches(5.2), Inches(3.6), Inches(2.0), Inches(0.75), "running")
comp = add_node(slide, Inches(8.9), Inches(3.6), Inches(2.2), Inches(0.75), "complete", fill=TERMINAL_OK, border=OK)
retry = add_node(slide, Inches(5.2), Inches(1.8), Inches(2.0), Inches(0.7), "failed_retry", fill=SURFACE2, border=WARN, font_size=10)
esc = add_node(slide, Inches(8.9), Inches(5.6), Inches(2.2), Inches(0.75), "escalated", fill=TERMINAL_STOP, border=STOP)
add_edge(slide, pending, 1, running, 3, label="claimed", elbow=False)
add_edge(slide, running, 1, comp, 3, label="validated", elbow=False)
add_edge(slide, running, 0, retry, 2, label="fails (≤2) ⇄ reclaimed", elbow=True)
add_edge(slide, running, 2, esc, 0, label="strikes exceeded", elbow=True)

# 11 mechanical gates
content_slide(prs, 11, TOTAL, LABEL, "The Mechanical Gates", "A model's account is never sufficient on its own",
    bullets=[
        ("declare_success gets checked, not trusted. ", "The exit-criteria command re-runs "
         "against the files on disk before a \"success\" is accepted."),
        ("execute_revised gets a source-side gate. ", "A violation silently downgrades to "
         "execute_as_is rather than committing a broken revision."),
        ("A stuck execution gets classified, not retried harder. ", "Fixed timing (12m "
         "checkpoint, 45m ceiling) judges real progress vs. no progress — timeout means "
         "\"too big,\" never \"give it more time.\" (An earlier version tried more time. Retired.)"),
        ("A parallel watchdog can kill a running execution. ", "MONITOR_EXECUTION polls a "
         "trace file and can SIGTERM/SIGKILL — a second opinion with no job row of its own."),
    ], bullet_size=15, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 12 escalation stat
stat_slide(prs, 12, TOTAL, LABEL, "Recovery", "12 ways",
    "to escalate or full-stop — every one routed to a human",
    "From a decomposition disagreement that never converges, to five rejected manifests, "
    "to two consecutive stalls or timeouts on the same bead. Recovery is requeue or rewind — "
    "never a hand-edited database or a test patched to force it green.")

# 13 verification one level deeper
content_slide(prs, 13, TOTAL, LABEL, "One Level Deeper", "Verification the model runs itself",
    bullets=[
        "The gates above check a model's output after the fact. Some verbs also get a way "
        "to check their own reasoning while they're still forming it.",
        ("run_go_snippet ", "— CRITIQUE, JUDGE, ADJUDICATE, and WRITE can all run a real "
         "snippet of Go and read back the actual output, mid-turn."),
        "\"This input produces this error\" or \"these two values are equal\" gets tested "
        "against a live interpreter, instead of simulated correctly inside the model's head "
        "and hoped to be right.",
        "Less mentally tracing code. More propose-a-check, read-back-what-happened.",
    ], bullet_size=17, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 14 blast radius
content_slide(prs, 14, TOTAL, LABEL, "Keeping the Blast Radius Small", "Narrow in, narrow out",
    bullets=[
        ("In: ", "WRITE and CRITIQUE don't see the whole design doc — just a bead-scoped "
         "excerpt, capped at a fixed size. COMPRESS_ANALYSIS exists purely to shrink an "
         "execution's history before ADJUDICATE ever sees it."),
        ("Out: ", "an EXECUTE_BEAD attempt runs inside its own per-attempt temp directory. "
         "When it ends, only the files the bead's spec actually declares get copied back — "
         "everything else the model touched is discarded with the temp dir."),
        "A frontier model can afford to have everything in view and sort out what matters "
        "itself. This fleet can't be trusted to do that sorting — so the framework does it, "
        "upstream, every time.",
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 15 generic vs language-specific
content_slide(prs, 15, TOTAL, LABEL, "Generic Prompts, Language-Specific Guidance", "Not all of this is Go plumbing",
    bullets=[
        "Every verb's system prompt splits into two pieces that never mix: a generic prompt "
        "(role framing, structural rules, output schema) with nothing language-specific in "
        "it, and a separate, swappable file carrying the actual language mechanics.",
        "A language column on every project already exists to select which file loads. The "
        "injection machinery works today — no second language has been written yet.",
        "The generic half is meant to survive untouched if that ever changes. The "
        "language-specific half is exactly, and only, the part that would need rewriting.",
    ], bullet_size=17, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 16 casting the fleet
content_slide(prs, 16, TOTAL, LABEL, "Casting the Fleet", "Independence, enforced in code",
    bullets=[
        "Five mechanical constraints on the model-assignment table: DECOMPOSE and RECONCILE "
        "stay the same model (continuity); AUDIT ≠ DECOMPOSE; EXECUTE ≠ ANALYZE; CERTIFY ≠ SURVEY; "
        "and — enforced most directly — WRITE ≠ CRITIQUE.",
        "WRITE and EXECUTE deliberately share a model — accepted because CRITIQUE and JUDGE "
        "remain independent of both, so the review layer between them is still a genuine second opinion.",
    ], bullet_size=18)

# 17 fleet table
table_slide(prs, 17, TOTAL, LABEL, "Casting the Fleet", "The standing cast",
    headers=["Verb", "Model"],
    rows=[
        ["REFINE_TESTS_WRITE", "muse-glimmer:30b-q8_0-dflash"],
        ["REFINE_TESTS_CRITIQUE", "qwen3:32b"],
        ["REFINE_TESTS_JUDGE", "qwen3.6:35b-a3b"],
        ["EXECUTE_BEAD", "muse-glimmer:30b-q8_0-dflash  (same as WRITE)"],
        ["ADJUDICATE_NEXT_EXECUTION", "qwen3.6:35b-a3b  (same as JUDGE)"],
    ], col_widths=[5, 7], font_size=17, top=Inches(2.3))

# 18 the gap
quote_slide(prs, 18, TOTAL, LABEL, "Casting the Fleet — The Gap",
    "JUDGE and ADJUDICATE are the same model, and that pairing isn't covered by any of the "
    "five mechanical constraints.",
    "If you're ever tempted to lean on ADJUDICATE as an independent check on a JUDGE call "
    "that seems wrong — right now it isn't independent at all. Same weights, second look at "
    "a related question. Surfaced by the burn-in, not designed in. Currently #2 on the fix list.")

# 19 how casting decisions get made
content_slide(prs, 19, TOTAL, LABEL, "The Bakeoff Harness", "qualify-model: evidence, not vibes",
    bullets=[
        "Takes a captured historical dispatch — a full DB + folder snapshot at the exact "
        "moment a real verb call happened — patches in a candidate model, and replays the "
        "verb's real code path (stopping short of Commit, so nothing leaks back to a live project).",
        ("Fidelity assertion: ", "does the replay reconstruct a byte-identical prompt to what "
         "was actually captured? If the verb's prompt-building has drifted, the comparison is "
         "flagged unsound rather than silently scored."),
        ("Per-verb rubrics: ", "WRITE graded against hand-planted mutants; CRITIQUE on catch-rate "
         "and false positives; JUDGE/ADJUDICATE on agreement with the confirmed decision, plus "
         "dead_turn_rate — turns that return no content and no tool call at all."),
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 20 bakeoff table
table_slide(prs, 20, TOTAL, LABEL, "The Bakeoff Results", "Not what you'd predict from reputation",
    headers=["Verb", "Result"],
    rows=[
        ["WRITE", "Winner 6/6 correct, 0 dead turns. Incumbent 3/6 (2 beads: 30-min ceiling, nothing written)."],
        ["CRITIQUE", "Incumbent held up: ~2/3 real defects, 0 false positives. Fast model: 0/6, 30% dead-turn rate."],
        ["JUDGE", "Same JSON-grammar constraint helps one model, breaks another, blocks a third outright."],
        ["ADJUDICATE", "Fast model matched incumbent on every case, 4–5× faster — but corpus only had easy cases."],
    ], col_widths=[3, 9], font_size=14, top=Inches(2.25))

# 21 throughline
quote_slide(prs, 21, TOTAL, LABEL, "The Throughline",
    "A fast small model does very well when handed clean evidence and asked to make a call — "
    "and badly when asked to go looking for problems with no evidence at all.",
    "This is why CRITIQUE (open-ended detection) and ADJUDICATE (evidence-based judgment) are "
    "cast so differently even though both are, on paper, \"review a thing and decide.\" It's "
    "also the direct ancestor of the framework's current top priority.")

# 22 stat: 0/31
stat_slide(prs, 22, TOTAL, LABEL, "One More Bakeoff", "0 / 31",
    "runs cleared the bead — and more turns made the best coder worse, not better",
    "A dedicated coder model plus a much bigger turn budget was the obvious fix for one "
    "stalling bead. Neither helped. This bakeoff never ended in a model swap.", color=STOP)

# 23 coder-swap lesson
content_slide(prs, 23, TOTAL, LABEL, "More Room to Work, More Room to Thrash", "The result, in full",
    bullets=[
        "The best-performing coder candidate's standard-budget runs reached checkpoint 32 "
        "and 26 out of 35. Given more than double the turns, the same model's results fell "
        "to 19, 19, and 0.",
        "One generous-budget run spent well over a hundred turns iterating and ended up "
        "breaking a type that had been working fine at turn one.",
        ("The eventual fix wasn't a model swap. ", "It was splitting the oversized bead in "
         "the design doc — the origin story for the bead-sizing discipline in Part 3."),
        "Giving a struggling model more room to work assumes the problem is room. Sometimes "
        "the problem is scope.",
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 24 NEW — the lesson found twice
quote_slide(prs, 24, TOTAL, LABEL, "Found Twice, Independently",
    "The mechanical gate on slide 11 — the one that used to double a stuck execution's time "
    "budget on every repeated timeout — doesn't exist in that form anymore.",
    "Both mechanisms were built on the same assumption: a model running out of time just "
    "needs more of it. Both times, in two unrelated investigations, the data said the "
    "opposite. A framework this willing to retire its own mechanisms on the strength of a "
    "bakeoff result is arguably a better signal than either bakeoff alone.")

# 25 the bug
content_slide(prs, 25, TOTAL, LABEL, "A Structural Bug, Not a Behavioral One", "0 of 139",
    bullets=[
        "A core reviewing model had, across the framework's entire history, never once "
        "successfully emitted a tool call while a strict JSON-format grammar was active.",
        "It looked like a prompting problem for a long time — verbose reasoning, repeated "
        "retries, escalations, blamed on the model \"not being disciplined enough.\"",
        "It wasn't. The model thinks in a separate stream before answering; the JSON grammar "
        "constraint was structurally incompatible with the tag syntax that stream needs to "
        "hand off into a tool call. No prompting fix was ever going to touch it.",
        "The fix: drop the format constraint on that verb's tool-invoking turn. Small change — "
        "found only by treating a year of \"reasoning spirals\" as a hypothesis to test against raw logs.",
    ], bullet_size=15, top_bullets=Inches(2.35), bullets_height=Inches(4.5))

# 25 nemotron capability flag
content_slide(prs, 26, TOTAL, LABEL, "A Model's Word for It Isn't Evidence", "nemotron-cascade-2:30b-a3b",
    bullets=[
        "Reported native tool-calling support through Ollama's own introspection endpoint. "
        "A quick test probe came back clean.",
        "On a real task, with a realistically-sized payload, it failed anyway — Ollama's own "
        "parser for that model choked with a hard XML error, before any of the framework's "
        "own code ever ran.",
        ("The lesson: ", "a model's advertised capability is not evidence it can do the job. "
         "The only real test is a live exchange with a realistic payload — the same \"verify, "
         "don't take its word for it\" instinct as the mechanical gates, aimed one level up."),
    ], bullet_size=17, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 26 cascade
content_slide(prs, 27, TOTAL, LABEL, "An Advanced Workflow", "Cascade iterations",
    bullets=[
        "A completed project can be cloned with a revised design doc — the clone inherits "
        "the original's beads and execution history, skips survey/decompose, and goes straight "
        "to a fresh audit against the new doc.",
        "Changed bead specs are diffed and reset; unchanged beads — including ones already "
        "succeeded — are left completely untouched.",
        "A small mechanism, but the concrete answer to a bigger question: how to avoid redoing "
        "work a model already got right, once \"the spec changed\" is a normal event.",
    ], bullet_size=18)

# 27 what this buys
content_slide(prs, 28, TOTAL, LABEL, "What This Buys, Put Plainly", "Three non-negotiable properties",
    bullets=[
        "Decisions get checked against reality instead of taken on trust.",
        "Work is broken into pieces small enough to verify independently, rather than trusted "
        "as one long session.",
        "The pipeline's own behavior is something you can replay, measure, and improve with "
        "evidence — not folklore about which model \"feels\" reliable.",
    ], bullet_size=19)

closing_slide(prs, 29, TOTAL, LABEL,
    "None of it makes the models smarter.",
    "It makes their mistakes cheap to catch, and their good decisions easy to tell apart "
    "from lucky ones.",
    next_label="Write for Zero Domain Knowledge — the craft of writing a spec precise enough "
               "for a 30B model to build correctly on the first pass.")

out = "/Users/mike/Documents/GitHub/ratchet/writing/decks/02-verbs-and-the-fleet.pptx"
import os
os.makedirs(os.path.dirname(out), exist_ok=True)
prs.save(out)
print("Saved", out, "slides:", len(prs.slides._sldIdLst))
