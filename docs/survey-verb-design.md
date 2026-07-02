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

The layout bead is removed from the execute loop entirely. SURVEY, VERIFY, and CERTIFY
replace it. DECOMPOSE runs after CERTIFY approves the manifest, receiving both the design
doc and a survey document produced by the mechanical extraction step.

This is the pipeline — not a mode or a flag. `project.Create()` unconditionally enqueues
SURVEY_SPEC as the first job. Existing in-flight projects at the execute stage continue
via their existing FSM state; their stubs are already on disk.

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

## FSM State

The FSM transitions for the new pipeline:

```
SURVEY_SPEC → VERIFY_MANIFEST → CERTIFY_MANIFEST
  ├── approve → [mechanical extraction] → DECOMPOSE_SPEC → AUDIT_DECOMPOSITION → ...
  └── reject  → (wipe files) → SURVEY_SPEC (retry)
```

At 5 consecutive CERTIFY rejections, the project is marked `full_stopped` instead of
re-enqueuing SURVEY_SPEC.

AUDIT and RECONCILE have separate retry loops from SURVEY/VERIFY/CERTIFY. The two loops
are distinct; no runtime mode detection or survey-awareness is needed in AUDIT/RECONCILE.

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

---

## Guidance Architecture

SURVEY and EXECUTE each receive only the guidance relevant to their task. Guidance is split
into two kinds — generic (language-agnostic, embedded in Go source) and language-specific
(a runtime file) — with no mixing between them.

### Language detection for SURVEY

SURVEY runs before `go.mod` exists on disk, so `guidance.Detect(folderPath)` (which checks
the filesystem) cannot be used. Instead, the `projects` table gains a `language` column
(default `"go"`, set at `new-project` time via a `--language` flag). The guidance package
is extended with `InjectForVerb(prompt, language, verb, guidanceDir)`, which loads
`go-survey.md` for SURVEY_SPEC and `go.md` for all other verbs. If the verb-specific file
is absent, fall back to the base language file.

### What goes where

**SURVEY generic system prompt** (in `prompts.go`, language-agnostic):
- Role: "You produce scaffolding only. Do not implement any logic."
- Stub-purity rule (language-agnostic: "every function body must return a zero value")
- Behavioral test prohibition ("no test files except the API check file")
- Manifest JSON schema with worked example
- Retry framing: incorporate rejection feedback before revising

**`go-survey.md`** (new runtime file, Go-specific SURVEY guidance):
- Go stub body syntax and zero-value return examples
- `api_check_test.go` format: package declaration, required imports, `var _` assertions
  for every exported function and package-level variable, blank-identifier assertion syntax
- `go.mod` format: module line + go version
- Stale file note: on retry, overwrite existing files rather than appending

**`go.md`** (existing runtime file, updated for EXECUTE):
- Remove: "Compile-time assertions" section — EXECUTE never writes `api_check_test.go`
- Remove: `go mod init` command — EXECUTE never creates `go.mod`
- Add: "Stub files already exist on disk — implement logic into them; do not recreate stubs"
- Add: "`api_check_test.go` is present and read-only — never modify it"

**EXECUTE generic system prompt** (in `execution/prompts.go`):
- No layout-bead-specific content exists here; no changes needed

### Paired behaviors and interconnected components

Guidance for paired functions (encode/decode, reader/writer, round-trip invariants) belongs
in **DECOMPOSE** and stays there. SURVEY's job is purely structural: which files exist and
what are their stubs. DECOMPOSE decides decomposition strategy — including when paired
functions need an integration bead and how to structure exit criteria for isolation vs.
joint verification. No changes to this section of the DECOMPOSE prompt.

---

## VERIFY

VERIFY materializes the SURVEY manifest to disk and runs all checks. It has both a
mechanical layer (always runs) and a model layer (`verifier_interpretation`, initially
minimal). The model slot exists so judgment can be added — e.g., "these violations appear
to be template boilerplate; the model may need stronger stub-only framing" — without a
structural change.

VERIFY is model-free in the initial implementation. It is NOT in `AllVerbs` and has no
entry in `verb_model_assignments`. The dispatch layer skips model warmup for VERIFY_MANIFEST
via a `verbSkipsModelWarmup(verb string) bool` guard.

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
- **Full stop**: 5 consecutive rejections → project marked `full_stopped`.

---

## DB Schema

### New tables

Parallel to `handoff_attempts` / `adjudications`:

```sql
CREATE TABLE IF NOT EXISTS verify_attempts (
  id                       INTEGER PRIMARY KEY,
  project_id               INTEGER NOT NULL REFERENCES projects(id),
  job_id                   INTEGER NOT NULL REFERENCES handoff_jobs(id),
  attempt_number           INTEGER NOT NULL,
  file_presence_pass       INTEGER NOT NULL,
  no_behavioral_tests_pass INTEGER NOT NULL,
  compile_pass             INTEGER NOT NULL,
  api_check_pass           INTEGER NOT NULL,
  stub_purity_pass         INTEGER NOT NULL,
  violations               TEXT,
  verifier_interpretation  TEXT,
  created_at               TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS certifications (
  id                    INTEGER PRIMARY KEY,
  project_id            INTEGER NOT NULL REFERENCES projects(id),
  verify_attempt_id     INTEGER NOT NULL REFERENCES verify_attempts(id),
  preliminary_decision  TEXT    NOT NULL CHECK (preliminary_decision IN ('approve', 'reject')),
  model_reasoning       TEXT,
  final_decision        TEXT    NOT NULL CHECK (final_decision IN ('approve', 'reject')),
  feedback              TEXT,
  created_at            TIMESTAMP NOT NULL
);
```

### Projects table addition

Add `language` column via `columnMigrations` (the existing backward-compat mechanism):

```sql
ALTER TABLE projects ADD COLUMN language TEXT NOT NULL DEFAULT 'go';
```

Set at `new-project` time via `--language` flag (default `"go"`). Used by SURVEY to select
the correct language-specific guidance file.

### Model seeding

SURVEY_SPEC and CERTIFY_MANIFEST are added to `AllVerbs` and seeded in
`SeedVerbModelAssignments`. VERIFY_MANIFEST is NOT in `AllVerbs` (model-free).

Default model assignments for fleet files that predate this change:
- SURVEY_SPEC → same model as DECOMPOSE_SPEC
- CERTIFY_MANIFEST → same model as AUDIT_DECOMPOSITION

---

## Mechanical Extraction (Survey Document)

After CERTIFY approves, the pipeline runs a mechanical extraction step (no model call) and
produces `survey.md` in the project folder. `survey.md` is an invariant: it always exists
on disk by the time DECOMPOSE runs. DECOMPOSE.Run() returns an error if `survey.md` is
missing rather than silently proceeding.

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

### JSON output schema change

The `full_text` field description currently has a "no source code except in the layout Bead"
exception. This exception is removed — `full_text` is now prose-only for all beads.

---

## AUDIT / RECONCILE Changes

AUDIT and RECONCILE are not "survey-aware." The SURVEY and execute loops are distinct; no
runtime mode detection is needed. The only changes remove obsolete layout-bead references:

- **AUDIT check #4** ("Layout Bead — Bead 1 must be a layout bead") — removed entirely.
  Renumber subsequent checks: current #5 → #4, #6 → #5, #7 → #6.
- **AUDIT check #2** — remove the "File overlap between Bead 1 (the layout Bead) and any
  other Bead is expected and must NOT be flagged" exception. All beads are now implementation
  beads; file overlap is always flagged.
- **`buildAuditUserMsg`** — remove `[Layout Bead]` label from `beads[0]`. All beads are
  labeled by position only.
- **`goMechanicalBeadChecks`** — remove the `i == 0` check requiring `api_check_test.go`
  in the first bead's output_files. VERIFY owns that check now.

---

## ANALYZE Changes

`analyze_execution.go` contains `checkLayoutBeadOutput`, a short-circuit path that fires
when a bead that owns `api_check_test.go` is missing or malformed assertions. In the new
pipeline, VERIFY owns this check before any execution happens. Remove `checkLayoutBeadOutput`
and its call site from `AnalyzeExecution.Run()`.

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