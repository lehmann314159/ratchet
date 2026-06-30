# Design Document Guide

This guide is a companion to `design_doc_template.md`. The template tells you what sections
to write; this guide explains how to write each one well, what the pipeline does with it,
and where projects have gone wrong in the past.

---

## How the pipeline uses your design doc

`DECOMPOSE_SPEC` reads the entire document and produces a bead list. It uses:

- **Data Types and Function Signatures** to generate the `var _` assertions in the layout
  bead's `api_check_test.go`
- **Cross-Bead Contracts** to populate consumer bead specs with verbatim interface text and
  to set exit criteria (smoke tests, round-trip tests, integration tests)
- **Decomposition Notes** as an authoritative override — anything written here supersedes
  the generic decomposition heuristics

`AUDIT_DECOMPOSITION` cross-checks the resulting bead list against the design doc. It flags
contract violations, missing test files, and handler beads with build-only exit criteria.

`RECONCILE_DECOMPOSITION` applies AUDIT's findings to produce a corrected bead list.

The quality of your design doc is the single largest factor in how many attempts each bead
takes. A complete, precise doc produces a decomposition that AUDIT passes in one round with
no findings. An incomplete doc produces beads with wrong signatures, missing contracts, or
underspecified exit criteria — each a potential multi-attempt failure.

---

## Overview

**What to write:** One paragraph covering what the project does, who uses it, the runtime
model (CLI, server, library), and what is explicitly out of scope.

**State domain parameters explicitly.** Models fill unstated parameters with plausible
defaults, which may not match your intent:

- A Go game without a stated board size will likely be implemented as 19×19 (the standard),
  even if you want 9×9.
- An image library without a stated color depth may default to 8-bit per channel.
- A calendar app without a stated timezone handling policy will invent one.

If there is a parameter the model could reasonably guess — board dimensions, file size
limits, date ranges, auth requirements — state it here.

**State what is out of scope.** This prevents the model from adding features you didn't ask
for. "AI opponent is out of scope" or "authentication is out of scope for this project" are
load-bearing sentences.

---

## Architecture

**What to write:** Directory layout with one-line descriptions of each file's responsibility.

**Be explicit about file ownership.** The architecture section drives `output_files`
assignments during decomposition. If `game.go` is listed as "core game logic," every bead
that modifies game logic will list `game.go` in its `output_files`. If a file's purpose is
ambiguous, two beads may fight over it.

**Note shared files.** If more than one bead writes to the same file (e.g., a `game.go`
that accumulates functions across three beads), note the dependency explicitly here or in
Decomposition Notes. The pipeline does not automatically sequence writes to shared files.

**Keep it high-level.** List files and concerns; don't explain implementation. The model
will infer implementation from the signatures section.

---

## Data Types and Function Signatures

**What to write:** Complete Go signatures for every exported type, constant, and function.
Followed by a subsection with the verbatim `var _` assertion lines.

**This is the highest-precision section.** DECOMPOSE uses these signatures to generate the
`api_check_test.go` compile-time assertions in the layout bead. Wrong parameter types or
return types here propagate to every bead that touches those functions — the model will
implement to the wrong signature and accumulate build failures.

**Include every cross-bead symbol.** If a function or type is used by more than one bead,
it must appear here. "Obvious" types are not exempt — write `type Color int` and its
constants even if they seem self-evident.

**Include package-level variables.** If a bead exports a package-level variable that other
beads consume (e.g., `var Templates *template.Template`), include a `var _ Type = varName`
assertion. This prevents name drift across beads.

**Write the assertions subsection.** After the signatures, copy the `var _` lines into a
dedicated subsection labeled "Compile-time assertions for api_check_test.go." DECOMPOSE
includes these verbatim in the layout bead's `full_text`. If you skip this step, the model
may invent its own assertion format.

```go
// Signatures
func NewGame() *Game
func (g *Game) PlaceStone(p Point) error
var Templates *template.Template

// Assertions subsection
var _ func() *Game = NewGame
var _ func(*Game, Point) error = (*Game).PlaceStone
var _ *template.Template = Templates
```

---

## Cross-Bead Contracts

**What to write:** Every interface produced by one bead and consumed by another.

This is the most commonly incomplete section, and incomplete entries here cause the most
subtle post-pipeline failures — bugs that `go build ./...` and all unit tests pass, but
the app doesn't behave correctly at runtime.

### The four contract types

#### data-shape

A struct or field set passed from one bead to another. Most common in web apps where a
handler constructs a view model and a template renders it.

Write the exact struct definition in the `interface` field. In `notes`, include:

- Any FuncMap helper functions the template must register before parsing (e.g.,
  `seq(start, end int) []int`). A helper used in a template but not registered in the
  FuncMap causes a runtime panic invisible to `go build`.
- Any variable scoping rules. In Go templates, inside a `{{range}}` loop, `.` is the
  loop element — top-level fields must be accessed via `$` (e.g., `$.Board`, `$.Selected`).
  State this if any template iterates and accesses root data.

#### format

A serialization format shared by a writer and a reader. PNG encoding/decoding is the
canonical example. The individual beads test in isolation; the integration bead tests the
round-trip.

The `interface` field should describe the binary or text format precisely: field order,
endianness, magic bytes, length encoding. If the format has a version or extension
mechanism, state it here.

#### protocol

A request/response exchange where one bead initiates and another responds. This type is
the **highest-risk gap for web applications.**

A protocol contract is needed any time a handler bead calls a function from a logic bead
in response to a user action. The function call is behavioral wiring — it doesn't appear
in any type signature, so it won't be caught by `api_check_test.go`, and a unit test for
the handler alone won't catch a missing call.

**The question to ask for every handler bead:** "What functions from other beads does this
handler call, and under what conditions?" Each answer is a protocol contract.

Example — an HTTP handler that auto-plays an AI move after a human placement:

```
- type: protocol
- producer: ai
- consumer: http-handlers
- interface: RandomAIMove(g *Game) (Point, bool, error)
- notes: handlePlace must call RandomAIMove after a successful PlaceStone and before
  responding. If RandomAIMove returns pass=true (AI has no legal moves), call g.Pass()
  instead — omitting this leaves ConsecutivePasses stuck and the game never ends.
  If the game is already over (CheckWinner != Empty), skip the AI call.
  TestHandlerSmoke must verify two branches:
  (1) Happy path: after POST /place, the AI places a stone (white score increases).
  (2) AI-pass branch: set up a game state where White has no valid moves; call the
  handler; verify ConsecutivePasses incremented (not just that no stone was placed).
```

Without this contract, the handler bead spec says nothing about calling `RandomAIMove`,
the smoke test doesn't verify it, and the function is implemented in isolation and never
wired in. The app compiles and all tests pass; the feature is simply absent.

**Protocol contract completeness:** A protocol contract is only complete if it specifies
what to do for every return value of the called function — not just the success case.
For any function that can return a "no action" result (pass, error, game over, empty
result), the contract notes must state the handler-side obligation for that case, and
the smoke test must exercise it. The AI-pass case (`passed=true`) is the canonical
example: the handler must call `g.Pass()`, not just skip placing a stone.

Protocol contracts are also relevant outside web apps: a CLI command that formats output
using a library function, or a background worker that calls a queue consumer, both require
a protocol contract to ensure the call is specified, implemented, and tested.

#### schema

A validation schema (JSON Schema, protobuf definition, SQL DDL). Requires tests against
both a valid and an invalid document in the producing bead's exit criteria.

### What AUDIT checks

AUDIT verifies that every contract has a consumer bead spec that quotes the interface
verbatim, and that exit criteria exercise the contract (render test for data-shape,
round-trip test for format, joint exchange test for protocol). Missing or paraphrased
interface text in a consumer bead spec is a finding.

---

## Decomposition Notes

**What to write:** Authoritative overrides to the generic decomposition heuristics.

**"Too good" decomposition is a feature.** If you know exactly what the bead list should
be, write it in the bead table. A design doc that fully specifies the bead structure will
produce a DECOMPOSE output that AUDIT passes with no findings. This is not cheating — it is
good specification.

### Integration bead scope

Integration beads are the most common source of multi-attempt failures, because the model
must generate a large test function and write it to disk before the budget expires.

**Write one bounded scenario.** Specify fixed inputs and one asserted output. Do not write
a coverage goal.

| Instead of | Write |
|---|---|
| "Test capture, ko, and scoring in a full game" | "Start a game, place four stones to surround one white stone, verify that stone is removed from the board" |
| "Verify the full encode/decode pipeline" | "Encode a 2×2 PNG with one red pixel; decode it; verify pixel (0,0) is red" |
| "Test all CRUD operations" | "Create one event with title 'Meeting'; retrieve it by ID; verify title matches" |

If your integration coverage needs are high, use multiple integration beads with distinct
bounded scenarios rather than one large one. Each bead is one attempt budget; splitting
reduces per-bead generation size and improves reliability.

### Handler beads

Handler beads must have a runtime smoke-test exit criterion. `go build ./...` alone is
explicitly prohibited — it cannot catch template execution errors, missing route
registrations, wrong response codes, or absent behavioral wiring.

The smoke test must:
1. Use `net/http/httptest` — never start the binary on a fixed port
2. Exercise at least one complete request/response cycle per handler
3. Verify a structural property of the response (element count, status code, key string)
4. Verify any downstream effects required by protocol contracts (e.g., AI stone in response)

### Shared files

If two beads write to the same file, the bead that writes first must be listed first in
the bead table, and the second bead's spec must acknowledge the existing content. Add a
note in Bead Boundaries:

> "Bead 3 (scoring) writes `game.go`. Bead 4 (placement) also writes `game.go` and must
> preserve all content from bead 3."

### Per-bead constraints

Use Bead Boundaries to prohibit behaviors the model would otherwise adopt:

- "The http-handlers bead must not define any HTML templates inline. All template content
  belongs in the templates bead." (Without this, handlers bead will create an inline
  `templates.go` and the templates bead becomes redundant.)
- "The encoding bead must not import the decoding bead's package." (Prevents accidental
  circular dependency.)

---

## Project-type orientation

### Library

A library project has no HTTP handlers, no view models, and no runtime server. The dominant
contract type is **format**.

**Checklist:**
- [ ] Format contract for every encode/decode or marshal/unmarshal pair
- [ ] Integration bead for each format contract with a round-trip exit criterion
- [ ] Signatures section covers every exported symbol (types, functions, constants, errors)
- [ ] Protocol and data-shape sections not applicable — omit them

**Common pitfall:** Skipping the integration bead when encode and decode are separate beads.
The unit tests pass for each in isolation; the round-trip test is the only thing that
catches a format mismatch between them.

### Web application

A web application has handlers, view models, and templates. The dominant contract types are
**data-shape** and **protocol**.

**Checklist:**
- [ ] Data-shape contract for every struct passed from a handler to a template
- [ ] Protocol contract for every handler that calls a function from another bead
- [ ] Notes on every FuncMap helper required by templates
- [ ] Notes on `$`-prefix scoping for any template that iterates and accesses root data
- [ ] Every handler bead has an httptest smoke test exit criterion
- [ ] HTMX swap target contains all dynamic state (score, turn, game-over message)
- [ ] Integration bead tests one user flow end-to-end (not "all routes")

**HTMX fragment scope:** For projects using HTMX fragment updates, all user-visible
state that changes after a move — score, turn indicator, game-over message — must
render inside the HTMX swap target. The fragment template must be self-contained.
Never place dynamic state in elements outside the swap target. The failure mode is
silent: the page loads correctly, but scores and turn indicators stop updating after
the first move. Specify the swap target explicitly in the handler bead spec and require
the smoke test to verify that the response fragment includes score and turn fields.

**Common pitfall:** Protocol contracts omitted. The handler and the logic function are both
implemented correctly in isolation; the wiring between them is never specified; the feature
is absent at runtime. Ask "what functions from other beads does each handler call?" for
every handler bead.

### CLI

A CLI project calls library functions and formats output to stdout. The dominant contract
type is **protocol**, but the exchange is command → library function → stdout rather than
HTTP request → handler → response.

**Checklist:**
- [ ] Protocol contract for each command that calls a library function
- [ ] Output format specified precisely in the contract interface (field order, delimiters,
  units)
- [ ] Integration bead invokes the compiled binary and asserts on stdout content
- [ ] httptest not applicable — omit handler guidance

**Common pitfall:** Output format underspecified. The model invents a format that is
plausible but not what you intended. If the output will be parsed by another tool, or
displayed in a specific way, write the exact format in the protocol contract.

---

## Common mistakes

| Mistake | What fails | How to prevent |
|---|---|---|
| Domain parameters unstated | Model uses plausible defaults (wrong size, wrong limits, wrong constants) | State every parameter explicitly in Overview |
| Protocol contract omitted | Handler doesn't call logic function; no test catches it | Ask "what functions from other beads does each handler call?" |
| Integration bead over-scoped | Long test generation hits budget before write_file; missing-path error | One bounded scenario per integration bead; split if needed |
| Handler bead with `go build` exit criterion | Model writes a stub that compiles; runtime behavior untested | Require httptest smoke test for every handler bead |
| FuncMap helper used in template but not registered | Runtime panic; invisible to `go build` | List required helpers in data-shape contract notes |
| `$.X` vs `.X` inside `{{range}}` | Wrong value rendered; invisible to `go build` | Note "$-prefix required inside {{range}}" in data-shape contract |
| Shared file not sequenced in Decomposition Notes | Later bead overwrites earlier bead's content | List every shared file and which bead writes first |
| Behavioral wiring left implicit | Feature absent at runtime; all tests pass | Write a protocol contract for every cross-bead function call in a handler |
| Protocol contract covers success only | "No action" branch (AI pass, game over) missing; state machine breaks silently | Contract notes must specify handler obligation for every return variant; smoke test must exercise each |
| HTMX swap target too narrow | Score, turn, game-over outside swap target never update after moves | All dynamic state inside swap target; fragment template must be self-contained |
| `package main` omitted from source files | `go build ./...` succeeds but produces ar archive (library), not executable | All `.go` files must declare `package main`; module name in `go.mod` does not determine package name |
| `var _` assertions omitted | Layout bead invents its own assertions; signature drift across beads | Write assertions subsection; include package-level variables |
| Per-bead constraints omitted | Handlers bead creates inline templates; templates bead becomes redundant | Add "must not embed HTML inline" to handler bead constraints |