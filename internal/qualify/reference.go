package qualify

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// ReferenceDB is the final-state project DB (corpus.db) — the daemon wrote it
// during the capture run, so it holds every committed gemma verdict. The
// per-dispatch db.sqlite snapshots are pre-decision; reference verdicts come
// from here.
type ReferenceDB struct {
	db *sql.DB
}

func OpenReferenceDB(path string) (*ReferenceDB, error) {
	d, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, err
	}
	return &ReferenceDB{db: d}, nil
}

func (r *ReferenceDB) Close() error { return r.db.Close() }

// JudgeDecision returns the baseline REFINE_TESTS_JUDGE decision ("approved" or
// "revise") for a bead+cycle.
func (r *ReferenceDB) JudgeDecision(ctx context.Context, beadID, cycle int64) (string, error) {
	var d string
	err := r.db.QueryRowContext(ctx, `
		SELECT decision FROM test_refinements
		WHERE bead_id = ? AND cycle_id = ? AND verb = 'REFINE_TESTS_JUDGE'
		  AND decision IN ('approved','revise')
		ORDER BY turn DESC LIMIT 1`, beadID, cycle).Scan(&d)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("no baseline JUDGE decision for bead %d cycle %d", beadID, cycle)
	}
	return d, err
}

// CritiqueLabel classifies a corpus CRITIQUE case for grading a candidate:
//
//	"bad"       — the reference CRITIQUE flagged it (all_correct=false) AND the
//	              next JUDGE agreed (revise): a real defect a good CRITIQUE catches.
//	"good"      — reference CRITIQUE clean (all_correct=true) AND JUDGE approved:
//	              a clean test a good CRITIQUE leaves alone.
//	"ambiguous" — the two disagree: reference CRITIQUE flagged but JUDGE overrode
//	              (finding was minor/wrong), or reference CRITIQUE missed what only
//	              JUDGE caught. Not a fair CRITIQUE bar either way — recorded but
//	              not scored.
//
// Earlier this keyed on the JUDGE decision alone; that mislabelled b316-c1 (only
// JUDGE caught it — the incumbent CRITIQUE missed it too) as a CRITIQUE "miss",
// and b313-c1 (CRITIQUE flagged, JUDGE overrode) as a clean "good".
func (r *ReferenceDB) CritiqueLabel(ctx context.Context, beadID, cycle int64) (string, error) {
	jd, err := r.JudgeDecision(ctx, beadID, cycle)
	if err != nil {
		return "", err
	}
	critiqueFlagged, err := r.critiqueFlagged(ctx, beadID, cycle)
	if err != nil {
		return "", err
	}
	switch {
	case critiqueFlagged && jd == "revise":
		return "bad", nil
	case !critiqueFlagged && jd == "approved":
		return "good", nil
	default:
		return "ambiguous", nil
	}
}

// critiqueFlagged reports whether the reference REFINE_TESTS_CRITIQUE for this
// bead+cycle returned all_correct=false (or any findings).
func (r *ReferenceDB) critiqueFlagged(ctx context.Context, beadID, cycle int64) (bool, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `
		SELECT ha.raw_output FROM handoff_attempts ha
		JOIN handoff_jobs hj ON hj.id = ha.job_id
		WHERE hj.verb = 'REFINE_TESTS_CRITIQUE' AND hj.bead_id = ? AND hj.refinement_cycle_id = ?
		  AND ha.validation_result = 'valid'
		ORDER BY ha.id DESC LIMIT 1`, beadID, cycle).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, fmt.Errorf("no reference CRITIQUE for bead %d cycle %d", beadID, cycle)
	}
	if err != nil {
		return false, err
	}
	lo, hi := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if lo < 0 || hi <= lo {
		return false, fmt.Errorf("reference CRITIQUE bead %d cycle %d: no JSON object", beadID, cycle)
	}
	var out struct {
		AllCorrect bool  `json:"all_correct"`
		Findings   []any `json:"findings"`
	}
	if err := json.Unmarshal([]byte(raw[lo:hi+1]), &out); err != nil {
		return false, fmt.Errorf("reference CRITIQUE bead %d cycle %d: %w", beadID, cycle, err)
	}
	return !out.AllCorrect || len(out.Findings) > 0, nil
}

// AdjudicationDecisions returns every committed adjudication decision for a
// bead, ordered by row id (== chronological). The Nth captured ADJUDICATE
// dispatch for that bead corresponds to the Nth entry.
func (r *ReferenceDB) AdjudicationDecisions(ctx context.Context, beadID int64) ([]AdjRef, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT decision, trend, bead_spec_fit FROM adjudications
		WHERE bead_id = ? ORDER BY id ASC`, beadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdjRef
	for rows.Next() {
		var a AdjRef
		if err := rows.Scan(&a.Decision, &a.Trend, &a.BeadSpecFit); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type AdjRef struct {
	Decision    string
	Trend       string
	BeadSpecFit string
}
