package verbs

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ratchet/internal/db"
)

// Real scaffold stubs from qual-corpus-cronstudio-1 (memory/handoff_cronstudio_1).
const (
	cronStudioFieldStub = `package main

var monthNames map[string]int
var weekdayNames map[string]int

func parseValue(token string, min, max int, names map[string]int) (int, error) { return 0, nil }
func parseAtom(atom string, min, max int, names map[string]int) (uint64, error) { return 0, nil }
func parseField(spec string, min, max int, names map[string]int) (uint64, error) { return 0, nil }
`
	cronStudioScheduleStub = `package main

import "time"

type Schedule struct {
	Minute, Hour, DayOfMonth, Month, DayOfWeek uint64
	DomRestricted, DowRestricted               bool
}

var macros map[string]string

func Parse(expr string) (Schedule, error) { return Schedule{}, nil }
func Match(s Schedule, t time.Time) bool  { return false }
`
)

func writeCronStudioScaffold(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "field.go"), []byte(cronStudioFieldStub), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schedule.go"), []byte(cronStudioScheduleStub), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func cronStudioBeads() []beadState {
	return []beadState{
		{BeadID: 1, Title: "field", OutputFiles: []string{"field.go", "field_test.go"}},
		{BeadID: 2, Title: "schedule", OutputFiles: []string{"schedule.go", "schedule_test.go"}},
	}
}

// TestBuildSiblingSymbolOwners: symbols declared in a sibling bead's scaffold
// file are attributed to that bead; the current bead's own symbols never are.
func TestBuildSiblingSymbolOwners(t *testing.T) {
	dir := writeCronStudioScaffold(t)
	beads := cronStudioBeads()

	owners := buildSiblingSymbolOwners(dir, beads, beads[0]) // current = field

	for _, sym := range []string{"Schedule", "Parse", "Match", "macros"} {
		if _, ok := owners[sym]; !ok {
			t.Errorf("expected %q attributed to a sibling, got none", sym)
		} else if !strings.Contains(owners[sym], "schedule") {
			t.Errorf("owner of %q = %q, want it to name bead 'schedule'", sym, owners[sym])
		}
	}
	for _, sym := range []string{"parseValue", "parseAtom", "parseField", "monthNames", "weekdayNames"} {
		if o, ok := owners[sym]; ok {
			t.Errorf("field's own symbol %q must not be attributed to a sibling (got %q)", sym, o)
		}
	}
}

// TestCrossBeadSymbolContamination_CronStudioRev2 runs the guard against the
// real ADJUDICATE execute_revised output that caused the cron-studio run 1
// escalation: it "completed" the `field` spec with `type Schedule` and
// `func Parse` (owned by schedule.go / bead 2), which would compile-error
// against the scaffold stub.
func TestCrossBeadSymbolContamination_CronStudioRev2(t *testing.T) {
	dir := writeCronStudioScaffold(t)
	beads := cronStudioBeads()
	owners := buildSiblingSymbolOwners(dir, beads, beads[0])

	specBytes, err := os.ReadFile("testdata/cronstudio-field-rev2-contaminated.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	revised := ParsedBead{Title: "field", FullText: string(specBytes), OutputFiles: []string{"field.go", "field_test.go"}}

	v := crossBeadSymbolContamination(revised, owners)
	joined := strings.Join(v, "\n")
	if !strings.Contains(joined, `"Schedule"`) || !strings.Contains(joined, `"Parse"`) {
		t.Errorf("guard missed the sibling-symbol contamination; violations:\n%s", joined)
	}
	// The field bead's own symbols appear in the same spec ("func parseValue(...)")
	// and must not be flagged.
	if strings.Contains(joined, "parseValue") || strings.Contains(joined, "parseField") {
		t.Errorf("guard flagged the bead's own symbols:\n%s", joined)
	}
}

// TestCrossBeadSymbolContamination_ReferenceIsFine: a revised spec that merely
// names or calls a sibling symbol in prose (no `type`/`func` declaration) is
// legitimate context and must not trip the guard.
func TestCrossBeadSymbolContamination_ReferenceIsFine(t *testing.T) {
	owners := map[string]string{"Schedule": "schedule (schedule.go)", "Parse": "schedule (schedule.go)"}
	revised := ParsedBead{
		Title: "field",
		FullText: "Implement parseField in field.go. schedule.go calls parseField and its " +
			"`Parse` function builds a `Schedule` from the masks this bead produces. " +
			"Do NOT define Parse or Schedule here.",
	}
	if v := crossBeadSymbolContamination(revised, owners); len(v) != 0 {
		t.Errorf("guard false-positived on prose references: %v", v)
	}
}

// TestAdjudicateExecuteRevisedDowngradesOnSiblingSymbol: the Commit-side gate.
// When execute_revised's rewrite re-declares a sibling bead's symbol, Commit
// must downgrade to execute_as_is — no broken revision stored, bead retried
// against its current spec.
func TestAdjudicateExecuteRevisedDowngradesOnSiblingSymbol(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE execute_revised sibling-symbol downgrade")
	beadID, revID := seedBead(t, d, -1, "field")
	zero := 0
	seedExecution(t, d, -1, beadID, revID, "stalled", &zero)

	specBytes, err := os.ReadFile("testdata/cronstudio-field-rev2-contaminated.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
	h := &AdjudicateNextExecution{
		budgetDefault: 300,
		folderPath:    t.TempDir(),
		currentBeadSpec: ParsedBead{
			Title: "field", FullText: "Implement field parsing in field.go.",
			OutputFiles: []string{"field.go", "field_test.go"}, ExitCriteria: []string{"go test ./..."},
		},
		siblingSymbolOwners: map[string]string{
			"Schedule": "schedule (schedule.go)",
			"Parse":    "schedule (schedule.go)",
			"Match":    "schedule (schedule.go)",
		},
	}
	out := AdjudicateNextExecutionOutput{
		Trend: "same", BeadSpecFit: "execution_capability_problem",
		Reasoning: "a more explicit spec that lists all required types and functions",
		Decision:  "execute_revised",
		RevisedBead: &ParsedBead{
			Title: "field", FullText: string(specBytes),
			OutputFiles: []string{"field.go", "field_test.go"}, ExitCriteria: []string{"go test ./..."},
			ExecutionBudget: 300, MonitorOverride: "honor",
		},
	}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	if n := countRows(t, d, `SELECT COUNT(*) FROM bead_revisions WHERE bead_id = ? AND revision_number > 1`, beadID); n != 0 {
		t.Errorf("bead_revisions beyond rev1 = %d, want 0 (contaminated revision must not be stored)", n)
	}
	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM beads WHERE id = ?`, beadID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Errorf("bead status = %q, want pending (execute_as_is downgrade)", status)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE verb = ? AND bead_id = ?`, db.VerbExecuteBead, beadID); n != 1 {
		t.Errorf("EXECUTE_BEAD jobs enqueued = %d, want 1", n)
	}
}
