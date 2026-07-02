# Inter-Bead Context — Design Specification

## Motivation

When a bead executes, it writes files. Subsequent beads that touch the same file must
preserve what was written. The current pipeline handles this statically: RECONCILE adds
"preserve existing code" instructions when AUDIT flags a shared-file conflict. This works
when the conflict is predictable from the spec alone.

Two failure modes are not covered:

**Ahead-of-scope implementation.** Executors sometimes write more than their spec asks for.
In othello-v3 project 45, the `game-init` bead wrote stub bodies for `Pass`, `Score`, and
`CheckWinner` — functions scoped to the later `game-state-logic` bead — because the
executor needed the package to compile. The `game-state-logic` spec says "implement Pass,
Score, CheckWinner" without knowing that stubs already exist. The executor may redeclare
them, producing a compile error, or may silently overwrite correct stubs with different
ones.

**Shared-file divergence from spec.** Even when RECONCILE adds preservation instructions,
those instructions are necessarily abstract ("preserve existing code in handlers.go"). An
executor that has never seen the current content of `handlers.go` must infer what is worth
preserving. Giving it the actual file removes the ambiguity.

Both failure modes share a root cause: bead specs are written before any implementation
exists, so they cannot reference the state of files that previous beads have modified. This
document describes two solutions at different points on the complexity/adaptability curve.

---

## Solution A — File-State Injection (lighter)

### Concept

Before a bead executes, inject the current on-disk content of each file in its
`output_files` list into the executor prompt. The executor sees exactly what is already
there and treats it as the ground truth to build on.

This is a mechanical operation — no model call, no spec mutation. The executor prompt gains
a new section, "Current file state," containing the verbatim content of each output file.
If a file does not exist yet (first bead to touch it), the section for that file reads
"(not yet created)."

### Where it happens

In `execution/executor.go` (or wherever the executor prompt is assembled), immediately
before the bead spec is rendered:

1. For each path in `bead.OutputFiles`:
   - Attempt to read the file from the project folder.
   - If present: include full content.
   - If absent: include "(not yet created)".
2. Append as a new prompt section after the bead spec prose and before the guidance
   injection.

### Prompt section format

```
## Current file state

The following files are in your output_files list. Their current content is shown below.
Build on what exists — do not remove or redeclare anything that is already correct.

### game.go

```go
package main

import "fmt"

type Color int
// ... (full file content verbatim)
```

### game_test.go

(not yet created)
```

### What this fixes

- **Stub overwrite**: the `game-state-logic` executor sees the existing stub bodies for
  `Pass`, `Score`, `CheckWinner` in `game.go` and fills them in rather than redeclaring.
- **Shared-file clobber**: the `ui-handler-index` executor sees the template functions
  already written by `ui-templates` and knows not to delete them.
- **Concrete preservation target**: "preserve existing code" becomes "preserve these
  specific functions at these specific line numbers" — no inference required.

### What this does not fix

- **Spec accuracy**: the bead spec still says "implement FindFlips" even if FindFlips
  was already fully implemented by a prior bead. The executor must read the file, notice
  the implementation is complete, and skip it. Most executors will do this correctly, but
  it is not enforced.
- **Cascading ahead-of-scope work**: if bead N wrote functions scoped to beads N+1 and
  N+2, the specs for N+1 and N+2 still describe work that is already done. File-state
  injection gives the executor enough context to notice, but does not update the spec
  to reflect reality.

### Implementation notes

- File reads happen at job dispatch time, not at DECOMPOSE time.
- No DB schema changes required.
- Token cost: full content of all output files per bead. For a typical Go project with
  files in the 100–500 line range, this adds ~500–2000 tokens per bead. Acceptable.
- If an output file is large (>500 lines), consider injecting only the function signature
  list (extracted via AST) rather than full content. Defer this optimization until a
  project produces a file that large.
- The injected content must be clearly labeled as "existing state, do not treat as spec"
  to prevent the executor from interpreting it as the target output.

---

## Solution B — Inter-Bead Spec Revision (heavier)

### Concept

After each bead reaches a terminal state (succeeded), run a lightweight model call that
reads the current state of the project folder and revises the specs of all remaining
pending beads. Revised specs replace the originals in the DB before the next bead
dispatches.

This is a new verb — `REVISE_PENDING` — that fires once per successful bead completion.
It receives: the just-completed bead's spec and output files, the current content of all
project files, and all pending bead specs. It produces an updated spec for each pending
bead, or a no-change signal if no revision is needed.

### When it runs

In `AdjudicateNextExecution.Commit`, after the bead status is set to `succeeded` and
before `advanceToNextBead` dispatches the next job:

```
bead N: succeeded
  → REVISE_PENDING fires
    → reads project folder
    → revises specs for beads N+1 ... M
    → writes revised specs as new bead_revisions
  → bead N+1 dispatches with updated spec
```

### Scope: all pending beads

After any bead succeeds, REVISE_PENDING revises ALL remaining pending beads — not just
the immediately next one.

The "revise N+1 only" approach fails because file dependencies are not always adjacent.
In othello-v3 project 45: `game-init` wrote stubs for `Pass`, `Score`, `CheckWinner`
scoped to `game-state-logic` (two positions later, with `game-move-logic` in between).
The handler beads shared `handlers.go` across four non-adjacent beads. The integration
bead references functions from files it doesn't own, written by beads anywhere in the
sequence.

Revising all pending beads is also simpler: no dependency tracking, no output_files
intersection graph. The revision model sees full current file state and either updates
a pending spec or returns `{"action": "no_change"}`. The no_change path is cheap. For a
9-bead project, the worst case is 36 total revision calls (8+7+...+1), but most will be
no_change for beads with no relevant overlap.

The output_files intersection approach (revise only beads whose output_files overlap with
the completed bead's files) is an optimization worth considering only if empirical data
shows the no_change call overhead is meaningful.

### What REVISE_PENDING does

The model receives:

- The completed bead's title and full output file content
- All pending bead specs (title + full_text + output_files + exit_criteria)
- A prompt asking it to:
  1. Identify any work described in pending specs that is already present on disk
  2. Update the spec to acknowledge what exists ("FindFlips is already implemented —
     fill in the body; do not redeclare")
  3. Add explicit file-content preservation instructions for shared files, grounded in
     what is actually present
  4. Leave the spec unchanged if no revision is needed (signal: `{"action": "no_change"}`)

REVISE_PENDING does NOT reorder beads, change output_files lists, or restructure
exit criteria. It only updates the prose of `full_text`.

### DB schema

```sql
CREATE TABLE IF NOT EXISTS spec_revisions (
  id              INTEGER PRIMARY KEY,
  project_id      INTEGER NOT NULL REFERENCES projects(id),
  trigger_bead_id INTEGER NOT NULL REFERENCES beads(id),
  revised_bead_id INTEGER NOT NULL REFERENCES beads(id),
  old_revision_id INTEGER NOT NULL REFERENCES bead_revisions(id),
  new_revision_id INTEGER,  -- NULL if action was no_change
  raw_output      TEXT,
  created_at      TIMESTAMP NOT NULL
);
```

`bead.current_revision_id` is updated to `new_revision_id` when REVISE_PENDING produces
a change. If `action` is `no_change`, no new revision is written and `current_revision_id`
is unchanged.

### What this fixes (beyond Solution A)

- **Spec accuracy**: the `game-state-logic` spec is updated to say "Pass, Score, and
  CheckWinner already exist as stubs at lines 100–104 of game.go — fill them in." The
  executor is not guessing; the spec reflects ground truth.
- **Ahead-of-scope detection**: if bead N fully implemented a function scoped to bead
  N+2, REVISE_PENDING can update bead N+2's spec to acknowledge this and redirect
  the executor to the remaining work.
- **Cascading preservation**: the handler beads receive updated specs that name the
  specific functions written by earlier handler beads, making preservation instructions
  concrete.

### What this does not fix

- **RECONCILE gaps**: REVISE_PENDING is not a replacement for AUDIT/RECONCILE. Structural
  decomposition problems (wrong file assignments, missing integration beads) still need
  the upfront audit cycle. REVISE_PENDING only handles divergence between plan and reality
  that emerges during execution.

---

## ANALYZE and ADJUDICATE context

ANALYZE and ADJUDICATE currently receive the execution trace, the current bead spec, and
the compressed history of prior attempts on that bead. They do not see what previous beads
produced or what reasoning REVISE_PENDING used when it updated the spec. Both verbs would
benefit substantially from this context.

### Revision summary (standard, when available)

When a bead has one or more `spec_revisions` records, include the revision summary in the
ANALYZE and ADJUDICATE prompts alongside the current spec. The summary should cover:

- What the original spec said (the `old_revision_id` full_text)
- What REVISE_PENDING changed and why (the raw_output reasoning)
- Which prior bead triggered the revision (`trigger_bead_id` title)

This is compact — a few sentences per revision — and high-signal. It lets ANALYZE
correctly attribute failures: "the executor tried to redeclare FindFlips despite
REVISE_PENDING noting it was already implemented by game-init" is a materially different
diagnosis from "the executor redeclared FindFlips for unknown reasons." ADJUDICATE uses
this to craft a more targeted `execute_revised` instruction, explicitly strengthening
whichever part of the revised spec the executor ignored.

Include this context in the DB query that assembles the ANALYZE/ADJUDICATE prompt. The
`spec_revisions` table provides everything needed; no additional model call is required.

### Shared-file content (conditional, on clobber detection)

ANALYZE can mechanically detect a shared-file clobber: compare the current content of each
output file against what it contained at the start of the execution. If a function that
existed before the execution is absent afterward, that is a clobber.

When ANALYZE detects a clobber, include the pre-execution content of the affected file in
the ADJUDICATE prompt. ADJUDICATE needs to see what was lost — the exact functions and
their signatures — to craft a recovery instruction that names them explicitly rather than
abstractly.

This is conditional: no file content is injected for clean executions. The token cost is
incurred only when there is a concrete problem to fix.

### Implementation notes

- Pre-execution file snapshots: take a snapshot of each output file's content at execution
  start time (when the EXECUTE_BEAD job dispatches). Store as a column on `executions` or
  as a sidecar file in `traces/`. The snapshot is needed only if ANALYZE detects a clobber;
  lazy storage (write only if content differs post-execution) avoids persisting unchanged
  files.
- Revision summary assembly: query `spec_revisions WHERE revised_bead_id = ?` ordered by
  `created_at`. For each row, join `bead_revisions` on `old_revision_id` to get the
  original spec text, and extract the reasoning from `raw_output`.
- Both additions are read-only from ANALYZE and ADJUDICATE's perspective — no new write
  paths required.

---

### Cost and risk

- **LLM call per succeeded bead.** For a 9-bead project: up to 9 revision calls. Each
  revision call receives all remaining pending specs plus current file content — moderate
  token cost per call, but cumulative.
- **New failure mode.** A bad revision could propagate incorrect assumptions forward.
  Mitigation: REVISE_PENDING is append-only (writes new `bead_revisions` rows); the old
  spec is always recoverable. The orchestrator can be given a flag to disable REVISE_PENDING
  for debugging.
- **Latency.** Adds one model round-trip between each bead's success and the next dispatch.
  For a fast model (gemma3:27b) this is 10–30s — acceptable but visible.

---

## Recommended sequencing

Implement Solution B as the primary path.

Solution A's mechanical injection solves a specific symptom (don't clobber output files)
but has a natural tendency to grow: once you add injection for output files, the next
logical step is injection for files the bead references but doesn't own (e.g., an
integration bead calling functions defined in handlers.go). At that point you are dumping
the whole project folder into every executor prompt — heavy, unstructured, and shifting
the synthesis burden onto the executor itself.

Solution B keeps a cleaner abstraction boundary. The REVISE_PENDING model call does the
synthesis once per bead — reading current file state and distilling it into actionable
spec updates — so the executor receives a focused, accurate spec rather than raw file
content to reason through. This is also a better fit for the execute models: Gemma 3
following an explicit instruction ("FindFlips is already implemented at lines 34–66 —
fill in the stub body, do not redeclare") is more reliable than Gemma 3 deriving the
same constraint from 400 lines of game.go.

Solution A is retained in this document as a documented alternative for simpler cases
(single-file beads with no cross-bead references) or as a fallback if Solution B's
revision model proves unreliable in practice. The two are not mutually exclusive, but
Solution B makes Solution A largely redundant — a spec that accurately describes current
file state is a better prompt input than raw file injection alongside an outdated spec.

---

## Open questions

- **Injection threshold.** At what file size does full-content injection become a token
  liability? Tentative answer: signature-only injection above 500 lines. Revisit with data.
- **REVISE_PENDING scope.** DECIDED: revise all pending beads after every succeed. See
  "Scope: all pending beads" section above.
- **Model assignment for REVISE_PENDING.** Should use a fast, cheap model — it is reading
  structured file content and updating prose, not doing deep reasoning. Tentative: same
  model as AUDIT (qwen3:32b or equivalent).
- **Interaction with ADJUDICATE spec revision.** ADJUDICATE already revises the current
  bead's spec when it returns `execute_revised`. REVISE_PENDING revises future beads'
  specs. These are orthogonal and should not conflict, but the interaction should be
  validated in practice.
