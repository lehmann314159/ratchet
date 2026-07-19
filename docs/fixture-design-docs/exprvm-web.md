# ExprVM-Web — Design Document

## Overview

A single-page web REPL for the same small integer expression language exprvm
compiles (arithmetic + variables + `print`, no control flow), served over
HTTP instead of a CLI. A visitor types one line into a text box, submits it,
and sees it added to a running transcript showing every line typed so far and
its result. Variables persist across submissions within the same server run —
assigning `x` on one submission and reading it on a later one works, exactly
like a real REPL/shell history.

This is a from-scratch reimplementation of exprvm's language, not a code
import of the exprvm project — every type and function below is fully
specified in this document; nothing is assumed carried over from exprvm.md.
Out of scope: floating-point numbers, control flow (no `if`, no loops, no
functions/procedures), comments in source, string or boolean types, a
disassembler, per-browser/per-user sessions (see Domain Parameters), state
that survives a server restart (in-memory only), and submitting more than one
statement in a single text-box submission (see Domain Parameters).

**Domain parameters:**
- All numeric values are signed 64-bit integers (Go `int64`). Arithmetic uses
  Go's native `int64` semantics, including silent wraparound on overflow — no
  overflow is ever detected or reported as an error.
- Integer division truncates toward zero (Go's native `/` operator semantics
  for `int64`), not toward negative infinity. Worked examples: `7/2` = 3;
  `-7/2` = -3 (NOT -4); `7/-2` = -3 (NOT -4); `-7/-2` = 3.
- Identifiers match `[a-zA-Z_][a-zA-Z0-9_]*` (first character a letter or
  underscore, never a digit; subsequent characters letters, digits, or
  underscore).
- Number literals are one or more ASCII digit characters (`0`-`9`) and
  represent a non-negative value; leading zeros are not an error (`"042"` is a
  valid literal with value 42). A digit run that does not fit in `int64` via
  `strconv.ParseInt(text, 10, 64)` (exceeds `math.MaxInt64` =
  9223372036854775807) is an error — NOT silently wrapped or truncated. There
  is no negative-number literal — a negative value is always produced by the
  unary-minus operator. There is no unary-plus operator; a leading `+` before
  an expression is always an error.
- Whitespace: space (`' '`) and tab (`'\t'`) between tokens are insignificant.
- **Exactly one statement per submission.** Unlike a general-purpose program,
  each text-box submission is parsed as exactly one statement — an
  assignment, a `print(...)` call, or (new in this project — see below) a
  bare expression. A submission containing two statements (e.g. two lines
  separated by a newline, or anything after the first statement other than
  trailing whitespace) is a parse error. This matches how a REPL is actually
  used (one line at a time) and removes an entire category of multi-statement
  sequencing concerns that don't apply here.
- **New construct — bare expression statements.** In addition to `print(expr)`
  and assignment, a submission may be a bare expression with no `print(...)`
  wrapper (e.g. submitting `2+3` directly) — this evaluates the expression and
  displays its value, exactly as if it had been wrapped in `print(...)`. This
  is the one deliberate language addition versus exprvm's CLI grammar,
  specified precisely below (Behavioral Specification, "Statement dispatch").
- **Single shared session, not per-browser/per-user.** There is exactly one
  persistent variable environment and one transcript history for the whole
  server process, shared by every browser that connects — not per-cookie,
  per-session, or per-tab. Two browser tabs open at once see and mutate the
  same shared state. This matches every other fixture project's single-
  instance convention (chess/goban/othello's single global `game` variable).
- **Concurrency**: a single package-level `sync.Mutex` guards all access to
  the shared environment and history (see Behavioral Specification) — this is
  a demo tool for one operator at a time, not a production multi-user service,
  but the mutex avoids a real data race if two requests do arrive concurrently
  (e.g. a double-click, or a second browser tab).
- State is in-memory only and resets on server restart (no persistence to
  disk or a database).
- Server listens on `:8080`.

## Architecture

```
exprvm-web/
├── go.mod
├── main.go       — var env *Environment, var history []HistoryEntry,
│                    var mu sync.Mutex, var templates *template.Template;
│                    func main() only
├── lexer.go      — Token, TokenType, NewLexer, (*Lexer).Next
├── parser.go     — Program, Stmt, AssignStmt, PrintStmt, ExprStmt, Expr,
│                    NumberExpr, VarExpr, UnaryExpr, BinaryExpr, NewParser,
│                    (*Parser).ParseProgram
├── compiler.go   — OpCode, Instruction, CompiledProgram, Compile
├── env.go        — Environment, NewEnvironment
├── vm.go         — VM, NewVM, (*VM).Run
├── handlers.go   — HistoryEntry, PageData, HandleIndex, HandleEval
├── templates.go  — InitTemplates, RenderIndex
└── *_test.go     — one test file per source file above, plus integration_test.go
```

All `.go` files use `package main` at the project root — no subdirectories.

**File assignment rules (strict):**
- `main.go` contains exactly: `var env *Environment`, `var history
  []HistoryEntry`, `var mu sync.Mutex`, `var templates *template.Template`,
  and `func main()`. Nothing else.
- `lexer.go` contains: `Token`, `TokenType` and its constants, `NewLexer`,
  `(*Lexer).Next`. No parsing logic — never references `Program`, `Stmt`, or
  `Expr`.
- `parser.go` contains: every AST type (`Program`, `Stmt`, `AssignStmt`,
  `PrintStmt`, `ExprStmt`, `Expr`, `NumberExpr`, `VarExpr`, `UnaryExpr`,
  `BinaryExpr`), `NewParser`, `(*Parser).ParseProgram`. Constructs its own
  `*Lexer` internally. Contains no `OpCode`, `Instruction`, `Environment`, or
  `VM` references, and performs NO variable-definedness checking (that check
  moved to `compiler.go` — see Behavioral Specification for why).
- `compiler.go` contains: `OpCode` and its constants, `Instruction`,
  `CompiledProgram`, `Compile`. Does NOT reference `Token`, `TokenType`, or
  `*Lexer`.
- `env.go` contains: `Environment`, `NewEnvironment`. No compiling or
  executing logic — purely the persistent-state type.
- `vm.go` contains: `VM`, `NewVM`, `(*VM).Run`. Does NOT reference any AST
  type — operates purely on `[]Instruction` plus `*Environment`.
- `handlers.go` contains: `HistoryEntry`, `PageData`, `HandleIndex`,
  `HandleEval`. No template-parsing logic.
- `templates.go` contains: `InitTemplates`, `RenderIndex`. No handler
  functions, no type declarations.
- Do NOT put `HandleIndex` or `HandleEval` in `templates.go`.
- Do NOT put `HistoryEntry` or `PageData` in `templates.go` — they belong in
  `handlers.go`.
- Do NOT put `OpCode`/`Instruction`/`CompiledProgram` in `vm.go` — they belong
  in `compiler.go`.
- Do NOT put any AST type in `compiler.go`, `env.go`, or `vm.go`.

## Data Types and Function Signatures

All `.go` source files use `package main`. The module name is `exprvm-web`.
Requires Go 1.22.

```go
// ---- lexer.go ----
// Identical token set and lexer behavior to a standalone expression-language
// lexer — no REPL-specific change in this file at all.

type TokenType int

const (
    TokenNumber  TokenType = iota // integer literal, e.g. "42"
    TokenIdent                     // identifier, e.g. "x", never "print" (see rule 4)
    TokenPlus                      // "+"
    TokenMinus                     // "-"
    TokenStar                      // "*"
    TokenSlash                     // "/"
    TokenLParen                    // "("
    TokenRParen                    // ")"
    TokenAssign                    // "="
    TokenPrint                     // the keyword "print"
    TokenNewline                   // "\n"
    TokenEOF                       // end of input
)

type Token struct {
    Type TokenType
    Text string // exact source text of the token, e.g. "42", "x", "print", "+"
}

func NewLexer(source string) *Lexer
func (l *Lexer) Next() (Token, error)

// ---- parser.go ----

type Program struct {
    Statements []Stmt // always exactly 1 element on a successful parse — see
                       // Behavioral Specification's "exactly one statement" rule
}

type Stmt interface {
    isStmt()
}

type AssignStmt struct {
    Name  string
    Value Expr
}

type PrintStmt struct {
    Value Expr
}

// ExprStmt is a bare expression with no print(...) wrapper — new in this
// project versus a plain CLI compiler. Compiles identically to PrintStmt
// (see compiler.go's Behavioral Specification) — same output, different
// surface syntax.
type ExprStmt struct {
    Value Expr
}

type Expr interface {
    isExpr()
}

type NumberExpr struct {
    Value int64
}

type VarExpr struct {
    Name string
}

type UnaryExpr struct {
    Op    TokenType // always TokenMinus
    Value Expr
}

type BinaryExpr struct {
    Op    TokenType // TokenPlus, TokenMinus, TokenStar, or TokenSlash
    Left  Expr
    Right Expr
}

func NewParser(source string) *Parser
func (p *Parser) ParseProgram() (*Program, error)

// ---- compiler.go ----

type OpCode int

const (
    OpPushConst OpCode = iota // Operand: the int64 constant to push
    OpLoad                     // Operand: variable slot index to push from
    OpStore                    // Operand: variable slot index to pop into
    OpAdd
    OpSub
    OpMul
    OpDiv
    OpNeg
    OpPrint
)

type Instruction struct {
    Op      OpCode
    Operand int64 // meaning depends on Op; 0 and unused for no-operand ops
}

type CompiledProgram struct {
    Instructions []Instruction
}

// Compile takes the single parsed statement plus the persistent Environment
// (for variable-slot lookup/registration — see Behavioral Specification).
// Compile mutates env: it may register a new slot for an AssignStmt's Name.
func Compile(stmt Stmt, env *Environment) (*CompiledProgram, error)

// ---- env.go ----

// Environment is the persistent, cross-submission variable state. It is
// never reset except by a server restart.
type Environment struct {
    Slots  map[string]int // variable name -> slot index; grows over the server's lifetime
    Values []int64        // current value per slot index; grows in lockstep with Slots
}

func NewEnvironment() *Environment

// ---- vm.go ----

type VM struct {
    // unexported fields only
}

func NewVM() *VM

// Run executes cp against env's persistent Values (reading/writing existing
// slots — never resizing env.Values itself; Compile already grew it to the
// necessary length before Run is ever called for this statement).
func (vm *VM) Run(cp *CompiledProgram, env *Environment, out io.Writer) error

// ---- handlers.go ----

// HistoryEntry is one past submission and its result, for transcript display.
type HistoryEntry struct {
    Input  string // the raw text the user submitted
    Output string // the printed value, if any; "" if the statement produced
                   // no output (a successful assignment) or if Err is non-empty
    Err    string // "" on success; the error message on failure
}

// PageData is passed to the "index" template.
type PageData struct {
    History []HistoryEntry
}

func HandleIndex(w http.ResponseWriter, r *http.Request)
func HandleEval(w http.ResponseWriter, r *http.Request)

// ---- templates.go ----

func InitTemplates() *template.Template
func RenderIndex(w http.ResponseWriter, data PageData)

// ---- main.go ----

var env       *Environment
var history   []HistoryEntry
var mu        sync.Mutex
var templates *template.Template
```

### Export signatures

```go
var _ func(string) *Lexer = NewLexer
var _ func(*Lexer) (Token, error) = (*Lexer).Next
var _ func(string) *Parser = NewParser
var _ func(*Parser) (*Program, error) = (*Parser).ParseProgram
var _ func(Stmt, *Environment) (*CompiledProgram, error) = Compile
var _ func() *Environment = NewEnvironment
var _ func() *VM = NewVM
var _ func(*VM, *CompiledProgram, *Environment, io.Writer) error = (*VM).Run
var _ func(http.ResponseWriter, *http.Request) = HandleIndex
var _ func(http.ResponseWriter, *http.Request) = HandleEval
var _ func() *template.Template = InitTemplates
var _ func(http.ResponseWriter, PageData) = RenderIndex
```

## Behavioral Specification

**`NewLexer(source string) *Lexer`** — returns a lexer positioned at the start
of `source`.

**`(*Lexer).Next() (Token, error)`** — returns the next token and advances
position. Rules, in this exact priority order (each character is consumed by
the first matching rule):

1. A run of one or more space (`' '`) or tab (`'\t'`) characters produces no
   token — skip and continue at the character after the run.
2. A single `'\n'` character produces exactly one `TokenNewline`.
3. A run of one or more ASCII digit characters (`'0'`-`'9'`) produces one
   `TokenNumber` whose `Text` is exactly that run of digits.
4. The exact 5-character sequence `"print"`, when the character immediately
   following it is not a letter, digit, or underscore (or there is no
   following character at all), produces one `TokenPrint`. Checked before
   rule 5. Worked example: `"printx"` falls through to rule 5 and produces one
   `TokenIdent` with `Text` `"printx"`; `"print("` matches this rule and
   produces `TokenPrint` followed separately by `TokenLParen`.
5. A run of one or more characters from `{letters, digits, '_'}` whose first
   character is a letter or `'_'` produces one `TokenIdent` with `Text` equal
   to that run — only reached when rule 4 did not match at this position.
6. Each of `+ - * / ( ) =` produces exactly one token of the corresponding
   type. Every such token is exactly one character — two adjacent operator
   characters are always two separate tokens (`"+-"` is `TokenPlus` then
   `TokenMinus`, never one token).
7. Reaching the end of `source` produces one `TokenEOF`. Calling `Next()`
   again after `TokenEOF` has already been returned also returns `TokenEOF`
   (no error).
8. Any character not matched by rules 1-7 is a lexer error: return a non-nil
   error and the zero-value `Token`.

**`NewParser(source string) *Parser`** — constructs a parser that creates and
owns its own `*Lexer` internally, via `NewLexer(source)`.

**`(*Parser).ParseProgram() (*Program, error)`** — parses the submission into
a `*Program` containing exactly one statement. Grammar:

```
program    := statement (single trailing NEWLINE)? EOF
statement  := assignment | print_stmt | expr
assignment := IDENT '=' expr
print_stmt := 'print' '(' expr ')'
expr       := term (('+' | '-') term)*
term       := factor (('*' | '/') factor)*
factor     := NUMBER | IDENT | '-' factor | '(' expr ')'
```

**Exactly-one-statement rule, stated as an exact algorithm:** skip zero or
more leading `TokenNewline`. Parse exactly one `statement` per the dispatch
rule below. After that statement, the next token must be either `TokenEOF`,
or exactly one `TokenNewline` followed by `TokenEOF` (a single optional
trailing newline is allowed; anything else — including a second statement, or
two or more trailing newlines — is a parse error). An entirely empty or
whitespace-only submission (first token is `TokenEOF`, or `TokenNewline` then
`TokenEOF`) is also a parse error (unlike exprvm's CLI, where an empty
program was valid — here there is always meant to be exactly one statement to
evaluate; an empty submission has nothing to evaluate and the web layer
should not even reach the parser for one — see `HandleEval` below, which
rejects an empty input string before calling `NewParser` at all, but
`ParseProgram` itself still errors defensively if ever called on empty input).

**Statement dispatch** (this is the one new decision versus a plain
expression-language grammar: distinguishing `assignment` from a bare `expr`
that happens to start with the same token): the parser needs exactly one
token of lookahead **beyond** a leading `TokenIdent` to decide, without
consuming anything irreversibly:
- If the next token is `TokenPrint`, parse as `print_stmt`.
- Else if the next token is `TokenIdent` **and** the token immediately after
  it (peeked, not yet consumed) is `TokenAssign`, parse as `assignment`:
  consume the `TokenIdent`, consume the `TokenAssign`, then parse `expr`.
- Else, parse a bare `expr` from the current position (do not consume
  anything before starting this — `expr`'s own `factor` rule will consume a
  leading `TokenIdent` as a `VarExpr` if that's what comes next). Wrap the
  result in `ExprStmt{Value: <the parsed expr>}`.

Worked examples: submitting `"x=5"` — next token `TokenIdent("x")`, token
after it is `TokenAssign` → `assignment`. Submitting `"x+1"` — next token
`TokenIdent("x")`, token after it is `TokenPlus`, NOT `TokenAssign` → falls
to the bare-`expr` case; `expr` parses `x` as a `VarExpr` via `factor`, then
continues with `+1` via `term`'s repetition, producing
`ExprStmt{Value: BinaryExpr{Op:TokenPlus, Left:VarExpr{"x"}, Right:NumberExpr{1}}}`.
This means `Parser` needs to support peeking one token ahead of the current
position without consuming it (a small 1-token lookahead buffer) — this is a
real, necessary internal capability, not an incidental implementation detail
to skip.

**Left-associativity, precedence, and unary minus** — identical rules and
worked examples as a standalone expression-language parser:
- `"10-3-2"` parses left-associatively: `(10-3)-2` = 5, NOT `10-(3-2)` = 9.
- `"2+3*4"` parses as `2+(3*4)` = 14 (term binds tighter than expr), NOT
  `(2+3)*4` = 20.
- `"-2+3"` parses as `(-2)+3` = 1 (unary minus binds inside `factor`, tighter
  than any binary operator), NOT `-(2+3)` = -5. `"2- -3"` parses as
  `2-(-3)` = 5 (first `-` is binary, second is unary — lexer rule 6 guarantees
  each `-` is always its own single-character token; the parser alone decides
  binary vs. unary from grammar position).

**Numeric-literal overflow check belongs to the parser, not the lexer.**
`(*Lexer).Next()`'s rule 3 always succeeds for any run of digit characters,
however long — it never errors on an over-long digit run; the lexer has no
concept of `int64` range at all. Instead, when `factor` consumes a
`TokenNumber` to build a `NumberExpr`, `parser.go` itself calls
`strconv.ParseInt(token.Text, 10, 64)`. If that call returns a non-nil error
(the digit run doesn't fit in `int64` — exceeds `math.MaxInt64` =
9223372036854775807), `ParseProgram` returns `(nil, err)` immediately,
propagating that `strconv` error. This is a **parser**-level error, produced
inside `factor`'s own logic — it is NOT a lexer error, and the
"Lexer-error propagation" rule below (which is specifically about
`(*Lexer).Next()`'s own return value) does not apply to it. Worked example:
`"99999999999999999999"` (20 digits, exceeds `math.MaxInt64`) as a bare
expression submission → `ParseProgram` returns a non-nil error from the
`strconv.ParseInt` call inside `factor`, before any `Compile` or `Run` step
is reached.

**Undefined-variable checking has moved to `Compile`, not `ParseProgram`**
(a deliberate change from a plain CLI compiler's design): whether a variable
name is "defined" now depends on the persistent `*Environment` accumulated
across every prior submission this server has processed — state
`ParseProgram` has no access to and should not be given, since it would then
need to be re-constructed per submission from history, an unnecessary
indirection. `ParseProgram` is purely syntactic: it builds the `*Program` from
tokens with zero semantic/definedness checking. See `Compile` below for the
actual check.

**Lexer-error propagation**: if any call to `(*Lexer).Next()` made anywhere
during parsing returns a non-nil error, `ParseProgram` immediately returns
`(nil, thatError)` — the lexer's error returned verbatim — and requests no
further tokens. Do NOT inspect only the returned `Token`'s `Type` field first;
the zero-value `Token` accompanying a lexer error has `Type == TokenNumber`
(`TokenNumber` is `TokenType`'s zero value) and `Text == ""`, which must never
be treated as a real empty numeric literal.

**`Compile(stmt Stmt, env *Environment) (*CompiledProgram, error)`** —
translates one statement into bytecode, using and mutating `env` for
variable-slot resolution. This function performs the undefined-variable
check that `ParseProgram` no longer does.

**Variable resolution algorithm, exact and order-dependent:**
1. To resolve a `VarExpr{Name: s}` encountered anywhere while compiling an
   expression (including inside an `AssignStmt`'s `Value`): `s` must already
   be a key in `env.Slots`. If it is not, `Compile` returns a non-nil error
   immediately and makes no further changes to `env`. If it is, emit `OpLoad`
   with `Operand` equal to `env.Slots[s]`.
2. To resolve an `AssignStmt{Name: s, Value: v}`'s assignment target:
   **first** fully compile `v` (per rule 1 above — this may fail if `v`
   references an undefined name). **Only after** `v` compiles successfully,
   check whether `s` is already in `env.Slots`; if not, register it now:
   `newSlot := len(env.Values)`; `env.Slots[s] = newSlot`;
   `env.Values = append(env.Values, 0)`. Then emit `OpStore` with `Operand`
   equal to `env.Slots[s]` (either the pre-existing or the just-registered
   slot).

**The ordering in rule 2 is load-bearing, not incidental**: compiling `Value`
before registering `Name` means a variable's own name is never valid inside
its own first-ever assignment's right-hand side. Worked example: submitting
`"x=x+1"` when `x` has never appeared before — compiling `Value` (`x+1`)
hits the `VarExpr{"x"}` inside it via rule 1, `x` is not yet in `env.Slots`
(registration for this same statement hasn't happened yet), so `Compile`
returns an error. This matches the intuitive reading (you can't reference a
variable before its first definition, even on the same line that defines it).
Contrast with `"x=1"` then, on a **later**, separate submission, `"x=x+1"` —
by the second submission, `x` is already in `env.Slots` from the first, so
`Value`'s `VarExpr{"x"}` resolves via rule 1 successfully, and the assignment
re-stores into the same existing slot (rule 2's "already in env.Slots"
branch — no new slot).

**Registration happens at compile time regardless of whether `Run` later
succeeds.** Once `Compile` registers a new slot for an `AssignStmt`'s `Name`
(rule 2), that registration is never rolled back, even if executing the
compiled instructions later fails (e.g. a division-by-zero in `Value`).
Worked example: submitting `"y=1/0"` where `y` has never appeared before —
`Compile` succeeds (registers `y` as a new slot with `env.Values`'s
freshly-appended entry at 0, since compiling `Value` itself, `1/0`, contains
no `VarExpr` to fail on — division by zero is a **runtime** failure, not a
compile-time one). `Run` then fails with the division-by-zero error, and the
newly-registered slot for `y` keeps its initialized value of 0 (never
updated, since `OpStore` for `y` is never reached). A later submission
`"print(y)"` succeeds and prints `"0"` — `y` is a known, defined variable
from this point on, even though its one assignment attempt never completed.
This is a deliberate, simpler rule than trying to roll back slot registration
on a later runtime failure (which would require `Compile` and `Run` to share
a rollback/commit protocol) — stated explicitly here, and covered by a
Required Test Scenario below, precisely because it is not the only
defensible design and must not be left for an implementer to guess.

**Code generation**, exact instruction sequence per AST node:
- `NumberExpr{Value: n}` → one instruction: `OpPushConst` with `Operand: n`.
- `VarExpr{Name: s}` → one instruction: `OpLoad` with `Operand:` `s`'s
  resolved slot index (per the variable resolution algorithm above).
- `UnaryExpr{Value: e}` → [instructions for `e`], then one `OpNeg`.
- `BinaryExpr{Op, Left, Right}` → [instructions for `Left`], then
  [instructions for `Right`], then one instruction for `Op` (`OpAdd` for
  `TokenPlus`, `OpSub` for `TokenMinus`, `OpMul` for `TokenStar`, `OpDiv` for
  `TokenSlash`). `Left`'s instructions always precede `Right`'s.
- `AssignStmt{Name, Value}` → [instructions for `Value`], then one `OpStore`
  with `Operand:` `Name`'s resolved slot index (per rule 2 above — resolved
  **after** `Value` is compiled).
- `PrintStmt{Value}` → [instructions for `Value`], then one `OpPrint`.
- `ExprStmt{Value}` → **identical to `PrintStmt`**: [instructions for
  `Value`], then one `OpPrint`. A bare expression is compiled exactly as if
  it had been written `print(<the same expression>)` — there is no
  separate opcode or VM behavior for a bare expression; the distinction
  exists only in the parser's AST, not in the bytecode or the VM.

**`NewEnvironment() *Environment`** — returns
`&Environment{Slots: map[string]int{}, Values: []int64{}}` (empty map, empty
slice — not nil for either field).

**`NewVM() *VM`** — returns a `VM` with no allocated stack yet (allocated
fresh inside each `Run` call). A single `*VM` may have `Run` called on it
repeatedly, across many submissions, with different `*CompiledProgram` and
(the same, persistent) `*Environment` arguments.

**`(*VM).Run(cp *CompiledProgram, env *Environment, out io.Writer) error`** —
executes `cp.Instructions` in order against a fresh `[]int64` operand stack
(empty at the start of every `Run` call — the stack is NOT persistent across
submissions, only `env.Values` is) and `env.Values` for variable storage.
`Run` never resizes `env.Values` itself — `Compile` already grew it (via the
variable resolution algorithm) to be long enough for every slot index this
`cp` references, before `Run` is ever called. Per-instruction effect (rightmost
stack element is the top):

- `OpPushConst` (Operand=n): `[...]` → `[..., n]`.
- `OpLoad` (Operand=slot): `[...]` → `[..., env.Values[slot]]`.
- `OpStore` (Operand=slot): `[..., v]` → `[...]`; sets `env.Values[slot] = v`.
- `OpAdd`: `[..., a, b]` → `[..., a+b]`.
- `OpSub`: `[..., a, b]` → `[..., a-b]` (`a` is `Left`, pushed first/deeper;
  `b` is `Right`, pushed second/on top — e.g. `"10-3"` compiles to
  `[OpPushConst 10, OpPushConst 3, OpSub]`; stack before `OpSub` is `[10, 3]`;
  result is `10-3` = 7, not `3-10`).
- `OpMul`: `[..., a, b]` → `[..., a*b]`.
- `OpDiv`: `[..., a, b]` → `[..., a/b]`, Go's native `int64` `/` (truncation
  toward zero). Same operand correspondence as `OpSub`. If `b == 0`, `Run`
  immediately returns a non-nil error with message exactly
  `"runtime error: division by zero"` and executes no further instructions.
  Because every submission is exactly one statement (see Domain Parameters),
  and a statement's instructions always end with at most one `OpStore` or
  `OpPrint`, a division-by-zero failure always means **zero** bytes are ever
  written to `out` for this `Run` call — there is no "partial output before
  the error" case to handle (unlike a multi-statement program would have).
- `OpNeg`: `[..., a]` → `[..., -a]`.
- `OpPrint`: `[..., a]` → `[...]`; writes to `out` the base-10 string form of
  `a` (Go's `strconv.FormatInt(a, 10)`) followed by one `'\n'` byte.

If every instruction executes with no division-by-zero, `Run` returns `nil`.

**`HandleIndex(w http.ResponseWriter, r *http.Request)`** — serves `GET /`.
Acquires `mu`, reads `history` into a local copy, releases `mu`, then calls
`RenderIndex(w, PageData{History: <the copy>})`.

**`HandleEval(w http.ResponseWriter, r *http.Request)`** — serves `POST
/eval`. Reads form field `input` (`r.FormValue("input")`). If it is the empty
string after no trimming is applied (exactly `""`), do not call `NewParser`
at all — acquire `mu`, append `HistoryEntry{Input: "", Err: "input was
empty"}` to `history`, release `mu`, then redirect (see below). This is the
one input-validation branch the web layer handles itself, before the
compiler pipeline, matching `ParseProgram`'s own "empty submission is a
parse error" rule but avoiding constructing a `*Lexer`/`*Parser` for a case
already known to fail. Otherwise, acquire `mu` for the remainder of this
handler (covering every read and write of `env` and `history` below, so no
other request can interleave), and run the fixed pipeline:
1. Call `NewParser(input).ParseProgram()`. On a non-nil error, append
   `HistoryEntry{Input: input, Err: <the error's message>}` to `history`
   (Output stays `""`), release `mu`, redirect.
2. Otherwise call `Compile(prog.Statements[0], env)`. On a non-nil error,
   append `HistoryEntry{Input: input, Err: <the error's message>}`, release
   `mu`, redirect. (Per the variable resolution algorithm, `env` may still be
   unchanged or may have been mutated up through the point of failure —
   `Compile`'s own rules above define exactly which mutations, if any,
   persist.)
3. Otherwise construct a `strings.Builder` as `out`, call
   `NewVM().Run(cp, env, &out)`. On a non-nil error, append
   `HistoryEntry{Input: input, Err: <the error's message>}` (Output stays
   `""` — per the "zero bytes written before a runtime error" rule above,
   `out`'s builder is guaranteed empty in this branch). On a nil error,
   append `HistoryEntry{Input: input, Output: <out.String(), with its
   trailing '\n' trimmed>, Err: ""}`.
4. Release `mu`. Redirect the client to `GET /` with
   `http.Redirect(w, r, "/", http.StatusSeeOther)` (a 303, not a 302 — so the
   browser re-requests with GET regardless of how it got here, avoiding a
   double-submit on refresh).

**`InitTemplates() *template.Template`** — parses an inline Go string literal
(no external `.html` files) defining one named template, `"index"`. Must
panic on a parse failure (mirrors every other fixture's `InitTemplates`
convention).

**`RenderIndex(w http.ResponseWriter, data PageData)`** — executes the
`"index"` template with `data`, writing to `w`.

**Templates** — the `"index"` template renders: an `<h1>` title, a `<form
method="POST" action="/eval">` containing one `<input type="text"
name="input">` and a submit button, and below it the transcript: iterate
`.History` in order (oldest first — the order they were appended, so newest
is last/at the bottom, like a terminal scrollback), rendering each entry as
one block showing the entry's `Input`, then either its `Output` (if `Err` is
empty) or, if `Err` is non-empty, `Err` rendered inside an element with
**exactly** `class="error"` (e.g. `<div class="error">{{.Err}}</div>`) — this
exact class name is required, not illustrative; it is asserted on literally
by the integration bead below. This is
a full-page server-rendered response on every load — no HTMX, no JavaScript,
no partial-page fragment updates; `POST /eval` always redirects to a full
`GET /` reload. (This is a deliberate simplicity choice: it removes an entire
class of fragment-swap-scope risk that HTMX-based fixture projects have
had to account for, at the cost of a full page reload per submission — an
acceptable tradeoff for a single-operator demo.)

**`main()`** — initializes `env = NewEnvironment()`, `history =
[]HistoryEntry{}`, `templates = InitTemplates()`, registers `GET /` →
`HandleIndex` and `POST /eval` → `HandleEval` on an `http.ServeMux`, calls
`http.ListenAndServe(":8080", mux)`.

## Required Test Scenarios

1. **Left-associativity**: submitting `"10-3-2"` → `Output` `"5"`, not `"9"`.
2. **Precedence**: submitting `"2+3*4"` → `Output` `"14"`, not `"20"`.
3. **Unary minus vs. binary minus**: `"-2+3"` → `"1"`. `"2- -3"` → `"5"`.
   `"-(2+3)"` → `"-5"`.
4. **Division truncation**: `"7/2"` → `"3"`. `"-7/2"` → `"-3"` (NOT `"-4"`).
   `"7/-2"` → `"-3"` (NOT `"-4"`). `"-7/-2"` → `"3"`.
5. **Division by zero**: submitting `"1/0"` → `Run` returns an error with
   message exactly `"runtime error: division by zero"`; the resulting
   `HistoryEntry` has `Output == ""` and a non-empty `Err`.
6. **Cross-submission variable persistence** (the core new behavior this
   project adds over a plain CLI compiler): submit `"x=5"` (one request),
   then separately submit `"print(x+1)"` (a second, later request) — the
   second must produce `Output` `"6"`. This is only possible because `env`
   is a persistent, package-level value, not freshly constructed per
   request.
7. **Bare-expression echo**: submitting `"2+3"` (no `print(...)` wrapper) →
   `Output` `"5"`, identical to what `"print(2+3)"` would produce.
8. **Assignment produces no output**: submitting `"x=5"` → the resulting
   `HistoryEntry` has `Output == ""` and `Err == ""` (success, but nothing
   printed).
9. **Statement-dispatch disambiguation**: submitting `"x=5"` on one request
   then `"x+1"` on a later request must NOT be parsed as an assignment
   attempt in the second case — `Output` for `"x+1"` must be `"6"` (treated
   as a bare expression, per the lookahead rule), not a parse error and not
   an assignment.
10. **Self-reference on first definition is an error**: submitting `"z=z+1"`
    when `z` has never appeared in any earlier submission this server run →
    `Compile` returns a non-nil error; the `HistoryEntry` has a non-empty
    `Err`.
11. **Slot registration survives a runtime failure**: submitting `"y=1/0"`
    when `y` has never appeared before → `Err` is non-empty (division by
    zero). A **later** submission `"print(y)"` must succeed and produce
    `Output` `"0"` — `y` became a known variable at compile time even though
    its assignment never completed.
12. **`print` keyword vs. identifier boundary**: submitting `"printx=5"`
    then `"print(printx)"` (two separate submissions) — the lexer must
    tokenize `printx` as a single `TokenIdent`, and the second submission's
    `Output` must be `"5"`.
13. **Multi-statement submission is a parse error**: submitting `"x=1\ny=2"`
    (two statements in one submission) → `ParseProgram` returns a non-nil
    error; the resulting `HistoryEntry` has a non-empty `Err`, and neither
    `x` nor `y` is registered in `env.Slots` afterward (verify via a
    following submission that using either as a bare expression is still an
    "undefined variable" `Compile` error).
14. **Empty submission**: submitting the empty string via `HandleEval` →
    `HistoryEntry{Input: "", Err: "input was empty"}` is appended;
    `NewParser`/`ParseProgram` are never invoked for this case (verify via an
    httptest request, not a unit test of `ParseProgram` alone, since the
    short-circuit lives in `HandleEval`).
15. **Oversized numeric literal is a parser error**: submitting
    `"99999999999999999999"` (20 digits, exceeds `math.MaxInt64`) → the
    `HistoryEntry` has a non-empty `Err` from `ParseProgram`'s
    `strconv.ParseInt` call inside `factor`; `Compile` and `Run` are never
    reached.
16. **Two submissions in sequence via real HTTP requests** (integration-level,
    not just library-level): `POST /eval` with `input=x=5`, then `POST
    /eval` with `input=print(x*2)` — assert the second response, after
    following the redirect to `GET /`, renders `"10"` somewhere in the
    transcript. This is the end-to-end confirmation that persistence works
    through the real handler chain, not just through directly-called Go
    functions in a unit test.

## Cross-Bead Contracts

### parser → compiler (format)

- **type**: format
- **producer**: parser (parser.go)
- **consumer**: compiler (compiler.go)
- **interface**: `*Program{Statements []Stmt}` (always length 1 on success),
  where `Stmt` is `AssignStmt{Name string, Value Expr}`,
  `PrintStmt{Value Expr}`, or `ExprStmt{Value Expr}`; `Expr` is
  `NumberExpr{Value int64}`, `VarExpr{Name string}`,
  `UnaryExpr{Op TokenType, Value Expr}`, or
  `BinaryExpr{Op TokenType, Left, Right Expr}`.
- **notes**: unlike a plain CLI compiler, `parser.go` performs NO
  variable-definedness checking — `Compile` is the sole place that checks
  whether a `VarExpr.Name` is known, because only `Compile` receives the
  persistent `*Environment`. A consumer must never assume `ParseProgram`
  already validated variable use.

### compiler → env (protocol)

- **type**: protocol
- **producer**: compiler (compiler.go)
- **consumer**: env (env.go), and indirectly vm (vm.go)
- **interface**: `Environment{Slots map[string]int, Values []int64}`,
  `NewEnvironment() *Environment`
- **notes**: `Compile` is the only code that ever registers a new key in
  `env.Slots` or appends to `env.Values` — it does so exactly per the
  variable resolution algorithm in the Behavioral Specification (register
  only an `AssignStmt`'s `Name`, only after its `Value` compiles
  successfully, never for a `VarExpr` read). `vm.go`'s `Run` only reads and
  overwrites existing indices in `env.Values` — it must never append to or
  resize `env.Values`, and must never write to `env.Slots` at all.

### compiler → vm (format)

- **type**: format
- **producer**: compiler (compiler.go)
- **consumer**: vm (vm.go)
- **interface**: `*CompiledProgram{Instructions []Instruction}`,
  `Instruction{Op OpCode, Operand int64}`, and the 9 `OpCode` constants.
- **notes**: exact per-opcode stack effect — including `OpSub`/`OpDiv`'s
  operand order (`Left` pushed first/deeper, `Right` second/on top) — is
  fixed in the Behavioral Specification and must match exactly between
  `Compile`'s emission order and `Run`'s interpretation.

### handlers → env/vm (protocol)

- **type**: protocol
- **producer**: parser, compiler, env, vm
- **consumer**: handlers (handlers.go)
- **interface**: `NewParser(source string) *Parser`,
  `(*Parser).ParseProgram() (*Program, error)`,
  `Compile(stmt Stmt, env *Environment) (*CompiledProgram, error)`,
  `NewVM() *VM`,
  `(*VM).Run(cp *CompiledProgram, env *Environment, out io.Writer) error`
- **notes**: `HandleEval` must call these in the fixed order `ParseProgram`
  → `Compile` → `Run`, stopping at the first non-nil error, and must acquire
  `mu` for the entire duration from reading `env`/`history` through writing
  the new `HistoryEntry` — not just around the `Run` call — so that two
  concurrent requests can never interleave their reads/writes of `env`.
  `Compile` is always called with the package-level `env`, never a
  locally-constructed `*Environment` (that would defeat cross-submission
  persistence entirely).

### handlers → templates (data-shape)

- **type**: data-shape
- **producer**: handlers (assembles `PageData`)
- **consumer**: templates (templates.go)
- **interface**: `PageData{History []HistoryEntry}`,
  `HistoryEntry{Input, Output, Err string}`
- **notes**: the `"index"` template iterates `.History` in slice order
  (oldest first). Each entry with a non-empty `Err` must render inside an
  element with exactly `class="error"` — this exact literal class name is
  required (not illustrative), since the integration bead below asserts on
  it directly. There is no HTMX fragment target — the whole page re-renders
  on every `GET /`, so there is no partial-update scope to specify.

## Decomposition Notes

**Critical dependency chain — do not reorder:**

1. **lexer**: `Token`, `TokenType`, `NewLexer`, `(*Lexer).Next`. No
   dependencies on any other bead.
2. **parser**: AST types, `NewParser`, `(*Parser).ParseProgram`. Calls
   `NewLexer`/`(*Lexer).Next` internally (bead 1). Does not call anything
   from compiler, env, or vm. Must implement the 1-token lookahead described
   in Behavioral Specification's "Statement dispatch."
3. **env**: `Environment`, `NewEnvironment`. No dependencies on any other
   bead — pure data type.
4. **compiler**: `OpCode`, `Instruction`, `CompiledProgram`, `Compile`. Takes
   a `Stmt` (bead 2's output type) and a `*Environment` (bead 3's type) as
   input. Does not call anything from lexer or vm.
5. **vm**: `VM`, `NewVM`, `(*VM).Run`. Takes a `*CompiledProgram` (bead 4's
   output type) and a `*Environment` (bead 3's type) as input. Does not
   reference any type from parser or lexer. **This bead's test file must
   include explicit cases for all four division sign combinations, not just
   the positive case**: `7/2`=3, `-7/2`=-3 (NOT -4), `7/-2`=-3 (NOT -4),
   `-7/-2`=3. Division-truncation direction is easy to get backwards, and the
   specific values must appear in this bead's spec verbatim — do not rely on
   the general "truncating toward zero" rule alone and leave REFINE_TESTS_WRITE
   to re-derive the negative-operand cases itself.
6. **handlers+templates**: `HistoryEntry`, `PageData`, `HandleIndex`,
   `HandleEval`, `InitTemplates`, `RenderIndex`. Calls beads 2, 3, 4, 5 in
   the fixed order specified in `HandleEval`'s Behavioral Specification. Must
   be decomposed last — its handler logic depends on the final signatures of
   every other bead.
7. **cli** (main.go): wires everything together in `main()`. Decomposed
   after bead 6, since it references `HandleIndex`/`HandleEval`.

**Integration bead scenarios** (bounded — one fixed scenario each):
- Using `httptest.NewServer`, `POST /eval` with `input=x=5`, follow the
  redirect, then `POST /eval` with `input=print(x*2)`, follow the redirect,
  and assert the final `GET /` response body contains `"10"` — confirming
  cross-request variable persistence through the real handler chain, not
  just directly-called Go functions.
- A second, separate integration bead for the error path: `POST /eval` with
  `input=print(1/0)`, follow the redirect, and assert the response body
  contains an element with exactly `class="error"` whose text includes
  `"division by zero"`.

**Must not bind to a fixed port in tests** — use `httptest.NewServer`
throughout, never `http.ListenAndServe` directly in a test.

**No jump/branch instructions exist in this instruction set.** Since the
source language has no control flow, `Run`'s execution order is always
exactly `cp.Instructions`' slice order. Do not add a program-counter field or
jump opcodes.

**`sync.Mutex` scope**: `mu` must be held for the entire body of `HandleEval`
from the point `env`/`history` are first touched to the point the new
`HistoryEntry` is appended — not narrowly scoped around just the `Run` call —
and for the read of `history` in `HandleIndex`. A narrower lock scope in
`HandleEval` (e.g. only around `vm.Run`) would still allow two concurrent
requests to interleave their `Compile` calls against the same `env`, corrupting
slot registration.