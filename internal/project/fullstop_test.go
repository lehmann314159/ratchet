package project

import (
	"context"
	"testing"
)

// TestFullStopProject_ResetsExecutingBead reproduces the Stage 6 audit finding:
// fullStopProject only ever reset status='pending' beads to full_stopped,
// leaving a bead caught mid-'executing' (e.g. its EXECUTE_BEAD job was still
// running when the stop fired) stuck displaying 'executing' forever after —
// cosmetically wrong even though the project itself is correctly terminal and
// never dispatched again.
func TestFullStopProject_ResetsExecutingBead(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if _, err := d.ExecContext(ctx, `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   max_execution_attempts, created_at, updated_at)
		VALUES (1, 'fullstop-test', '/tmp/x', 'design.md', 'active', 'honor', 300, 5,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	if _, err := d.ExecContext(ctx,
		`INSERT INTO beads (id, project_id, status) VALUES
		 (1, 1, 'executing'), (2, 1, 'pending'), (3, 1, 'succeeded')`); err != nil {
		t.Fatalf("seed beads: %v", err)
	}

	_, beadsStopped, _, err := fullStopProject(ctx, d, 1)
	if err != nil {
		t.Fatalf("fullStopProject: %v", err)
	}
	if beadsStopped != 2 {
		t.Errorf("beadsStopped = %d, want 2 (the executing and pending beads)", beadsStopped)
	}

	rows, err := d.QueryContext(ctx, `SELECT id, status FROM beads WHERE project_id = 1 ORDER BY id`)
	if err != nil {
		t.Fatalf("query beads: %v", err)
	}
	defer rows.Close()

	want := map[int64]string{1: "full_stopped", 2: "full_stopped", 3: "succeeded"}
	got := map[int64]string{}
	for rows.Next() {
		var id int64
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = status
	}
	for id, wantStatus := range want {
		if got[id] != wantStatus {
			t.Errorf("bead %d status = %q, want %q", id, got[id], wantStatus)
		}
	}
}
