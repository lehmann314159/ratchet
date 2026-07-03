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

**CERTIFY check details:**
  2. no_behavioral_tests: no *_test.go files other than api_check_test.go are present
  3. compile: go test -c -o /dev/null ./... exits 0 (imports, types, and stub signatures are valid)
  4. api_check: api_check_test.go was generated with at least one package-level exported symbol
     assertion (var _ = form at file scope — assertions inside test functions are insufficient)

**Exit criteria:**
  Use `go test ./...` in exit_criteria (no `-v` — the verbose flag is for interactive use,
  not criteria; pass/fail is the same either way).

  HTTP handler beads: `go build ./...` is not a sufficient exit criterion when output_files
  include HTTP handler or route files (handlers.go, routes.go, server.go). Build success
  cannot catch template render errors, missing FuncMap entries, or incorrect HTML structure.
  The exit criterion must use `net/http/httptest.NewServer` to start the handler on a randomly
  assigned free port, make HTTP requests, and assert structural properties of the responses
  (status code, element count, required attributes). Do not use a fixed port (e.g. :8080) —
  another process may already be bound to it.

  Template/styling beads: when the design doc describes visual styling (colors, layout, shaped
  elements), add `grep -q '<style>' <output_file>` as an early criterion — a build or test
  check cannot detect a missing CSS block.

  Test function naming: when a bead writes to a *_test.go file, its full_text must explicitly
  name the test functions to write (e.g. "Write TestEncode and TestDecode to codec_test.go").
  Without explicit names, an executor that writes only the implementation will see
  `go test -run TestEncode .` exit 0 with "no tests to run".

  -run flag syntax: the -run flag takes a Go regex. To run multiple test functions use the OR
  operator: `go test -run 'TestFoo|TestBar' .`. A space (`'TestFoo TestBar'`) matches nothing
  and silently selects zero tests.

  Grep guard (vacuous-pass prevention): when updating an exit criterion to guard against a
  vacuous pass, use: `grep -q 'func TestFoo' foo_test.go && go test -run TestFoo .`
  The grep command MUST include the filename as the last argument — without it, grep reads
  stdin (empty in a subprocess shell) and always exits 1, blocking the entire criterion.
  For multiple functions:
    `grep -q 'func TestFoo' f_test.go && grep -q 'func TestBar' f_test.go && go test -run 'TestFoo|TestBar' .`