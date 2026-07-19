# othello-v3 — Design Document

## Overview

A web-based Othello (Reversi) game. Two players alternate placing stones on an 8×8 board.
A move is legal only if it flanks at least one opponent stone in a straight line (horizontal,
vertical, or diagonal); all flanked stones flip to the current player's color. A player who
has no legal moves must pass. The game ends when both players pass consecutively; the player
with more stones wins. Ties are possible.

The human always plays Black; the server plays White using a random-legal-move AI. The UI
is served over HTTP with HTMX fragment updates — no full-page reloads after moves. One game
is held in server memory; no persistence, no authentication, no multi-player networking.

**Domain parameters:**
- Board: 8×8, zero-indexed rows and columns
- Initial position: White at (3,3) and (4,4); Black at (3,4) and (4,3)
- Black moves first
- Color constants: `Empty=0`, `Black=1`, `White=-1`
- Game ends when `ConsecutivePasses >= 2`
- Winner: player with more stones; `Empty` returned on tie

Out of scope: undo, game history, color selection, time controls, networked multiplayer.

---

## Architecture

```
othello/
├── go.mod
├── main.go     — var game *Game; func main() only
├── game.go     — Color, Point, Game types; NewGame, FindFlips, ValidMoves,
│                 PlaceStone, Pass, Score, CheckWinner
├── ai.go       — RandomAIMove
├── handlers.go — GameView type, toView helper, HandleIndex, HandlePlace, HandlePass
├── templates.go — Templates var, InitTemplates, RenderIndex, RenderBoard
└── *_test.go   — one test file per source file above, plus integration_test.go
```

All `.go` files use `package main` at the project root — no subdirectories.

**File assignment rules (strict):**
- `main.go` contains exactly: `var game *Game` and `func main()`. Nothing else.
- `game.go` contains: `Color`, `Point`, `Game` type declarations; `NewGame`,
  `FindFlips`, `ValidMoves`, `PlaceStone`, `Pass`, `Score`, `CheckWinner`.
- `ai.go` contains: `RandomAIMove` only.
- `handlers.go` contains: the `GameView` type, a `toView` helper that builds a
  `GameView` from `*Game`, and all three HTTP handlers (`HandleIndex`, `HandlePlace`,
  `HandlePass`).
- `templates.go` contains: `Templates`, `InitTemplates`, `RenderIndex`, `RenderBoard`.
  No handler functions. No type declarations.
- Do NOT put `HandleIndex`, `HandlePlace`, or `HandlePass` in `templates.go`.
- Do NOT put `GameView` in `templates.go` or `game.go` — it belongs in `handlers.go`,
  since http-handlers is the producer (see Cross-Bead Contracts).

## Data Types and Function Signatures

All `.go` source files in this project use `package main`. The module name is `othello`.

```go
type Color int

const (
    Empty Color = 0
    Black Color = 1
    White Color = -1
)

type Point struct {
    Row, Col int
}

type Game struct {
    Board             [8][8]Color
    Turn              Color
    ConsecutivePasses int
    LastMove          *Point
}

func NewGame() *Game

func (g *Game) FindFlips(p Point) []Point
func (g *Game) ValidMoves() []Point

func (g *Game) PlaceStone(p Point) error
func (g *Game) Pass()

func (g *Game) Score() (black, white int)
func (g *Game) CheckWinner() Color

func RandomAIMove(g *Game) (Point, bool, error)

type GameView struct {
    Board      [8][8]Color
    Turn       Color
    ValidMoves [8][8]bool
    LastMove   *Point
    Score      [2]int
    GameOver   bool
    Winner     Color
    Message    string
}
```

### Export signatures

```go
var _ func() *Game = NewGame
var _ func(*Game, Point) []Point = (*Game).FindFlips
var _ func(*Game) []Point = (*Game).ValidMoves
var _ func(*Game, Point) error = (*Game).PlaceStone
var _ func(*Game) = (*Game).Pass
var _ func(*Game) (int, int) = (*Game).Score
var _ func(*Game) Color = (*Game).CheckWinner
var _ func(*Game) (Point, bool, error) = RandomAIMove
var _ *template.Template = Templates
```

---

## Behavioral Specification

**`NewGame`** — sets the initial board position exactly as specified in domain parameters;
`Turn = Black`.

**`FindFlips(p Point) []Point`** — pure read; does not modify `Board`. Returns the list of
opponent stones that would be flipped if `g.Turn` placed at `p`. Returns empty if the move
is illegal (no flanked stones).

**`ValidMoves() []Point`** — returns every position on the board where `FindFlips` returns
at least one stone for `g.Turn`.

**`PlaceStone(p Point) error`** — returns an error if `FindFlips(p)` is empty (illegal
move). On a legal move: places `g.Turn` at `p`, flips all stones in `FindFlips(p)`,
sets `LastMove = &p`, resets `ConsecutivePasses = 0`, switches `Turn`.

**`Pass()`** — increments `ConsecutivePasses` by 1, switches `Turn`.

**`CheckWinner() Color`** — returns `Empty` while `ConsecutivePasses < 2`. Once the game
ends, returns the `Color` with more stones on the board, or `Empty` on a tie.

**`RandomAIMove(g *Game) (Point, bool, error)`** — selects a random element from
`g.ValidMoves()`. Returns `(point, false, nil)` on success. Returns `(zero, true, nil)` if
`ValidMoves` is empty (AI must pass). Returns a non-nil error only on an unexpected failure.

**Game logic structure** — the game functions form a dependency chain: `FindFlips` is
foundational to both `ValidMoves` and `PlaceStone`. Each functional group — board
initialization, flip computation, move application, and game-state evaluation — is
independently testable and should be treated as a separate unit of work.

**HTTP routes** — `GET /` serves the full page. `POST /place?row=R&col=C` places the
human's stone and triggers the AI move; returns the board fragment. `POST /pass` passes for
the human and triggers the AI move; returns the board fragment. All POST handlers return the
same HTMX swap fragment targeting `#board-container`.

**`Templates`** — a package-level `*template.Template` initialized by `InitTemplates()`,
which is called once from `main()` before the server starts. Templates are defined as Go
string literals inside `InitTemplates()` — no external `.html` files.

The `index` template is a full HTML page. It must:
- Load htmx from CDN: `<script src="https://unpkg.com/htmx.org@1.9.10"></script>`
- Render the initial board state inline (not an empty container) — `HandleIndex` passes a `GameView` to the index template, so the board is populated on first load
- Each valid-move cell must carry `hx-post="/place?row={{$r}}&col={{$c}}" hx-target="#board-container" hx-swap="outerHTML"` so clicking a cell POSTs the move without a full page reload
- Include a Pass button: `hx-post="/pass" hx-target="#board-container" hx-swap="outerHTML"`

The `board` template renders only the `#board-container` div (the HTMX swap fragment) and is returned by `/place` and `/pass`.

`HandleIndex` must call `NewGame()`, build a `GameView`, and execute the `index` template with that view — not `nil` — so the board is immediately visible.

**`main()`** — calls `InitTemplates()`, registers `HandleIndex` on `GET /`, `HandlePlace` on `POST /place`, and `HandlePass` on `POST /pass`, then starts the server with `http.ListenAndServe(":8080", nil)`. Implemented in `main.go`.

---

## Domain-Specific Test Scenarios

### Coordinate system and directions

```
Point{Row: 0, Col: 0} = top-left cell
Point{Row: 0, Col: 7} = top-right cell
Point{Row: 7, Col: 0} = bottom-left cell
Point{Row: 7, Col: 7} = bottom-right cell
```

`FindFlips` must check all eight directions from `p`, not just one. The eight
`(Δrow, Δcol)` direction vectors are:
`{-1,-1}, {-1,0}, {-1,1}, {0,-1}, {0,1}, {1,-1}, {1,0}, {1,1}`.

**The scan rule for a single direction:** starting one step from `p` in direction
`(Δrow, Δcol)`, walk cell by cell. If the current cell is off the board, or empty,
the scan for that direction ends with **zero flips** — an empty cell or the edge
never validates a flip, even if a same-color stone exists further along the same
line beyond the gap. If the current cell is the opponent's color, keep walking and
remember it as a candidate flip. If the current cell is `g.Turn`'s own color and at
least one candidate has been collected, every candidate collected in that direction
flips. `FindFlips` is the union of the flip lists from all eight directions — a move
that flips in three directions returns all three directions' stones in one slice.

### Required test scenarios — flip-computation bead

**TestFindFlips/SingleDirection:** Starting position (White at `{3,3}`,`{4,4}`;
Black at `{3,4}`,`{4,3}`), Black to move, `FindFlips({2,3})`. Direction `{1,0}` (down)
from `{2,3}`: `{3,3}`=White (candidate), `{4,3}`=Black (own color, candidate found) →
flips `{3,3}`. No other direction from `{2,3}` has an adjacent White stone in this
position, so the result is exactly `[{3,3}]`.
Do NOT return an empty slice — `{2,3}` is a legal Black move in the standard opening
and must flip the White stone directly below it.

**TestFindFlips/MultiDirectionCombined:** Board cleared except White at `{3,3}` and
`{3,4}`, Black at `{3,5}`; White at `{4,2}`, Black at `{5,2}`. Black plays `{3,2}`.
- Direction `{0,1}` (right): `{3,3}`=White, `{3,4}`=White, `{3,5}`=Black (own color) →
  candidates `{3,3}`,`{3,4}` flip.
- Direction `{1,0}` (down): `{4,2}`=White, `{5,2}`=Black (own color) → candidate
  `{4,2}` flips.
`FindFlips({3,2})` must return all three points: `{3,3}`, `{3,4}`, `{4,2}`.
Do NOT return only the first direction's flips — a function that checks one
direction, finds a valid flip, and returns immediately will miss `{4,2}`. All eight
directions must be checked and their results combined into one slice.

**TestFindFlips/GapBreaksTheLine:** Board cleared except White at `{3,4}` and Black
at `{5,4}`, with `{4,4}` empty. Black plays `{2,4}`. Direction `{1,0}` (down) from
`{2,4}`: `{3,4}`=White (candidate), `{4,4}`=Empty → the scan stops here with **zero**
flips for this direction, even though a Black stone sits at `{5,4}` further down the
same line.
Do NOT skip over the empty cell at `{4,4}` to find the Black stone at `{5,4}` and
conclude `{3,4}` flips — a flip requires an unbroken run of opponent stones
immediately terminated by an own-color stone, with no empty cell anywhere in between.

### Required test scenarios — placement bead

**TestPlaceStone/APIRejectsIllegalMove:** From the starting position, Black attempts
`PlaceStone({0,0})` — a corner with no adjacent stones at all, so `FindFlips({0,0})`
is empty. `PlaceStone` must return a non-nil error and must not modify the board.

**TestValidMoves/StartingPositionHasFourMoves:** From the starting position, Black's
four legal opening moves are `{2,3}`, `{3,2}`, `{4,5}`, `{5,4}` — the four cells
adjacent to the central four stones that each flip exactly one White stone by the
same single-direction logic as `SingleDirection` above. `ValidMoves()` must return
exactly these four points (order-independent).

---

## Cross-Bead Contracts

### GameView binding (data-shape)

- **type**: data-shape
- **producer**: http-handlers
- **consumer**: templates
- **interface**:
  ```
  GameView{
      Board      [8][8]Color,
      Turn       Color,
      ValidMoves [8][8]bool,
      LastMove   *Point,
      Score      [2]int,   // Score[0] = Black count, Score[1] = White count
      GameOver   bool,
      Winner     Color,
      Message    string,
  }
  ```
- **notes**: All user-visible state that changes after a move (score, turn indicator,
  game-over message) must be rendered inside `#board-container`, which is the HTMX swap
  target for all POST responses. The board fragment returned by `/place` and `/pass` must
  include score, turn, and game-over — not just the board grid.

### AI response after human move (protocol)

- **type**: protocol
- **producer**: ai
- **consumer**: http-handlers
- **interface**: `RandomAIMove(g *Game) (Point, bool, error)`
- **notes**: Both the place handler and the pass handler must call `RandomAIMove` after
  the human move, provided the game is not already over (`CheckWinner() == Empty`).
  - `passed=false` → call `PlaceStone` to place the AI stone
  - `passed=true` → call `Pass()` to advance `ConsecutivePasses`; omitting this leaves
    the game unable to end
  - non-nil error → return HTTP 500

---

## Decomposition Notes

**Dependency chain** — `FindFlips` is foundational to both `ValidMoves` and
`PlaceStone`; `Score` and `CheckWinner` depend only on `Board` and can be tested
independently of move generation. `RandomAIMove` depends on `ValidMoves` and
`PlaceStone`. Templates must be implemented before http-handlers so handler tests
render real HTML rather than stub output.

**templates bead must come before http-handlers bead.** Handler tests use httptest
and require `InitTemplates()` to parse real templates. If templates runs after
http-handlers, handler tests will get stub output instead of rendered HTML.

**Exit criterion for the http-handlers bead:** must use `net/http/httptest` — `go
build ./...` alone is explicitly insufficient, since it cannot catch a missing
`RandomAIMove` call or a template execution error. The smoke test must call
`InitTemplates()`, then exercise `HandleIndex`, and verify the response contains
`id="board-container"`.

**Integration bead scope** (bounded): from the starting position, POST `/place` with
`row=2&col=3` (the `SingleDirection` scenario above), assert the response contains
`id="board-container"`, assert the response reflects the flip at `{3,3}` (e.g. a
White-class cell became Black-class), and assert the AI has responded (turn indicator
reads "Black to move" again, or the AI's own move produced a further board change).
Do not attempt to play a full game to completion in the integration bead.
