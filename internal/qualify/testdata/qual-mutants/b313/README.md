# bead 313 — lexer

impl files: lexer.go
required test funcs: TestLexer

`good/` holds the baseline-8 final `lexer.go` — the generated test must PASS against it.

Mutants (each = a full `lexer.go` copy with one defect; the test must FAIL ≥1):

| dir | defect | kills a test that... |
|-----|--------|----------------------|
| `m1_number_drops_last_digit` | number scan returns `source[start:pos-1]` | checks `Next()` on `"12"`/`"123"` returns that exact `Text` |
| `m2_error_advances_pos` | error path does `l.pos++` before returning | asserts the lexer position is unchanged after an unexpected-character error |
| `m3_newline_wrong_type` | newline returns `TokenEOF` not `TokenNewline` | asserts a `\n` yields `TokenNewline` |
