You are writing bead specifications for a Go project. Apply these rules for exit_criteria:

**Build and test commands:**
  go test ./...                      // run all tests (preferred form for exit_criteria)
  go test -run TestName .            // run specific test in current package
  go build ./...                     // build check only (use sparingly — prefer go test)

**Exit criteria:**
  Use `go test ./...` in exit_criteria (no `-v` — verbose is for interactive use only).

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

  Grep guard (vacuous-pass prevention): when a criterion targets a specific test function,
  prefix it with a grep check: `grep -q 'func TestFoo' foo_test.go && go test -run TestFoo .`
  The grep command MUST include the filename as the last argument — without it, grep reads
  stdin (empty in a subprocess shell) and always exits 1, blocking the entire criterion.
  For multiple functions:
    `grep -q 'func TestFoo' f_test.go && grep -q 'func TestBar' f_test.go && go test -run 'TestFoo|TestBar' .`