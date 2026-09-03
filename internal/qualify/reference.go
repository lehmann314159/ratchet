package qualify

import (
	"context"
	"database/sql"
	"fmt"

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

// CritiqueLabel classifies a corpus CRITIQUE case as "good" or "bad" by what
// the real JUDGE decided next that cycle: approved => the test file was good,
// revise => it had a real defect CRITIQUE should have caught.
func (r *ReferenceDB) CritiqueLabel(ctx context.Context, beadID, cycle int64) (string, error) {
	jd, err := r.JudgeDecision(ctx, beadID, cycle)
	if err != nil {
		return "", err
	}
	if jd == "approved" {
		return "good", nil
	}
	return "bad", nil
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
