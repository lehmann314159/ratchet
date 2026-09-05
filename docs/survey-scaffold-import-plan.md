# SURVEY scaffold-import precision — build plan

**Status:** IMPLEMENTED 2026-09-05 (branch `feat/survey-scaffold-import-disambiguation`).
A + C landed as described below. The "stronger variant" of C (post-EXECUTE /
ADJUDICATE mechanical precondition) was deliberately deferred — see the
decision note under Part C. x/tools bump skipped (it drags the `go` directive
1.23→1.25 plus four transitive deps — out of scope for a targeted fix; still
worth doing on its own).
**Origin:** `memory/project_survey_scaffold_designdoc_compliance`, root-caused
during `exprvm-web-baseline-12` (report:
`~/Documents/ratchet-projects/qual-corpus-baseline-12-REPORT.md`).
**Blocks:** a clean full-pipeline baseline for `docs/decompose-precision-plan.md`
— the bug below `full_stop`s ~50% of `exprvm-web` runs at the templates bead.
**Separate-conversation note:** this is framework work
(`memory/feedback_separate_framework_from_project_runs`) — execute it in its own
conversation, not a project run.

## The bug

`exprvm-web`'s design doc places `var templates *template.Template` in `main.go`
with an inline `// import "html/template" (NOT "text/template")` directive, and
states the rule prose-side 8+ times. Despite that, `main.go`'s scaffold stub gets
`text/template` about half the time.

Chain (all mechanically verified — see the memory for the evidence):

1. `SurveyManifestFile` has only `{Path, Declarations}` — **no import block by
   schema design** (`internal/verbs/outputs.go:10-17`,
   `internal/verbs/schemas.go:213`). The SURVEY prompt explicitly tells the
   model *"No import block"* (`internal/verbs/prompts.go`, `surveySpecSystemPrompt`).
2. `SURVEY_SPEC` also strips the design doc's inline `// import "html/template"`
   comment when transcribing the declaration.
3. `scaffold_go.go:buildGoFile` synthesizes the import block by running the
   declaration text through `golang.org/x/tools/imports` (goimports).
4. **goimports cannot disambiguate `text/template` vs `html/template` from a bare
   `*template.Template`, and its choice is non-deterministic across runs.**
   ratchet vendors `golang.org/x/tools v0.33.0`. Real scaffold history:
   `html/template` in baseline-2/9/10, `text/template` in baseline-3/11/12 —
   same version, same declarations. (Isolated `goimports@v0.33.0` on the exact
   declarations picks `text/template`; `goimports@latest` picks `html/template`.)
   Comments are never consulted.
5. When goimports lands on `text/template` for the `main.go` stub, **and** a
   later `templates` bead's EXECUTE writes `html/template` to `templates.go`
   (correctly), **and** `templates` is decomposed before `main` (318 < 320,
   always) → `templates.go` disagrees with the still-unexecuted `main.go` stub
   on a shared symbol → `templates` bead's tests fail impossibly → `ADJUDICATE`
   correctly returns `full_stop`. `go build` passes throughout (the two files
   declare *different* identifiers), so no mechanical check catches it.

Prior hit: baseline-3 (project 40) `full_stopped` at bead 246 on this exact
mismatch. The ADJUDICATE-side response was hardened afterward (`2afb088`,
`memory/project_refine_write_scope_diagnosis_fixes`) and works — the scaffold
cause was deferred and never fixed.

## Fix: A + C

### Part A — SURVEY declares imports; the scaffold stops guessing

Make the import list a first-class, model-owned part of the manifest, so the
verb that read the design doc makes the disambiguation decision instead of a
context-free mechanical resolver.

- **`internal/verbs/outputs.go`** — add `Imports []string` to `SurveyManifestFile`:
  ```go
  type SurveyManifestFile struct {
      Path         string   `json:"path"`
      Imports      []string `json:"imports"`
      Declarations string   `json:"declarations"`
  }
  ```
- **`internal/verbs/schemas.go`** — add `"imports": {"type": "array", "items":
  {"type": "string"}}` to the per-file object in `SurveySpecSchema`, and add
  `"imports"` to the file object's `required` list. Keep field order
  path → imports → declarations (schema-mode is order-sensitive; mirror it in
  the struct and the prompt's JSON template).
- **`internal/verbs/prompts.go` `surveySpecSystemPrompt`** — replace the *"No
  import block / do not worry about imports"* language with: each file lists its
  imports explicitly in `imports` (stdlib + module-internal as needed); when the
  design document names a specific import for a symbol (e.g. `html/template` vs
  `text/template`, `math/rand/v2` vs `math/rand`), use exactly the one named.
  Still no `package` line, still no literal `import (...)` block inside
  `declarations`.
- **`internal/verbs/scaffold_go.go` `buildGoFile`** — take the imports:
  ```go
  func buildGoFile(pkg string, imports []string, declarations string) string
  ```
  Render `package X` + an `import (...)` block from `imports` + `declarations`,
  then still run `imports.Process` over the result. goimports keeps
  already-present used imports and won't add a competing one, so SURVEY's list
  is authoritative and goimports only covers anything SURVEY missed (a missed
  *unambiguous* import self-heals; a missed *ambiguous* one is exactly what A
  makes SURVEY responsible for). Drop an import SURVEY listed but the
  declarations don't use? `imports.Process` already prunes unused imports —
  acceptable, and CERTIFY's compile check would catch a genuinely wrong list.
  - Both call sites: `scaffoldGoProject` (line 40) passes `f.Imports`;
    `writeScaffoldStubsFromManifest` (line 148) must carry `Imports` alongside
    `Declarations` in `declByPath` (change it to `map[string]SurveyManifestFile`
    or a small struct).
- **`writeAPICheckTest`** is unaffected (it parses declarations only, never
  imports).
- **Back-compat**: existing stored manifests (older projects, fixtures) have no
  `imports` key → unmarshals to `nil` → `buildGoFile` renders no explicit block
  and goimports behaves exactly as today. No migration needed; only new SURVEY
  runs get the new behavior. Confirm `clone-project` / `rewind-bead` paths that
  re-scaffold from a stored manifest still work with `nil` imports.

### Part C — rule-agnostic backstop in VERIFY_MANIFEST

A does the real work; C catches the failure shape regardless of cause (goimports
regression, a future ambiguous pair, SURVEY listing the wrong import).

- **`internal/verbs/verify_manifest.go`** — add check 6: after scaffolding, for
  every package-level identifier declared in more than one scaffolded file,
  resolve the imported type of its declaration in each file; if two files
  resolve the same identifier to types from **different imported packages**,
  that's a violation (`"cross_file_type: identifier %q scaffolded as %s in %s
  but %s in %s"`). Parse each built file with `go/parser` + `go/types` (or, for
  a cheaper first cut, `go/ast` + match the import path backing the selector
  expression in the declaration).
  - This is the scaffold-time snapshot. At scaffold time both files may
    currently agree (both `text/template`) — so this check alone would NOT have
    flagged baseline-12 at VERIFY time. Its value is (a) catching the case where
    goimports splits *within one scaffold pass* across files, and (b) as a
    post-EXECUTE guard — see below.
- **Stronger variant — DEFERRED (decision, 2026-09-05).** The idea: run the same
  cross-file identifier/type-consistency check inside `ANALYZE_EXECUTION` or as a
  mechanical precondition in `ADJUDICATE`, so a bead-introduced divergence
  against a not-yet-executed sibling stub is a *mechanical* finding naming the
  one-line fix rather than a 15-min EXECUTE timeout + ANALYZE/ADJUDICATE chain.
  Not built now, because:
  1. Part A removes the root cause — SURVEY now owns the import and pins it
     deterministically, so the scaffold-stub divergence this variant guards
     against should not arise in the first place.
  2. The ADJUDICATE-side *response* to this exact conflict class was already
     hardened (`2afb088`, `memory/project_refine_write_scope_diagnosis_fixes`):
     it full_stops cleanly with a correctly-scoped diagnosis instead of
     thrashing. The expensive-spiral symptom is already mitigated.
  3. The variant would need to hit BOTH (a) Part A failing or SURVEY pinning the
     wrong import AND (b) a later bead introducing a divergence — low joint
     probability — while adding a new mechanical precondition to the hot,
     recently-hardened ADJUDICATE/ANALYZE path, with its own false-positive
     surface against legitimately divergent beads.
  It stays a clean follow-up PR if baseline-13+ shows the VERIFY-time check 6
  isn't enough. VERIFY-time check 6 (below) is the scaffold-pass snapshot and
  ships now.
- **`certifyManifestSystemPrompt`** — add check 6 to the enumerated list.
- **Preliminary decision** (`VerifyManifest.Run`) — any check-6 violation →
  reject, same as the other five.

## Tests

- **`scaffold_go_test.go`**:
  - `buildGoFile` with an explicit `imports: ["html/template"]` on a
    `*template.Template` declaration → output imports `html/template`, not
    `text/template` (the regression test for this whole bug).
  - `buildGoFile` with `imports: nil` → identical output to today (back-compat).
  - `buildGoFile` with an import SURVEY listed but declarations don't use →
    goimports prunes it, still compiles.
  - `writeScaffoldStubsFromManifest` carries `Imports` through the rewind path.
- **new `verify_manifest_test.go` cases** (or extend the existing test file):
  - two scaffolded files declaring `var x *template.Template` backed by
    different imports → check 6 violation, preliminary decision reject.
  - same identifier, same import, in two files → pass.
  - identifier in only one file → pass.
- **`schemas_test.go`** if it asserts schema shape — update the SURVEY fixture.
- Full suite + `go vet`. Grep for other `SurveyManifestFile{` literals in tests
  and fixtures that need the new field.

## Language coupling

This whole layer is already Go-only: `scaffoldProject` errors on any non-Go
`project.Language`, `scaffold_go.go` holds the entire Go implementation behind
that switch, and `verify_manifest.go` doesn't even dispatch after the one
`scaffoldProject` call — `verifyCompile` shells `go test -c`, `checkStubPurity`
/ `verifyAPICheck` use `go/ast`. A+C extend that existing surface; they do not
add a new coupling axis. Keep the split clean:

- **Language-neutral (shared code):** `SurveyManifestFile.Imports []string`
  (`outputs.go`), the schema field (`schemas.go`), and the generic "list each
  file's external references explicitly" instruction. A per-file import/use/
  include list is a cross-language concept.
- **Go-specific (behind the switch / in the already-Go-only files):**
  `buildGoFile` rendering the block + `imports.Process`; the disambiguation
  guidance (`html/template` vs `text/template`, `math/rand/v2` vs `math/rand`)
  goes through the existing `guidance.InjectForVerb(prompt, project.Language, …)`
  per-language channel SURVEY already uses; VERIFY_MANIFEST check 6's
  `go/types` cross-file resolution sits with the other Go checks.

A future second language would need its own `scaffold_<lang>.go` + verify
checks regardless — which is already true for everything else here. The
manifest would already carry `imports` for it.

## Also consider (not blocking)

- **Bump `golang.org/x/tools`** while in here — latest goimports resolves
  `*template.Template` → `html/template`. Not a fix (a different ambiguous pair
  still breaks, and it's luck it prefers the one this fixture wants), but it
  removes one recurring source of scaffold flakiness and there's no reason to
  stay on v0.33.0.
- **CERTIFY_MANIFEST prompt** already asks the model to check "does the API
  surface match the design doc" — worth a line specifically calling out
  import-disambiguation directives, as a model-side backstop to A's mechanical one.

## Validation

After A+C land: fresh `exprvm-web-baseline-13` (new project-run conversation),
same design doc, and confirm (a) `main.go`'s scaffold stub imports
`html/template` deterministically across a couple of re-scaffolds, and (b) the
run reaches the back half of the decomposition (templates → integration) that
every prior baseline has been cut short of. That corpus is the real input to
`docs/decompose-precision-plan.md`.
