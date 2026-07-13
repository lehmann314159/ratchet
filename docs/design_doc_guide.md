# Design Document Guide

This guide is a companion to `design_doc_template.md`. The template tells you what sections
to write; this guide explains how to write each one well, what the pipeline does with it,
and where projects have gone wrong in the past.

---

## How the pipeline uses your design doc

The design doc passes through three pipeline stages, each of which reads it differently:

**SURVEY_SPEC** reads the design doc to make structural decisions: which source files to
create, what types and function signatures to declare, and how to name things. SURVEY
outputs declaration text (types, stubs, signatures) — no package statements, no imports,
no build files. A separate scaffolding step generates complete `.go` files, `go.mod`, and
`do_not_use_this_test.go` mechanically from those declarations. Your design doc's Data Types and
Behavioral Specification sections are SURVEY's primary input.

**DECOMPOSE_SPEC** reads the design doc plus the `survey.md` document that SURVEY produced.
It uses:
- **Behavioral Specification** to understand what each function does and identify natural
  decomposition boundaries
- **Cross-Bead Contracts** to populate consumer bead specs with verbatim interface text and
  to set exit criteria (smoke tests, round-trip tests, integration tests)
- **Decomposition Notes** as an authoritative override — anything written here supersedes
  the generic decomposition heuristics

**AUDIT_DECOMPOSITION** cross-checks the resulting bead list against the design doc. It
flags contract violations, missing test files, and handler beads with build-only exit
criteria.

**RECONCILE_DECOMPOSITION** applies AUDIT's findings to produce a corrected bead list.

The quality of your design doc is the single largest factor in how many attempts each bead
takes. A complete, precise doc produces a decomposition that AUDIT passes in one round with
no findings. An incomplete doc produces beads with wrong signatures, missing contracts, or
underspecified exit criteria — each a potential multi-attempt failure.

---

## Writing for small models

The design doc is typically written by a frontier model or an experienced human. The
execution model that implements each bead is smaller — often 24B–31B parameters. This
creates a systematic blind spot: **the author will not naturally see what the implementer
will get wrong**, because the author cannot make those mistakes themselves.

A frontier model writing "Group uses BFS flood fill" knows exactly what that means and
would implement it correctly on the first try. It has no felt sense that the execution
model will pre-load the starting element before the loop and then append it again on
dequeue. That error is invisible to the author because it requires thinking like a model
that knows the algorithm's name but not its pitfalls.

This is not a gap that careful re-reading will close. The author re-reading their own
description sees a correct description. The failure only becomes visible by asking a
different question: *what is the first plausible wrong implementation of this?*

**The core principle:** for every algorithm in the behavioral specification, identify the
most natural wrong implementation — the one a programmer who knows the algorithm name
would write without thinking. If that implementation would fail your tests, the spec
needs more detail. One sentence of pseudocode pinning the non-obvious choice is usually
enough.

### Specific patterns that need explicit guidance

**Algorithm initialization.** Small models know BFS/DFS/flood-fill abstractly but
frequently make the same initialization mistakes:
- Pre-loading the starting element into the result before the loop, then adding it again
  when dequeued — producing every result one element too large.
- Using a set to track "enqueued" rather than "visited" — causing elements to be processed
  multiple times if enqueued from different paths.

When a function uses BFS or flood-fill, provide pseudocode that shows initialization order
explicitly. The one load-bearing line is usually "result starts empty" or "mark visited
when dequeuing, not when enqueuing." State it directly.

**Step ordering with side-effect dependencies.** Small models implement steps in the
stated order, but may not recognize that step N depends on step N−1's side effects.
When this dependency exists and violating the order produces a wrong-but-plausible result,
name it: *"Step 6 must occur after step 5 — captures in step 5 may free liberties that
determine whether step 6 fires."*

**"Enumerate all" vs "start from one."** A function that must process all members of a
set (all empty regions, all connected components) requires an outer loop over all cells
plus a visited set that persists across flood fills. Small models frequently call the
inner function once from a single starting point. Write the outer loop explicitly in
pseudocode rather than saying "find all X."

**Loop-scoped variables.** When a variable must be re-initialized for each iteration
(a scratch copy of game state, a per-trial accumulator), small models often declare it
once before the loop. Name the required scope: *"Declare `scratch := *g` inside the
loop body, not before it — each trial must start from the original game state."*

**Don't pair a precise rule with a relative gloss for the same fact.** "Red moves
toward lower row indices (up the board)" says the same thing twice — once precisely
(a comparison the model can check) and once relatively (up/down, forward/backward,
clockwise/counterclockwise, earlier/later — meaningless without recalling which
entity it was assigned to). When two symmetric entities each get their own gloss,
a model revising one of them later can recall the gloss but misattribute it to the
other entity, reversing a rule that was originally correct while still sounding like
a direct quote from the spec. This isn't specific to board orientation — the same
risk applies to clock direction, sort order, timeline direction, or any other
bidirectional relationship. State only the checkable form of the rule and drop the
relative gloss; if it aids human readers, put it in a comment clearly separated from
the normative sentence, never juxtaposed as an alternate phrasing of it.

### How to find your blind spots

Ask yourself: *if I removed every word from this section except the function signature,
what would a programmer who knows the algorithm name write?* If the most natural
implementation of "BFS flood fill" would fail your tests, add pseudocode. If the most
natural reading of "steps 1–8" allows reordering, add an ordering constraint. The goal
is not to write an implementation — it is to close the gap between what "everyone knows"
and what the small model will actually produce.

A useful check: after writing a behavioral description, identify the one decision a
programmer would make implicitly (initialization order, step dependency, enumeration
pattern, variable scope). Write that decision down explicitly. One sentence is usually
enough.

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

**What to write (optional):** A directory listing showing which file owns which concern,
followed by explicit file-ownership rules for every function in the project.

The Architecture section is read by SURVEY to make file-placement decisions. A directory
tree with short descriptions is helpful but not sufficient: SURVEY must also know which
specific functions belong in each file. Without explicit attribution, SURVEY infers
ownership from context — and will systematically misplace functions when two files have
related concerns (e.g., templates and handlers in a web app).

**For any project with ≥ 4 source files, add explicit file-ownership rules after the
tree.** Name each function-to-file assignment, and name the wrong placement explicitly:

```
**File assignment rules (strict):**
- `main.go` contains exactly: `var game *Game` and `func main()`. Nothing else.
- `handlers.go` contains: toView helper and all HTTP handler functions
  (HandleIndex, HandlePlace, HandlePass, HandleReset).
- `templates.go` contains: InitTemplates, RenderIndex, RenderBoard. No handler
  functions. No type declarations.
- Do NOT put HandleIndex, HandlePlace, HandlePass, or HandleReset in templates.go.
- Do NOT put GameView in templates.go — it belongs in game.go.
```

The "do NOT" lines matter as much as the positive assignments. Models converge on
the same wrong placement independently across retries; naming it explicitly is the only
reliable way to prevent it.

**Also state `main.go` explicitly.** If `main.go` contains only `func main()` and one or
two package-level variables, SURVEY may generate it with empty declarations (validation
fails). List its contents explicitly even if minimal.

If you omit this section, SURVEY will still produce a reasonable file layout. Include it
when you have a strong opinion about file organization or when the project has a non-obvious
structure (e.g., multiple packages, a cmd/ subdirectory).

Note that `go.mod` and `do_not_use_this_test.go` are always generated automatically by the
scaffolding step — do not list them as SURVEY outputs or include them in the manifest.

---

## Data Types and Function Signatures

**What to write:** Complete signatures for every exported type, constant, and function.
Followed by an "Export signatures" subsection with verbatim `var _` assertion lines.

**This is SURVEY's primary precision input.** SURVEY reads these signatures to declare types
and stubs correctly. Wrong parameter types or return types here propagate to every bead that
touches those functions — the model will implement to the wrong signature and accumulate
build failures.

**Include every cross-bead symbol.** If a function or type is used by more than one bead,
it must appear here. "Obvious" types are not exempt — write `type Color int` and its
constants even if they seem self-evident.

**Include package-level variables.** If a bead exports a package-level variable that other
beads consume (e.g., `var Templates *template.Template`), include it. This prevents name
drift across beads.

**Omit `package` and `import` declarations.** SURVEY outputs declaration text only; the
scaffolder adds the package statement and computes imports automatically. Including
`package main` at the top of a code block may cause SURVEY to copy it verbatim into its
declarations output, which will then appear twice in the generated file.

**Write the Export signatures subsection.** After the signatures, write the `var _`
assertion lines in a dedicated subsection. These give SURVEY a concise, unambiguous
specification of exact return types, especially in cases where the prose might be
ambiguous (e.g., `Score() (int, int)` vs `Score() (black, white int)`). The scaffolder
generates `do_not_use_this_test.go` from the exported symbols SURVEY declares, so these
assertions also serve as a correctness check: if SURVEY declares the wrong signature, the
generated `do_not_use_this_test.go` will carry a wrong assertion and the compile check will fail.

```go
// Signatures
func NewGame() *Game
func (g *Game) PlaceStone(p Point) error
var Templates *template.Template

// Export signatures subsection
var _ func() *Game = NewGame
var _ func(*Game, Point) error = (*Game).PlaceStone
var _ *template.Template = Templates
```

---

## Behavioral Specification

**What to write:** One or two sentences per function explaining what it does — the
behavioral contract, not the implementation.

This section bridges the gap between type signatures (which SURVEY reads) and cross-bead
contracts (which describe inter-bead dependencies). It captures domain logic that isn't
visible from the signature alone:

- `FindFlips` is pure read — it computes without modifying state
- `PlaceStone` composes `FindFlips` and applies its result
- `CheckWinner` depends on `ConsecutivePasses`, not just stone counts
- `Pass` increments `ConsecutivePasses` and switches `Turn`

**Why this matters for DECOMPOSE.** DECOMPOSE reads behavioral descriptions to identify
natural decomposition boundaries. Functions that share a dependency or build on each other
(e.g., `FindFlips` → `ValidMoves` and `PlaceStone`) are candidates for separate beads
with an integration bead to verify the composition. If behavioral descriptions are absent,
DECOMPOSE may collapse related functions into a single oversized bead.

**Signal natural seams explicitly.** If a group of functions forms a dependency chain or
represents a distinct independently-testable concern, say so:

> "The game functions form a dependency chain: `FindFlips` is foundational to both
> `ValidMoves` and `PlaceStone`. Each functional group — board initialization, flip
> computation, move application, and game-state evaluation — is independently testable
> and should be treated as a separate unit of work."

This is a statement about the domain structure, not a decomposition plan. DECOMPOSE
translates it into bead boundaries using its own judgment.

**Also use this section to resolve implementation ambiguities.** If the design doc leaves
something that SURVEY or DECOMPOSE would have to guess — template storage (inline strings
vs external files), JSON field names, error return conventions — state it here as a WHAT,
not a HOW:

> "Templates are defined as Go string literals inside `InitTemplates()` — no external
> `.html` files."

---

## Domain-Specific Test Scenarios

**When this section applies:** any bead that tests correctness in a domain with
non-obvious geometry — game boards, coordinate systems, spatial algorithms, image
processing, matrix operations. If test positions can be described as raw array indices
that look equally plausible whether correct or not, this section is needed.

**The core problem.** REFINE_TESTS_WRITE knows domain rules abstractly but cannot
reliably self-verify that a specific position satisfies them. A model that knows "a
bishop moves diagonally" will still write `Board[0][2]` → `Board[7][7]` (c1→h8,
Δrank=7, Δfile=5) without noticing the files differ by 5 not 7. The error is invisible
as raw indices and the implementation is blamed for five execution attempts before anyone
looks at the test. The design doc author — human or Claude — can check this arithmetic
once; encode it so the pipeline doesn't have to rediscover it.

**What to write:** For each bead that tests geometric or positional correctness, add a
"Required test scenarios" block. Specify exact positions in domain-legible notation,
include the arithmetic verification, and name the specific wrong position the model is
likely to produce.

```
**Required test scenarios for [bead]:**

[Scenario name]: [piece/element] at [domain notation] ([raw coords]).
[Expected target] at [domain notation] ([raw coords]): Δrow=[N], Δcol=[N] ✓.
Do NOT use [wrong target]: Δrow=[N], Δcol=[M] — [why it's wrong].
```

**Use domain-legible notation as the canonical form.** Write algebraic squares ("a3",
"f8"), pixel coordinates ("(0,0)", "(255,127)"), or named positions rather than raw
indices. The purpose is that a geometry error becomes self-evident in the notation —
"bishop at c1 can reach h6, not h8" is immediately checkable; `Board[0][2]` → `Board[7][7]`
is not.

**Name the wrong answer explicitly.** Models converge on plausible-sounding wrong
positions independently. Stating "do NOT use c1→h8: Δfile=5≠Δrank=7, not a diagonal"
prevents that specific failure more reliably than a correct example alone. The wrong
answer belongs in the design doc alongside the right one.

**Where this flows.** DECOMPOSE reads the full design doc including these blocks and
propagates them into each bead spec. REFINE_TESTS_WRITE then reads the bead spec as its
primary input. A required-scenarios block written here will reach the test-writing model
directly. The pipeline cannot compensate for scenarios left out; it can only follow
scenarios provided.

**Coordinate system mapping.** If the domain uses 0-indexed coordinates that differ from
the conventional human notation (rank 0 = rank 1 in chess, row 0 = top in image
processing), include an explicit mapping table in this section or in the Overview. Do not
assume the model will derive the mapping from the struct field names alone.

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
in any type signature, so it won't be caught by `do_not_use_this_test.go`, and a unit test for
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
  responding. If RandomAIMove returns passed=true (AI has no legal moves), call g.Pass()
  instead — omitting this leaves ConsecutivePasses stuck and the game never ends.
  If the game is already over (CheckWinner != Empty), skip the AI call.
```

Without this contract, the handler bead spec says nothing about calling `RandomAIMove`,
the smoke test doesn't verify it, and the function is implemented in isolation and never
wired in. The app compiles and all tests pass; the feature is simply absent.

**Protocol contract completeness:** A protocol contract is only complete if it specifies
what to do for every return value of the called function — not just the success case.
For any function that can return a "no action" result (pass, error, game over, empty
result), the contract notes must state the handler-side obligation for that case. The
AI-pass case (`passed=true`) is the canonical example: the handler must call `g.Pass()`,
not just skip placing a stone.

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

**What to write:** Targeted overrides when DECOMPOSE's generic heuristics would produce
wrong bead boundaries for this specific project.

**Start without this section.** DECOMPOSE has strong built-in heuristics: 200-line cap,
independence requirement, paired-behavior detection, integration bead generation, and
httptest requirement for handler beads. It also reads the behavioral specification and
cross-bead contracts to understand the project structure. For most projects, this is
sufficient.

**Add guidance only when you know something DECOMPOSE can't infer.** The right signal is
a specific wrong choice you've seen or can predict — not a desire to control the output.
Good uses:
- Specifying one bounded scenario for an integration bead (DECOMPOSE may over-scope it)
- Calling out a per-bead constraint that prevents a common mistake for this project type
  (e.g., "handlers bead must not define HTML templates inline")
- Sequencing two beads that share a file and have a non-obvious dependency order

**Avoid pre-writing the full bead table.** A complete bead table makes DECOMPOSE redundant
and removes its ability to apply judgment. If the table is wrong (even slightly), AUDIT
will flag it and RECONCILE will need to fix it — at the cost of a full extra round. Let
DECOMPOSE make structural decisions, then add guidance only where it guesses wrong.

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
- [ ] Template storage form stated explicitly (inline strings vs external files)
- [ ] Integration bead tests one user flow end-to-end (not "all routes")

**HTMX fragment scope:** For projects using HTMX fragment updates, all user-visible
state that changes after a move — score, turn indicator, game-over message — must
render inside the HTMX swap target. The fragment template must be self-contained.
Never place dynamic state in elements outside the swap target. The failure mode is
silent: the page loads correctly, but scores and turn indicators stop updating after
the first move. Specify the swap target in the cross-bead contract notes.

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
| `package` declaration in Data Types code block | SURVEY copies it into declarations; scaffolder writes it twice, compile fails | Omit `package` and `import` from signatures — scaffolder adds them |
| Behavioral structure omitted for functions with dependency chain | DECOMPOSE collapses all functions into one oversized bead | Add a sentence noting the dependency chain and that each group is independently testable |
| Template storage form unstated | Model invents external `.html` files or inline strings — whichever wasn't intended | State "inline strings in InitTemplates()" or "external .html files" explicitly |
| Protocol contract omitted | Handler doesn't call logic function; no test catches it | Ask "what functions from other beads does each handler call?" |
| Integration bead over-scoped | Long test generation hits budget before write_file; missing-path error | One bounded scenario per integration bead; split if needed |
| Handler bead with `go build` exit criterion | Model writes a stub that compiles; runtime behavior untested | Require httptest smoke test for every handler bead |
| FuncMap helper used in template but not registered | Runtime panic; invisible to `go build` | List required helpers in data-shape contract notes |
| `$.X` vs `.X` inside `{{range}}` | Wrong value rendered; invisible to `go build` | Note "$-prefix required inside {{range}}" in data-shape contract |
| Shared file not sequenced | Later bead overwrites earlier bead's content | Note sequential dependency explicitly in Decomposition Notes |
| Behavioral wiring left implicit | Feature absent at runtime; all tests pass | Write a protocol contract for every cross-bead function call in a handler |
| Protocol contract covers success only | "No action" branch (AI pass, game over) missing; state machine breaks silently | Contract notes must specify handler obligation for every return variant |
| HTMX swap target too narrow | Score, turn, game-over outside swap target never update after moves | All dynamic state inside swap target; state this in the data-shape contract notes |
| Template bead after handler bead | Handler httptest assertions get stub template output; tests fail or produce vacuous passes | Order the template bead before the handler bead; a cycle in this ordering (each bead's tests require the other's real behavior) reveals a boundary problem — merge or narrow test scope |
| Test positions for domain geometry left to model | Correct implementation blamed for failing tests (wrong expected values invisible as raw indices); bead retried until budget exhausted | Write required test scenarios with domain-legible notation and Δrow/Δcol verification; see Domain-Specific Test Scenarios section |
| Correct example without naming the wrong answer | Model independently converges on the same plausible wrong position across retries | Name the specific wrong position and explain why it fails (e.g., "c1→h8: Δfile=5≠Δrank=7, not a diagonal") |
| Coordinate index mapping left implicit | Model applies wrong 0-vs-1 indexing or swaps row/column; errors surface as off-by-one test failures | Include an explicit mapping table (e.g., "rank 0 = rank 1 in algebraic notation, file 0 = file a") |
| Architecture file tree without explicit function attribution | SURVEY places functions in wrong files (handlers in templates.go, types in wrong module); CERTIFY loops requesting corrections | After the directory tree, add explicit file-ownership rules naming each function and its file; include "do NOT put X in Y" for likely wrong placements |
| `main.go` contents unspecified | SURVEY generates `main.go` with empty declarations; validation fails with "missing declarations" error | Always list `main.go` contents explicitly, even if minimal (e.g., "var game *Game and func main() only") |
| BFS/flood-fill described abstractly | Model pre-loads starting element before loop then appends it again on dequeue; every group/region is one element too large | Provide pseudocode with explicit "result starts empty; add when dequeuing"; see "Writing for small models" section |
| Step ordering dependency unstated | Model implements steps in listed order but checks suicide before captures, incorrectly rejecting legal capture moves | When a step's correctness depends on a prior step's side effects, add: "must occur after step N — [reason]" |
| "Find all regions" described as a single flood fill | Model calls flood-fill once from one starting point; only one region found; rest of board misclassified | Write the outer loop explicitly: "iterate every cell; for each unvisited empty cell, start a new flood fill" |
| Loop-scoped scratch copy declared before the loop | Model reuses a modified scratch copy across trials; later trials see side effects of earlier ones | Name the required scope: "declare `scratch := *g` inside the loop body, not before it" |