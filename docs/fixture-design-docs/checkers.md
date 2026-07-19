# Checkers — Design Document

## Overview

A single-player checkers game playable in a web browser. The human plays Red; the AI
plays Black and selects moves randomly from all legal options. The server is written in
Go. The UI uses HTMX for partial page updates — clicking a piece selects it, clicking a
highlighted destination executes the move. The AI responds immediately after every human
move, and the updated board is returned as a single HTML fragment that HTMX swaps into
the page. No persistence; game state lives in a single server-side global for the
duration of the process.

**Domain parameters:**
- Board is 8×8. `Board[Row][Col]`, both 0-indexed. Row 0 is Black's home back rank; Row 7
  is Red's home back rank.
- Red pieces start on rows 5–7; Black pieces start on rows 0–2. Only dark squares are
  used: a square is dark when `(Row+Col)` is odd.
- Red moves toward decreasing Row (Row 7 → Row 0). Black moves toward increasing Row
  (Row 0 → Row 7).
- Regular pieces move one square diagonally forward only. Kings move one square
  diagonally in any of the four directions.
- Crowning: a piece reaching the opponent's back rank (Row 0 for Red, Row 7 for Black)
  is promoted to King immediately upon landing there — including as the final square of
  a multi-jump.
- Mandatory capture and mandatory multi-jump continuation (see Checkers Rules below).
- Server listens on the default `net/http` port via `http.ListenAndServe`.

Out of scope: draws, move undo, difficulty levels for the AI, persistence across
restarts.

## Checkers Rules Implemented

- 8×8 board. Human (Red) pieces start on rows 5–7; AI (Black) pieces start on rows 0–2.
  Only the dark squares are used.
- Regular pieces move diagonally forward one square. Red: `Row` decreases by 1 each
  move. Black: `Row` increases by 1 each move.
- A piece reaching the opponent's back rank (row 0 for Red, row 7 for Black) is crowned
  a King. Kings move diagonally in any direction.
- **Mandatory capture:** if any legal jump is available for the active player, the
  player must jump. Non-jumping moves are not legal on that turn.
- **Multi-jump:** after a capture, if the capturing piece can make another capture from
  its landing square, it must continue jumping. The entire multi-jump chain constitutes
  a single move.
- A captured piece is removed from the board after the full move completes (not
  mid-chain).
- Win conditions: a player wins when the opponent has no legal moves (all pieces
  captured or all pieces blocked).

## Architecture

```
checkers/
├── go.mod
├── main.go           — HTTP server setup, global game state, main()
├── game.go           — Game struct, board logic, move generation, win detection
├── ai.go             — RandomAIMove
├── handlers.go       — HTTP handler functions, template rendering, view model
├── do_not_use_this_test.go — compile-time signature assertions
├── game_test.go      — tests for game.go
├── ai_test.go        — tests for ai.go
└── templates/
    ├── index.html    — full page layout (served only on GET /)
    ├── board.html    — game-area fragment (returned by all POST endpoints)
    └── templates_test.go — verifies all templates parse without error
```

All `.go` files use `package main` at the project root — no subdirectories.

**File assignment rules (strict):**
- `main.go` contains exactly: `var game *Game` and `func main()`. Nothing else.
- `game.go` contains: `Color`, `Piece`, `Square`, `Move`, `Game` type declarations;
  `NewGame`, `ValidMoves`, `AllValidMoves`, `ApplyMove`, `CheckWinner`.
- `ai.go` contains: `RandomAIMove` only.
- `handlers.go` contains: the `GameView` construction helper and all HTTP handler
  functions.
- Do NOT put HTTP handler functions in `game.go` or `ai.go`.
- Do NOT put `GameView` construction in `main.go`.

`templates_test.go` is in `package main` (the root package), not inside `templates/`. It
imports `html/template` and calls `template.ParseFiles` on both template files.

## Data Types and Function Signatures

```go
// Color identifies which player owns a piece, or None for empty squares.
type Color int

const (
    None  Color = 0
    Human Color = 1  // Red — moves toward row 0
    AI    Color = -1 // Black — moves toward row 7
)

// Piece is a single checker on the board.
type Piece struct {
    Color Color
    King  bool
}

// Square is a position on the board.
type Square struct {
    Row, Col int
}

// Move represents a complete move, including all squares jumped in a multi-jump.
// From is the starting square; To is the final landing square after all jumps.
// Captures lists each captured piece's square in jump order. A non-capturing
// move has an empty Captures slice.
type Move struct {
    From     Square
    To       Square
    Captures []Square
}

// Game holds complete game state.
type Game struct {
    Board    [8][8]*Piece // nil = empty square
    Turn     Color        // whose turn: Human or AI
    Selected *Square      // nil if no piece is currently selected
    Winner   Color        // None while game is in progress
}

// NewGame returns a Game with pieces in their standard starting positions,
// Human's turn to move, no selection, and no winner.
func NewGame() *Game

// ValidMoves returns all moves the piece at `from` could make, ignoring the
// mandatory-capture rule. Returns nil if the square is empty or does not
// belong to g.Turn.
func (g *Game) ValidMoves(from Square) []Move

// AllValidMoves returns the complete set of legal moves for `color` after
// applying mandatory capture: if any jumping move exists, only jumping moves
// are returned. Returns nil if the player has no legal moves (loss condition).
func (g *Game) AllValidMoves(color Color) []Move

// ApplyMove executes m on the board, removes captured pieces, promotes pieces
// to King if they reach the back rank, and advances g.Turn to the opponent.
// Returns an error if m is not a legal move for the current state.
func (g *Game) ApplyMove(m Move) error

// CheckWinner returns the winner if the game is over, or None if it is still
// in progress. A player wins when their opponent has no legal moves.
func (g *Game) CheckWinner() Color

// RandomAIMove selects a uniformly random move from g.AllValidMoves(AI).
// Returns an error if the AI has no legal moves.
func RandomAIMove(g *Game) (Move, error)
```

### Export signatures

```go
var _ func() *Game = NewGame
var _ func(*Game, Square) []Move = (*Game).ValidMoves
var _ func(*Game, Color) []Move = (*Game).AllValidMoves
var _ func(*Game, Move) error = (*Game).ApplyMove
var _ func(*Game) Color = (*Game).CheckWinner
var _ func(*Game) (Move, error) = RandomAIMove
```

The layout bead must include these exact lines in `do_not_use_this_test.go`.

## Behavioral Specification

**`NewGame`** — places 12 Red pieces on the dark squares of rows 5–7 and 12 Black
pieces on the dark squares of rows 0–2, sets `Turn = Human`, `Selected = nil`,
`Winner = None`.

**`ValidMoves`** is a pure read — it does not modify `g`. It returns every
geometrically legal move for the piece at `from` (regular diagonal steps for
non-kings, all four diagonal directions for kings, plus any jumps available to that
specific piece) without applying the mandatory-capture filter. `AllValidMoves` calls
`ValidMoves` for every piece of `color`, then applies mandatory capture: if the
combined result contains any jump, only jumps are kept.

**`ApplyMove`** composes the result of move generation: it does not re-derive
legality from scratch, it validates `m` against `AllValidMoves(g.Turn)` first. On a
legal move it mutates the board (moves the piece, removes every square in
`m.Captures`), promotes to King if `m.To` lands on the opponent's back rank, and
switches `g.Turn`.

**`CheckWinner`** depends on `AllValidMoves`, not on piece counts alone — a player
with pieces remaining but no legal moves (fully blocked) has still lost.

**`RandomAIMove`** depends on `AllValidMoves(AI)` and does not call `ApplyMove` — the
caller applies the returned move.

**Function dependency chain** — `ValidMoves` is foundational to `AllValidMoves`,
which `ApplyMove`, `CheckWinner`, and `RandomAIMove` all depend on. Each of move
generation, move execution, win detection, and the AI is independently testable
against this shared foundation and should be treated as a separate unit of work.

## Domain-Specific Test Scenarios

### Coordinate system

```
Square{Row: 0, Col: 0} = a dark square only if (0+0) is odd — it is not, so {0,0} is a
                          light square and never occupied.
Square{Row: 0, Col: 1} = Black's home back rank, dark (0+1=1, odd).
Square{Row: 7, Col: 0} = Red's home back rank, dark (7+0=7, odd).
```

A square `{Row, Col}` is dark, and therefore playable, exactly when `(Row+Col)` is odd.
Every worked example below uses only dark squares; check `(Row+Col)` is odd before
reusing a square in a new test.

Red moves toward decreasing Row (Δrow negative). Black moves toward increasing Row
(Δrow positive). Diagonal adjacency means `|Δrow| == |Δcol|` for every single step.

### Required test scenarios — move-generation bead

**TestValidMoves/RegularForwardDiagonal:** Red (non-king) piece at `{5,2}`
(5+2=7, dark). Forward diagonal targets: `{4,1}` (Δrow=−1,Δcol=−1 ✓) and `{4,3}`
(Δrow=−1,Δcol=+1 ✓). Both must appear in `ValidMoves({5,2})`.
Do NOT include `{6,1}` or `{6,3}`: Δrow=+1 — backward, illegal for a non-king Red piece.
Do NOT include `{4,2}`: Δrow=−1,Δcol=0 — not diagonal, not a legal checkers move at all.

**TestValidMoves/KingAllFourDiagonals:** Black King at `{3,4}` (3+4=7, dark). A King's
four one-step targets: `{2,3}` (Δrow=−1,Δcol=−1 ✓), `{2,5}` (Δrow=−1,Δcol=+1 ✓), `{4,3}`
(Δrow=+1,Δcol=−1 ✓), `{4,5}` (Δrow=+1,Δcol=+1 ✓). All four must appear — unlike a
regular piece, a King is not restricted to forward Δrow.

**TestValidMoves/JumpGeometry:** Red piece at `{5,2}`, Black piece at `{4,3}`
(diagonally adjacent: Δrow=−1,Δcol=+1 from `{5,2}`). The only legal jump lands at
`{3,4}`: Δrow=−2,Δcol=+2 relative to `{5,2}` — double the single-step delta in the
same direction, magnitude 2 in both dimensions. The captured square is the midpoint
`{4,3}`, not the landing square itself.
Do NOT land at `{4,4}` (Δrow=−1,Δcol=+2 — magnitudes unequal, not a diagonal jump) or
compute the captured square as `{3,3}` (Δrow=−2,Δcol=0 from the start — not the
midpoint of a `{5,2}`→`{3,4}` jump, which is `{4,3}`).

### Required test scenarios — move-execution bead

**TestApplyMove/MultiJumpChain:** Red piece at `{5,2}`; Black pieces at `{4,3}` and
`{2,5}`. First jump `{5,2}`→`{3,4}` (captures `{4,3}`, per JumpGeometry above). From
`{3,4}`, a second jump is available over `{2,5}` (Δrow=−1,Δcol=+1 from `{3,4}`) landing
at `{1,6}` (Δrow=−2,Δcol=+2 from `{3,4}`). Because a second capture is available from
the landing square, the move is mandatory to continue: a single `Move{From:{5,2},
To:{1,6}, Captures:[{4,3},{2,5}]}` must be the result, not two separate moves, and
`ApplyMove` must remove both captured pieces only once the full chain lands at `{1,6}`.
Do NOT stop at `{3,4}` and treat it as a complete move — a jump that leaves a further
capture available from the landing square is not yet finished.

**TestApplyMove/PromotionOnLanding:** Red (non-king) piece at `{1,2}` (1+2=3, dark)
moves to `{0,1}` (Δrow=−1,Δcol=−1 ✓, Red's forward direction) — Row 0 is Red's
promotion rank. After `ApplyMove`, the piece at `{0,1}` must have `King == true`.
The same rule applies when Row 0 is reached as the final landing square of a
multi-jump, not only on a single non-capturing step.

**TestApplyMove/MandatoryCaptureRejectsNonJump:** Red piece at `{5,2}`, Black piece
at `{4,3}` (jump available per JumpGeometry above), and a second Red piece at `{5,6}`
with an open forward diagonal to `{4,5}` and `{4,7}` (no jump available for this
second piece). `AllValidMoves(Human)` must contain only the jump from `{5,2}` — the
open, non-capturing moves for the piece at `{5,6}` must be absent, because mandatory
capture applies across the whole side's move set, not just to the jumping piece.

## Cross-Bead Contracts

### GameView binding (data-shape)

- **type**: data-shape
- **producer**: http-handlers
- **consumer**: templates
- **interface**:
  ```go
  type GameView struct {
      Board      [8][8]*Piece
      Selected   *Square
      ValidDests map[[2]int]bool // destination squares for the selected piece
      Message    string          // e.g. "Your turn", "AI wins!", "You win!"
      GameOver   bool
  }
  ```
- **notes**: Inside `{{range}}` loops over the board, top-level `GameView` fields
  (`Selected`, `ValidDests`, `Message`, `GameOver`) must use the `$` prefix. All
  dynamic state (message, game-over banner, board) must render inside `#game-area`,
  the HTMX swap target for every POST response. `GameView` is unexported-fields-free;
  it does not need a `var _` assertion.

### AI response after human move (protocol)

- **type**: protocol
- **producer**: ai (RandomAIMove)
- **consumer**: http-handlers (`/move` handler)
- **interface**: `RandomAIMove(g *Game) (Move, error)`
- **notes**: `/move` handler sequence: (1) parse and validate the human move
  parameters, (2) call `g.ApplyMove`, (3) call `g.CheckWinner` — if the human just
  won, render immediately and skip the AI, (4) call `RandomAIMove` and `g.ApplyMove`
  for the AI's move, (5) call `g.CheckWinner` again, (6) render and return the
  game-area fragment. If `RandomAIMove` returns an error (AI has no legal moves), the
  human has already won by the check in step 3 — this should not occur in step 4;
  treat it as a rendering no-op if it does. The AI move is synchronous; no async or
  polling needed.

### HTTP routes (protocol)

- **type**: protocol
- **producer**: templates (client-side HTMX triggers)
- **consumer**: http-handlers
- **interface**:

  | Method | Path      | Returns            | Description                                    |
  |--------|-----------|---------------------|-------------------------------------------------|
  | GET    | /         | Full HTML page      | Renders `index.html` with a fresh game          |
  | POST   | /new-game | game-area fragment  | Resets to a new game                            |
  | POST   | /select   | game-area fragment  | Selects or deselects a piece; highlights moves  |
  | POST   | /move     | game-area fragment  | Executes human move + AI response               |

- **notes**: `/select` takes query parameters `row` and `col`. `/move` takes
  `from_row`, `from_col`, `to_row`, `to_col`. All POST endpoints return one HTML
  fragment that HTMX swaps in its entirety with `hx-target="#game-area"` and
  `hx-swap="outerHTML"` — this keeps the full-page shell (`<html>`, `<head>`,
  `<body>`, `<h1>`) untouched across swaps.

### Worked example — HTMX wiring

**New Game button** (inside the fragment, so it persists across swaps):
```html
<button hx-post="/new-game" hx-target="#game-area" hx-swap="outerHTML">New Game</button>
```

**Selecting a piece** (a clickable square with a human piece):
```html
<div class="square dark"
     hx-post="/select?row=5&col=0"
     hx-target="#game-area"
     hx-swap="outerHTML">
  <div class="piece human"></div>
</div>
```

**Executing a move** (a highlighted destination square, shown after selection):
```html
<div class="square dark valid-dest"
     hx-post="/move?from_row=5&from_col=0&to_row=4&to_col=1"
     hx-target="#game-area"
     hx-swap="outerHTML">
</div>
```

Non-interactive squares (empty dark squares not highlighted, light squares) carry no
HTMX attributes.

### HTMX CDN

`index.html` loads HTMX from the CDN:
```html
<script src="https://unpkg.com/htmx.org@1.9.12"></script>
```

No local copy needed.

## Decomposition Notes

### Bead list

| # | Title              | Output files                                              | Exit criterion                                    |
|---|--------------------|------------------------------------------------------------|---------------------------------------------------|
| 1 | layout             | game.go, ai.go, handlers.go, main.go, go.mod, do_not_use_this_test.go | `go build ./...`                           |
| 2 | game-state         | game.go, game_test.go                                      | `go test -v . -run=TestNewGame`                   |
| 3 | move-generation    | game.go, game_test.go                                      | `go test -v . -run=TestValidMoves\|TestAllValidMoves` |
| 4 | move-execution     | game.go, game_test.go                                      | `go test -v . -run=TestApplyMove`                 |
| 5 | win-detection      | game.go, game_test.go                                      | `go test -v . -run=TestCheckWinner`               |
| 6 | game-integration   | game_test.go                                               | `go test -v . -run=TestGameRoundTrip`             |
| 7 | ai                 | ai.go, ai_test.go                                          | `go test -v . -run=TestRandomAIMove`              |
| 8 | http-handlers      | handlers.go, main.go                                       | `go build ./...`                                  |
| 9 | templates          | templates/index.html, templates/board.html, templates_test.go | `go test -v . -run=TestTemplatesParse`         |

### Bead boundaries and rules

**Beads 2–5 all write `game.go` and `game_test.go`.** This is an explicit sequential
dependency on the layout bead (bead 1), which creates the stubs. Each subsequent bead
fills in one function group. AUDIT must not flag this as a non-independence
violation — it is the expected pattern for a single-file implementation with multiple
logical concerns. Each bead owns a distinct set of functions within game.go.

**Bead 3 (move-generation)** implements `ValidMoves` and `AllValidMoves`. The
mandatory-capture rule must be implemented in `AllValidMoves` (not in `ValidMoves`).
Required test scenarios: see "Required test scenarios — move-generation bead" above
(RegularForwardDiagonal, KingAllFourDiagonals, JumpGeometry).

**Bead 4 (move-execution)** implements `ApplyMove`. Required test scenarios: see
"Required test scenarios — move-execution bead" above (MultiJumpChain,
PromotionOnLanding, MandatoryCaptureRejectsNonJump).

**Bead 6 (game-integration)** writes only `game_test.go`. It tests the full game
loop: `NewGame` → human `ApplyMove` → `CheckWinner` → AI `AllValidMoves` → AI
`ApplyMove` → `CheckWinner`. It depends on beads 2–5's output and is the designated
home for cross-function correctness assertions. AUDIT must not flag its dependency on
game.go as an independence violation.

**Bead 8 (http-handlers) must not define HTML templates inline.** All HTML —
including any base layout — lives in the `templates/` files owned by bead 9. Handlers
call `template.ExecuteTemplate` with a named template and a `GameView`. No
`template.Must(template.New(...).Parse(...))` calls with inline HTML strings are
permitted in handlers.go or main.go. The global `*template.Template` is loaded from
disk at startup in `main()`.

**Bead 9 (templates)** writes the two HTML files and `templates_test.go` (in
`package main`, in the root directory). `templates_test.go` contains
`TestTemplatesParse`, which calls
`template.ParseFiles("templates/index.html", "templates/board.html")` and fails if
parsing returns an error. The template files must define named blocks that
`template.ExecuteTemplate` can address by name: `index.html` defines the full-page
shell and includes HTMX; `board.html` defines the `"game-area"` template block that
all POST handlers execute.

### Template naming convention

`board.html` must begin with:
```html
{{define "game-area"}}
<div id="game-area">
  ...
</div>
{{end}}
```

`index.html` must contain a `{{template "game-area" .}}` call to embed the initial
board on first load, and must load the `board.html` file in the same
`template.ParseFiles` call in `main()`.

### Global state and concurrency

`main.go` holds a single package-level `var game *Game`. For this demo, no mutex is
needed (single-player, sequential interactions). `main()` initializes
`game = NewGame()` before starting the HTTP server.

### Integration bead scope

**Bounded scenario:** `NewGame()`, apply one Red regular move, apply one Black AI
move (via `RandomAIMove` + `ApplyMove`), call `CheckWinner` and assert it is still
`None`. Do not attempt to play a full game to completion in the integration bead.