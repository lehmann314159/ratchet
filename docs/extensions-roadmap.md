# Extending ratchet beyond from-scratch pipelines

Started 2026-07-18, after the design-doc rewrite pass on the five stress-test fixtures
(`docs/stress-test-roadmap.md`) and a live demo of the fractal-smoke-2 output. Motivation:
every capability ratchet has today assumes a fresh design doc describing a from-scratch
build. The next class of work is teaching it to build *on top of* prior projects instead
of only building new ones from nothing — reuse another project's code, modify a
project that's already complete, or fix a defect in one. This doc captures the concrete
cases considered, the shared primitives extracted from them, and the sequencing decision.

Do not implement anything here without a fresh proposal — this is a planning document,
not an approved design. Per [[feedback_propose_before_apply]], each phase below still
needs its own concrete design pass before code changes land, the same way
`docs/stress-test-roadmap.md` Phase C already calls for.

---

## The five concrete cases

1. **Modify functionality in an existing codebase.** Change chess's text-based board
   and input to a graphical, click-to-move UI, on top of an already-COMPLETE chess
   project.
2. **Fix a reported defect in an existing codebase.** Given a symptom ("castling
   queenside gives the wrong result"), diagnose and repair it — no design doc, no
   behavioral spec to expand.
3. **Reuse an existing codebase as a library/import.** A new project's own code calls
   directly into another project's exported functions (e.g. wrapping the fractal
   library or a set of http-handlers in a new project).
4. **Reuse an existing codebase as an external executable dependency.** A new project
   shells out to another project's *compiled binary* rather than linking its code
   in-process (e.g. the fractal library as a PNG-generating engine behind an HTTP
   server).
5. **Sequential composition via a published interface.** Write a backend project,
   then write a frontend project against the backend's own generated API docs. Not
   really a new case — included as a test of whether the others' generalized
   mechanism also covers it.

## What each case actually needs

Verified before writing this down, not assumed:

- **Case 5 already works today, unmodified.** Confirmed by reading the actual verb
  code (`internal/verbs/decompose_spec.go`, `audit_decomposition.go`): Cross-Bead
  Contract `producer`/`consumer` fields are pure free-text prose. No code anywhere
  parses or validates them against a bead-title/ID registry — DECOMPOSE reads the
  whole design doc as a text blob, AUDIT is prompted to cross-check bead content
  against that prose, but it's LLM judgment, never a structural lookup. Nothing stops
  a frontend project's design doc from citing a backend project's published API docs
  as a contract's producer.

- **Case 1** needs a way to *resurvey real code* instead of SURVEY_SPEC authoring from
  nothing, a delta design doc describing only the intended change, and a DECOMPOSE
  variant scoped to changed files only. This is `docs/stress-test-roadmap.md` Phase
  C's original framing, unchanged.

- **Case 3** needs one of two alternatives:
  - **(a) Copy pre-vetted source files into the new project's own package.** Simpler —
    both projects already use `package main`, so files copied verbatim need no
    rename, no import statement, no `go.mod` changes at all; they just compile as
    part of the same package as the new handlers/templates/main.go. Requires the
    **unowned-files primitive** below.
  - **(b) True Go module dependency** (`go.mod` `replace` directive). Requires
    renaming the dependency's package away from `main` (Go cannot import a `main`
    package — a language rule, not a ratchet limitation) and teaching SURVEY_SPEC to
    recognize externally-supplied symbols it must not scaffold. Only worth it if the
    same library needs reuse across *multiple* future consumer projects; copy-in
    doesn't scale to that without manual re-copying on every upstream change.

  **Candidate consumer projects for Case 3** (build once the unowned-files
  primitive lands — not before):
  - [ ] fractal library + HTTP wrapper — the original motivating example, a web
        server exposing `GenerateMandelbrot`/`GenerateJulia`/`GenerateSierpinski`.
  - [ ] kafka-sim + visualization partner (added 2026-07-18) — a project that
        leverages the `kafkasim` library to run a live simulation and serves a
        real-time view of it, in the same spirit as the one-off hand-built demo
        artifact (swimlanes per partition, routing by key hash, consumer offset
        progress) but as an actual generated ratchet project, not a scratch script.
        This is deliberately the **second** independent consumer of the
        leverage-by-copy mechanism — see the Case 3a/3b open question below, which
        was explicitly waiting on a second real example before deciding anything.

- **Case 4** needs the dependency project to have an actual CLI entry point first —
  fractal-smoke-2 has no `func main()` today, so *adding one* is itself a Case-1-shaped
  "modify an existing project" problem. Once the dependency is invocable, Case 4 also
  needs a new Cross-Bead Contract flavor for an external-process contract (args, exit
  codes, stdout/file format) and a build-time step ensuring the binary exists before
  the consuming project's beads run.

- **Case 2 does not reduce to decomposition at all.** There is no written behavioral
  spec to expand into beads — the only artifact is a symptom description. It needs
  something upstream of DECOMPOSE that doesn't exist in any form today: reproduce →
  localize → hypothesize root cause → scope a fix. Only the final "patch + verify"
  step resembles EXECUTE_BEAD, and only once a precise, localized, bead-shaped fix
  spec (`output_files` + `exit_criteria`) already exists.

## The shared primitives

Two mechanisms recur across the four cases that need new work (1, 3a, 4's CLI-adding
sub-problem):

**(a) Unowned files.** The pipeline has an unstated invariant — every file in a
project folder belongs to exactly one bead — enforced independently in three
separate places, found by reading the actual code, not assumed:
- `checkNoBehavioralTests` (`internal/verbs/certify_manifest.go:175-196`) walks the
  entire folder for stray `_test.go` files, not scoped to the manifest.
- `wipeGoProject` (`internal/verbs/scaffold_go.go:271-282`), fired on any
  CERTIFY_MANIFEST rejection, unconditionally deletes every `.go`/`go.mod`/`go.sum`
  file in the folder — a normal reject-and-resurvey cycle would destroy pre-existing
  library files, not just a rare edge case.
- `checkUndeclaredFiles` (`internal/verbs/analyze_execution.go:237-294`) flags
  anything on disk absent from every bead's `output_files`, forever, with no
  allowlist mechanism anywhere in the schema today.

  The fix is one canonical "files this project run doesn't own" concept that all
  three consult, mirroring `fixtureScopedTables` in `internal/project/fixture.go` —
  a single named source of truth other code paths reference, not three independent
  ad hoc checks that can silently drift out of sync with each other.

  This primitive serves **both** leverage (Case 3a: files copied in from elsewhere)
  and modify-in-place (Case 1: files already in this project's own folder from a
  prior, already-succeeded bead) — the downstream mechanical handling is identical
  either way; only the provenance of the files differs.

**(b) Resurvey-from-real-code + delta decomposition.** Needed directly by Case 1, and
as a prerequisite for Case 4's "give the dependency a CLI" sub-problem. Not yet
designed in any detail — this is the bigger of the two lifts and needs its own
dedicated design pass before code changes, per Phase C's original caution against
assuming a shape ahead of that pass.

## Sequencing decision

Agreed order:

1. **Unowned-files primitive** — smallest, already scoped above, serves two cases.
2. **Resurvey + delta decomposition** — bigger, but load-bearing for both Case 1 and
   Case 4.
3. **Case 2 (bug-fix), evaluated empirically, not assumed to need a separate app.**

On the "separate app" question specifically: rejected, in favor of keeping the
orchestrator/model-fleet/trace-logging/UI/DB substrate shared and only letting the
*pipeline type* (the verb sequence, the shape of a "unit of work") differ per case.
Reasoning: that substrate is real accumulated value, not incidental scaffolding — this
session alone paid down a long list of substrate-level bugs (RECONCILE's escalation
tie-break, no-write false positives, budget-merge bugs, orchestrator queue-blocking)
that a forked harness would have to rediscover independently over time, with no
guarantee a fix in one ever reaches the other. That's the bit-rot risk named when this
was raised, and it's judged to outweigh the complexity saved by not forcing Case 2
into ratchet's existing verb/schema conventions.

The reframe that makes this tractable: Case 2's only genuinely novel piece is the
*front end* — turning a symptom ("castling queenside gives the wrong result") into a
localized, scoped fix. Once that localization exists, it is shaped exactly like a
bead (`output_files` + `exit_criteria`), and the existing
EXECUTE_BEAD → REFINE_TESTS → ADJUDICATE machinery may just work on it unchanged. So
the new capability is plausibly a diagnostic verb chain (something like
DIAGNOSE_DEFECT → LOCALIZE) that terminates by handing off exactly where
DECOMPOSE_SPEC would, not a parallel app with its own queue and schema.

**This reframe is not yet verified.** Before committing to it, prototype the
diagnostic front end against one real case — the chess castling-queenside bug is a
good first test, since its actual root cause is already known from this project's own
history — and check empirically whether it cleanly bottoms out at a normal bead, or
whether the iteration pattern genuinely fights the existing verb/job conventions. If
it fights hard, that's real evidence for reconsidering the separate-app option; if it
doesn't, the duplication was correctly avoided.

## Open questions / not yet decided

- Case 3a vs. 3b (copy-in vs. true module dependency): no decision yet. The
  fractal-http and kafka-sim-visualization candidates above give two independent
  consumers to compare once both exist, rather than deciding off the fractal
  example alone — per the "don't design off one example" caution already applied
  to the unowned-files primitive. Still don't decide until both are actually built.
- Where the unowned-files list actually lives (new `projects` column vs. a separate
  table) — not designed yet, deliberately deferred to its own proposal.
- The exact shape of "resurvey real code" (a new entry verb vs. a SURVEY_SPEC mode
  flag) — explicitly not decided ahead of a dedicated design pass, per
  `docs/stress-test-roadmap.md` Phase C's original caution.

---

**How to apply**: when resuming this thread, update this doc directly rather than
re-deriving the case taxonomy from scratch — same convention as
`docs/stress-test-roadmap.md`. `[[project_roadmap]]` memory should carry only a
one-line pointer here.
