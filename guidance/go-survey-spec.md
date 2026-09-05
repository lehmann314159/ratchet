You are writing a Go project manifest (types, function signatures, stub bodies).

**Stub bodies:**
Every function must return zero values for its declared return types. No logic — only
a valid return statement:

  func Add(a, b int) int                         { return 0 }
  func Find(id int) (*User, error)               { return nil, nil }
  func IsValid(s string) bool                    { return false }
  func Process(items []string) ([]string, error) { return nil, nil }

A stub must compile. If the function returns an interface, return nil. If it returns
a struct value, return the zero value: return MyStruct{}.

**Type completeness — every referenced type must be declared:**
If any declaration uses a type (e.g. `*Piece`, `Color`, `CastlingRights`), that type must
appear in the declarations of SOME file in this manifest. Do not reference types you have
not declared. Check each file's declarations for undefined types before finalising your output.

Example — if game.go declares:
  type Game struct { Board [8][8]*Piece; Turn Color; Castling CastlingRights }
then Piece, Color, and CastlingRights must each appear as a type declaration in game.go
(or another file). Omitting them produces "undefined: Piece" compile errors.

**Imports — you choose them, and you resolve every ambiguity:**
List each file's imports in its `imports` array as full import paths, e.g.
`["net/http", "html/template", "example.com/mymod/store"]`. The scaffolder
renders the import block from this list — it does NOT guess for you, and a
context-free guesser cannot tell `html/template` from `text/template` given a
bare `*template.Template`. When the design document names a specific import
for a symbol, transcribe exactly that one:

  - `html/template` vs `text/template` — a web project rendering HTML uses
    `html/template`; only use `text/template` if the doc explicitly says so.
  - `math/rand/v2` vs `math/rand`
  - `crypto/rand` vs `math/rand`
  - `log/slog` vs `log`

If a package-level symbol's type comes from an imported package (e.g.
`var templates *template.Template`), every file that declares or references
that symbol must list the SAME import path for it. A split — one file's
`templates` backed by `html/template`, another's by `text/template` — is a
hard VERIFY_MANIFEST failure (check 6, cross_file_type).

**var declarations — one space between `var` and the name:**
Write `var templates *template.Template`, not `vartemplates *template.Template`.
The `var` keyword and the variable name are always separated by a space.

**Compile-time assertions (do_not_use_this_test.go):**
The do_not_use_this_test.go file is generated automatically by the scaffolder from your
manifest's exported functions, variables, and constants — you never write it yourself. It
locks each exported symbol's existence at compile time using package-level blank-identifier
assignments:

  var (
      _ = Fib
      _ = Encode
  )

A package-level var _ assignment fails the build immediately if the symbol doesn't exist
with the name and signature your manifest declared.