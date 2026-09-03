package qualify

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	_ "modernc.org/sqlite"
)

// requiredTestFuncRe mirrors verbs.requiredTestFuncRe.
var requiredTestFuncRe = regexp.MustCompile(`grep\s+-q\s+['"]func\s+(Test\w+)['"]`)

// BeadSpec is the slice of a bead's current-revision full_text the graders need.
type BeadSpec struct {
	Title        string   `json:"title"`
	FullText     string   `json:"full_text"`
	OutputFiles  []string `json:"output_files"`
	ExitCriteria []string `json:"exit_criteria"`
}

// ImplFiles returns the non-test .go output files.
func (b BeadSpec) ImplFiles() []string {
	var out []string
	for _, f := range b.OutputFiles {
		if strings.HasSuffix(f, ".go") && !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	return out
}

// TestFiles returns the _test.go output files, minus the mechanically-owned
// api-check file.
func (b BeadSpec) TestFiles() []string {
	var out []string
	for _, f := range b.OutputFiles {
		if strings.HasSuffix(f, "_test.go") && !strings.Contains(f, "api_check") {
			out = append(out, f)
		}
	}
	return out
}

// RequiredTestFuncs mirrors verbs.extractRequiredTestFuncs: every `func TestX`
// named in an exit-criterion grep.
func (b BeadSpec) RequiredTestFuncs() []string {
	seen := map[string]bool{}
	var out []string
	for _, ec := range b.ExitCriteria {
		for _, m := range requiredTestFuncRe.FindAllStringSubmatch(ec, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

// caseBeadSpec reads the bead's current-revision spec from a case's db.sqlite.
func caseBeadSpec(ctx context.Context, dbPath string, beadID int64) (BeadSpec, error) {
	d, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return BeadSpec{}, err
	}
	defer d.Close()
	var fullText string
	err = d.QueryRowContext(ctx, `
		SELECT br.full_text FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID).Scan(&fullText)
	if err != nil {
		return BeadSpec{}, fmt.Errorf("load bead %d spec: %w", beadID, err)
	}
	var s BeadSpec
	if err := json.Unmarshal([]byte(fullText), &s); err != nil {
		return BeadSpec{}, fmt.Errorf("parse bead %d full_text: %w", beadID, err)
	}
	return s, nil
}
