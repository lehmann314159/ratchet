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

**var declarations — one space between `var` and the name:**
Write `var templates *template.Template`, not `vartemplates *template.Template`.
The `var` keyword and the variable name are always separated by a space.

**Compile-time assertions (api_check_test.go):**
The api_check_test.go file locks exported function signatures at compile time using
package-level blank-identifier assignments:

  var _ func(n int) (int, error) = Fib
  var _ func(src image.Image, msg string) (image.Image, error) = Encode

Generate one var _ line per exported function in the public API. Place assertions at
file scope (not inside any test function) — package-level declarations fail the build
immediately if a signature is wrong.