# Post-Execution Report Specification

## Overview

Every bead and every project produces a structured markdown report when it reaches a terminal state. Reports are written mechanically — no model call, no synthesis. The structured data from the DB and trace files is the story; a reader (human or model) draws conclusions from it.

**Primary use case:** Async review. After a long run completes without an interactive observer, these files provide a complete reconstruction of what happened without requiring DB queries or trace file spelunking.

**Secondary use case:** Prompt context. Future sessions can load a bead or project report as context instead of re-deriving state from raw tables.

**Design constraint:** No model call. Reliable, fast, free. A wrong root cause injected by a model is worse than no synthesis at all.

---

## File Locations

Both files live in the project's `traces/` directory, which is created on first execution. For beads that never executed (cascaded full_stop before they ran), the `traces/` directory may not exist — create it before writing the bead report.

```
<project_folder>/
└── traces/
    ├── bead-{id}-attempt-{n}.log     (existing — execution traces)
    ├── bead-{id}-report.md           (new — written at bead terminal state)
    └── project-report.md             (new — written at project terminal state)
```

Bead reports use the bead's integer DB id (e.g. `bead-82-report.md`), matching the existing trace file naming convention.

---

## Trigger Conditions

**Bead report** — written in `AdjudicateNextExecution.Commit` immediately after the bead status is set to a terminal state:
- `declare_success` → bead status becomes `succeeded`
- `full_stop` → bead status becomes `full_stopped`
- Escalation at attempt cap → job status becomes `escalated`

Beads that are cascade-stopped (marked `full_stopped` by a prior bead's `full_stop` decision, without ever running) also need a report. These are written in the same `full_stop` branch, after the bulk `UPDATE beads SET status = 'full_stopped' WHERE id > ?` query, by iterating over the newly-stopped beads and writing a report for each.

In all cases, if the write fails, log a warning but do not fail the transaction — the report is observational, not load-bearing.

**Project report** — written:
- When `advanceToNextBead` finds no next bead (all beads succeeded → project `complete`)
- When `checkProjectTerminal` marks the project `full_stopped`
- When `atExecutionCap` escalates the last blocking bead

---

## Bead Report Format

### Filename
`traces/bead-{bead_id}-report.md`

### Sections (always present, in this order)

1. Header (title, status, attempt count, wall time, final exit criterion)
2. Spec History (all revisions, all fields, full prose)
3. Attempt History (table; empty if bead never executed)
4. ADJUDICATE Decisions (one entry per non-success adjudication; omitted section if none)
5. Compressed History (full text; "(none)" if bead never executed)
6. Final Output Files (each file in output_files, full content or "(not present)")
7. Last Trace Excerpt (final 60 lines of most recent trace; "(none)" if bead never executed)

### Full Example

```markdown
# Bead 82: move-generation

**Status:** escalated  
**Attempts:** 6  
**Wall time:** 7320s (122m)  
**Final exit criterion:** `go test -v . -run=TestValidMoves\|TestAllValidMoves`

---

## Spec History

### Revision 1 — created by DECOMPOSE_SPEC

**Title:** move-generation  
**Output files:** game.go, game_test.go  
**Exit criteria:** `go test -v . -run=TestValidMoves\|TestAllValidMoves`  
**Execution budget:** 900s  
**Monitor override:** honor  

Implement ValidMoves (returns all geometrically possible moves for a piece) and
AllValidMoves (enforces mandatory capture rules) in game.go. ValidMoves should return
non-jump moves when no jumps are available, while AllValidMoves must filter to
jumps-only when any jump exists. Write tests in game_test.go for: (a) regular
non-jump move, (b) forced jump, (c) multi-jump chain detection, and (d) empty moves
when no legal options exist.

### Revision 2 — created by ADJUDICATE_NEXT_EXECUTION (after attempt 1)

**Title:** move-generation  
**Output files:** game.go, game_test.go  
**Exit criteria:** `go test -v . -run=TestValidMoves\|TestAllValidMoves`  
**Execution budget:** 900s  
**Monitor override:** honor  

Implement ValidMoves and AllValidMoves in game.go.

Requirements:
1. ValidMoves(piece): Should return all potential moves for the given piece based on
   board geometry, including both simple moves and jumps.
2. AllValidMoves():
   - Must first check if any jump is available for any piece of the current player.
   - If jumps are available, it must return ONLY those jump moves (mandatory capture rule).
   - If no jumps are available, return all regular non-jump moves for all pieces.
3. Write comprehensive tests in game_test.go covering:
   (a) A regular non-jump move.
   (b) A forced jump (where a jump exists and must be taken over a simple move).
   (c) Multi-jump chain detection.
   (d) Empty moves when no legal options exist for the player.

*(further revisions follow the same format)*

---

## Attempt History

| # | Execution ID | Termination        | Duration | Monitor  | write_file ok/total | Last test result                                                  |
|---|--------------|--------------------|----------|----------|---------------------|-------------------------------------------------------------------|
| 1 | 147          | timeout            | 900s     | no fire  | 0/1                 | not run                                                           |
| 2 | 148          | timeout            | 900s     | no fire  | 0/1                 | not run                                                           |
| 3 | 149          | (killed/no record) | —        | —        | —                   | —                                                                 |
| 4 | 150          | monitor_terminated | 312s     | FIRED    | 2/2                 | TestValidMoves PASS, TestAllValidMoves FAIL (got 2, want 1)       |
| 5 | 151          | success            | 1169s    | no fire  | 2/2                 | TestValidMoves PASS, TestAllValidMoves FAIL (exit 1 — false done) |
| 6 | 152          | timeout            | 1800s    | no fire  | —                   | not run                                                           |

*Note: "write_file ok/total" counts from trace parser. "Last test result" is the last
run of the exit criterion command in the trace; for termination_cause=success this is
from ANALYZE mechanical findings (which may differ from the executor's self-assessment).*

---

## ADJUDICATE Decisions

### After attempt 1 → execute_revised

**Trend:** same  
**Bead spec fit:** execution_capability_problem  
**Actual execution budget:** 900s  
**Reasoning:** The previous attempt timed out after only one command (ls), likely due
to the execution_budget being set to 0 or an infrastructure issue. The agent made no
progress. I will provide a realistic execution budget.

### After attempt 2 → execute_revised

**Trend:** same  
**Bead spec fit:** execution_capability_problem  
**Actual execution budget:** 900s  
**Reasoning:** Agent has timed out twice without progress. Will double the budget and
add explicit planning requirement.

### After attempt 4 → execute_revised

**Trend:** same  
**Bead spec fit:** execution_capability_problem  
**Actual execution budget:** 900s  
**Reasoning:** ValidMoves passing but AllValidMoves returning jump+slide when only
jumps should be returned. Providing more explicit algorithmic guidance.

### After attempt 5 → execute_revised

**Trend:** same  
**Bead spec fit:** execution_capability_problem  
**Actual execution budget:** 900s  
**Reasoning:** Agent declared success despite tests failing. Escalating to verbatim
code for the filtering logic per the specificity ratchet.

### After attempt 6 → escalated (attempt cap reached)

No adjudication written — escalated mechanically at cap.

---

## Compressed History

NEW — attempt 1: execution timed out after 900s. write_file called without path
argument; error returned was "open /project: is a directory". game.go not updated.

NEW — attempt 2: same write_file path failure. Agent ran ls, attempted write_file
with no path, re-read game.go, then timed out generating response.

RECURRING — attempt 4: monitor terminated at turn 10. game.go and game_test.go
written successfully. TestValidMoves PASS. TestAllValidMoves FAIL: "expected exactly
1 move due to forced jump, got 2". AllValidMoves returning both jumps and simple
moves when mandatory capture should suppress simple moves.

RECURRING — attempt 5: agent declared done after test run returned exit status 1.
TestValidMoves PASS. TestAllValidMoves FAIL (same error). ADJUDICATE overrode
false success declaration.

RECURRING — attempt 6: timed out at 1800s. Verbatim code in spec used wrong field
names (g.Pieces, g.CurrentPlayer, m.IsJump — none exist in Game struct).

---

## Final Output Files

*State of each output_file on disk at report time. Files left by the last attempt —
may be partially correct on escalation or full_stop.*

### game.go

```go
package main

import "errors"

// ... (full file content verbatim)
```

### game_test.go

```go
package main

import "testing"

// ... (full file content verbatim)
```

---

## Last Trace Excerpt

*Final 60 lines of `traces/bead-82-attempt-6.log`*

```
[TURN 3]
[tool: run_command map[command:go build ./...]]
[result]
stdout:
stderr:
game.go:92:25: g.Pieces undefined (type *Game has no field or method Pieces)
game.go:93:38: g.CurrentPlayer undefined (type *Game has no field or method CurrentPlayer)
exit: exit status 1

[TURN 4]
[terminated: timeout]
```
```

---

## Zero-Execution Bead Report

When a bead is cascade-stopped (full_stopped before any EXECUTE_BEAD job ran), the
report uses the following form for all sections that require execution data:

- **Attempts:** 0
- **Wall time:** 0s
- **Attempt History:** *(bead never executed — cascade stopped by bead {N})*
- **ADJUDICATE Decisions:** *(none)*
- **Compressed History:** *(none)*
- **Final Output Files:** each file listed as `(not present)`
- **Last Trace Excerpt:** *(none)*

The Spec History section is still fully populated — it shows the spec that would have
run, which is useful for understanding what work was planned.

### Example header

```markdown
# Bead 88: templates

**Status:** full_stopped (cascade — stopped by bead 87)  
**Attempts:** 0  
**Wall time:** 0s  
**Final exit criterion:** `go test -v . -run=TestTemplatesParse`
```

---

## Project Report Format

### Filename
`traces/project-report.md`

### Full Example

```markdown
# Project: checkers-v1 (id: 24)

**Status:** complete  
**Beads:** 9 total — 9 succeeded, 0 escalated, 0 full_stopped, 0 never ran  
**Total attempts:** 14  
**Wall time:** 14220s (237m)  
**Completed:** 2026-06-28T22:41:00Z

---

## Bead Summary

| Bead | Title             | Status    | Attempts | Revisions | Wall time |
|------|-------------------|-----------|----------|-----------|-----------|
| 80   | layout            | succeeded | 1        | 1         | 332s      |
| 81   | game-state        | succeeded | 1        | 1         | 492s      |
| 82   | move-generation   | succeeded | 6        | 5         | 7320s     |
| 83   | move-execution    | succeeded | 2        | 1         | 1140s     |
| 84   | win-detection     | succeeded | 1        | 1         | 387s      |
| 85   | game-integration  | succeeded | 1        | 1         | 420s      |
| 86   | ai                | succeeded | 1        | 1         | 298s      |
| 87   | http-handlers     | succeeded | 2        | 2         | 1640s     |
| 88   | templates         | succeeded | 1        | 1         | 631s      |

---

## Attempt Distribution

| Attempts to reach terminal state | Bead count |
|----------------------------------|------------|
| 1                                | 6          |
| 2                                | 2          |
| 6                                | 1          |

---

## Final Source Files

*All files present in the project folder at project completion, excluding traces/,
design_doc.md, and go.sum. For a partial run (status: full_stopped), files from
unsucceeded beads may be incomplete or incorrect — see individual bead reports.*

### go.mod

```
module checkers

go 1.21
```

### game.go

```go
package main

// ... (full file content verbatim)
```

### game_test.go

```go
package main

// ... (full file content verbatim)
```

### ai.go

```go
package main

// ... (full file content verbatim)
```

### ai_test.go

```go
package main

// ... (full file content verbatim)
```

### handlers.go

...

### main.go

...

### api_check_test.go

...

### templates/index.html

...

### templates/board.html

...

### templates_test.go

...
```

*Files are listed in dependency order where determinable (go.mod first, then .go files
alphabetically, then templates). For a partial run, files in output_files of
unsucceeded beads that are nonetheless present on disk are included with a note:
`*(present on disk — written by bead {N} which did not succeed)*`*

---

## Implementation Notes

### Trace parser extension

Extend `trace.Parse` and `ParsedTrace` to capture `write_file` calls:

```go
type WriteFileResult struct {
    Turn      int
    Path      string // empty string if model omitted the path argument
    Succeeded bool   // true if result line starts with "ok:"
}

// Add to ParsedTrace:
WriteFiles []WriteFileResult
```

The trace format for write_file is:
```
[tool: write_file map[content:...\n path:game.go]]
[result]
ok: wrote 1234 bytes to game.go
```
or on failure:
```
[result]
error: write_file requires a 'path' argument ...
```

Parse by scanning for lines starting with `[tool: write_file`. Extract the `path:` value
from the last line before `]]` (it appears after the content, due to Go map key sorting).
Read the result line to determine success.

### Spec history

Show every revision in full — all fields every time. No diff computation. This ensures
each revision is a complete, standalone document and eliminates the need for a reader to
mentally reconstruct the current spec from a chain of diffs.

Fields to include per revision (in this order):
1. Title
2. Output files (comma-separated)
3. Exit criteria (one per line if multiple)
4. Execution budget
5. Monitor override
6. Full prose (the `full_text` JSON field's prose content, not the raw JSON)

### Output file collection for project report

Read all files present in the project folder at report time, recursively, excluding:
- The `traces/` directory itself
- `design_doc.md` (input artifact, not output)
- `go.sum` (generated, not authored by any bead)
- Any file matching `.git/`

For a partial run, include all files found regardless of whether their bead succeeded,
with the note described in the format section.

### Wall time computation

- **Per-bead wall time:** sum of `(ended_at - started_at)` across all executions for
  that bead where `ended_at IS NOT NULL`. Excludes the ANALYZE/COMPRESS/ADJUDICATE
  overhead — this is execution time only, which is the expensive part.
- **Project wall time:** `(project.updated_at - project.created_at)` — total elapsed
  time including all pipeline overhead.

### Attempt numbering

Number attempts 1-N in order of `executions.started_at`. Include executions with NULL
`termination_cause` (killed mid-run by restart) in the table — they consumed time and
may have partial trace files worth reading. Mark their termination as "(killed/no record)".

### Last trace excerpt

Include the final 60 lines of the trace file for the most recent execution (highest
`execution_id` for the bead). Read with a tail operation rather than loading the full
file. 60 lines is sufficient to capture the last tool call, its result, and the
termination marker in nearly all cases.

### Error handling

Report generation is best-effort. A failure to write any report must not fail the
pipeline transaction. Log at WARN level and continue. Partial report files (written
before a failure) are acceptable — they still contain partial information useful for
diagnosis.

---

## Future: Open-Model Compatibility

The current format is optimized for a strong model (Claude) that can read unstructured
prose and extract signal. When adapting for a weaker open model, the changes are
additive — the content stays the same, the structure tightens:

- Add a YAML frontmatter block at the top of each report with all scalar fields
  (status, attempt_count, wall_time_s, bead_id, etc.) for easy programmatic extraction
- Cap file content sections to a token budget (e.g. first 100 lines + last 20 lines,
  with a truncation marker)
- Replace prose ADJUDICATE reasoning with tagged key-value blocks
- Add explicit section delimiters (e.g. `<!-- section: attempt-history -->`) for
  reliable splitting

These changes are deferred. The current format captures all necessary information;
structure can be tightened without loss of content when the use case demands it.
