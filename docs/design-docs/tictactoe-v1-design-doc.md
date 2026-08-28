# Design Document: Tic-Tac-Toe (Web App)

## Overview

A single-player tic-tac-toe web app. The human plays `X` and always moves
first; the computer plays `O` and replies automatically after every valid
human move, using a simple deterministic rule (no search) — see Behavioral
Specification below. The board is a 3×3 grid, indexed 0–8, row-major:

```
0 1 2
3 4 5
6 7 8
```

A line (row, column, or diagonal) is a win once one player occupies all
three of its cells. The game ends in a win or, once every cell is filled
with no winner, a draw.

Deliberately picked as a minimal fixture: no search algorithm, no scoring
heuristic, no internal (unexported) helper functions a test needs to call
directly — every function a test touches is part of the bead's own
exported public contract. The goal is a clean signal on the pipeline's own
mechanics, not a rich source of new design-doc iteration material (see
`connect-four-v1-design-doc.md` for that).

## Architecture

```
├── go.mod
├── main.go       — var game *Game; func main()
├── game.go       — Cell, Game, NewGame, PlaceMark, CheckWinner, IsFull,
│                    ValidMoves
├── ai.go         — BestMove
├── handlers.go   — GameView, toView, HandleIndex, HandleMove, HandleReset
└── templates.go  — Templates, InitTemplates, RenderIndex, RenderBoard
```

- `main.go` contains only `var game *Game` and `func main()`. No other
  functions, types, or package-level variables belong in `main.go`.
- `game.go` contains: `Cell` and its constants, `Game`, `NewGame`,
  `(*Game).PlaceMark`, `(*Game).CheckWinner`, `(*Game).IsFull`,
  `(*Game).ValidMoves`. Pure board mechanics only — no reference to
  `GameView`, `http`, or `template`.
- `ai.go` contains only `BestMove`. No unexported helper functions — the
  algorithm is simple enough to implement as straight-line logic inside
  `BestMove` itself (see Behavioral Specification).
- `handlers.go` contains: `GameView`, `toView`, `HandleIndex`,
  `HandleMove`, `HandleReset`. Bridges `game` state to HTTP.
- `templates.go` contains: `Templates`, `InitTemplates`, `RenderIndex`,
  `RenderBoard`.

## Data Types and Function Signatures

Do NOT include a `package` declaration or `import` block — the scaffolding
step adds those automatically.

All `.go` source files in this project use `package main`. The module name
is `tictactoe`.

```go
const NumCells = 9

type Cell int

const (
    Empty Cell = iota
    X
    O
)

type Game struct {
    Board  [NumCells]Cell // index 0-8, row-major (see Overview for the grid)
    Turn   Cell            // X or O — whose move is next; meaningless once Winner != Empty or Draw is true
    Winner Cell             // Empty while no one has won yet; X or O once that player has a line
    Draw   bool             // true once the board is completely full and Winner == Empty
}

func NewGame() *Game
func (g *Game) PlaceMark(pos int) error
func (g *Game) CheckWinner() Cell
func (g *Game) IsFull() bool
func (g *Game) ValidMoves() []int

func BestMove(g *Game) (int, error)

type GameView struct {
    Board    [NumCells]string // "x", "o", or "empty" per cell — same index order as Game.Board
    Cells    []int             // precomputed 0..NumCells-1, for the template to range over when rendering cells
    Turn     string            // "x" or "o" — meaningful only when GameOver is false
    Winner   string            // "x" or "o" once someone has won; "" otherwise
    Draw     bool
    GameOver bool              // true if Winner != "" or Draw
    Message  string            // human-readable status/error line, e.g. "X wins!", "Cell 4 is taken", "Your turn"
}

func toView(g *Game) GameView

func HandleIndex(w http.ResponseWriter, r *http.Request)
func HandleMove(w http.ResponseWriter, r *http.Request)
func HandleReset(w http.ResponseWriter, r *http.Request)

var Templates *template.Template

func InitTemplates()
func RenderIndex(w http.ResponseWriter, view GameView) error
func RenderBoard(w http.ResponseWriter, view GameView) error

var game *Game
```

### Export signatures

```go
var _ func() *Game = NewGame
var _ func(*Game, int) error = (*Game).PlaceMark
var _ func(*Game) Cell = (*Game).CheckWinner
var _ func(*Game) bool = (*Game).IsFull
var _ func(*Game) []int = (*Game).ValidMoves
var _ func(*Game) (int, error) = BestMove
var _ func(*Game) GameView = toView
var _ func(http.ResponseWriter, *http.Request) = HandleIndex
var _ func(http.ResponseWriter, *http.Request) = HandleMove
var _ func(http.ResponseWriter, *http.Request) = HandleReset
var _ *template.Template = Templates
var _ func() = InitTemplates
var _ func(http.ResponseWriter, GameView) error = RenderIndex
var _ func(http.ResponseWriter, GameView) error = RenderBoard
var _ *Game = game
```

## Behavioral Specification

**Dependency chain (read this before the pipeline decomposes the
project):** the board-mechanics functions (`NewGame`, `PlaceMark`,
`CheckWinner`, `IsFull`, `ValidMoves`) are foundational and independently
testable as one group with no dependency on anything else in this doc.
`BestMove` builds on top of that group (it calls the same win-detection
logic against hypothetical moves) and forms a separate, independently
testable concern. The HTTP layer (`GameView`/`toView`/handlers, and the
templates) builds on both of the above and is testable only once they
exist. Each of these three groups is independently testable as a unit of
work.

**`NewGame() *Game`** — returns a `*Game` with every cell `Empty`,
`Turn = X`, `Winner = Empty`, `Draw = false`.

**`(*Game).PlaceMark(pos int) error`** is the single entry point that keeps
`Board`, `Turn`, `Winner`, and `Draw` mutually consistent — every step
below must happen inside this one call, in this exact order:
1. If `g.Winner != Empty` or `g.Draw` is true, return a non-nil error and
   change nothing — the game is already over.
2. If `pos < 0` or `pos >= NumCells`, return a non-nil error and change
   nothing.
3. If `g.Board[pos] != Empty`, return a non-nil error and change nothing —
   the cell is already taken.
4. Place `g.Turn`'s mark at `pos`.
5. Call `g.CheckWinner()`. If it returns non-`Empty`, set `g.Winner` to
   that value and return `nil` — **do not** also flip `g.Turn` in this
   branch; the game has ended on this move.
6. Otherwise, if `g.IsFull()` is now true (the board became completely
   full on this move and nobody won), set `g.Draw = true` and return
   `nil` — again, do not flip `g.Turn`. See Domain-Specific Test
   Scenarios for the required "drawing move" test's exact board — a
   genuine draw board is easy to construct wrong (a naive fill is likely
   to contain an accidental line).
7. Otherwise, flip `g.Turn` (`X` becomes `O`, `O` becomes `X`) and return
   `nil`.

   This ordering matters: a move that both completes a line *and* fills
   the last empty cell must be scored as a win (step 5), never as a draw
   — step 6 only runs when step 5 found no winner.

**`(*Game).CheckWinner() Cell`** — pure read; does not modify `g`. Scans
*all eight* lines (three rows, three columns, two diagonals — see
Domain-Specific Test Scenarios for the exact line list and worked
examples) and returns the winning mark, or `Empty` if none exists. Called
from inside `PlaceMark`; also usable standalone (e.g. by `BestMove`,
against hypothetical boards — see below).

**`(*Game).IsFull() bool`** — pure read; true iff every cell in `Board` is
non-`Empty`.

**`(*Game).ValidMoves() []int`** — pure read; returns every index
`0..NumCells-1` where `Board[i] == Empty`, in ascending order. Empty slice
(not nil-vs-empty-sensitive; either is fine) if the board is completely
full.

**`BestMove(g *Game) (int, error)`** computes the cell the computer should
play next, for whichever mark `g.Turn` currently is (in practice this is
always called only when `g.Turn == O`, but the function itself is generic
and does not assume that). Returns a non-nil error only if `g.ValidMoves()`
is empty (no legal move exists) — this should not normally happen if the
caller checked `!g.Draw` first (see the `handlers` contract below), but
`BestMove` must not panic or crash if it does.

**Algorithm — no search, a fixed priority order** (this is deliberately
not minimax; tic-tac-toe's whole point in this project is to be a minimal
fixture, not a search-algorithm exercise):

```
function BestMove(g):
    aiMark := g.Turn
    oppMark := opponent(aiMark)  // X <-> O
    moves := g.ValidMoves()
    if moves is empty:
        return 0, error("no legal moves")

    // 1. Win now if a single move completes a line for aiMark.
    for pos in moves:
        hypothetical := g.Board with aiMark placed at pos
        if lineWinner(hypothetical) == aiMark:
            return pos, nil

    // 2. Otherwise block: play wherever oppMark would win next turn.
    for pos in moves:
        hypothetical := g.Board with oppMark placed at pos
        if lineWinner(hypothetical) == oppMark:
            return pos, nil

    // 3. Otherwise take the center if it's open.
    if 4 is in moves:
        return 4, nil

    // 4. Otherwise take the first open corner, in this exact order.
    for pos in [0, 2, 6, 8]:
        if pos is in moves:
            return pos, nil

    // 5. Otherwise take the first open edge, in this exact order.
    for pos in [1, 3, 5, 7]:
        if pos is in moves:
            return pos, nil
```

`lineWinner` above is the same all-eight-lines scan `CheckWinner` uses,
applied to a hypothetical board (not `g.CheckWinner()`, since the board
here is a hypothetical future position, not `g`'s real board) — do not
implement a second, different win-check for `BestMove`.

**Cell index to human-readable position, if useful in test names**: 0/1/2
are the top row (left/middle/right), 3/4/5 the middle row, 6/7/8 the
bottom row — matching the grid diagram in Overview.

## Domain-Specific Test Scenarios

**Line enumeration used by both `CheckWinner` and `BestMove`'s
`lineWinner` — state this identically in both places:**

- **Rows:** `{0,1,2}`, `{3,4,5}`, `{6,7,8}`.
- **Columns:** `{0,3,6}`, `{1,4,7}`, `{2,5,8}`.
- **Diagonals:** `{0,4,8}`, `{2,4,6}`.

A line is a win for a mark iff all three of its cells equal that mark
(equivalently: the first cell is non-`Empty` and all three are equal).
`CheckWinner` returns the first winning mark found scanning in the order
above (rows, then columns, then diagonals); if no line anywhere on the
board is a win, it returns `Empty`.

**Required test scenarios for the `game` bead's `CheckWinner` tests** —
use these exact positions (mechanically verified, not hand-derived):

1. **Row win:** `X` at `0, 1, 2` (top row). `CheckWinner()` must return
   `X`.
2. **Column win:** `O` at `0, 3, 6` (left column). `CheckWinner()` must
   return `O`.
3. **Diagonal win:** `X` at `0, 4, 8` (top-left to bottom-right).
   `CheckWinner()` must return `X`.
4. **Anti-diagonal win:** `O` at `2, 4, 6` (top-right to bottom-left).
   `CheckWinner()` must return `O`.
5. **NOT a win — two in a line, third empty:** `X` at `0, 1` only (cell 2
   empty). `CheckWinner()` must return `Empty`.
6. **NOT a win — scattered marks, no complete line:** `X` at `0`, `O` at
   `1`, `X` at `4`. `CheckWinner()` must return `Empty`.
7. **Full board, no winner (draw) — required for `PlaceMark`'s "drawing
   move" test:** a naive fill is likely to contain an accidental
   three-in-a-row long before the board is full. Use this exact
   mechanically verified board instead (index order 0–8, row-major):
   ```
   X O X
   X O O
   O X X
   ```
   That is: `X` at `0,2,3,7,8`; `O` at `1,4,5,6`. Construct the test as:
   pre-fill every cell exactly as shown *except* `8`, which stays `Empty`;
   set `Turn = X` (so the dropped mark matches this board's own
   `8 = X`, keeping the completed board identical to the verified one
   above); call `PlaceMark(8)`. `CheckWinner()` must return `Empty`
   (verified over the complete 9-cell board, `8` included), so
   `IsFull()` becomes `true` and `Draw` must be set to `true`; `Turn`
   must stay `X` (the draw branch does not flip it). Do not construct a
   different full board for this test.

**Required test scenarios for the `ai` bead's `BestMove` tests** — use
these exact positions (mechanically verified, not hand-derived):

8. **Win now:** `O` at `0, 1` (`Turn = O`), cell `2` empty, no other marks.
   `BestMove` must return `2` (completes `O`'s row) — priority-1 in the
   algorithm above, checked before any blocking logic.
9. **Block:** `X` at `0, 1` (`Turn = O`), cell `2` empty, no other marks.
   `BestMove` must return `2` (blocks `X`'s row) — `O` has no
   immediately-winning move of its own here, so this exercises
   priority-2.
10. **Win takes priority over block:** `O` at `0, 1` (cell `2` empty,
    would win) *and* `X` at `3, 4` (`Turn = O`, cell `5` empty, would also
    let `X` win next turn). `BestMove` must return `2`, not `5` — the
    algorithm checks priority-1 (its own win) before priority-2 (blocking)
    even though both a winning move and a blocking move exist.
11. **Center fallback:** empty board, `Turn = O`. No win or block is
    available (board is empty). `BestMove` must return `4` (the center) —
    priority-3.
12. **Corner fallback:** `Turn = O`, center (`4`) already occupied by `X`,
    every other cell empty. No win or block is available. `BestMove` must
    return `0` (the first open corner in the fixed order `0, 2, 6, 8`) —
    priority-4.

## Cross-Bead Contracts

- **type**: data-shape
- **producer**: handlers
- **consumer**: templates
- **interface**: `GameView{Board [NumCells]string, Cells []int, Turn string, Winner string, Draw bool, GameOver bool, Message string}`
- **notes**: No FuncMap helpers required. Inside `{{range}}` over `.Board`,
  top-level fields must use the `$` prefix (`$.Turn`, `$.Winner`,
  `$.Message`, `$.GameOver`). All of this data — board, turn, win/draw
  message — must render inside the `"board"` template (the
  `#board-container` swap target, mirroring the pattern in
  `connect-four-v1-design-doc.md`); nothing dynamic may sit in `"index"`
  outside of `{{template "board" .}}`.

- **type**: protocol
- **producer**: game (`PlaceMark`, `CheckWinner`, `IsFull`)
- **consumer**: handlers (`HandleMove`)
- **interface**: `(*Game).PlaceMark(pos int) error`
- **notes**: `HandleMove` must call `game.PlaceMark(pos)` and branch on its
  error return; `PlaceMark` itself is solely responsible for updating
  `Winner`/`Draw`/`Turn` correctly (including the win-before-draw
  ordering) — `HandleMove` must not separately call `CheckWinner` or
  `IsFull` itself.

- **type**: protocol
- **producer**: ai (`BestMove`)
- **consumer**: handlers (`HandleMove`)
- **interface**: `BestMove(g *Game) (int, error)`
- **notes**: `HandleMove` must call `BestMove(game)` after a *successful*
  human `PlaceMark` call, but only if the game is still in progress
  (`game.Winner == Empty && !game.Draw`) — never after a human move that
  already won or drew the game, and never after a *rejected* (errored)
  human move. On success, call `game.PlaceMark(aiPos)` with the returned
  position (its error can be ignored — see notes above). On error from
  `BestMove` (only reachable if `ValidMoves()` is empty, which
  `!game.Draw` already rules out in practice), build the view from the
  human-move-only state and note the computer could not find a move.

- **type**: format
- **producer**: handlers (`HandleMove`)
- **consumer**: (none within this project — HTMX partial swap target)
- **interface**: HTTP `POST /move` with form field `pos` (string,
  parseable as an int `0..NumCells-1`)
- **notes**: an unparseable or out-of-range `pos` must be treated the same
  as any other rejected move — render the board with an error message,
  do not panic or 500.

## Decomposition Notes

- **Pin the exact `CheckWinner` test positions to the `game` bead**: the
  six scenarios (1-6) listed verbatim in Domain-Specific Test Scenarios
  above (four win cases, two non-win cases). Do not let this bead's spec
  substitute different, unverified coordinates.
- **Pin the exact full-board draw grid to the `game` bead, literally, not
  by reference**: scenario 7 above (the `X O X` / `X O O` / `O X X` 3×3
  grid). A spec that says "use the exact board provided in the design
  document" instead of reproducing the grid itself forces the executing
  bead to reconstruct it from scratch with no way to verify the result —
  this exact failure recurred live three times on this project's sibling
  Connect Four fixture (`connect-four-v1-design-doc.md`) before being
  fixed there. Do not repeat it here.
- **Pin the exact `BestMove` test positions to the `ai` bead**: scenarios
  8-12 listed verbatim in Domain-Specific Test Scenarios above (win, block,
  win-over-block priority, center fallback, corner fallback). Do not let
  this bead's spec substitute different, unverified positions or drop the
  priority-order worked example (scenario 10).
- **Integration bead — one bounded scenario, not "test a full game":**
  start a new game; send one `POST /move` with `pos=0`; verify the
  resulting board has exactly 2 non-empty cells total (1 `X` from the
  human move, 1 `O` from the computer's automatic reply) and that
  `game.Turn == X` afterward (assuming neither single move won or drew
  the game, which is true for a first move on an empty board). This
  scenario deliberately does not assert *which* cell the computer chose
  — that is the `ai` bead's own concern, verified separately by the
  `BestMove` scenarios above.
- **Integration bead clobber risk**: if the integration bead's
  `output_files` includes a `_test.go` file that already appears in a
  prior bead's `output_files`, flag it — each bead must own distinct test
  files.
- **Handler beads must not re-implement game-mechanics or ai logic.**
  `HandleMove` calls `game.PlaceMark` and `BestMove` and branches on their
  results; it does not scan the board for winners or search for a move
  itself.
