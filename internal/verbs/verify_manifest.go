package verbs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

// scaffoldProject materializes manifest files for the given language. It is
// the only place that knows about language-specific file generation.
func scaffoldProject(language, folderPath string, manifest *SurveySpecOutput) error {
	switch language {
	case "go", "Go", "golang":
		return scaffoldGoProject(manifest.Package, manifest.Module, folderPath, manifest.Files)
	default:
		return fmt.Errorf("scaffoldProject: unsupported language %q", language)
	}
}

// VerifyManifest materializes the SURVEY_SPEC manifest to disk and runs five
// mechanical checks. It does not call Ollama — it is model-free.
type VerifyManifest struct {
	folderPath string
}

func (h *VerifyManifest) Verb() string { return db.VerbVerifyManifest }

func (h *VerifyManifest) Run(ctx context.Context, d *db.DB, _ *ollama.Client, job *db.HandoffJob) (string, error) {
	manifest, err := latestSurveyManifest(ctx, d, job.ProjectID)
	if err != nil {
		return "", fmt.Errorf("load survey manifest: %w", err)
	}

	project, err := loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	h.folderPath = project.FolderPath

	// Scaffold all project files from the manifest declarations.
	if err := scaffoldProject(project.Language, project.FolderPath, manifest); err != nil {
		return "", fmt.Errorf("scaffold project: %w", err)
	}

	var out VerifyManifestOutput
	var violations []string

	// Check 1: every source file listed in the manifest exists on disk.
	missing := checkFilePresence(project.FolderPath, manifest)
	out.FilePresencePass = len(missing) == 0
	for _, f := range missing {
		violations = append(violations, "file_presence: "+f+" missing after scaffolding")
	}

	// Check 2: no behavioral test files other than apiCheckTestFilename.
	badTests := checkNoBehavioralTests(project.FolderPath)
	out.NoBehavioralTestsPass = len(badTests) == 0
	for _, f := range badTests {
		violations = append(violations, "no_behavioral_tests: "+f+" is a behavioral test file")
	}

	// Check 3: go test -c -o /dev/null ./... exits 0.
	compileErr := verifyCompile(ctx, project.FolderPath)
	out.CompilePass = compileErr == ""
	if compileErr != "" {
		violations = append(violations, "compile: "+compileErr)
	}

	// Check 4: apiCheckTestFilename contains package-level var _ lines.
	apiErr := verifyAPICheck(project.FolderPath)
	out.APICheckPass = apiErr == ""
	if apiErr != "" {
		violations = append(violations, "api_check: "+apiErr)
	}

	// Check 5: stub bodies contain no control flow.
	purityViolations := checkStubPurity(project.FolderPath, manifest)
	out.StubPurityPass = len(purityViolations) == 0
	for _, v := range purityViolations {
		violations = append(violations, "stub_purity: "+v)
	}

	out.Violations = violations

	data, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal verify output: %w", err)
	}
	return string(data), nil
}

func (h *VerifyManifest) Validate(raw string) (string, any) {
	var out VerifyManifestOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	return "valid", out
}

func (h *VerifyManifest) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(VerifyManifestOutput)
	now := time.Now().UTC().Format(time.RFC3339)

	var attemptNumber int
	_ = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM verify_attempts WHERE project_id = ?`,
		job.ProjectID,
	).Scan(&attemptNumber)
	attemptNumber++

	violationsJSON, _ := json.Marshal(out.Violations)

	res, err := tx.ExecContext(ctx, `
		INSERT INTO verify_attempts (
			project_id, job_id, attempt_number,
			file_presence_pass, no_behavioral_tests_pass, compile_pass,
			api_check_pass, stub_purity_pass, violations, verifier_interpretation,
			created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ProjectID, job.ID, attemptNumber,
		boolToInt(out.FilePresencePass), boolToInt(out.NoBehavioralTestsPass),
		boolToInt(out.CompilePass), boolToInt(out.APICheckPass),
		boolToInt(out.StubPurityPass),
		string(violationsJSON), nullableStr(out.VerifierInterpretation),
		now,
	)
	if err != nil {
		return fmt.Errorf("insert verify_attempt: %w", err)
	}
	_ = res

	_, err = tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'pending', ?, ?)`,
		job.ProjectID, db.VerbCertifyManifest, now, now)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", db.VerbCertifyManifest, err)
	}
	if pause, err := shouldPauseAfterVerb(ctx, tx, job.ProjectID, db.VerbVerifyManifest); err != nil {
		return err
	} else if pause {
		return pauseProject(ctx, tx, job.ProjectID, now)
	}
	return nil
}


// checkFilePresence returns paths of manifest files absent from disk.
func checkFilePresence(folderPath string, manifest *SurveySpecOutput) []string {
	var missing []string
	for _, f := range manifest.Files {
		if _, err := os.Stat(filepath.Join(folderPath, f.Path)); err != nil {
			missing = append(missing, f.Path)
		}
	}
	return missing
}

// checkNoBehavioralTests returns paths of *_test.go files (other than
// apiCheckTestFilename) found anywhere in folderPath.
func checkNoBehavioralTests(folderPath string) []string {
	var bad []string
	_ = filepath.WalkDir(folderPath, func(path string, de os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if de.IsDir() {
			base := filepath.Base(path)
			if base == ".git" || base == "traces" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(folderPath, path)
		base := filepath.Base(rel)
		if strings.HasSuffix(base, "_test.go") && base != apiCheckTestFilename {
			bad = append(bad, rel)
		}
		return nil
	})
	return bad
}

// verifyCompile runs go test -c -o /dev/null ./... and returns the compiler
// output on failure, or "" on success.
func verifyCompile(ctx context.Context, folderPath string) string {
	tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(tctx, "go", "test", "-c", "-o", "/dev/null", "./...")
	cmd.Dir = folderPath
	out, err := cmd.CombinedOutput()
	if err == nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkStubPurity returns one violation string per function body (in a
// manifest source file) that contains control flow. Stub bodies must be an
// empty block or a bare return of zero values — no if/for/range/switch/
// select. Parse errors are skipped; the compile check already surfaces them.
func checkStubPurity(folderPath string, manifest *SurveySpecOutput) []string {
	var violations []string
	fset := token.NewFileSet()
	for _, f := range manifest.Files {
		if !strings.HasSuffix(f.Path, ".go") || filepath.Base(f.Path) == apiCheckTestFilename {
			continue
		}
		astFile, err := parser.ParseFile(fset, filepath.Join(folderPath, f.Path), nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range astFile.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if kind := findControlFlow(fd.Body); kind != "" {
				violations = append(violations, fmt.Sprintf("%s: %s contains %s (stub bodies must be a bare return or empty body only)", f.Path, fd.Name.Name, kind))
			}
		}
	}
	return violations
}

// findControlFlow returns a description of the first banned control-flow
// statement found in body, or "" if none is present.
func findControlFlow(body *ast.BlockStmt) string {
	var found string
	ast.Inspect(body, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		switch n.(type) {
		case *ast.IfStmt:
			found = "an if statement"
		case *ast.ForStmt:
			found = "a for statement"
		case *ast.RangeStmt:
			found = "a range loop"
		case *ast.SwitchStmt:
			found = "a switch statement"
		case *ast.TypeSwitchStmt:
			found = "a type switch statement"
		case *ast.SelectStmt:
			found = "a select statement"
		}
		return true
	})
	return found
}

// verifyAPICheck returns "" if apiCheckTestFilename contains at least one
// package-level var _ line, or an error string otherwise.
func verifyAPICheck(folderPath string) string {
	apiPath := filepath.Join(folderPath, apiCheckTestFilename)
	content, err := os.ReadFile(apiPath)
	if err != nil {
		return fmt.Sprintf("read %s: %v", apiCheckTestFilename, err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "_ =") || strings.HasPrefix(trimmed, "_ func") {
			return ""
		}
	}
	return apiCheckTestFilename + " contains no var _ assertions"
}


// extractGoDeclarations returns formatted Go source for all exported type/const
// blocks and all package-level var blocks across the manifest's .go files.
func extractGoDeclarations(folderPath string, manifest *SurveySpecOutput) (typesAndConsts, packageVars string) {
	fset := token.NewFileSet()
	var typesBuf, varsBuf bytes.Buffer

	for _, f := range manifest.Files {
		if !strings.HasSuffix(f.Path, ".go") || strings.HasSuffix(f.Path, "_test.go") {
			continue
		}
		astFile, err := parser.ParseFile(fset, filepath.Join(folderPath, f.Path), nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range astFile.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch gd.Tok {
			case token.TYPE, token.CONST:
				if err := format.Node(&typesBuf, fset, gd); err == nil {
					typesBuf.WriteString("\n\n")
				}
			case token.VAR:
				fmt.Fprintf(&varsBuf, "// from %s\n", f.Path)
				if err := format.Node(&varsBuf, fset, gd); err == nil {
					varsBuf.WriteString("\n\n")
				}
			}
		}
	}
	return strings.TrimSpace(typesBuf.String()), strings.TrimSpace(varsBuf.String())
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
