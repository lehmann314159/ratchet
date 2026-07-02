# <Project Name> — Design Document

## Overview

One paragraph: what does this project do, who uses it, what is the runtime model
(CLI, server, library, etc.), and what is explicitly out of scope.

**Domain parameters:** list every parameter that a model could reasonably guess
(board size, color depth, constants, limits) — state each one explicitly.

## Data Types and Function Signatures

List every exported type, constant, and function with complete signatures.
SURVEY_SPEC uses this section to declare stubs correctly. Be precise — wrong
parameter or return types here propagate through the whole project.

Do NOT include a `package` declaration or `import` block — the scaffolding step
adds those automatically. Starting the code block with `package main` will cause
the scaffolder to write it twice.

All `.go` source files in this project use `package main`. The module name is
`<modulename>`.

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

```go
var _ func(bar string) *Foo = NewFoo
var _ func(*Foo, []byte) ([]byte, error) = (*Foo).Process
var _ *Foo = State
```

## Behavioral Specification

One or two sentences per function or functional group describing WHAT it does —
the behavioral contract, not the implementation.

Use this section to capture domain logic invisible to the type signatures:
- Which functions are pure reads vs state mutators
- How functions compose (e.g., "Foo uses Bar internally")
- What conditions trigger state changes (e.g., "ConsecutivePasses resets on PlaceStone")
- Which functions form a dependency chain, and that each group is independently testable

Also use this section to resolve implementation ambiguities SURVEY would otherwise
guess (e.g., "Templates are inline Go strings — no external .html files").

**Example:**

**`NewFoo(bar string) *Foo`** — initializes Foo with Bar set to bar and Baz set to 0.

**`(*Foo).Process(input []byte) ([]byte, error)`** — pure read; does not modify
receiver state. Returns an error if input is empty.

**`State`** — package-level singleton initialized by `Init()`, called once from
`main()` before the server starts.

## Cross-Bead Contracts

List every interface produced by one bead and consumed by another. DECOMPOSE_SPEC
uses this section to set exit criteria and populate consumer bead specs with the
exact interface text. AUDIT uses it to verify adequate test coverage. Omit this
section entirely if no cross-bead interfaces exist.

Each entry must declare:
- **type**: `data-shape` | `format` | `protocol` | `schema`
- **producer**: the producing concern (need not match a bead title exactly)
- **consumer**: the consuming concern
- **interface**: the exact specification — struct definition, function signature,
  format description, or schema excerpt
- **notes** *(optional)*: scoping rules, required helper registrations, handler
  obligations for every return variant

### Example — handler → template (data-shape)

- **type**: data-shape
- **producer**: http-handlers
- **consumer**: templates
- **interface**: `GameView{Board [8][8]*Piece, Selected *Square, ValidDests map[[2]int]bool, Message string, GameOver bool}`
- **notes**: Inside `{{range}}` loops, top-level GameView fields must use `$` prefix
  (e.g. `$.Selected`, `$.ValidDests`). Template must register FuncMap helpers
  `add(a, b int) int` and `mod(a, b int) int` before parsing. All dynamic state
  (score, turn, game-over) must render inside `#board-container` (the HTMX swap target).

### Example — handler calls logic (protocol)

- **type**: protocol
- **producer**: ai
- **consumer**: http-handlers
- **interface**: `RandomAIMove(g *Game) (Point, bool, error)`
- **notes**: Both place and pass handlers must call RandomAIMove after the human move,
  provided the game is not already over. If passed=true, call g.Pass() — omitting this
  leaves ConsecutivePasses stuck and the game unable to end. Return HTTP 500 on error.

### Example — encode/decode pair (format)

- **type**: format
- **producer**: encoder
- **consumer**: decoder
- **interface**: Little-endian binary: `[4]byte magic | uint32 length | []byte payload`
- **notes**: Zero-length payload is valid; magic must be exactly `\x89PNG`.

## Decomposition Notes

*(Optional — include only when DECOMPOSE's generic heuristics would produce wrong
bead boundaries for this specific project.)*

DECOMPOSE has strong built-in heuristics (200-line cap, independence, paired-behavior
detection, integration bead generation). It also reads the Behavioral Specification and
Cross-Bead Contracts. For most projects, no Decomposition Notes are needed.

Add targeted guidance only for things DECOMPOSE cannot infer:
- One bounded scenario for an integration bead (to prevent over-scoping)
- A per-bead constraint that prevents a known mistake for this project type
- Explicit sequencing for two beads that share a file

Do not pre-write the full bead table. Let DECOMPOSE make structural decisions.