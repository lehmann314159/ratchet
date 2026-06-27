package verbs

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
)

// injectMechanicalFindings parses the raw AUDIT model output, appends any
// mechanical structural violations the model missed, and re-serializes. If raw
// is not valid JSON or no mechanical findings exist, it is returned unchanged.
func injectMechanicalFindings(raw, folderPath string, beads []beadState) string {
	lang := guidance.Detect(folderPath)
	if lang == "" {
		return raw
	}

	var mechanical []AuditFinding
	switch lang {
	case "go":
		mechanical = goMechanicalBeadChecks(beads)
	}
	if len(mechanical) == 0 {
		return raw
	}

	var out AuditDecompositionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return raw // leave for Validate to reject
	}
	out.Findings = append(out.Findings, mechanical...)
	out.OverallVerdict = "issues_found"

	merged, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return string(merged)
}

// goMechanicalBeadChecks returns structural findings for Go projects that do
// not require model judgment:
//  1. Any bead with a "go test" exit criterion must own a *_test.go file.
//  2. The layout bead (index 0) must include api_check_test.go in output_files.
func goMechanicalBeadChecks(beads []beadState) []AuditFinding {
	var findings []AuditFinding
	for i, b := range beads {
		for _, criterion := range b.ExitCriteria {
			if strings.Contains(criterion, "go test") && !hasTestGoFile(b.OutputFiles) {
				findings = append(findings, AuditFinding{
					BeadTitle: b.Title,
					Issue: fmt.Sprintf(
						"exit criterion %q runs go test but output_files contains no *_test.go file — "+
							"the command exits 0 with \"no test files\" and verifies nothing (vacuous pass)",
						criterion),
					DesignDocReference: "N/A — structural",
				})
				break // one finding per bead for this rule
			}
		}
		if i == 0 && !hasNamedFile(b.OutputFiles, "api_check_test.go") {
			findings = append(findings, AuditFinding{
				BeadTitle: b.Title,
				Issue: "layout bead output_files does not include api_check_test.go; without compile-time " +
					"type assertions, wrong function signatures in stubs will pass go build ./... silently " +
					"and propagate into all subsequent beads",
				DesignDocReference: "N/A — structural",
			})
		}
	}
	return findings
}

// applyMechanicalBeadFixes corrects structural violations in a ParsedBead before
// it is written to the DB, so the problem never reaches AUDIT or RECONCILE.
// Returns true if any fix was applied (caller may want to log this).
func applyMechanicalBeadFixes(lang string, bead *ParsedBead) bool {
	if lang != "go" {
		return false
	}
	return goFixBeadSpec(bead)
}

// goFixBeadSpec fixes Go-specific structural violations in-place:
//
//   - If a bead has a "go test" exit criterion but no *_test.go in output_files,
//     those criteria are changed to "go build ./...". A bead that does not own a
//     test file cannot verify test results; the build check is the correct gate.
func goFixBeadSpec(bead *ParsedBead) bool {
	if hasTestGoFile(bead.OutputFiles) {
		return false // owns test file — no fix needed
	}
	fixed := false
	for i, c := range bead.ExitCriteria {
		if strings.Contains(c, "go test") {
			bead.ExitCriteria[i] = "go build ./..."
			fixed = true
		}
	}
	return fixed
}

func hasTestGoFile(files []string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			return true
		}
	}
	return false
}

func hasNamedFile(files []string, name string) bool {
	for _, f := range files {
		if filepath.Base(f) == name {
			return true
		}
	}
	return false
}
