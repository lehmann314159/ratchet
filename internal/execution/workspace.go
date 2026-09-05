package execution

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// execWorkspace is a per-attempt sandbox directory seeded from the live project
// folder. EXECUTE_BEAD's write_file / read_file / run_command tools and its
// in-loop exit-criteria check all operate here, never on the live folder, so a
// scratch program or a botched run_command cannot poison the shared tree that
// every downstream verb (ANALYZE_EXECUTION, the next bead's REFINE_TESTS, the
// exit-criteria compile) then trusts.
//
// Root cause this closes (exprvm-web-baseline-13 bead 319): EXECUTE wrote
// standalone `package main` repro programs into the project root while
// debugging; they persisted and broke every later `go test` with
// "main redeclared", and the execute->adjudicate retry loop had no mechanical
// lever to sweep them. See docs/execute-workspace-sandbox-plan.md and
// memory/project_execute_workspace_hygiene.
type execWorkspace struct {
	dir     string          // sandbox root (a temp dir)
	live    string          // the real project folder
	seedSet map[string]bool // relpaths present at seed time
}

// newExecWorkspace creates a temp dir and seeds it from liveFolder (minus the
// top-level traces/ subtree, matching resetWorkDir). The caller must call
// cleanup() when done.
func newExecWorkspace(liveFolder string) (*execWorkspace, error) {
	dir, err := os.MkdirTemp("", "ratchet-execbead-*")
	if err != nil {
		return nil, fmt.Errorf("create exec work dir: %w", err)
	}
	if err := resetWorkDir(liveFolder, dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("seed exec work dir from %s: %w", liveFolder, err)
	}
	ws := &execWorkspace{dir: dir, live: liveFolder, seedSet: map[string]bool{}}
	if err := filepath.WalkDir(dir, func(p string, e os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if e.IsDir() || !e.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		ws.seedSet[rel] = true
		return nil
	}); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("index exec work dir seed: %w", err)
	}
	return ws, nil
}

func (ws *execWorkspace) cleanup() {
	if ws == nil || ws.dir == "" {
		return
	}
	_ = os.RemoveAll(ws.dir)
}

// copyBack writes each writable output file that exists in the sandbox back to
// the live folder. It returns, sorted, the relpaths of every other file the
// model created or modified in the sandbox — those are NOT copied back
// (discarded), which is both the stray-file fix and output_files whitelist
// enforcement (an EXECUTE write to a locked test file or a sibling .go file
// never reaches the live tree).
func (ws *execWorkspace) copyBack(writable []string) (discarded []string, err error) {
	allow := make(map[string]bool, len(writable))
	for _, f := range writable {
		allow[filepath.Clean(f)] = true
	}

	for _, f := range writable {
		src := filepath.Join(ws.dir, filepath.Clean(f))
		st, statErr := os.Stat(src)
		if statErr != nil || st.IsDir() {
			continue // model didn't write this one this attempt
		}
		if cErr := copyFileTo(src, filepath.Join(ws.live, filepath.Clean(f))); cErr != nil {
			err = errors.Join(err, cErr)
		}
	}

	seen := map[string]bool{}
	_ = filepath.WalkDir(ws.dir, func(p string, e os.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if e.IsDir() {
			if rel, _ := filepath.Rel(ws.dir, p); rel == "traces" {
				return filepath.SkipDir
			}
			return nil
		}
		if !e.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(ws.dir, p)
		clean := filepath.Clean(rel)
		if allow[clean] {
			return nil
		}
		if !ws.seedSet[rel] {
			discarded = append(discarded, rel) // model created it
			seen[rel] = true
			return nil
		}
		if changed, _ := fileChanged(filepath.Join(ws.live, rel), p); changed {
			discarded = append(discarded, rel) // model modified a file it doesn't own
			seen[rel] = true
		}
		return nil
	})

	sort.Strings(discarded)
	return discarded, err
}

// fileChanged reports whether the bytes at a and b differ. A read error on
// either side is treated as "changed" so the file is reported rather than
// silently trusted.
func fileChanged(a, b string) (bool, error) {
	ba, ea := os.ReadFile(a)
	if ea != nil {
		return true, ea
	}
	bb, eb := os.ReadFile(b)
	if eb != nil {
		return true, eb
	}
	return !bytes.Equal(ba, bb), nil
}

// copyFileTo copies src to dst, creating parent dirs, with 0o644 perms
// (matching write_file's toolWriteFile).
func copyFileTo(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// discardedFilesTraceLine formats the one-line trace record consumed by
// ANALYZE_EXECUTION's checkDiscardedWorkspaceFiles. Empty when nothing was
// discarded.
func discardedFilesTraceLine(discarded []string) string {
	if len(discarded) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"[workspace] discarded %d file(s) written/modified outside output_files (not copied to project): %s",
		len(discarded), strings.Join(discarded, ", "),
	)
}
