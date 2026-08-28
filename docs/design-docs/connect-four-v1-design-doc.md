# Connect Four — Design Document

## Overview

A single-player web application: a human plays Connect Four against a
computer opponent. The human always plays Red and always moves first; the
computer always plays Yellow and moves automatically, synchronously, right
after each Red move, within the same HTTP request/response cycle (no
polling, no websockets, no background goroutine). The server holds exactly
one game in memory at a time — there is no multiplayer, no per-user session,
no persistence across restarts, and no authentication. The UI is server-
rendered HTML updated in place via HTMX partial swaps: dropping a piece and
the computer's automatic reply are reflected in a single response.

Out of scope: multiplayer / networked play, persistence (the game resets to
empty on process restart), authentication or per-user state, move
history/replay, undo, difficulty levels or any UI control that changes the
computer's search depth at runtime (the depth is a fixed compile-time
constant — see Domain parameters), sound/animation, and mobile-specific
styling.

**Domain parameters:**
- Board size: 6 rows × 7 columns (the standard Connect Four board). These
  are named constants `NumRows = 6`, `NumCols = 7` — do not hard-code the
  numbers 6/7 anywhere else; reference the constants.
- Win condition: exactly 4 of the same color in an unbroken straight line —
  horizontal, vertical, or either diagonal direction. Having 4 same-colored
  pieces that are *not* consecutive along one of these lines is not a win
  (see Domain-Specific Test Scenarios for a worked non-example).
- Two colors: Red and Yellow. Red is the human and always moves first;
  Yellow is the computer and always moves second. This never changes within
  a game — there is no color-swap or handicap setting.
- The computer's search depth is a fixed named constant `SearchDepth = 4`
  (see Data Types). This is a deliberately named, easily-changed single
  constant — a natural target for a future difficulty adjustment.
- HTTP server listens on a fixed port, `:8080`. No command-line flags, no
  environment variables for configuration.
- Routes: `GET /` (full page), `POST /drop` (human move, form field `col`),
  `POST /reset` (start a new game). No other routes. Handlers do not need to
  check HTTP method beyond what `net/http`'s default mux already enforces by
  route registration — do not add extra method-switch logic.

**Coordinate system (state this precisely — it is used throughout the rest
of this document and must not be re-derived):**
- `Board[row][col]`. **Row 0 is the top of the board (and the top of the
  screen); row `NumRows-1` (row 5) is the bottom of the board (and the
  bottom of the screen), where a freshly dropped piece settles first if the
  column is empty.** Column 0 is the leftmost column; column `NumCols-1`
  (column 6) is the rightmost.
- This matches ordinary top-down 2D array / image-processing convention.
  `Game.Board` and the view model's `Board` field (see Data Types) use the
  *same* orientation — a template that iterates `Board` in stored order,
  outer loop over rows then inner loop over columns, renders correctly
  top-to-bottom with no reversal step anywhere in the pipeline.
- **Gravity / drop rule, stated precisely:** dropping a piece into column
  `c` places it in the *largest* row index `r` (i.e. the physically lowest,
  bottommost) such that `Board[r][c] == Empty`. Equivalently: scan rows from
  `NumRows-1` (bottom) up to `0` (top); the piece lands in the first empty
  cell found scanning in that direction. If `Board[0][c]` (the top cell of
  that column) is already occupied, the column has no empty cell and the
  drop is illegal.
  - **Worked example:** on an empty board, dropping into column 3 lands at
    `(row=5, col=3)` — the bottom row. Dropping into column 3 again lands at
    `(row=4, col=3)` — one row up, because row 5 of that column is now
    occupied. Do NOT place a new piece at a fixed row (e.g. always row 0, or
    always row 5) — the landing row depends on how many pieces already
    occupy that column: landing row = `NumRows - 1 - (existing piece count
    in that column)`.

## Architecture

```
connectfour/
├── go.mod
├── main.go       — var game *Game; func main()
├── game.go       — Cell, NumRows, NumCols, Game, NewGame, DropPiece,
│                    CheckWinner, IsFull, ValidMoves
├── ai.go         — SearchDepth, BestMove (+ unexported minimax/evaluate
│                    helpers)
├── handlers.go   — GameView, toView, HandleIndex, HandleDrop, HandleReset
├── templates.go  — Templates, InitTemplates, RenderIndex, RenderBoard
└── *_test.go     — one test file per source file above, plus
                     integration_test.go
```

All `.go` files use `package main` at the project root — no subdirectories
(this pipeline never places Go source in subpackages; see the shared
pipeline convention).

**File assignment rules (strict):**
- `main.go` contains exactly: `var game *Game` and `func main()`. `main()`
  calls `game = NewGame()`, calls `InitTemplates()`, registers the three
  routes (`/`, `/drop`, `/reset`) on the default `http.ServeMux` via
  `http.HandleFunc`, and calls `http.ListenAndServe(":8080", nil)`. No other
  functions, types, or package-level variables belong in `main.go`.
- `game.go` contains: `Cell` and its constants, `NumRows`, `NumCols`,
  `Game`, `NewGame`, `(*Game).DropPiece`, `(*Game).CheckWinner`,
  `(*Game).IsFull`, `(*Game).ValidMoves`. Pure board mechanics only — no
  reference to `GameView`, `http`, `template`, or the AI search.
- `ai.go` contains: `SearchDepth` and `BestMove`, plus any unexported
  minimax/alpha-beta/evaluation helper functions it needs. Operates only on
  `Game`/`Cell`/board data from `game.go` — no reference to `GameView`,
  `http`, or `template`.
- `handlers.go` contains: `GameView`, `toView`, `HandleIndex`, `HandleDrop`,
  `HandleReset`. `HandleIndex`, `HandleDrop`, and `HandleReset` are plain
  `func(w http.ResponseWriter, r *http.Request)` functions that read and
  write the package-level `game` variable declared in `main.go` directly —
  they are not closures and do not take `*Game` as a parameter. `toView`,
  by contrast, takes a `*Game` parameter explicitly so it is unit-testable
  independently of the global. Do NOT put `GameView` or `toView` in
  `templates.go` — they belong in `handlers.go` because they depend on
  `Game`, not on `template.Template`.
- `templates.go` contains: `Templates`, `InitTemplates`, `RenderIndex`,
  `RenderBoard`. No handler functions, no `GameView` type declaration (it
  consumes `GameView` as a parameter type but does not define it).
- Do NOT put `HandleIndex`, `HandleDrop`, or `HandleReset` in
  `templates.go`.
- Do NOT put `BestMove` or any minimax logic in `game.go` — board mechanics
  and search strategy are separate concerns and separately testable.

## Data Types and Function Signatures

Do NOT include a `package` declaration or `import` block — the scaffolding
step adds those automatically.

All `.go` source files in this project use `package main`. The module name
is `connectfour`.

```go
const NumRows = 6
const NumCols = 7

type Cell int

const (
    Empty Cell = iota
    Red
    Yellow
)

type Game struct {
    Board  [NumRows][NumCols]Cell // Board[row][col]; row 0 = top, row NumRows-1 = bottom
    Turn   Cell                    // Red or Yellow — whose move is next; meaningless once Winner != Empty or Draw is true
    Winner Cell                    // Empty while no one has won yet; Red or Yellow once that color has four in a row
    Draw   bool                    // true once the board is completely full and Winner == Empty
}

func NewGame() *Game
func (g *Game) DropPiece(col int) error
func (g *Game) CheckWinner() Cell
func (g *Game) IsFull() bool
func (g *Game) ValidMoves() []int

const SearchDepth = 4

func BestMove(g *Game) (int, error)
func evaluate(board [NumRows][NumCols]Cell, aiColor Cell) int

type GameView struct {
    Board    [NumRows][NumCols]string // "red", "yellow", or "empty" per cell — same row/col orientation as Game.Board
    Columns  []int                     // precomputed 0..NumCols-1, for the template to range over when rendering drop buttons
    Turn     string                    // "red" or "yellow" — meaningful only when GameOver is false
    Winner   string                    // "red" or "yellow" once someone has won; "" otherwise
    Draw     bool
    GameOver bool                      // true if Winner != "" or Draw
    Message  string                    // human-readable status/error line, e.g. "Red wins!", "Column 3 is full", "Your turn"
}

func toView(g *Game) GameView

func HandleIndex(w http.ResponseWriter, r *http.Request)
func HandleDrop(w http.ResponseWriter, r *http.Request)
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
var _ func(*Game, int) error = (*Game).DropPiece
var _ func(*Game) Cell = (*Game).CheckWinner
var _ func(*Game) bool = (*Game).IsFull
var _ func(*Game) []int = (*Game).ValidMoves
var _ func(*Game) (int, error) = BestMove
var _ func([NumRows][NumCols]Cell, Cell) int = evaluate
var _ func(*Game) GameView = toView
var _ func(http.ResponseWriter, *http.Request) = HandleIndex
var _ func(http.ResponseWriter, *http.Request) = HandleDrop
var _ func(http.ResponseWriter, *http.Request) = HandleReset
var _ *template.Template = Templates
var _ func() = InitTemplates
var _ func(http.ResponseWriter, GameView) error = RenderIndex
var _ func(http.ResponseWriter, GameView) error = RenderBoard
var _ *Game = game
```

## Behavioral Specification

**Dependency chain (read this before the pipeline decomposes the project):**
the board-mechanics functions (`NewGame`, `DropPiece`, `CheckWinner`,
`IsFull`, `ValidMoves`) are foundational and independently testable as one
group with no dependency on anything else in this doc. `BestMove` and its
internal minimax search build on top of that group (it calls the same
win-detection and legal-move logic against hypothetical future boards) and
form a separate, independently testable concern. The HTTP layer
(`GameView`/`toView`/handlers, and the templates) builds on both of the
above and is testable only once they exist. Each of these three groups is
independently testable as a unit of work.

**`NewGame() *Game`** — returns a `*Game` with every cell `Empty`,
`Turn = Red`, `Winner = Empty`, `Draw = false`.

**`(*Game).DropPiece(col int) error`** is the single entry point that keeps
`Board`, `Turn`, `Winner`, and `Draw` mutually consistent — every step below
must happen inside this one call, in this exact order:
1. If `g.Winner != Empty` or `g.Draw` is true, return a non-nil error and
   change nothing — the game is already over.
2. If `col < 0` or `col >= NumCols`, return a non-nil error and change
   nothing.
3. Find the landing row for `col` per the gravity rule in Overview. If the
   column has no empty cell (`Board[0][col] != Empty`), return a non-nil
   error and change nothing.
4. Place `g.Turn`'s color at the landing cell.
5. Call `g.CheckWinner()`. If it returns non-`Empty`, set `g.Winner` to that
   value and return `nil` — **do not** also flip `g.Turn` in this branch;
   the game has ended on this move.
6. Otherwise, if `g.IsFull()` is now true (the board became completely full
   on this move and nobody won), set `g.Draw = true` and return `nil` —
   again, do not flip `g.Turn`.
7. Otherwise, flip `g.Turn` (`Red` becomes `Yellow`, `Yellow` becomes `Red`)
   and return `nil`.

   This ordering matters: a move that both completes four-in-a-row *and*
   fills the last empty cell must be scored as a win (step 5), never as a
   draw — step 6 only runs when step 5 found no winner. See Domain-Specific
   Test Scenarios' scenario 7 for the required "drawing move" test's exact
   board — a genuine draw board is easy to construct wrong.

**`(*Game).CheckWinner() Cell`** — pure read; does not modify `g`. Scans the
*entire* board for any four-in-a-row (see Domain-Specific Test Scenarios for
the exact window enumeration and worked examples) and returns the winning
color, or `Empty` if none exists. Called from inside `DropPiece`; also
usable standalone (e.g. by `BestMove`'s search, against hypothetical
boards — see below).

**`(*Game).IsFull() bool`** — pure read; true iff every cell in `Board` is
non-`Empty`.

**`(*Game).ValidMoves() []int`** — pure read; returns every column index
`0..NumCols-1` whose top cell (`Board[0][col]`) is `Empty`, in ascending
order. Empty slice (not nil-vs-empty-sensitive; either is fine) if the
board is completely full.

**`BestMove(g *Game) (int, error)`** computes the column the computer
should play next, for whichever color `g.Turn` currently is (in practice
this is always called only when `g.Turn == Yellow`, but the function itself
is generic and does not assume that). Returns a non-nil error only if
`g.ValidMoves()` is empty (no legal move exists) — this should not normally
happen if the caller checked `!g.Draw` first (see the `handlers` contract
below), but `BestMove` must not panic or crash if it does.

Algorithm — minimax search with alpha-beta pruning, searching `SearchDepth`
plies beyond the move `BestMove` itself is choosing (so `SearchDepth = 4`
means the computer's own candidate move plus 4 further plies of lookahead,
5 total). `BestMove` tries every one of its own legal moves once at the
root, and picks whichever yields the highest score from the search below:

```
function BestMove(g):
    aiColor := g.Turn
    moves := g.ValidMoves()
    if moves is empty:
        return 0, error("no legal moves")
    bestScore := -infinity
    bestCol := moves[0]
    for col in moves:
        childBoard := g.Board with aiColor's piece dropped into col
                      (apply the same gravity rule as DropPiece)
        score := minimax(childBoard, SearchDepth, -infinity, +infinity,
                          aiColor, opponent(aiColor))
        if score > bestScore:
            bestScore = score
            bestCol = col
    return bestCol, nil

function minimax(board, depth, alpha, beta, aiColor, colorToMove):
    w := winner of board (same four-in-a-row scan CheckWinner uses, applied
         to this hypothetical board — not g.CheckWinner(), since board here
         is a hypothetical future position, not g's real board)
    if w == aiColor:              return +1_000_000 + depth  // prefer a faster win
    if w == opponent(aiColor):    return -1_000_000 - depth  // prefer a slower loss
    if depth == 0 or board is completely full:
        return evaluate(board, aiColor)

    moves := legal columns for board (same rule as ValidMoves, applied to board)
    if colorToMove == aiColor:                       // maximizing node
        value := -infinity
        for col in moves:
            child := board with colorToMove's piece dropped into col
            value = max(value, minimax(child, depth-1, alpha, beta,
                                        aiColor, opponent(colorToMove)))
            alpha = max(alpha, value)
            if alpha >= beta: break
        return value
    else:                                             // minimizing node
        value := +infinity
        for col in moves:
            child := board with colorToMove's piece dropped into col
            value = min(value, minimax(child, depth-1, alpha, beta,
                                        aiColor, opponent(colorToMove)))
            beta = min(beta, value)
            if alpha >= beta: break
        return value
```

**Critical implementation note — why `Board` must stay a fixed-size array,
not a slice:** `[NumRows][NumCols]Cell` is a Go *array* type, which is a
value type — `childBoard := board` (plain assignment) already performs a
full, independent copy of every cell. This is what makes `child := board
with ... dropped into col` in the pseudocode above safe and correct: each
recursive branch and each of `BestMove`'s root-level trial moves gets its
own independent copy automatically, with no aliasing between branches and
no explicit copy loop needed. **Do not** change `Board`'s type to a slice
(`[][]Cell`) anywhere in this project — a slice is a reference type, and
using one here would make every "hypothetical" board in the search alias
the same underlying array, silently corrupting sibling branches' results
with each other's trial moves.

`aiColor` is fixed for the entire recursion (the color `BestMove` is
computing a move for); `colorToMove` alternates every ply. Do not confuse
the two — `aiColor` decides the sign convention of the returned score
(positive is good for `aiColor`); `colorToMove` decides whose legal moves
are being enumerated at this node and whether this node maximizes or
minimizes.

**`evaluate(board, aiColor)` (unexported helper) — exact scoring rule,**
stated precisely because this score must be deterministic and testable, not
left to the implementer's judgment:

For every possible four-cell "window" on the board — every horizontal run
of 4 consecutive columns in a row, every vertical run of 4 consecutive rows
in a column, and every diagonal run of 4 in both diagonal directions (the
exact same window enumeration `CheckWinner` uses — see Domain-Specific Test
Scenarios) — classify the window by how many `aiColor` pieces, how many
opponent-color pieces, and how many `Empty` cells it contains (these three
counts always sum to 4):
- If the window contains pieces of **both** colors, it contributes **0**
  (dead — neither color can ever complete four-in-a-row using this window).
- If it contains only `aiColor` pieces (plus possibly empties), it
  contributes `+WindowScore(n)`, where `n` is the count of `aiColor` pieces
  in the window and `WindowScore(1) = 1`, `WindowScore(2) = 10`,
  `WindowScore(3) = 50`. (A window with `n == 4` would mean `aiColor` has
  already won, which `minimax`'s terminal check above already catches
  before `evaluate` is ever called — `evaluate` never needs to handle
  `n == 4`.)
- If it contains only opponent pieces (plus possibly empties), it
  contributes `-WindowScore(n)` using the *same* `WindowScore` function
  above, where `n` is the opponent's piece count in that window.
- If the window is entirely empty, it contributes `0`.

`evaluate(board, aiColor)` is the sum of every window's contribution.

**Worked example (verify any implementation against this exact number):** a
board that is entirely empty except for one Red piece at `(row=5, col=0)`
(the bottom-left corner), evaluated with `aiColor = Yellow`, must return
exactly **-3**. That single piece is the opponent's (Red, when
`aiColor = Yellow`), so every window containing it contributes `-1`
(`-WindowScore(1)`). Exactly three windows contain the bottom-left corner
cell: one horizontal window (`(5,0)-(5,3)`), one vertical window
(`(2,0)-(5,0)`), and one down-left-diagonal window (`(2,3),(3,2),(4,1),
(5,0)`) — **no down-right-diagonal window contains it**, because a
down-right diagonal run of length 4 through the bottom-left corner would
have to extend either above row 0 or left of column 0, both off the board.
`3 × -1 = -3`.

**`toView(g *Game) GameView`** — pure read; builds the view model handlers
and templates use. `view.Board[r][c]` is `"red"`, `"yellow"`, or `"empty"`
according to `g.Board[r][c]` — same `[row][col]` orientation as `Game.Board`
(no flip). `view.Columns` is `[0, 1, ..., NumCols-1]`. `view.Turn` is
`"red"` or `"yellow"` from `g.Turn`. `view.Winner` is `"red"`/`"yellow"` if
`g.Winner != Empty`, else `""`. `view.Draw` mirrors `g.Draw`.
`view.GameOver` is `view.Winner != "" || view.Draw`. `view.Message` is set
by the caller (see `HandleDrop` below) — `toView` itself always leaves
`Message` as the zero value `""`; it does not compute status text.

**`InitTemplates()`** — parses the HTML templates from Go string literals
(no external `.html` files) into the package-level `Templates` variable.
Must be called once from `main()` before the server starts handling
requests. Defines (at minimum) two named templates: `"index"` (the full
HTML page, including `<html>`/`<head>`/`<body>`) and `"board"` (just the
contents that go inside `#board-container` — the board grid, the status/
message line, and the drop/reset controls). The `"index"` template includes
`"board"` via `{{template "board" .}}` so that the very first full-page load
and every later HTMX partial swap render byte-identical markup for
`#board-container`. **All dynamic state that can change after a move —
the board itself, whose turn it is, the win/draw message — must render
inside `#board-container`, i.e. inside the `"board"` template. Nothing
dynamic may live in the `"index"` template outside of
`{{template "board" .}}`**, or it will silently stop updating after the
first move (HTMX only swaps `#board-container`'s contents).

No FuncMap helper functions are required by these templates — `GameView`
already precomputes `Board` as ready-to-render strings and `Columns` as a
ready-to-range-over slice, specifically so no template-side arithmetic or
type conversion helper is needed. Do not add one.

**`RenderIndex(w, view) error`** — executes the `"index"` template with
`view` as the data and writes the result to `w`. **`RenderBoard(w, view)
error`** — executes the `"board"` template with `view` as the data and
writes the result to `w`. Both templates iterate `view.Board` as
`{{range $r, $row := .Board}}{{range $c, $cell := $row}}...{{end}}{{end}}`
(or equivalent) — inside that nested range, top-level `GameView` fields
(`.Turn`, `.Winner`, `.Message`, `.GameOver`) are no longer accessible via a
bare `.`; they must be accessed with the `$`-prefixed root, e.g. `$.Turn`,
`$.Message`.

**`HandleIndex(w, r)`** — `GET /`. Renders the current state:
`RenderIndex(w, toView(game))`. Does not modify `game`.

**`HandleDrop(w, r)`** — `POST /drop`, human's move. The column is sent in
form field `col` (parse with `r.FormValue("col")` then `strconv.Atoi`); if
it is missing or not a valid integer, treat it the same as an out-of-range
column (see below) — respond with `RenderBoard` showing the current,
unchanged state and `view.Message` set to an explanatory string, then
return; do not call `DropPiece` or the AI.

1. If `game.Turn != Red` (a stray or duplicate request arriving while it is
   not actually Red's turn — the AI's own move below is always applied
   synchronously within the same request that triggered it, so this should
   only happen from a client-side double-submit): call
   `RenderBoard(w, toView(game))` with the state unchanged and return. Do
   not call `DropPiece` or the AI.
2. Otherwise call `game.DropPiece(col)`.
   - If it returns an error: build `view := toView(game)`, set
     `view.Message` to a short explanation of what went wrong (e.g. the
     column is full, or out of range), call `RenderBoard(w, view)`, and
     return. Do not call the AI — no valid human move was made.
   - If it succeeds: continue to step 3.
3. If, after the human's move, `game.Winner == Empty && !game.Draw` (the
   game is still in progress), call `BestMove(game)`:
   - If it returns an error: build `view := toView(game)`, set
     `view.Message` to note the computer could not find a move, call
     `RenderBoard(w, view)`, and return, without crashing the handler.
   - If it succeeds with column `aiCol`: call `game.DropPiece(aiCol)` and
     ignore its error return — `BestMove` only ever returns a column from
     `game.ValidMoves()`, so this call cannot legitimately fail.
4. Call `RenderBoard(w, toView(game))` — this reflects both the human's
   move and (if it happened) the computer's automatic reply in one
   response.

**`HandleReset(w, r)`** — `POST /reset`. Sets `game = NewGame()`, then calls
`RenderBoard(w, toView(game))`.

## Domain-Specific Test Scenarios

**Window enumeration used by both `CheckWinner` and `evaluate` — state this
identically in both places, it is easy to get the loop bounds wrong at the
board edges:**

- **Horizontal:** for each row `r` in `0..NumRows-1`, for each starting
  column `c` in `0..NumCols-4` (inclusive — i.e. `0,1,2,3` when
  `NumCols=7`), the window is `Board[r][c], Board[r][c+1], Board[r][c+2],
  Board[r][c+3]`.
- **Vertical:** for each column `c` in `0..NumCols-1`, for each starting row
  `r` in `0..NumRows-4` (inclusive — i.e. `0,1,2` when `NumRows=6`), the
  window is `Board[r][c], Board[r+1][c], Board[r+2][c], Board[r+3][c]`.
- **Diagonal, down-right (top-left corner of the window to bottom-right,
  row and column both increasing):** for each starting row `r` in
  `0..NumRows-4`, for each starting column `c` in `0..NumCols-4`, the
  window is `Board[r][c], Board[r+1][c+1], Board[r+2][c+2],
  Board[r+3][c+3]`.
- **Diagonal, down-left (top-right corner of the window to bottom-left, row
  increasing, column decreasing):** for each starting row `r` in
  `0..NumRows-4`, for each starting column `c` in `3..NumCols-1`
  (inclusive — i.e. `3,4,5,6` when `NumCols=7`), the window is
  `Board[r][c], Board[r+1][c-1], Board[r+2][c-2], Board[r+3][c-3]`.

A window is a win for a color iff all four of its cells equal that color
(equivalently: the first cell is non-`Empty` and all four are equal).
`CheckWinner` returns the first winning color found scanning in the order
above (horizontal, then vertical, then down-right diagonal, then down-left
diagonal); if no window anywhere on the board is a win, it returns `Empty`.

**Required test scenarios for the `game` bead's `CheckWinner` tests** — use
these exact positions (mechanically verified, not hand-derived):

1. **Horizontal win:** Red at `(5,0), (5,1), (5,2), (5,3)` (bottom row, four
   consecutive columns). `CheckWinner()` must return `Red`.
2. **Vertical win:** Yellow at `(5,3), (4,3), (3,3), (2,3)` (column 3, the
   bottom four rows). `CheckWinner()` must return `Yellow`.
3. **Diagonal down-right win:** Red at `(2,0), (3,1), (4,2), (5,3)`.
   `CheckWinner()` must return `Red`.
4. **Diagonal down-left win:** Red at `(2,3), (3,2), (4,1), (5,0)`.
   `CheckWinner()` must return `Red`.
5. **NOT a win — non-consecutive diagonal:** Red at `(5,0), (4,1), (3,2),
   (1,3)`. These four cells lie on the same down-right diagonal line, but
   are **not** four *consecutive* cells of it — the run `(5,0),(4,1),(3,2)`
   is immediately followed by `(2,3)` (empty), and `(1,3)` is one row
   further still. `CheckWinner()` must return `Empty`. Do NOT implement
   win-checking as "are there 4 same-colored pieces somewhere on a
   diagonal line" — it must require 4 *consecutive* cells of one specific
   window as enumerated above.
6. **NOT a win — blocked by the opponent:** Red at `(5,0), (5,1), (5,2)`
   and Yellow at `(5,3)`. Three Red in a row, but the fourth cell of that
   window is the opponent's color, not empty. `CheckWinner()` must return
   `Empty`.
7. **Full board, no winner (draw) — required for `DropPiece`'s "drawing
   move" test:** a naive fill (e.g. one color repeated, or "every cell but
   one is Red") is virtually guaranteed to contain an accidental
   four-in-a-row long before the board is full — `CheckWinner` would then
   correctly return a winner instead of `Empty`, making a draw test
   unsatisfiable by any correct implementation, no matter how it's written.
   This happened for real, twice (connect-four-v1 bead 47, connect-four-v3
   bead 59, 2026-08-27) before being caught — use this exact board instead,
   mechanically verified against the window enumeration above to contain
   *zero* four-in-a-row windows anywhere. Row 0 (top) to row 5 (bottom),
   column 0 to 6 left to right, `R`=Red, `Y`=Yellow:
   ```
   RRRYYYR
   YYYRRRY
   RRRYYYR
   YYYRRRY
   RRRYYYR
   YYYRRRY
   ```
   Construct the test as: pre-fill every cell exactly as shown *except*
   `(0,0)`, which stays `Empty`; set `Turn = Red` (so the dropped piece
   matches this board's own `(0,0) = Red`, keeping the completed board
   identical to the verified one above); call `DropPiece(0)`. Column 0's
   only empty cell is `(0,0)` (rows 1–5 are already filled), so the piece
   lands there per the gravity rule. `CheckWinner()` must return `Empty`
   (verified over the complete 42-cell board, `(0,0)` included), so
   `IsFull()` becomes `true` and `Draw` must be set to `true`; `Turn` must
   stay `Red` (the draw branch does not flip it). Do not construct a
   different full board for this test.

**Required worked example for the `ai` bead's `evaluate` tests** — see the
worked example already stated in full in the Behavioral Specification
section above (single Red piece at `(5,0)`, `aiColor = Yellow`,
`evaluate() == -3`, via exactly 3 contributing windows: 1 horizontal +
1 vertical + 1 down-left diagonal, and explicitly 0 down-right-diagonal
windows since the bottom-left corner is not part of any). Use that exact
board and exact expected value; do not construct a different example.

## Cross-Bead Contracts

- **type**: data-shape
- **producer**: handlers
- **consumer**: templates
- **interface**: `GameView{Board [NumRows][NumCols]string, Columns []int, Turn string, Winner string, Draw bool, GameOver bool, Message string}`
- **notes**: No FuncMap helpers required. Inside `{{range}}` over `.Board`
  (and the nested per-row range), top-level fields must use the `$` prefix
  (`$.Turn`, `$.Winner`, `$.Message`, `$.GameOver`). All of this data —
  board, turn, win/draw message — must render inside the `"board"`
  template (the `#board-container` swap target); nothing dynamic may sit in
  `"index"` outside of `{{template "board" .}}`.

- **type**: protocol
- **producer**: game (`DropPiece`, `CheckWinner`, `IsFull`)
- **consumer**: handlers (`HandleDrop`)
- **interface**: `(*Game).DropPiece(col int) error`
- **notes**: `HandleDrop` must call `game.DropPiece(col)` and branch on its
  error return before doing anything else — see the full step-by-step
  contract in `HandleDrop`'s Behavioral Specification entry above,
  including the `game.Turn != Red` stray-request guard and the
  error-vs-success rendering paths. `DropPiece` itself is solely
  responsible for updating `Winner`/`Draw`/`Turn` correctly (including the
  win-before-draw ordering) — `HandleDrop` must not separately call
  `CheckWinner` or `IsFull` itself.

- **type**: protocol
- **producer**: ai (`BestMove`)
- **consumer**: handlers (`HandleDrop`)
- **interface**: `BestMove(g *Game) (int, error)`
- **notes**: `HandleDrop` must call `BestMove(game)` after a *successful*
  human `DropPiece` call, but only if the game is still in progress
  (`game.Winner == Empty && !game.Draw`) — never after a human move that
  already won or drew the game, and never after a *rejected* (errored)
  human move. On success, call `game.DropPiece(aiCol)` with the returned
  column (its error can be ignored — see notes above). On error from
  `BestMove`, do not crash the handler; render the current state with an
  explanatory message instead. Omitting the post-move `BestMove` call
  entirely leaves the computer permanently passive — the app would compile
  and the human-only tests would pass, but the computer would never play.

## Decomposition Notes

- **Pin exact literal values to the `ai` bead** (whichever bead ends up
  implementing `ai.go`): `SearchDepth = 4`; `WindowScore(1) = 1`,
  `WindowScore(2) = 10`, `WindowScore(3) = 50`; and the worked
  `evaluate()` example — single Red piece at `(row=5, col=0)`,
  `aiColor = Yellow`, expected result `-3`. Do not let this bead's spec
  keep only the general scoring *rule* while dropping these specific
  numbers — the numbers are exactly what its test file must assert against.
- **Pin the exact `CheckWinner` test positions to the `game` bead**: the
  six scenarios listed verbatim in Domain-Specific Test Scenarios above
  (four win cases, two non-win cases). Do not let this bead's spec
  substitute different, unverified coordinates.
- **Pin the exact full-board draw grid to the `game` bead, literally, not
  by reference**: scenario 7 in Domain-Specific Test Scenarios above (the
  `RRRYYYR`/`YYYRRRY` 6×7 grid). A spec that says "use the exact board
  provided in the design document" instead of reproducing the grid itself
  forces the executing bead to reconstruct it from scratch with no way to
  verify the result — confirmed live (connect-four-v5 bead 71,
  2026-08-28): DECOMPOSE_SPEC referenced the board instead of embedding it,
  `REFINE_TESTS_WRITE` invented its own fill pattern that turned out to
  contain an accidental four-in-a-row, and the bead burned all 5
  `REFINE_TESTS` cycles failing to self-correct before escalating on the
  cycle cap. Same failure class as the `evaluate` worked example above —
  do not let this bead's spec keep only a pointer to the grid.
- **Integration bead — one bounded scenario, not "test a full game":**
  start a new game; send one `POST /drop` with `col=3`; verify the
  resulting board has exactly 2 non-empty cells total (1 Red from the
  human move, 1 Yellow from the computer's automatic reply) and that
  `game.Turn == Red` afterward (assuming neither single move won or drew
  the game, which is true for a first move on an empty board). This
  scenario deliberately does not assert *which* column the computer
  chose — that is the `ai` bead's own concern, verified separately by the
  `evaluate`/`BestMove` tests, not by this integration test.
- **Sequencing:** implement and test the `templates` bead (real template
  parsing and rendering, not a stub) before the `handlers` bead's httptest
  exit criteria are written against it — a handler test run against a
  stub template produces a vacuous pass.
