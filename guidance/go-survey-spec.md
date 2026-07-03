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

**Compile-time assertions (api_check_test.go):**
The api_check_test.go file locks exported function signatures at compile time using
package-level blank-identifier assignments:

  var _ func(n int) (int, error) = Fib
  var _ func(src image.Image, msg string) (image.Image, error) = Encode

Generate one var _ line per exported function in the public API. Place assertions at
file scope (not inside any test function) — package-level declarations fail the build
immediately if a signature is wrong.