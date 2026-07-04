// Package orchestrator implements the persistent loop that drives the Ratchet
// pipeline: read state, identify the next job, dispatch to the appropriate
// verb handler, validate, commit.
//
// The orchestrator is the only process that writes to the DB (except for
// EXECUTE_BEAD and MONITOR_EXECUTION subprocesses writing their own columns
// in executions — see Step 3). Its state is fully reconstructable from
// SQLite on restart: load the active project, find the oldest non-complete
// job, resume from there.
package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
	"ratchet/internal/verbs"
)

const pollInterval = 2 * time.Second

// Run drives the orchestrator loop until ctx is cancelled.
func Run(ctx context.Context, d *db.DB, oc *ollama.Client) error {
	handlers := verbs.All(oc.BaseURL)

	owner := fmt.Sprintf("pid-%d", os.Getpid())
	if err := acquireLock(ctx, d, owner); err != nil {
		return err
	}
	go runHeartbeat(ctx, d, owner)
	defer releaseLock(context.Background(), d, owner)

	// Recover executions orphaned by a previous crash before resetting jobs,
	// so their EXECUTE_BEAD jobs are marked pending (not failed_retry).
	if err := recoverOrphanedExecutions(ctx, d); err != nil {
		return err
	}

	// Reset any jobs left in 'running' from a previous crash.
	if err := resetStaleRunning(ctx, d); err != nil {
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil // clean shutdown
		}

		if err := tick(ctx, d, oc, handlers); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// No active project — wait and retry.
				slog.Info("no active project, waiting", "retry_in", pollInterval)
			} else {
				slog.Error("tick error", "error", err)
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pollInterval):
		}
	}
}

// tick is one iteration of the orchestrator loop.
func tick(ctx context.Context, d *db.DB, oc *ollama.Client, handlers map[string]verbs.Handler) error {
	project, err := activeProject(ctx, d)
	if err != nil {
		return err
	}

	job, err := claimNextJob(ctx, d, project.ID)
	if err != nil {
		return err
	}
	if job == nil {
		// Nothing pending — project may be waiting on a subprocess (Step 3)
		// or be fully complete.
		slog.Debug("no pending jobs", "project_id", project.ID)
		return nil
	}

	return dispatch(ctx, d, oc, handlers, job)
}
