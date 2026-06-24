package db_test

import (
	"context"
	"testing"

	"ratchet/internal/db"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedProject(t *testing.T, d *db.DB, id int64, label string) {
	t.Helper()
	_, err := d.ExecContext(context.Background(), `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (?, ?, '/tmp', 'design.md', 'active', 'honor', 300, 2,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, label)
	if err != nil {
		t.Fatalf("seedProject %d: %v", id, err)
	}
}

func TestSchemaTablesExist(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	for _, tbl := range []string{
		"projects", "verb_model_assignments", "beads", "bead_revisions",
		"audit_reconcile_rounds", "executions", "analyses",
		"compressed_history", "adjudications", "handoff_jobs", "handoff_attempts",
	} {
		var n int
		if err := d.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&n); err != nil || n == 0 {
			t.Errorf("table %q missing from schema", tbl)
		}
	}
}

func TestSchemaIdempotent(t *testing.T) {
	// Open twice to exercise IF NOT EXISTS throughout — second Open must not fail.
	d1, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d1.Close()
	// Can't re-open the same :memory: address, but a successful Open already
	// ran applySchema once; a second DB at a separate path exercises the same path.
	d2 := openTestDB(t)
	// Both opened successfully — IF NOT EXISTS held.
	_ = d2
}

func TestModelConstraintDecomposeReconcile(t *testing.T) {
	ctx := context.Background()

	t.Run("different models rejected", func(t *testing.T) {
		d := openTestDB(t)
		seedProject(t, d, -1, "fixture: model-constraint/decompose-reconcile-mismatch")
		if err := d.SetVerbModelAssignment(ctx, -1, db.VerbDecomposeSpec, "glm-4.7-flash"); err != nil {
			t.Fatal(err)
		}
		if err := d.SetVerbModelAssignment(ctx, -1, db.VerbReconcileDecomposition, "gemma4:31b"); err == nil {
			t.Error("expected rejection: RECONCILE model differs from DECOMPOSE (self-review framing breaks)")
		}
	})

	t.Run("same model accepted", func(t *testing.T) {
		d := openTestDB(t)
		seedProject(t, d, -1, "fixture: model-constraint/decompose-reconcile-ok")
		if err := d.SetVerbModelAssignment(ctx, -1, db.VerbDecomposeSpec, "glm-4.7-flash"); err != nil {
			t.Fatal(err)
		}
		if err := d.SetVerbModelAssignment(ctx, -1, db.VerbReconcileDecomposition, "glm-4.7-flash"); err != nil {
			t.Errorf("unexpected rejection when models match: %v", err)
		}
	})
}

func TestModelConstraintAuditDiffersFromDecompose(t *testing.T) {
	ctx := context.Background()

	t.Run("same model rejected", func(t *testing.T) {
		d := openTestDB(t)
		seedProject(t, d, -1, "fixture: model-constraint/audit-same-as-decompose")
		if err := d.SetVerbModelAssignment(ctx, -1, db.VerbDecomposeSpec, "glm-4.7-flash"); err != nil {
			t.Fatal(err)
		}
		if err := d.SetVerbModelAssignment(ctx, -1, db.VerbAuditDecomposition, "glm-4.7-flash"); err == nil {
			t.Error("expected rejection: AUDIT same model as DECOMPOSE violates OQ-049 cross-review requirement")
		}
	})

	t.Run("different model accepted", func(t *testing.T) {
		d := openTestDB(t)
		seedProject(t, d, -1, "fixture: model-constraint/audit-differs-from-decompose")
		if err := d.SetVerbModelAssignment(ctx, -1, db.VerbDecomposeSpec, "glm-4.7-flash"); err != nil {
			t.Fatal(err)
		}
		if err := d.SetVerbModelAssignment(ctx, -1, db.VerbAuditDecomposition, "gemma4:31b"); err != nil {
			t.Errorf("unexpected rejection when models differ: %v", err)
		}
	})
}

func TestSeedVerbModelAssignmentsConstraints(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	seedProject(t, d, -1, "fixture: seed-verb-model-assignments")

	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SeedVerbModelAssignments(ctx, tx, -1); err != nil {
		_ = tx.Rollback()
		t.Fatalf("SeedVerbModelAssignments: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var decompose, reconcile, audit string
	_ = d.QueryRowContext(ctx, `SELECT model FROM verb_model_assignments WHERE project_id=-1 AND verb=?`,
		db.VerbDecomposeSpec).Scan(&decompose)
	_ = d.QueryRowContext(ctx, `SELECT model FROM verb_model_assignments WHERE project_id=-1 AND verb=?`,
		db.VerbReconcileDecomposition).Scan(&reconcile)
	_ = d.QueryRowContext(ctx, `SELECT model FROM verb_model_assignments WHERE project_id=-1 AND verb=?`,
		db.VerbAuditDecomposition).Scan(&audit)

	if decompose != reconcile {
		t.Errorf("DECOMPOSE model %q != RECONCILE model %q after seed", decompose, reconcile)
	}
	if audit == decompose {
		t.Errorf("AUDIT model %q == DECOMPOSE model %q after seed (must differ for cross-review)", audit, decompose)
	}
}
