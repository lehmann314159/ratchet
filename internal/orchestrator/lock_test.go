package orchestrator

import (
	"context"
	"testing"
	"time"
)

func TestAcquireLock_RejectsFreshLock(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := acquireLock(ctx, d, "owner-a"); err != nil {
		t.Fatalf("acquireLock(owner-a): %v", err)
	}
	if err := acquireLock(ctx, d, "owner-b"); err == nil {
		t.Fatal("acquireLock(owner-b): expected an error — owner-a's lock is fresh")
	}
}

func TestAcquireLock_StealsStaleLock(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := acquireLock(ctx, d, "owner-a"); err != nil {
		t.Fatalf("acquireLock(owner-a): %v", err)
	}
	// Simulate owner-a's heartbeat having lapsed past lockStaleAfter (crashed).
	stale := time.Now().UTC().Add(-2 * lockStaleAfter).Format(time.RFC3339)
	if _, err := d.ExecContext(ctx,
		`UPDATE orchestrator_lock SET heartbeat_at = ? WHERE id = 1`, stale,
	); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	if err := acquireLock(ctx, d, "owner-b"); err != nil {
		t.Fatalf("acquireLock(owner-b): expected to steal the stale lock, got: %v", err)
	}

	var owner string
	if err := d.QueryRowContext(ctx, `SELECT owner FROM orchestrator_lock WHERE id = 1`).Scan(&owner); err != nil {
		t.Fatalf("query owner: %v", err)
	}
	if owner != "owner-b" {
		t.Errorf("expected owner-b to hold the lock, got %q", owner)
	}
}

// TestHeartbeatTick_ReportsLockLoss reproduces the Stage 7 audit finding: if
// this instance's heartbeat lapses past lockStaleAfter while the process is
// still alive (not crashed — e.g. a long scheduler stall), another instance
// can steal the lock via acquireLock. Before this fix, the original owner's
// heartbeat write silently affected zero rows forever, with nothing in the
// main loop noticing — both instances would keep dispatching jobs against
// the same DB. Verifies heartbeatTick now reports the loss so the caller
// (runHeartbeat / Run's main loop) can stop.
func TestHeartbeatTick_ReportsLockLoss(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := acquireLock(ctx, d, "owner-a"); err != nil {
		t.Fatalf("acquireLock(owner-a): %v", err)
	}

	held, err := heartbeatTick(ctx, d, "owner-a")
	if err != nil {
		t.Fatalf("heartbeatTick (still owner): %v", err)
	}
	if !held {
		t.Fatal("expected owner-a to still hold the lock")
	}

	// Steal the lock out from under owner-a, as another instance would after
	// owner-a's heartbeat lapses past lockStaleAfter.
	stale := time.Now().UTC().Add(-2 * lockStaleAfter).Format(time.RFC3339)
	if _, err := d.ExecContext(ctx,
		`UPDATE orchestrator_lock SET heartbeat_at = ? WHERE id = 1`, stale,
	); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}
	if err := acquireLock(ctx, d, "owner-b"); err != nil {
		t.Fatalf("acquireLock(owner-b): %v", err)
	}

	held, err = heartbeatTick(ctx, d, "owner-a")
	if err != nil {
		t.Fatalf("heartbeatTick (lock lost): %v", err)
	}
	if held {
		t.Error("expected heartbeatTick to report the lock as lost (bug reproduced: stolen lock went unnoticed)")
	}
}
