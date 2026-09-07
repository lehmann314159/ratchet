package execution

import (
	"sync/atomic"
	"time"
)

// Stall-detection tunables for one EXECUTE_BEAD attempt. See
// docs/execute-progress-detection-plan.md,
// docs/execute-checkpoint-decouple-plan.md, and
// memory/project_execute_progress_detection for the rationale (baseline-14 bead
// 318: the model's only stop condition was the wall-clock budget, and ADJUDICATE
// mechanically doubled it on every timeout while retrying the identical spec —
// nothing detected "same spec, same wall, no measurable progress").
//
// All fixed durations — they are NOT derived from the bead's execution_budget.
// A large budget (from a pre-decouple fixture, or ADJUDICATE's old
// timeout-doubling) used to push the first checkpoint out to 60 min and keep the
// whole mechanism from firing (baseline-15). execution_budget no longer affects
// EXECUTE_BEAD timing at all.
const (
	// execStallWindow: no productive turn (a write_file leaving an in-scope
	// output file at a content hash it has not held before this attempt) for
	// this long, with at least one non-productive turn on record, is a stall.
	// 15m comfortably exceeds one full non-terminating muse-glimmer think turn
	// (~16 384 tokens ≈ 30 min at ~9 tok/s would actually be longer — the
	// window is measured from the last *productive* write, so a single long
	// think that never writes still trips it after 15m). This per-turn check is
	// the backstop for a stall that develops between checkpoint boundaries;
	// execCheckpointInterval (below, 12m) is the primary trigger.
	execStallWindow = 15 * time.Minute

	// execCheckpointInterval: the soft wall-clock checkpoint cadence. Once per
	// interval the loop decides — between turns — whether this interval saw
	// forward progress (extend for another interval) or not (inject the one
	// graceful-finalize directive). 12m is comfortably longer than one full
	// non-terminating muse-glimmer think turn, and short enough that three
	// checkpoint decisions fit inside execAbsoluteCeiling.
	execCheckpointInterval = 12 * time.Minute

	// execAbsoluteCeiling: the hard wall-clock backstop for one EXECUTE_BEAD
	// attempt. A model making a novel write every checkpoint but never
	// converging is extended past each soft checkpoint; this ceiling is what
	// finally ends it, as "timeout" (progress was seen — routes to ADJUDICATE's
	// repeated-timeout escalation, which treats a timeout as "bead too big",
	// not "needs more wall-clock"). ≈ the old execMaxWall (60m) minus the
	// headroom that existed only to absorb budget-doubling.
	execAbsoluteCeiling = 45 * time.Minute

	// execFinalizeGrace: after the one graceful-finalize directive is injected,
	// the model gets a single turn; this bounds how long that turn may run
	// before the hard timer ends the attempt as "stalled".
	execFinalizeGrace = 5 * time.Minute

	// execMaxTurns: pure backstop against a pathological fast tool-call loop
	// that would not trip the wall-clock for a long time.
	execMaxTurns = 50

	// execSpiralStreakLimit: consecutive turns that hit the per-turn token cap
	// mid-think (done_reason "length", no tool call, no content). Two running is
	// the confirmed non-convergent reasoning-spiral shape (cf.
	// memory/project_tool_loop_length_cap_fix).
	execSpiralStreakLimit = 2

	// execIdenticalStreakLimit: consecutive turns whose tool call(s) + result(s)
	// exactly repeat the immediately prior turn with no productive write
	// between.
	execIdenticalStreakLimit = 3

	// execEmptyTurnStreakLimit: consecutive "empty" turns — a turn that made no
	// productive write while nothing has ever been written to disk this attempt
	// (see turnObs.emptyTurn) — before the attempt is marked stalled. The first
	// empty turn triggers one write-now redirect (buildEmptyTurnRedirect); this
	// limit gives the model two further turns to act on it before giving up.
	// This is the fast path for the muse-glimmer planning spiral
	// (lsystem-baseline-1 bead 2): ~14 min/turn of pure thinking, content_chars
	// and write_file calls both zero, that the wall-clock backstops
	// (execCheckpointInterval / execStallWindow) only caught after ~19 min with a
	// generic finalize directive that did not break the spiral. Detected the
	// instant a turn ends — no wall-clock wait of its own.
	execEmptyTurnStreakLimit = 3
)

// turnObs is one turn's worth of mechanical progress signal, computed by the
// caller (runExecuteBeadReal) and handed to progressTracker.observe once per
// turn. No field requires model judgment.
type turnObs struct {
	// productive: a write_file call this turn targeted an in-scope output file,
	// its tool result began with "ok:", and it left that file at a content hash
	// the file has not held before this attempt (see hashLedger).
	productive bool
	// lengthCapEmpty: the model hit its per-turn token budget while still
	// reasoning — DoneReason == "length", zero tool calls, empty content.
	lengthCapEmpty bool
	// identicalCall: this turn's tool call(s) and their result(s) are
	// byte-identical to the immediately prior turn's, with no productive write
	// in between.
	identicalCall bool
	// emptyTurn: this turn made no productive write AND nothing has been written
	// to disk at all this attempt (write_file call count still zero). Covers the
	// planning-spiral shapes — zero tool calls, or a single throwaway read_file
	// after a long think — where the model emits nothing actionable. A turn that
	// wrote something earlier in the attempt is NOT empty even if it made no
	// progress this turn: that case is the wall-clock backstop's job.
	emptyTurn bool
}

// progressTracker accumulates per-turn progress signal for one EXECUTE_BEAD
// attempt and decides when the attempt has walled: turns (or wall-clock)
// advancing with no measurable forward progress toward the bead's output files.
//
// It performs no I/O. The caller computes each turnObs (hashing files, comparing
// tool-call signatures) and calls observe exactly once per turn, then consults
// wall / progressWithin. lastProductiveNanos is published as an atomic so the
// wall-clock timer goroutine in runExecuteBeadReal can read "time since last
// progress" without sharing a lock with the turn loop.
type progressTracker struct {
	start               time.Time
	nonProductiveStreak int
	spiralStreak        int
	identicalStreak     int
	emptyTurnStreak     int
	lastProductiveNanos atomic.Int64
}

func newProgressTracker(start time.Time) *progressTracker {
	t := &progressTracker{start: start}
	t.lastProductiveNanos.Store(start.UnixNano())
	return t
}

// observe folds one turn's signal into the tracker. now is the turn-completion
// time (passed in rather than read internally so tests control the clock).
func (t *progressTracker) observe(now time.Time, o turnObs) {
	if o.productive {
		t.nonProductiveStreak = 0
		t.lastProductiveNanos.Store(now.UnixNano())
	} else {
		t.nonProductiveStreak++
	}
	if o.lengthCapEmpty {
		t.spiralStreak++
	} else {
		t.spiralStreak = 0
	}
	if o.identicalCall {
		t.identicalStreak++
	} else {
		t.identicalStreak = 0
	}
	if o.emptyTurn {
		t.emptyTurnStreak++
	} else {
		t.emptyTurnStreak = 0
	}
}

// lastProductive is the time of the most recent productive turn, or the attempt
// start if there has not been one.
func (t *progressTracker) lastProductive() time.Time {
	return time.Unix(0, t.lastProductiveNanos.Load())
}

// progressWithin reports whether a productive turn happened within the last d.
// Used by the wall-clock checkpoint to decide extend-vs-finalize.
func (t *progressTracker) progressWithin(now time.Time, d time.Duration) bool {
	return now.Sub(t.lastProductive()) < d
}

// wall reports whether the attempt has stalled, with a short reason for the
// trace. Called once per turn after observe, and also consulted on the
// wall-clock checkpoint path.
func (t *progressTracker) wall(now time.Time, turn int) (bool, string) {
	switch {
	case t.spiralStreak >= execSpiralStreakLimit:
		return true, "reasoning spiral — per-turn token cap hit with no output on consecutive turns"
	case t.identicalStreak >= execIdenticalStreakLimit:
		return true, "identical tool call repeated with no forward progress"
	case turn >= execMaxTurns:
		return true, "turn cap reached"
	case t.nonProductiveStreak >= 1 && now.Sub(t.lastProductive()) > execStallWindow:
		return true, "no output file changed for " + execStallWindow.String()
	default:
		return false, ""
	}
}

// hashLedger tracks, per in-scope output file, the set of content hashes it has
// held during this attempt. recordWrite reports whether hash is one the file has
// not held before — i.e. the write made a real change, not a no-op rewrite of
// identical bytes and not a revert to an earlier state (given A->B->A, the
// second A is not novel and the turn that produced it is not productive).
type hashLedger struct {
	seen map[string]map[string]bool
}

func newHashLedger() *hashLedger {
	return &hashLedger{seen: map[string]map[string]bool{}}
}

func (l *hashLedger) recordWrite(relpath, hash string) bool {
	s := l.seen[relpath]
	if s == nil {
		s = map[string]bool{}
		l.seen[relpath] = s
	}
	if s[hash] {
		return false
	}
	s[hash] = true
	return true
}
