You are working on a Go project. Apply these language-specific rules:

**Orient step (step 1 in your process):**
  Source files to read: all *.go files in the project root (not in subdirectories unless
  the spec places code there).
  Build command: go build ./...
  Stale file cleanup (step 2): overwrite a stray .go file with only its package line,
  e.g.: package fib

**Compile-time assertions:**
Use these in api_check_test.go to lock function signatures at build time. Place at package
scope — not inside any function:

  var _ func(n int) (int, error) = Fib
  var _ func(src image.Image, msg string) (image.Image, error) = Encode

A package-level var _ assignment fails the build immediately if the signature is wrong.
Assertions inside Test functions only fail when tests run — use package-level declarations.

**Imports:**
Only import packages you are actually using in the current file. Go will refuse to compile
if any import is unused. Add imports as you write code that needs them, not speculatively.

**Build and test commands:**
  go build ./...                     // build all packages
  go test -v -run TestName ./...     // run a specific test
  go test -v ./...                   // run all tests
  go mod init <modulename>           // initialize module (only if go.mod absent)
  go mod tidy                        // sync go.sum after adding imports
