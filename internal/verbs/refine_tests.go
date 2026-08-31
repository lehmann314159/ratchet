package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
	"ratchet/internal/splice"
)

// requiredTestFuncRe matches grep -q 'func TestXxx' patterns (single or double quotes)
// in exit criteria strings to extract required test function names.
var requiredTestFuncRe = regexp.MustCompile(`grep\s+-q\s+['"]func\s+(Test\w+)['"]`)

// extractRequiredTestFuncs returns the unique ordered list of Test* function names
// that the exit criteria require to be present in the test file.
func extractRequiredTestFuncs(exitCriteria []string) []string {
	seen := map[string]bool{}
	var names []string
	for _, c := range exitCriteria {
		for _, m := range requiredTestFuncRe.FindAllStringSubmatch(c, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				names = append(names, m[1])
			}
		}
	}
	return names
}

// missingRequiredFuncs returns which names from required are absent from testPath.
// If the file cannot be read or parsed, all names are returned as missing.
func missingRequiredFuncs(testPath string, required []string) []string {
	if len(required) == 0 {
		return nil
	}
	src, err := os.ReadFile(testPath)
	if err != nil {
		return required
	}
	present, err := splice.FuncMap(string(src))
	if err != nil {
		return required
	}
	var missing []string
	for _, name := range required {
		if _, ok := present[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

// refinementCycleCap is the maximum number of write-critique-judge cycles
// per bead before escalating to the user.
const refinementCycleCap = 5

// refinementWriteAttempts is the maximum number of chat rounds within a single
// REFINE_TESTS_WRITE call to fix compile errors before giving up. One more
// than the original 3-round compile-fix budget, to leave room for the
// mandatory run_go_snippet verification round (see its use in Run) without
// crowding out compile-error retries.
const refinementWriteAttempts = 4

// --- shared helpers ---

func loadRefineContext(ctx context.Context, d *db.DB, job *db.HandoffJob) (
	bead *beadState, project *db.Project, folderPath string,
	implContext string, testFilePaths []string, currentTestContent string, err error,
) {
	if !job.BeadID.Valid {
		return nil, nil, "", "", nil, "", fmt.Errorf("job %d has no bead_id", job.ID)
	}
	bead, err = loadBeadByID(ctx, d, job.BeadID.Int64)
	if err != nil {
		return
	}
	project, err = loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return
	}
	folderPath = project.FolderPath

	// Collect non-test .go files from prior beads for domain context.
	var implBuf strings.Builder
	if entries, rdErr := os.ReadDir(folderPath); rdErr == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if content, rerr := os.ReadFile(filepath.Join(folderPath, name)); rerr == nil {
				fmt.Fprintf(&implBuf, "### %s\n\n```go\n%s\n```\n\n", name, string(content))
			}
		}
	}
	implContext = implBuf.String()

	// Collect current test file content. apiCheckTestFilename is mechanically
	// owned by writeAPICheckTest (regenerated from the SURVEY_SPEC manifest,
	// see scaffold_go.go) — it holds compile-time `var _ = X` assertions only,
	// never hand-written behavioral tests, so REFINE_TESTS must not target it.
	var testBuf strings.Builder
	for _, f := range bead.OutputFiles {
		if !strings.HasSuffix(f, "_test.go") || filepath.Base(f) == apiCheckTestFilename {
			continue
		}
		testFilePaths = append(testFilePaths, f)
		content, rerr := os.ReadFile(filepath.Join(folderPath, f))
		if rerr != nil {
			continue
		}
		fmt.Fprintf(&testBuf, "### %s\n\n```go\n%s\n```\n\n", f, string(content))
	}
	currentTestContent = testBuf.String()
	return
}

func runCompile(ctx context.Context, folderPath string) (ok bool, output string) {
	cmd := exec.CommandContext(ctx, "go", "test", "-c", "-o", os.DevNull, ".")
	cmd.Dir = folderPath
	out, err := cmd.CombinedOutput()
	return err == nil, strings.TrimSpace(string(out))
}

// maxSnippetRuntime bounds a single run_go_snippet execution so a runaway or
// looping snippet can't stall REFINE_TESTS_CRITIQUE. A var, not a const, so
// tests can shrink it rather than waiting out the real timeout.
var maxSnippetRuntime = 10 * time.Second

// runGoSnippet compiles and runs a single self-contained Go source file
// (package main, stdlib imports only) in an isolated temp directory,
// returning its combined stdout+stderr (compile errors, panics, and normal
// output are all returned this way — they're the useful signal, not an
// exec-level failure). err is reserved for infrastructure problems (temp
// dir creation, writing the file) that have nothing to do with the
// snippet's own correctness.
//
// This exists so REFINE_TESTS_CRITIQUE, ADJUDICATE_NEXT_EXECUTION, and
// REFINE_TESTS_WRITE can each verify a specific claim about Go/stdlib
// runtime behavior by actually executing it, instead of only reasoning
// about it in text. Root cause of the 2026-07-20 exprvm-web-v1 incidents
// this closes: across separate real beads, both a CRITIQUE call and an
// ADJUDICATE call reasoned in prose about (a) whether html/template escapes
// '+' in rendered output, and (b) whether two fmt.Errorf-produced errors
// compare equal with ==, and were confidently wrong — an LLM predicting the
// shape of an answer is not the same as Go actually computing it.
func runGoSnippet(ctx context.Context, src string) (output string, err error) {
	dir, mkErr := os.MkdirTemp("", "ratchet-snippet-*")
	if mkErr != nil {
		return "", fmt.Errorf("create snippet dir: %w", mkErr)
	}
	defer os.RemoveAll(dir)

	// A minimal go.mod is required for `go run` to work at all outside an
	// existing module tree (the temp dir isn't nested under one) — stdlib-only
	// snippets need no dependencies, so this never touches the network.
	if werr := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module snippet\n\ngo 1.21\n"), 0o644); werr != nil {
		return "", fmt.Errorf("write snippet go.mod: %w", werr)
	}
	if werr := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); werr != nil {
		return "", fmt.Errorf("write snippet main.go: %w", werr)
	}

	runCtx, cancel := context.WithTimeout(ctx, maxSnippetRuntime)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "go", "run", "main.go")
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("snippet exceeded %s timeout", maxSnippetRuntime)
	}
	result := strings.TrimSpace(string(out))
	if runErr != nil && result == "" {
		result = runErr.Error()
	}
	return result, nil
}

func cycleID(job *db.HandoffJob) int64 {
	if job.RefinementCycleID.Valid {
		return job.RefinementCycleID.Int64
	}
	return 1
}

func insertRefinement(ctx context.Context, tx *sql.Tx, projectID, beadID, cycle int64,
	verb, summary, decision string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO test_refinements (project_id, bead_id, cycle_id, turn, verb, changed, summary, decision, created_at)
		VALUES (?, ?, ?, (SELECT COALESCE(MAX(turn),0)+1 FROM test_refinements WHERE bead_id=? AND cycle_id=?), ?, 0, ?, ?, ?)`,
		projectID, beadID, cycle, beadID, cycle, verb, summary, decision, now)
	return err
}

// --- REFINE_TESTS_WRITE ---

type RefineTestsWrite struct{}

func (h *RefineTestsWrite) Verb() string { return db.VerbRefineTestsWrite }

// writeFunctionTool is the sole tool available to REFINE_TESTS_WRITE.
// The model writes one test function per call; Ratchet assembles/splices.
var writeFunctionTool = ollama.Tool{
	Type: "function",
	Function: ollama.ToolFunction{
		Name:        "write_function",
		Description: "Submit a test function. Call once per function. Do not include package declarations or imports.",
		Parameters: ollama.ToolParameters{
			Type: "object",
			Properties: map[string]ollama.ToolProperty{
				"name": {Type: "string", Description: "Exact function name, must start with 'Test'"},
				"body": {Type: "string", Description: "Complete function from 'func TestXxx' through the closing '}'"},
			},
			Required: []string{"name", "body"},
		},
	},
}

func (h *RefineTestsWrite) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	bead, _, folderPath, implContext, testFilePaths, _, err := loadRefineContext(ctx, d, job)
	if err != nil {
		return "", err
	}
	if len(testFilePaths) == 0 {
		return "", fmt.Errorf("no test file paths for bead %d", job.BeadID.Int64)
	}

	model, err := loadVerbModel(ctx, d, job.ProjectID, h.Verb())
	if err != nil {
		return "", err
	}

	// Restore any missing implementation files as scaffold stubs so the compile
	// check succeeds. Handles the case where rewind-bead deleted impl files.
	if restoreErr := restoreMissingScaffolds(ctx, d, job.ProjectID, folderPath, bead.OutputFiles); restoreErr != nil {
		return "", fmt.Errorf("restore missing scaffolds: %w", restoreErr)
	}

	cid := cycleID(job)
	beadID := job.BeadID.Int64
	testPath := filepath.Join(folderPath, testFilePaths[0])
	requiredFuncs := extractRequiredTestFuncs(bead.ExitCriteria)

	// Save original file content before any writes (needed for verified-function lock).
	var originalSrc string
	if origBytes, rerr := os.ReadFile(testPath); rerr == nil {
		originalSrc = string(origBytes)
	}

	// Cycle 1: write all functions from scratch.
	// Cycle 2+: only rewrite functions flagged by the prior JUDGE.
	var allowedFuncs map[string]bool // nil = unrestricted (cycle 1)
	var userMsg string

	docExcerpt := loadDesignDocExcerptForBead(ctx, d, job.ProjectID, bead)

	if cid == 1 {
		userMsg = buildFirstWriteMsg(bead, folderPath, implContext, docExcerpt)
	} else {
		judgeOut, jErr := loadPriorJudgeOutput(ctx, d, beadID, cid-1)
		if jErr != nil {
			return "", fmt.Errorf("load judge output for cycle %d: %w", cid-1, jErr)
		}
		allowedFuncs = make(map[string]bool, len(judgeOut.FunctionsToRewrite))
		for _, name := range judgeOut.FunctionsToRewrite {
			allowedFuncs[name] = true
		}

		// Extract the current bodies of broken functions from disk.
		brokenBodies := make(map[string]string)
		if originalSrc != "" {
			if fm, fmErr := splice.FuncMap(originalSrc); fmErr == nil {
				for name := range allowedFuncs {
					brokenBodies[name] = fm[name]
				}
			}
		}
		userMsg = buildRevisionWriteMsg(bead, folderPath, implContext, brokenBodies, string(judgeOut.Instructions), docExcerpt)
	}

	messages := []ollama.Message{
		{Role: "system", Content: refineTestsWriteSystemPrompt},
		{Role: "user", Content: userMsg},
	}

	// writtenFuncs collects accepted function bodies; funcOrder preserves
	// insertion order for cycle-1 assembly (Go maps are unordered).
	writtenFuncs := make(map[string]string)
	var funcOrder []string
	var summary string

	// requiredCases/coveredCases replace the old usedSnippet boolean: each
	// accepted write_function call records the t.Run subtests (or, absent
	// subtests, the bare function name) it needs verified; each tagged
	// run_go_snippet call marks one covered. Neither exit path below is
	// allowed to finish until every required case for every written function
	// is covered.
	//
	// Why per-case rather than a flat "called at least once": the 2026-07-20
	// exprvm-web-v1 bead 34 incident showed a one-call floor lets a single
	// easy verification anywhere satisfy the gate no matter how many
	// separate fixture claims a written function actually contains — e.g. a
	// TestHandlers function with 8 subtests, 3 of which hand-built a raw '+'
	// into an x-www-form-urlencoded request body (the stdlib decodes that as
	// a space, not a literal '+'), certified after one unrelated check.
	// Scaling the requirement to what's actually being written closes that
	// generally, without encoding anything about URL decoding, HTML
	// escaping, or any other specific bug — catching it here, before the
	// function is even submitted, is earlier and cheaper than catching it in
	// CRITIQUE.
	requiredCases := make(map[string][]string)
	coveredCases := map[string]bool{}
	turn := 0
	for {
		turn++
		totalReq := 0
		for _, cases := range requiredCases {
			totalReq += len(cases)
		}
		maxTurns := refinementWriteAttempts
		if totalReq+2 > maxTurns {
			maxTurns = totalReq + 2
		}
		if turn > maxTurns {
			break
		}

		// NOT schema-mode: REFINE_TESTS_WRITE's real output is the write_function
		// tool calls, not a structured field. A reasoning-first schema on every
		// turn let the model emit its plan as `reasoning` + a summary claiming
		// completion and never call write_function — the completeness gate then
		// escalated on the missing test function (project 36 bead 203, 2026-08-31).
		// Tool-primary verbs stay on bare "json".
		msg, toolErr := oc.ChatWithTools(ctx, model, messages, []ollama.Tool{writeFunctionTool, runGoSnippetCaseTool}, nil, nil)
		if toolErr != nil {
			return "", toolErr
		}
		if strings.TrimSpace(msg.Content) != "" {
			summary = strings.TrimSpace(msg.Content)
		}
		if len(msg.ToolCalls) == 0 {
			missing := missingWriteCases(requiredCases, coveredCases)
			if len(missing) == 0 || turn == maxTurns {
				if len(missing) > 0 {
					slog.Warn("REFINE_TESTS_WRITE finalized with uncovered subtests",
						"bead_id", beadID, "turn", turn, "missing", missing)
				}
				break
			}
			messages = append(messages, ollama.Message{Role: "user", Content: "Before finishing, call " +
				"run_go_snippet — tagged with for_case — to verify a specific runtime-behavior assumption " +
				"behind each of these still-unverified subtests: " + strings.Join(missing, ", ") +
				" — even if you feel confident. Then continue."})
			continue
		}

		messages = append(messages, msg)
		for _, tc := range msg.ToolCalls {
			var result string
			switch tc.Function.Name {
			case "run_go_snippet":
				if src, _ := tc.Function.Arguments["source"].(string); strings.TrimSpace(src) == "" {
					result = "error: source is empty"
				} else if forCase, _ := tc.Function.Arguments["for_case"].(string); strings.TrimSpace(forCase) == "" {
					result = "error: for_case is empty — name the t.Run subtest (or Test* function) this verifies"
				} else if out, rerr := runGoSnippet(ctx, src); rerr != nil {
					result = fmt.Sprintf("error: %v", rerr)
				} else {
					coveredCases[strings.TrimSpace(forCase)] = true
					if out == "" {
						result = "(snippet ran with no output — add a print statement to see a result)"
					} else {
						result = out
					}
				}
			case "write_function":
				name, _ := tc.Function.Arguments["name"].(string)
				body, _ := tc.Function.Arguments["body"].(string)
				switch {
				case !strings.HasPrefix(name, "Test"):
					result = fmt.Sprintf("error: name %q must start with 'Test'", name)
				case cid > 1 && !allowedFuncs[name]:
					allowed := make([]string, 0, len(allowedFuncs))
					for k := range allowedFuncs {
						allowed = append(allowed, k)
					}
					result = fmt.Sprintf("error: %q is not in the list of functions to rewrite; allowed: %s", name, strings.Join(allowed, ", "))
				case !strings.HasPrefix(strings.TrimSpace(body), "func "):
					result = "error: body must begin with 'func '"
				default:
					if _, exists := writtenFuncs[name]; !exists {
						funcOrder = append(funcOrder, name)
					}
					writtenFuncs[name] = body
					if parsed := extractSubtestCases("package p\n\n" + body); parsed != nil {
						requiredCases[name] = parsed[name]
					} else {
						delete(requiredCases, name)
					}
					result = fmt.Sprintf("ok: accepted %s (%d bytes)", name, len(body))
					slog.Info("REFINE_TESTS_WRITE: function accepted", "name", name, "bytes", len(body))
				}
			default:
				result = fmt.Sprintf("error: unknown tool %q — only write_function and run_go_snippet are available", tc.Function.Name)
			}
			messages = append(messages, ollama.Message{Role: "tool", Content: result})
		}

		// Assemble or splice, write to disk, compile check.
		if len(writtenFuncs) == 0 {
			continue
		}
		var fileContent string
		if originalSrc == "" {
			// No prior content at this path (fresh file — the common cid==1
			// case) — assemble from scratch.
			funcs := make([]string, 0, len(funcOrder))
			for _, name := range funcOrder {
				funcs = append(funcs, writtenFuncs[name])
			}
			fileContent, _ = splice.Assemble(splice.DetectPackage(folderPath), funcs)
		} else {
			// The path already has content — whether from this bead's own
			// earlier cycle, or from a prior bead sharing this test file —
			// splice onto it rather than discarding it.
			fileContent = originalSrc
			for _, name := range funcOrder {
				fileContent, _ = splice.Replace(fileContent, name, writtenFuncs[name])
			}
		}
		if err := os.WriteFile(testPath, []byte(fileContent), 0o644); err != nil {
			return "", fmt.Errorf("write test file: %w", err)
		}

		ok, compileOut := runCompile(ctx, folderPath)
		if ok {
			// Completeness check: verify all required Test* functions are present.
			if missing := missingRequiredFuncs(testPath, requiredFuncs); len(missing) > 0 && turn < maxTurns {
				slog.Warn("REFINE_TESTS_WRITE: compile passed but required functions missing",
					"bead_id", beadID, "turn", turn, "missing", missing)
				messages = append(messages, ollama.Message{
					Role: "user",
					Content: "Compile passed, but the following required test functions are still missing: " +
						strings.Join(missing, ", ") +
						". Call write_function once for each missing function.",
				})
				continue
			}
			slog.Info("REFINE_TESTS_WRITE: compile passed", "bead_id", beadID, "turn", turn)
			if summary == "" {
				summary = "Test functions written and compiling."
			}
			missing := missingWriteCases(requiredCases, coveredCases)
			if len(missing) == 0 || turn == maxTurns {
				if len(missing) > 0 {
					slog.Warn("REFINE_TESTS_WRITE finalized with uncovered subtests",
						"bead_id", beadID, "turn", turn, "missing", missing)
				}
				break
			}
			messages = append(messages, ollama.Message{Role: "user", Content: "Before finishing, call " +
				"run_go_snippet — tagged with for_case — to verify a specific runtime-behavior assumption " +
				"behind each of these still-unverified subtests: " + strings.Join(missing, ", ") +
				" — even if you feel confident. Then continue."})
			continue
		}
		slog.Error("REFINE_TESTS_WRITE: compile failed", "bead_id", beadID, "turn", turn, "output", compileOut)
		if turn < maxTurns {
			messages = append(messages, ollama.Message{
				Role:    "user",
				Content: "Compile failed:\n```\n" + compileOut + "\n```\nFix the errors in the affected function(s). Call write_function again with the corrected body.",
			})
		}
	}

	// Verified-function lock: restore any verified functions WRITE changed.
	// This is a safety net — the tool constraint should prevent writes to
	// verified functions, but belt-and-suspenders is cheap here.
	if cid > 1 && originalSrc != "" {
		verifiedSet, _ := loadVerifiedFunctionSet(ctx, d, beadID)
		// JUDGE's rewrite decision takes precedence: if JUDGE flagged a function
		// for rewriting, it is not truly verified regardless of what CRITIQUE said.
		for name := range allowedFuncs {
			delete(verifiedSet, name)
		}
		if len(verifiedSet) > 0 {
			currentBytes, _ := os.ReadFile(testPath)
			currentSrc := string(currentBytes)
			origFuncs, _ := splice.FuncMap(originalSrc)
			restoredAny := false
			for name := range verifiedSet {
				if origBody, ok := origFuncs[name]; ok {
					restored, rErr := splice.Replace(currentSrc, name, origBody)
					if rErr == nil && restored != currentSrc {
						currentSrc = restored
						restoredAny = true
						slog.Warn("REFINE_TESTS_WRITE: restored verified function", "name", name)
					}
				}
			}
			if restoredAny {
				_ = os.WriteFile(testPath, []byte(currentSrc), 0o644)
			}
		}
	}

	if summary == "" {
		summary = "Test function write attempted."
	}
	out, _ := json.Marshal(RefineTestsWriteOutput{Summary: summary})
	return string(out), nil
}

// buildFirstWriteMsg builds the user message for WRITE on cycle 1 (no existing file).
func buildFirstWriteMsg(bead *beadState, folderPath, implContext, docExcerpt string) string {
	msg := "## Bead Specification\n\n" + bead.FullText
	if docExcerpt != "" {
		msg += "\n\n## Authoritative Design Document (excerpts)\n\n" + docExcerpt
	}
	if implContext != "" {
		msg += "\n\n## Implementation Files (prior beads — types and conventions)\n\n" + strings.TrimSpace(implContext)
	}
	msg += "\n\n## Task\n\nWrite test functions covering every behavior required by the spec above. " +
		"Call write_function once per test function. " +
		"Do not write package declarations, import statements, or helper functions — only Test* functions."
	if required := extractRequiredTestFuncs(bead.ExitCriteria); len(required) > 0 {
		msg += "\n\nYou MUST write ALL of the following functions (one write_function call each):\n"
		for _, name := range required {
			msg += fmt.Sprintf("- %s\n", name)
		}
	}
	return msg
}

// buildRevisionWriteMsg builds the user message for WRITE on cycle 2+ (rewriting broken functions).
func buildRevisionWriteMsg(bead *beadState, folderPath, implContext string, brokenBodies map[string]string, instructions, docExcerpt string) string {
	msg := "## Functions to Rewrite\n\n"
	for name, body := range brokenBodies {
		msg += fmt.Sprintf("### %s (current body)\n\n```go\n%s\n```\n\n", name, body)
	}
	msg += "## Fix Instructions\n\n" + instructions
	if docExcerpt != "" {
		msg += "\n\n## Reference: Authoritative Design Document (excerpts)\n\n" + docExcerpt
	}
	if implContext != "" {
		msg += "\n\n## Reference: Implementation Files\n\n" + strings.TrimSpace(implContext)
	}
	msg += "\n\n## Reference: Bead Specification\n\n" + bead.FullText
	msg += "\n\n## Task\n\nRewrite only the functions listed above, applying every fix instruction. " +
		"Call write_function exactly once per function listed. Do not write any other function."
	return msg
}

// loadPriorJudgeOutput fetches the validated JUDGE raw output for the given
// bead and cycle from handoff_attempts.
func loadPriorJudgeOutput(ctx context.Context, d *db.DB, beadID, cid int64) (*RefineTestsJudgeOutput, error) {
	var raw string
	err := d.QueryRowContext(ctx, `
		SELECT ha.raw_output FROM handoff_attempts ha
		JOIN handoff_jobs hj ON hj.id = ha.job_id
		WHERE hj.bead_id = ? AND hj.verb = ? AND hj.refinement_cycle_id = ?
		  AND ha.validation_result = 'valid'
		ORDER BY ha.id DESC LIMIT 1`,
		beadID, db.VerbRefineTestsJudge, cid,
	).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("query judge output bead=%d cycle=%d: %w", beadID, cid, err)
	}
	var out RefineTestsJudgeOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return nil, fmt.Errorf("parse judge output: %w", err)
	}
	return &out, nil
}

// loadVerifiedFunctionSet unions all verified_functions reported by CRITIQUE
// across all cycles for beadID.
func loadVerifiedFunctionSet(ctx context.Context, d *db.DB, beadID int64) (map[string]bool, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT ha.raw_output FROM handoff_attempts ha
		JOIN handoff_jobs hj ON hj.id = ha.job_id
		WHERE hj.bead_id = ? AND hj.verb = ? AND ha.validation_result = 'valid'
		ORDER BY ha.id ASC`,
		beadID, db.VerbRefineTestsCritique)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		var out RefineTestsCritiqueOutput
		if json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out) != nil {
			continue
		}
		for _, name := range out.VerifiedFunctions {
			result[name] = true
		}
	}
	return result, rows.Err()
}

func (h *RefineTestsWrite) Validate(rawOutput string) (string, any) {
	var out RefineTestsWriteOutput
	if err := json.Unmarshal([]byte(rawOutput), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if strings.TrimSpace(out.Summary) == "" {
		return "malformed: summary is empty", nil
	}
	return "valid", out
}

func (h *RefineTestsWrite) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(RefineTestsWriteOutput)
	beadID := job.BeadID.Int64
	cid := cycleID(job)
	now := time.Now().UTC().Format(time.RFC3339)

	var folderPath string
	if err := tx.QueryRowContext(ctx, `SELECT folder_path FROM projects WHERE id = ?`, job.ProjectID).Scan(&folderPath); err != nil {
		return fmt.Errorf("load folder_path: %w", err)
	}

	slog.Info("REFINE_TESTS_WRITE complete", "bead_id", beadID, "cycle_id", cid, "summary", out.Summary)

	if err := insertRefinement(ctx, tx, job.ProjectID, beadID, cid, h.Verb(), out.Summary, ""); err != nil {
		return fmt.Errorf("insert test_refinement: %w", err)
	}

	// Check compile state of what's now on disk.
	ok, compileOut := runCompile(ctx, folderPath)
	if !ok {
		slog.Error("REFINE_TESTS_WRITE: compile still failing after all attempts — escalating",
			"bead_id", beadID, "cycle_id", cid, "output", compileOut)
		_, err := tx.ExecContext(ctx,
			`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`, now, job.ID)
		return err
	}

	// Hard completeness gate: every test function the exit criteria require
	// (`grep -q 'func TestX'`) must actually be in the test file. The write
	// loop's in-turn nag is soft — it stops nagging on the last turn and
	// finalizes an incomplete file, which CRITIQUE/JUDGE then rubber-stamp and
	// EXECUTE thrashes on for ~1h before the grep guard fails. Escalate here
	// instead, with a specific message. (exprvm-web bead 144: TestHandlerRuntime.)
	var beadFullText string
	if err := tx.QueryRowContext(ctx, `
		SELECT br.full_text FROM beads b JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID).Scan(&beadFullText); err == nil {
		var spec ParsedBead
		if json.Unmarshal([]byte(beadFullText), &spec) == nil {
			required := extractRequiredTestFuncs(spec.ExitCriteria)
			var testPath string
			for _, f := range spec.OutputFiles {
				if strings.HasSuffix(f, "_test.go") && filepath.Base(f) != apiCheckTestFilename {
					testPath = filepath.Join(folderPath, f)
					break
				}
			}
			if testPath != "" {
				if missing := missingRequiredFuncs(testPath, required); len(missing) > 0 {
					slog.Error("REFINE_TESTS_WRITE: required test functions still missing after all attempts — escalating",
						"bead_id", beadID, "cycle_id", cid, "missing", missing)
					_, err := tx.ExecContext(ctx,
						`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`, now, job.ID)
					return err
				}
			}
		}
	}

	// Enqueue CRITIQUE for this cycle.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		job.ProjectID, db.VerbRefineTestsCritique, beadID, cid, now, now)
	return err
}

// --- REFINE_TESTS_CRITIQUE ---

// runGoSnippetTool is available to REFINE_TESTS_CRITIQUE so it can verify a
// specific, narrow claim about Go/stdlib runtime behavior by actually
// executing it (via runGoSnippet) instead of only reasoning about it in
// text. Not a general sandbox — the model doesn't need project files or
// state to check "does html/template escape '+'" or "does comparing these
// two errors with == succeed"; it only needs a stdlib-only, self-contained
// program.
var runGoSnippetTool = ollama.Tool{
	Type: "function",
	Function: ollama.ToolFunction{
		Name: "run_go_snippet",
		Description: "Compile and run a small, self-contained Go program to verify a specific factual claim " +
			"about Go or standard-library runtime behavior (e.g. does a given string survive a particular " +
			"escaping/formatting function unchanged, does a comparison evaluate as expected). Use this before " +
			"citing any such claim in a finding — do not guess or rely on general impressions of how Go " +
			"behaves. The program must be complete (package main, func main, stdlib imports only) and print " +
			"whatever result answers your question.",
		Parameters: ollama.ToolParameters{
			Type: "object",
			Properties: map[string]ollama.ToolProperty{
				"source": {Type: "string", Description: "Complete Go source: 'package main' through the final '}', stdlib imports only."},
			},
			Required: []string{"source"},
		},
	},
}

// runGoSnippetCaseTool is runGoSnippetTool plus a required for_case tag, used
// by REFINE_TESTS_CRITIQUE and REFINE_TESTS_WRITE so each call can be
// attributed to the specific t.Run subtest (or bare Test* function, if it has
// no subtests) it verifies a claim for. This closes a gap the flat "call it
// at least once" mandate left open: the 2026-07-20/21 exprvm-web-v1 bead 34
// incident showed REFINE_TESTS_CRITIQUE calling run_go_snippet exactly once,
// on some easy claim, then approving all 8 subtests of a single TestHandlers
// function as correct — including 3 that hand-built a raw '+' into an
// x-www-form-urlencoded request body, which the stdlib decodes as a space.
// One untargeted call anywhere satisfied the old boolean gate regardless of
// how many claims were actually being certified. Tagging each call and
// requiring coverage of every subtest belonging to a function before it can
// be certified closes that gap without hardcoding anything about URL
// encoding, escaping, or any other specific bug — it's a strengthening of the
// existing mandatory-verification mechanism, not a new domain-specific rule.
var runGoSnippetCaseTool = ollama.Tool{
	Type: "function",
	Function: ollama.ToolFunction{
		Name: "run_go_snippet",
		Description: "Compile and run a small, self-contained Go program to verify a specific factual claim " +
			"about Go or standard-library runtime behavior (e.g. does a given string survive a particular " +
			"escaping/formatting function unchanged, does a comparison evaluate as expected). Use this before " +
			"citing any such claim in a finding — do not guess or rely on general impressions of how Go " +
			"behaves. The program must be complete (package main, func main, stdlib imports only) and print " +
			"whatever result answers your question.",
		Parameters: ollama.ToolParameters{
			Type: "object",
			Properties: map[string]ollama.ToolProperty{
				"source":   {Type: "string", Description: "Complete Go source: 'package main' through the final '}', stdlib imports only."},
				"for_case": {Type: "string", Description: "The exact t.Run subtest name (e.g. HandleEval_Success) this check verifies a claim for — or the Test* function name itself, if it has no t.Run subtests."},
			},
			Required: []string{"source", "for_case"},
		},
	},
}

// extractSubtestCases parses src and returns, for each top-level Test*
// function, the list of its t.Run subtest names (string-literal names only —
// dynamic names aren't resolvable statically). A Test* function with no
// t.Run calls maps to its own name as the sole case, so per-case coverage
// degrades gracefully to per-function for simple tests. Returns nil if src
// doesn't parse (caller falls back to the pre-existing at-least-one-call
// floor in that case).
func extractSubtestCases(src string) map[string][]string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return nil
	}
	result := make(map[string][]string)
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || !strings.HasPrefix(fd.Name.Name, "Test") {
			continue
		}
		var cases []string
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Run" || len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if name, uerr := strconv.Unquote(lit.Value); uerr == nil {
				cases = append(cases, name)
			}
			return true
		})
		if len(cases) == 0 {
			cases = []string{fd.Name.Name}
		}
		result[fd.Name.Name] = cases
	}
	return result
}

// caseCovered reports whether required case name c was verified by any
// tagged run_go_snippet call, tolerating models that tag with the fuller
// "Test*/Subtest" form, or minor surrounding text, rather than the bare
// subtest name.
func caseCovered(covered map[string]bool, c string) bool {
	if covered[c] {
		return true
	}
	for tag := range covered {
		if strings.Contains(tag, c) || strings.Contains(c, tag) {
			return true
		}
	}
	return false
}

// snippetVerificationTurns bounds how many run_go_snippet round-trips a
// single REFINE_TESTS_CRITIQUE or ADJUDICATE_NEXT_EXECUTION call may use
// before it must give its final verdict/decision — enough for a handful of
// independent verifications without letting the call run indefinitely.
const snippetVerificationTurns = 6

type RefineTestsCritique struct{}

func (h *RefineTestsCritique) Verb() string { return db.VerbRefineTestsCritique }

func (h *RefineTestsCritique) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	bead, _, _, implContext, _, currentTestContent, err := loadRefineContext(ctx, d, job)
	if err != nil {
		return "", err
	}

	model, err := loadVerbModel(ctx, d, job.ProjectID, h.Verb())
	if err != nil {
		return "", err
	}

	userMsg := "## Bead Specification\n\n" + bead.FullText
	if excerpt := loadDesignDocExcerptForBead(ctx, d, job.ProjectID, bead); excerpt != "" {
		userMsg += "\n\n## Authoritative Design Document (excerpts)\n\n" + excerpt
	}
	if implContext != "" {
		userMsg += "\n\n## Implementation Files (prior beads — types and conventions)\n\n" +
			strings.TrimSpace(implContext)
	}
	userMsg += "\n\n## Current Test File\n\n" + strings.TrimSpace(currentTestContent)

	messages := []ollama.Message{
		{Role: "system", Content: refineTestsCritiqueSystemPrompt},
		{Role: "user", Content: userMsg},
	}

	// allCases is an upper bound (every subtest in the whole file) used only
	// to size the turn budget below; the actual gate per attempt is scoped to
	// whichever functions the model is about to certify as verified.
	allCases := extractSubtestCases(currentTestContent)
	totalCases := 0
	for _, cases := range allCases {
		totalCases += len(cases)
	}
	maxTurns := snippetVerificationTurns
	if totalCases+2 > maxTurns {
		maxTurns = totalCases + 2
	}

	// Tool loop: the model may call run_go_snippet to verify a runtime-
	// behavior claim before giving its final JSON verdict. Mirrors
	// RefineTestsWrite's turn loop below, with one addition: a final
	// (zero-tool-call) response is only accepted once every subtest belonging
	// to a function it's about to list in verified_functions has been
	// covered by a tagged run_go_snippet call.
	//
	// Why: "verify when you're not certain" (the original prompt wording)
	// only helps when the model perceives itself as uncertain. The 2026-07-20
	// exprvm-web-v1 bead 34 incident showed that failure — CRITIQUE stated,
	// as flat unhedged fact, that html/template does not escape '+', with no
	// sign the tool was ever invoked. Requiring at least one call, full stop,
	// fixed that specific case but left a subtler gap: a single untargeted
	// call anywhere satisfied the old boolean gate no matter how many
	// separate claims were being certified. The very next cycle, CRITIQUE
	// called run_go_snippet once and then approved all 8 subtests of a
	// single TestHandlers function as correct — including 3 that hand-built
	// a raw '+' into an x-www-form-urlencoded request body (the stdlib
	// decodes that as a space, not a literal '+'). Requiring coverage scaled
	// to what's actually being certified — one tagged call per subtest, not
	// per file — closes that gap generally, without encoding anything about
	// URL decoding, HTML escaping, or any other specific bug.
	var lastContent string
	coveredCases := map[string]bool{}
	for turn := 1; turn <= maxTurns; turn++ {
		// NOT schema-mode: this is the ChatWithTools path (run_go_snippet loop).
		// Reverted to bare "json" pending a deliberate re-approach to tool-loop
		// schema-mode (see docs/schema-mode-reasoning-field.md — WRITE showed the
		// tool loop is where schema-mode breaks).
		msg, toolErr := oc.ChatWithTools(ctx, model, messages, []ollama.Tool{runGoSnippetCaseTool}, nil, nil)
		if toolErr != nil {
			return "", toolErr
		}
		if strings.TrimSpace(msg.Content) != "" {
			lastContent = msg.Content
		}
		if len(msg.ToolCalls) == 0 {
			missing := missingVerificationCases(msg.Content, allCases, coveredCases)
			if len(missing) == 0 || turn == maxTurns {
				// Accept: either every certified function's subtests were
				// covered, or the turn budget is exhausted — fall back to
				// whatever it gave rather than fail the job outright. Logged
				// so an under-verified verdict is visible, not silent.
				if len(missing) > 0 {
					slog.Warn("REFINE_TESTS_CRITIQUE finalized with uncovered subtests",
						"bead_id", job.BeadID.Int64, "turn", turn, "missing", missing)
				}
				return msg.Content, nil
			}
			messages = append(messages, msg)
			messages = append(messages, ollama.Message{Role: "user", Content: "Before giving your final " +
				"verdict, call run_go_snippet — tagged with for_case — to verify a specific claim behind each " +
				"of these still-unverified subtests, since you're about to certify their enclosing function(s) " +
				"as correct: " + strings.Join(missing, ", ") + ". Confidence is not evidence; that gap is " +
				"exactly what this tool exists to close. Then give your final JSON verdict."})
			continue
		}

		messages = append(messages, msg)
		for _, tc := range msg.ToolCalls {
			var result string
			if tc.Function.Name != "run_go_snippet" {
				result = fmt.Sprintf("error: unknown tool %q — only run_go_snippet is available", tc.Function.Name)
			} else if src, _ := tc.Function.Arguments["source"].(string); strings.TrimSpace(src) == "" {
				result = "error: source is empty"
			} else if forCase, _ := tc.Function.Arguments["for_case"].(string); strings.TrimSpace(forCase) == "" {
				result = "error: for_case is empty — name the t.Run subtest (or Test* function) this verifies"
			} else if out, rerr := runGoSnippet(ctx, src); rerr != nil {
				result = fmt.Sprintf("error: %v", rerr)
			} else {
				coveredCases[strings.TrimSpace(forCase)] = true
				if out == "" {
					result = "(snippet ran with no output — add a print statement to see a result)"
				} else {
					result = out
				}
			}
			messages = append(messages, ollama.Message{Role: "tool", Content: result})
		}
	}
	// Turn cap reached without a final non-tool response — return whatever
	// content the model last produced (Validate will reject it as malformed
	// if it isn't valid JSON, triggering the normal retry path).
	return lastContent, nil
}

// missingVerificationCases parses content as a tentative RefineTestsCritiqueOutput
// and returns, for every function it lists in verified_functions, the
// subtest cases (from allCases) not yet present in covered. If content
// doesn't parse as valid JSON yet (the model may still be mid-thought before
// its real final answer), it falls back to requiring at least one covered
// case, matching this verb's pre-existing floor. If allCases itself is nil
// (the test file didn't parse), no per-case requirement can be computed and
// the same one-call floor applies.
func missingVerificationCases(content string, allCases map[string][]string, covered map[string]bool) []string {
	var out RefineTestsCritiqueOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(content)), &out); err != nil || allCases == nil {
		if len(covered) == 0 {
			return []string{"(at least one verified claim)"}
		}
		return nil
	}
	var missing []string
	for _, fn := range out.VerifiedFunctions {
		for _, c := range allCases[fn] {
			if !caseCovered(covered, c) {
				missing = append(missing, fn+"/"+c)
			}
		}
	}
	return missing
}

// missingWriteCases returns, for every function REFINE_TESTS_WRITE has
// written or revised, the subtest cases not yet covered by a tagged
// run_go_snippet call.
func missingWriteCases(required map[string][]string, covered map[string]bool) []string {
	var missing []string
	for fn, cases := range required {
		for _, c := range cases {
			if !caseCovered(covered, c) {
				missing = append(missing, fn+"/"+c)
			}
		}
	}
	sort.Strings(missing)
	return missing
}

func (h *RefineTestsCritique) Validate(rawOutput string) (string, any) {
	var out RefineTestsCritiqueOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(rawOutput)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if strings.TrimSpace(out.Summary) == "" {
		return "malformed: summary is empty", nil
	}
	return "valid", out
}

func (h *RefineTestsCritique) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(RefineTestsCritiqueOutput)
	beadID := job.BeadID.Int64
	cid := cycleID(job)
	now := time.Now().UTC().Format(time.RFC3339)

	slog.Info("REFINE_TESTS_CRITIQUE complete", "bead_id", beadID, "cycle_id", cid,
		"all_correct", out.AllCorrect, "findings", len(out.Findings), "summary", out.Summary)

	if err := insertRefinement(ctx, tx, job.ProjectID, beadID, cid, h.Verb(), out.Summary, ""); err != nil {
		return fmt.Errorf("insert test_refinement: %w", err)
	}

	// Enqueue JUDGE for this cycle.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		job.ProjectID, db.VerbRefineTestsJudge, beadID, cid, now, now)
	return err
}

// --- REFINE_TESTS_JUDGE ---

type RefineTestsJudge struct {
	// fromAdjudicateReRefine/adjudicateGuidance/currentTestContent cache
	// state from Run for Validate to enforce a mechanical override: see
	// their use in Validate for why this exists (2026-07-20 exprvm-web-v1
	// bead 33 incident).
	fromAdjudicateReRefine bool
	adjudicateGuidance     string
	currentTestContent     string
}

func (h *RefineTestsJudge) Verb() string { return db.VerbRefineTestsJudge }

func (h *RefineTestsJudge) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	bead, _, _, _, _, currentTestContent, err := loadRefineContext(ctx, d, job)
	if err != nil {
		return "", err
	}
	h.currentTestContent = currentTestContent

	model, err := loadVerbModel(ctx, d, job.ProjectID, h.Verb())
	if err != nil {
		return "", err
	}

	cid := cycleID(job)

	// Prefer the full structured JSON from the critique's raw output over the summary.
	var critiqueRaw string
	_ = d.QueryRowContext(ctx, `
		SELECT ha.raw_output FROM handoff_attempts ha
		JOIN handoff_jobs hj ON hj.id = ha.job_id
		WHERE hj.project_id = ? AND hj.verb = ? AND hj.bead_id = ? AND hj.refinement_cycle_id = ?
		  AND ha.validation_result = 'valid'
		ORDER BY ha.id DESC LIMIT 1`,
		job.ProjectID, db.VerbRefineTestsCritique, job.BeadID.Int64, cid,
	).Scan(&critiqueRaw)

	if critiqueRaw == "" {
		// Fallback: use the summary stored in test_refinements. Reaching this
		// branch means no real REFINE_TESTS_CRITIQUE job ran this cycle — the
		// only way that happens is ADJUDICATE_NEXT_EXECUTION's re_refine
		// decision injecting its own diagnosis directly (see its Commit,
		// "Inject ADJUDICATE's diagnosis as CRITIQUE findings"). That
		// diagnosis is not shaped like a real CRITIQUE's structured
		// findings/all_correct JSON — it's free-form, often hedged prose
		// ("if this is failing consistently... the expected value ... is
		// incorrect") — and JUDGE's prompt, written assuming CRITIQUE-shaped
		// input, is not reliably recognizing it as "genuine correctness
		// problems". Track this so Validate can enforce the already-made
		// upstream decision rather than let JUDGE silently rubber-stamp a
		// test ADJUDICATE has already diagnosed as broken.
		h.fromAdjudicateReRefine = true
		_ = d.QueryRowContext(ctx, `
			SELECT summary FROM test_refinements
			WHERE bead_id = ? AND verb = ? AND cycle_id = ?
			ORDER BY created_at DESC LIMIT 1`,
			job.BeadID.Int64, db.VerbRefineTestsCritique, cid,
		).Scan(&critiqueRaw)
	}
	h.adjudicateGuidance = critiqueRaw

	userMsg := "## Bead Specification\n\n" + bead.FullText
	if excerpt := loadDesignDocExcerptForBead(ctx, d, job.ProjectID, bead); excerpt != "" {
		userMsg += "\n\n## Authoritative Design Document (excerpts)\n\n" + excerpt
	}
	userMsg += "\n\n## Test File\n\n" + strings.TrimSpace(currentTestContent)
	userMsg += "\n\n## Critique Findings\n\n" + critiqueRaw

	messages := []ollama.Message{
		{Role: "system", Content: refineTestsJudgeSystemPrompt},
		{Role: "user", Content: userMsg},
	}

	// Mandatory run_go_snippet verification, same rationale and enforcement
	// shape as ADJUDICATE_NEXT_EXECUTION's (see its Run for the fuller
	// incident history this class of fix closes) — but JUDGE previously had
	// zero tool access at all and decided purely from prose, and unlike
	// ADJUDICATE's soft nudge-then-finalize-anyway pattern, this is a hard
	// requirement: never calling the tool is a Run failure (retried via the
	// normal strike mechanism), not a silently-accepted decision with only a
	// log warning. Confirmed live (tictactoe-v1 bead 81, 2026-08-28): with
	// no verification available, JUDGE confidently asserted "HTML escaping
	// of the apostrophe is not required in this context" — false, and a
	// direct reversal of ADJUDICATE's own earlier, correct, verified
	// diagnosis of the same test — sending the bead back into the exact
	// failure it had just been fixed out of. Every JUDGE decision rests on
	// some claim about what Go/the stdlib actually does with the test's
	// assertions; requiring verification unconditionally (not gated on
	// decision or on the model's own sense of whether this call needs it)
	// is what closes that gap, mirroring ADJUDICATE's own unconditional
	// (not decision-gated) rationale for the identical requirement.
	//
	// Confirmed live before wiring this in that format (a JSON Schema, not
	// the loose "json" string) coexists with tool calls the same way the
	// loose string already does — a tool-call-only turn still returns a
	// proper tool_calls entry with empty Content, not a schema-violation
	// error, so the mandatory-summary schema fix (refineTestsJudgeFormatSchema)
	// doesn't need to be dropped to add this.
	var lastContent string
	usedTool := false
	for turn := 1; turn <= snippetVerificationTurns; turn++ {
		// NOT reasoning-first schema-mode — the flat refineTestsJudgeFormatSchema
		// (field-presence only, predates this session) is the pre-schema-mode
		// state. Reverted pending a deliberate tool-loop re-approach.
		msg, toolErr := oc.ChatWithTools(ctx, model, messages, []ollama.Tool{runGoSnippetTool},
			&ollama.Options{Format: refineTestsJudgeFormatSchema}, nil)
		if toolErr != nil {
			return "", toolErr
		}
		if strings.TrimSpace(msg.Content) != "" {
			lastContent = msg.Content
		}
		if len(msg.ToolCalls) == 0 {
			if usedTool {
				return msg.Content, nil
			}
			if turn == snippetVerificationTurns {
				return "", fmt.Errorf("REFINE_TESTS_JUDGE finalized without ever calling run_go_snippet "+
					"to verify a runtime-behavior claim, after %d prompted turns", snippetVerificationTurns)
			}
			messages = append(messages, msg)
			messages = append(messages, ollama.Message{Role: "user", Content: "Before giving your final " +
				"decision, call run_go_snippet at least once to verify a specific claim about Go/stdlib " +
				"runtime behavior your reasoning depends on — even if you feel confident. Confidence is " +
				"not evidence; that gap is exactly what this tool exists to close. Then give your final " +
				"JSON decision."})
			continue
		}

		messages = append(messages, msg)
		for _, tc := range msg.ToolCalls {
			var result string
			if tc.Function.Name != "run_go_snippet" {
				result = fmt.Sprintf("error: unknown tool %q — only run_go_snippet is available", tc.Function.Name)
			} else if src, _ := tc.Function.Arguments["source"].(string); strings.TrimSpace(src) == "" {
				result = "error: source is empty"
			} else if out, rerr := runGoSnippet(ctx, src); rerr != nil {
				result = fmt.Sprintf("error: %v", rerr)
			} else if out == "" {
				result = "(snippet ran with no output — add a print statement to see a result)"
			} else {
				result = out
			}
			usedTool = true
			messages = append(messages, ollama.Message{Role: "tool", Content: result})
		}
	}
	return lastContent, nil
}

// refineTestsJudgeFormatSchema grammar-constrains REFINE_TESTS_JUDGE's output
// so "summary" is structurally unreachable-to-omit, mirroring Validate's own
// unconditional requirement below. Only "decision" and "summary" are marked
// required — "functions_to_rewrite"/"instructions" are genuinely conditional
// on decision=="revise", which a flat "required" can't express; Validate
// enforces that in code. Closes the gap that recurred live (connect-four-v2
// bead 56, 2026-08-27: 3/3 attempts omitted "summary" despite the prompt).
// This is field-presence enforcement only — NOT the reasoning-first schema-mode
// (that was tried in Phase 3 and reverted; see docs/schema-mode-reasoning-field.md).
var refineTestsJudgeFormatSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"decision":             map[string]any{"type": "string"},
		"functions_to_rewrite": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"instructions":         map[string]any{"type": "string"},
		"summary":              map[string]any{"type": "string"},
	},
	"required": []string{"decision", "summary"},
}

func (h *RefineTestsJudge) Validate(rawOutput string) (string, any) {
	var out RefineTestsJudgeOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(rawOutput)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if strings.TrimSpace(out.Summary) == "" {
		return "malformed: summary is empty", nil
	}
	if out.Decision != "approved" && out.Decision != "revise" {
		return fmt.Sprintf("malformed: decision must be 'approved' or 'revise', got %q", out.Decision), nil
	}
	if out.Decision == "revise" && strings.TrimSpace(string(out.Instructions)) == "" {
		return "malformed: decision is 'revise' but instructions is empty", nil
	}
	if out.Decision == "revise" && len(out.FunctionsToRewrite) == 0 {
		return "malformed: decision is 'revise' but functions_to_rewrite is empty", nil
	}

	// Mechanical override: this cycle's "critique findings" came from
	// ADJUDICATE_NEXT_EXECUTION's re_refine diagnosis, not a real
	// REFINE_TESTS_CRITIQUE call — that decision has already been made
	// upstream (ADJUDICATE only reaches re_refine on strong mechanical
	// evidence: byte-identical failure across genuinely different
	// implementations, or an explicit forced override — see
	// adjudicate_next_execution.go). JUDGE re-approving here isn't a
	// legitimate second opinion, it's silently discarding that diagnosis: the
	// test stays unchanged, and the bead is guaranteed to fail EXECUTE_BEAD
	// identically again. Force revise, targeting every top-level Test* func
	// in the current file (a general fallback since the diagnosis names
	// subtests, not top-level function names) with the diagnosis itself as
	// instructions.
	if h.fromAdjudicateReRefine && out.Decision == "approved" {
		funcs, ferr := splice.FuncMap(h.currentTestContent)
		var names []string
		if ferr == nil {
			for name := range funcs {
				if strings.HasPrefix(name, "Test") {
					names = append(names, name)
				}
			}
			sort.Strings(names)
		}
		if len(names) > 0 && strings.TrimSpace(h.adjudicateGuidance) != "" {
			slog.Warn("REFINE_TESTS_JUDGE decision mechanically overridden to revise",
				"model_decision", out.Decision, "forced_functions", names)
			out.Decision = "revise"
			out.FunctionsToRewrite = names
			out.Instructions = flexString("[Forced by the orchestrator — ADJUDICATE_NEXT_EXECUTION already " +
				"diagnosed this test via re_refine; approving it here would silently discard that diagnosis " +
				"and guarantee an identical failure on the next attempt.]\n\n" + h.adjudicateGuidance)
			out.Summary = "forced revision — ADJUDICATE's re_refine diagnosis was not acted on"
		}
	}
	return "valid", out
}

func (h *RefineTestsJudge) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(RefineTestsJudgeOutput)
	beadID := job.BeadID.Int64
	cid := cycleID(job)
	now := time.Now().UTC().Format(time.RFC3339)

	slog.Info("REFINE_TESTS_JUDGE complete", "bead_id", beadID, "cycle_id", cid,
		"decision", out.Decision, "summary", out.Summary)

	// Store instructions in summary so WRITE can retrieve them next cycle.
	summary := out.Summary
	if out.Decision == "revise" {
		summary = string(out.Instructions)
	}

	if err := insertRefinement(ctx, tx, job.ProjectID, beadID, cid, h.Verb(), summary, out.Decision); err != nil {
		return fmt.Errorf("insert test_refinement: %w", err)
	}

	if out.Decision == "approved" {
		slog.Info("REFINE_TESTS_JUDGE: approved — enqueuing EXECUTE_BEAD", "bead_id", beadID)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			job.ProjectID, db.VerbExecuteBead, beadID, now, now,
		); err != nil {
			return err
		}
		if pause, err := shouldPauseAfterVerb(ctx, tx, job.ProjectID, db.VerbRefineTestsJudge); err != nil {
			return err
		} else if pause {
			return pauseProject(ctx, tx, job.ProjectID, now)
		}
		return nil
	}

	// revise — check cycle cap.
	nextCycle := cid + 1
	if nextCycle > refinementCycleCap {
		slog.Error("ESCALATION — REFINE_TESTS: judge requested revision after cycle cap",
			"bead_id", beadID, "cycle_id", cid, "cap", refinementCycleCap)
		_, err := tx.ExecContext(ctx,
			`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`, now, job.ID)
		return err
	}

	slog.Info("REFINE_TESTS_JUDGE: requesting revision", "bead_id", beadID, "next_cycle", nextCycle)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		job.ProjectID, db.VerbRefineTestsWrite, beadID, nextCycle, now, now)
	return err
}
