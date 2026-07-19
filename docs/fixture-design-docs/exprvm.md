# ExprVM — Design Document

## Overview

A CLI tool that compiles a small integer expression language into a custom
stack-based bytecode format, then executes that bytecode on a virtual machine.
Usage: `exprvm <file>` reads the named source file, compiles it, runs it, and
prints output to stdout. There is no interactive/REPL mode and no reading from
stdin.

Out of scope: floating-point numbers, control flow (no `if`, no loops, no
functions/procedures), comments in source, string or boolean types, a
disassembler or any bytecode-inspection output, and any file I/O beyond reading
the one source file named on the command line.

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
  `strconv.ParseInt(text, 10, 64)` (i.e. exceeds `math.MaxInt64` =
  9223372036854775807) is a parse error — it is NOT silently wrapped or
  truncated. Worked example: the literal `"9223372036854775808"` (one more
  than `math.MaxInt64`) causes `ParseProgram` to return a non-nil error; the
  literal `"9223372036854775807"` (exactly `math.MaxInt64`) is valid. There is
  no negative-number literal — a
  negative value is always produced by the unary-minus operator applied to a
  non-negative literal or sub-expression (see Lexer/Parser rules below).
  There is no unary-plus operator; a leading `+` before an expression is
  always a parse error.
- Whitespace: space (`' '`) and tab (`'\t'`) between tokens are insignificant.
  Newline (`'\n'`) is significant — it separates statements (see Parser rules).
- The only output-producing construct is `print(<expr>)`. There is no implicit
  "auto-print the value of a bare expression" behavior — an expression
  statement with no `print(...)` wrapper is not part of the grammar at all (a
  bare expression on its own line is a parse error, not a silently-discarded
  statement).

## Architecture

```
exprvm/
├── go.mod
├── main.go       — func main() only
├── lexer.go      — Token, TokenType, NewLexer, (*Lexer).Next
├── parser.go     — Program, Stmt, AssignStmt, PrintStmt, Expr, NumberExpr,
│                    VarExpr, UnaryExpr, BinaryExpr, NewParser, (*Parser).ParseProgram
├── compiler.go   — OpCode, Instruction, CompiledProgram, Compile
├── vm.go         — VM, NewVM, (*VM).Run
└── *_test.go     — one test file per source file above, plus integration_test.go
```

All `.go` files use `package main` at the project root — no subdirectories.

**File assignment rules (strict):**
- `main.go` contains exactly `func main()`. No other functions, no types, no
  package-level variables.
- `lexer.go` contains: `Token`, `TokenType` and its constants, `NewLexer`,
  `(*Lexer).Next`. No parsing logic — `lexer.go` never references `Program`,
  `Stmt`, or `Expr`.
- `parser.go` contains: every AST type (`Program`, `Stmt`, `AssignStmt`,
  `PrintStmt`, `Expr`, `NumberExpr`, `VarExpr`, `UnaryExpr`, `BinaryExpr`),
  `NewParser`, `(*Parser).ParseProgram`. `parser.go` constructs its own
  `*Lexer` internally (see Behavioral Specification) but contains no `OpCode`,
  `Instruction`, or `VM` references.
- `compiler.go` contains: `OpCode` and its constants, `Instruction`,
  `CompiledProgram`, `Compile`. Does NOT reference `Token`, `TokenType`, or
  `*Lexer` — `Compile` takes an already-parsed `*Program` as its only input.
- `vm.go` contains: `VM`, `NewVM`, `(*VM).Run`. Does NOT reference any AST type
  (`Program`, `Stmt`, `Expr`, or any of their variants) — `vm.go` operates
  purely on `[]Instruction`, never on the parse tree.
- Do NOT put `OpCode`/`Instruction`/`CompiledProgram` in `vm.go` — they belong
  in `compiler.go` since that is the bead that produces bytecode; `vm.go`
  consumes the format `compiler.go` defines.
- Do NOT put any AST type in `compiler.go` or `vm.go`.
- Do NOT put `func main()`'s pipeline-wiring logic anywhere but `main.go`.

## Data Types and Function Signatures

All `.go` source files use `package main`. The module name is `exprvm`.
Requires Go 1.22.

```go
// ---- lexer.go ----

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
    Statements []Stmt
}

type Stmt interface {
    isStmt()
}

type AssignStmt struct {
    Name  string // variable name being assigned
    Value Expr
}

type PrintStmt struct {
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
    OpAdd                      // no operand (Operand is 0, unused)
    OpSub                      // no operand
    OpMul                      // no operand
    OpDiv                      // no operand
    OpNeg                      // no operand
    OpPrint                    // no operand
)

type Instruction struct {
    Op      OpCode
    Operand int64 // meaning depends on Op; 0 and unused for no-operand ops
}

type CompiledProgram struct {
    Instructions []Instruction
    NumSlots     int // count of distinct variable names; see Compile's slot rule
}

func Compile(prog *Program) (*CompiledProgram, error)

// ---- vm.go ----

type VM struct {
    // unexported fields only; no exported state
}

func NewVM() *VM
func (vm *VM) Run(cp *CompiledProgram, out io.Writer) error
```

### Export signatures

```go
var _ func(string) *Lexer = NewLexer
var _ func(*Lexer) (Token, error) = (*Lexer).Next
var _ func(string) *Parser = NewParser
var _ func(*Parser) (*Program, error) = (*Parser).ParseProgram
var _ func(*Program) (*CompiledProgram, error) = Compile
var _ func() *VM = NewVM
var _ func(*VM, *CompiledProgram, io.Writer) error = (*VM).Run
```

## Behavioral Specification

**`NewLexer(source string) *Lexer`** — returns a lexer positioned at the start
of `source`.

**`(*Lexer).Next() (Token, error)`** — returns the next token and advances
position. Rules, in this exact priority order (each character is consumed by
the first matching rule):

1. A run of one or more space (`' '`) or tab (`'\t'`) characters produces no
   token — skip and continue to the next rule at the character after the run.
2. A single `'\n'` character produces exactly one `TokenNewline`. Consecutive
   newlines each produce their own `TokenNewline` — the lexer never merges
   them (`"\n\n\n"` produces three separate `TokenNewline` tokens across three
   `Next()` calls; the parser, not the lexer, treats runs of newlines as
   equivalent to one statement separator — see `ParseProgram` below).
3. A run of one or more ASCII digit characters (`'0'`-`'9'`) produces one
   `TokenNumber` whose `Text` is exactly that run of digits.
4. The exact 5-character sequence `"print"`, when the character immediately
   following it is not a letter, digit, or underscore (or there is no
   following character at all), produces one `TokenPrint`. This rule is
   checked before rule 5. Worked example: `"printx"` does NOT match this rule
   (the character after `"print"` is `'x'`, a letter) — it falls through to
   rule 5 and produces one `TokenIdent` with `Text` `"printx"`. `"print("`
   DOES match this rule (the character after `"print"` is `'('`, not a
   letter/digit/underscore) and produces `TokenPrint` followed separately by
   `TokenLParen`.
5. A run of one or more characters from `{letters, digits, '_'}` whose first
   character is a letter or `'_'` (never a digit) produces one `TokenIdent`
   with `Text` equal to that run — only reached when rule 4 did not match at
   this position.
6. Each of `+ - * / ( ) =` produces exactly one token of the corresponding
   type (`TokenPlus`, `TokenMinus`, `TokenStar`, `TokenSlash`, `TokenLParen`,
   `TokenRParen`, `TokenAssign`). Every such token is exactly one character —
   two adjacent operator characters are always two separate tokens, never
   combined (`"+-"` is `TokenPlus` then `TokenMinus`, never one token).
7. Reaching the end of `source` produces one `TokenEOF`. Calling `Next()`
   again after a `TokenEOF` has already been returned also returns
   `TokenEOF` (with no error) — `Next()` never panics or errors at end of
   input.
8. Any character not matched by rules 1-7 is a lexer error: return a non-nil
   error and the zero-value `Token`. This error path does NOT advance `pos`
   past the offending character — `pos` must remain exactly where it was
   when the error was detected. Consequently, calling `Next()` again on the
   same `*Lexer` after it has returned an error (with `source` unchanged)
   encounters the same character at the same position and returns the same
   error again — it must NOT silently skip past the bad character and
   return a token computed from further along the string. Worked example:
   for source `"@"`, two consecutive `Next()` calls each return the same
   error (e.g. `unexpected character '@' at position 0`); the second call
   never returns `TokenEOF` or any other token.

**`NewParser(source string) *Parser`** — constructs a parser that creates and
owns its own `*Lexer` internally, via `NewLexer(source)`. Callers of
`NewParser` never construct or interact with a `*Lexer` directly.

**`(*Parser).ParseProgram() (*Program, error)`** — parses the full token
stream into a `*Program`. Grammar:

```
statement  := assignment | print_stmt
assignment := IDENT '=' expr
print_stmt := 'print' '(' expr ')'
expr       := term (('+' | '-') term)*
term       := factor (('*' | '/') factor)*
factor     := NUMBER | IDENT | '-' factor | '(' expr ')'
```

Top-level driving loop, stated as an exact algorithm (not left to
interpretation): skip zero or more leading `TokenNewline`. Then repeat: if the
next token is `TokenEOF`, stop successfully — no more statements. Otherwise,
parse exactly one `statement` per the grammar above and append it to
`Program.Statements`; then require the next token to be either `TokenNewline`
(consume it, then also skip zero or more further `TokenNewline` — i.e. blank
lines between statements are permitted and are not statements themselves) or
`TokenEOF` (stop successfully — a final statement need not be followed by a
newline). Any other token immediately after a statement is a parse error. An
empty source file (or one containing only whitespace/newlines) is valid and
produces a `Program` with `Statements` empty (length 0) — this is not an
error.

**Lexer-error propagation** (applies to every `Next()` call made anywhere
during parsing — the driving loop's own calls and every call made while
parsing a `statement`/`expr`/`term`/`factor`, not just the top level): if any
call to `(*Lexer).Next()` returns a non-nil error, `ParseProgram` immediately
returns `(nil, thatError)` — the lexer's error returned verbatim as
`ParseProgram`'s own error — and requests no further tokens. This case must
be checked before inspecting the returned `Token`'s `Type` field; do NOT
inspect only `Type` and treat the accompanying zero-value `Token` (which has
`Type == TokenNumber` and `Text == ""`, since `TokenNumber` is `TokenType`'s
zero value per the `iota` block) as if it were a real empty numeric literal.
Worked example: source `"x=@\n"` — `'@'` matches none of `Next`'s rules 1-7
and is a lexer error (rule 8); `ParseProgram` must return that error, not a
`Program` containing an `AssignStmt` with a bogus zero-valued `NumberExpr`.

Worked examples for the driving loop: `"x=1\n\n\nprint(x)\n"` (two blank
newlines between statements) and `"x=1\nprint(x)"` (no trailing newline at
all) both produce the identical 2-statement `Program` (one `AssignStmt`, one
`PrintStmt`). `"x=1 y=2\n"` (no newline between the two assignments) is a
parse error: after parsing `x=1`, the next token is `TokenIdent("y")`, which
is neither `TokenNewline` nor `TokenEOF`.

**Left-associativity** (the `expr`/`term` repetition folds left, matching
standard left-to-right evaluation): source `"10-3-2"` parses as
`BinaryExpr{Op:TokenMinus, Left:BinaryExpr{Op:TokenMinus,Left:NumberExpr{10},Right:NumberExpr{3}}, Right:NumberExpr{2}}`
— i.e. `(10-3)-2`, which the VM will evaluate to 5. It is NOT
`10-(3-2)` (which would evaluate to 9).

**Precedence** (`term` binds `*`/`/` tighter than `expr` binds `+`/`-`, because
`expr`'s repetition operates on whole `term`s): source `"2+3*4"` parses as
`BinaryExpr{Op:TokenPlus, Left:NumberExpr{2}, Right:BinaryExpr{Op:TokenStar,Left:NumberExpr{3},Right:NumberExpr{4}}}`
(evaluates to 2+(3*4)=14). It is NOT
`BinaryExpr{Op:TokenStar, Left:BinaryExpr{Op:TokenPlus,Left:2,Right:3}, Right:4}`
(which would evaluate to (2+3)*4=20).

**Unary minus** (`factor`'s `'-' factor` rule binds tighter than any binary
operator, since it is applied while still inside `factor`, before `term` or
`expr` ever combine anything): source `"-2+3"` parses as
`BinaryExpr{Op:TokenPlus, Left:UnaryExpr{Op:TokenMinus,Value:NumberExpr{2}}, Right:NumberExpr{3}}`
(evaluates to (-2)+3=1), NOT
`UnaryExpr{Value:BinaryExpr{Op:TokenPlus,Left:2,Right:3}}` (which would be
-(2+3)=-5). Source `"2- -3"` (a space separates the two `'-'` characters, but
per lexer rule 1 that space is insignificant) parses as
`BinaryExpr{Op:TokenMinus, Left:NumberExpr{2}, Right:UnaryExpr{Op:TokenMinus,Value:NumberExpr{3}}}`
(evaluates to 2-(-3)=5) — the first `'-'` is consumed by `expr`'s
binary-minus alternative, the second by `factor`'s unary-minus rule. There is
no "double negative" merging; lexer rule 6 guarantees each `'-'` is always its
own single-character `TokenMinus`, so the parser alone determines whether a
given `TokenMinus` is binary or unary based on grammar position.

**Undefined-variable rule** (checked here in `ParseProgram`, not deferred to
`Compile` or `Run` — see the parser→compiler contract below for why this
matters): a `VarExpr{Name: s}` may only be produced for a use of identifier
`s` inside an `expr` if some earlier `AssignStmt` with `Name == s` already
exists at a smaller index in `Program.Statements` (i.e., appears earlier in
the source, scanning top to bottom). If identifier `s` is used inside an
`expr` and no earlier `AssignStmt` with that `Name` exists yet,
`ParseProgram` returns a non-nil error. Re-assignment is explicitly allowed
and is not an error: a second `AssignStmt` for a `Name` that already has an
earlier `AssignStmt` is valid. Worked examples: `"print(x)\nx=1\n"` is a
parse error (`x` used at statement index 0; its only `AssignStmt` is at
index 1, which is not earlier). `"x=1\nprint(x)\n"` is valid. `"x=1\nx=2\nprint(x)\n"`
is valid (two `AssignStmt`s for `x`, both allowed; the `VarExpr` at index 2
is satisfied by either earlier assignment — see `Compile`'s slot rule for
which one wins at runtime).

**`Compile(prog *Program) (*CompiledProgram, error)`** — translates an
already-validated `*Program` into bytecode. `Compile` never itself checks for
undefined variables; it relies entirely on `ParseProgram` having already
enforced the undefined-variable rule above (see the parser→compiler contract).

**Variable slot assignment**: scan `Program.Statements` from index 0 upward;
each time an `AssignStmt.Name` is encountered that has not been seen before in
this scan, assign it the next sequential slot index starting from 0. A `Name`
seen again (a re-assignment) reuses its already-assigned slot — it never gets
a second slot. `CompiledProgram.NumSlots` is set to the count of distinct
names assigned this way (not the count of `AssignStmt` nodes). Worked example:
`"a=1\nb=2\na=3\nprint(a)\nprint(b)\n"` has `AssignStmt.Name` values in
statement order `[a, b, a]`; distinct names in first-appearance order are
`[a, b]`, so `a`→slot 0, `b`→slot 1 (the third statement, `a=3`, reuses slot
0), and `NumSlots` = 2.

**Code generation**, exact instruction sequence per AST node (this fixes
operand order for the non-commutative operators — see the VM's per-opcode
rules for why the order matters):
- `NumberExpr{Value: n}` → one instruction: `OpPushConst` with `Operand: n`.
- `VarExpr{Name: s}` → one instruction: `OpLoad` with `Operand:` s's assigned
  slot index.
- `UnaryExpr{Value: e}` → [instructions for `e`], then one `OpNeg`.
- `BinaryExpr{Op, Left, Right}` → [instructions for `Left`], then
  [instructions for `Right`], then one instruction for `Op` (`OpAdd` for
  `TokenPlus`, `OpSub` for `TokenMinus`, `OpMul` for `TokenStar`, `OpDiv` for
  `TokenSlash`). `Left`'s instructions always precede `Right`'s.
- `AssignStmt{Name, Value}` → [instructions for `Value`], then one `OpStore`
  with `Operand:` `Name`'s assigned slot index.
- `PrintStmt{Value}` → [instructions for `Value`], then one `OpPrint`.
- `Program` → the concatenation of each `Statement`'s instructions, in
  `Statements` order, index 0 first.

**`NewVM() *VM`** — returns a `VM` with no allocated stack or slots yet (both
are allocated fresh inside `Run`, from `cp.NumSlots` and an initially-empty
stack). A single `*VM` returned by one `NewVM()` call may have `Run` called on
it more than once, with different `*CompiledProgram` arguments; each `Run`
call starts with a fresh, empty operand stack and a fresh slot array of length
`cp.NumSlots` (all zero-initialized) — no state carries over between `Run`
calls on the same `*VM`.

**`(*VM).Run(cp *CompiledProgram, out io.Writer) error`** — executes
`cp.Instructions` in order (index 0, 1, 2, ... — there is no branching or
jump instruction in this instruction set, since the source language has no
control flow, so execution order is always exactly the instruction slice's
order) against a fresh `[]int64` operand stack (initially empty, meaning
length 0) and a fresh `[]int64` slot array of length `cp.NumSlots` (all
initialized to 0). Per-instruction effect, stated as an exact stack
before/after (rightmost element is the top of stack):

- `OpPushConst` (Operand=n): `[...]` → `[..., n]`.
- `OpLoad` (Operand=slot): `[...]` → `[..., slots[slot]]`.
- `OpStore` (Operand=slot): `[..., v]` → `[...]`; sets `slots[slot] = v`. (The
  value is removed from the stack — after any `AssignStmt`'s compiled
  instructions run, the operand stack is back to exactly what it was before
  that statement began.)
- `OpAdd`: `[..., a, b]` → `[..., a+b]`.
- `OpSub`: `[..., a, b]` → `[..., a-b]`. Here `a` is the value pushed first
  (deeper in the stack) and `b` is pushed second (on top) — per `Compile`'s
  rule that `Left`'s instructions run before `Right`'s, `a` corresponds to
  `Left` and `b` to `Right`. Worked example: source `"10-3"` compiles to
  `[OpPushConst 10, OpPushConst 3, OpSub]`; immediately before `OpSub`
  executes, the stack is `[10, 3]` (10 deeper, 3 on top); `OpSub` computes
  `a-b` = `10-3` = 7 — "10 minus 3", matching the source, not "3 minus 10".
- `OpMul`: `[..., a, b]` → `[..., a*b]`.
- `OpDiv`: `[..., a, b]` → `[..., a/b]` using Go's native `int64` `/`
  (truncation toward zero — see Domain Parameters for worked truncation
  examples). Same operand correspondence as `OpSub`: `a` is `Left`, `b` is
  `Right`. Worked example: source `"7/2"` compiles to
  `[OpPushConst 7, OpPushConst 2, OpDiv]`; stack before `OpDiv` is `[7, 2]`;
  result is `7/2` = 3 (not `2/7` = 0). If `b == 0` at the moment `OpDiv`
  executes, `Run` immediately returns a non-nil error with message exactly
  `"runtime error: division by zero"` and does not execute any further
  instructions in `cp.Instructions` — anything already written to `out` by
  earlier `OpPrint` instructions in this same `Run` call remains written
  (`Run` never retroactively removes or rewinds prior output). Worked
  example: for `"print(5)\nprint(1/0)\n"`, `out` receives exactly `"5\n"`
  before `Run` returns the division-by-zero error — the second `print`'s
  `OpPrint` is never reached.
- `OpNeg`: `[..., a]` → `[..., -a]`.
- `OpPrint`: `[..., a]` → `[...]`; additionally writes to `out` the base-10
  string form of `a` (Go's `strconv.FormatInt(a, 10)`: a leading `'-'` for
  negative values, no leading `'+'` for non-negative values, no leading
  zeros, exactly `"0"` for zero) followed by one `'\n'` byte.

If every instruction executes with no division-by-zero encountered, `Run`
returns `nil` after the last instruction. An empty `cp.Instructions` (from an
empty source `Program`) is valid: `Run` executes zero instructions, writes
nothing to `out`, and returns `nil`.

**`main()`** — reads `os.Args[1]` as a file path. If `len(os.Args) < 2` (no
file argument given), print `"usage: exprvm <file>"` to stderr and exit with
status 2, without attempting to open any file or run any part of the
pipeline. Otherwise, call `os.ReadFile(os.Args[1])`. If it returns a non-nil
error (file does not exist, is unreadable, is a directory, or any other OS
error), print that error's message to stderr prefixed with
`"error reading file: "` (e.g. `"error reading file: <message>"`) and exit
with status 1 — do not call `NewParser` or any later pipeline stage, and do
not treat this as either the "usage" case (status 2) or a "compile error: "
case. Otherwise, with the file's contents as `source`, run the pipeline in
this fixed order: call `NewParser(source).ParseProgram()`; if it returns a
non-nil error, print that error's message to stderr prefixed with
`"compile error: "` (e.g. `"compile error: <message>"`) and exit with status
1 — do not call `Compile` or construct a `*VM`. Otherwise call
`Compile(prog)`; if it returns a non-nil error, print that error's message to
stderr prefixed with `"compile error: "` and exit with status 1 — do not
construct a `*VM`. Otherwise call `NewVM().Run(cp, os.Stdout)` — `out` must be
`os.Stdout` itself, not a buffer. If `Run` returns a non-nil error, print that
error's message to stderr exactly as returned (it already reads
`"runtime error: division by zero"` — do not add another prefix) and exit
with status 1. If `Run` returns `nil`, exit with status 0 (Go's default for a
`main()` that returns normally — do not call `os.Exit(0)` explicitly, but
also do not let any earlier step's `os.Exit(1)`/`os.Exit(2)` calls be skipped).

## Required Test Scenarios

These scenarios exist because a model that knows the algorithm names
(recursive-descent parsing, stack VM) can still write a plausible-looking but
wrong expected value; each one gives the exact required output so no test
has to re-derive it.

1. **Left-associativity**: source `"print(10-3-2)\n"` → output exactly
   `"5\n"`. Do NOT expect `"9\n"` (that would be `10-(3-2)`, the wrong
   grouping).
2. **Precedence**: source `"print(2+3*4)\n"` → output exactly `"14\n"`. Do
   NOT expect `"20\n"` (that would be `(2+3)*4`).
3. **Unary minus vs. binary minus**: source `"print(-2+3)\n"` → `"1\n"`.
   Source `"print(2- -3)\n"` → `"5\n"`. Source `"print(-(2+3))\n"` →
   `"-5\n"`.
4. **Division truncation**: source `"print(7/2)\n"` → `"3\n"`. Source
   `"print(-7/2)\n"` → `"-3\n"` (do NOT expect `"-4\n"`). Source
   `"print(7/-2)\n"` → `"-3\n"` (do NOT expect `"-4\n"`). Source
   `"print(-7/-2)\n"` → `"3\n"`.
5. **Division by zero**: source `"print(5)\nprint(1/0)\n"` — `Run` must
   return an error with message exactly `"runtime error: division by zero"`,
   and `out` must have received exactly `"5\n"` (from the first `print`)
   before that error is returned.
6. **Variable slot reuse**: source `"a=1\nb=2\na=3\nprint(a)\nprint(b)\n"` —
   `Compile` must produce `NumSlots == 2` (not 3), and running the compiled
   program must produce output exactly `"3\n2\n"`.
7. **Undefined variable is a parse error**: source `"print(x)\n"` (no prior
   assignment anywhere) — `ParseProgram` must return a non-nil error;
   `Compile` and `Run` must never be reached in this scenario.
8. **Re-assignment is valid**: source `"x=1\nx=2\nprint(x)\n"` —
   `ParseProgram` returns no error, and running the compiled program produces
   output exactly `"2\n"`.
9. **Blank-line tolerance**: sources `"x=1\n\n\nprint(x)\n"` and
   `"x=1\nprint(x)\n"` must both produce a `Program` with exactly 2 elements
   in `Statements`.
10. **`print` keyword vs. identifier boundary**: source
    `"printx=5\nprint(printx)\n"` — the lexer must tokenize `printx` as a
    single `TokenIdent` (never `TokenPrint` followed by an identifier `"x"`),
    and running the compiled program must produce output exactly `"5\n"`.
11. **Empty program**: an empty source string (`""`) — `ParseProgram` must
    return a `Program` with `Statements` of length 0 and a nil error; running
    the resulting compiled program must produce no output and a nil error.
12. **Oversized numeric literal is a parse error**: source
    `"print(9223372036854775808)\n"` (one more than `math.MaxInt64`) —
    `ParseProgram` must return a non-nil error; `Compile` and `Run` must
    never be reached. Source `"print(9223372036854775807)\n"` (exactly
    `math.MaxInt64`) must be valid and produce output exactly
    `"9223372036854775807\n"`.
13. **Missing/unreadable file (integration-level, exercises `main` directly,
    not the library functions)**: running the compiled binary with a file
    path that does not exist must exit with status 1 and print a stderr
    message starting with `"error reading file: "` — not `"compile error: "`
    and not the usage message.
14. **Lexer error propagates through `ParseProgram`**: source `"x=@\n"` —
    `ParseProgram` must return a non-nil error (the lexer's rule-8 error for
    `'@'`, propagated verbatim). It must NOT return a nil error with a
    `Program` containing an `AssignStmt{Name:"x", Value:NumberExpr{Value:0}}`
    (the zero-value `Token`/`Type` trap described in the Behavioral
    Specification's lexer-error-propagation rule).
15. **`(*Lexer).Next()` does not advance past an unrecognized character**:
    for a `*Lexer` constructed directly (not through `ParseProgram`) with
    source `"abc @ def"`, calling `Next()` three times (consuming `"abc"`,
    then the whitespace-skip-and-`'@'` call, then a third call) — the second
    call must return a non-nil error and NOT advance `pos` past `'@'`. The
    third call must repeat the same error (still at `'@'`), NOT return
    `TokenIdent{Text:"def"}`. (This is the exact case where an
    unconditional `pos++` before checking which rule matched — rather than
    only on a matched case — causes an error call to silently consume the
    bad character, so a later call resumes past it instead of repeating the
    same error.)

## Cross-Bead Contracts

### parser → compiler (format)

- **type**: format
- **producer**: parser (parser.go)
- **consumer**: compiler (compiler.go)
- **interface**: `*Program{Statements []Stmt}`, where `Stmt` is
  `AssignStmt{Name string, Value Expr}` or `PrintStmt{Value Expr}`, and
  `Expr` is `NumberExpr{Value int64}`, `VarExpr{Name string}`,
  `UnaryExpr{Op TokenType, Value Expr}`, or
  `BinaryExpr{Op TokenType, Left, Right Expr}`.
- **notes**: `Compile` assumes every `VarExpr.Name` in the given `*Program`
  already satisfies the undefined-variable rule from the Behavioral
  Specification (an earlier `AssignStmt` with the same `Name` exists at a
  smaller `Statements` index) — `Compile` does not re-check this itself. This
  invariant is only guaranteed by `ParseProgram` having produced the
  `*Program`; `main()` (and any test) must always call `ParseProgram` before
  `Compile` and must never hand-construct a `*Program` that skips that
  validation.

### compiler → vm (format)

- **type**: format
- **producer**: compiler (compiler.go)
- **consumer**: vm (vm.go)
- **interface**: `*CompiledProgram{Instructions []Instruction, NumSlots int}`,
  `Instruction{Op OpCode, Operand int64}`, and the 9 `OpCode` constants
  (`OpPushConst`, `OpLoad`, `OpStore`, `OpAdd`, `OpSub`, `OpMul`, `OpDiv`,
  `OpNeg`, `OpPrint`).
- **notes**: the exact per-opcode stack effect — including `OpSub`/`OpDiv`'s
  operand order (`Left` is pushed first/deeper, `Right` second/on top) — is
  fixed in the Behavioral Specification and must match exactly between
  `Compile`'s emission order and `Run`'s interpretation; a mismatch here
  would silently reverse subtraction/division results without any build or
  type error. `NumSlots` must equal exactly the count of distinct variable
  names `Compile` assigned slots to (see the slot-assignment rule) — `Run`
  allocates `make([]int64, cp.NumSlots)`, so `Compile` must never emit an
  `OpLoad`/`OpStore` `Operand` outside `[0, NumSlots)`.

### main → parser/compiler/vm (protocol)

- **type**: protocol
- **producer**: parser, compiler, vm
- **consumer**: main (main.go)
- **interface**: `NewParser(source string) *Parser`,
  `(*Parser).ParseProgram() (*Program, error)`,
  `Compile(prog *Program) (*CompiledProgram, error)`, `NewVM() *VM`,
  `(*VM).Run(cp *CompiledProgram, out io.Writer) error`
- **notes**: `main` must call these in the fixed order `os.ReadFile` →
  `ParseProgram` → `Compile` → `Run`, stopping at the first non-nil error,
  exactly as specified in the Behavioral Specification's `main()` entry
  (missing argument → stderr `"usage: exprvm <file>"` + exit 2; file-read
  error → stderr `"error reading file: "` + the OS error message + exit 1;
  `ParseProgram`/`Compile` error → stderr `"compile error: "` + the error
  message + exit 1; `Run` error → stderr the error message as-is + exit 1;
  success → exit 0). `Run`'s `out` argument must be `os.Stdout` itself.

## Decomposition Notes

**Critical dependency chain — do not reorder:**

1. **lexer**: `Token`, `TokenType`, `NewLexer`, `(*Lexer).Next`. No
   dependencies on any other bead.
2. **parser**: `Program`/`Stmt`/`Expr` types, `NewParser`,
   `(*Parser).ParseProgram`. Calls `NewLexer`/`(*Lexer).Next` internally
   (bead 1). Does not call anything from compiler or vm.
3. **compiler**: `OpCode`, `Instruction`, `CompiledProgram`, `Compile`. Takes
   a `*Program` (bead 2's output type) as input. Does not call anything from
   lexer or vm.
4. **vm**: `VM`, `NewVM`, `(*VM).Run`. Takes a `*CompiledProgram` (bead 3's
   output type) as input. Does not reference any type from parser or lexer.
   **This bead's test file must include explicit cases for all four
   division sign combinations, not just the positive case**: `7/2`=3,
   `-7/2`=-3 (NOT -4), `7/-2`=-3 (NOT -4), `-7/-2`=3. Division-truncation
   direction is easy to get backwards, and the specific values must appear
   in this bead's spec verbatim — do not rely on the general "truncating
   toward zero" rule alone and leave REFINE_TESTS_WRITE to re-derive the
   negative-operand cases itself.
5. **cli** (main.go): wires beads 1-4 together in `main()`. Must be
   decomposed last, since its exit-code/error-prefix behavior depends on the
   final signatures of `ParseProgram`, `Compile`, and `Run`.

**Integration bead scenarios** (bounded, per project's CLI checklist — write
one fixed scenario each, not a coverage goal):
- Build the compiled `exprvm` binary. Write a temp file containing
  `"x=2\ny=3\nprint(x+y*2)\n"`. Run the binary with that file's path as its
  only argument. Assert the process exit code is 0 and stdout is exactly
  `"8\n"` (2+3*2=8, confirming precedence holds end-to-end through the real
  binary, not just in unit tests).
- A second, separate integration bead for the runtime-error path: write a
  temp file containing `"print(1/0)\n"`. Run the binary with that file's
  path as its only argument. Assert the process exit code is 1 and stderr
  contains the substring `"division by zero"`.
- A third, separate integration bead for the missing-file path: run the
  binary with a file path known not to exist (e.g. a path inside a freshly
  created empty temp directory). Assert the process exit code is 1 and
  stderr contains the substring `"error reading file: "`.

**No jump/branch instructions exist in this instruction set.** Since the
source language has no control flow, `Run`'s execution order is always
exactly `cp.Instructions`' slice order, start to end — there is no program
counter manipulation to specify beyond "execute index 0, then 1, then 2, ...".
Do not add a program-counter field or jump opcodes; they are out of scope for
this version of the language.