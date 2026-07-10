package splice

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FuncMap parses src and returns a map from function name to complete function
// source text (from the "func" keyword through the closing "}").
func FuncMap(src string) (map[string]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	result := make(map[string]string)
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		start := fset.Position(fd.Pos()).Offset
		end := fset.Position(fd.End()).Offset
		result[fd.Name.Name] = src[start:end]
	}
	return result, nil
}

// Replace replaces the named function in src with newFunc and returns the
// gofmt-formatted result. If the function is not found, newFunc is appended.
// newFunc must be a complete function declaration ("func Xxx(...) { ... }").
func Replace(src, name, newFunc string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	newFunc = strings.TrimSpace(newFunc)
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != name {
			continue
		}
		start := fset.Position(fd.Pos()).Offset
		end := fset.Position(fd.End()).Offset
		result := src[:start] + newFunc + src[end:]
		formatted, fmtErr := format.Source([]byte(result))
		if fmtErr != nil {
			return result, nil
		}
		return string(formatted), nil
	}
	// Not found: append.
	result := strings.TrimRight(src, "\n") + "\n\n" + newFunc + "\n"
	formatted, fmtErr := format.Source([]byte(result))
	if fmtErr != nil {
		return result, nil
	}
	return string(formatted), nil
}

// Assemble builds a complete Go test file from a package name and an ordered
// list of function body strings. Each entry must be a complete function
// declaration. Imports are detected automatically from the function bodies by
// scanning for pkg.Func selector expressions and mapping them to known import
// paths; "testing" is always included. The result is gofmt-formatted.
func Assemble(pkg string, funcs []string) (string, error) {
	// Build body section first so we can detect what packages are used.
	var body strings.Builder
	for _, f := range funcs {
		body.WriteString(strings.TrimSpace(f))
		body.WriteString("\n\n")
	}
	imports := detectImports("package " + pkg + "\n\n" + body.String())

	var sb strings.Builder
	sb.WriteString("package ")
	sb.WriteString(pkg)
	sb.WriteString("\n\nimport (\n")
	for _, imp := range imports {
		sb.WriteString("\t")
		sb.WriteString(fmt.Sprintf("%q", imp))
		sb.WriteString("\n")
	}
	sb.WriteString(")\n\n")
	sb.WriteString(body.String())

	formatted, err := format.Source([]byte(sb.String()))
	if err != nil {
		return sb.String(), fmt.Errorf("format: %w", err)
	}
	return string(formatted), nil
}

// detectImports parses src (a syntactically complete Go source fragment) and
// returns a sorted list of import paths for every pkg.Func selector expression
// found whose package name appears in the known-packages table. "testing" is
// always included.
func detectImports(src string) []string {
	// Well-known package name → import path.
	known := map[string]string{
		"atomic":   "sync/atomic",
		"bufio":    "bufio",
		"bytes":    "bytes",
		"context":  "context",
		"errors":   "errors",
		"filepath": "path/filepath",
		"fmt":      "fmt",
		"http":     "net/http",
		"httptest": "net/http/httptest",
		"io":       "io",
		"json":     "encoding/json",
		"log":      "log",
		"math":     "math",
		"os":       "os",
		"rand":     "math/rand",
		"regexp":   "regexp",
		"sort":     "sort",
		"strconv":  "strconv",
		"strings":  "strings",
		"sync":     "sync",
		"testing":  "testing",
		"time":     "time",
		"unicode":  "unicode",
		"url":      "net/url",
		"utf8":     "unicode/utf8",
	}

	used := map[string]bool{"testing": true}

	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "", src, parser.AllErrors)
	if f != nil {
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok {
				used[id.Name] = true
			}
			return true
		})
	}

	var imports []string
	for name, path := range known {
		if used[name] {
			imports = append(imports, path)
		}
	}
	sort.Strings(imports)
	return imports
}

// DetectPackage reads non-test .go files in dir and returns the package name
// declared in the first one found. Falls back to "main".
func DetectPackage(dir string) string {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "", content, parser.PackageClauseOnly)
		if err == nil && f.Name != nil {
			return f.Name.Name
		}
	}
	return "main"
}
