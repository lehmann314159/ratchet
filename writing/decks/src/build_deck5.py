import sys
sys.path.insert(0, "/private/tmp/claude-501/-Users-mike-Documents-GitHub-ratchet/f5519e05-1f50-4803-9a55-6fbc3753dac6/scratchpad")
from deck_common import *

DECK = 5
LABEL = "RATCHET · PART 5/6 · DRAFT, CHECK, SIGN OFF"
prs = new_deck()
TOTAL = 16

title_slide(prs, DECK, "Draft, Check, Sign Off",
    "Where a spec precise enough for this actually comes from",
    "Part 5 of 6. The last piece was about what a good design doc looks like once it's "
    "finished. This one is about the workflow that gets it there — and the independent "
    "check that decides whether it's allowed to start a project.",
    title_size=44, series_total=6)

# 2 three incidents
content_slide(prs, 2, TOTAL, LABEL, "The Gap This Closes", "One conversation was the entire quality bar",
    bullets=[
        ("A tasklist bead ", "churned four-plus REFINE_TESTS cycles over shallow- vs. "
         "deep-copy semantics — in the same authoring session whose own commit message "
         "described applying worked-example discipline elsewhere."),
        ("A connect-four bead ", "lost a full bead's worth of cycles because a worked "
         "scenario was added to the doc without its matching Decomposition Notes pin — "
         "correct content, missing plumbing."),
        ("A web-app doc ", "had pins that were real and correct, but in a format later "
         "tooling didn't expect — because nothing had checked the convention against a "
         "doc written before that convention existed."),
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 3 two skills
quote_slide(prs, 3, TOTAL, LABEL, "Two Skills, Not One",
    "Independence is worth something only if it's real — and a check that only ever "
    "sees its own draft never means anything.",
    "draft-design-doc and check-design-doc stay separate on purpose. Drafting is authoring "
    "work Claude already does well, inline, with full context. Checking only means "
    "something when it's done by something that never saw the draft get written — no "
    "benefit of the doubt for a phrase that reads clearly to the person who chose it.")

# 4 the template
content_slide(prs, 4, TOTAL, LABEL, "Drafting, Section by Section", "A fixed seven-section template",
    bullets=[
        "Overview · Architecture · Data Types and Function Signatures · Behavioral "
        "Specification · Domain-Specific Test Scenarios · Cross-Bead Contracts · "
        "Decomposition Notes.",
        "Three are conditional and get omitted entirely, never left as an empty heading — "
        "Architecture for a small project, Test Scenarios when nothing has geometry a raw "
        "index could misrepresent, Cross-Bead Contracts when no bead feeds another.",
        "Not a style choice: checkdesigndoc parses by heading, and SURVEY/DECOMPOSE read "
        "specific sections by name downstream. A doc that drifts from the template silently "
        "stops being machine-readable exactly where it matters.",
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 5 three hard rules
content_slide(prs, 5, TOTAL, LABEL, "Three Hard Rules", "Requirements, not judgment calls",
    bullets=[
        ("Every worked example is verified by actually running something, ", "in-session, "
         "before it enters the doc — with an inline citation of what ran and what it "
         "produced. A number that \"looks right\" from memory doesn't clear this bar."),
        ("A gap becomes an Open Questions entry, ", "quoting the underspecified spot and "
         "naming the concrete alternatives — never a plausible guess written in unmarked. "
         "The section persists whether or not anyone's there to ask."),
        ("Every load-bearing literal gets an explicit pin, ", "a bullet naming the exact "
         "bead and value, generated as part of drafting rather than trusted to survive "
         "later paraphrasing."),
    ], bullet_size=15, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 6 checkdesigndoc naive version
content_slide(prs, 6, TOTAL, LABEL, "The Mechanical Scan", "The naive version, and why it didn't work",
    bullets=[
        "checkdesigndoc parses the doc by section, applies keyword-triggered rules, prints "
        "a report — an over-flagging report a human reads, never a pass/fail gate.",
        "The first arithmetic check flagged any \"identifier operator identifier\" pattern "
        "— and lit up on every single Go pointer type, because g *Game reads exactly like "
        "arithmetic to a regex that doesn't know Go syntax.",
        "Rebuilt around actual arithmetic language instead — \"sum of,\" \"the formula,\" "
        "\"FNV,\" \"hash of\" — cleared by a computed number nearby. Geometry went the other "
        "way: \"above\"/\"below\" dropped entirely once they turned out to almost always be "
        "ordinary cross-references, not geometric claims.",
    ], bullet_size=15, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 7 two more checks
content_slide(prs, 7, TOTAL, LABEL, "Two Sharper Checks", "construction-form and bead-size",
    bullets=[
        ("construction-form ", "flags a Cross-Bead Contract that enumerates a polymorphic "
         "type's variants without ever saying pointer or value — the exact defect from the "
         "expression-VM story, where two independent beads picked opposite constructions of "
         "the same undeclared type."),
        ("bead-size ", "flags a Decomposition Notes entry that owns too many functions and "
         "is wired too tightly into other beads' work — calibrated against the project's "
         "own history until it caught every bead known to have actually spiraled."),
    ], bullet_size=17)

# 8 the class judgment can't catch
quote_slide(prs, 8, TOTAL, LABEL, "The One Class Judgment Can't Catch",
    "\"Standard movement rules\" is invisible to a reviewer who shares the author's "
    "fluency — for the identical reason the author didn't notice writing it.",
    "This class gets a mechanical pre-filter treated as authoritative, on purpose, not "
    "just as a backstop: not \"judgment usually misses it\" but structurally can't catch "
    "it, because the fluency that makes a reviewer fast is the same fluency that makes "
    "them blind to this one shape of gap.", quote_color=ACCENT)

# 9 check-design-doc orchestration
content_slide(prs, 9, TOTAL, LABEL, "check-design-doc", "Mirrors DECOMPOSE → AUDIT → RECONCILE, one layer earlier",
    bullets=[
        "Runs the mechanical scan first, keeps every path:line hit.",
        "Separately dispatches a fresh subagent with nothing but the doc and the current "
        "checklist — not the mechanical report, not who wrote the doc or why. Feeding the "
        "mechanical hits in first would anchor its judgment and defeat the point of a "
        "second, independent pass.",
        "The subagent is explicitly told that finding nothing is an acceptable result — "
        "manufacturing findings to look thorough is its own kind of failure.",
    ], bullet_size=16)

# 10 reconciliation table
table_slide(prs, 10, TOTAL, LABEL, "Reconciling Two Independent Opinions", "The asymmetric rule",
    headers=["Site flagged by…", "Becomes", "Rule"],
    rows=[
        ["Both passes", "One item, source \"both\"", "Straightforward agreement."],
        ["Mechanical only", "Carried forward, unresolved", "Never dropped just because "
         "the reviewer didn't raise it."],
        ["Class 17 (any pass)", "Always carried forward", "A same-fluency reviewer not "
         "raising it is expected, not reassurance."],
        ["Judgment only", "One item, source \"judgment\"", "The independent read caught "
         "something mechanical can't reach."],
    ], col_widths=[3, 4, 7], font_size=14, top=Inches(2.15))

# 11 sign-off
content_slide(prs, 11, TOTAL, LABEL, "Sign-Off", "Every row starts as \"needs decision\"",
    bullets=[
        "Resolving a row means one of three things happens explicitly: apply the "
        "suggested rewrite, apply a different answer, or affirmatively waive it — with "
        "the reason written into the doc as an inline comment, so the next run doesn't "
        "silently re-litigate it.",
        "A class-17 item can't be waived with \"a reviewer would understand this\" — that "
        "sentence is the exact failure mode the class exists to catch.",
        "Clearance is two separate confirmations: \"cleared for new-project\" prints only "
        "once every row is resolved, and copying the doc into a project folder is a second, "
        "separate action. Nothing about a clean report auto-starts anything.",
    ], bullet_size=15, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 12 section: what it found for real
section_slide(prs, "What It Found, the First Time It Ran for Real",
    "Four docs, already scheduled to run, during burn-in prep",
    note="The workflow's own design notes are candid that it hadn't been run on a doc "
         "nobody had drafted with the tool already in mind. This was that test.")

# 13 tier-3 numbers
stat_slide(prs, 13, TOTAL, LABEL, "The Tier-3 Run", "20 / 4",
    "genuine ambiguities found · shared by the hand-verified reference implementations",
    "About fifteen were doc-only imprecision the reference implementation happened to "
    "sidestep. Four were sharper: the reference implementation had faithfully carried "
    "the same gap forward rather than exposing it.")

# 14 the bug that beat the reference impl
content_slide(prs, 14, TOTAL, LABEL, "The Bug the Doc and the Reference Shared", "One implementation, internally consistent, still wrong",
    bullets=[
        "A decimal library's coefficient range was stated as including math.MinInt64 — "
        "but the library's own Neg and Abs can't actually negate that value. The doc's "
        "range and its own required operations were quietly incompatible.",
        "A retry engine's CLI silently defaulted a malformed delay argument to zero "
        "instead of reporting it as bad input.",
        "The pointer-versus-value story again, in a different shape: an ambiguous spec "
        "doesn't just risk two implementations disagreeing — it can produce one "
        "implementation that's consistent with itself and still wrong, with nothing in a "
        "single build to expose the seam.",
    ], bullet_size=15, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# 15 a new class born from convergence
quote_slide(prs, 15, TOTAL, LABEL, "A New Class, Born From Convergence",
    "Three of four independent subagents, on three unrelated docs, raised the same kind "
    "of gap without being told to look for it.",
    "A function or CLI command with more than one validation guard that can fail "
    "simultaneously, each mapping to a different error, with the doc never stating which "
    "one wins. Now the checklist's eighteenth class — about as strong a signal as this "
    "catalogue ever produces that a class is real, not one reviewer's idiosyncratic taste.")

closing_slide(prs, 16, TOTAL, LABEL,
    "The reviewing model is drawn from the same fleet as everything else in this series —",
    "same blind spots, same fallibility. What changes is the arrangement: drafting and "
    "checking never share a memory of the same conversation, a mechanical scan runs "
    "regardless of whether judgment seems thorough enough to skip it, and the one class "
    "judgment structurally can't catch is carried forward whether or not anyone raises it.",
    next_label="The Burn-In — the deliberate stress-test of everything in these five "
               "pieces, what it found, and what surprised even the people running it.")

out = "/Users/mike/Documents/GitHub/ratchet/writing/decks/05-draft-check-sign-off.pptx"
import os
os.makedirs(os.path.dirname(out), exist_ok=True)
prs.save(out)
print("Saved", out, "slides:", len(prs.slides._sldIdLst))
