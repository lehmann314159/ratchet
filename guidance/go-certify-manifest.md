You are certifying a Go project manifest. Apply these language-specific rules:

**CERTIFY check details:**
  2. no_behavioral_tests: no *_test.go files other than api_check_test.go are present
  3. compile: go test -c -o /dev/null ./... exits 0 (imports, types, and stub signatures are valid)
  4. api_check: api_check_test.go was generated with at least one package-level exported symbol
     assertion (var _ = form at file scope — assertions inside test functions are insufficient)

**Compile-time assertions:**
The api_check_test.go file locks exported function signatures using package-level
blank-identifier assignments at file scope:

  var _ func(n int) (int, error) = Fib
  var _ func(src image.Image, msg string) (image.Image, error) = Encode

These must appear at file scope (not inside any Test function). Package-level declarations
fail the build immediately if a signature is wrong; assertions inside test functions only
fire when tests run.