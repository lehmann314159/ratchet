# Chess — Design Document

## Overview

A single-player chess server. The human plays White; the server plays Black using
a minimax search at depth 2 with alpha-beta pruning and material evaluation. The UI
is a single-page HTML app using HTMX — the human enters a move (from/to squares in
algebraic notation), the server validates it, applies it, runs the AI, and returns
the updated board fragment without a full-page reload.

Storage is in-memory; the game resets on server restart. Out of scope: draw detection
beyond stalemate (no 50-move rule, no threefold repetition, no insufficient material),
promotion choice UI (server auto-promotes to Queen), time controls, PGN export,
multi-game sessions.

**Domain parameters:**
- Board is 8×8. `Board[rank][file]` where rank 0 = rank 1 (White's back rank),
  rank 7 = rank 8 (Black's back rank), file 0 = file a, file 7 = file h.
- Human is always White; AI is always Black.
- Algebraic square notation: "a1"–"h8". File letter + rank digit. "e2" = file 4, rank 1.
- Promotion: `Move.Promotion` zero value (Pawn) means no promotion. PseudoLegalMoves
  emits four Move objects per promoting pawn (Knight, Bishop, Rook, Queen).
  HandleMove defaults Promotion to Queen when the parameter is absent.
- Server listens on :8080.

## Architecture

```
chess/
├── go.mod
├── main.go     — var game *Game, var templates *template.Template; func main() only
├── game.go     — Game, Square, Piece, Move, Color, PieceType, CastlingRights types;
│                 NewGame, ApplyMove
├── moves.go    — PseudoLegalMoves
├── check.go    — IsInCheck
├── legal.go    — LegalMoves, IsCheckmate, IsStalemate
├── ai.go       — Evaluate, BestMove
├── handlers.go — GameView type, HandleIndex, HandleMove, HandleReset
├── templates.go — InitTemplates, RenderIndex, RenderBoard (inline string templates only)
└── *_test.go   — one test file per source file above, plus integration_test.go
```

All `.go` files use `package main` at the project root — no subdirectories.

**File assignment rules (strict):**
- `main.go` contains exactly: `var game *Game`, `var templates *template.Template`,
  and `func main()`. Nothing else.
- `game.go` contains: all core types (`Game`, `Square`, `Piece`, `Move`, `Color`,
  `PieceType`, `CastlingRights`) plus `NewGame` and `ApplyMove`. No move generation,
  no check detection.
- `moves.go` contains: `PseudoLegalMoves` only. Does NOT call `IsInCheck`.
- `check.go` contains: `IsInCheck` only.
- `legal.go` contains: `LegalMoves`, `IsCheckmate`, `IsStalemate`.
- `ai.go` contains: `Evaluate`, `BestMove`.
- `handlers.go` contains: the `GameView` type and all three HTTP handlers
  (`HandleIndex`, `HandleMove`, `HandleReset`). No template parsing logic.
- `templates.go` contains: `InitTemplates`, `RenderIndex`, `RenderBoard`. No handler
  functions. No type declarations.
- Do NOT put `HandleIndex`, `HandleMove`, or `HandleReset` in `templates.go`.
- Do NOT put `GameView` in `templates.go` — it belongs in `handlers.go`.
- Do NOT put `PseudoLegalMoves` or `IsInCheck` in `legal.go` — see the Decomposition
  Notes dependency chain for why the split exists.

## Data Types and Function Signatures

All `.go` source files use `package main`. The module name is `chess`. Requires Go 1.22.

The following type choices are non-obvious and must be used consistently across all files:

```go
// Board cells are pointers so nil represents an empty square (no zero-value Piece ambiguity).
// Board[rank][file]: rank 0 = rank 1 (White's back rank), file 0 = file a.
type Game struct {
    Board          [8][8]*Piece
    // ...other fields: Turn Color, Castling CastlingRights,
    //    EnPassantSq *Square, HalfMoveClock int, FullMoveNumber int
}

// EnPassantSq is a pointer: nil means no en passant available.
// When set, it is the square the capturing pawn moves TO (the pass-through square),
// NOT the captured pawn's location. Example: White e2→e4 → EnPassantSq = e3.
// The captured pawn sits on e4 and is removed separately; the capturing pawn lands on e3.

// PieceType uses Pawn as the zero value — this doubles as the "no promotion" sentinel.
type PieceType int
const (
    Pawn PieceType = iota // zero value; also means "no promotion" in Move.Promotion
    Knight
    Bishop
    Rook
    Queen
    King
)

type Move struct {
    From      Square
    To        Square
    Promotion PieceType // Pawn (zero) = not a promotion; Knight/Bishop/Rook/Queen = promote to that piece
}

// GameView is the data passed to both the "index" and "board" templates.
type GameView struct {
    Board  [8][8]*Piece
    Turn   Color
    Status string // e.g. "White to move", "Check!", "Checkmate — Black wins", "Stalemate"
}

// Package-level singletons initialized by main() before serving.
var game      *Game
var templates *template.Template
```

## Behavioral Specification

**`NewGame() *Game`** — returns the standard starting position with White to move,
all four castling rights true, no en passant target, half-move clock 0, full-move
number 1.

**`ApplyMove(g *Game, m Move) *Game`** — returns a new Game with the move applied;
does not modify g. Does not validate legality. Handles all special cases:
- Castling: moves both king and rook to their destination squares.
- En passant: removes the captured pawn from the board (it is one rank behind m.To,
  not on m.To itself).
- Promotion: when m.Promotion != Pawn, replaces the pawn on m.To with that piece type.
- En passant target: sets EnPassantSq to the pass-through square (NOT the landing
  square) when a pawn advances two squares; clears it on all other moves.
  Example: White e2→e4 → EnPassantSq = e3 (captured pawn is on e4, never stored here).
- Castling rights: clears the relevant right when a king or rook moves, or when a
  capture lands on a rook's starting square. Use the destination square to determine
  which right to clear, not the type of the captured piece — if anything is captured
  at (rank=7,file=0), (rank=7,file=7), (rank=0,file=0), or (rank=0,file=7), clear the
  corresponding right unconditionally. (In a legal game the rook would have already
  moved, making this a no-op; the destination check handles edge cases correctly.)

**`PseudoLegalMoves(g *Game) []Move`** — returns all moves for g.Turn without
checking whether they leave g.Turn's king in check. Includes:
- Pawn: single push, double push from starting rank, diagonal captures, en passant
  captures (when g.EnPassantSq is non-nil and a pawn can reach it), promotions
  (four Move objects per promoting pawn: Knight, Bishop, Rook, Queen).
- Knight, Bishop, Rook, Queen, King: standard movement rules.
- Castling: generated when (a) the relevant CastlingRights flag is true, (b) no
  pieces stand between king and rook, and (c) the king is not currently in check.
  Condition (c) is checked in PseudoLegalMoves by examining whether any opponent
  pseudo-legal move attacks the king's current square — this avoids generating
  castling moves when already in check. When generating the opponent's moves for
  this check, exclude castling moves (castling is not an attack and including it
  would cause infinite recursion: each side's castling check would call
  PseudoLegalMoves on the other side indefinitely).

**`IsInCheck(g *Game, color Color) bool`** — returns true if color's king is
attacked by any opponent pseudo-legal move. Implementation: generate pseudo-legal
moves for the opponent on position g (with Turn temporarily set to the opponent),
then check whether any move's To square matches color's king square.

**`LegalMoves(g *Game) []Move`** — filters PseudoLegalMoves: applies each move via
ApplyMove, then calls IsInCheck on the resulting position for g.Turn. Keeps only
moves where the king is NOT in check after the move. For castling moves (where the
king moves two squares), also checks the intermediate king square: builds a
one-square king step to that square, applies it, and calls IsInCheck; discards the
castling move if either the intermediate or final position leaves the king in check.

**`IsCheckmate(g *Game) bool`** — LegalMoves(g) is empty AND IsInCheck(g, g.Turn).

**`IsStalemate(g *Game) bool`** — LegalMoves(g) is empty AND NOT IsInCheck(g, g.Turn).

**`Evaluate(g *Game) int`** — sums material on the board in centipawns; positive
favors White. Piece values: Pawn=100, Knight=320, Bishop=330, Rook=500, Queen=900,
King=20000. White pieces add; Black pieces subtract.

**`BestMove(g *Game) Move`** — negamax with alpha-beta pruning at depth 2. Returns
Move{} (zero value) if LegalMoves(g) is empty. Must NOT be called on a terminal
position; the caller (HandleMove) is responsible for checking IsCheckmate/IsStalemate
first.

**`InitTemplates() *template.Template`** — parses inline template strings (no
external .html files) and returns the result. Must register a `pieceClass` FuncMap
entry — a function `func(*Piece) string` that returns a CSS class string: `"empty"`
for nil, or `"<color> <type>"` for a piece (e.g. `"white pawn"`, `"black king"`).
Color is `"white"` or `"black"`; type is `"pawn"`, `"knight"`, `"bishop"`, `"rook"`,
`"queen"`, or `"king"`. Register the FuncMap before calling Parse. Panics on parse
failure.

**`RenderIndex`** — executes the "index" named template. **`RenderBoard`** —
executes the "board" named template; returned by HandleMove and HandleReset.

**`HandleMove`** — serves `POST /move`. Reads form fields `from` and `to`
(algebraic squares, e.g. "e2" / "e4") and optional `promotion` (default "q").
Returns HTTP 400 if the squares are unparseable or the move is not in LegalMoves.
After applying the human move, checks IsCheckmate/IsStalemate before calling
BestMove. If the game is not over, applies the AI's move. Calls RenderBoard with
the final state.

**`HandleReset`** — serves `POST /reset`. Sets `game = NewGame()` and calls
RenderBoard.

**`HandleIndex`** — serves `GET /`. Assembles GameView and calls RenderIndex.

**`main()`** — initializes `game = NewGame()` and `templates = InitTemplates()`,
registers routes on an http.ServeMux, calls `http.ListenAndServe(":8080", mux)`.

**Templates** — two named templates: `"index"` (full HTML page) and `"board"` (the
fragment). The "index" template embeds `{{template "board" .}}`. The "board"
template renders an 8×8 HTML table with rank 7 at the top, iterating ranks 7→0 and
files 0→7. Each cell is a `<td>` with `class="square {{pieceClass .}}"` and
contains a single capital letter for the piece type (P/N/B/R/Q/K) or a non-breaking
space for empty squares. The board must be visually styled: light squares `#f0d9b5`,
dark squares `#b58863`, each cell 64×64px, white piece letters in white with a dark
text-shadow, black piece letters in `#1a1a1a`. Status text, the move form, and the
reset button all live inside the "board" template.

**HTMX wiring** — `<head>` includes `<script src="https://unpkg.com/htmx.org@1.9.12"></script>`.
Move form: `hx-post="/move" hx-target="#board-container" hx-swap="outerHTML"`.
Reset button: `hx-post="/reset" hx-target="#board-container" hx-swap="outerHTML"`.
The outermost element of the "board" template is `<div id="board-container">` and
must contain all state that changes after a move (board, status, form).

## Domain-Specific Test Scenarios

**Test position conventions — algebraic notation required**: Every test file that
sets up a chess position must include a local helper that converts algebraic square
notation (e.g. `"e1"`) to the internal `Square` type. All position setup must go
through this helper; raw `Board[rank][file]` index literals are forbidden in test
bodies. This eliminates an entire class of bugs where 0-indexed rank/file diverges
from 1-based algebraic: the conversion `"e1" → Square{Rank:0, File:4}` is done once,
in one place, and every test is written in the notation a chess player can verify by
inspection. When writing a test position, state the algebraic squares and confirm the
geometry — e.g. "knight on c2 to a1: Δfile=−2, Δrank=−1, valid knight jump" — as a
comment next to the assertion.

**Required test scenarios for legal-moves** — use these exact positions; each
includes the geometric verification comment that confirms validity:

1. **Pinned piece cannot move**: White King on e1, Black Rook on a1, White Knight on
   d1 (all three on rank 0). The rook attacks the king along rank 0 with the knight
   between them. Every move from d1 must be absent from LegalMoves (the knight is
   pinned — moving it exposes the king). `// d1=rank0,file3; a1=rank0,file0; e1=rank0,file4 — all rank 0`

2. **Move that removes check is legal**: White King on e1, Black Rook on a1 (checking
   the king along rank 0), White Knight on c2. The jump c2→a1 (Δrank=−1, Δfile=−2)
   is a valid knight move and captures the checking rook. This move must appear in
   LegalMoves. `// c2=rank1,file2; a1=rank0,file0; Δrank=−1,Δfile=−2 — valid knight jump`
   *(Using d2 instead of c2 gives Δrank=−1, Δfile=−3, which is not a valid knight
   jump — the test would require a move that can never be generated.)*

3. **Castling rejected when intermediate square is attacked**: White King on e1, White
   Rook on h1, kingside castling right true, Black Bishop on c4. The bishop on c4
   attacks f1 along the diagonal c4–d3–e2–f1 (Δrank=−3, Δfile=+3, |Δrank|=|Δfile|).
   The f1 intermediate square is attacked, so kingside castling (e1→g1) must be absent
   from LegalMoves. `// c4=rank3,file2; f1=rank0,file5; Δrank=−3,Δfile=+3 — valid bishop diagonal`
   *(A bishop on f4 is on the same file as f1, not a diagonal — f4→f1 is Δrank=−3,
   Δfile=0, which is a rook move. f4 does not attack f1 at all.)*

4. **Castling allowed when path is safe**: White King on e1, White Rook on h1,
   kingside castling right true, no Black pieces. Kingside castling must appear in
   LegalMoves.

**Required test scenarios for IsCheckmate and IsStalemate** — use these exact
positions:

5. **Detect checkmate**: White King on a1, Black Rook on a2, Black Rook on b2. King
   is in check from the a2 rook (same file: a1 and a2 are both file a). Escape
   analysis: a2 is occupied, and after a king capture there, the b2 rook still covers
   a2 (same rank 1); b1 is covered by the b2 rook (same file b); b2 can be captured
   but the a2 rook covers b2 (same rank 1) so the king walks into check. No legal
   moves. IsCheckmate must return true. `// a1=rank0,file0; a2=rank1,file0; b2=rank1,file1`
   *(The buggy version uses a2+b1. With b1 at rank0,file1 and the a2 rook at
   rank1,file0, the a2 rook does NOT cover b1 — different rank and file — so the king
   can legally capture b1, making it not checkmate.)*

6. **Detect stalemate**: White King on a1, Black Queen on c2. King is not in check
   (c2 does not attack a1: Δrank=−1,Δfile=−2, not same rank/file/diagonal). King's
   three adjacent squares are all controlled: a2 via queen's rank-1 coverage; b1 via
   the c2→b1 diagonal (Δrank=−1, Δfile=−1); b2 via queen's rank-1 coverage. No legal
   moves. IsStalemate must return true. `// a1=rank0,file0; c2=rank1,file2`

**Required test scenarios for pseudo-moves** — use these exact positions; each
includes the geometric verification comment:

7. **Bishop sliding moves**: Board cleared. King at e1 (rank=0, file=4), White Bishop
   at a3 (rank=2, file=0). The {+1,+1} diagonal from a3: b4(Δ1,+1)→c5(Δ2,+2)→
   d6(Δ3,+3)→e7(Δ4,+4)→f8(Δ5,+5). King at e1 is NOT on this path (rank 0, file 4).
   Assert both moves present: a3→b4 (Δrank=1,Δfile=1 ✓) and a3→f8 (Δrank=5,Δfile=5 ✓).
   `// a3=rank2,file0; b4=rank3,file1; f8=rank7,file5; e1=rank0,file4 — king off diagonal`
   *(Common mistake: bishop at c1→h8 is Δrank=7,Δfile=5, NOT a diagonal. Or bishop at
   c1→h8 via direction {+1,+1} only reaches h6=rank5,file7, not h8=rank7,file7.)*

8. **Queen sliding moves**: Board cleared. King at e1 (rank=0, file=4), White Queen
   at a3 (rank=2, file=0). Assert all three ray types present:
   a3→h3 (Δrank=0,Δfile=7 ✓ — rook ray along rank 2),
   a3→a8 (Δrank=5,Δfile=0 ✓ — rook ray along file 0),
   a3→f8 (Δrank=5,Δfile=5 ✓ — diagonal {+1,+1}).
   King at e1 is on none of these rays.
   `// a3=rank2,file0; h3=rank2,file7; a8=rank7,file0; f8=rank7,file5`
   *(Common mistake: queen at d1 cannot reach a8 — Δrank=7,Δfile=−3 is neither a
   diagonal nor a rook ray. Queen at d1 also cannot reach h1 because the king at e1
   blocks the rook ray along rank 0.)*

**Required test scenario for game-state**: `TestApplyMove` must include a sub-test
verifying that when any non-rook piece captures on a rook's starting square, the
corresponding castling right is cleared. Example: place a black knight on b3
(Rank 2, File 1) and a white piece on a1 (Rank 0, File 0); apply the knight's capture
move to a1; assert `g.Castling.WhiteQueenSide` is false afterwards. This tests the
"destination square, not piece type" rule — the most common implementation error is
checking whether the captured piece was a rook instead of checking the destination
square unconditionally.

## Cross-Bead Contracts

### game-state → pseudo-moves (data-shape)

- **type**: data-shape
- **producer**: game-state (game.go)
- **consumer**: pseudo-moves (moves.go)
- **interface**: the full `Game` struct (especially `Board [8][8]*Piece`, `EnPassantSq *Square`,
  `Castling CastlingRights`), `Square`, `Piece`, `Move`, `Color`, `PieceType` constants,
  `ApplyMove(g *Game, m Move) *Game`
- **notes**: PseudoLegalMoves reads Board cells as `*Piece` (nil = empty). It uses
  ApplyMove only for the castling pre-check (condition c). It does NOT call IsInCheck.

### pseudo-moves → is-in-check (protocol)

- **type**: protocol
- **producer**: pseudo-moves (moves.go)
- **consumer**: is-in-check (check.go)
- **interface**: `PseudoLegalMoves(g *Game) []Move`
- **notes**: IsInCheck generates opponent pseudo-legal moves by calling PseudoLegalMoves
  on a copy of g with Turn set to the opponent, then checks if any Move.To equals
  color's king square.

### is-in-check → legal-moves (protocol)

- **type**: protocol
- **producer**: is-in-check (check.go)
- **consumer**: legal-moves (legal.go)
- **interface**: `IsInCheck(g *Game, color Color) bool`
- **notes**: LegalMoves calls PseudoLegalMoves(g), then for each candidate calls
  ApplyMove + IsInCheck on the resulting position. Castling also requires the
  intermediate-square check described in the Behavioral Specification.

### legal-moves → ai (protocol)

- **type**: protocol
- **producer**: legal-moves (legal.go)
- **consumer**: ai (ai.go)
- **interface**: `LegalMoves(g *Game) []Move`, `ApplyMove(g *Game, m Move) *Game`,
  `Evaluate(g *Game) int`
- **notes**: BestMove calls LegalMoves to enumerate candidates, ApplyMove to advance
  the position, and Evaluate at leaf nodes. Returns Move{} when LegalMoves is empty.

### legal-moves → http-handlers (protocol)

- **type**: protocol
- **producer**: legal-moves (legal.go)
- **consumer**: http-handlers (handlers.go)
- **interface**: `LegalMoves(g *Game) []Move`, `IsCheckmate(g *Game) bool`,
  `IsStalemate(g *Game) bool`
- **notes**: HandleMove validates the human move against LegalMoves. It must call
  IsCheckmate/IsStalemate before calling BestMove — BestMove must not be called on
  a terminal position.

### http-handlers → templates (data-shape)

- **type**: data-shape
- **producer**: http-handlers (assembles GameView)
- **consumer**: templates (templates.go)
- **interface**: `GameView{Board [8][8]*Piece, Turn Color, Status string}`
- **notes**: The "board" template iterates `Board[rank][file]` with rank 7 at the
  top. Each cell is `<td class="square {{pieceClass .}}">` where `pieceClass` returns
  `"empty"` for nil or `"white pawn"` / `"black king"` etc. for pieces. Status
  encodes all terminal states verbatim. All changing state must be inside
  `#board-container`.

## Decomposition Notes

**Critical dependency chain — do not reorder these four beads:**

The check-detection logic creates a dependency that makes bead ordering non-negotiable:

1. **game-state**: defines all types + `ApplyMove`. No move generation.
2. **pseudo-moves**: implements `PseudoLegalMoves`. Does NOT call `IsInCheck`.
3. **is-in-check**: implements `IsInCheck` only → `check.go` + `check_test.go`.
   Calls `PseudoLegalMoves` (bead 2). Single function; isolated bead.
4. **legal-moves**: implements `LegalMoves`, `IsCheckmate`, `IsStalemate` → `legal.go` + `legal_test.go`.
   Calls `IsInCheck` (bead 3), `PseudoLegalMoves` (bead 2), and `ApplyMove` (bead 1).

`IsInCheck` works by calling `PseudoLegalMoves` on the opponent's position. If
`PseudoLegalMoves` also called `IsInCheck` to filter its own output, the two
functions would be mutually recursive with no base case. The split breaks the cycle:
pseudo-legal moves are generated without check filtering; legal moves filter
pseudo-legal moves by post-move check detection.

**`IsInCheck` implementation — Color toggling**: `Color` is `type Color int` with
`White = 0` and `Black = 1` (iota). To get the opponent color, use `1 - color` or
an explicit conditional. Do NOT use `^color` — bitwise NOT on a signed int produces
`-1` for White, which is not a valid Color and causes `PseudoLegalMoves` to generate
no moves, making `IsInCheck` always return false.

**Castling intermediate square**: LegalMoves must check both the intermediate king
square and the destination square. Applying the full castling move only catches the
king landing in check — not passing through an attacked square. The legal-moves bead
spec must include a test that verifies castling is rejected when the path is attacked.

**ApplyMove belongs in game-state**: Both legal-moves (check detection) and ai (tree
search) call ApplyMove. It must be defined before both beads run. See "Domain-Specific
Test Scenarios" above for the required castling-rights-clearing test.

**http-handlers owns both handlers.go and main.go**: main() is the only wiring point
for `game`, `templates`, and the ServeMux routes.

**Templates use inline Go string literals**: no external .html files. The `pieceClass`
FuncMap entry must be registered before Parse is called. Tests for the templates bead
must assert structural HTML properties (element IDs, CSS class attribute values like
`class="white pawn"`) — not Unicode characters or displayed text content.

**Integration bead scenario** (bounded): using httptest.NewServer, POST /move with
`from=e2&to=e4`, assert the response contains `id="board-container"`, assert the
response contains `"White to move"` (AI has responded and it is White's turn again),
assert the response contains at least one `class="black` element (AI placed a piece).
Must not bind to a fixed port.
