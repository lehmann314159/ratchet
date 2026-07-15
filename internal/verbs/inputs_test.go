package verbs

import (
	"context"
	"database/sql"
	"testing"
)

// --- currentLineage: pure-logic reproductions from the stage-5 audit ---

// TestCurrentLineageFiltersPostRewind reproduces the bug found auditing
// ADJUDICATE (2026-07-14): the original currentLineage inferred a rewind
// boundary from revision_number failing to exceed the running max, but every
// bead_revisions insert site numbers new revisions via a bead-wide
// MAX(revision_number)+1 to avoid collisions — so revision_number is always
// strictly increasing across the whole history, rewinds included, and the
// old heuristic could never fire. rewound_at (set by rewind-bead) fixes this.
func TestCurrentLineageFiltersPostRewind(t *testing.T) {
	all := []revisionEntry{
		{ID: 1, RevisionNumber: 1, CreatedAt: "2026-07-14T20:00:00Z"}, // DECOMPOSE_SPEC
		{ID: 2, RevisionNumber: 2, CreatedAt: "2026-07-14T20:10:00Z"}, // pre-rewind
		{ID: 3, RevisionNumber: 3, CreatedAt: "2026-07-14T20:20:00Z"}, // pre-rewind (triggered rewind-bead)
		// rewind-bead: repoints current_revision_id -> id 1, sets rewound_at,
		// inserts no row of its own.
		{ID: 4, RevisionNumber: 4, CreatedAt: "2026-07-14T20:40:00Z"}, // post-rewind (MAX+1 numbering)
	}
	got := currentLineage(all, sql.NullString{String: "2026-07-14T20:30:00Z", Valid: true})
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 4 {
		t.Fatalf("expected [rev1, rev4] after rewind, got %+v", got)
	}
}

// TestCurrentLineageNeverRewound confirms a bead with no rewind (rewound_at
// NULL) returns its full, unfiltered history.
func TestCurrentLineageNeverRewound(t *testing.T) {
	all := []revisionEntry{
		{ID: 1, RevisionNumber: 1, CreatedAt: "2026-07-14T20:00:00Z"},
		{ID: 2, RevisionNumber: 2, CreatedAt: "2026-07-14T20:10:00Z"},
	}
	got := currentLineage(all, sql.NullString{})
	if len(got) != 2 {
		t.Fatalf("never-rewound bead should return full history, got %+v", got)
	}
}

// TestLoadBeadRevisionLogFiltersPostRewind is the end-to-end version: seeds a
// real bead through rewind-bead's actual UPDATE pattern (rewound_at set,
// current_revision_id repointed to revision 1, no row inserted for the
// rewind itself) and confirms loadBeadRevisionLog — the function ADJUDICATE
// and COMPRESS_ANALYSIS actually call — filters out the pre-rewind revisions.
func TestLoadBeadRevisionLogFiltersPostRewind(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "lineage filter test")
	beadID, rev1ID := seedBead(t, d, -1, "bead under test")

	insertRev := func(revNum int, createdAt string) int64 {
		var maxRevNum int
		_ = d.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(revision_number), 0) FROM bead_revisions WHERE bead_id = ?`, beadID,
		).Scan(&maxRevNum)
		res, err := d.ExecContext(ctx, `
			INSERT INTO bead_revisions
			  (project_id, bead_id, revision_number, full_text,
			   execution_budget, monitor_override, created_by_verb, created_at)
			VALUES (-1, ?, ?, '{}', 300, 'honor', 'ADJUDICATE_NEXT_EXECUTION', ?)`,
			beadID, revNum, createdAt)
		if err != nil {
			t.Fatalf("insert revision %d: %v", revNum, err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	// Two pre-rewind ADJUDICATE revisions, same pattern as production
	// (bead-wide MAX(revision_number)+1).
	insertRev(2, "2026-07-14T20:10:00Z")
	insertRev(3, "2026-07-14T20:20:00Z")

	// rewind-bead's actual UPDATE statement (internal/project/rewind.go):
	// repoints current_revision_id to revision 1, sets rewound_at, inserts
	// no new row.
	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ?, rewound_at = ? WHERE id = ?`,
		rev1ID, "2026-07-14T20:30:00Z", beadID,
	); err != nil {
		t.Fatalf("simulate rewind: %v", err)
	}

	// Post-rewind ADJUDICATE revision, numbered via the same bead-wide
	// MAX(revision_number)+1 the production code uses — this is the row
	// that must survive the lineage filter.
	postRewindID := insertRev(4, "2026-07-14T20:40:00Z")

	log, err := loadBeadRevisionLog(ctx, d, beadID)
	if err != nil {
		t.Fatalf("loadBeadRevisionLog: %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("expected 2 entries (revision 1 + post-rewind revision), got %d: %+v", len(log), log)
	}
	if log[0].ID != rev1ID {
		t.Errorf("log[0].ID = %d, want revision 1 (%d)", log[0].ID, rev1ID)
	}
	if log[1].ID != postRewindID {
		t.Errorf("log[1].ID = %d, want post-rewind revision (%d)", log[1].ID, postRewindID)
	}
}
