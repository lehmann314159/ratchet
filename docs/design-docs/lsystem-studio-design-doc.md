# L-System Studio — Design Document

## Overview

A single-user web application for exploring **Lindenmayer systems** (L-systems). The user
writes an L-system in a small text DSL — an axiom, a set of production rules, and a few
numeric settings — picks an iteration count, and sees the rewritten string rendered as a
2-D line drawing via turtle graphics. Rendered drawings are emitted as **SVG** (Go standard
library string formatting only — no raster image code). The user can save a drawing they
like to a server-side directory whose contents show as a gallery on the page, and can load
one of several built-in preset systems (Koch curve, Koch snowflake, Heighway dragon,
Hilbert curve, fractal plant) into the editor.

The DSL is **parametric and deterministic**: a module can carry numeric arguments
(`F(2.5)`, `A(1.0, 0.3)`), and a production's right-hand side computes each new module's
arguments with a small **arithmetic expression language** (`+ - * /`, unary minus,
parentheses, parameter references, float literals) evaluated against the matched module's
argument values.

**Runtime model:** one HTTP server process (`package main`, listens on `:8080`), serving an
HTML UI updated with HTMX fragment swaps (`html/template` partials returned from handlers —
no client-side JavaScript beyond the HTMX library itself, no browser-swap behavior asserted
in tests). Saved drawings persist as `.svg` files in a `saved/` directory relative to the
working directory. No user accounts, no sessions, no database.

**Out of scope** (do not implement):
- **Stochastic productions** (a symbol with several weighted right-hand sides), **context-
  sensitive productions** (`a < b > c`), and **conditional productions** (`A(x) : x>0 -> …`).
  Every rule is an unconditional context-free parametric production.
- 3-D turtle interpretation, tropism/gravity, polygon/surface (`{` `}`) symbols, the
  cut symbol (`%`), coloured or variable-width strokes.
- Raster output (PNG/JPEG). Output is SVG only. No `image`, `image/png`, or any
  pixel-level drawing.
- Animation, incremental derivation display, or showing the intermediate strings. Only the
  final drawing for the chosen iteration count is rendered.
- Re-loading a *saved* drawing's source back into the editor. The gallery is view-only.
  (Loading a *preset* into the editor is in scope — that is the `POST /select` flow.)
- User-adjustable canvas size. The canvas is always 800×800.
- Functions in the expression language (`sin`, `cos`, `pow`, …). Arithmetic operators only.

**Domain parameters** (stated so nothing is guessed):
- **Canvas:** always 800×800. `CanvasSize = 800`, `Margin = 20`, compile-time constants,
  never read from a request.
- **Turtle coordinate space.** The turtle works in ordinary math convention: position is a
  `float64` `(x, y)` pair, heading is in **degrees**, heading `0` points along **+X**
  (right), and a **positive** angle turns **counter-clockwise**. `+Y` is **up**. The
  renderer flips `Y` when mapping to SVG canvas coordinates (SVG `+Y` is down) — that flip
  happens in exactly one place, `RenderSVG`. See the mapping table in Domain-Specific Test
  Scenarios.
- **Turtle start.** Position `(0, 0)`, heading `System.Heading` (the `heading:` setting,
  default `0`).
- **Default step / angle.** A bare `F` or `f` moves `System.Step` units (the `step:`
  setting, default `1`). A bare `+` or `-` turns `System.Angle` degrees (the `angle:`
  setting, default `90`). A parenthesised argument overrides the default for that one
  module: `F(3)` moves 3, `+(25)` turns 25.
- **Iteration count.** The `iterations:` setting (default `0`) is the number of parallel
  rewrite steps. It is **clamped to `[0, 12]`**. The web form's iteration field overrides
  the setting and is clamped the same way.
- **Derivation size cap.** If a derivation ever exceeds **2,000,000 modules**, it stops
  with the error `system too large (>2000000 modules)`. `MaxModules = 2000000`.
- **Number format.** All DSL numeric literals parse with `strconv.ParseFloat(s, 64)`
  (so `2`, `2.5`, `.5` are all valid). `iterations:` parses with `strconv.Atoi`.
- **SVG number format.** Path coordinates are formatted `%.3f` (three decimal places,
  never scientific notation).
- **Stroke.** Black (`stroke="black"`), width 1, no fill, on a white background rectangle.
- **Saved files.** `SaveSVG` writes the SVG text to `filepath.Join(dir, name)` where
  `name` is `fmt.Sprintf("%019d.svg", now.UnixNano())` — a nanosecond timestamp
  zero-padded to at least 19 digits plus `.svg`. Zero-padding makes lexical filename order
  match chronological order. `ListSaved` returns every `*.svg` file in the directory,
  **newest first** (reverse lexical filename order).

## Architecture

```
lsystem/
├── go.mod                — module lsystem, Go 1.22
├── main.go               — var templates *template.Template; func main() only
├── expr.go               — Token, TokenKind + constants, Lex, Expr + node types,
│                            ParseExpr, EvalExpr
├── grammar.go            — Module, BodyModule, Rule, System, ParseSystem
├── rewrite.go            — MaxModules constant, Derive
├── turtle.go             — Segment, Interpret
├── render.go             — CanvasSize, Margin constants, Box, BBox, RenderSVG
├── studio.go             — Studio (the parse→derive→interpret→render pipeline)
├── save.go               — SavedImage, SaveSVG, ListSaved, SavedDir constant, saveMu
├── templates.go          — InitTemplates, RenderPage, RenderResult
├── handlers.go           — Preset, Presets, PageView, DefaultIterations, assemble,
│                            HandleIndex, HandleRender, HandleSelect, HandleSave
└── *_test.go             — one test file per source file above, plus integration_test.go
```

All `.go` files use `package main` at the project root — a single flat package, no
subdirectories. `go.mod` and `do_not_use_this_test.go` are generated automatically by the
scaffolding step — do not list them as SURVEY outputs.

**File assignment rules (strict):**
- `main.go` contains exactly: `var templates *template.Template` and `func main()`.
  Nothing else — no types, no handlers, no constants.
- `expr.go` contains: `Token`, `TokenKind` and its constants, `Lex`, the `Expr` interface
  and its node types (`NumLit`, `Ref`, `Neg`, `BinOp`), `ParseExpr`, `EvalExpr`. No
  L-system concepts (no `Module`, no `Rule`), no turtle, no HTTP.
- `grammar.go` contains: `Module`, `BodyModule`, `Rule`, `System`, `ParseSystem`. It
  **uses** `Lex`, `ParseExpr`, `EvalExpr`, and the `Expr` type from `expr.go` (same
  package — no import). It does **not** define token or expression types.
- `rewrite.go` contains: `MaxModules`, `Derive`. It uses `System`, `Rule`, `BodyModule`,
  `Module` (from `grammar.go`) and `EvalExpr` (from `expr.go`). No parsing, no turtle.
- `turtle.go` contains: `Segment`, `Interpret`. It uses `Module` (from `grammar.go`). No
  parsing, no rewriting, no SVG, no `Box`/`BBox`.
- `render.go` contains: `CanvasSize`, `Margin`, `Box`, `BBox`, `RenderSVG`. It uses
  `Segment` (from `turtle.go`). It does **not** contain `Interpret` and does **not** call
  the turtle — it only consumes `[]Segment`.
- `studio.go` contains: `Studio`. It calls `ParseSystem`, `Derive`, `Interpret`,
  `RenderSVG` in that order. It defines no types.
- `save.go` contains: `SavedImage`, `SaveSVG`, `ListSaved`, `const SavedDir = "saved"`,
  `var saveMu sync.Mutex`. No HTTP handlers.
- `templates.go` contains: `InitTemplates`, `RenderPage`, `RenderResult`. No handler
  functions, no type declarations.
- `handlers.go` contains: `Preset`, `var Presets []Preset`, `PageView`,
  `const DefaultIterations`, `assemble`, `HandleIndex`, `HandleRender`, `HandleSelect`,
  `HandleSave`. No template parsing, no re-implementation of `Studio`/`Derive`/`Interpret`.
- Do NOT put `Interpret` in `render.go` or `RenderSVG` in `turtle.go`.
- Do NOT put `Module`/`Rule`/`System` in `expr.go` — they belong in `grammar.go`.
- Do NOT put `Token`/`Expr` in `grammar.go` — they belong in `expr.go`.
- Do NOT put `PageView` or `Preset` in `templates.go` — they belong in `handlers.go`.
- Do NOT put any `Handle*` function in `templates.go` or `studio.go`.
- Do NOT put `var templates` anywhere except `main.go`.

## Data Types and Function Signatures

All `.go` source files use `package main`. Module name is `lsystem`. Requires Go 1.22.

```go
// ---- expr.go ----

type TokenKind int

const (
    TokNum    TokenKind = iota // a numeric literal, ParseFloat-compatible
    TokIdent                    // a parameter name (one or more letters)
    TokPlus                     // +
    TokMinus                   // -
    TokStar                    // *
    TokSlash                   // /
    TokLParen                  // (
    TokRParen                  // )
    TokComma                   // ,
)

type Token struct {
    Kind TokenKind
    Text string // the exact source substring
}

// Lex tokenizes one expression string (the text between a module's parentheses).
// Recognises numbers, letter-runs (identifiers), the operators + - * /, parens, and
// commas. Whitespace separates tokens and is otherwise discarded. Any other character
// is an error.
func Lex(src string) ([]Token, error)

type Expr interface{ isExpr() }

type NumLit struct{ Val float64 }        // a literal, already parsed
type Ref    struct{ Name string }        // a reference to a bound parameter
type Neg    struct{ X Expr }             // unary minus
type BinOp  struct {                     // a binary operation
    Op   byte // one of '+' '-' '*' '/'
    L, R Expr
}

// All four node types satisfy Expr with VALUE receivers: func (NumLit) isExpr() {},
// func (Ref) isExpr() {}, func (Neg) isExpr() {}, func (BinOp) isExpr() {}. ParseExpr
// returns and nests them as values, never pointers — `-x` parses to
// Neg{X: Ref{Name: "x"}}, `a-b` to BinOp{Op: '-', L: Ref{Name: "a"}, R: Ref{Name: "b"}},
// `2` to NumLit{Val: 2}. EvalExpr type-switches on the value types (case NumLit:, …).

// ParseExpr parses one full expression starting at toks[pos]. It returns the Expr and
// the index of the first token it did NOT consume (so the caller can look for a comma
// or the end of the list). Grammar, all operators left-associative:
//
//     expr   := term  (('+' | '-') term)*
//     term   := factor (('*' | '/') factor)*
//     factor := NUMBER | IDENT | '(' expr ')' | '-' factor
//
// Unary minus is a factor prefix, so it binds tighter than '*' and '/'.
func ParseExpr(toks []Token, pos int) (expr Expr, next int, err error)

// EvalExpr evaluates e. env maps a parameter name to its value. A Ref to a name not in
// env is an error ("unknown parameter %q"). Division whose right operand evaluates to
// exactly 0 is an error ("division by zero").
func EvalExpr(e Expr, env map[string]float64) (float64, error)

// ---- grammar.go ----

// Module is a concrete module in a derived string: a one-byte symbol plus its evaluated
// numeric arguments. The bracket tokens are Module{Sym: '['} and Module{Sym: ']'} with
// no args.
type Module struct {
    Sym  byte
    Args []float64
}

// BodyModule is a module on a production's right-hand side: a symbol plus argument
// expressions, evaluated at rewrite time under the matched module's parameter binding.
// Brackets are BodyModule{Sym: '['} / {Sym: ']'} with no args.
type BodyModule struct {
    Sym  byte
    Args []Expr
}

type Rule struct {
    Head   byte     // the module symbol this rule rewrites
    Params []string // formal parameter names; len(Params) is the arity this rule matches
    Body   []BodyModule
}

type System struct {
    Angle      float64  // default turn for a bare + or -           (setting "angle", default 90)
    Step       float64  // default distance for a bare F or f       (setting "step",  default 1)
    Heading    float64  // turtle's starting heading in degrees     (setting "heading", default 0)
    Iterations int      // parallel rewrite steps, clamped to [0,12] (setting "iterations", default 0)
    Axiom      []Module
    Rules      []Rule
}

// ParseSystem parses a whole source document: blank lines and lines beginning with '#'
// are ignored; a line containing "->" is a production rule; any other non-blank line
// must be "key: value" for key in {angle, step, heading, iterations, axiom}. axiom is
// required. Returns an error (with a 1-based line number) on any malformed line, an
// unknown key, a missing axiom, or an axiom argument that is not a constant expression.
// Iterations is clamped to [0, 12] before returning.
func ParseSystem(src string) (System, error)

// ---- rewrite.go ----

const MaxModules = 2_000_000

// Derive applies sys.Rules to sys.Axiom for exactly `iterations` parallel rewrite steps
// and returns the resulting module string. See the Behavioral Specification for the
// matching and parallelism rules. Returns an error if the string ever exceeds
// MaxModules, or if evaluating a body argument fails. The returned slice and every
// Module.Args slice within it are freshly allocated and independent of sys.Axiom — even
// when iterations == 0 (in which case each axiom module is copied, not aliased).
func Derive(sys System, iterations int) ([]Module, error)

// ---- turtle.go ----

type Segment struct{ X1, Y1, X2, Y2 float64 }

// Interpret walks mods with a turtle and returns the drawn line segments in turtle
// space (+Y up). angleDefault and stepDefault are used for bare +/-/F/f. headingStart
// is the turtle's initial heading in degrees. See the Behavioral Specification for the
// per-symbol semantics. Returns an error on ']' with an empty stack, or on an F/f/+/-
// module with two or more arguments. On any error the first return is nil — segments
// computed before the error are discarded. (Only the non-error leftover-'[' case
// returns a non-nil slice.)
func Interpret(mods []Module, angleDefault, stepDefault, headingStart float64) ([]Segment, error)

// ---- render.go ----

const CanvasSize = 800
const Margin = 20

type Box struct{ MinX, MinY, MaxX, MaxY float64 }

// BBox returns the bounding box over every endpoint of every segment, and false in the
// second return iff segs is empty.
func BBox(segs []Segment) (Box, bool)

// RenderSVG returns a complete SVG document (800×800) with a white background and one
// black polyline path. The drawing is uniformly scaled and centred to fit inside the
// canvas with a Margin-pixel border, flipping Y (turtle +Y up → SVG +Y down). With no
// segments it returns the SVG with the background rect and no path. See the Behavioral
// Specification for the exact transform and path format.
func RenderSVG(segs []Segment) string

// ---- studio.go ----

// Studio runs the whole pipeline: ParseSystem(source) → Derive(sys, iterations) →
// Interpret(mods, sys.Angle, sys.Step, sys.Heading) → RenderSVG(segs). iterations is
// clamped to [0, 12] and overrides sys.Iterations. Returns the SVG string, or the first
// error from any stage.
func Studio(source string, iterations int) (string, error)

// ---- save.go ----

const SavedDir = "saved"

var saveMu sync.Mutex

type SavedImage struct {
    Filename string // base name within SavedDir, e.g. "1700000000123456789.svg"
    Title    string // display label; equal to Filename in this version
}

// SaveSVG writes svg to filepath.Join(dir, name) where
// name = fmt.Sprintf("%019d.svg", now.UnixNano()). Holds saveMu for the write. Returns
// name (the base filename, .svg included) on success, or ("", err) if the directory is
// not writable.
func SaveSVG(svg string, dir string, now time.Time) (string, error)

// ListSaved returns every *.svg entry in dir as a SavedImage, sorted newest first
// (reverse lexical order by Filename). A missing directory returns (nil, nil).
func ListSaved(dir string) ([]SavedImage, error)

// ---- handlers.go ----

const DefaultIterations = 4

type Preset struct {
    Name   string // URL/identifier form, e.g. "plant"
    Label  string // human label, e.g. "Fractal plant"
    Source string // the full DSL source for this preset
}

var Presets []Preset // koch, snowflake, dragon, hilbert, plant — in this order

type PageView struct {
    Source     string        // current editor contents
    SVG        template.HTML  // the rendered drawing, injected as trusted markup
    Iterations int            // current iteration-count field value
    Presets    []Preset       // == Presets, for the <select>
    Saved      []SavedImage   // ListSaved(SavedDir) output — newest first
    Error      string         // non-empty → a parse/derive/interpret error to show inline
}

func HandleIndex(w http.ResponseWriter, r *http.Request)  // GET /
func HandleRender(w http.ResponseWriter, r *http.Request) // POST /render
func HandleSelect(w http.ResponseWriter, r *http.Request) // POST /select
func HandleSave(w http.ResponseWriter, r *http.Request)   // POST /save

// ---- templates.go ----

// InitTemplates parses the two inline templates ("page" and "result") and returns the
// set. It panics on a parse error.
func InitTemplates() *template.Template

// RenderPage executes the "page" template (full HTML document) with v, writing to w.
func RenderPage(w http.ResponseWriter, v PageView)

// RenderResult executes the "result" template (the #app fragment only) with v, writing
// to w. This is the body of every POST /render, POST /select, and POST /save response.
func RenderResult(w http.ResponseWriter, v PageView)

// ---- main.go ----

var templates *template.Template
```

### Export signatures

```go
var _ TokenKind = TokNum
var _ func(string) ([]Token, error) = Lex
var _ func([]Token, int) (Expr, int, error) = ParseExpr
var _ func(Expr, map[string]float64) (float64, error) = EvalExpr
var _ func(string) (System, error) = ParseSystem
var _ func(System, int) ([]Module, error) = Derive
var _ func([]Module, float64, float64, float64) ([]Segment, error) = Interpret
var _ func([]Segment) (Box, bool) = BBox
var _ func([]Segment) string = RenderSVG
var _ func(string, int) (string, error) = Studio
var _ func(string, string, time.Time) (string, error) = SaveSVG
var _ func(string) ([]SavedImage, error) = ListSaved
var _ func(http.ResponseWriter, *http.Request) = HandleIndex
var _ func(http.ResponseWriter, *http.Request) = HandleRender
var _ func(http.ResponseWriter, *http.Request) = HandleSelect
var _ func(http.ResponseWriter, *http.Request) = HandleSave
var _ func() *template.Template = InitTemplates
var _ func(http.ResponseWriter, PageView) = RenderPage
var _ func(http.ResponseWriter, PageView) = RenderResult
var _ *template.Template = templates
var _ int = CanvasSize
var _ int = Margin
var _ int = MaxModules
var _ int = DefaultIterations
var _ string = SavedDir
var _ []Preset = Presets
```

## Behavioral Specification

The pipeline is a five-stage chain: **Lex/ParseExpr/EvalExpr** (the expression sub-
language) → **ParseSystem** (source text → `System`) → **Derive** (parallel rewriting) →
**Interpret** (turtle) → **RenderSVG** (fit + SVG). `Studio` composes stages 2–5. Each
stage is pure and independently testable; each is a natural bead seam. `ParseSystem`
depends on the expression stage; `Derive` depends on `ParseSystem`'s output types and on
`EvalExpr`; `Interpret` depends on `Derive`'s output type; `RenderSVG` depends on
`Interpret`'s output type.

### The expression sub-language (`Lex`, `ParseExpr`, `EvalExpr`)

**`Lex(src)`** — a single left-to-right scan. Whitespace (space, tab) is skipped. `+ - *
/ ( ) ,` each become one token. A run of digits and `.` becomes a `TokNum` whose `Text`
is the exact substring. A run of Unicode letters becomes a `TokIdent`. Any other rune is
an error.

**`ParseExpr(toks, pos)`** — recursive descent over the grammar in the signature comment.
Returns `(expr, next, err)` where `next` is the index one past the last consumed token.
It does **not** require that it consume all tokens — the caller (grammar's argument-list
parser) checks for a following comma or end. `-` at the start of a factor is unary minus
(`Neg`); `-` between terms is subtraction (`BinOp{Op:'-'}`). `+ - * /` are all
**left-associative**: `10 - 3 - 2` parses as `(10 - 3) - 2`.

**`EvalExpr(e, env)`** — straightforward recursive evaluation. `Ref{Name}` looks `Name`
up in `env`; absent → error. `BinOp{Op:'/'}` with a right operand that evaluates to
exactly `0.0` → error. All arithmetic is `float64`. No rounding, no clamping.

The most natural wrong implementations:
- **Right-associative subtraction / division** — parsing `10-3-2` as `10-(3-2)=9`. The
  loop in `expr := term (('+'|'-') term)*` must fold **left**: keep the running result on
  the left and combine with each new right term.
- **Unary minus at the wrong precedence** — `-2*3` must be `-6` (whether you read it as
  `(-2)*3` or `-(2*3)` the result is the same here; but `-` must be accepted as a factor
  prefix so `A(-x)` and `A(2 * -3)` parse at all).
- **Consuming trailing tokens** — after parsing one argument, `ParseExpr` must stop at the
  comma, not error on it.

### `ParseSystem(src string) (System, error)`

Split `src` on `"\n"`. For each line, `strings.TrimSpace` it, then:
1. Empty, or starts with `#` → skip.
2. Contains `"->"` → a **rule** (below).
3. Otherwise it must contain `":"`. The key is everything before the first `:`
   (trimmed), the value everything after (trimmed). Keys:
   - `angle`, `step`, `heading` → `strconv.ParseFloat(value, 64)` into the field.
   - `iterations` → `strconv.Atoi(value)` into the field.
   - `axiom` → parse `value` as a **module sequence** (below) in which every argument
     expression must evaluate under the **empty** environment (a constant); an axiom
     argument that references a parameter is an error.
   - any other key → error.
4. A non-blank line that is neither a comment, a rule, nor `key: value` → error.

After all lines: a missing `axiom` is an error. `Iterations` is clamped: `< 0 → 0`,
`> 12 → 12`. Defaults for unset settings: `Angle 90`, `Step 1`, `Heading 0`,
`Iterations 0`.

**Rule syntax.** `head -> body`, split on the first `->`. The `head` substring **before**
the first `(` (or all of `head` when there is no `(`) must be **exactly one ASCII
letter** — an empty head (`-> body`), a multi-letter head (`AB -> …`), or a non-letter
head is a malformed-line error reported with its 1-based line number (never a panic and
never a silent truncation to the first character). After that one letter, an optional
`(name, name, …)` gives the formal parameters; a `(` not matched by a trailing `)` is an
error, and an empty parameter name is an error. `Rule.Head` is that one letter;
`Rule.Params` is the parameter names in order; the rule's arity is `len(Params)`.

**Module sequence syntax** (used for `axiom` and rule bodies). A left-to-right scan:
- whitespace is skipped;
- `[` and `]` each emit a bracket module;
- a letter, or `+`, or `-`, is a module symbol; if the **very next** character is `(`,
  everything up to the matching `)` (tracking nested parens) is the argument text —
  otherwise the module has no arguments;
- any other character is an error.
- Argument text is split into comma-separated expressions with `Lex` + `ParseExpr`. An
  empty argument list (`F()`) yields no args. A trailing comma, or two commas in a row,
  is an error.

**The `+`/`-` ambiguity is resolved by context.** At the module-sequence level, a leading
`+` or `-` is a **module symbol** (turn). Inside a module's parentheses we are parsing an
**expression**, so `+` and `-` are **operators**. `+(a)` is the module `+` with one
argument expression `a`. `A(s-1)` is the module `A` with one argument `s-1`. `A(-x)` is
the module `A` with one argument `-x` (unary minus).

### `Derive(sys System, iterations int) ([]Module, error)`

```
cur := sys.Axiom
repeat `iterations` times:
    next := empty
    for each module m in cur:                       # iterate the string as it was at the
        if m.Sym is '[' or ']':                       #   START of this step (parallel!)
            append m to next
            continue
        rule := the first rule with rule.Head == m.Sym AND len(rule.Params) == len(m.Args)
        if no such rule:
            append m to next                          # identity: unmatched modules copy through
            continue
        env := { rule.Params[i] : m.Args[i]  for each i }
        for each BodyModule bm in rule.Body:
            if bm.Sym is '[' or ']':
                append Module{Sym: bm.Sym} to next
                continue
            vals := [ EvalExpr(e, env) for e in bm.Args ]   # propagate any error
            append Module{Sym: bm.Sym, Args: vals} to next
        if len(next) > MaxModules: return error "system too large (>2000000 modules)"
    cur := next
return cur
```

Load-bearing points a natural implementation gets wrong:
- **Rewriting is parallel, not sequential.** All replacements in one step read the string
  as it was at the *start* of the step. Do not rewrite in place, and do not feed a
  module's replacement back into the same step's scan. Building a fresh `next` list from a
  read-only pass over `cur` (as the pseudocode does) is the correct shape.
- **Matching requires equal arity, not just equal symbol.** A rule `A(s) -> …` (arity 1)
  matches `A(1.0)` but **not** bare `A` (arity 0) and **not** `A(1, 2)` (arity 2). An
  unmatched module is copied unchanged. So axiom `A A(2)` with only the rule
  `A(s) -> F(s)` derives (after one step) to `A F(2)` — the bare `A` is untouched.
- **`[` and `]` are always copied verbatim** — there are never productions for them.
- **Identity for unmatched modules** — `F` with no `F -> …` rule copies through every
  step (this is how `F` reaches the turtle in systems like the fractal plant, whose only
  `F` rule is `F -> FF`).
- **The result must not alias `sys.Axiom`.** When `iterations == 0`, still return a fresh
  slice with each axiom module *copied* (`Module{Sym: m.Sym, Args: append([]float64(nil), m.Args...)}`),
  not `sys.Axiom` itself. Callers may keep and inspect the result independently of the
  `System`.

### `Interpret(mods, angleDefault, stepDefault, headingStart) ([]Segment, error)`

Turtle state: `x, y float64` (position), `h float64` (heading, degrees). A stack of
`(x, y, h)` triples. Start: `x = 0`, `y = 0`, `h = headingStart`, empty stack.

For each module `m`, on `m.Sym`:
- **`F`** — let `d` = `m.Args[0]` if it has an argument, else `stepDefault`. Compute
  `nx = x + d*math.Cos(h*math.Pi/180)`, `ny = y + d*math.Sin(h*math.Pi/180)`. Append
  `Segment{x, y, nx, ny}`. Set `x, y = nx, ny`.
- **`f`** — same movement as `F` (same `d`, same formulas), but **append no segment**.
- **`+`** — let `a` = `m.Args[0]` if present, else `angleDefault`. `h += a`.
- **`-`** — let `a` = `m.Args[0]` if present, else `angleDefault`. `h -= a`.
- **`[`** — push a copy of `(x, y, h)` onto the stack.
- **`]`** — if the stack is empty → error `']' with empty stack`. Otherwise pop and
  restore **all three** of `x`, `y`, `h` from the popped triple.
- **any other symbol** (`A`, `B`, `X`, `Y`, …) — do nothing. These letters exist only to
  drive rewriting; the turtle ignores them.

For `F`, `f`, `+`, `-`: if `len(m.Args) >= 2` → error
`module "X" takes at most one argument`.

A `[` with no matching `]` (stack non-empty at the end) is **not** an error — the
segments drawn so far are still returned.

On any error (`]` empty stack, `F`/`f`/`+`/`-` with ≥2 args) `Interpret` returns
`(nil, err)` — any segments already appended are dropped, not returned alongside the
error.

Load-bearing points:
- **`]` restores position AND heading, from a stack.** The natural wrong versions: (a)
  restore only position, leaving the heading wherever the branch left it; (b) keep a
  single saved `(x, y, h)` instead of a stack, so nested `[ [ … ] ]` corrupts. Use a
  real LIFO stack and restore the whole triple.
- **`f` still moves.** It is not a no-op; it advances the turtle without drawing, which
  matters for the next `F`'s start point.
- **Angle is in degrees; convert to radians for `math.Cos`/`math.Sin`** with
  `h * math.Pi / 180`.
- **Non-`F`/`f`/`+`/`-`/`[`/`]` letters are silently skipped** — do not error on them,
  do not draw for them.

### `BBox` and `RenderSVG`

**`BBox(segs)`** — over every `(X1,Y1)` and every `(X2,Y2)` of every segment: min and max
of each axis. `(Box{}, false)` iff `segs` is empty.

**`RenderSVG(segs)`** returns exactly (no leading/trailing whitespace, all one line):

```
<svg xmlns="http://www.w3.org/2000/svg" width="800" height="800" viewBox="0 0 800 800"><rect width="800" height="800" fill="white"/>PATH</svg>
```

where `PATH` is empty when `segs` is empty, and otherwise:

```
<path d="D" fill="none" stroke="black" stroke-width="1"/>
```

`D` is the space-joined concatenation, one entry per segment in order, of
`fmt.Sprintf("M %.3f %.3f L %.3f %.3f", mapX(s.X1), mapY(s.Y1), mapX(s.X2), mapY(s.Y2))`.
(Each segment gets its own `M … L …` — the path does not try to chain consecutive
segments.)

The fit transform, given `b, _ := BBox(segs)`:
```
usable := float64(CanvasSize - 2*Margin)          // 760
bw := b.MaxX - b.MinX
bh := b.MaxY - b.MinY
sx := +Inf; if bw > 0 { sx = usable / bw }
sy := +Inf; if bh > 0 { sy = usable / bh }
s := math.Min(sx, sy)
if math.IsInf(s, 1) { s = 1 }                       // degenerate: a single point
ox := float64(Margin) + (usable - bw*s) / 2
oy := float64(Margin) + (usable - bh*s) / 2
mapX(tx) := ox + (tx - b.MinX) * s
mapY(ty) := oy + (b.MaxY - ty) * s                  // note: MaxY - ty  → Y is flipped
```

Load-bearing points:
- **`mapY` uses `b.MaxY - ty`, not `ty - b.MinY`.** Turtle `+Y` is up; SVG `+Y` is down.
  Getting this wrong flips every drawing vertically (invisible on the vertically-symmetric
  Koch snowflake, obvious and wrong on the plant and dragon).
- **Uniform scale.** `s` is the *same* for both axes (`math.Min` of the two per-axis
  fits) — the drawing keeps its aspect ratio. Scaling X and Y independently stretches it.
- **Centre the leftover space.** `ox`/`oy` add half of the unused width/height so the
  drawing is centred, not pinned to the top-left.
- **Degenerate box.** If all points coincide (`bw == bh == 0`), `s` would be `+Inf`;
  clamp it to `1`. `ox = oy = Margin + usable/2 = 400`.

### `Studio(source string, iterations int) (string, error)`

```
sys, err := ParseSystem(source);      if err != nil { return "", err }
it := iterations; if it < 0 { it = 0 }; if it > 12 { it = 12 }
mods, err := Derive(sys, it);          if err != nil { return "", err }
segs, err := Interpret(mods, sys.Angle, sys.Step, sys.Heading)
                                       if err != nil { return "", err }
return RenderSVG(segs), nil
```

`Studio` **always** uses its `iterations` argument (clamped), ignoring `sys.Iterations` —
the web form is the single source of truth for the count once the app is running.

### `SaveSVG` / `ListSaved`

**`SaveSVG(svg, dir, now)`** — holds `saveMu` for the whole call. `name :=
fmt.Sprintf("%019d.svg", now.UnixNano())`. Writes `svg` (as bytes) to
`filepath.Join(dir, name)`. On any filesystem error returns `("", err)`; on success
`(name, nil)`.

**`ListSaved(dir)`** — reads `dir`. If it does not exist, returns `(nil, nil)`. Collects
every entry whose name ends in `.svg` into `SavedImage{Filename: name, Title: name}`,
sorts the slice by `Filename` **descending**, returns it.

### Handlers

`InitTemplates` is called once in `main`; handlers use the package-level `templates`.
Templates are inline Go string literals (see Templates), `InitTemplates` panics on a
parse error.

Every `POST` handler calls `r.ParseForm()` and **ignores its error** — proceed with
whatever fields parsed (`r.PostForm.Get` returns `""` for anything missing, and an empty
`source` then flows through `Studio` as an ordinary parse error shown inline). Never
return an HTTP error for a `ParseForm` failure.

All four handlers build a `PageView` the same way — call this **assemble(source, svg,
iterations, errMsg)**:
- `Source = source`
- `SVG = template.HTML(svg)` (empty string when there is nothing to show)
- `Iterations = iterations`
- `Presets = Presets`
- `Saved, _ = ListSaved(SavedDir)` (ignore the error — a broken gallery must not break
  the page)
- `Error = errMsg`

**`HandleIndex`** (`GET /`) — `src := Presets[0].Source`; `svg, err := Studio(src,
DefaultIterations)`; `RenderPage(w, assemble(src, svg, DefaultIterations, errString(err)))`
where `errString` is `""` when `err == nil` else `err.Error()`. (Preset 0 is a valid
system, so `err` is nil in practice, but handle it uniformly.)

**`HandleRender`** (`POST /render`) — `r.ParseForm()`. `source := r.PostForm.Get("source")`.
`iters := parseIters(r.PostForm.Get("iterations"))` where `parseIters` is
`strconv.Atoi`; on error or a value `< 0` use `0`; a value `> 12` becomes `12`.
`svg, err := Studio(source, iters)`. Respond **HTTP 200** with
`RenderResult(w, assemble(source, svg, iters, errString(err)))` — a `Studio` error is
shown inline (`PageView.Error`), **not** an HTTP error status, because editing a
half-finished system is the normal case. On error `svg` is `""`.

**`HandleSelect`** (`POST /select`) — `r.ParseForm()`. Look up `r.PostForm.Get("preset")`
in `Presets` by `Name`. Not found → `http.Error(w, "unknown preset",
http.StatusBadRequest)` and return. Found → `src := preset.Source`;
`svg, err := Studio(src, DefaultIterations)`;
`RenderResult(w, assemble(src, svg, DefaultIterations, errString(err)))`. Selecting a
preset **replaces** the editor contents and resets the iteration field to
`DefaultIterations`.

**`HandleSave`** (`POST /save`) — `r.ParseForm()`. Same `source` / `iters` extraction as
`HandleRender`. `svg, err := Studio(source, iters)`. If `err != nil`: respond 200 with
`assemble(source, "", iters, err.Error())` — a broken system cannot be saved, show the
error. Otherwise `_, saveErr := SaveSVG(svg, SavedDir, time.Now())`; if `saveErr != nil`
respond `http.Error(w, saveErr.Error(), http.StatusInternalServerError)` and return. On
success respond 200 with `RenderResult(w, assemble(source, svg, iters, ""))` — the same
`#app` fragment as `/render`, whose gallery now includes the just-saved file.

### Templates

`InitTemplates()` parses inline Go string literals into a `*template.Template` with
exactly two named templates, `"page"` and `"result"`. It `panic`s on a parse error. No
FuncMap is needed.

- **`"result"`** — the entire dynamic fragment, wrapped in a single
  `<div id="app"> … </div>`. In order:
  1. `<form hx-post="/render" hx-target="#app" hx-swap="outerHTML">` containing:
     - `<select name="preset" hx-post="/select" hx-trigger="change" hx-target="#app" hx-swap="outerHTML">`
       with one `<option value="{{.Name}}">{{.Label}}</option>` per `{{range .Presets}}`.
       Changing the select fires `POST /select` (not `/render`).
     - `<textarea name="source" rows="14" cols="64">{{.Source}}</textarea>`
     - `<input name="iterations" type="number" min="0" max="12" value="{{.Iterations}}">`
     - `<button type="submit">Render</button>`
     - `<button type="submit" hx-post="/save">Save</button>` — same enclosing form, so it
       submits the same `source` and `iterations`, but to `/save`. Both buttons inherit
       the form's `hx-target="#app"` / `hx-swap="outerHTML"`.
  2. `{{if .Error}}<p class="error">{{.Error}}</p>{{end}}`
  3. `<div class="viz">{{.SVG}}</div>` — `.SVG` is `template.HTML`, so the `<svg>…</svg>`
     markup is injected as-is, not escaped.
  4. A gallery: `{{range .Saved}}<figure><img src="/saved/{{.Filename}}" width="150" alt="{{.Title}}"><figcaption>{{.Title}}</figcaption></figure>{{end}}`.
     Inside this range `.` is the `SavedImage`.
- **`"page"`** — the full document: `<!doctype html>`, a `<head>` with
  `<script src="https://unpkg.com/htmx.org@1.9.12"></script>` and a `<title>`, and a
  `<body>` with an `<h1>` and then `{{template "result" .}}`.

**Swap discipline:** every `POST /render`, `POST /select`, and `POST /save` response body
is exactly the `"result"` fragment and replaces `#app` via `hx-swap="outerHTML"`. Because
the fragment re-declares `<div id="app">` and re-renders the whole form (with the current
`source`/`iterations` as the values), the editor, the drawing, the error line, and the
gallery all update together with no JavaScript. **All dynamic state lives inside `#app`.**

### `main()`

`templates = InitTemplates()`; `os.MkdirAll(SavedDir, 0o755)` (log-fatal on error); build
an `*http.ServeMux` with `GET /{$}` → `HandleIndex`, `POST /render` → `HandleRender`,
`POST /select` → `HandleSelect`, `POST /save` → `HandleSave`, and
`GET /saved/` → `http.StripPrefix("/saved/", http.FileServer(http.Dir(SavedDir)))`;
then `http.ListenAndServe(":8080", mux)`.

## Domain-Specific Test Scenarios

### Coordinate systems

Two coordinate spaces, related only inside `RenderSVG`:

<!-- ambiguity class 1 waived 2026-09-06: this IS the definitional coordinate-system
     table the guide's "Coordinate system mapping" advice calls for. The checkable
     worked anchor immediately below (heading 0 -> +(90) -> F draws to (1,1) = up/+Y),
     its verified comment, and the `turtle` bead pin together fix exactly one reading.
     The independent design-doc-ambiguity-check pass reviewed this area and found it
     unambiguous. Do not reword the table cells. -->

| | origin | +X | +Y | angle 0 | positive angle |
|---|---|---|---|---|---|
| **turtle space** (`Segment`, `Box`) | turtle start | right | **up** | points +X | **counter-clockwise** |
| **SVG canvas** (path `d`) | top-left | right | **down** | — | — |

`RenderSVG` maps turtle `(tx, ty)` to canvas `(ox + (tx - MinX)*s, oy + (MaxY - ty)*s)`.
The `MaxY - ty` (not `ty - MinY`) is the only Y-flip in the program.

Worked anchor for the turtle table: from `(0, 0)` heading `0`, `F(1)` draws to `(1, 0)`
(east); then `+(90)` sets the heading to `90` and another `F(1)` draws to `(1, 1)` — the
second move went **up** (`+Y` increased), confirming that `+` turns counter-clockwise and
`+Y` is up. `-(90)` from heading `0` would instead point the turtle at heading `-90` and
`F(1)` would draw toward `(0+ε, -1)` (down).
<!-- verified: go run scratchpad/refimpl (verify.go, "TURTLE: F +90 F") =>
     start (0,0) h=0; F(1) -> seg {0,0,1,0}; +(90) -> h=90; F(1) -> seg {1,0,1,1}
     (math.Cos(90*Pi/180)=6.12e-17, 1+that == 1.0 exactly; math.Sin(90*Pi/180)=1) -->


### Required test scenarios for the `expr` bead (`ParseExpr` + `EvalExpr`)

Evaluate the parsed expression under the given environment. All results are exact
`float64`.

- `2+3*4` (empty env) → `14` — precedence: `*` before `+`. Do NOT get `20`.
- `-2*3` (empty env) → `-6` — unary minus is accepted as a factor prefix.
- `1/3` (empty env) → `0.3333333333333333` — plain `float64` division.
- `10-3-2` (empty env) → `5` — subtraction is **left**-associative: `(10-3)-2`. Do NOT
  get `9` (`10-(3-2)`).
- `2-(3-1)` (empty env) → `0` — parentheses override.
- `s*0.6+a` with `env = {s: 2, a: 10}` → `11.2`.
- `-s` with `env = {s: 5}` → `-5`.
- `2*-3` (empty env) → `-6` — `-` accepted directly after `*`.
- `s+1` with **empty** env → error (`unknown parameter "s"`).
- `1/0` (empty env) → error (`division by zero`).
<!-- verified: go run scratchpad/refimpl (verify.go, "EXPR") =>
     2+3*4=14  -2*3=-6  1/3=0.3333333333333333  10-3-2=5  2-(3-1)=0
     s*0.6+a {s:2,a:10}=11.2  -s {s:5}=-5  2*-3=-6  .5=0.5
     's+1' empty env => unknown parameter "s"   '1/0' => division by zero -->


### Required test scenarios for the `rewrite` bead (`Derive`)

- **Non-parametric (Lindenmayer's algae).** `axiom: A`, rules `A -> AB`, `B -> A`. As a
  symbol-only string (no arguments): step 0 `A`; step 1 `AB`; step 2 `ABA`; step 3
  `ABAAB`; step 4 `ABAABABA`. (Lengths 1, 2, 3, 5, 8.)
- **Parametric.** `axiom: A(1)`, rule `A(s) -> F(s) A(s*0.5)`. Step 1: modules
  `F(1) A(0.5)`. Step 2: `F(1) F(0.5) A(0.25)`. Step 3: `F(1) F(0.5) F(0.25) A(0.125)`.
- **Parametric with a branch.** `axiom: A(1)`, rule
  `A(s) -> F(s)[+(25)A(s*0.6)][-(25)A(s*0.6)]`. Step 1 modules, in order:
  `F(1)`, `[`, `+(25)`, `A(0.6)`, `]`, `[`, `-(25)`, `A(0.6)`, `]`.
- **Arity discrimination.** `axiom: A A(2)`, rule `A(s) -> F(s)`. Step 1: `A F(2)` — the
  bare `A` (arity 0) does not match the arity-1 rule and is copied unchanged.
- **Identity for unmatched.** `axiom: F`, no rules, any iteration count → `F` unchanged.
<!-- verified: go run scratchpad/refimpl (verify.go, "REWRITE" sections) =>
     algae A/AB/ABA/ABAAB/ABAABABA (len 1,2,3,5,8);
     A(1)->F(1)A(0.5)->F(1)F(0.5)A(0.25)->F(1)F(0.5)F(0.25)A(0.125);
     A(1) w/ A(s)->F(s)[+(25)A(s*0.6)][-(25)A(s*0.6)] step1 => F(1)[+(25)A(0.6)][-(25)A(0.6)];
     axiom 'A A(2)' w/ A(s)->F(s) step1 => AF(2) (bare A untouched);
     iterations:30 clamps to 12; branching system hits >2000000 => "system too large (>2000000 modules)" -->


### Required test scenarios for the `turtle` bead (`Interpret`)

All start at `(0, 0)` heading `0`, with `angleDefault = 90`, `stepDefault = 1`.

- **`F(1) +(90) F(1)`** → two segments: `{0,0, 1,0}` then `{1,0, 1,1}`. (After `+(90)` the
  heading is `90`; `math.Cos(90*math.Pi/180)` is `6.12e-17` and `1 + 6.12e-17` rounds to
  exactly `1.0` in `float64`, and `math.Sin(90*math.Pi/180)` is exactly `1`, so the
  second endpoint is exactly `(1, 1)`.)
- **`F(1) [ +(90) F(1) ] F(1)`** → **three** segments: `{0,0, 1,0}`, `{1,0, 1,1}`,
  `{1,0, 2,0}`. The third segment starts at `(1, 0)` with heading `0` — proving `]`
  restored **both** the position (back to `(1,0)`, not left at `(1,1)`) **and** the
  heading (back to `0`, not left at `90`). Do NOT get a third segment `{1,1, 2,1}`
  (position not restored) or `{1,0, 1,-1}` (heading not restored).
- **Bare `F + F`** (no arguments) → identical to `F(1) +(90) F(1)`: `{0,0,1,0}`,
  `{1,0,1,1}` — bare symbols use `stepDefault` / `angleDefault`.
- **`]` with an empty stack** → error.
- **`F(1,2)`** → error (`module "F" takes at most one argument`).
- **`X F(1)`** (a non-turtle letter) → one segment `{0,0, 1,0}`; the `X` is skipped.
<!-- verified: go run scratchpad/refimpl (verify.go, "TURTLE" sections) =>
     F(1)+(90)F(1) => segs {0,0,1,0},{1,0,1,1};
     F(1)[+(90)F(1)]F(1) => 3 segs {0,0,1,0},{1,0,1,1},{1,0,2,0};
     bare 'F + F' => {0,0,1,0},{1,0,1,1};
     ']' empty stack => error;  F(1,2) => "module \"F\" takes at most one argument";
     leftover '[' at end => no error, segs returned -->


### Required test scenarios for the `render` bead (`BBox`, `RenderSVG`)

- **`BBox`** of `[{0,0, 2,0}, {2,0, 2,1}]` → `Box{MinX:0, MinY:0, MaxX:2, MaxY:1}`,
  `true`. `BBox(nil)` → `Box{}`, `false`.
- **Fit transform** for that box (`CanvasSize 800`, `Margin 20`, so `usable = 760`):
  `bw = 2`, `bh = 1`, `sx = 380`, `sy = 760`, `s = 380`, `ox = 20`, `oy = 210`. Mapped:
  `(0,0) → (20, 590)`, `(2,1) → (780, 210)`, `(2,0) → (780, 590)`.
- **`RenderSVG([{0,0, 1,0}])`** → exactly
  `<svg xmlns="http://www.w3.org/2000/svg" width="800" height="800" viewBox="0 0 800 800"><rect width="800" height="800" fill="white"/><path d="M 20.000 400.000 L 780.000 400.000" fill="none" stroke="black" stroke-width="1"/></svg>`
  (single horizontal segment: `bw = 1`, `bh = 0` → `s = 760`, `ox = 20`,
  `oy = 20 + (760-0)/2 = 400`; `mapY(0) = 400 + (0 - 0)*760 = 400`).
- **`RenderSVG(nil)`** → exactly
  `<svg xmlns="http://www.w3.org/2000/svg" width="800" height="800" viewBox="0 0 800 800"><rect width="800" height="800" fill="white"/></svg>`
  — background rect, no `<path>`.
<!-- verified: go run scratchpad/refimpl (verify.go, "RENDER" sections) =>
     BBox([{0,0,2,0},{2,0,2,1}]) = {MinX:0 MinY:0 MaxX:2 MaxY:1}, true;
     fitTransform: usable=760, bw=2 bh=1, sx=380 sy=760, s=380, ox=20, oy=210;
     map (0,0)->(20,590) (2,1)->(780,210) (2,0)->(780,590);
     square box 0..1: s=760 ox=20 oy=20;  single point: s=1 ox=400 oy=400;
     RenderSVG([{0,0,1,0}]) and RenderSVG(nil) exact strings as pinned -->


### Required test scenario for the `studio` bead (`Studio`)

- **`Studio("axiom: F(1) +(90) F(1)\n", 0)`** → `(svg, nil)` where `svg` is exactly
  `<svg xmlns="http://www.w3.org/2000/svg" width="800" height="800" viewBox="0 0 800 800"><rect width="800" height="800" fill="white"/><path d="M 20.000 780.000 L 780.000 780.000 M 780.000 780.000 L 780.000 20.000" fill="none" stroke="black" stroke-width="1"/></svg>`.
  (Two segments `{0,0,1,0}` and `{1,0,1,1}`; box `0..1 × 0..1`; `s = 760`, `ox = oy = 20`;
  `(0,0)→(20,780)`, `(1,0)→(780,780)`, `(1,1)→(780,20)`.)
- **`Studio("angle: 90\naxiom: A\nA -> AB\nB -> A\n", 3)`** → `("<svg …/></svg>", nil)`
  with **no `<path>`** — the derived string `ABAAB` has no turtle-drawing symbols, so
  there are zero segments.
- **`Studio("nonsense", 0)`** → `("", err)` with a non-nil error.
<!-- verified: go run scratchpad/refimpl (verify.go, "RENDER: full SVG ... it=0") =>
     Studio("axiom: F(1) +(90) F(1)\n", 0) = exact SVG with
     d="M 20.000 780.000 L 780.000 780.000 M 780.000 780.000 L 780.000 20.000";
     presets koch/snowflake/dragon/hilbert/plant all parse+derive+render with no error
     (koch 5/125/3125 segs at it 1/3/5; plant 3/84/1488; dragon 2/8/32; hilbert 3/63/1023) -->


## Cross-Bead Contracts

### expr → grammar (data-shape)

- **type**: data-shape
- **producer**: expr (`expr.go`)
- **consumer**: grammar (`grammar.go`)
- **interface**: `type Token struct { Kind TokenKind; Text string }`, `type Expr interface{ isExpr() }`, `func Lex(src string) ([]Token, error)`, `func ParseExpr(toks []Token, pos int) (Expr, int, error)`, `func EvalExpr(e Expr, env map[string]float64) (float64, error)`
- **notes**: `grammar.go`'s module-sequence parser, when it reads a module's parenthesised
  argument text, calls `Lex` on that text and then `ParseExpr` repeatedly (once per
  comma-separated argument), checking that `ParseExpr`'s returned `next` index lands on a
  comma or the end of the token slice. For `axiom` arguments it then calls
  `EvalExpr(expr, map[string]float64{})` and treats an error (e.g. an unbound parameter)
  as a parse error. Same package — no import; `expr.go` must be decomposed before
  `grammar.go` so grammar's tests run against the real lexer/parser/evaluator.

### grammar → rewrite (data-shape)

- **type**: data-shape
- **producer**: grammar (`grammar.go`)
- **consumer**: rewrite (`rewrite.go`)
- **interface**: `type Module struct { Sym byte; Args []float64 }`, `type BodyModule struct { Sym byte; Args []Expr }`, `type Rule struct { Head byte; Params []string; Body []BodyModule }`, `type System struct { Angle, Step, Heading float64; Iterations int; Axiom []Module; Rules []Rule }`, `func ParseSystem(src string) (System, error)`
- **notes**: `Derive` reads `sys.Axiom` and `sys.Rules` only (not `sys.Iterations` — the
  caller passes the count). It matches a `Module` against a `Rule` by `Sym == Head` **and**
  `len(Args) == len(Params)`, binds `Params[i] → Args[i]`, and calls
  `EvalExpr(bodyModule.Args[j], binding)` for each body-module argument. `'['` / `']'`
  modules (`Sym` is the ASCII bracket byte, `Args` empty) are copied verbatim and never
  matched.

### grammar → turtle (data-shape)

- **type**: data-shape
- **producer**: grammar (`grammar.go`) — via rewrite
- **consumer**: turtle (`turtle.go`)
- **interface**: `type Module struct { Sym byte; Args []float64 }`
- **notes**: `Interpret` switches on `Module.Sym`. It acts on `'F'`, `'f'`, `'+'`, `'-'`,
  `'['`, `']'` and **ignores every other byte value** (letters used only for rewriting).
  For `'F'`/`'f'`/`'+'`/`'-'` it reads `Args[0]` when `len(Args) == 1`, uses the supplied
  default when `len(Args) == 0`, and errors when `len(Args) >= 2`.

### turtle → render (data-shape)

- **type**: data-shape
- **producer**: turtle (`turtle.go`)
- **consumer**: render (`render.go`)
- **interface**: `type Segment struct { X1, Y1, X2, Y2 float64 }`
- **notes**: segments are in turtle space (`+Y` up). `BBox` spans every `X1,Y1,X2,Y2`.
  `RenderSVG` is the sole place that flips `Y` (`mapY(ty) = oy + (MaxY - ty)*s`) and the
  sole place that scales/centres. `render.go` must not call `Interpret` or re-derive
  anything — it consumes `[]Segment` and nothing else.

### pipeline composition → studio (protocol)

- **type**: protocol
- **producer**: expr + grammar + rewrite + turtle + render
- **consumer**: studio (`studio.go`)
- **interface**: `func Studio(source string, iterations int) (string, error)`
- **notes**: `Studio` calls, in this exact order: `ParseSystem(source)` →
  clamp `iterations` to `[0, 12]` → `Derive(sys, clampedIterations)` →
  `Interpret(mods, sys.Angle, sys.Step, sys.Heading)` → `RenderSVG(segs)`. It returns
  `("", err)` on the first stage that errors, and `(svg, nil)` otherwise. It ignores
  `sys.Iterations`. The `studio` bead's exit criterion runs the whole chain on a fixed
  source (see Domain-Specific Test Scenarios) and asserts the exact SVG string.

### studio → handlers (protocol)

- **type**: protocol
- **producer**: studio (`studio.go`)
- **consumer**: handlers (`handlers.go`)
- **interface**: `func Studio(source string, iterations int) (string, error)`
- **notes**: `HandleRender` and `HandleSave` both call `Studio(source, iters)` where
  `iters` comes from `strconv.Atoi` of the `iterations` form field (parse failure or a
  negative → `0`; `> 12` → `12`). `HandleSelect` and `HandleIndex` call
  `Studio(presetSource, DefaultIterations)`. **On a `Studio` error, every handler responds
  HTTP 200** with the `"result"` fragment and `PageView.Error = err.Error()` and
  `PageView.SVG = ""` — a malformed L-system is expected user input, not an HTTP error.
  `HandleSave` additionally must **not** call `SaveSVG` when `Studio` errored.

### save → handlers (protocol)

- **type**: protocol
- **producer**: save (`save.go`)
- **consumer**: handlers (`handlers.go`)
- **interface**: `func SaveSVG(svg string, dir string, now time.Time) (string, error)`, `func ListSaved(dir string) ([]SavedImage, error)`, `type SavedImage struct { Filename, Title string }`, `const SavedDir = "saved"`
- **notes**: `HandleSave` calls `SaveSVG(svg, SavedDir, time.Now())` only after a
  successful `Studio`; a non-nil error → HTTP 500, no fragment. On success it falls
  through to the `"result"` fragment, whose gallery is rebuilt via `ListSaved(SavedDir)`
  and therefore includes the new file. `HandleIndex`, `HandleRender`, `HandleSelect`,
  `HandleSave` **all** call `ListSaved(SavedDir)` and **ignore its error** (a broken
  gallery must not break the page — treat as empty).

### handlers → templates (data-shape)

- **type**: data-shape
- **producer**: handlers (`handlers.go` — assembles `PageView`)
- **consumer**: templates (`templates.go`)
- **interface**: `type PageView struct { Source string; SVG template.HTML; Iterations int; Presets []Preset; Saved []SavedImage; Error string }`, `type Preset struct { Name, Label, Source string }`, `type SavedImage struct { Filename, Title string }`
- **notes**: `PageView.SVG` is `template.HTML`, **not** `string` — this is what lets
  `{{.SVG}}` inject the `<svg>…</svg>` markup without HTML-escaping it. A plain `string`
  field here renders the SVG as visible angle-bracket text and is the natural mistake. No
  FuncMap. Both `"page"` and `"result"` consume the same `PageView`; `"page"` renders
  `"result"` via `{{template "result" .}}`. Inside `{{range .Presets}}` and
  `{{range .Saved}}` the loop variable `.` is the element; neither loop needs `$`. All
  dynamic content (`<form>`, the `{{if .Error}}` line, `<div class="viz">`, the gallery)
  renders **inside** `<div id="app">`, the `hx-target`; the `POST /render`, `POST /select`
  and `POST /save` responses are the `"result"` fragment only and replace `#app` via
  `hx-swap="outerHTML"`. The type `<select>` carries its own `hx-post="/select"`
  overriding the form's `hx-post`; the enclosed `Save` button carries `hx-post="/save"`.

### saved-file endpoint (format)

- **type**: format
- **producer**: save (files written by `SaveSVG`) + `main` (the `FileServer` mount)
- **consumer**: any HTTP client (the gallery `<img>` tags)
- **interface**: `GET /saved/<name>.svg` → the file's bytes, `Content-Type:
  image/svg+xml` (set automatically by `http.FileServer` from the `.svg` extension),
  HTTP 200. A request for a missing file → HTTP 404.
- **notes**: the file body is exactly what `RenderSVG` produced and `SaveSVG` wrote — a
  complete `<svg …>…</svg>` document. No integration bead is required for this contract
  beyond confirming the mount exists (covered by the handlers/main wiring).

## Decomposition Notes

**Bead dependency order (do not reorder):**

1. **expr** — `Token`/`TokenKind`, `Lex`, `Expr` + nodes, `ParseExpr`, `EvalExpr`. No
   dependencies. Owns `expr.go`.
2. **grammar** — `Module`, `BodyModule`, `Rule`, `System`, `ParseSystem`. Depends on bead
   1. Owns `grammar.go`.
3. **rewrite** — `MaxModules`, `Derive`. Depends on beads 1 and 2. Owns `rewrite.go`.
4. **turtle** — `Segment`, `Interpret`. Depends on bead 2 (`Module`). Owns `turtle.go`.
5. **render** — `CanvasSize`, `Margin`, `Box`, `BBox`, `RenderSVG`. Depends on bead 4
   (`Segment`). Owns `render.go`.
6. **studio** — `Studio`. Depends on beads 2, 3, 4, 5. Owns `studio.go`. Its exit
   criterion runs the whole chain on a fixed source and asserts the exact SVG string —
   not `go build`.
7. **save** — `SavedImage`, `SaveSVG`, `ListSaved`, `SavedDir`, `saveMu`. No dependency on
   the L-system beads. Owns `save.go`.
8. **templates** — `InitTemplates`, `RenderPage`, `RenderResult`. Decompose **before**
   handlers so the handler bead's httptest assertions run against real template output.
   Owns `templates.go`.
9. **handlers** — `Preset`, `Presets`, `DefaultIterations`, `PageView`, `assemble`,
   `HandleIndex`, `HandleRender`, `HandleSelect`, `HandleSave`. Depends on beads 6, 7, 8.
   Every handler bead exit criterion is an `httptest` smoke test (not `go build`): at
   minimum one request/response per handler, asserting status + one structural property.
   For `GET /`, `POST /render`, `POST /select`, `POST /save`: the response body contains
   `id="app"` and, for a valid system, contains `<svg`. Add one assertion that
   `POST /render` with `source` = `"nonsense"` returns **HTTP 200** with a body containing
   `class="error"`, and one that `POST /select` with `preset=plant` returns a body
   containing `<textarea` whose contents include `X ->` (the plant source loaded into the
   editor). Owns `handlers.go`.
10. **main** — `var templates`, `func main()`. Wires the mux. Depends on bead 9. Owns
    `main.go`.
11. **integration** — one bounded `httptest` scenario (below).

**Integration bead — one bounded scenario:** stand up `httptest.NewServer` with the real
mux; issue `POST /render` with form values `source=axiom: F(1) +(90) F(1)` and
`iterations=0`; assert HTTP 200, that the response body contains `id="app"`, and that it
contains the exact substring
`<path d="M 20.000 780.000 L 780.000 780.000 M 780.000 780.000 L 780.000 20.000"`.
Do **not** also test `/select`, `/save`, and the gallery here — those are covered by the
handlers bead.

**Must not bind to a fixed port in any test** — use `httptest` throughout.

**Preset sources (verbatim — the `handlers` bead must embed `Presets` as exactly these
five entries, in this order, with each `Source` the exact Go string literal shown —
including the trailing `\n`, and no leading indentation):**

```go
var Presets = []Preset{
    {Name: "koch", Label: "Koch curve",
        Source: "angle: 90\nstep: 1\naxiom: F\nF -> F+F-F-F+F\n"},
    {Name: "snowflake", Label: "Koch snowflake",
        Source: "angle: 60\nstep: 1\naxiom: F--F--F\nF -> F+F--F+F\n"},
    {Name: "dragon", Label: "Heighway dragon",
        Source: "angle: 90\nstep: 1\naxiom: FX\nX -> X+YF+\nY -> -FX-Y\n"},
    {Name: "hilbert", Label: "Hilbert curve",
        Source: "angle: 90\nstep: 1\naxiom: A\nA -> +BF-AFA-FB+\nB -> -AF+BFB+FA-\n"},
    {Name: "plant", Label: "Fractal plant",
        Source: "angle: 25\nstep: 1\nheading: 90\naxiom: X\nX -> F+[[X]-X]-F[-FX]+X\nF -> FF\n"},
}
```
<!-- verified: go run scratchpad/refimpl (verify.go, "PRESETS" + "WRITE PRESET SVGS") =>
     all five string literals parse and render error-free; segment counts at iterations 1/3/5:
     koch 5/125/3125, snowflake 12/192/3072, dragon 2/8/32, hilbert 3/63/1023, plant 3/84/1488;
     SVGs written to scratchpad/svgs/*.svg and visually confirmed as the expected classic figures -->


**Pins (verbatim worked values — carry into the named bead's spec exactly, do not
re-derive or paraphrase):**

- **Pin — `expr` bead, `ParseExpr` + `EvalExpr`:** with the stated environment, the parsed
  expression evaluates to: `2+3*4` → `14`; `-2*3` → `-6`; `1/3` → `0.3333333333333333`;
  `10-3-2` → `5` (left-associative, NOT `9`); `2-(3-1)` → `0`; `s*0.6+a` with
  `{s:2, a:10}` → `11.2`; `-s` with `{s:5}` → `-5`; `2*-3` → `-6`. `s+1` under the empty
  environment → error; `1/0` → error.
- **Pin — `rewrite` bead, `Derive` (non-parametric):** `axiom: A`, `A -> AB`, `B -> A`.
  Symbol strings: step 0 `A`; step 1 `AB`; step 2 `ABA`; step 3 `ABAAB`; step 4
  `ABAABABA`.
- **Pin — `rewrite` bead, `Derive` (parametric):** `axiom: A(1)`, `A(s) -> F(s) A(s*0.5)`.
  Step 1 modules `F(1) A(0.5)`; step 2 `F(1) F(0.5) A(0.25)`; step 3
  `F(1) F(0.5) F(0.25) A(0.125)`.
- **Pin — `rewrite` bead, `Derive` (branch + arity):** `axiom: A(1)`,
  `A(s) -> F(s)[+(25)A(s*0.6)][-(25)A(s*0.6)]` → step 1 module sequence
  `F(1) [ +(25) A(0.6) ] [ -(25) A(0.6) ]`. Separately: `axiom: A A(2)`, `A(s) -> F(s)`
  → step 1 `A F(2)` (bare `A` unmatched, copied).
- **Pin — `grammar` bead, `ParseSystem` malformed rule head:** `A -> B` parses;
  `AB -> C` is an error (multi-letter head); `-> B` is an error, **not** a panic (empty
  head). Every error carries a 1-based line number.
- **Pin — `turtle` bead, `Interpret`:** start `(0,0)` heading `0`, `angleDefault 90`,
  `stepDefault 1`. `F(1) +(90) F(1)` → segments `{0,0,1,0}`, `{1,0,1,1}` (both exact).
  `F(1) [ +(90) F(1) ] F(1)` → **three** segments `{0,0,1,0}`, `{1,0,1,1}`, `{1,0,2,0}`
  (the `]` restores position to `(1,0)` and heading to `0`). `]` on an empty stack →
  `(nil, error)`. `F(1,2)` → `(nil, error)`.
- **Pin — `render` bead, `BBox` + `RenderSVG`:** `BBox([{0,0,2,0},{2,0,2,1}])` →
  `Box{0,0,2,1}, true`; the fit transform for that box (`CanvasSize 800`, `Margin 20`) is
  `s=380, ox=20, oy=210`, mapping `(0,0)→(20,590)`, `(2,1)→(780,210)`. `BBox(nil)` →
  `Box{}, false`. `RenderSVG([{0,0,1,0}])` →
  `<svg xmlns="http://www.w3.org/2000/svg" width="800" height="800" viewBox="0 0 800 800"><rect width="800" height="800" fill="white"/><path d="M 20.000 400.000 L 780.000 400.000" fill="none" stroke="black" stroke-width="1"/></svg>`.
  `RenderSVG(nil)` →
  `<svg xmlns="http://www.w3.org/2000/svg" width="800" height="800" viewBox="0 0 800 800"><rect width="800" height="800" fill="white"/></svg>`.
- **Pin — `studio` bead, `Studio`:** `Studio("axiom: F(1) +(90) F(1)\n", 0)` returns
  `(svg, nil)` with `svg` exactly
  `<svg xmlns="http://www.w3.org/2000/svg" width="800" height="800" viewBox="0 0 800 800"><rect width="800" height="800" fill="white"/><path d="M 20.000 780.000 L 780.000 780.000 M 780.000 780.000 L 780.000 20.000" fill="none" stroke="black" stroke-width="1"/></svg>`.
  `Studio("axiom: A\nA -> AB\nB -> A\n", 3)` returns an SVG containing `<rect` and **no**
  `<path`. `Studio("nonsense", 0)` returns `("", non-nil error)`.
- **Pin — `save` bead, `SaveSVG` / `ListSaved`:**
  `SaveSVG("<svg/>", tmpDir, time.Unix(0, 1700000000123456789))` →
  `("1700000000123456789.svg", nil)` and that file exists in `tmpDir` with body
  `<svg/>`. After writing `0000000000000000001.svg` and `0000000000000000002.svg` into a
  temp dir, `ListSaved` returns them in the order
  `["0000000000000000002.svg", "0000000000000000001.svg"]` and ignores any non-`.svg`
  file. `ListSaved` on a missing path → `(nil, nil)`.
- **Pin — `integration` bead:** `POST /render` with `source=axiom: F(1) +(90) F(1)` and
  `iterations=0` → HTTP 200, body contains `id="app"` and the exact substring
  `<path d="M 20.000 780.000 L 780.000 780.000 M 780.000 780.000 L 780.000 20.000"`.

## Open Questions

*(none open — see the resolution log below)*

<!-- Resolved 2026-09-06 (draft-design-doc interactive pass):
  - +/- as both module symbols and operators: kept — single-character turn symbols
    (`+` `-` `+(a)` `-(a)`), disambiguated by context (module-sequence level vs inside
    parentheses). No `Turn(a)` keyword.
  - Preset set: kept — Koch curve, Koch snowflake, Heighway dragon, Hilbert curve,
    fractal plant. All five verified in the reference implementation.
  - Output format: SVG only — no raster/PNG, no image/image/png, no line rasteriser.
    RenderSVG returns a string; SaveSVG writes .svg; gallery serves .svg. (kept as drafted)
  - Expression sub-language: arithmetic only (+ - * /, unary minus, parens, refs, floats).
    No conditional/stochastic/context-sensitive productions. (kept as drafted)
  - Malformed-source response: HTTP 200 with PageView.Error set and blank drawing, not
    HTTP 400 — deliberate deviation from exprvm-web/fractalviz for the HTMX editing loop.
    (kept as drafted)
  - Iteration count on load/select: Studio always uses its (clamped) iterations argument
    and ignores the source's iterations: setting; HandleIndex/HandleSelect pass
    DefaultIterations = 4. Handlers never call ParseSystem. (kept as drafted)
-->

