You are working on a Go project. Apply these language-specific rules:

**Orient step (step 1 in your process):**
  Source files to read: all *.go files in the project root (not in subdirectories unless
  the spec places code there).
  Build command: go build ./...
  Stale file cleanup (step 2): overwrite a stray .go file with only its package line,
  e.g.: package fib

**do_not_use_this_test.go is generated and read-only:**
This file already exists on disk before you start. It was generated from the SURVEY_SPEC
manifest and contains one `_ = Symbol` assertion per exported function/variable/constant,
e.g. `_ = Fib`. Never write to it, edit it, or add assertions to it yourself — it is
mechanically regenerated, and any change you make will be discarded or cause a mismatch.

If the build fails because of an assertion in do_not_use_this_test.go, the problem is your
function's signature, not the file: match the exported name and signature SURVEY_SPEC
declared. Never modify do_not_use_this_test.go to make the error go away.

**Imports:**
Only import packages you are actually using in the current file. Go will refuse to compile
if any import is unused. Add imports as you write code that needs them, not speculatively.

**Build and test commands:**
  go build ./...                     // build all packages
  go test -v -run TestName ./...     // run a specific test
  go test -v ./...                   // run all tests
  go mod init <modulename>           // initialize module (only if go.mod absent)
  go mod tidy                        // sync go.sum after adding imports
