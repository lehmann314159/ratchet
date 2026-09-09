// Package execcheck re-runs a bead's exit_criteria commands against real
// on-disk state, independent of anything a model reported. It is the shared,
// mechanical "did this actually pass" ground truth used wherever a decision
// would otherwise have to trust a model's narrative about its own success.
package execcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// VerifyExitCriteriaIsolated runs VerifyExitCriteria against a throwaway copy of
// folderPath rather than folderPath itself, so a criterion that produces a build
// artifact as a side effect — `go build .` drops `./<dirname>`, `go build -o app .`
// drops `./app` — cannot litter the real project folder. Used by the ADJUDICATE
// declare_success gate: that check is pure verification and must not mutate the
// tree it inspects (observed n=3: exprvm-web baseline-16, fractalviz-baseline-1,
// cron-studio run 2 all left a stray multi-MB binary in the live folder).
//
// If the copy cannot be made it falls back to an in-place VerifyExitCriteria — a
// correct verdict matters more than the hygiene this wrapper adds. The top-level
// traces/ subtree is excluded from the copy (large, and never referenced by an
// exit criterion).
func VerifyExitCriteriaIsolated(ctx context.Context, folderPath string, exitCriteria []string) (bool, string) {
	if len(exitCriteria) == 0 {
		return true, ""
	}
	tmp, err := os.MkdirTemp("", "ratchet-exitcheck-*")
	if err != nil {
		return VerifyExitCriteria(ctx, folderPath, exitCriteria)
	}
	defer os.RemoveAll(tmp)
	dst := filepath.Join(tmp, "folder")
	if err := copyTreeExcludingTop(folderPath, dst, map[string]bool{"traces": true}); err != nil {
		return VerifyExitCriteria(ctx, folderPath, exitCriteria)
	}
	return VerifyExitCriteria(ctx, dst, exitCriteria)
}

// copyTreeExcludingTop copies the directory tree at src to dst (which must not
// already exist), skipping any top-level entry whose name is in skipTop. Regular
// files only — symlinks, devices, and sockets are ignored.
func copyTreeExcludingTop(src, dst string, skipTop map[string]bool) error {
	return filepath.WalkDir(src, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if skipTop[strings.SplitN(rel, string(filepath.Separator), 2)[0]] {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if e.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !e.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

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