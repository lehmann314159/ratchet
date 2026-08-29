# <Project Name> — Design Document

> Skeleton only. Read `docs/design_doc_guide.md` for what each section is for, how the
> pipeline consumes it, and the failure modes each one prevents. Section order and
> headings here are load-bearing: `cmd/checkdesigndoc` parses this document by `##`
> heading, and SURVEY/DECOMPOSE read specific sections by name.
>
> Sections marked *(conditional)* are omitted entirely when they don't apply — do not
> leave an empty heading.

## Overview

One paragraph: what the project does, who uses it, the runtime model (CLI, server,
library), and what is explicitly **out of scope** (load-bearing — it stops the model
adding unrequested features).

**Domain parameters:** list every parameter a model could reasonably guess — board
size, color depth, constants, size/date limits, auth requirements — and state each one
explicitly. An unstated parameter gets a plausible default that may not be yours.

## Architecture

*(Conditional — include when you have a specific opinion about file layout within the
single flat package, or for any project with ≥ 4 source files.)*

A directory listing showing which file owns which concern, followed by **explicit
file-ownership rules** naming every function's file — including the "do NOT put X in Y"
lines for the placements models get wrong.

Constraints that always hold (state them if this section exists):

- Single flat package. No subdirectories — `CERTIFY_MANIFEST` rejects any source file
  in a subdirectory.
- All `.go` files use `package main` unless this doc says otherwise; module name is
  `<modulename>`.
- `main.go`'s contents are listed explicitly even if minimal (e.g. "`var game *Game`
  and `func main()` only") — otherwise SURVEY may generate it with empty declarations
  and validation fails.
- `go.mod` and `do_not_use_this_test.go` are generated automatically — never list them
  as SURVEY outputs.

## Data Types and Function Signatures

Complete signatures for every type, constant, function, and package-level variable that
any bead touches — exported *or* unexported if a test calls it directly. This is
SURVEY's primary precision input; a wrong type here propagates to every bead.

Do **not** include a `package` declaration or `import` block — the scaffolder adds
those. Starting the code block with `package main` causes it to be written twice.

```go
type Foo struct {
    Bar string
    Baz int
}

func NewFoo(bar string) *Foo
func (f *Foo) Process(input []byte) ([]byte, error)

var State *Foo
```

### Export signatures

Verbatim `var _` assertion lines for every exported symbol — an unambiguous
specification of exact return types, and a correctness check against the generated
`do_not_use_this_test.go`.

```go
var _ func(bar string) *Foo = NewFoo
var _ func(*Foo, []byte) ([]byte, error) = (*Foo).Process
var _ *Foo = State
```

## Behavioral Specification

One or two sentences per function or functional group describing WHAT it does — the
contract, not the implementation. Capture what the signatures can't show:

- Which functions are pure reads vs. state mutators.
- How functions compose ("`PlaceStone` applies the result of `FindFlips`").
- What conditions trigger state changes ("`ConsecutivePasses` resets on `PlaceStone`").
- Which functions form a dependency chain, and that each group is independently
  testable — DECOMPOSE uses this to find bead seams.
- Resolutions for ambiguities SURVEY/DECOMPOSE would otherwise guess ("templates are
  inline Go string literals in `InitTemplates()` — no external `.html` files").

**Example:**

**`NewFoo(bar string) *Foo`** — initializes Foo with Bar set to bar and Baz set to 0.

**`(*Foo).Process(input []byte) ([]byte, error)`** — pure read; does not modify
receiver state. Returns an error if input is empty.

## Domain-Specific Test Scenarios

*(Conditional — include when any bead tests correctness in a domain with non-obvious
geometry: game boards, coordinate systems, spatial algorithms, image/matrix work. If a
test position can be written as raw array indices that look equally plausible right or
wrong, this section is needed.)*

For each such bead, a "Required test scenarios" block: exact positions in
domain-legible notation (algebraic squares, pixel coords, named positions) **with the
arithmetic verification inline**, and the specific wrong answer named explicitly.

```
[Scenario name]: [element] at [domain notation] ([raw coords]).
[Expected target] at [domain notation] ([raw coords]): Δrow=[N], Δcol=[N] ✓.
Do NOT use [wrong target]: Δrow=[N], Δcol=[M] — [why it's wrong].
```

Include a coordinate-system mapping table if 0-indexed coords differ from human
notation. Every worked example whose *specific values* must survive into a bead needs a
matching **Pin** bullet in Decomposition Notes — run `cmd/checkdesigndoc` to confirm.

## Cross-Bead Contracts

*(Conditional — omit entirely if no interface is produced by one bead and consumed by
another.)*

The most commonly incomplete section; gaps here cause runtime bugs that `go build` and
all unit tests pass. Each entry declares:

- **type**: `data-shape` | `format` | `protocol` | `schema`
- **producer**: the producing concern
- **consumer**: the consuming concern
- **interface**: the exact specification — struct definition, function signature,
  binary/text format, or schema excerpt (verbatim — a consumer bead spec must quote it
  exactly, and AUDIT flags paraphrase)
- **notes** *(optional)*: scoping rules, required FuncMap helper registrations, and the
  handler-side obligation **for every return variant**, not just the success path

### Example — handler → template (data-shape)

- **type**: data-shape
- **producer**: http-handlers
- **consumer**: templates
- **interface**: `GameView{Board [8][8]*Piece, Selected *Square, ValidDests map[[2]int]bool, Message string, GameOver bool}`
- **notes**: Inside `{{range}}` loops, top-level GameView fields must use `$` prefix
  (`$.Selected`, `$.ValidDests`). Template must register FuncMap helpers `add(a, b int) int`
  and `mod(a, b int) int` before parsing. All dynamic state (score, turn, game-over)
  must render inside `#board-container` (the HTMX swap target).

### Example — handler calls logic (protocol)

- **type**: protocol
- **producer**: ai
- **consumer**: http-handlers
- **interface**: `RandomAIMove(g *Game) (Point, bool, error)`
- **notes**: Both place and pass handlers call RandomAIMove after the human move,
  provided the game is not already over. If `passed=true`, call `g.Pass()` — omitting
  this leaves ConsecutivePasses stuck and the game cannot end. Return HTTP 500 on error.

### Example — encode/decode pair (format)

- **type**: format
- **producer**: encoder
- **consumer**: decoder
- **interface**: Little-endian binary: `[4]byte magic | uint32 length | []byte payload`
- **notes**: Zero-length payload is valid; magic must be exactly `\x89PNG`.

## Decomposition Notes

*(Conditional — include only when DECOMPOSE's generic heuristics would produce wrong
bead boundaries for this project. Start without this section.)*

DECOMPOSE already applies a 200-line cap, independence requirement, paired-behavior
detection, integration-bead generation, and an httptest requirement for handler beads,
and it reads the Behavioral Specification and Cross-Bead Contracts. Add targeted
guidance only for what it cannot infer:

- One bounded scenario for an integration bead (fixed inputs, one asserted output) to
  stop it over-scoping.
- A per-bead constraint that prevents a known mistake for this project type.
- Explicit sequencing for two beads that share a file.
- A **Pin** bullet for every load-bearing literal / worked-example value that must
  reach a named bead's spec verbatim — name the bead and the exact values. Prose
  elsewhere is not reliably carried; a `**Pin ...**` bullet is.

Do not pre-write the full bead table — let DECOMPOSE make structural decisions.
