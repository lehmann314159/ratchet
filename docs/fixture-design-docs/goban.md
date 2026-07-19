# Goban (9×9) — Design Document

## Overview

A single-player 9×9 Go game playable in a web browser. The human plays Black; the AI
plays White and selects moves uniformly at random from all legal placements, passing if
no legal placement exists. The server is written in Go. The UI uses HTMX for partial
page updates — clicking an empty intersection places a stone; a Pass button passes. The
AI responds immediately after every human move.

**Scoring is Chinese (area) scoring:** at game end (two consecutive passes), each
player's score is the number of their stones on the board plus the number of empty
intersections enclosed solely by their stones. White receives 6.5 komi. The player with
the higher score wins; ties are impossible with half-point komi.

**Board parameters:** 9×9 intersections. Black plays first. Standard 4-directional
adjacency (no diagonal connections).

**Out of scope:** territory scoring during play (only at game end), dead stone removal,
superko, handicap stones, AI opponent strength, authentication.

**Rules implemented:** capture, suicide prohibition, simple ko (one ko point per turn),
pass, two-pass game end, Chinese scoring.

---

## Architecture

```
goban/
├── go.mod
├── main.go          — var game *Game; func main() only
├── game.go          — Color, Point, Game, GameView types; NewGame, Group, Liberties, PlaceStone, Pass
├── scoring.go       — Territory, Score, CheckWinner
├── ai.go            — RandomAIMove
├── handlers.go      — toView, HandleIndex, HandlePlace, HandlePass, HandleReset
├── templates.go     — InitTemplates, RenderIndex, RenderBoard (inline string templates only)
├── game_test.go     — tests for game.go
├── scoring_test.go  — tests for scoring.go
├── ai_test.go       — tests for ai.go
├── templates_test.go
├── handlers_test.go
└── api_check_test.go
```

All files are in `package main` at the project root. No subdirectories.

**File assignment rules (strict):**
- `main.go` contains exactly: `var game *Game` and `func main()`. Nothing else.
- `game.go` contains: Color, Point, Game, GameView type declarations; NewGame, Group, Liberties, PlaceStone, Pass, ValidMoves functions.
- `scoring.go` contains: Territory, Score, CheckWinner functions.
- `ai.go` contains: RandomAIMove function.
- `handlers.go` contains: toView helper and all HTTP handler functions (HandleIndex, HandlePlace, HandlePass, HandleReset).
- `templates.go` contains: InitTemplates, RenderIndex, RenderBoard. No handler functions. No type declarations.
- Do NOT put HandleIndex, HandlePlace, HandlePass, or HandleReset in templates.go.
- Do NOT put GameView in templates.go — it belongs in game.go.

---

## Data Types and Function Signatures

```go
type Color int

const (
    Empty Color = 0
    Black Color = 1
    White Color = -1
)

type Point struct {
    Row, Col int  // Row 0 = top, Col 0 = left; valid range [0,8]
}

type Game struct {
    Board             [9][9]Color
    Turn              Color  // Black or White
    PrevBoard         [9][9]Color  // board before the most recent placement (ko detection)
    LastMove          *Point       // nil after a pass or at game start
    ConsecutivePasses int          // 2 = game over
    KoPoint           *Point       // if non-nil, this intersection cannot be played this turn
}

// GameView is the template data constructed immediately before each render call.
type GameView struct {
    Board          [9][9]Color
    Turn           Color
    LastMove       *Point
    BlackStones    int
    WhiteStones    int
    BlackTerritory int
    WhiteTerritory int
    Dame           int
    BlackScore     float64  // BlackStones + BlackTerritory
    WhiteScore     float64  // WhiteStones + WhiteTerritory + 6.5
    GameOver       bool
    Winner         Color    // Empty if game not over or draw (impossible with komi)
    Message        string
}

// NewGame returns a Game with an empty 9×9 board, Black to move,
// ConsecutivePasses=0, and nil LastMove and KoPoint.
func NewGame() *Game

// Group returns all Points connected 4-directionally to the stone at p,
// including p itself. Returns nil if p is empty or out of bounds.
func Group(b [9][9]Color, p Point) []Point

// Liberties returns the count of distinct empty intersections adjacent
// (4-directionally) to any stone in the group containing p.
// Returns 0 if p is empty or out of bounds.
func Liberties(b [9][9]Color, p Point) int

// PlaceStone places a stone of g.Turn's color at p, resolves captures,
// enforces suicide and ko, and advances the game state.
// Returns an error if: out of bounds; occupied; matches g.KoPoint; suicide
// (placing group has 0 liberties after all captures); results in board == g.PrevBoard.
// On success: saves board to PrevBoard, sets LastMove, resets ConsecutivePasses,
// updates KoPoint (see Ko rule below), switches Turn.
func PlaceStone(g *Game, p Point) error

// Pass records a pass for g.Turn: increments ConsecutivePasses, sets LastMove=nil,
// clears KoPoint, switches Turn. No-op if game is already over.
func Pass(g *Game)

// ValidMoves returns all Points at which g.Turn may legally place a stone
// (not suicide, not ko, not occupied). Does not include passing.
func ValidMoves(g *Game) []Point

// Territory partitions all empty intersections of b into three groups using
// 4-directional flood fill. An empty region bordered only by Black stones is
// Black territory; only by White stones is White territory; both colors → dame.
func Territory(b [9][9]Color) (black, white, dame int)

// Score returns the Chinese area score for each player at the current board state.
// blackScore = Black stones on board + Black territory.
// whiteScore = White stones on board + White territory + 6.5 (komi).
// Valid at any point in the game; typically called only at game end.
func Score(g *Game) (blackScore, whiteScore float64)

// CheckWinner returns the winner if ConsecutivePasses >= 2, or Empty otherwise.
// Uses Score() to determine the winner; ties are impossible with 6.5 komi.
func CheckWinner(g *Game) Color

// RandomAIMove selects a uniformly random legal placement for g.Turn by shuffling
// ValidMoves and attempting each on a scratch copy of g. Returns the first legal
// move. If no legal placement exists, returns (Point{}, true, nil) — the caller
// must then call Pass(g). Returns an error if the game is already over.
func RandomAIMove(g *Game) (p Point, isPass bool, err error)

// InitTemplates parses the inline template strings and registers the FuncMap.
// Must be called before any render function. Panics on parse error.
func InitTemplates()

// RenderIndex writes the full HTML page to w using the "index" template.
func RenderIndex(w http.ResponseWriter, v GameView)

// RenderBoard writes the board fragment to w using the "board" template.
func RenderBoard(w http.ResponseWriter, v GameView)

// Handler functions registered on the mux:
func HandleIndex(w http.ResponseWriter, r *http.Request)
func HandlePlace(w http.ResponseWriter, r *http.Request)
func HandlePass(w http.ResponseWriter, r *http.Request)
func HandleReset(w http.ResponseWriter, r *http.Request)
```

### Ko rule

After a successful PlaceStone:
1. Count the opponent stones removed in this move.
2. If exactly 1 opponent stone was removed AND the placing stone now has exactly 1
   liberty, set g.KoPoint to that liberty (the recapture point). Otherwise clear it.

This implements simple ko: the one point where a recapture would immediately recreate
the previous position.

### Export signatures

```go
var _ func() *Game = NewGame
var _ func([9][9]Color, Point) []Point = Group
var _ func([9][9]Color, Point) int = Liberties
var _ func(*Game, Point) error = PlaceStone
var _ func(*Game) = Pass
var _ func(*Game) []Point = ValidMoves
var _ func([9][9]Color) (int, int, int) = Territory
var _ func(*Game) (float64, float64) = Score
var _ func(*Game) Color = CheckWinner
var _ func(*Game) (Point, bool, error) = RandomAIMove
var _ func() = InitTemplates
var _ func(http.ResponseWriter, GameView) = RenderIndex
var _ func(http.ResponseWriter, GameView) = RenderBoard
```

---

## Behavioral Specification

**Group and Liberties** are pure read functions — they do not modify game state. They
accept the board array directly (not *Game) so they can be called on scratch copies.
Liberties counts distinct empty neighbors of any stone in the group (deduplication
required — a liberty shared by two group members counts once).

Group BFS pseudocode (group starts EMPTY — do not pre-load p):
```
func Group(b, p):
  if out-of-bounds(p) or b[p] == Empty: return nil
  color := b[p]
  visited := empty set
  queue := [p]
  group := []          // empty — p is added when it is dequeued, not here
  while queue not empty:
    curr := dequeue(queue)
    if curr in visited: continue
    visited.add(curr)
    group.append(curr) // add here, after dequeue, not before the loop
    for each orthogonal neighbor n of curr:
      if in-bounds(n) and b[n] == color and n not in visited:
        queue.append(n)
  return group
```
Do NOT initialize `group := []Point{p}` before the loop — that causes p to appear
twice (once at initialization and again when dequeued), making every group one element
too large.

**PlaceStone** composes the following steps in order:
1. Validate bounds and occupancy.
2. Reject if g.KoPoint != nil and p == *g.KoPoint.
3. Save g.Board as `saved`.
4. Place stone at p.
5. Remove all adjacent opponent groups with 0 liberties (captures).
6. **After captures:** if g's own group at p has 0 liberties → suicide → restore, return error.
7. **After captures:** if g.Board == g.PrevBoard → ko → restore, return error.
8. Commit: set PrevBoard=saved, LastMove=&p, ConsecutivePasses=0, update KoPoint, switch Turn.

Steps 6 and 7 MUST occur after step 5. A move that places into a zero-liberty position
but captures opponent stones first is legal (the captures free liberties). Checking
suicide before captures incorrectly rejects these moves. See TestPlaceStone/CaptureGrantsLiberty.

**ValidMoves** returns all empty intersections where PlaceStone would succeed. It must
test each candidate on a scratch copy of g (PlaceStone must not be called on g itself).
The scratch copy idiom: `scratch := *g` (struct copy; Board array copies by value).

**Territory** enumerates ALL empty cells to find every connected empty region:
```
func Territory(b):
  assigned := empty set  // tracks cells already assigned to a region
  black, white, dame := 0, 0, 0
  for r in 0..8:
    for c in 0..8:
      if b[r][c] != Empty or {r,c} in assigned: continue
      // start a new flood fill for this unvisited empty cell
      region, borderColors := floodFill(b, {r,c})
      assigned.addAll(region)
      if borderColors == {Black}: black += len(region)
      elif borderColors == {White}: white += len(region)
      else: dame += len(region)
  return black, white, dame
```
Do NOT call flood fill once from a single starting point — that finds only one region.
You must iterate every cell of the board and start a new flood fill for each empty cell
not yet assigned.

**Score** calls Territory(g.Board), counts stones of each color, and applies komi:
blackScore = black_stones + black_territory; whiteScore = white_stones + white_territory + 6.5.

**RandomAIMove** must not call PlaceStone on g directly. The scratch copy must be
re-declared inside the loop body for each candidate — not once before the loop:
```
for _, p := range candidates:
  scratch := *g          // fresh copy every iteration
  if PlaceStone(&scratch, p) == nil:
    PlaceStone(g, p)     // apply to real game
    return p, false, nil
```
If `scratch := *g` is declared once before the loop, each trial runs on an already-
modified board from the previous trial.

**Templates** are defined as Go string literals inside templates.go (no external .html
files). The "index" template embeds the "board" template via `{{template "board" .}}`.
The "board" template renders `<div id="board-container">` and is the HTMX swap target
for all POST responses.

The FuncMap must register:
- `colorClass(c Color) string` — returns `"empty"`, `"black"`, or `"white"`
- `isLastMove(r, c int, lm *Point) bool` — returns `lm != nil && lm.Row==r && lm.Col==c`
- `seq(n int) []int` — returns `[]int{0,1,...,n-1}` for range loops
- `add(a, b int) int` — addition for template arithmetic
- `formatScore(f float64) string` — formats score as "9" or "6.5" (no trailing zero for integers)

Inside the board template, `{{range}}` loops use `$` to access root fields:
`{{range seq 9}}{{$r := .}}{{range seq 9}}{{$c := .}}{{colorClass (index (index $.Board $r) $c)}}{{end}}{{end}}`

The board template must render each intersection as an HTML button (for clickable
placement) with `class="intersection {{colorClass ...}}"`. Empty intersections send
`hx-post="/place" hx-vals='{"row":"{{$r}}","col":"{{$c}}"}'`. Non-empty intersections
render as non-interactive spans.

**Handlers** use `toView(g *Game, msg string) GameView` to construct the view model
before every render call. `toView` calls `Territory(g.Board)` and `Score(g)` on every
invocation — acceptable given the 9×9 board size.

**HandlePlace** reads form values `row` and `col` as integers. These are sent as JSON
via HTMX `hx-vals`. Parse with `strconv.Atoi(r.FormValue("row"))` and
`strconv.Atoi(r.FormValue("col"))`.

---

## Domain-Specific Test Scenarios

### Coordinate system

```
Point{Row: 0, Col: 0} = top-left intersection
Point{Row: 0, Col: 8} = top-right intersection
Point{Row: 8, Col: 0} = bottom-left intersection
Point{Row: 8, Col: 8} = bottom-right intersection
```

4-directional adjacency: from Point{Row: r, Col: c}, the four neighbors are:
`{r-1,c}` (up), `{r+1,c}` (down), `{r,c-1}` (left), `{r,c+1}` (right).
Diagonal connections do NOT exist in Go.

### Required scenarios — group-detection bead

**TestLiberties/Corner:** Stone at `{0,0}` on an empty board.
Neighbors: `{0,1}` (Δrow=0,Δcol=1 ✓) and `{1,0}` (Δrow=1,Δcol=0 ✓). Liberties = 2.
Do NOT use 4 — `{-1,0}` and `{0,-1}` are off the board.

**TestLiberties/Center:** Stone at `{4,4}` on an empty board.
Neighbors: `{3,4}`,`{5,4}`,`{4,3}`,`{4,5}` — all Δ=1 in exactly one dimension ✓. Liberties = 4.

**TestGroup/TwoConnected:** Black stones at `{1,1}` and `{1,2}`.
Δrow=0, Δcol=1 between them ✓ (horizontally adjacent). Group returns both points.
Do NOT use `{1,1}` and `{2,2}`: Δrow=1,Δcol=1 — diagonal, NOT connected in Go.

**TestGroup/ColorBoundary:** Black stone at `{1,1}`, White stone at `{1,2}`.
Group({1,1}) returns only `{1,1}`. White stone is not part of Black group even though adjacent.

**TestGroup/EmptyReturnsNil:** `Group(b, {3,3})` on an empty board returns nil.

### Required scenarios — placement-capture bead

**TestPlaceStone/CornerCapture:** The minimal one-move capture.

```go
g := NewGame()
g.Board[0][0] = White     // target stone
g.Board[0][1] = Black     // pre-placed (Δrow=0,Δcol=1 from corner ✓)
g.Turn = Black
err := PlaceStone(g, Point{Row: 1, Col: 0})  // Δrow=1,Δcol=0 from corner ✓
// After: g.Board[0][0] == Empty (captured), err == nil
```

White at `{0,0}` has exactly 2 neighbors: `{0,1}` (Black, pre-placed) and `{1,0}` (Black, just placed).
Both occupied by Black → 0 liberties → captured.
Do NOT use `Point{Row:1,Col:1}`: Δrow=1,Δcol=1 — diagonal, NOT adjacent to `{0,0}`.

**TestPlaceStone/SuicideRejected:**

```go
g := NewGame()
g.Board[0][1] = Black
g.Board[1][0] = Black
g.Turn = White
err := PlaceStone(g, Point{Row: 0, Col: 0})
// err != nil: White at {0,0} has no liberties and captures nothing
```

**TestPlaceStone/CaptureGrantsLiberty:** A move that looks like suicide but captures first.

```go
g := NewGame()
// White group of 1 at {0,0}, Black stones surrounding except {1,0}
g.Board[0][0] = White
g.Board[0][1] = Black
// {1,0} is empty — that's where Black will play
// After Black plays {1,0}, White at {0,0} is captured, freeing {0,0} as a liberty
// for the Black group. NOT suicide.
g.Turn = Black
err := PlaceStone(g, Point{Row: 1, Col: 0})
// err == nil: capture happens before suicide check
```

**TestPlaceStone/KoRejected:**

```go
// Set up a ko position:
//   Row 0: _ B W _
//   Row 1: B W . W   (White just captured Black at {1,2}, KoPoint={1,2})
//   Row 2: _ B W _
g := NewGame()
g.Board[0][1] = Black; g.Board[0][2] = White
g.Board[1][0] = Black; g.Board[1][1] = White; /* {1,2} empty */ g.Board[1][3] = White
g.Board[2][1] = Black; g.Board[2][2] = White
koPoint := Point{Row: 1, Col: 2}
g.KoPoint = &koPoint
g.Turn = Black
err := PlaceStone(g, Point{Row: 1, Col: 2})
// err != nil: {1,2} is the ko point, cannot play here this turn
```

**TestPass:**

```go
g := NewGame()
Pass(g)
// g.Turn == White, g.ConsecutivePasses == 1, g.LastMove == nil, g.KoPoint == nil
Pass(g)
// g.ConsecutivePasses == 2
```

### Required scenarios — territory-scoring bead

**TestTerritory/EnclosedCorner:**

```go
var b [9][9]Color
// 8 Black stones form a 3×3 ring enclosing {1,1}:
b[0][0]=Black; b[0][1]=Black; b[0][2]=Black
b[1][0]=Black;               b[1][2]=Black
b[2][0]=Black; b[2][1]=Black; b[2][2]=Black
// {1,1} is empty, neighbors: {0,1}=B,{2,1}=B,{1,0}=B,{1,2}=B — all Black ✓
black, white, dame := Territory(b)
// black==1, white==0, dame==9*9-8-1==72
```

Verification: `{1,1}` neighbors are `{0,1}` (Δrow=-1,Δcol=0 ✓), `{2,1}` (Δrow=1,Δcol=0 ✓),
`{1,0}` (Δrow=0,Δcol=-1 ✓), `{1,2}` (Δrow=0,Δcol=1 ✓) — all Black.
Do NOT include `{0,0}`, `{0,2}`, `{2,0}`, `{2,2}` as part of `{1,1}`'s territory region —
they are stones, not empty intersections. Do NOT connect `{1,1}` to the exterior — it is
fully enclosed by the ring.

**TestTerritory/Dame:** An empty board has `black=0, white=0, dame=81`.
All 81 intersections form one connected empty region with no bordering stones → dame.

**TestTerritory/BothColorsBorderingIsDame:**

```go
var b [9][9]Color
b[0][0] = Black
b[0][2] = White
// {0,1} is adjacent to both Black at {0,0} and White at {0,2}:
// {0,0}: Δrow=0,Δcol=-1 ✓  {0,2}: Δrow=0,Δcol=1 ✓
black, white, dame := Territory(b)
// {0,1} borders both colors → dame (not Black or White territory)
// All other empty intersections connect to {0,1} (same region) → all dame
// black==0, white==0, dame==79
```

**TestScore/EndgamePosition:**

```go
g := NewGame()
// Same ring position as TestTerritory/EnclosedCorner:
g.Board[0][0]=Black; g.Board[0][1]=Black; g.Board[0][2]=Black
g.Board[1][0]=Black;                      g.Board[1][2]=Black
g.Board[2][0]=Black; g.Board[2][1]=Black; g.Board[2][2]=Black
g.ConsecutivePasses = 2  // game is over
black, white := Score(g)
// blackScore: 8 stones + 1 territory = 9.0
// whiteScore: 0 stones + 0 territory + 6.5 komi = 6.5
// assert black==9.0, white==6.5
winner := CheckWinner(g)
// winner == Black (9.0 > 6.5)
```

---

## Cross-Bead Contracts

### `GameView` data-shape (handlers → templates)

- **type:** data-shape
- **producer:** http-handlers
- **consumer:** templates
- **interface:**
  ```go
  type GameView struct {
      Board          [9][9]Color
      Turn           Color
      LastMove       *Point
      BlackStones    int
      WhiteStones    int
      BlackTerritory int
      WhiteTerritory int
      Dame           int
      BlackScore     float64
      WhiteScore     float64
      GameOver       bool
      Winner         Color
      Message        string
  }
  ```
- **notes:** Inside `{{range}}` loops, all root fields require `$` prefix:
  `$.Board`, `$.Turn`, `$.LastMove`, `$.GameOver`, etc. Access a board cell as
  `index (index $.Board $r) $c`. The FuncMap helpers `colorClass`, `isLastMove`,
  `seq`, `add`, and `formatScore` must be registered before parsing.

### Move POST protocol (templates → handlers)

- **type:** protocol
- **producer:** templates
- **consumer:** http-handlers (HandlePlace)
- **interface:** `POST /place` with form body `row=<int>&col=<int>`
- **notes:** The board template sends the row and col of each clicked empty intersection
  as integer form values via HTMX `hx-vals='{"row":"{{$r}}","col":"{{$c}}"}'`.
  HandlePlace reads them with `r.FormValue("row")` and `r.FormValue("col")` and
  converts with `strconv.Atoi`. Do NOT combine into a single field like `"r3c4"` or
  `"d5"` — the handler and template must agree on this two-field integer format.

### HandlePlace AI-response protocol

- **type:** protocol
- **producer:** ai (RandomAIMove)
- **consumer:** http-handlers (HandlePlace, HandlePass)
- **interface:** `func RandomAIMove(g *Game) (Point, bool, error)`
- **notes:** If the human's `PlaceStone` call in `HandlePlace` returns a non-nil error
  (illegal move), do not invoke this AI-response protocol at all: render current state
  (unchanged `g`) with `RenderBoard` and an error `Message`, HTTP 200.
  After a successful human PlaceStone or Pass:
  1. Call `CheckWinner(g)` — if non-Empty, render final state immediately (skip AI).
  2. Call `RandomAIMove(g)`. If `isPass==true`, call `Pass(g)`. Otherwise call
     `PlaceStone(g, aiMove)` (this should always succeed; log if it errors).
  3. Call `CheckWinner(g)` again for the AI's move.
  4. Render `RenderBoard`.
  If `RandomAIMove` returns an error (game already over), render current state without
  attempting to move.

### Template fragment contract

- **type:** data-shape
- **producer:** templates
- **consumer:** http-handlers
- **interface:** Named templates: `"index"` (full page, used by RenderIndex) and
  `"board"` (HTMX fragment wrapped in `<div id="board-container">`, used by RenderBoard
  and embedded in "index" via `{{template "board" .}}`).
- **notes:** The `<div id="board-container">` must contain ALL user-visible dynamic
  state: stone counts, territory counts, scores, turn indicator, game-over message,
  Pass button, New Game button. Nothing dynamic may be outside this div — it is the
  HTMX swap target for all POST responses (`hx-target="#board-container"`,
  `hx-swap="outerHTML"`).

---

## Decomposition Notes

The game functions form a clear dependency chain:
- `Group` and `Liberties` are foundational; `PlaceStone` and `ValidMoves` depend on them.
- `Territory` and `Score` depend only on the board type and are independently testable.
- `RandomAIMove` depends on `ValidMoves` and `PlaceStone`.
- Templates must be implemented before http-handlers so handler tests can render real HTML.

**Integration bead scope:** Start a game, place four Black stones to surround the single
White stone at `{1,1}` (place Black at `{0,1}`,`{1,0}`,`{1,2}`,`{2,1}` using alternating
moves), verify `{1,1}` is empty after the final capture. Call `PlaceStone(g, ...)`
directly for both Black and White moves — do NOT go through `HandlePlace`/
`RandomAIMove` for this scenario, since AI moves are random and would make the
scripted board position non-deterministic. Do not test scoring or ko in the
integration bead — those have their own unit tests.

**templates bead must come before http-handlers bead.** Handler tests use httptest and
require `InitTemplates()` to parse real templates. If templates bead runs after
http-handlers, handler tests will get stub output.

**Exit criterion for templates bead:** Must be a render test, not just a parse test.
Render the "board" template with a synthetic GameView (empty board, Black to move,
nil LastMove, GameOver=false) and verify the output contains exactly 81 occurrences of
the substring `class="intersection ` (note the trailing space — every rendered cell is
`class="intersection empty"`, `class="intersection black"`, or `class="intersection
white"`, never the bare closed literal `class="intersection"` with no trailing content).
`go build ./...` alone is not sufficient.

**Exit criterion for http-handlers bead:** Must use `net/http/httptest`. The smoke test
must call `InitTemplates()`, then `HandleIndex`, and verify the response contains
`id="board-container"` and at least one occurrence of the substring `class="intersection `
(same trailing-space note as above — never the bare closed literal). `go build ./...`
alone is not sufficient.

**The integration bead must write a new test file** (`integration_test.go`), not append
to `game_test.go` or `handlers_test.go`.
