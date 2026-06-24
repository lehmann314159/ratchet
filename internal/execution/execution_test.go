package execution

import (
	"context"
	"database/sql"
	"testing"

	"ratchet/internal/db"
)

// --- parseDecision ---

func TestParseDecision(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"FIRE", "DECISION: FIRE\nREASON: same failure repeating three times", "FIRE"},
		{"NO_FIRE", "DECISION: NO_FIRE\nREASON: each attempt targets a different bug", "NO_FIRE"},
		{"extra whitespace", "DECISION:  FIRE \nREASON: loop detected", "FIRE"},
		{"FIRE after prose", "some preamble\nDECISION: FIRE\nREASON: yes", "FIRE"},
		{"malformed — no DECISION line", "I think this is a loop.", "NO_FIRE"},
		{"malformed — unknown value", "DECISION: MAYBE\nREASON: unsure", "NO_FIRE"},
		{"empty", "", "NO_FIRE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDecision(tc.input)
			if got != tc.want {
				t.Errorf("parseDecision(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// --- DB helpers shared across execution tests ---

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedProject(t *testing.T, d *db.DB, id int64) {
	t.Helper()
	_, err := d.ExecContext(context.Background(), `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (?, 'fixture: execution test', '/tmp', 'design.md',
		        'active', 'honor', 300, 2,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id)
	if err != nil {
		t.Fatalf("seedProject: %v", err)
	}
}

func seedBeadWithRevision(t *testing.T, d *db.DB, projectID int64, budget int) (beadID, revID int64) {
	t.Helper()
	ctx := context.Background()
	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status, current_revision_id) VALUES (?, 'executing', NULL)`, projectID)
	if err != nil {
		t.Fatalf("seedBead: %v", err)
	}
	beadID, _ = res.LastInsertId()
	res, err = d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text,
		   execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (?, ?, 1, '{"title":"B01","full_text":"spec"}', ?, 'honor',
		        'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		projectID, beadID, budget)
	if err != nil {
		t.Fatalf("seedRevision: %v", err)
	}
	revID, _ = res.LastInsertId()
	_, _ = d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID)
	return beadID, revID
}

func seedExecution(t *testing.T, d *db.DB, projectID, beadID, revID int64) int64 {
	t.Helper()
	res, err := d.ExecContext(context.Background(), `
		INSERT INTO executions
		  (project_id, bead_id, bead_revision_id, trace_path,
		   monitor_honored, started_at)
		VALUES (?, ?, ?, '/tmp/test-trace.log', 1, '2026-01-01T00:00:00Z')`,
		projectID, beadID, revID)
	if err != nil {
		t.Fatalf("seedExecution: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// --- writeMonitorFired ---

func TestWriteMonitorFired(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, -1)
	beadID, revID := seedBeadWithRevision(t, d, -1, 300)
	execID := seedExecution(t, d, -1, beadID, revID)

	t.Run("write fired=true", func(t *testing.T) {
		if err := writeMonitorFired(d, execID, true); err != nil {
			t.Fatalf("writeMonitorFired: %v", err)
		}
		var v sql.NullInt64
		_ = d.QueryRowContext(context.Background(),
			`SELECT monitor_fired FROM executions WHERE id = ?`, execID).Scan(&v)
		if !v.Valid || v.Int64 != 1 {
			t.Errorf("monitor_fired = %v, want 1", v)
		}
	})

	t.Run("write fired=false", func(t *testing.T) {
		if err := writeMonitorFired(d, execID, false); err != nil {
			t.Fatalf("writeMonitorFired: %v", err)
		}
		var v sql.NullInt64
		_ = d.QueryRowContext(context.Background(),
			`SELECT monitor_fired FROM executions WHERE id = ?`, execID).Scan(&v)
		if !v.Valid || v.Int64 != 0 {
			t.Errorf("monitor_fired = %v, want 0", v)
		}
	})
}

// --- writeTerminationCause ---

func TestWriteTerminationCause(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, -1)
	beadID, revID := seedBeadWithRevision(t, d, -1, 300)
	execID := seedExecution(t, d, -1, beadID, revID)

	cases := []string{"success", "timeout", "monitor_terminated", "monitor_force_killed"}
	for _, cause := range cases {
		t.Run(cause, func(t *testing.T) {
			if err := writeTerminationCause(d, execID, cause); err != nil {
				t.Fatalf("writeTerminationCause(%q): %v", cause, err)
			}
			var got string
			_ = d.QueryRowContext(context.Background(),
				`SELECT termination_cause FROM executions WHERE id = ?`, execID).Scan(&got)
			if got != cause {
				t.Errorf("termination_cause = %q, want %q", got, cause)
			}
		})
	}
}

// --- readMonitorFired ---

func TestReadMonitorFired(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, -1)
	beadID, revID := seedBeadWithRevision(t, d, -1, 300)
	execID := seedExecution(t, d, -1, beadID, revID)

	t.Run("NULL → not fired", func(t *testing.T) {
		fired, err := readMonitorFired(d, execID)
		if err != nil {
			t.Fatal(err)
		}
		if fired {
			t.Error("expected not-fired when monitor_fired is NULL")
		}
	})

	t.Run("0 → not fired", func(t *testing.T) {
		_ = writeMonitorFired(d, execID, false)
		fired, err := readMonitorFired(d, execID)
		if err != nil {
			t.Fatal(err)
		}
		if fired {
			t.Error("expected not-fired when monitor_fired=0")
		}
	})

	t.Run("1 → fired", func(t *testing.T) {
		_ = writeMonitorFired(d, execID, true)
		fired, err := readMonitorFired(d, execID)
		if err != nil {
			t.Fatal(err)
		}
		if !fired {
			t.Error("expected fired when monitor_fired=1")
		}
	})
}

// --- stubLine ---

func TestStubLine(t *testing.T) {
	t.Run("loop mode returns identical content each step", func(t *testing.T) {
		line1 := stubLine("loop", 1)
		line2 := stubLine("loop", 2)
		line3 := stubLine("loop", 10)
		// Timestamps differ, but the error description must be identical.
		// Strip timestamp prefix for comparison.
		trim := func(s string) string {
			idx := 0
			for i, c := range s {
				if c == ']' {
					idx = i + 2
					break
				}
			}
			return s[idx:]
		}
		if trim(line1) != trim(line2) || trim(line2) != trim(line3) {
			t.Errorf("loop mode lines differ:\n  %q\n  %q\n  %q", line1, line2, line3)
		}
	})

	t.Run("success mode returns distinct steps", func(t *testing.T) {
		line1 := stubLine("success", 1)
		line2 := stubLine("success", 2)
		if line1 == line2 {
			t.Error("success mode step 1 and step 2 are identical")
		}
	})
}
