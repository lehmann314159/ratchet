package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"ratchet/internal/db"
)

const (
	lockHeartbeatInterval = 10 * time.Second
	lockStaleAfter        = 60 * time.Second
)

// acquireLock attempts to acquire the single-row orchestrator lock. If another
// instance holds a fresh lock (heartbeat within lockStaleAfter), it returns an
// error. A stale lock (crashed predecessor) is stolen unconditionally.
func acquireLock(ctx context.Context, d *db.DB, owner string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	staleThreshold := time.Now().UTC().Add(-lockStaleAfter).Format(time.RFC3339)

	result, err := d.ExecContext(ctx, `
		INSERT INTO orchestrator_lock (id, owner, acquired_at, heartbeat_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		    owner        = excluded.owner,
		    acquired_at  = excluded.acquired_at,
		    heartbeat_at = excluded.heartbeat_at
		WHERE heartbeat_at < ?`,
		owner, now, now, staleThreshold)
	if err != nil {
		return fmt.Errorf("acquire orchestrator lock: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		var currentOwner string
		_ = d.QueryRowContext(ctx,
			`SELECT owner FROM orchestrator_lock WHERE id = 1`).Scan(&currentOwner)
		return fmt.Errorf("orchestrator already running (owner: %s) — stop that instance first", currentOwner)
	}
	return nil
}

// releaseLock deletes the lock row if we still own it.
func releaseLock(ctx context.Context, d *db.DB, owner string) {
	_, _ = d.ExecContext(ctx,
		`DELETE FROM orchestrator_lock WHERE id = 1 AND owner = ?`, owner)
}

// runHeartbeat updates the lock's heartbeat_at every lockHeartbeatInterval
// until ctx is cancelled. Must run in a goroutine.
//
// If this instance's heartbeat lapses past lockStaleAfter (e.g. a long
// scheduler stall, not a crash), another orchestrator could steal the lock in
// acquireLock while this process is still alive and still dispatching jobs —
// two orchestrators writing to the same DB, violating the single-writer
// invariant the rest of this package assumes. The heartbeat UPDATE is
// owner-scoped, so once the lock is stolen it silently affects zero rows;
// lost records that so the main loop (see Run in orchestrator.go) can notice
// and stop instead of continuing to dispatch under a lock it no longer holds.
func runHeartbeat(ctx context.Context, d *db.DB, owner string, lost *atomic.Bool) {
	ticker := time.NewTicker(lockHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			held, err := heartbeatTick(ctx, d, owner)
			if err != nil {
				continue
			}
			if !held {
				slog.Error("orchestrator lock lost — another instance holds it; stopping dispatch", "owner", owner)
				lost.Store(true)
				return
			}
		}
	}
}

// heartbeatTick performs one heartbeat write, returning whether owner still
// holds the lock (false if another instance has stolen it, in which case the
// owner-scoped UPDATE affects zero rows). Factored out of runHeartbeat so the
// lock-loss detection can be tested directly instead of waiting a real
// lockHeartbeatInterval.
func heartbeatTick(ctx context.Context, d *db.DB, owner string) (held bool, err error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := d.ExecContext(ctx,
		`UPDATE orchestrator_lock SET heartbeat_at = ? WHERE id = 1 AND owner = ?`,
		now, owner)
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	return rows > 0, nil
}
