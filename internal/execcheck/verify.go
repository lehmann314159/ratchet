// Package execcheck re-runs a bead's exit_criteria commands against real
// on-disk state, independent of anything a model reported. It is the shared,
// mechanical "did this actually pass" ground truth used wherever a decision
// would otherwise have to trust a model's narrative about its own success.
package execcheck

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// VerifyExitCriteria re-runs a bead's exit_criteria commands against the
// current on-disk state. This is the hard, non-model-overridable gate behind
// any "this bead is done" decision: a model's own interpretation of a trace
// can be wrong even when the mechanical findings already contain the correct
// signal.
//
// Real-world case this catches (checkers-v8, project 98, bead 627): the exit
// criterion's own literal run failed early in the attempt, but the model's
// own later self-check command (`grep ... && echo Pass || echo Fail`) always
// exits 0 regardless of the grep result — that shell construct cannot fail —
// and the analyzer misread the ambiguous "exit 0" as the criterion having
// passed, even though the literal criterion itself was never re-run to a
// passing state. Re-running it here removes all such ambiguity: no model
// narrative involved, matching the "mechanical, not model" philosophy behind
// forwardFileReferenceChecks and the AUDIT/RECONCILE convergence comparator.
func VerifyExitCriteria(ctx context.Context, folderPath string, exitCriteria []string) (bool, string) {
	for _, criterion := range exitCriteria {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		cmd := exec.CommandContext(cctx, "bash", "-c", criterion)
		cmd.Dir = folderPath
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			detail := fmt.Sprintf("exit criterion %q currently fails on disk (%v)", criterion, err)
			if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
				detail += ":\n" + trimmed
			}
			return false, detail
		}
	}
	return true, ""
}