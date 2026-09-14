"""Build docs/state-machine.pptx — an engineering-reference deck version of
docs/ratchet_state_machine.md, for walking someone through the FSM on a
screen-share instead of scrolling a markdown file or PDF.

Distinct from writing/decks/ (the coworker/Medium article series): this one
tracks the technical reference doc's precision (exact caps, file/function
names, the full escalation table), not the simplified narrative version.
Reuses the shared visual library from the article-deck series for continuity,
but writes its own title/closing slides since this isn't "Part N of 4."

Run: python3 scripts/build-state-machine-pptx.py
Deps: the venv at scratchpad (python-pptx) — see writing/decks/src for setup,
or `pip install python-pptx` in any environment.
"""
import sys, os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "writing", "decks", "src"))
from deck_common import *
from deck_common import _set_run

LABEL = "RATCHET · STATE MACHINE REFERENCE"
prs = new_deck()
TOTAL = 16

# ---------------------------------------------------------------- 1: title
slide = add_slide(prs, bg=INK)
add_kicker(slide, "Ratchet · Engineering Reference", color=RGBColor(0x9A, 0xC7, 0xD3), top=Inches(2.35))
box, tf = add_textbox(slide, Inches(0.9), Inches(2.85), Inches(11.5), Inches(1.15))
p = tf.paragraphs[0]
r = p.add_run()
_set_run(r, "The Ratchet State Machine", size=48, color=WHITE, font=TITLE_FONT, bold=True)
p.line_spacing = 1.0
box2, tf2 = add_textbox(slide, Inches(0.95), Inches(4.15), Inches(10.8), Inches(0.7))
p2 = tf2.paragraphs[0]
r2 = p2.add_run()
_set_run(r2, "Four nested diagrams, from a project's whole lifecycle down to one job row",
          size=22, color=RGBColor(0xC9, 0xCD, 0xD2), font=BODY_FONT, italic=True)
box3, tf3 = add_textbox(slide, Inches(0.95), Inches(5.15), Inches(9.8), Inches(1.6))
p3 = tf3.paragraphs[0]
p3.line_spacing = 1.3
r3 = p3.add_run()
_set_run(r3, "Tracks docs/ratchet_state_machine.md at full precision — exact caps, function "
             "names, the complete escalation table. Source of truth is that file; this is a "
             "presentable view of the same facts, regenerated the same way.",
          size=15, color=RGBColor(0x9A, 0x9F, 0xA6), font=BODY_FONT)
add_footer_dark(slide, 1, TOTAL, LABEL)

# ---------------------------------------------------------------- 2: the four layers
content_slide(prs, 2, TOTAL, LABEL, "Overview", "Four diagrams, outermost to innermost",
    bullets=[
        ("1. Project status — ", "the five values of projects.status."),
        ("2. Bootstrap — ", "runs once per project, before any bead executes. Two entry "
         "points: a fresh project starts at SURVEY_SPEC; a cascade iteration starts at "
         "AUDIT_DECOMPOSITION against inherited beads."),
        ("3. Per-bead pipeline — ", "the loop every bead goes through. Most of the "
         "complexity lives here."),
        ("4. Generic job status — ", "the low-level handoff_jobs FSM every verb call goes "
         "through underneath diagrams 2 and 3."),
    ], bullet_size=17)

# ---------------------------------------------------------------- 3: diagram — project status
slide = diagram_header(prs, 3, TOTAL, LABEL, "Diagram 1 of 4", "Project status",
    caption="projects.status CHECK IN ('active','full_stopped','complete','paused','fixture'). fixture and full_stopped/complete are terminal by design.")
active = add_node(slide, Inches(2.5), Inches(3.85), Inches(2.3), Inches(0.75), "active")
paused = add_node(slide, Inches(2.5), Inches(1.95), Inches(2.3), Inches(0.7), "paused", fill=SURFACE2, border=INK_DIM)
complete = add_node(slide, Inches(9.55), Inches(2.4), Inches(2.75), Inches(0.75), "complete", fill=TERMINAL_OK, border=OK)
full_stopped = add_node(slide, Inches(9.55), Inches(5.15), Inches(2.75), Inches(0.8), "full_stopped", fill=TERMINAL_STOP, border=STOP, font_size=10)
fixture = add_node(slide, Inches(2.5), Inches(5.65), Inches(2.5), Inches(0.75), "fixture", fill=SURFACE2, border=INK_DIM)
add_edge_xy(slide, Inches(0.4), Inches(4.225), Inches(2.5), Inches(4.225), label="new-project / clone-project")
add_edge(slide, active, 0, paused, 2, label="pause knob ⇄ resume-project CLI", elbow=False)
add_edge(slide, active, 1, complete, 3, label="last bead succeeds", elbow=False)
add_edge(slide, active, 2, full_stopped, 0, label="recovery exhausted", elbow=True)
add_edge(slide, active, 3, fixture, 1, label="save-fixture CLI", elbow=False)

# ---------------------------------------------------------------- 4: project status notes
content_slide(prs, 4, TOTAL, LABEL, "Project Status — Notes", "What decides which bootstrap path a project takes",
    bullets=[
        ("\"last bead succeeds,\" precisely: ", "declare_success on the last remaining "
         "pending bead, OR a zero-diff cascade iteration (CASCADE_REVIEW finds nothing "
         "changed)."),
        ("\"recovery exhausted,\" precisely: ", "a full_stop decision on any bead, 5 total "
         "CERTIFY_MANIFEST rejections, DECOMPOSE_SPEC bead-ordering violations past cap, or "
         "the full-stop-project CLI."),
        ("Provenance columns, not status. ", "lineage_root_id + iteration_number (every "
         "project belongs to a lineage); cascade_baseline_project_id — set only by "
         "clone-project --design-doc, and its presence is what routes bootstrap onto the "
         "cascade path."),
        ("fixture is terminal by design. ", "In-place renumbered to a negative id, never "
         "dispatched again. clone-project (from any status, including fixture) spawns a "
         "brand-new project row — a deep copy, not a transition of this row."),
        ("paused can also reach fixture directly ", "(save-fixture CLI) — the paused "
         "project's inert pending job moves with it. Omitted above for diagram clarity."),
    ], bullet_size=16, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# ---------------------------------------------------------------- 5: diagram — bootstrap
slide = diagram_header(prs, 5, TOTAL, LABEL, "Diagram 2 of 4", "Bootstrap",
    caption="Runs once, before bead 1. All transitions automatic except the branch points marked with a verb's decision field.")
survey = add_node(slide, Inches(0.35), Inches(3.6), Inches(1.75), Inches(0.8), "SURVEY_SPEC", font_size=10)
verify = add_node(slide, Inches(2.25), Inches(3.6), Inches(1.85), Inches(0.8), "VERIFY_MANIFEST", font_size=10)
certify = add_node(slide, Inches(4.25), Inches(3.6), Inches(1.85), Inches(0.8), "CERTIFY_MANIFEST", font_size=10)
decompose = add_node(slide, Inches(6.25), Inches(3.6), Inches(1.8), Inches(0.8), "DECOMPOSE_SPEC", font_size=10)
audit = add_node(slide, Inches(8.2), Inches(3.6), Inches(2.05), Inches(0.8), "AUDIT_DECOMPOSITION", font_size=9.5)
approved = add_node(slide, Inches(10.4), Inches(3.6), Inches(2.35), Inches(0.8), "DECOMPOSITION_APPROVED", fill=TERMINAL_OK, border=OK, font_size=9)
reconcile = add_node(slide, Inches(8.2), Inches(5.55), Inches(2.05), Inches(0.8), "RECONCILE_DECOMPOSITION", font_size=8.5)
add_edge_xy(slide, Inches(0.35), Inches(2.3), Inches(1.225), Inches(3.6), label="new-project (fresh)", label_size=9)
add_edge_xy(slide, Inches(9.4), Inches(1.7), Inches(9.22), Inches(3.6), label="clone-project (cascade)", label_size=9)
add_edge(slide, survey, 1, verify, 3, elbow=False)
add_edge(slide, verify, 1, certify, 3, elbow=False)
add_edge(slide, certify, 1, decompose, 3, label="approve", elbow=False)
add_edge(slide, certify, 0, survey, 0, label="reject, count < 5", elbow=True)
add_edge(slide, decompose, 1, audit, 3, elbow=False)
add_edge(slide, audit, 1, approved, 3, label="no issues", elbow=False)
add_edge(slide, audit, 2, reconcile, 0, label="issues found", elbow=False)
add_edge(slide, reconcile, 3, audit, 3, label="disagree, round < cap (2)", elbow=True, label_nudge=(0, 0.35))
add_edge(slide, reconcile, 1, approved, 2, label="converged", elbow=True)

# ---------------------------------------------------------------- 6: bootstrap notes
content_slide(prs, 6, TOTAL, LABEL, "Bootstrap — the Two Retry Loops", "Neither is a model judgment call",
    bullets=[
        ("CERTIFY_MANIFEST reject loop. ", "Any reject re-enqueues SURVEY_SPEC with "
         "CERTIFY's feedback prepended. At reject count 5, project.status = full_stopped."),
        ("DECOMPOSE_SPEC ↔ AUDIT/RECONCILE loop (not drawn above, kept as escalation edges "
         "only). ", "A mechanical structural check runs on the proposed decomposition: "
         "forward file-reference, bead-ordering, or a bead-list merge/drop against the "
         "design doc's own numbered list. DECOMPOSE_SPEC failing it re-enqueues with the "
         "violations in its prompt; at decomposeRedecomposeCap (3), full_stopped."),
        ("RECONCILE_DECOMPOSITION's own reject loop. ", "Failing the same structural check "
         "on its own proposed fix re-enqueues RECONCILE; at reconcileRejectCap (3), the job "
         "escalates."),
        "DECOMPOSITION_APPROVED is not a verb — it's the shared checkpoint both the "
        "AUDIT-no_issues and RECONCILE-converged paths funnel through, before dispatching "
        "bead 1, pausing, or (cascade) entering CASCADE_REVIEW.",
    ], bullet_size=14, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# ---------------------------------------------------------------- 7: diagram — per-bead pipeline
slide = diagram_header(prs, 7, TOTAL, LABEL, "Diagram 3 of 4", "The per-bead pipeline",
    caption="beads.status: pending -> executing -> succeeded/full_stopped. Everything below is the verb chain inside 'executing.'")
write = add_node(slide, Inches(0.35), Inches(3.6), Inches(1.5), Inches(0.8), "WRITE", font_size=10)
critique = add_node(slide, Inches(2.0), Inches(3.6), Inches(1.5), Inches(0.8), "CRITIQUE", font_size=10)
judge = add_node(slide, Inches(3.65), Inches(3.6), Inches(1.35), Inches(0.8), "JUDGE", font_size=10)
execute = add_node(slide, Inches(5.15), Inches(3.6), Inches(1.5), Inches(0.8), "EXECUTE", font_size=10)
analyze = add_node(slide, Inches(6.8), Inches(3.6), Inches(1.5), Inches(0.8), "ANALYZE", font_size=9.5)
compress = add_node(slide, Inches(8.45), Inches(3.6), Inches(1.6), Inches(0.8), "COMPRESS", font_size=9.5)
adjudicate = add_node(slide, Inches(10.2), Inches(3.6), Inches(1.85), Inches(0.8), "ADJUDICATE", font_size=9.5)
done = add_node(slide, Inches(9.7), Inches(5.7), Inches(1.3), Inches(0.55), "done", fill=TERMINAL_OK, border=OK, font_size=9)
stopped = add_node(slide, Inches(11.2), Inches(5.7), Inches(1.9), Inches(0.55), "full_stop", fill=TERMINAL_STOP, border=STOP, font_size=9)
add_edge(slide, write, 1, critique, 3, label="compiles", elbow=False)
add_edge(slide, critique, 1, judge, 3, elbow=False)
add_edge(slide, judge, 0, write, 0, label="revise, cycle ≤ 5", elbow=True)
add_edge(slide, judge, 1, execute, 3, label="approved", elbow=False)
add_edge(slide, execute, 1, analyze, 3, elbow=False)
add_edge(slide, analyze, 1, compress, 3, elbow=False)
add_edge(slide, compress, 1, adjudicate, 3, elbow=False)
add_edge(slide, adjudicate, 0, execute, 0, label="retry: as-is/revised/test_reject", elbow=True, label_size=9)
add_edge(slide, adjudicate, 2, judge, 2, label="re_refine — bypasses cap", elbow=True, label_size=9)
add_edge(slide, adjudicate, 2, done, 0, label="declare_success, criteria pass", elbow=True, label_size=9)
add_edge(slide, adjudicate, 1, stopped, 0, label="full_stop", elbow=True)

# ---------------------------------------------------------------- 8: adjudicate's menu
content_slide(prs, 8, TOTAL, LABEL, "ADJUDICATE's Decision Menu", "Six outcomes, each mechanically gated",
    bullets=[
        ("execute_as_is / execute_revised ", "— retry, under max_execution_attempts. A "
         "revised spec passes a source-side gate first (see next slide) or silently "
         "downgrades to execute_as_is."),
        ("test_reject ", "— test-first mode only (a bead that starts at EXECUTE_BEAD "
         "because it already has test files from elsewhere): deletes the test files, "
         "revises the spec, retries under the attempt cap."),
        ("re_refine ", "— REFINE_TESTS mode only: routes back to REFINE_TESTS_JUDGE, grants "
         "a fresh attempt budget, and deliberately bypasses the attempt cap — \"the test was "
         "wrong\" is accounted differently than \"the model couldn't implement it.\""),
        ("declare_success ", "— gated: VerifyExitCriteriaIsolated re-runs the criteria "
         "command against a copy of the files. Failure retries EXECUTE under the cap."),
        ("full_stop ", "— terminal; the project's remaining pending beads cascade to "
         "full_stopped too."),
    ], bullet_size=14, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# ---------------------------------------------------------------- 9: mechanical gates
content_slide(prs, 9, TOTAL, LABEL, "Mechanical Gates on ADJUDICATE", "The model's decision is not taken on trust",
    bullets=[
        ("declare_success ", "runs VerifyExitCriteriaIsolated against a COPY of the files, "
         "not the live folder — an entrypoint bead's own \"go build\" criterion was "
         "littering the real tree with binaries as a side effect of merely checking it."),
        ("execute_revised ", "gets a source-side gate: an orphan -run name, a grep guard "
         "for a file the bead doesn't own, a symbol a sibling bead's on-disk scaffold "
         "already declares (cross_bead_symbols.go), or a newly-invented required test "
         "function (that one is re_refine's job). A violation downgrades to execute_as_is."),
        ("Every execute_revised commit ", "re-runs InjectDesignDocPins against the new "
         "full_text — a full rewrite is exactly the kind of edit that can silently drop a "
         "verbatim Decomposition-Notes pin block."),
        ("applyMechanicalBeadFixes ", "normalizes the revised spec (e.g. go test with no "
         "test file) the same way DECOMPOSE/RECONCILE do at decomposition time."),
    ], bullet_size=14, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# ---------------------------------------------------------------- 10: stall/timeout detection pt1
content_slide(prs, 10, TOTAL, LABEL, "Mechanical Stall/Timeout Detection (1/2)", "Timing is fixed, not budget-derived",
    bullets=[
        ("execCheckpointInterval = 12m ", "soft checkpoint. execAbsoluteCeiling = 45m hard "
         "backstop. execution_budget is no longer read for timing at all — the column stays "
         "in the schema, inert; execute_revised still floors a stored value at the project "
         "default."),
        ("progressTracker ", "judges a turn \"productive\" only if a write_file call leaves "
         "an in-scope output file at a content hash it hasn't held before this attempt — a "
         "no-op rewrite or an A→B→A revert doesn't count."),
        ("Fast paths to stalled, well before the hard ceiling: ", "an empty-turn streak, "
         "three consecutive identical tool calls, two consecutive length-cap \"thinking with "
         "no output\" turns, or no forward progress since the last soft checkpoint (which "
         "first injects one graceful-finalize directive and grants one more turn)."),
        "If the hard ceiling fires with genuine progress still happening and no stall "
        "condition met, the cause is timeout instead.",
    ], bullet_size=14, top_bullets=Inches(2.1), bullets_height=Inches(4.7))

# ---------------------------------------------------------------- 11: stall/timeout detection pt2
content_slide(prs, 11, TOTAL, LABEL, "Mechanical Stall/Timeout Detection (2/2)", "timeout and stalled get different treatment",
    bullets=[
        ("timeout is read as a SCOPE signal, ", "not a speed problem. timeoutExecutionNote "
         "steers execute_revised toward narrowing the spec, never toward \"try again with "
         "more room.\""),
        ("Retired: the earlier budget-doubling behavior ", "(900s → 1800s → 3600s → 7200s "
         "on repeated timeouts) — removed once checkpoint/ceiling stopped being "
         "budget-derived. A bigger window bought more thrashing, not more correctness."),
        ("Two consecutive same-cause terminations escalate automatically: ", "escalateOnRepeatedTimeout "
         "and escalateOnRepeatedStall mirror each other. re_refine is never chosen for "
         "either (the tests were never reached)."),
        ("A repeated-stall escalation is tagged \"EXECUTE-ceiling\" ", "in its report when "
         "ADJUDICATE had already tried two execute_revised rewrites first — the bead likely "
         "needs a doc-side split, not another revision. Tag only; no control-flow change."),
        ("monitor_terminated / monitor_force_killed sit outside all of this — ", "MONITOR_EXECUTION "
         "killing the process externally isn't a timing decision EXECUTE_BEAD made about itself.",),
    ], bullet_size=13, top_bullets=Inches(2.0), bullets_height=Inches(4.8))

# ---------------------------------------------------------------- 12: diagram — job status
slide = diagram_header(prs, 12, TOTAL, LABEL, "Diagram 4 of 4", "Generic job status",
    caption="handoff_jobs.status, underneath every verb above except EXECUTE_BEAD (its own supervised loop, drawn in Diagram 3).")
pending = add_node(slide, Inches(1.4), Inches(3.6), Inches(2.0), Inches(0.75), "pending")
running = add_node(slide, Inches(5.1), Inches(3.6), Inches(2.0), Inches(0.75), "running")
comp = add_node(slide, Inches(8.8), Inches(3.6), Inches(2.2), Inches(0.75), "complete", fill=TERMINAL_OK, border=OK)
retry = add_node(slide, Inches(5.1), Inches(1.75), Inches(2.0), Inches(0.7), "failed_retry", fill=SURFACE2, border=WARN, font_size=10)
esc = add_node(slide, Inches(8.8), Inches(5.6), Inches(2.2), Inches(0.75), "escalated", fill=TERMINAL_STOP, border=STOP)
add_edge(slide, pending, 1, running, 3, label="claimNextJob (atomic)", elbow=False, label_size=9)
add_edge(slide, running, 1, comp, 3, label="Validate succeeds", elbow=False, label_size=9)
add_edge(slide, running, 0, retry, 2, label="Validate fails, strikes ≤ 2", elbow=True, label_size=9)
add_edge(slide, retry, 2, running, 0, label="reclaimed", elbow=True, label_nudge=(1.6, 0), label_size=9)
add_edge(slide, running, 2, esc, 0, label="strikes exceeded", elbow=True, label_size=9)

# ---------------------------------------------------------------- 13: escalation table
table_slide(prs, 13, TOTAL, LABEL, "Escalation Points", "Every way a job reaches escalated, or a project full_stopped",
    headers=["#", "Where", "Trigger", "Result"],
    rows=[
        ["1", "RECONCILE_DECOMPOSITION", "audit/reconcile round cap hit, unresolved", "job escalated"],
        ["2", "CERTIFY_MANIFEST", "5 total rejections", "project full_stopped"],
        ["3", "REFINE_TESTS_WRITE", "test won't compile after retries, or a required test fn is never written", "job escalated"],
        ["4", "REFINE_TESTS_JUDGE", "revise requested past refinementCycleCap (5)", "job escalated"],
        ["5", "EXECUTE_BEAD", "infraFailureCap (3) consecutive startup crashes", "job escalated"],
        ["6", "ADJUDICATE_NEXT_EXECUTION", "execute_as_is/revised/test_reject/declare_success gate fail, at max_execution_attempts", "job escalated"],
        ["7", "ADJUDICATE_NEXT_EXECUTION", "re_refine past refinementCycleCap", "job escalated"],
        ["8", "ADJUDICATE_NEXT_EXECUTION", "2 consecutive stalled terminations", "job escalated (tagged EXECUTE-ceiling if ≥2 prior execute_revised)"],
        ["9", "ADJUDICATE_NEXT_EXECUTION", "2 consecutive timeout terminations", "job escalated"],
        ["10", "any verb (generic)", "strikes exceed flat tolerance of 2", "job escalated"],
        ["11", "DECOMPOSE_SPEC", "ordering/reference/merge-drop violation past redecomposeCap (3)", "project full_stopped"],
        ["12", "RECONCILE_DECOMPOSITION", "own fix reintroduces a violation past reconcileRejectCap (3)", "job escalated"],
    ], col_widths=[0.5, 2.6, 5.2, 2.7], font_size=10.5, top=Inches(1.95))

# ---------------------------------------------------------------- 14: cascade iterations
content_slide(prs, 14, TOTAL, LABEL, "Cascade Iterations (Loop-Mode)", "clone-project --design-doc <new.md>",
    bullets=[
        "Creates a new project in the same lineage, inheriting the baseline's beads, "
        "bead_revisions, and full execution/adjudication history — then overwrites the "
        "design doc and sets cascade_baseline_project_id.",
        "One fresh AUDIT_DECOMPOSITION is enqueued. SURVEY_SPEC and DECOMPOSE_SPEC never "
        "run — the inherited beads ARE the decomposition. RECONCILE may not rename, add, or "
        "remove beads, so titles stay stable across the clone boundary.",
        ("Every changed bead — ", "spec differs from baseline, by title, deliberately "
         "liberal comparison — gets reset by resetBeadForRerun, even if already succeeded: "
         "active jobs cancelled, test files deleted, impl reset to stubs, pre-reset files "
         "snapshotted first."),
        "Unchanged beads are left exactly as cloned — status, files, history untouched.",
        "≥1 changed bead: lowest-id changed bead's execution is enqueued. Zero changed: "
        "project marked complete directly, no pause, nothing to resume into.",
    ], bullet_size=13.5, top_bullets=Inches(2.15), bullets_height=Inches(4.65))

# ---------------------------------------------------------------- 15: recovery tools
two_col_slide(prs, 15, TOTAL, LABEL, "Recovery Tools", "Four ways a human intervenes",
    left_head="PER-BEAD",
    left_items=[
        ("rewind-bead — ", "the sanctioned path for any per-bead escalation. Resets to "
         "REFINE_TESTS_WRITE cycle 1, stubs impl files, fresh attempt budget. Always "
         "discards whatever implementation exists on disk. Refuses a succeeded bead."),
        ("Requeue (UI) — ", "try the exact same job again. Useful for transient failures, "
         "not a spec problem."),
    ],
    right_head="PROJECT-LEVEL",
    right_items=[
        ("resume-project — ", "pure status flip; only ever re-dispatches bead 1, or for a "
         "cascade project, re-enters at DECOMPOSITION_APPROVED."),
        ("full-stop-project — ", "the manual equivalent of a project-wide escalation."),
        ("Never: ", "hand-editing the database, or patching a test to force it green — "
         "convention, not code, but held."),
    ], title_size=28)

# ---------------------------------------------------------------- 16: closing
slide = add_slide(prs, bg=INK)
add_kicker(slide, "SOURCE OF TRUTH", color=RGBColor(0x9A, 0xC7, 0xD3), top=Inches(1.6))
box, tf = add_textbox(slide, Inches(0.9), Inches(2.05), Inches(11.3), Inches(1.6))
p = tf.paragraphs[0]
p.line_spacing = 1.1
r = p.add_run()
_set_run(r, "docs/ratchet_state_machine.md", size=36, color=WHITE, font=MONO_FONT, bold=True)
box2, tf2 = add_textbox(slide, Inches(0.95), Inches(3.55), Inches(10.6), Inches(2.3))
p2 = tf2.paragraphs[0]
p2.line_spacing = 1.35
r2 = p2.add_run()
_set_run(r2, "This deck is a presentable view of that file, not a replacement for it — regenerated "
             "the same way its four PNGs are: scripts/build-state-machine-pdf.sh renders the "
             "Mermaid source via mermaid-cli; this deck is built straight from the same facts "
             "via scripts/build-state-machine-pptx.py. When the .md changes, re-run both.",
          size=16, color=RGBColor(0xC9, 0xCD, 0xD2), font=BODY_FONT)
add_footer_dark(slide, 16, TOTAL, LABEL)

out = "/Users/mike/Documents/GitHub/ratchet/docs/state-machine.pptx"
prs.save(out)
print("Saved", out, "slides:", len(prs.slides._sldIdLst))
