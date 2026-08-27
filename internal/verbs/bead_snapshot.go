package verbs

import (
	"fmt"
	"os"
	"path/filepath"
)

// SnapshotBeadFiles copies every file in outputFiles from folderPath into a
// fresh traces/_bead-{id}-{kind}-{n}/ directory, preserving relative paths,
// before the caller deletes test files or stubs impl files. kind labels why
// the reset happened ("rewind" for project.rewindBead; "cascade" for
// enqueueCascadeReview's diff-driven bead reset, cascade_review.go) so the
// forensic trail under traces/ is honest about which mechanism destroyed the
// pre-reset content. Missing files (never written, or already stubbed by a
// prior reset) are skipped, not an error. n increments per bead+kind so
// repeated resets of the same bead don't overwrite each other's snapshots.
// Returns the snapshot dir and the list of files actually preserved; the
// caller writes the dir's manifest once the rest of the reset's outcome is
// known.
//
// The leading underscore is load-bearing, not cosmetic: `go/build` (and
// therefore `go test ./...`) skips any directory starting with '_' or '.',
// or named "testdata". Without it, a snapshot containing a copy of the
// bead's pre-reset .go files (test file included) has no go.mod of its own
// and is a perfectly ordinary-looking subpackage to the Go toolchain —
// `go test ./...` silently picks it up and runs its tests too, doubling
// (and, across repeated rewinds, further multiplying) the output size of
// every exit-criterion check that uses `go test ./...`, since it now
// matches both the real package and every snapshot. Confirmed as the actual
// root cause of a real ADJUDICATE_NEXT_EXECUTION escalation
// (connect-four-v1, bead 47, 2026-08-27): `go list ./...` from the project
// root returned both "connectfour" and "connectfour/traces/bead-47-rewind-1"
// as separate packages, and the mechanical findings fed into ADJUDICATE
// showed the same test's output appearing twice — that duplication was
// large enough to push the prompt past the model's available context,
// producing a completely empty response (done_reason "length") on every one
// of the verb's 6 tool-loop turns.
//
// Lives in internal/verbs (not internal/project, where rewindBead itself
// lives) because internal/project already imports internal/verbs for
// ParsedBead/WriteScaffoldStubs/ApplyMechanicalBeadFixes — an import the
// other direction would cycle, and this logic has no DB dependency of its
// own, so verbs is the natural shared home both callers can reach.
func SnapshotBeadFiles(folderPath, kind string, beadID int64, outputFiles []string) (dir string, preserved []string, err error) {
	tracesDir := filepath.Join(folderPath, "traces")
	if err := os.MkdirAll(tracesDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create traces dir: %w", err)
	}

	n := 1
	for {
		if _, err := os.Stat(filepath.Join(tracesDir, fmt.Sprintf("_bead-%d-%s-%d", beadID, kind, n))); os.IsNotExist(err) {
			break
		}
		n++
	}
	snapshotDir := filepath.Join(tracesDir, fmt.Sprintf("_bead-%d-%s-%d", beadID, kind, n))
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create snapshot dir: %w", err)
	}

	var copied []string
	for _, rel := range outputFiles {
		data, err := os.ReadFile(filepath.Join(folderPath, rel))
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return "", nil, fmt.Errorf("read %s: %w", rel, err)
		}
		dst := filepath.Join(snapshotDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", nil, fmt.Errorf("create snapshot dir for %s: %w", rel, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return "", nil, fmt.Errorf("write snapshot of %s: %w", rel, err)
		}
		copied = append(copied, rel)
	}

	return snapshotDir, copied, nil
}
