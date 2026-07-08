package splice

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
// declaration. Import "testing" is added automatically; the result is
// gofmt-formatted.
func Assemble(pkg string, funcs []string) (string, error) {
	var sb strings.Builder
	sb.WriteString("package ")
	sb.WriteString(pkg)
	sb.WriteString("\n\nimport \"testing\"\n\n")
	for _, body := range funcs {
		sb.WriteString(strings.TrimSpace(body))
		sb.WriteString("\n\n")
	}
	formatted, err := format.Source([]byte(sb.String()))
	if err != nil {
		return sb.String(), fmt.Errorf("format: %w", err)
	}
	return string(formatted), nil
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
