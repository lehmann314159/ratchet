package execution

import (
	"testing"
	"time"
)

func TestProgressTracker_ProductiveResetsStreakAndClock(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	tr := newProgressTracker(start)

	// Three non-productive turns build the streak.
	for i := 1; i <= 3; i++ {
		tr.observe(start.Add(time.Duration(i)*time.Minute), turnObs{})
	}
	if tr.nonProductiveStreak != 3 {
		t.Fatalf("nonProductiveStreak = %d, want 3", tr.nonProductiveStreak)
	}

	// A productive turn resets the streak and advances lastProductive.
	at := start.Add(10 * time.Minute)
	tr.observe(at, turnObs{productive: true})
	if tr.nonProductiveStreak != 0 {
		t.Errorf("nonProductiveStreak = %d, want 0 after productive turn", tr.nonProductiveStreak)
	}
	if !tr.lastProductive().Equal(at) {
		t.Errorf("lastProductive = %v, want %v", tr.lastProductive(), at)
	}
}

func TestProgressTracker_StallWindow(t *testing.T) {
	start := time.Unix(2_000_000, 0)
	tr := newProgressTracker(start)

	// One non-productive turn just inside the window: no stall.
	tr.observe(start.Add(execStallWindow-time.Minute), turnObs{})
	if stall, reason := tr.wall(start.Add(execStallWindow-time.Minute), 2); stall {
		t.Fatalf("unexpected stall inside window: %s", reason)
	}

	// Past the window with a non-productive turn on record: stall.
	now := start.Add(execStallWindow + time.Minute)
	stall, reason := tr.wall(now, 2)
	if !stall {
		t.Fatalf("expected stall past window")
	}
	if reason == "" {
		t.Errorf("stall reason should be non-empty")
	}

	// But a productive turn in between clears it.
	tr.observe(now, turnObs{productive: true})
	if stall, reason := tr.wall(now.Add(time.Minute), 3); stall {
		t.Errorf("unexpected stall right after productive turn: %s", reason)
	}
}

func TestProgressTracker_StallWindowNeedsANonProductiveTurn(t *testing.T) {
	start := time.Unix(3_000_000, 0)
	tr := newProgressTracker(start)
	// No turns observed at all — even well past the window, wall() must not
	// fire on the time predicate (nonProductiveStreak == 0).
	if stall, reason := tr.wall(start.Add(2*execStallWindow), 1); stall {
		t.Errorf("wall fired with zero turns observed: %s", reason)
	}
}

func TestProgressTracker_SpiralStreak(t *testing.T) {
	start := time.Unix(4_000_000, 0)
	tr := newProgressTracker(start)

	tr.observe(start.Add(time.Minute), turnObs{lengthCapEmpty: true})
	if stall, _ := tr.wall(start.Add(time.Minute), 1); stall {
		t.Fatalf("one length-cap turn should not stall")
	}
	tr.observe(start.Add(2*time.Minute), turnObs{lengthCapEmpty: true})
	stall, reason := tr.wall(start.Add(2*time.Minute), 2)
	if !stall {
		t.Fatalf("two consecutive length-cap turns should stall")
	}
	if reason == "" {
		t.Errorf("want a reason")
	}

	// A normal turn resets it.
	tr.observe(start.Add(3*time.Minute), turnObs{})
	if tr.spiralStreak != 0 {
		t.Errorf("spiralStreak = %d, want 0 after a normal turn", tr.spiralStreak)
	}
}

func TestProgressTracker_IdenticalStreak(t *testing.T) {
	start := time.Unix(5_000_000, 0)
	tr := newProgressTracker(start)

	for i := 1; i <= 2; i++ {
		tr.observe(start.Add(time.Duration(i)*time.Minute), turnObs{identicalCall: true})
		if stall, _ := tr.wall(start.Add(time.Duration(i)*time.Minute), i); stall {
			t.Fatalf("stall at identicalStreak=%d, want no stall until %d", i, execIdenticalStreakLimit)
		}
	}
	tr.observe(start.Add(3*time.Minute), turnObs{identicalCall: true})
	if stall, _ := tr.wall(start.Add(3*time.Minute), 3); !stall {
		t.Fatalf("expected stall at identicalStreak=%d", execIdenticalStreakLimit)
	}

	tr.observe(start.Add(4*time.Minute), turnObs{})
	if tr.identicalStreak != 0 {
		t.Errorf("identicalStreak = %d, want 0 after a differing turn", tr.identicalStreak)
	}
}

func TestProgressTracker_EmptyTurnStreak(t *testing.T) {
	start := time.Unix(6_500_000, 0)
	tr := newProgressTracker(start)

	// Two empty turns build the streak.
	for i := 1; i <= 2; i++ {
		tr.observe(start.Add(time.Duration(i)*time.Minute), turnObs{emptyTurn: true})
	}
	if tr.emptyTurnStreak != 2 {
		t.Fatalf("emptyTurnStreak = %d, want 2", tr.emptyTurnStreak)
	}

	// A productive turn resets it.
	tr.observe(start.Add(3*time.Minute), turnObs{productive: true})
	if tr.emptyTurnStreak != 0 {
		t.Errorf("emptyTurnStreak = %d, want 0 after a productive turn", tr.emptyTurnStreak)
	}

	// A non-empty, non-productive turn (e.g. a read after something was already
	// written) also resets it.
	tr.observe(start.Add(4*time.Minute), turnObs{emptyTurn: true})
	tr.observe(start.Add(5*time.Minute), turnObs{})
	if tr.emptyTurnStreak != 0 {
		t.Errorf("emptyTurnStreak = %d, want 0 after a non-empty turn", tr.emptyTurnStreak)
	}

	// An empty turn is non-productive but does not advance the productive clock.
	fresh := newProgressTracker(start)
	at := fresh.lastProductive()
	fresh.observe(start.Add(time.Minute), turnObs{emptyTurn: true})
	if fresh.nonProductiveStreak != 1 {
		t.Errorf("nonProductiveStreak = %d, want 1", fresh.nonProductiveStreak)
	}
	if !fresh.lastProductive().Equal(at) {
		t.Errorf("lastProductive moved on an empty turn")
	}
}

func TestProgressTracker_Elapsed(t *testing.T) {
	start := time.Unix(9_000_000, 0)
	tr := newProgressTracker(start)
	if got := tr.elapsed(start.Add(20 * time.Minute)); got != 20*time.Minute {
		t.Errorf("elapsed = %v, want 20m", got)
	}
}

func TestProgressTracker_TurnCap(t *testing.T) {
	start := time.Unix(6_000_000, 0)
	tr := newProgressTracker(start)
	// Keep it productive every turn so no other predicate fires.
	for turn := 1; turn < execMaxTurns; turn++ {
		tr.observe(start.Add(time.Duration(turn)*time.Second), turnObs{productive: true})
		if stall, _ := tr.wall(start.Add(time.Duration(turn)*time.Second), turn); stall {
			t.Fatalf("unexpected stall at turn %d", turn)
		}
	}
	if stall, reason := tr.wall(start, execMaxTurns); !stall || reason != "turn cap reached" {
		t.Fatalf("wall at turn cap = (%v, %q), want (true, \"turn cap reached\")", stall, reason)
	}
}

func TestProgressTracker_ProgressWithin(t *testing.T) {
	start := time.Unix(7_000_000, 0)
	tr := newProgressTracker(start)
	tr.observe(start.Add(time.Minute), turnObs{productive: true})

	if !tr.progressWithin(start.Add(2*time.Minute), 5*time.Minute) {
		t.Errorf("progressWithin should be true 1 min after a productive turn")
	}
	if tr.progressWithin(start.Add(10*time.Minute), 5*time.Minute) {
		t.Errorf("progressWithin should be false 9 min after the last productive turn")
	}
}

// The EXECUTE_BEAD window is fixed now — the checkpoint cadence and the
// absolute ceiling are constants, not derived from execution_budget (see
// docs/execute-checkpoint-decouple-plan.md). Lock in the ordering the loop
// relies on: a checkpoint fires before the per-turn stall window, and both
// well inside the ceiling.
func TestExecTimingConstantsOrdering(t *testing.T) {
	if !(execCheckpointInterval < execStallWindow) {
		t.Errorf("execCheckpointInterval (%v) must be < execStallWindow (%v)", execCheckpointInterval, execStallWindow)
	}
	if !(execStallWindow < execAbsoluteCeiling) {
		t.Errorf("execStallWindow (%v) must be < execAbsoluteCeiling (%v)", execStallWindow, execAbsoluteCeiling)
	}
	if !(execFinalizeGrace < execAbsoluteCeiling) {
		t.Errorf("execFinalizeGrace (%v) must be < execAbsoluteCeiling (%v)", execFinalizeGrace, execAbsoluteCeiling)
	}
}

func TestHashLedger_Oscillation(t *testing.T) {
	l := newHashLedger()

	if !l.recordWrite("game.go", "A") {
		t.Errorf("first write of A should be novel")
	}
	if l.recordWrite("game.go", "A") {
		t.Errorf("immediate rewrite of identical bytes should NOT be novel")
	}
	if !l.recordWrite("game.go", "B") {
		t.Errorf("write of B should be novel")
	}
	if l.recordWrite("game.go", "A") {
		t.Errorf("revert to earlier state A should NOT be novel")
	}
	// Different file, same hash string — tracked independently.
	if !l.recordWrite("other.go", "A") {
		t.Errorf("first write of A to a different file should be novel")
	}
}
