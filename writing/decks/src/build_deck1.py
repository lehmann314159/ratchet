import sys
sys.path.insert(0, "/private/tmp/claude-501/-Users-mike-Documents-GitHub-ratchet/f5519e05-1f50-4803-9a55-6fbc3753dac6/scratchpad")
from deck_common import *

DECK = 1
LABEL = "RATCHET · PART 1/6"
prs = new_deck()

title_slide(prs, DECK, "Ratchet",
    "Teaching a fleet of small local models to build working software",
    "Part 1 of 6 — the light, orienting piece. Five deep dives follow: the architecture "
    "and model fleet, the wire-level mechanics of talking to it over Ollama, the craft of "
    "writing specs precise enough for small models, the workflow that turns prose into one "
    "of those specs, and a full account of the recent burn-in.",
    series_total=6)

TOTAL = 18

def C(page, *a, **kw):
    return content_slide(prs, page, TOTAL, LABEL, *a, **kw)

# 2
content_slide(prs, 2, TOTAL, LABEL, "The Primer", "What changes when the model isn't Claude",
    bullets=[
        "Claude Code: loosely-specified task in, sensible judgment calls on every ambiguity, "
        "usually actually done when it says it's done.",
        "Ratchet runs on a fleet of small, open-weight models (24–35B params) hosted locally "
        "via Ollama — cheap, private, and a real test of whether process can substitute for capability.",
        "None of Claude's convenient assumptions hold. Everything in this deck is a consequence "
        "of that one gap.",
    ], bullet_size=19)

# 3
content_slide(prs, 3, TOTAL, LABEL, "The Primer", "Four ways a small model fails that Claude mostly doesn't",
    bullets=[
        ("Ambiguity gets guessed at. ", "\"Trim trailing whitespace\" gets a mix of inconsistent "
         "interpretations across a hundred functions, silently."),
        ("Structured output fights reasoning. ", "One core model never once emitted a tool call "
         "under a JSON-format constraint — 0 of 139 recorded calls. A structural conflict, not a prompting bug."),
        ("They get stuck. ", "Looping on the same failed compile error, or burning the whole "
         "response budget \"thinking\" without ever answering."),
        ("They lie about success — unknowingly. ", "A model that writes a file has no way to "
         "confirm it landed on disk with the right bytes. It has to be checked."),
    ], bullet_size=17, top_bullets=Inches(2.05), bullets_height=Inches(4.8))

# 4
stat_slide(prs, 4, TOTAL, LABEL, "The Primer", "Assembly line,",
    "not one long agentic conversation.",
    "Many small, single-purpose model calls, each narrow enough that a small model can "
    "reliably do it, wrapped in mechanical checks that never trust a model's own account "
    "of what happened.")

# 5
content_slide(prs, 5, TOTAL, LABEL, "The System", "What Ratchet actually is",
    bullets=[
        ("Design doc → beads. ", "A plain-English spec gets broken into beads — small, "
         "independently verifiable units of work, each with its own test and exit criteria."),
        ("A fixed pipeline, per bead. ", "Write a test, review it, implement against it, "
         "check the result, decide what happens next — bead by bead until the project is done."),
        ("A database, not a black box. ", "Every job, model call, and decision lives in a "
         "SQLite row. Recovery is a query, not scrollback archaeology."),
        ("Humans only when it escalates. ", "A small web UI surfaces anything that needs "
         "review; an operator requeues or rewinds."),
    ], bullet_size=18)

# 6
content_slide(prs, 6, TOTAL, LABEL, "The Model Fleet", "A cast, not one model for everything",
    bullets=[
        ("muse-glimmer ", "writes both the tests and the implementation code."),
        ("qwen3:32b ", "critiques the tests — deliberately a different model than the writer."),
        ("qwen3.6:35b-a3b ", "(fast MoE) judges the critique and adjudicates what happens after "
         "an execution attempt."),
        ("qualify-model ", "— a homegrown harness that replays real historical decisions against "
         "a candidate model and grades the result, so casting is evidence, not intuition."),
    ], bullet_size=18)

# 7
stat_slide(prs, 7, TOTAL, LABEL, "The Model Fleet", "Judgment: yes.",
    "Open-ended detection: no.",
    "The fast MoE model agreed with the incumbent's verdict on every single adjudication "
    "case, 4–5× faster — then collapsed to a near-0% catch rate the moment it was asked to "
    "go looking for bugs with no evidence handed to it. That single distinction shaped a "
    "lot of what came later.", color=ACCENT)

# 8
content_slide(prs, 8, TOTAL, LABEL, "The Verbs", "The pipeline, roughly in order",
    bullets=[
        ("SURVEY / DECOMPOSE — ", "read the doc and the codebase, break the spec into beads."),
        ("AUDIT / RECONCILE — ", "a second model reviews the plan before any code is written."),
        ("WRITE → CRITIQUE → JUDGE — ", "per bead: write a test, review it, approve or send it back."),
        ("EXECUTE → ANALYZE / COMPRESS — ", "implement against the approved test, summarize what happened."),
        ("ADJUDICATE — ", "retry as-is, revise the spec, reject the test, declare success, or escalate."),
    ], bullet_size=17)

# 9 — mechanical-over-inference + narrow context + generic/language-specific
content_slide(prs, 9, TOTAL, LABEL, "How the Verbs Are Built", "Three habits that hold the whole chain together",
    bullets=[
        ("Mechanical checks beat inference, wherever one is available. ", "Did the file "
         "compile, does the test assert this exact string, did the exit-criteria command "
         "actually pass — the framework checks it directly rather than asking a model to "
         "judge it. Inference is spent only where nothing mechanical can substitute."),
        ("Each call sees a narrow slice, not the whole project. ", "WRITE and CRITIQUE get "
         "only the design-doc excerpt relevant to their one bead; a dedicated step exists "
         "purely to compress an execution's history before ADJUDICATE sees it."),
        ("Verb prompts split generic from language-specific. ", "Role framing and structural "
         "rules carry nothing Go-specific; a separate, swappable file carries the actual "
         "language mechanics. Only Go is wired up today — the split exists so that isn't permanent."),
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 10
content_slide(prs, 10, TOTAL, LABEL, "Typical Workflows", "Query the database. Don't tail logs.",
    bullets=[
        "Progress is a query against projects.status — the daemon runs unattended.",
        ("Escalation → requeue or rewind. ", "Never patch the database, never hand-edit a test "
         "to force it green, never nudge state to resume mid-stream."),
        ("\"Touch a project once.\" ", "Patching a live run to rescue it teaches less about the "
         "defect and leaves a state nobody else can reason about."),
        ("Cascade cloning ", "diffs a revised design doc against a completed project bead-by-bead, "
         "re-running only what changed."),
    ], bullet_size=17)

# 11
content_slide(prs, 11, TOTAL, LABEL, "Crafting a Design Doc", "The hardest skill in the whole system",
    bullets=[
        "Claude fills ambiguity gaps with good judgment. A 30B model fills them with "
        "a judgment — inconsistently, run to run.",
        "Worked examples with the exact computed number, not \"compute this correctly.\"",
        "Construction forms pinned verbatim — pointer or value, spelled out, never implied.",
        ("draft-design-doc + checkdesigndoc ", "— tooling that flags ambiguity mechanically "
         "before a doc ever reaches the pipeline."),
    ], bullet_size=18)

# 12 History
content_slide(prs, 12, TOTAL, LABEL, "History, Briefly", "Games → fixes → precision → burn-in",
    bullets=[
        ("Started with the games. ", "Connect Four, tic-tac-toe, goban — built to find bugs, "
         "not to be interesting. An early audit turned up ~20 issues."),
        ("A run of framework fixes. ", "Tool-loop reasoning spirals, the JSON-grammar/reasoning "
         "conflict, a proper model-qualification harness, progress-vs-thrashing detection."),
        ("A decomposition + precision chain. ", "Bead-size limits, a doc-quality lint, the "
         "pinning discipline — aimed at the single largest recurring defect class: ambiguity "
         "compression quietly drops."),
        "September 2026: all of it converged into a deliberate validation exercise.",
    ], bullet_size=17)

# 13 Burn-in headline
stat_slide(prs, 13, TOTAL, LABEL, "The Burn-In", "9 docs · 23 runs",
    "14 complete · 9 escalated (39%) · 0 showstoppers · 0 repeated root causes",
    "Every escalation traced to a genuinely different mechanism. After the runs finished, "
    "every one of the 14 named findings was re-examined against raw evidence — nothing was "
    "found to be simply wrong.")

# 14 Surprising results
content_slide(prs, 14, TOTAL, LABEL, "Surprising Results", "Four findings worth sitting with",
    bullets=[
        ("The priciest step barely does anything. ", "Test review = 32.4% of all compute, "
         "zero findings on 78% of calls — and most of what it does catch is mechanically checkable."),
        ("Right diagnosis, wrong playbook. ", "A model correctly diagnosed a stalled bead — then "
         "reached for the wrong recovery strategy because the prompt had no guidance for that exact case."),
        ("A \"bug\" turned out to be a tested feature. ", "The proposed fix would have quietly "
         "reversed a deliberate, already-tested design decision."),
        ("A doc contradicted itself in adjacent sentences ", "— caught live, fixed with a "
         "two-line prose correction."),
    ], bullet_size=16, top_bullets=Inches(2.05), bullets_height=Inches(4.8))

# 15 frontier
content_slide(prs, 15, TOTAL, LABEL, "Current Frontier", "What the burn-in says to fix next",
    bullets=[
        ("#1 — Test-review's cost & reliability. ", "The clearest, best-evidenced priority the "
         "whole exercise produced."),
        ("#2 — A quiet independence gap. ", "The model that judges a critique and the model "
         "that adjudicates afterward are currently the same model."),
        ("A handful of smaller, cheap fixes. ", "A planning step whose stated reasoning doesn't "
         "always match its structured output; missing guidance for one failure case; a few "
         "single-instance findings with proposed fixes already written."),
    ], bullet_size=18)

# 16 roadmap
content_slide(prs, 16, TOTAL, LABEL, "Where It's Headed", "Extend and debug existing code, not just build from nothing",
    bullets=[
        "Everything Ratchet does today assumes a fresh design doc describing something built "
        "from scratch. Next: extend a finished project, diagnose a defect with no spec, reuse "
        "one project's code as a dependency for another.",
        "Rather than chase every capability a general coding agent has, the roadmap is explicit "
        "about which three things actually matter for this approach.",
        ("Three things must survive any new capability: ", "unconditional mechanical gates, "
         "decomposition into independently-verifiable units, and a database that makes the "
         "pipeline itself reproducibly testable."),
    ], bullet_size=18)

# 17 app tour
content_slide(prs, 17, TOTAL, LABEL, "A Tour of What It's Built", "Real, working, end-to-end — no hand-written code",
    bullets=[
        "A fractal visualizer (Mandelbrot, Julia, Sierpinski) with a web front end.",
        "An L-system generator — recursive rewriting systems behind procedural plants and curves.",
        "A cron-expression studio and a glob-pattern studio.",
        "Connect Four, tic-tac-toe, and a Go board — the original bug-finding dogfoods.",
        "A Kafka simulator with a live partition/routing/offset visualization.",
        "A toy expression-language VM with a web front end — the most-iterated project in the framework's history.",
    ], bullet_size=16, top_bullets=Inches(2.05), bullets_height=Inches(4.8))

closing_slide(prs, 18, TOTAL, LABEL,
    "None of this makes the models smarter.",
    "It makes their mistakes cheap to catch and their good decisions easy to tell apart "
    "from lucky ones. That's the whole trade this series is about.",
    next_label="Verbs and the Fleet — the architecture and model fleet, in the detail that lets you argue with it.")

out = "/Users/mike/Documents/GitHub/ratchet/writing/decks/01-ratchet.pptx"
import os
os.makedirs(os.path.dirname(out), exist_ok=True)
prs.save(out)
print("Saved", out, "slides:", len(prs.slides.__iter__.__self__._sldIdLst))
