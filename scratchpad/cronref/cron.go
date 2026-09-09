package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ---- field.go equivalent ----

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var weekdayNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// parseField parses one whole cron field (which may be a comma-separated list of
// atoms) into a bitmask where bit i is set iff value i is included. min/max are
// the inclusive numeric bounds for this field; names maps lowercase names to
// numbers (nil if this field has no names). For the day-of-week field, pass
// min=0 max=7 and post-fold bit 7 into bit 0 (7 == Sunday).
func parseField(spec string, min, max int, names map[string]int) (uint64, error) {
	if spec == "" {
		return 0, fmt.Errorf("empty field")
	}
	var mask uint64
	for _, atom := range strings.Split(spec, ",") {
		m, err := parseAtom(atom, min, max, names)
		if err != nil {
			return 0, err
		}
		mask |= m
	}
	// day-of-week: 7 == Sunday == 0
	if max == 7 {
		if mask&(1<<7) != 0 {
			mask |= 1 << 0
		}
		mask &^= 1 << 7
	}
	return mask, nil
}

// parseAtom parses a single atom: one of
//
//   - *\/step
//     N            N\/step        (N\/step means N-max\/step)
//     A-B          A-B\/step
//     NAME         NAME-NAME      NAME-NAME\/step
func parseAtom(atom string, min, max int, names map[string]int) (uint64, error) {
	atom = strings.ToLower(strings.TrimSpace(atom))
	if atom == "" {
		return 0, fmt.Errorf("empty atom")
	}

	body := atom
	step := 1
	if i := strings.IndexByte(atom, '/'); i >= 0 {
		body = atom[:i]
		stepStr := atom[i+1:]
		s, err := strconv.Atoi(stepStr)
		if err != nil || s < 1 {
			return 0, fmt.Errorf("bad step %q in %q", stepStr, atom)
		}
		step = s
	}

	var lo, hi int
	dash := -1
	if len(body) > 1 {
		dash = strings.IndexByte(body[1:], '-')
		if dash >= 0 {
			dash++
		}
	}
	switch {
	case body == "*":
		lo, hi = min, max
	case dash >= 0:
		aStr, bStr := body[:dash], body[dash+1:]
		a, err := parseValue(aStr, min, max, names)
		if err != nil {
			return 0, err
		}
		b, err := parseValue(bStr, min, max, names)
		if err != nil {
			return 0, err
		}
		if a > b {
			return 0, fmt.Errorf("range %q is descending (%d > %d); ranges do not wrap", body, a, b)
		}
		lo, hi = a, b
	default:
		v, err := parseValue(body, min, max, names)
		if err != nil {
			return 0, err
		}
		if strings.Contains(atom, "/") {
			// N/step  ==  N-max/step
			lo, hi = v, max
		} else {
			lo, hi = v, v
		}
	}

	var mask uint64
	for v := lo; v <= hi; v += step {
		mask |= 1 << uint(v)
	}
	return mask, nil
}

func parseValue(s string, min, max int, names map[string]int) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if names != nil {
		if v, ok := names[s]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("not a number or known name: %q", s)
	}
	if v < min || v > max {
		return 0, fmt.Errorf("value %d out of range [%d,%d]", v, min, max)
	}
	return v, nil
}

// ---- schedule.go equivalent ----

type Schedule struct {
	Minute        uint64
	Hour          uint64
	Dom           uint64
	Month         uint64
	Dow           uint64
	DomRestricted bool
	DowRestricted bool
}

var macros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

func Parse(expr string) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return Schedule{}, fmt.Errorf("empty expression")
	}
	if strings.HasPrefix(expr, "@") {
		exp, ok := macros[strings.ToLower(expr)]
		if !ok {
			return Schedule{}, fmt.Errorf("unknown macro %q", expr)
		}
		expr = exp
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return Schedule{}, fmt.Errorf("expected 5 fields, got %d", len(fields))
	}
	var s Schedule
	var err error
	if s.Minute, err = parseField(fields[0], 0, 59, nil); err != nil {
		return Schedule{}, fmt.Errorf("minute: %w", err)
	}
	if s.Hour, err = parseField(fields[1], 0, 23, nil); err != nil {
		return Schedule{}, fmt.Errorf("hour: %w", err)
	}
	if s.Dom, err = parseField(fields[2], 1, 31, nil); err != nil {
		return Schedule{}, fmt.Errorf("day-of-month: %w", err)
	}
	if s.Month, err = parseField(fields[3], 1, 12, monthNames); err != nil {
		return Schedule{}, fmt.Errorf("month: %w", err)
	}
	if s.Dow, err = parseField(fields[4], 0, 7, weekdayNames); err != nil {
		return Schedule{}, fmt.Errorf("day-of-week: %w", err)
	}
	s.DomRestricted = fields[2] != "*"
	s.DowRestricted = fields[4] != "*"
	return s, nil
}

func Match(s Schedule, t time.Time) bool {
	t = t.UTC()
	min := s.Minute&(1<<uint(t.Minute())) != 0
	hr := s.Hour&(1<<uint(t.Hour())) != 0
	mon := s.Month&(1<<uint(int(t.Month()))) != 0
	dom := s.Dom&(1<<uint(t.Day())) != 0
	dow := s.Dow&(1<<uint(int(t.Weekday()))) != 0
	if !min || !hr || !mon {
		return false
	}
	if s.DomRestricted && s.DowRestricted {
		return dom || dow
	}
	return dom && dow
}

// ---- iterate.go equivalent ----

func Next(s Schedule, after time.Time) (time.Time, bool) {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := after.UTC().AddDate(5, 0, 0)
	for !t.After(limit) {
		if Match(s, t) {
			return t, true
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, false
}

func NextN(s Schedule, after time.Time, n int) []time.Time {
	var out []time.Time
	cur := after
	for i := 0; i < n; i++ {
		nx, ok := Next(s, cur)
		if !ok {
			break
		}
		out = append(out, nx)
		cur = nx
	}
	return out
}

// ---- describe.go equivalent ----

type FieldSummary struct {
	Minute, Hour, DayOfMonth, Month, DayOfWeek string
	DomDowOr                                   bool
}

var monthFull = []string{"", "January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December"}
var weekdayFull = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func setValues(mask uint64, min, max int) []int {
	var out []int
	for v := min; v <= max; v++ {
		if mask&(1<<uint(v)) != 0 {
			out = append(out, v)
		}
	}
	return out
}

func allOnes(mask uint64, min, max int) bool {
	for v := min; v <= max; v++ {
		if mask&(1<<uint(v)) == 0 {
			return false
		}
	}
	return true
}

func Describe(s Schedule) FieldSummary {
	num := func(mask uint64, min, max int, unit string) string {
		if allOnes(mask, min, max) {
			return "every " + unit
		}
		vs := setValues(mask, min, max)
		parts := make([]string, len(vs))
		for i, v := range vs {
			parts[i] = strconv.Itoa(v)
		}
		return strings.Join(parts, ", ")
	}
	fs := FieldSummary{
		Minute:     num(s.Minute, 0, 59, "minute"),
		Hour:       num(s.Hour, 0, 23, "hour"),
		DayOfMonth: num(s.Dom, 1, 31, "day-of-month"),
		DomDowOr:   s.DomRestricted && s.DowRestricted,
	}
	if allOnes(s.Month, 1, 12) {
		fs.Month = "every month"
	} else {
		vs := setValues(s.Month, 1, 12)
		parts := make([]string, len(vs))
		for i, v := range vs {
			parts[i] = monthFull[v]
		}
		fs.Month = strings.Join(parts, ", ")
	}
	if allOnes(s.Dow, 0, 6) {
		fs.DayOfWeek = "every day-of-week"
	} else {
		vs := setValues(s.Dow, 0, 6)
		parts := make([]string, len(vs))
		for i, v := range vs {
			parts[i] = weekdayFull[v]
		}
		fs.DayOfWeek = strings.Join(parts, ", ")
	}
	return fs
}

func mustParse(expr string) Schedule {
	s, err := Parse(expr)
	if err != nil {
		panic(err)
	}
	return s
}

func rfc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func main() {
	p := fmt.Println
	pf := fmt.Printf

	checkSaved()

	// ===== atom / field masks =====
	p("=== parseAtom / parseField masks (as sorted value lists) ===")
	fieldCases := []struct {
		spec     string
		min, max int
		names    map[string]int
	}{
		{"*", 0, 59, nil},
		{"*/15", 0, 59, nil},
		{"5", 0, 59, nil},
		{"0,15,30,45", 0, 59, nil},
		{"10-30/5", 0, 59, nil},
		{"5/15", 0, 59, nil},
		{"*/6", 0, 23, nil},
		{"9-17", 0, 23, nil},
		{"1-5", 0, 7, weekdayNames},
		{"mon-fri", 0, 7, weekdayNames},
		{"sun", 0, 7, weekdayNames},
		{"7", 0, 7, weekdayNames},
		{"0,7", 0, 7, weekdayNames},
		{"jan,jun,dec", 1, 12, monthNames},
		{"*/3", 1, 12, monthNames},
		{"15-45/15", 0, 59, nil},
	}
	for _, c := range fieldCases {
		m, err := parseField(c.spec, c.min, c.max, c.names)
		if err != nil {
			pf("  %-14q [%d,%d] -> ERROR %v\n", c.spec, c.min, c.max, err)
			continue
		}
		hi := c.max
		if c.max == 7 {
			hi = 6
		}
		pf("  %-14q [%d,%d] -> %v\n", c.spec, c.min, c.max, setValues(m, c.min, hi))
	}

	p("")
	p("=== parse errors ===")
	errCases := []string{
		"* * * *",         // 4 fields
		"* * * * * *",     // 6 fields
		"",                // empty
		"60 * * * *",      // minute 60
		"* 24 * * *",      // hour 24
		"* * 0 * *",       // dom 0
		"* * * 13 *",      // month 13
		"* * * * 8",       // dow 8
		"* * * jan *",     // name in wrong field
		"sat-sun * * * *", // descending range (6 > 0)  -- wait min/max 0,59 so names invalid anyway
		"fri-mon * * * *", // in minute field: invalid names
		"@bogus",          // unknown macro
		"*/0 * * * *",     // step 0
		"5-1 * * * *",     // descending numeric range
	}
	for _, e := range errCases {
		_, err := Parse(e)
		pf("  %-20q -> %v\n", e, err)
	}
	// descending weekday range specifically
	_, err := parseField("sat-sun", 0, 7, weekdayNames)
	pf("  parseField(%q, dow) -> %v\n", "sat-sun", err)

	p("")
	p("=== more field cases ===")
	moreCases := []struct {
		spec     string
		min, max int
		names    map[string]int
	}{
		{"jan", 0, 59, nil},               // name in minute field
		{"MON-FRI", 0, 7, weekdayNames},   // uppercase
		{"mon-fri/2", 0, 7, weekdayNames}, // name range + step
		{"?", 0, 59, nil},                 // question mark not supported
		{"0-59", 0, 59, nil},              // explicit full range == *
	}
	for _, c := range moreCases {
		m, err := parseField(c.spec, c.min, c.max, c.names)
		if err != nil {
			pf("  %-12q -> ERROR %v\n", c.spec, err)
			continue
		}
		hi := c.max
		if c.max == 7 {
			hi = 6
		}
		pf("  %-12q -> %v (allOnes=%v)\n", c.spec, setValues(m, c.min, hi), allOnes(m, c.min, hi))
	}
	// whitespace tolerance + ? in Parse
	for _, e := range []string{"  0   9  *  *  1-5  ", "? * * * *", "*/2 * * * *"} {
		s, err := Parse(e)
		if err != nil {
			pf("  Parse(%q) -> ERROR %v\n", e, err)
			continue
		}
		pf("  Parse(%q) -> minute=%v domR=%v dowR=%v\n", e, setValues(s.Minute, 0, 59), s.DomRestricted, s.DowRestricted)
	}
	// Next strictly-after check: after exactly on a fire minute
	{
		s := mustParse("0 9 * * *")
		nx, ok := Next(s, rfc("2026-01-01T09:00:00Z"))
		pf("  Next(%q, exactly 2026-01-01T09:00Z) -> %s %v (must be next day, strictly after)\n", "0 9 * * *", nx.Format(time.RFC3339), ok)
		nx2, _ := Next(s, rfc("2026-01-01T08:59:00Z"))
		pf("  Next(%q, 2026-01-01T08:59Z) -> %s\n", "0 9 * * *", nx2.Format(time.RFC3339))
	}

	p("")
	p("=== macro expansions (Schedule fields) ===")
	for _, mc := range []string{"@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly"} {
		s := mustParse(mc)
		pf("  %-10s minute=%v hour=%v dom=%v month=%v dow=%v domR=%v dowR=%v\n",
			mc, setValues(s.Minute, 0, 59), setValues(s.Hour, 0, 23),
			setValues(s.Dom, 1, 31), setValues(s.Month, 1, 12), setValues(s.Dow, 0, 6),
			s.DomRestricted, s.DowRestricted)
	}

	// ===== Match =====
	p("")
	p("=== Match ===")
	matchCases := []struct {
		expr string
		t    string
	}{
		{"0 9 * * 1-5", "2026-01-05T09:00:00Z"}, // Monday 09:00
		{"0 9 * * 1-5", "2026-01-05T09:01:00Z"}, // minute off
		{"0 9 * * 1-5", "2026-01-03T09:00:00Z"}, // Saturday
		{"*/15 * * * *", "2026-01-05T13:30:00Z"},
		{"*/15 * * * *", "2026-01-05T13:31:00Z"},
		{"0 0 13 * 5", "2026-02-13T00:00:00Z"}, // Friday the 13th: dom=13 AND dow=Fri, both restricted -> OR
		{"0 0 13 * 5", "2026-03-13T00:00:00Z"}, // March 13 2026 is a Friday -> matches on both
		{"0 0 13 * 5", "2026-11-13T00:00:00Z"}, // Nov 13 2026 is a Friday
		{"0 0 13 * 5", "2026-01-13T00:00:00Z"}, // Jan 13 2026 is Tuesday: dom matches, dow no -> OR true
		{"0 0 13 * 5", "2026-01-02T00:00:00Z"}, // Friday Jan 2: dow matches, dom no -> OR true
		{"0 0 * * 0", "2026-01-04T00:00:00Z"},  // Sunday
		{"0 12 1 * *", "2026-05-01T12:00:00Z"},
	}
	for _, c := range matchCases {
		s := mustParse(c.expr)
		pf("  %-14q @ %s -> %v\n", c.expr, c.t, Match(s, rfc(c.t)))
	}

	// ===== Next / NextN =====
	p("")
	p("=== Next / NextN ===")
	nextCases := []struct {
		expr  string
		after string
		n     int
	}{
		{"0 9 * * 1-5", "2026-01-01T00:00:00Z", 6}, // Jan 1 2026 is Thursday
		{"*/15 * * * *", "2026-01-01T00:00:00Z", 5},
		{"0 0 29 2 *", "2026-01-01T00:00:00Z", 3},  // Feb 29 - leap years only (2028, 2032, 2036)
		{"0 0 30 2 *", "2026-01-01T00:00:00Z", 1},  // Feb 30 - never
		{"0 0 1 1 *", "2026-06-15T12:00:00Z", 3},   // every Jan 1
		{"30 3 * * 1", "2026-01-01T00:00:00Z", 4},  // Mondays 03:30
		{"0 0 13 * 5", "2026-01-01T00:00:00Z", 4},  // Friday the 13th OR 13th OR Friday
		{"0 0 * * 0", "2026-01-01T00:00:00Z", 3},   // Sundays midnight
		{"15 14 1 * *", "2026-01-01T00:00:00Z", 3}, // 1st of month 14:15
		{"@hourly", "2026-01-01T00:30:00Z", 3},
	}
	for _, c := range nextCases {
		s := mustParse(c.expr)
		res := NextN(s, rfc(c.after), c.n)
		strs := make([]string, len(res))
		for i, t := range res {
			strs[i] = t.Format(time.RFC3339)
		}
		pf("  %-14q after %s (n=%d) -> %v\n", c.expr, c.after, c.n, strs)
	}
	// single Next false case
	s := mustParse("0 0 30 2 *")
	nx, ok := Next(s, rfc("2026-01-01T00:00:00Z"))
	pf("  Next(%q, 2026-01-01) -> %v %v\n", "0 0 30 2 *", nx.Format(time.RFC3339), ok)

	// weekday sanity for the Friday-the-13th claims
	p("")
	p("=== weekday sanity ===")
	for _, d := range []string{"2026-01-13", "2026-02-13", "2026-03-13", "2026-11-13", "2026-01-02", "2026-01-01", "2026-01-05", "2026-01-04"} {
		t := rfc(d + "T00:00:00Z")
		pf("  %s is %s\n", d, t.Weekday())
	}

	// ===== Describe =====
	p("")
	p("=== Describe ===")
	descCases := []string{
		"*/15 9-17 * * 1-5",
		"0 0 1 1 *",
		"0 0 13 * 5",
		"* * * * *",
		"0 0,12 * jan,jun,dec *",
		"30 3 * * 1",
	}
	for _, e := range descCases {
		fs := Describe(mustParse(e))
		pf("  %q:\n    Minute=%q Hour=%q DoM=%q Month=%q DoW=%q DomDowOr=%v\n",
			e, fs.Minute, fs.Hour, fs.DayOfMonth, fs.Month, fs.DayOfWeek, fs.DomDowOr)
	}
	_ = err
}
