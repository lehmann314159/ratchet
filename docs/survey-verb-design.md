# SURVEY Verb — Design Specification

## Motivation

The layout bead's role is to create stub files that establish the project's file structure,
type definitions, and API contract (via `api_check_test.go`). In practice, the layout bead
frequently over-implements: given a design doc that specifies exact types and function
signatures, the execute model writes full implementations rather than stubs. Later beads
then find the work already done and do nothing, producing incomplete or incorrect results.

The root cause is structural. DECOMPOSE produces bead specs so prescriptive that the execute
model cannot distinguish "scaffold this" from "implement this." The fix is to give the layout
work its own verb — SURVEY — with purpose-built guidance that prohibits implementation, and
to delay the remaining bead specifications until after SURVEY succeeds, so they are grounded
in what actually exists rather than in what was planned.

---

## Pipeline Flow

**Before:**
```
DECOMPOSE → AUDIT → RECONCILE → [layout bead] → [impl beads...] → done
```

**After:**
```
SURVEY → VERIFY → CERTIFY → [mechanical extraction] → DECOMPOSE → AUDIT → RECONCILE → [impl beads...] → done
```

The layout bead is removed from the execute loop. SURVEY, VERIFY, and CERTIFY replace it.
DECOMPOSE runs after CERTIFY approves the manifest, receiving both the design doc and a
survey document produced by the mechanical extraction step.

---

## Verb Pairs

The pipeline uses three verb pairs, each handling a distinct artifact type:

| Pair | Input artifact | Responsibility |
|------|---------------|----------------|
| SURVEY / VERIFY+CERTIFY | Design doc → file manifest | Scaffold the project; verify stub correctness |
| DECOMPOSE / AUDIT+RECONCILE | Design doc + survey doc → bead specs | Plan the implementation; critique and fix the plan |
| EXECUTE / ANALYZE+ADJUDICATE | Bead spec → written files | Implement each bead; assess and decide next action |

VERIFY and CERTIFY together form the checking pair for SURVEY output, analogous to
ANALYZE+ADJUDICATE for EXECUTE output. Both VERIFY and CERTIFY are designed with mechanical
and model layers so judgment can be added without a structural change.

---

## SURVEY Verb

### What it does

SURVEY reads the design doc and produces a **file manifest**: a structured JSON document
listing every file to be created and its complete stub content. SURVEY does not write files
to disk. Files are materialized by VERIFY. On rejection by CERTIFY, the orchestrator wipes
the materialized files and SURVEY produces a revised manifest.

SURVEY does not use `write_file` or `run_command` tools. It produces the manifest as its
structured text output, similar to how DECOMPOSE produces bead specs.

### Input

- Design doc (full text)
- On retry attempts: CERTIFY rejection feedback from the previous attempt

### Output

A JSON file manifest:

```json
{
  "module": "othello",
  "package": "main",
  "files": [
    { "path": "go.mod",            "content": "module othello\n\ngo 1.22\n" },
    { "path": "game.go",           "content": "package main\n\n..." },
    { "path": "api_check_test.go", "content": "package main\n\nimport ...\n\nvar _ ..." }
  ]
}
```

### Files included

SURVEY must produce stubs for every implementation file the project needs, plus `go.mod`
and `api_check_test.go`. It must not produce any behavioral test files (`*_test.go` files
other than `api_check_test.go`). Behavioral tests are written by implementation beads.

### Stub-purity rule

Every function in every implementation file must be a stub. A function is a stub if its
body contains no branching nodes. The following AST node types are prohibited in any
function body:

- `ast.IfStmt`
- `ast.ForStmt`
- `ast.RangeStmt`
- `ast.SwitchStmt`
- `ast.TypeSwitchStmt`
- `ast.SelectStmt`

Valid stub bodies:
```go
func Foo() {}                           // empty (void function)
func Bar() *Game     { return nil }     // zero-value pointer
func Baz() error     { return nil }     // zero-value error
func NewGame() *Game { return &Game{} } // zero-value struct
func Score() (int, int) { return 0, 0 }
```

`api_check_test.go` is exempt from the stub-purity check (it contains `var _` declarations,
not function bodies with logic).

### Prompt guidance

The SURVEY system prompt differs from the EXECUTE prompt in emphasis:

- "You are producing scaffolding only. Do not implement any logic. Every function body must
  return a zero value."
- "Your output will be read by automated tools to generate specifications for other agents.
  If you implement logic now, those agents will have nothing meaningful to do."
- "Do not write behavioral test files. `api_check_test.go` is the only test file permitted
  in your manifest."
- The manifest JSON schema is described explicitly with a worked example.

---

## VERIFY

VERIFY materializes the SURVEY manifest to disk and runs all checks. It has both a
mechanical layer (always runs) and a model layer (`verifier_interpretation`, initially
minimal). The model slot exists so judgment can be added — e.g., "these violations appear
to be template boilerplate; the model may need stronger stub-only framing" — without a
structural change.

### Mechanical checks

1. **File presence**: Every file listed in the manifest exists on disk after
   materialization.
2. **No behavioral test files**: No `*_test.go` files other than `api_check_test.go` are
   present in the project folder.
3. **Compile check**: `go test -c -o /dev/null ./...` exits 0.
4. **api_check assertion check**: `grep -q '^var _' api_check_test.go`.
5. **Stub-purity check**: AST walk of all implementation `.go` files. Each function
   containing a blacklisted node is reported with file name, function name, and violation
   type (e.g., `handlers.go: InitTemplates contains ast.RangeStmt`).

### Model layer

`verifier_interpretation`: optional narrative over the mechanical findings. Initially a
passthrough or omitted. Intended for cases where the pattern of violations is informative
beyond the raw finding list.

### Output

A structured findings report: pass/fail per check, violation list for stub-purity, and
optional verifier interpretation. This feeds into CERTIFY.

---

## CERTIFY

CERTIFY receives VERIFY's findings and makes the approve/reject decision. Like VERIFY, it
has both a mechanical layer (preliminary decision from check results) and a model layer
(final decision + targeted feedback). The model slot allows escalating to full judgment
without structural change — e.g., overriding a rejection for a minor violation when the
compile check passed.

### Mechanical layer

Preliminary decision derived directly from VERIFY findings:
- All five checks passed → preliminary approve
- Any check failed → preliminary reject, violations listed

### Model layer

The model receives the mechanical findings, optional verifier interpretation, and the
preliminary decision. It produces:

- **Final decision**: approve or reject (may override preliminary in edge cases)
- **Feedback for SURVEY**: actionable revision guidance if rejecting. Raw violation names
  are unambiguous but terse; the model translates them into targeted instructions
  (e.g., "InitTemplates must return immediately without iteration — replace the range loop
  with a single `Templates = template.Must(...)` stub return").

Initially the model call can be lightweight, formatting raw violations into feedback.
The structure supports escalating to deeper reasoning later.

### Decision outcomes

- **Approve**: all checks passed (and model confirms). Orchestrator proceeds to mechanical
  extraction. Materialized files remain in the project folder.
- **Reject**: one or more checks failed. Orchestrator wipes materialized files. CERTIFY
  feedback is prepended to SURVEY's next attempt input.
- **Full stop**: configurable consecutive-rejection limit (default 5) with no improvement
  across attempts.

### DB schema

Parallel to `handoff_attempts` / `adjudications`:

- `verify_attempts`: one row per VERIFY run; columns for job_id, attempt_number,
  mechanical_findings (structured), verifier_interpretation (nullable text), created_at.
- `certifications`: one row per CERTIFY decision; columns for project_id, verify_attempt_id,
  preliminary_decision, model_reasoning (nullable text), final_decision, feedback (nullable
  text), created_at.

---

## Mechanical Extraction (Survey Document)

After CERTIFY approves, the pipeline runs a mechanical extraction step (no model call) and
produces `survey.md` in the project folder.

### Extraction method

Go AST parsing of the materialized files. Extracts:

- Module name and package name
- File inventory
- All exported types with complete declarations (structs with fields, type aliases, const
  blocks)
- All package-level variables with declared types, annotated with source file
- All exported functions with exact signatures, grouped by file
- Full content of `api_check_test.go`
- Full stub content of every implementation file

### Survey document schema

```markdown
# Survey — <module>

## Module
module: <module name>
package: <package name>

## Files
- <file1>
- <file2>
...

## Types and Constants
[complete Go source for all exported type and const declarations]

## Package-Level Variables
[complete Go source for all package-level var declarations, annotated with source file]

## api_check_test.go
[full file content]

## File Contents

### <file1>
[full stub content]

### <file2>
[full stub content]

...
```

`survey.md` is passed to DECOMPOSE as an additional input alongside the design doc.

---

## DECOMPOSE Changes

DECOMPOSE receives the design doc and the survey document. Its behavior changes in three
ways:

1. **No layout bead.** DECOMPOSE does not produce a layout bead. The stub files already
   exist on disk and `api_check_test.go` is already correct.

2. **Survey doc is ground truth.** For types, function signatures, package-level variables,
   and api_check assertions, DECOMPOSE treats the survey document as authoritative — not the
   design doc. If the design doc and survey doc differ (SURVEY chose different field names or
   rearranged a struct), the survey doc wins. DECOMPOSE must not re-derive types from the
   design doc when the survey doc provides them.

3. **Simpler bead specs.** Because stubs already exist on disk, implementation bead specs do
   not need to embed type blocks or api_check file content verbatim. EXECUTE models will read
   the existing stubs during orientation. Bead specs focus on what to implement, which test
   functions to write, and what the exit criterion is.

AUDIT and RECONCILE are unchanged — they run after DECOMPOSE on the resulting bead specs,
same as today.

---

## Backward Compatibility

Existing projects in flight continue with the old flow (layout bead as the first execute
bead). The new SURVEY flow applies to new projects only. No migration needed.

---

## Open Questions (deferred)

- **Non-Go projects.** The stub-purity check is Go-specific (Go AST). Stub-purity for other
  languages would need language-specific implementations. Not addressed here.
- **Pre-SURVEY hints.** Should SURVEY receive any decomposition hints (bead count, output
  file assignments) from an upstream step, or always derive everything from the design doc
  alone? Current decision: design doc only, no upstream hints.
- **survey.md persistence.** The survey document is a generated artifact. It should be
  excluded from version control (`.gitignore`) but retained in the project folder for
  debugging and for DECOMPOSE to read.
