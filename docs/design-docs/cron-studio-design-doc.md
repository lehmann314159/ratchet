# Cron Studio — Design Document

## Overview

A single-user web application for understanding a cron expression. The user types a
5-field cron expression and a base timestamp; the server parses the expression, shows a
plain-language breakdown of each field, and lists the next N times the expression will
fire at or after the base timestamp. The user can also save an expression under a name to
a server-side directory, whose contents are shown as a list on the page. The server uses
only the Go standard library (`net/http`, `html/template`, `time`, `strconv`, `strings`,
`sort`, `os`, `path/filepath`, `sync`, `fmt`, `errors`) and serves an HTML UI updated with
HTMX fragment swaps (`html/template` partials returned from handlers — no client-side
JavaScript beyond the HTMX library itself, no browser-swap behavior asserted in tests).

**Runtime model:** one HTTP server process (`package main`, listens on `:8080`). Saved
expressions persist as files in a `saved/` directory relative to the working directory.
There are no user accounts, no sessions, and no database.

**Cron dialect** (this is the whole grammar — nothing outside it is supported):

- Exactly **5 whitespace-separated fields**: minute, hour, day-of-month, month,
  day-of-week — in that order. Runs of whitespace between fields collapse; leading and
  trailing whitespace is ignored.
- Field value ranges: minute `0–59`, hour `0–23`, day-of-month `1–31`, month `1–12`,
  day-of-week `0–7` where **both 0 and 7 mean Sunday** (Monday is 1 … Saturday is 6).
- Each field is a comma-separated list of **atoms**. An atom is one of:
  `*`, `*/S`, `N`, `N/S`, `A-B`, `A-B/S` — where `N`, `A`, `B` are a number in the
  field's range **or** a name (see below), and `S` is a step integer `≥ 1`.
  - `*` → every value in the field's range.
  - `*/S` → every `S`-th value **starting from the field's minimum** (minute `*/15` →
    0, 15, 30, 45).
  - `N` → just `N`.
  - `A-B` → every value from `A` through `B` inclusive. `A` must be `≤ B`; a descending
    range (`5-1`, `fri-mon`) is an **error** — ranges do not wrap.
  - `A-B/S` → every `S`-th value from `A` through `B` inclusive (minute `10-30/5` →
    10, 15, 20, 25, 30).
  - `N/S` → shorthand for `N-<field max>/S` (minute `5/15` → 5, 20, 35, 50).
- **Names.** Month accepts `jan feb mar apr may jun jul aug sep oct nov dec`
  (case-insensitive) as aliases for `1–12`. Day-of-week accepts
  `sun mon tue wed thu fri sat` (case-insensitive) for `0–6`. Names are allowed anywhere
  a number is (including as range endpoints: `mon-fri`, `mon-fri/2`). A name in a field
  that has no name table (minute, hour, day-of-month) is an error.
- **Macros.** A single token beginning with `@` replaces the whole expression:
  `@yearly` and `@annually` → `0 0 1 1 *`; `@monthly` → `0 0 1 * *`;
  `@weekly` → `0 0 * * 0`; `@daily` and `@midnight` → `0 0 * * *`;
  `@hourly` → `0 * * * *`. Any other `@token` is an error.
- **The day-of-month / day-of-week rule.** A field is **restricted** iff its source text
  is not exactly `*` (so `*/2`, `1-31`, and `0-6` are all restricted). When **both**
  day-of-month and day-of-week are restricted, a timestamp matches the day if **either**
  the day-of-month **or** the day-of-week matches (Vixie-cron behavior — this is what
  makes `0 0 13 * 5` mean "the 13th, and also every Friday"). Otherwise the day matches
  only when both the day-of-month and the day-of-week fields match (a `*` field always
  matches).

**Time handling:** all timestamps are **UTC**. The base timestamp is entered in RFC 3339
form (`2026-01-01T00:00:00Z`, or with any offset — the result is normalized to UTC).
Fire times are computed at 1-minute granularity (seconds and sub-seconds of both the base
timestamp and the fire times are ignored / always `:00`). **Each individual search for
the next fire time looks ahead at most 5 years past the time it starts from** — and
`NextN` starts each search from the previous fire time it found, so the total span the
returned sequence covers has no fixed bound and can exceed 5 years for a sparse schedule
(`0 0 29 2 *` from 2026 → `2028-02-29`, `2032-02-29`, `2036-02-29`). A search
that finds nothing within 5 years of where it started (e.g. `0 0 30 2 *` — February 30th
never occurs) ends the sequence there.

**Domain parameters** (stated so nothing is guessed):
- Field bounds are exactly as listed above. Day-of-week `7` is a valid input meaning
  Sunday; `8` and above are errors. Day-of-month accepts `1–31` literally with no
  month-length validation at parse time (`0 0 31 2 *` parses fine and simply never fires).
- Step `S` must be an integer `≥ 1`. `*/0`, `1-5/0` are errors.
- **Search horizon:** `SearchHorizonYears = 5`. `Next` scans minute-by-minute from just
  after its own `after` argument up to `after.AddDate(5, 0, 0)` inclusive — the window is
  relative to `Next`'s argument (which `NextN` advances to each fire time), never to
  `NextN`'s original base.
- **Fire-time count:** the UI's "count" field defaults to `10` when blank, and any
  supplied value is clamped to `[1, 50]`. A non-numeric count is an error.
- **Saved names:** an expression is saved under a name matching `^[A-Za-z0-9_-]+$` (one or
  more of ASCII letters, digits, underscore, hyphen). Any other name is an error. The file
  written is `<name>.cron` containing the raw expression text.

**Out of scope** (do not implement):
- A seconds field or a year field. This dialect is exactly 5 fields.
- Quartz extensions: `L` (last), `W` (nearest weekday), `#` (nth weekday), `?`
  (no-specific-value). `?` is not a valid atom and is an error.
- `@reboot` (no meaning without a daemon), and any `@every <duration>` syntax.
- Timezone-aware scheduling, DST handling, or a user-selectable timezone. Everything is
  UTC.
- Non-Gregorian calendars, leap-second handling.
- Reloading a saved expression back into the form, editing, or deleting saved
  expressions. The saved list is view-only.
- Any persistence other than the `.cron` files (no metadata sidecars, no history).
- Concurrency beyond a single mutex guarding writes to `saved/`. This is a one-operator
  tool.

## Architecture

```
cronstudio/
├── go.mod                — module cronstudio, Go 1.22
├── main.go               — var templates *template.Template; func main() only
├── field.go              — monthNames, weekdayNames maps; parseValue, parseAtom, parseField
├── schedule.go           — Schedule, macros map, Parse, Match
├── iterate.go            — SearchHorizonYears constant, Next, NextN
├── describe.go           — FieldSummary, Describe
├── saved.go              — SavedExpr, SavedDir constant, saveMu, SaveExpr, ListSaved
├── handlers.go           — PageView, HandleIndex, HandleParse, HandleSave
├── templates.go          — InitTemplates, RenderPage, RenderResult
└── *_test.go             — one test file per source file above, plus integration_test.go
```

All `.go` files use `package main` at the project root — a single flat package, no
subdirectories (`CERTIFY_MANIFEST` rejects any source file in a subdirectory). `go.mod`
and `do_not_use_this_test.go` are generated automatically by the scaffolding step — do not
list them as SURVEY outputs.

**File assignment rules (strict):**
- `main.go` contains exactly: `var templates *template.Template` and `func main()`.
  Nothing else — no types, no handlers, no constants.
- `field.go` contains: `monthNames`, `weekdayNames` (package-level `map[string]int`
  variables), `parseValue`, `parseAtom`, `parseField`. No `time` import, no HTTP, no
  `Schedule`.
- `schedule.go` contains: `Schedule`, the `macros` map, `Parse`, `Match`. It imports
  `time` (for `Match`). It calls `parseField` — it does **not** re-implement field
  parsing.
- `iterate.go` contains: `const SearchHorizonYears = 5`, `Next`, `NextN`. It imports
  `time`. It calls `Match` — it does **not** re-implement matching.
- `describe.go` contains: `FieldSummary`, `Describe`. It reads a `Schedule`'s mask and
  restricted-flag fields; it does **not** call `Parse` or `Match`.
- `saved.go` contains: `SavedExpr`, `const SavedDir = "saved"`, `var saveMu sync.Mutex`,
  `SaveExpr`, `ListSaved`. It imports `os`, `path/filepath`, `sort`, `strings`. No cron
  logic.
- `handlers.go` contains: `PageView`, `HandleIndex`, `HandleParse`, `HandleSave`. No
  template parsing, no re-implementation of `Parse`/`Next`/`Describe`.
- `templates.go` contains: `InitTemplates`, `RenderPage`, `RenderResult`. No handler
  functions, no type declarations.
- Do NOT put `HandleIndex`, `HandleParse`, or `HandleSave` in `templates.go`.
- Do NOT put `PageView` or `FieldSummary` in `templates.go` — `PageView` belongs in
  `handlers.go`, `FieldSummary` in `describe.go`.
- Do NOT put `parseField`/`parseAtom` logic in `schedule.go` — `schedule.go` calls them.
- Do NOT put `Match` in `iterate.go` or `Next` in `schedule.go`.
- Do NOT put `var templates` anywhere except `main.go`.

## Data Types and Function Signatures

All `.go` source files use `package main`. Module name is `cronstudio`. Requires Go 1.22.
`parseValue`, `parseAtom`, `parseField`, `Match`, `Next` are exercised directly by unit
tests, so they are declared here even though `parseValue`/`parseAtom`/`parseField` are
unexported.

```go
// ---- field.go ----

// monthNames maps the lowercase three-letter month names jan..dec to 1..12.
// weekdayNames maps sun..sat to 0..6.
var monthNames map[string]int
var weekdayNames map[string]int

// parseValue resolves a single field token to a number. It lowercases and trims token,
// looks it up in names (when names is non-nil), else parses it with strconv.Atoi, then
// checks min <= v <= max. Returns a non-nil error for an unknown name, a non-number, or
// an out-of-range number.
func parseValue(token string, min, max int, names map[string]int) (int, error)

// parseAtom parses one atom (one comma-separated piece of a field) into a bitmask where
// bit i is set iff value i is included. Handles *, */S, N, N/S, A-B, A-B/S with N/A/B a
// number or a name from names. See the Behavioral Specification for the exact rules.
// Returns a non-nil error for a bad step, a descending range, or anything parseValue
// rejects.
func parseAtom(atom string, min, max int, names map[string]int) (uint64, error)

// parseField parses a whole field — a comma-separated list of atoms — by OR-ing the
// atom masks together. For the day-of-week field callers pass min=0, max=7; parseField
// folds bit 7 into bit 0 and clears bit 7 before returning. Returns a non-nil error if
// any atom fails to parse, or if the field text is empty.
func parseField(spec string, min, max int, names map[string]int) (uint64, error)

// ---- schedule.go ----

// Schedule is a parsed cron expression. Each mask has bit i set iff value i is allowed
// in that field (minute 0..59, hour 0..23, DayOfMonth 1..31, Month 1..12,
// DayOfWeek 0..6 — Sunday is 0). DomRestricted / DowRestricted record whether the
// day-of-month / day-of-week source field was something other than "*", which selects
// the OR-vs-AND day rule in Match.
type Schedule struct {
    Minute        uint64
    Hour          uint64
    DayOfMonth    uint64
    Month         uint64
    DayOfWeek     uint64
    DomRestricted bool
    DowRestricted bool
}

// macros maps a lowercase "@name" token to its 5-field expansion.
var macros map[string]string

// Parse parses a cron expression into a Schedule. It trims the input, expands a leading
// "@macro" token, splits on whitespace into exactly 5 fields (error otherwise), and
// calls parseField for each with that field's bounds and name table. DomRestricted is
// (field-3 text != "*"); DowRestricted is (field-5 text != "*"). Returns a non-nil
// error, with the failing field named, for any malformed field or an unknown macro.
func Parse(expr string) (Schedule, error)

// Match reports whether t (interpreted in UTC, to minute precision) satisfies s. The
// minute, hour, and month fields must all match. The day rule: if s.DomRestricted &&
// s.DowRestricted, the day matches when the day-of-month OR the day-of-week matches;
// otherwise it matches when both match (a field whose mask covers its whole range,
// e.g. from "*", always matches).
func Match(s Schedule, t time.Time) bool

// ---- iterate.go ----

// SearchHorizonYears bounds how far Next looks ahead.
const SearchHorizonYears = 5

// Next returns the earliest minute-aligned UTC time strictly after `after` that
// Match(s, …) accepts, and true. It truncates `after` to the minute, advances one
// minute at a time, and stops at after.AddDate(SearchHorizonYears, 0, 0) inclusive
// (the window is relative to this call's `after`). If no match is found in that window
// it returns (time.Time{}, false).
func Next(s Schedule, after time.Time) (time.Time, bool)

// NextN calls Next repeatedly, feeding each result back as the new `after`, until it has
// n fire times or Next returns false. Returns the times in ascending order; the slice
// has fewer than n entries (possibly zero) when the horizon is reached first.
func NextN(s Schedule, after time.Time, n int) []time.Time

// ---- describe.go ----

// FieldSummary is a plain-language rendering of a Schedule, one string per field, for
// the template. DomDowOr is true when Match will use the OR rule for the day.
type FieldSummary struct {
    Minute     string
    Hour       string
    DayOfMonth string
    Month      string
    DayOfWeek  string
    DomDowOr   bool
}

// Describe renders s into a FieldSummary. A field whose mask covers its whole range
// renders as "every minute" / "every hour" / "every day-of-month" / "every month" /
// "every day-of-week". Otherwise: minute/hour/day-of-month list their set values as
// decimal numbers joined by ", " in ascending order; month and day-of-week list their
// set values as full English names ("January", "Monday") in ascending numeric order
// (Sunday first for weekdays). DomDowOr = s.DomRestricted && s.DowRestricted.
func Describe(s Schedule) FieldSummary

// ---- saved.go ----

const SavedDir = "saved"

var saveMu sync.Mutex

// SavedExpr is one saved expression: Name is the base filename without ".cron"; Expr is
// the trimmed file contents.
type SavedExpr struct {
    Name string
    Expr string
}

// SaveExpr writes expr to filepath.Join(dir, name+".cron"), holding saveMu for the
// write. name must match ^[A-Za-z0-9_-]+$ (error otherwise). Returns an error if the
// name is invalid or the write fails.
func SaveExpr(name, expr, dir string) error

// ListSaved returns every "*.cron" file in dir as a SavedExpr{Name: <base without
// ".cron">, Expr: <TrimSpace of file contents>}, sorted by Name ascending. Any
// os.ReadDir failure — a missing directory, a dir that is a regular file, a permission
// error — returns (nil, nil); ListSaved never returns a non-nil error in v1 (the error
// in the signature is reserved for future use).
func ListSaved(dir string) ([]SavedExpr, error)

// ---- handlers.go ----

// PageView is the data the templates render. Error != "" means parsing/validation
// failed and Summary/FireTimes are the zero value; the template shows only the error.
type PageView struct {
    Expr      string       // echoed into the expression input
    After     string       // echoed into the base-timestamp input
    Count     int          // echoed into the count input (already clamped)
    Error     string       // "" on success; otherwise the message to show
    Summary   FieldSummary // populated only when Error == ""
    FireTimes []string      // RFC 3339 UTC strings, up to Count; nil when Error != ""
    Exhausted bool         // true when NextN returned fewer than Count times (a search hit its 5-year limit)
    Saved     []SavedExpr  // ListSaved(SavedDir), name-ascending
}

func HandleIndex(w http.ResponseWriter, r *http.Request) // GET /
func HandleParse(w http.ResponseWriter, r *http.Request) // POST /parse
func HandleSave(w http.ResponseWriter, r *http.Request)  // POST /save

// ---- templates.go ----

func InitTemplates() *template.Template

// RenderPage executes the "page" template (full HTML document) with v, writing to w.
func RenderPage(w http.ResponseWriter, v PageView)

// RenderResult executes the "result" template (the #app fragment only) with v,
// writing to w. This is the body of every POST /parse and POST /save response.
func RenderResult(w http.ResponseWriter, v PageView)

// ---- main.go ----

var templates *template.Template
```

### Export signatures

```go
var _ map[string]int = monthNames
var _ map[string]int = weekdayNames
var _ func(string, int, int, map[string]int) (int, error) = parseValue
var _ func(string, int, int, map[string]int) (uint64, error) = parseAtom
var _ func(string, int, int, map[string]int) (uint64, error) = parseField
var _ map[string]string = macros
var _ func(string) (Schedule, error) = Parse
var _ func(Schedule, time.Time) bool = Match
var _ int = SearchHorizonYears
var _ func(Schedule, time.Time) (time.Time, bool) = Next
var _ func(Schedule, time.Time, int) []time.Time = NextN
var _ func(Schedule) FieldSummary = Describe
var _ func(string, string, string) error = SaveExpr
var _ func(string) ([]SavedExpr, error) = ListSaved
var _ string = SavedDir
var _ func(http.ResponseWriter, *http.Request) = HandleIndex
var _ func(http.ResponseWriter, *http.Request) = HandleParse
var _ func(http.ResponseWriter, *http.Request) = HandleSave
var _ func() *template.Template = InitTemplates
var _ func(http.ResponseWriter, PageView) = RenderPage
var _ func(http.ResponseWriter, PageView) = RenderResult
var _ *template.Template = templates
```

## Behavioral Specification

### `field.go` — `parseValue`, `parseAtom`, `parseField`

These three functions form a dependency chain (`parseField` calls `parseAtom` calls
`parseValue`) and are one independently-testable unit — the field grammar.

**`parseValue(token string, min, max int, names map[string]int) (int, error)`**

```
token = strings.ToLower(strings.TrimSpace(token))
if names != nil:
    if v, ok := names[token]; ok:
        return v, nil
v, err := strconv.Atoi(token)
if err != nil:
    return 0, error("not a number or known name: <token>")
if v < min || v > max:
    return 0, error("value <v> out of range [<min>,<max>]")
return v, nil
```

- Name lookup happens **before** the numeric parse, and only when `names != nil`. A name
  passed to a field with no name table (`names == nil` for minute/hour/day-of-month)
  falls through to `strconv.Atoi`, fails, and returns the "not a number or known name"
  error.
- The range check uses the caller's `min`/`max`. For day-of-week the caller passes
  `max = 7`, so `parseValue` accepts `7` here; the fold to `0` happens later in
  `parseField`.

**`parseAtom(atom string, min, max int, names map[string]int) (uint64, error)`**

```
atom = strings.ToLower(strings.TrimSpace(atom))
if atom == "": return 0, error("empty atom")

// 1. split off an optional "/S" suffix
body, step := atom, 1
if i := index of first '/' in atom; i >= 0:
    body = atom[:i]
    step, err = strconv.Atoi(atom[i+1:])
    if err != nil || step < 1: return 0, error("bad step ...")

// 2. resolve body to an inclusive [lo, hi] range
find the first '-' in body at an index >= 1  (so a leading '-' is not a separator —
    there are no negative values, but this keeps the split unambiguous)
if body == "*":
    lo, hi = min, max
else if a '-' was found at index d >= 1:
    a, err = parseValue(body[:d], min, max, names)      ; if err: return err
    b, err = parseValue(body[d+1:], min, max, names)    ; if err: return err
    if a > b: return 0, error("range <body> is descending; ranges do not wrap")
    lo, hi = a, b
else:
    v, err = parseValue(body, min, max, names)          ; if err: return err
    if atom contained a '/':          // N/S form
        lo, hi = v, max
    else:                              // bare N
        lo, hi = v, v

// 3. set every step-th bit in [lo, hi]
mask = 0
for v := lo; v <= hi; v += step:
    mask |= uint64(1) << uint(v)
return mask, nil
```

- **Shift `uint64(1)`, not a bare `1`.** Minute values go up to 59; `1 << 59` with a
  bare `1` is an `int` shift, which overflows on a 32-bit build. Write
  `uint64(1) << uint(v)` (and likewise everywhere a mask bit is set or tested in
  `Match` / `Describe`).

Load-bearing points a natural implementation gets wrong:

- **`*/S` starts at the field minimum, not at `S`.** Minute `*/15` is 0, 15, 30, 45 — the
  `*` expands to `[min, max]` = `[0, 59]` and the step walks from 0. A version that emits
  15, 30, 45 (starting at the step value) is the common error.
- **`N/S` means `N` through the field max, stepping by `S`** — not "just `N`, ignoring the
  step", and not "`N`, `N+S` only". Minute `5/15` → 5, 20, 35, 50.
- **`A-B` is inclusive of `B`** when `B` is reachable by the step. Minute `10-30/5`
  includes 30.
- **A descending range is an error, not a wrap.** `5-1`, and `fri-mon` (Friday is 5,
  Monday is 1), both return an error. Some cron implementations wrap `sat-sun`; this one
  does not.
- **The `-` separator is the first `-` at index ≥ 1.** There are no negative numbers in
  any field, so in practice this is just "the first `-`", but stating "index ≥ 1" keeps a
  malformed leading-`-` token routed to `parseValue` (which rejects it) rather than
  producing an empty left endpoint.
- **The step suffix is split off before the range is examined**, so `A-B/S` and `N/S` and
  `*/S` all share one code path for the step.

**`parseField(spec string, min, max int, names map[string]int) (uint64, error)`**

```
if spec == "": return 0, error("empty field")
mask = 0
for each atom in strings.Split(spec, ","):
    m, err = parseAtom(atom, min, max, names)
    if err != nil: return 0, err
    mask |= m
if max == 7:                     // day-of-week: fold 7 -> 0, then clear bit 7
    if mask & (uint64(1)<<7) != 0: mask |= uint64(1)<<0
    mask &^= uint64(1)<<7
return mask, nil
```

- **The day-of-week fold happens on the assembled mask, once, at the end** — not per
  atom, and not by rewriting `7` to `0` in the text. After `parseField` returns, bit 7 is
  always clear and any Sunday contribution is in bit 0. `Match` reads `t.Weekday()` which
  is `0–6`, so a mask with bit 7 still set would make `* * * * 7` (or `* * * * sat-sun`
  after a hypothetical non-folding parse) never match Sunday.
- `parseField` is called with `max = 7` **only** for day-of-week; every other field
  passes its true max and the fold branch does not run.
- The list is a plain OR — order does not matter, duplicates are harmless.

### `schedule.go` — `Parse`, `Match`

**`Parse(expr string) (Schedule, error)`**

```
expr = strings.TrimSpace(expr)
if expr == "": return Schedule{}, error("empty expression")
if expr starts with "@":
    exp, ok := macros[strings.ToLower(expr)]
    if !ok: return Schedule{}, error("unknown macro <expr>")
    expr = exp
fields := strings.Fields(expr)                 // collapses runs of whitespace
if len(fields) != 5: return Schedule{}, error("expected 5 fields, got <n>")

s.Minute,     err = parseField(fields[0], 0, 59, nil)            ; wrap err as "minute: ..."
s.Hour,       err = parseField(fields[1], 0, 23, nil)            ; "hour: ..."
s.DayOfMonth, err = parseField(fields[2], 1, 31, nil)            ; "day-of-month: ..."
s.Month,      err = parseField(fields[3], 1, 12, monthNames)     ; "month: ..."
s.DayOfWeek,  err = parseField(fields[4], 0, 7,  weekdayNames)   ; "day-of-week: ..."

s.DomRestricted = fields[2] != "*"
s.DowRestricted = fields[4] != "*"
return s, nil
```

- **`DomRestricted` / `DowRestricted` are a string comparison of the raw field text to
  `"*"`, taken before parsing.** `1-31` in the day-of-month field is "restricted" even
  though its mask covers every day; `*/2` is restricted; only the literal single character
  `*` is unrestricted. Deriving the flag from the mask instead (all-bits-set ⇒
  unrestricted) is wrong — it changes the day rule for expressions like `0 0 1-31 * 5`.
- The macro token is matched case-insensitively and must be the entire (trimmed) input —
  `@daily extra` has `strings.HasPrefix(expr, "@")` true but is not in `macros`, so it is
  an "unknown macro" error (it is never split into fields).
- `strings.Fields` handles the "runs of whitespace collapse" and "leading/trailing
  whitespace ignored" requirements; do not `strings.Split(expr, " ")`.
**Canonical error format.** Every error `Parse` returns is one of:

- **Field errors** — prefixed `<field>: `, where `<field>` is exactly one of `minute`,
  `hour`, `day-of-month`, `month`, `day-of-week`. The message after the prefix is the
  unwrapped text from `parseField` / `parseAtom` / `parseValue`:
  - `parseValue`: `value <N> out of range [<min>,<max>]`, or
    `not a number or known name: <token>`.
  - `parseAtom`: `bad step <s>`, or `range <body> is descending; ranges do not wrap`.
  - `parseField`: `empty field`.
- **Whole-expression errors** — no field prefix: `empty expression`;
  `unknown macro <token>`; `expected 5 fields, got <n>`.

**Error-case tests assert a stable substring, never the whole string** — the field-name
prefix (`minute:`), the core phrase (`out of range`, `descending`, `not a number or
known name`, `unknown macro`, `expected 5 fields`), and the offending value where one
applies (`60`, `8`, `@bogus`). The exact rendering of `<token>` / `<body>` / `<N>` and
any surrounding punctuation is not pinned — assert the phrase, not the formatting.

**`Match(s Schedule, t time.Time) bool`**

```
t = t.UTC()
bit := func(n int) uint64 { return uint64(1) << uint(n) }
minuteOK := s.Minute     & bit(t.Minute())        != 0
hourOK   := s.Hour       & bit(t.Hour())          != 0
monthOK  := s.Month      & bit(int(t.Month()))    != 0
domOK    := s.DayOfMonth & bit(t.Day())           != 0
dowOK    := s.DayOfWeek  & bit(int(t.Weekday()))  != 0

if !minuteOK || !hourOK || !monthOK: return false
if s.DomRestricted && s.DowRestricted: return domOK || dowOK
return domOK && dowOK
```

- **The OR only applies when both `DomRestricted` and `DowRestricted` are true.** For
  `0 9 * * 1-5` the day-of-month field is `*`, so `DomRestricted` is false and the day
  rule is `domOK && dowOK`; `domOK` is always true (the `*` mask covers 1–31), so the
  match reduces to "is it a weekday". Applying OR unconditionally would make this match
  every day at 09:00.
- **Conversely**, always AND-ing day-of-month and day-of-week makes `0 0 13 * 5` mean
  "Friday the 13th only" instead of "the 13th, or any Friday".
- `t.Weekday()` returns `time.Sunday == 0` … `time.Saturday == 6`, which lines up with
  the mask directly (Sunday is bit 0). `t.Month()` returns `time.January == 1` …
  `time.December == 12`. `t.Day()` returns `1–31`.
- Seconds and nanoseconds of `t` are never read, so match granularity is one minute.

### `iterate.go` — `Next`, `NextN`

**`Next(s Schedule, after time.Time) (time.Time, bool)`**

```
t     := after.UTC().Truncate(time.Minute).Add(time.Minute)
limit := after.UTC().AddDate(SearchHorizonYears, 0, 0)
for !t.After(limit):
    if Match(s, t): return t, true
    t = t.Add(time.Minute)
return time.Time{}, false
```

- **The result is strictly after `after`.** The first candidate is
  `after` truncated to the minute **plus one minute** — so `Next(s, 09:00:00)` for a
  schedule that fires at 09:00 daily returns the *next* day's 09:00, not the same
  instant. Starting the scan at `after` itself (or at the truncated `after` without the
  `+ 1 minute`) returns `after` when it is already a fire time, which is wrong for
  "next".
- **Candidates are always minute-aligned** (`Truncate(time.Minute)` zeroes seconds and
  below). Every returned time has `:00` seconds.
- The loop bound is `after.AddDate(5, 0, 0)` **inclusive** (`!t.After(limit)`), computed
  from **this call's** `after`. An expression with no fire time in that window returns
  `(time.Time{}, false)` — the zero `time.Time`, which callers detect via the bool, not
  by comparing to a sentinel.
- `time.AddDate` and minute-stepping give correct leap-year behavior for free:
  `0 0 29 2 *` finds Feb 29 of the next leap year.

**`NextN(s Schedule, after time.Time, n int) []time.Time`**

```
out := nil
cur := after
for i := 0; i < n; i++:
    nx, ok := Next(s, cur)
    if !ok: break
    out = append(out, nx)
    cur = nx
return out
```

- Each iteration feeds the previous fire time back in as `after`; because `Next` is
  strictly-after, this always advances. The returned slice is ascending and may be
  shorter than `n` (including empty) when a `Next` call hits its horizon.
- **Each `Next` call's 5-year window is measured from that call's `after`** (the previous
  fire time), so `NextN` as a whole can return times spanning far more than 5 years for a
  sparse schedule — e.g. `NextN("0 0 29 2 *", 2026-01-01T00:00:00Z, 3)` returns
  `2028-02-29`, `2032-02-29`, `2036-02-29` (the last is 10 years past the base).
- `NextN(s, after, 0)` returns `nil` (or an empty slice) — the loop does not run.

### `describe.go` — `Describe`

`Describe(s Schedule) FieldSummary` renders each mask to a string:

- **A mask that covers the field's whole range** renders as the fixed phrase for that
  field: `"every minute"`, `"every hour"`, `"every day-of-month"`, `"every month"`,
  `"every day-of-week"`. This is decided from the mask, not the source text — `*` and
  `0-59` in the minute field both produce `"every minute"`.
- **Otherwise:**
  - minute, hour, day-of-month: the set bits as decimal numbers, ascending, joined by
    `", "` — e.g. `"0, 15, 30, 45"`.
  - month: the set bits as full English month names, ascending by number, joined by
    `", "` — e.g. `"January, June, December"`. Use a `[13]string` table indexed 1–12
    (index 0 unused).
  - day-of-week: the set bits as full English weekday names, ascending by number
    (Sunday = 0 first), joined by `", "` — e.g.
    `"Monday, Tuesday, Wednesday, Thursday, Friday"`. Use a `[7]string` table indexed
    0–6.
- `DomDowOr = s.DomRestricted && s.DowRestricted`.
- `Describe` never returns an error and never reads the clock.

### `saved.go` — `SaveExpr`, `ListSaved`

**`SaveExpr(name, expr, dir string) error`** — validates `name` against
`^[A-Za-z0-9_-]+$` (reject empty or any other character with an error), then, holding
`saveMu`, writes `[]byte(expr)` to `filepath.Join(dir, name+".cron")` with mode `0o644`.
Returns the write error (or the validation error) unwrapped enough for the handler to show
its `.Error()` text. Assume `dir` already exists (`main` creates it).

**`ListSaved(dir string) ([]SavedExpr, error)`** — reads `dir` with `os.ReadDir`. **Any
`os.ReadDir` error — a non-existent `dir`, a `dir` that is a regular file, a permission
error — returns `(nil, nil)`; `ListSaved` never returns a non-nil error in v1** (the
`error` in the signature is reserved for future use, and the consumer discards it anyway
— "a broken saved directory must not break the page"). On success: for every entry whose
name ends in `.cron` (case-sensitive), read the file and append
`SavedExpr{Name: strings.TrimSuffix(entry.Name(), ".cron"), Expr: strings.TrimSpace(string(contents))}`;
a single file that cannot be read is skipped, not fatal. Sort the result by `Name`
ascending (`sort.Slice`).

### `handlers.go` — handlers

All three handlers build a `PageView` the same way — call this **assemble(expr, after,
count string)**:

```
pv.Expr  = expr
pv.After = after
pv.Count = 10
pv.Saved, _ = ListSaved(SavedDir)   // always populated; error always ignored

if count != "":
    n, err := strconv.Atoi(count)
    if err != nil:  pv.Error = "count must be a whole number"; return pv
    pv.Count = clamp(n, 1, 50)      // clamp: n<1 -> 1, n>50 -> 50

s, err := Parse(expr)
if err != nil:  pv.Error = err.Error(); return pv

if after == "":
    pv.Error = "base timestamp is required (RFC 3339, e.g. 2026-01-01T00:00:00Z)"; return pv
base, err := time.Parse(time.RFC3339, after)
if err != nil:  pv.Error = "base timestamp must be RFC 3339, e.g. 2026-01-01T00:00:00Z"; return pv
base = base.UTC()

pv.Summary = Describe(s)
fts := NextN(s, base, pv.Count)
pv.FireTimes = make([]string, len(fts))
for i, t := range fts:  pv.FireTimes[i] = t.Format(time.RFC3339)
pv.Exhausted = len(fts) < pv.Count
return pv
```

- On any error path, `pv.Error` is set, `pv.Summary`/`pv.FireTimes` stay zero, and
  `pv.Saved` is still populated (so the saved list keeps rendering). The `ListSaved` error
  is always ignored.
- `pv.Count` is stored **after clamping**, so the form redisplays the value actually used.

**`HandleIndex`** (`GET /`) — renders the full page for a fixed default:
`RenderPage(w, assemble("*/15 9-17 * * 1-5", "2026-01-01T00:00:00Z", "10"))`. (A fixed
default keeps `GET /` deterministic; the user edits the base timestamp.)

**`HandleParse`** (`POST /parse`) — `r.ParseForm()`; then
`RenderResult(w, assemble(r.PostForm.Get("expr"), r.PostForm.Get("after"), r.PostForm.Get("count")))`.
Always HTTP 200 — a parse error is shown in the fragment, never as an HTTP error status.

**`HandleSave`** (`POST /save`) — `r.ParseForm()`; read `name`, `expr`, `after`, `count`.
First validate the expression: `if _, err := Parse(expr); err != nil` → render the result
fragment via `assemble(expr, after, count)` (its `Error` will carry the parse message) and
return, **without** calling `SaveExpr`. Otherwise call
`SaveExpr(r.PostForm.Get("name"), expr, SavedDir)`; on error, render
`assemble(expr, after, count)` but overwrite `pv.Error` with the save error's text.
On success, render `assemble(expr, after, count)` — the response is the same `#app`
fragment as `/parse`, and its `Saved` list (rebuilt by `assemble` via `ListSaved`) now
includes the new file. Always HTTP 200.

### `templates.go` — templates

`InitTemplates()` parses inline Go string literals (no external `.html` files) into a
`*template.Template` with exactly two named templates, `"page"` and `"result"`. It
`panic`s on a parse error (fixture convention). **No FuncMap is needed** — every value the
templates use is a precomputed field of `PageView` / `FieldSummary` / `SavedExpr`.

- **`"result"`** is the entire dynamic fragment, wrapped in a single
  `<div id="app"> … </div>`. It contains, in order:
  1. A `<form hx-post="/parse" hx-target="#app" hx-swap="outerHTML">` with:
     - `<input name="expr" value="{{.Expr}}">` (text),
     - `<input name="after" value="{{.After}}">` (text),
     - `<input name="count" value="{{.Count}}" type="number">`,
     - `<input name="name" placeholder="save as…">` (text, for the Save action),
     - `<button type="submit">Parse</button>`,
     - `<button type="submit" hx-post="/save">Save</button>` — same enclosing form, so it
       submits the same fields but to `/save`. Both buttons inherit the form's
       `hx-target="#app"` / `hx-swap="outerHTML"`.
  2. `{{if .Error}}<p class="error">{{.Error}}</p>{{else}} … {{end}}` — on the success
     branch:
     - a field breakdown, one line per field, from `.Summary`:
       `<li>Minute: {{.Summary.Minute}}</li>` and likewise `Hour`, `DayOfMonth`, `Month`,
       `DayOfWeek`;
     - `{{if .Summary.DomDowOr}}<p>Day-of-month and day-of-week are both restricted, so a
       day matches when <em>either</em> one matches.</p>{{end}}`;
     - `<ol>{{range .FireTimes}}<li>{{.}}</li>{{end}}</ol>` — the next fire times;
     - `{{if .Exhausted}}<p>Fewer than {{.Count}} found — the schedule has no further
       occurrence within 5 years of the last one shown.</p>{{end}}`.
  3. A saved-list section: `<ul>{{range .Saved}}<li>{{.Name}}: <code>{{.Expr}}</code></li>{{end}}</ul>`.
     Inside this `{{range}}` the loop variable `.` is the `SavedExpr`; it needs no
     `$`-prefixed root access. No other block iterates.
- **`"page"`** is the full document: `<!doctype html>`, a `<head>` with
  `<script src="https://unpkg.com/htmx.org@1.9.12"></script>` and a `<title>`, and a
  `<body>` with an `<h1>` and then `{{template "result" .}}`.

**Swap discipline:** every `POST /parse` and `POST /save` response body is exactly the
`"result"` fragment and replaces `#app` via `hx-swap="outerHTML"`. The fragment
re-declares `<div id="app">` and re-renders the whole form with the submitted values as
the new input values, so the form and the results update together with no JavaScript.
**All dynamic state lives inside `#app`; nothing dynamic renders outside it.**

**Form-body encoding (authoritative — applies to every `httptest` request in the handlers
and integration beads).** The request body is `application/x-www-form-urlencoded`.
`r.ParseForm()` / `r.PostForm.Get` decode `+` as a space and require a literal plus to
arrive as `%2B`. A cron expression contains spaces (the field separators) and can contain
`+` only never — but the base timestamp can contain `+` in a numeric offset
(`2026-01-01T00:00:00+02:00`). **Build every test request body with
`url.Values{"expr": {expr}, "after": {after}, "count": {count}, "name": {name}}.Encode()`**
and set `Content-Type: application/x-www-form-urlencoded`. Do **not** hand-build the body
string, and do **not** add a `+`-preserving shim to the handler — `r.ParseForm()` on a
correctly-encoded body is the contract.

**Assertions on rendered HTML use plain-text fragments only.** `html/template` escapes by
context: the expression echoed into `<input value="0 9 * * 1-5">` and the saved list's
`<code>{{.Expr}}</code>` are fine (no HTML-special characters in this grammar except that
a stray `<`/`>`/`&` in malformed input would be escaped). Assert on stable plain-text
content — a field-summary line like `Monday, Tuesday, Wednesday, Thursday, Friday`, a fire
time like `2026-01-01T09:00:00Z`, the `class="error"` marker, or an `id="app"` — never on
a fragment that could contain an escaped character.

### `main()`

`templates = InitTemplates()`; `os.MkdirAll(SavedDir, 0o755)` (log-fatal on error); build
an `*http.ServeMux` with `GET /{$}` → `HandleIndex`, `POST /parse` → `HandleParse`,
`POST /save` → `HandleSave`; then `http.ListenAndServe(":8080", mux)`.

## Domain-Specific Test Scenarios

A cron field mask, a match result, and a fire-time sequence are all values that look
equally plausible right or wrong when written as bare numbers or timestamps. Every
scenario below is stated with the exact expected value and, where a natural wrong
implementation produces a specific different value, that wrong value is named. All were
computed with a reference implementation
(`<!-- verified: go run scratchpad/cronref  =>  see per-block citations below -->`).

### Required test scenarios for the `field` bead (`parseValue`, `parseAtom`, `parseField`)

Masks are given as the **sorted list of set values**. For day-of-week, the list is after
the 7→0 fold (so only 0–6 appear).

**`parseField` (minute field, min=0 max=59, names=nil):**
- `"*"` → all of `0..59`.
- `"*/15"` → `[0, 15, 30, 45]`. Do NOT get `[15, 30, 45]` (that starts the step at 15
  instead of at the field minimum 0).
- `"5"` → `[5]`.
- `"0,15,30,45"` → `[0, 15, 30, 45]` (same mask as `*/15`).
- `"10-30/5"` → `[10, 15, 20, 25, 30]` (30 is included).
- `"5/15"` → `[5, 20, 35, 50]` (`N/S` == `N` through max, step `S`). Do NOT get `[5]` or
  `[5, 20]`.
- `"15-45/15"` → `[15, 30, 45]`.

**`parseField` (hour field, min=0 max=23):**
- `"*/6"` → `[0, 6, 12, 18]`.
- `"9-17"` → `[9, 10, 11, 12, 13, 14, 15, 16, 17]`.

**`parseField` (day-of-week field, min=0 max=7, names=weekdayNames):**
- `"1-5"` → `[1, 2, 3, 4, 5]`.
- `"mon-fri"` → `[1, 2, 3, 4, 5]` (same as `1-5`).
- `"MON-FRI"` → `[1, 2, 3, 4, 5]` (names are case-insensitive).
- `"mon-fri/2"` → `[1, 3, 5]` (name range plus step).
- `"sun"` → `[0]`.
- `"7"` → `[0]` (7 folds to Sunday; bit 7 is cleared).
- `"0,7"` → `[0]`.

**`parseField` (month field, min=1 max=12, names=monthNames):**
- `"jan,jun,dec"` → `[1, 6, 12]`.
- `"*/3"` → `[1, 4, 7, 10]`.

**`Parse` errors** (each returns a non-nil error, no panic; assert `err.Error()` *contains*
the listed substrings — see "Canonical error format" in the Behavioral Specification):
- `"* * * *"` → contains `expected 5 fields` and `4`.
- `"* * * * * *"` → contains `expected 5 fields` and `6`.
- `""` → contains `empty expression`.
- `"@bogus"` → contains `unknown macro` and `@bogus`.
- `"60 * * * *"` → contains `minute:`, `out of range`, `60`.
- `"* 24 * * *"` → contains `hour:`, `out of range`, `24`.
- `"* * 0 * *"` → contains `day-of-month:`, `out of range`, `0`.
- `"* * * 13 *"` → contains `month:`, `out of range`, `13`.
- `"* * * * 8"` → contains `day-of-week:`, `out of range`, `8` (day-of-week `7` is valid,
  `8` is not).
- `"jan * * * *"` → contains `minute:` and `not a number or known name` (minute has no
  name table).
- `"5-1 * * * *"` → contains `minute:` and `descending`.
- `"*/0 * * * *"` → contains `minute:` and `step`.
- `"? * * * *"` → contains `minute:` and `not a number or known name` (Quartz `?` is not a
  valid atom).

**`parseField` / `parseAtom` direct errors** (called from a unit test, not through `Parse`,
so no `<field>: ` prefix):
- `parseField("", 0, 59, nil)` → contains `empty field`.
- `parseAtom("sat-sun", 0, 7, weekdayNames)` → contains `descending` (Saturday 6 > Sunday 0).
- `parseAtom("*/0", 0, 59, nil)` → contains `step`.

<!-- verified: go run scratchpad/cronref  =>
  "*/15" [0,59] -> [0 15 30 45];  "5/15" -> [5 20 35 50];  "10-30/5" -> [10 15 20 25 30];
  "15-45/15" -> [15 30 45];  "*/6" [0,23] -> [0 6 12 18];  "9-17" -> [9..17];
  "1-5"/"mon-fri"/"MON-FRI" (dow) -> [1 2 3 4 5];  "mon-fri/2" -> [1 3 5];
  "sun"/"7"/"0,7" (dow) -> [0];  "jan,jun,dec" -> [1 6 12];  "*/3" (month) -> [1 4 7 10];
  "0-59" -> allOnes=true;  errors: 4 fields / 6 fields / empty / 60 / 24 / 0 / 13 / 8 /
  jan-in-minute / "5-1" descending / "sat-sun" descending / "*/0" step 0 / "?" not-a-name /
  "@bogus" unknown macro -->

### Required test scenarios for the `schedule` bead (`Parse` restricted flags, `Match`)

**Macro expansions** (`Parse("@x")` then check the mask value lists and the flags):

| macro | minute | hour | day-of-month | month | day-of-week | DomRestricted | DowRestricted |
|---|---|---|---|---|---|---|---|
| `@yearly` / `@annually` | `[0]` | `[0]` | `[1]` | `[1]` | `0..6` | true | false |
| `@monthly` | `[0]` | `[0]` | `[1]` | `1..12` | `0..6` | true | false |
| `@weekly` | `[0]` | `[0]` | `1..31` | `1..12` | `[0]` | false | true |
| `@daily` / `@midnight` | `[0]` | `[0]` | `1..31` | `1..12` | `0..6` | false | false |
| `@hourly` | `[0]` | `0..23` | `1..31` | `1..12` | `0..6` | false | false |

**Restricted-flag detail:** `Parse("0 0 1-31 * *")` has `DomRestricted == true` (the field
text is `"1-31"`, not `"*"`), even though the day-of-month mask covers every day.
`Parse("*/2 * * * *")` has `DomRestricted == false` and `DowRestricted == false` (both are
literally `*`).

**`Match`** (all timestamps UTC, RFC 3339):
- `Parse("0 9 * * 1-5")`, `Match` at `2026-01-05T09:00:00Z` (a Monday) → **true**.
- same schedule at `2026-01-05T09:01:00Z` → **false** (minute).
- same schedule at `2026-01-03T09:00:00Z` (a Saturday) → **false** (day-of-week). Note
  day-of-month is `*` here so the rule is AND; `domOK` is trivially true and the result is
  purely "is it a weekday".
- `Parse("*/15 * * * *")` at `2026-01-05T13:30:00Z` → **true**; at `13:31:00Z` → **false**.
- `Parse("0 0 13 * 5")` (both day fields restricted → OR rule):
  - `2026-01-13T00:00:00Z` (Tuesday) → **true** (day-of-month 13 matches; day-of-week
    does not; OR).
  - `2026-01-02T00:00:00Z` (Friday) → **true** (day-of-week matches; day-of-month does
    not; OR).
  - `2026-02-13T00:00:00Z` (Friday) → **true** (both match).
  - A natural AND-only implementation returns **false** for the first two.
- `Parse("0 0 * * 0")` at `2026-01-04T00:00:00Z` (a Sunday) → **true**.

<!-- verified: go run scratchpad/cronref  =>
  @yearly minute=[0] hour=[0] dom=[1] month=[1] dow=[0..6] domR=true dowR=false;
  @weekly dom=[1..31] month=[1..12] dow=[0] domR=false dowR=true;
  @daily domR=false dowR=false;  @hourly hour=[0..23] domR=false dowR=false;
  Parse("*/2 * * * *") domR=false dowR=false;
  Match "0 9 * * 1-5" @ Mon 09:00 -> true; @ 09:01 -> false; @ Sat 09:00 -> false;
  Match "*/15 * * * *" @ 13:30 -> true; @ 13:31 -> false;
  Match "0 0 13 * 5" @ 2026-01-13(Tue) -> true; @ 2026-01-02(Fri) -> true; @ 2026-02-13(Fri) -> true;
  Match "0 0 * * 0" @ 2026-01-04(Sun) -> true;
  weekday sanity: 2026-01-13 Tue, 2026-01-02 Fri, 2026-02-13 Fri, 2026-01-04 Sun,
  2026-01-01 Thu, 2026-01-05 Mon -->

### Required test scenarios for the `iterate` bead (`Next`, `NextN`)

All base timestamps and results are UTC RFC 3339. `2026-01-01T00:00:00Z` is a **Thursday**.

- `NextN(Parse("0 9 * * 1-5"), 2026-01-01T00:00:00Z, 6)` →
  ```
  2026-01-01T09:00:00Z  (Thu)
  2026-01-02T09:00:00Z  (Fri)
  2026-01-05T09:00:00Z  (Mon — Sat/Sun skipped)
  2026-01-06T09:00:00Z  (Tue)
  2026-01-07T09:00:00Z  (Wed)
  2026-01-08T09:00:00Z  (Thu)
  ```
- `NextN(Parse("*/15 * * * *"), 2026-01-01T00:00:00Z, 5)` →
  `00:15, 00:30, 00:45, 01:00, 01:15` on `2026-01-01`.
- `NextN(Parse("0 0 29 2 *"), 2026-01-01T00:00:00Z, 3)` →
  `2028-02-29T00:00:00Z, 2032-02-29T00:00:00Z, 2036-02-29T00:00:00Z` (leap years only —
  minute-stepping over `time.Time` gets this right; 2026, 2027 have no Feb 29). Note the
  third result is `2036` — 10 years past the base: each `Next` call's 5-year window is
  measured from the previous result (2028, then 2032), not from the base, so the total
  span `NextN` covers has no fixed bound.
- `Next(Parse("0 0 30 2 *"), 2026-01-01T00:00:00Z)` → `(time.Time{}, false)` — February
  30th never occurs within 5 years of the base. `NextN(..., 3)` → empty slice.
- `NextN(Parse("0 0 13 * 5"), 2026-01-01T00:00:00Z, 4)` →
  ```
  2026-01-02T00:00:00Z  (Fri — day-of-week)
  2026-01-09T00:00:00Z  (Fri — day-of-week)
  2026-01-13T00:00:00Z  (Tue — day-of-month 13)
  2026-01-16T00:00:00Z  (Fri — day-of-week)
  ```
  This sequence is the clearest check of the OR rule: it interleaves "every Friday" with
  "the 13th".
- `NextN(Parse("30 3 * * 1"), 2026-01-01T00:00:00Z, 4)` →
  `2026-01-05T03:30:00Z, 2026-01-12T03:30:00Z, 2026-01-19T03:30:00Z, 2026-01-26T03:30:00Z`
  (Mondays).
- `NextN(Parse("15 14 1 * *"), 2026-01-01T00:00:00Z, 3)` →
  `2026-01-01T14:15:00Z, 2026-02-01T14:15:00Z, 2026-03-01T14:15:00Z`.
- **Strictly-after:** `Next(Parse("0 9 * * *"), 2026-01-01T09:00:00Z)` →
  `2026-01-02T09:00:00Z` (not `2026-01-01T09:00:00Z` — the result must be *after* the
  base). `Next(Parse("0 9 * * *"), 2026-01-01T08:59:00Z)` → `2026-01-01T09:00:00Z`.

<!-- verified: go run scratchpad/cronref  =>
  "0 9 * * 1-5" after 2026-01-01 (n=6) -> [2026-01-01T09:00Z 2026-01-02T09:00Z 2026-01-05T09:00Z 2026-01-06T09:00Z 2026-01-07T09:00Z 2026-01-08T09:00Z];
  "*/15 * * * *" (n=5) -> [00:15 00:30 00:45 01:00 01:15] on 2026-01-01;
  "0 0 29 2 *" (n=3) -> [2028-02-29 2032-02-29 2036-02-29];
  "0 0 30 2 *" -> Next returns zero-time,false;  NextN empty;
  "0 0 13 * 5" (n=4) -> [2026-01-02 2026-01-09 2026-01-13 2026-01-16];
  "30 3 * * 1" (n=4) -> [2026-01-05 2026-01-12 2026-01-19 2026-01-26] at 03:30;
  "15 14 1 * *" (n=3) -> [2026-01-01 2026-02-01 2026-03-01] at 14:15;
  Next("0 9 * * *", exactly 2026-01-01T09:00Z) -> 2026-01-02T09:00:00Z;
  Next("0 9 * * *", 2026-01-01T08:59Z) -> 2026-01-01T09:00:00Z -->

### Required test scenarios for the `describe` bead (`Describe`)

- `Describe(Parse("*/15 9-17 * * 1-5"))` →
  `FieldSummary{Minute: "0, 15, 30, 45", Hour: "9, 10, 11, 12, 13, 14, 15, 16, 17",
  DayOfMonth: "every day-of-month", Month: "every month",
  DayOfWeek: "Monday, Tuesday, Wednesday, Thursday, Friday", DomDowOr: false}`.
- `Describe(Parse("0 0 1 1 *"))` →
  `{Minute: "0", Hour: "0", DayOfMonth: "1", Month: "January",
  DayOfWeek: "every day-of-week", DomDowOr: false}`.
- `Describe(Parse("0 0 13 * 5"))` →
  `{Minute: "0", Hour: "0", DayOfMonth: "13", Month: "every month",
  DayOfWeek: "Friday", DomDowOr: true}` (both day fields restricted).
- `Describe(Parse("* * * * *"))` →
  `{Minute: "every minute", Hour: "every hour", DayOfMonth: "every day-of-month",
  Month: "every month", DayOfWeek: "every day-of-week", DomDowOr: false}`.
- `Describe(Parse("0 0,12 * jan,jun,dec *"))` →
  `{Minute: "0", Hour: "0, 12", DayOfMonth: "every day-of-month",
  Month: "January, June, December", DayOfWeek: "every day-of-week", DomDowOr: false}`.

<!-- verified: go run scratchpad/cronref  =>
  "*/15 9-17 * * 1-5" -> Minute "0, 15, 30, 45"  Hour "9, 10, 11, 12, 13, 14, 15, 16, 17"
    DoM "every day-of-month"  Month "every month"  DoW "Monday, Tuesday, Wednesday, Thursday, Friday"  DomDowOr false;
  "0 0 1 1 *" -> Minute "0" Hour "0" DoM "1" Month "January" DoW "every day-of-week" DomDowOr false;
  "0 0 13 * 5" -> DoM "13" Month "every month" DoW "Friday" DomDowOr true;
  "* * * * *" -> all "every ..." DomDowOr false;
  "0 0,12 * jan,jun,dec *" -> Hour "0, 12" Month "January, June, December" -->

### Required test scenarios for the `saved` bead (`SaveExpr`, `ListSaved`)

Every scenario uses `dir := t.TempDir()` — the test must not read or write the process
working directory.

- `SaveExpr("weekday-mornings", "0 9 * * 1-5", dir)` → `nil`, and `dir/weekday-mornings.cron`
  exists with contents exactly `0 9 * * 1-5`.
- `SaveExpr("bad name!", "0 9 * * 1-5", dir)` → non-nil error (the name has a space and a
  `!`, failing `^[A-Za-z0-9_-]+$`); no file is written.
- After writing `b.cron` (contents `0 0 * * 0`) then `a.cron` (contents `* * * * *`) and
  a non-`.cron` file `notes.txt` into `dir`, `ListSaved(dir)` →
  `[]SavedExpr{{Name: "a", Expr: "* * * * *"}, {Name: "b", Expr: "0 0 * * 0"}}`
  (Name-ascending; `notes.txt` ignored; `Expr` is the trimmed file contents).
- `ListSaved(<a path that does not exist>)` → `(nil, nil)`.
- `ListSaved(<a path that is a regular file, not a directory>)` → `(nil, nil)` — every
  `os.ReadDir` failure is swallowed; `ListSaved` never returns a non-nil error.

<!-- verified: go run scratchpad/cronref  =>
  SaveExpr("weekday-mornings", "0 9 * * 1-5", dir) -> nil, file contents "0 9 * * 1-5";
  SaveExpr("bad name!", …) -> err, no file;
  ListSaved(dir with a.cron/b.cron/notes.txt) -> [{a,"* * * * *"},{b,"0 0 * * 0"}] (notes.txt ignored, Name-ascending);
  ListSaved(missing) -> (nil,nil);  ListSaved(regular file) -> (nil,nil) -->

### Required test scenarios for the `integration` bead

Using `httptest.NewServer` with the real mux, `POST /parse` with body built by
`url.Values{"expr": {"0 9 * * 1-5"}, "after": {"2026-01-01T00:00:00Z"}, "count": {"3"}}.Encode()`
and `Content-Type: application/x-www-form-urlencoded` → HTTP 200, and the response body
contains all of: `id="app"`, the substring
`Monday, Tuesday, Wednesday, Thursday, Friday`, and the three fire-time substrings
`2026-01-01T09:00:00Z`, `2026-01-02T09:00:00Z`, `2026-01-05T09:00:00Z` in that order.

## Cross-Bead Contracts

### field → schedule (protocol)

- **type**: protocol
- **producer**: field (`field.go`)
- **consumer**: schedule (`schedule.go`)
- **interface**: `func parseField(spec string, min, max int, names map[string]int) (uint64, error)`, `var monthNames map[string]int`, `var weekdayNames map[string]int`
- **notes**: `Parse` calls `parseField` exactly five times, in field order, with these
  arguments: minute `(fields[0], 0, 59, nil)`, hour `(fields[1], 0, 23, nil)`,
  day-of-month `(fields[2], 1, 31, nil)`, month `(fields[3], 1, 12, monthNames)`,
  day-of-week `(fields[4], 0, 7, weekdayNames)`. Only day-of-week passes `max = 7`, which
  triggers the internal 7→0 fold — `Parse` does not do the fold itself. Same package, no
  import. `field.go` is decomposed before `schedule.go`.

### schedule → iterate (data-shape)

- **type**: data-shape
- **producer**: schedule (`schedule.go`)
- **consumer**: iterate (`iterate.go`)
- **interface**: `Schedule{Minute, Hour, DayOfMonth, Month, DayOfWeek uint64; DomRestricted, DowRestricted bool}` and `func Match(s Schedule, t time.Time) bool`
- **notes**: `Next` builds candidate `time.Time` values (UTC, minute-aligned, strictly
  after `after`) and calls `Match(s, candidate)` for each; it does **not** read the mask
  fields directly and does not re-implement the day-of-month/day-of-week rule. `Match`
  interprets its `time.Time` in UTC internally, so `Next` passing UTC times is consistent.
  Same package, no import; `schedule.go` is decomposed before `iterate.go`.

### schedule → describe (data-shape)

- **type**: data-shape
- **producer**: schedule (`schedule.go`)
- **consumer**: describe (`describe.go`)
- **interface**: `Schedule{Minute, Hour, DayOfMonth, Month, DayOfWeek uint64; DomRestricted, DowRestricted bool}`
- **notes**: `Describe` reads the five mask fields and the two restricted flags; it never
  calls `Parse` or `Match`. `DomDowOr` in its output is exactly
  `s.DomRestricted && s.DowRestricted`. Same package, no import.

### schedule → handlers (protocol)

- **type**: protocol
- **producer**: schedule (`schedule.go`)
- **consumer**: handlers (`handlers.go`)
- **interface**: `func Parse(expr string) (Schedule, error)`
- **notes**: every handler's `assemble` helper calls `Parse(expr)` first. A non-nil error
  is shown to the user as `pv.Error = err.Error()` and the handler still returns HTTP 200
  with the result fragment (or, for `GET /`, the full page) — a parse error is **never**
  an HTTP 4xx/5xx. `HandleSave` calls `Parse` to validate before it calls `SaveExpr`, and
  skips the save entirely on a parse error.

### iterate → handlers (protocol)

- **type**: protocol
- **producer**: iterate (`iterate.go`)
- **consumer**: handlers (`handlers.go`)
- **interface**: `func NextN(s Schedule, after time.Time, n int) []time.Time`
- **notes**: `assemble` calls `NextN(s, base, pv.Count)` where `base` is
  `time.Parse(time.RFC3339, after)` normalized to UTC and `pv.Count` is the count field
  clamped to `[1, 50]` (default 10 when blank). `assemble` formats each result with
  `t.Format(time.RFC3339)` into `pv.FireTimes` and sets
  `pv.Exhausted = len(result) < pv.Count`. A returned slice shorter than `pv.Count`
  (including empty) is normal and not an error.

### describe → handlers (data-shape)

- **type**: data-shape
- **producer**: describe (`describe.go`)
- **consumer**: handlers (`handlers.go`)
- **interface**: `FieldSummary{Minute, Hour, DayOfMonth, Month, DayOfWeek string; DomDowOr bool}` and `func Describe(s Schedule) FieldSummary`
- **notes**: `assemble` sets `pv.Summary = Describe(s)` only on the success branch; on an
  error branch `pv.Summary` stays the zero `FieldSummary` and the template does not render
  it (guarded by `{{if .Error}}`).

### saved → handlers (protocol)

- **type**: protocol
- **producer**: saved (`saved.go`)
- **consumer**: handlers (`handlers.go`)
- **interface**: `func SaveExpr(name, expr, dir string) error`, `func ListSaved(dir string) ([]SavedExpr, error)`, `SavedExpr{Name, Expr string}`, `const SavedDir = "saved"`
- **notes**: `HandleSave` calls `SaveExpr(name, expr, SavedDir)` only after `Parse(expr)`
  succeeds; a non-nil `SaveExpr` error becomes `pv.Error` (still HTTP 200, fragment
  rendered, no save). Every handler's `assemble` calls `ListSaved(SavedDir)` to populate
  `pv.Saved` and **ignores its error** (a broken saved directory must not break the
  page — treat as empty). After a successful save, the fragment's `Saved` list includes
  the new entry because `assemble` re-reads the directory.

### handlers → templates (data-shape)

- **type**: data-shape
- **producer**: handlers (`handlers.go` — assembles `PageView`)
- **consumer**: templates (`templates.go`)
- **interface**: `PageView{Expr, After string; Count int; Error string; Summary FieldSummary; FireTimes []string; Exhausted bool; Saved []SavedExpr}`, `FieldSummary{Minute, Hour, DayOfMonth, Month, DayOfWeek string; DomDowOr bool}`, `SavedExpr{Name, Expr string}`
- **notes**: No FuncMap — every value is a precomputed field. Both `"page"` and
  `"result"` consume the same `PageView`; `"page"` renders `"result"` via
  `{{template "result" .}}`. The only `{{range}}` blocks are over `.FireTimes` (elements
  are plain strings) and `.Saved` (elements are `SavedExpr`); neither needs `$`-prefixed
  root access because no field outside the loop element is referenced inside the loop. The
  `{{if .Error}} … {{else}} … {{end}}` and `{{if .Summary.DomDowOr}}` / `{{if .Exhausted}}`
  blocks are all at the top level, so `.Error` / `.Summary` / `.Exhausted` resolve against
  the root `PageView`. All dynamic content renders **inside** `<div id="app">`, the
  `hx-target`; the `POST /parse` and `POST /save` responses are the `"result"` fragment
  only and replace `#app` via `hx-swap="outerHTML"`. httptest bodies are built with
  `url.Values{...}.Encode()` and read with `r.ParseForm()` — no hand-rolled
  `+`-preserving parser. Assertions on the rendered body use plain-text fragments only
  (`id="app"`, `class="error"`, a weekday-name list, an RFC 3339 timestamp) — nothing
  `html/template` escapes.

## Decomposition Notes

**Bead dependency order (do not reorder):**

1. **field** — `monthNames`, `weekdayNames`, `parseValue`, `parseAtom`, `parseField`. No
   dependencies. Owns `field.go`.
2. **schedule** — `Schedule`, `macros`, `Parse`, `Match`. Depends on bead 1. Owns
   `schedule.go`.
3. **iterate** — `SearchHorizonYears`, `Next`, `NextN`. Depends on bead 2. Owns
   `iterate.go`.
4. **describe** — `FieldSummary`, `Describe`. Depends on bead 2. Owns `describe.go`.
5. **saved** — `SavedExpr`, `SavedDir`, `saveMu`, `SaveExpr`, `ListSaved`. No dependency
   on any cron bead. Owns `saved.go`. sizing rationale: two small file-I/O functions plus
   a type and two package vars; no shared logic between `SaveExpr` and `ListSaved`.
6. **templates** — `InitTemplates`, `RenderPage`, `RenderResult`. Depends on bead 7's
   `PageView` / `FieldSummary` / `SavedExpr` types **only** for the data shape — decompose
   templates **before** handlers so the handler bead's httptest assertions run against
   real template output. Owns `templates.go`. If SURVEY places `PageView`/`FieldSummary`
   such that a template↔handler type cycle appears, `PageView` belongs in `handlers.go`
   and `FieldSummary` in `describe.go`; the template bead references them across the file
   boundary in the same package (no import).
7. **handlers** — `PageView`, `HandleIndex`, `HandleParse`, `HandleSave`. Depends on beads
   3, 4, 5, 6. Owns `handlers.go`. sizing rationale: three thin handler wrappers over one
   `assemble` helper; no per-handler branching beyond which form action is taken. Every
   handler bead exit criterion is an `httptest` smoke test (not `go build`): at minimum
   one request/response per handler asserting status + one structural property. Required
   extra assertions: `POST /parse` with `expr=0 9 * * 1-5`, `after=2026-01-01T00:00:00Z`,
   `count=3` returns a body containing `2026-01-01T09:00:00Z` and
   `Monday, Tuesday, Wednesday, Thursday, Friday`; `POST /parse` with `expr=nonsense`
   returns HTTP 200 with a body containing `class="error"`. All request bodies built with
   `url.Values{...}.Encode()`. **Do not exercise `POST /save` in this bead at all** — a
   handler-level save test would write a file into the process working directory
   (`SavedDir` is a `const`, not injectable) or fail because `saved/` does not exist. The
   `SaveExpr` / `ListSaved` write path is covered by the `saved` bead against an explicit
   temp dir; the `HandleSave` wiring is verified only that `main` registers the route.
8. **main** — `var templates`, `func main()`. Wires the mux
   (`GET /{$}`, `POST /parse`, `POST /save`). Depends on bead 7. Owns `main.go`.
9. **integration** — one bounded `httptest` scenario (below).

**Integration bead — one bounded scenario:** stand up `httptest.NewServer` with the real
mux; `POST /parse` with a body built by
`url.Values{"expr": {"0 9 * * 1-5"}, "after": {"2026-01-01T00:00:00Z"}, "count": {"3"}}.Encode()`
and `Content-Type: application/x-www-form-urlencoded`; assert HTTP 200 and that the
response body contains, in order, `2026-01-01T09:00:00Z`, `2026-01-02T09:00:00Z`,
`2026-01-05T09:00:00Z`, and also contains `Monday, Tuesday, Wednesday, Thursday, Friday`
and `id="app"`. Do **not** also test `/save` or the error path here — those are covered by
the handlers bead.

**Must not bind to a fixed port in any test** — use `httptest` throughout.

**Form-body encoding:** every `httptest` request body in the handlers and integration
beads is built with `url.Values{...}.Encode()` and read by the handler via
`r.ParseForm()` / `r.PostForm.Get`. Do not hand-build a body string; do not add a
`+`-preserving parser to a handler. (An RFC 3339 `after` value with a numeric offset
contains `+`, which a hand-built body would corrupt to a space.)

**HTML assertions:** assertions on a rendered response body use only plain-text fragments
with no HTML-special characters — `id="app"`, `class="error"`, a full weekday-name list, a
`YYYY-MM-DDTHH:MM:SSZ` timestamp. Never assert on a fragment that `html/template` would
escape.

**Pins (verbatim worked values — carry into the named bead's spec exactly, do not
re-derive or paraphrase):**

- **Pin — `field` bead, `parseField` masks:** as sorted value lists — minute `"*/15"` →
  `[0, 15, 30, 45]` (NOT `[15, 30, 45]`); minute `"5/15"` → `[5, 20, 35, 50]`; minute
  `"10-30/5"` → `[10, 15, 20, 25, 30]` (30 included); hour `"*/6"` → `[0, 6, 12, 18]`;
  hour `"9-17"` → `[9, 10, 11, 12, 13, 14, 15, 16, 17]`; day-of-week `"1-5"` and
  `"mon-fri"` and `"MON-FRI"` → `[1, 2, 3, 4, 5]`; day-of-week `"mon-fri/2"` →
  `[1, 3, 5]`; day-of-week `"7"` and `"sun"` and `"0,7"` → `[0]`; month `"jan,jun,dec"` →
  `[1, 6, 12]`; month `"*/3"` → `[1, 4, 7, 10]`.
- **Pin — `field` bead, atom rule shapes:** `*/S` steps from the field **minimum**
  (minute `*/15` starts at 0). `N/S` means `N` through the field **maximum** stepping by
  `S` (minute `5/15` → 5, 20, 35, 50). `A-B` is inclusive of `B`. A descending range
  (`5-1`, `fri-mon`, `sat-sun`) is an **error**, not a wrap. Step `0` is an error.
- **Pin — `field` bead, day-of-week 7→0 fold and error bounds:** `parseField` with
  `max == 7` folds bit 7 into bit 0 and clears bit 7, so results only ever have bits
  0–6. Day-of-week `8` is out of range `[0,7]` and is an error; day-of-week `7` is valid
  (Sunday). A month/weekday name in a field with no name table (minute/hour/day-of-month)
  is a "not a number or known name" error (`"jan * * * *"` fails on the minute field).
  Quartz `?` is not a valid atom. Field errors from `Parse` are prefixed `<field>: `
  (`minute:`, …, `day-of-week:`); error-case tests assert a substring — the prefix plus
  the core phrase (`out of range`, `descending`, `not a number or known name`, `step`) —
  not the whole string.
- **Pin — `schedule` bead, restricted flags:** `DomRestricted` is `(day-of-month field
  text != "*")` and `DowRestricted` is `(day-of-week field text != "*")`, decided from
  the raw text before parsing — `"1-31"` and `"*/2"` are **restricted**; only the literal
  `"*"` is not. `Parse("*/2 * * * *")` → both flags false. Macro flags:
  `@weekly` → `DomRestricted=false, DowRestricted=true`;
  `@monthly`/`@yearly` → `DomRestricted=true, DowRestricted=false`;
  `@daily`/`@hourly` → both false.
- **Pin — `schedule` bead, `Match` day rule:** when `DomRestricted && DowRestricted`, the
  day matches if day-of-month **OR** day-of-week matches; otherwise if **both** match (a
  `*` field always matches). `Parse("0 0 13 * 5")` matches `2026-01-13T00:00:00Z`
  (Tuesday — 13th) and `2026-01-02T00:00:00Z` (Friday — weekday) and
  `2026-02-13T00:00:00Z` (both). `Parse("0 9 * * 1-5")` does **not** match a Saturday at
  09:00 (day-of-month is `*` → AND rule → weekday required).
- **Pin — `iterate` bead, `NextN` sequences** (all UTC RFC 3339; base
  `2026-01-01T00:00:00Z` is a Thursday):
  `NextN("0 9 * * 1-5", base, 6)` → `2026-01-01T09:00:00Z`, `2026-01-02T09:00:00Z`,
  `2026-01-05T09:00:00Z`, `2026-01-06T09:00:00Z`, `2026-01-07T09:00:00Z`,
  `2026-01-08T09:00:00Z`.
  `NextN("*/15 * * * *", base, 5)` → `2026-01-01T00:15:00Z`, `00:30`, `00:45`,
  `2026-01-01T01:00:00Z`, `01:15`.
  `NextN("0 0 29 2 *", base, 3)` → `2028-02-29T00:00:00Z`, `2032-02-29T00:00:00Z`,
  `2036-02-29T00:00:00Z` (each `Next` call's 5-year window is measured from the previous
  result, so the last value is past `base` + 5 years).
  `NextN("0 0 13 * 5", base, 4)` → `2026-01-02T00:00:00Z`, `2026-01-09T00:00:00Z`,
  `2026-01-13T00:00:00Z`, `2026-01-16T00:00:00Z`.
  `NextN("30 3 * * 1", base, 4)` → `2026-01-05T03:30:00Z`, `2026-01-12T03:30:00Z`,
  `2026-01-19T03:30:00Z`, `2026-01-26T03:30:00Z`.
- **Pin — `iterate` bead, `Next` boundary behavior:** the result is **strictly after**
  `after` — `Next("0 9 * * *", 2026-01-01T09:00:00Z)` → `2026-01-02T09:00:00Z`, while
  `Next("0 9 * * *", 2026-01-01T08:59:00Z)` → `2026-01-01T09:00:00Z`. Results are
  minute-aligned (`:00` seconds). `Next("0 0 30 2 *", base)` → `(time.Time{}, false)`
  (February 30th never occurs within the 5-year horizon); `NextN` of it is an empty
  slice.
- **Pin — `describe` bead, `Describe` output:** a field whose mask covers its whole range
  renders as `"every minute"` / `"every hour"` / `"every day-of-month"` / `"every month"`
  / `"every day-of-week"` — decided from the mask (so `*` and `0-59` both give
  `"every minute"`). Otherwise minute/hour/day-of-month are the set values as decimals
  joined by `", "`; month and day-of-week are full English names joined by `", "`
  (ascending by number, Sunday first). `Describe(Parse("*/15 9-17 * * 1-5"))` →
  `Minute "0, 15, 30, 45"`, `Hour "9, 10, 11, 12, 13, 14, 15, 16, 17"`,
  `DayOfMonth "every day-of-month"`, `Month "every month"`,
  `DayOfWeek "Monday, Tuesday, Wednesday, Thursday, Friday"`, `DomDowOr false`.
  `Describe(Parse("0 0 13 * 5"))` has `DayOfWeek "Friday"` and `DomDowOr true`.
- **Pin — `saved` bead, `SaveExpr` / `ListSaved`** (all against `dir := t.TempDir()`):
  `SaveExpr("weekday-mornings", "0 9 * * 1-5", dir)` → `nil`, file `weekday-mornings.cron`
  with contents exactly `0 9 * * 1-5`. A name not matching `^[A-Za-z0-9_-]+$`
  (`"bad name!"`) → error, no file. `ListSaved` returns `SavedExpr{Name, Expr}` for every
  `*.cron` file, `Name` = base without `.cron`, `Expr` = trimmed contents, sorted by
  `Name` ascending, non-`.cron` files ignored. **Every** `os.ReadDir` failure — missing
  dir, dir is a regular file, permission error — returns `(nil, nil)`; `ListSaved` never
  returns a non-nil error in v1.
- **Pin — `integration` bead:** `POST /parse` with body
  `url.Values{"expr": {"0 9 * * 1-5"}, "after": {"2026-01-01T00:00:00Z"}, "count": {"3"}}.Encode()`
  → HTTP 200, body contains `2026-01-01T09:00:00Z`, `2026-01-02T09:00:00Z`,
  `2026-01-05T09:00:00Z` (in that order), plus `Monday, Tuesday, Wednesday, Thursday, Friday`
  and `id="app"`.

## Open Questions

None outstanding.

<!-- Resolved 2026-09-08 (draft-design-doc interactive pass):
  1. Base timestamp default: GET / renders with a FIXED base (2026-01-01T00:00:00Z) so
     the initial render is deterministic; the user edits the field. (kept as drafted)
  2. Load a saved expression back into the form: OUT OF SCOPE for v1 — the saved list is
     view-only (already stated in Overview → out of scope). (kept as drafted)
  3. N/S atom form: KEEP the permissive interpretation (5/15 == 5-<field max>/15 →
     5, 20, 35, 50), matching Vixie cron. (kept as drafted) -->

<!-- Resolved 2026-09-08 (check-design-doc — independent judgment pass raised 4, mechanical raised 0):
  1. [class 13] Search horizon: SLIDING — each Next call's 5-year window is measured from
     its own `after`; NextN can span >5y. Overview + Domain params + template message +
     iterate spec/pin reworded to say so. (0 0 29 2 * -> 2028/2032/2036 pin stands.)
  2. [class 13] Error-message format: canonicalized to "<field>: <message>" stated once
     in the Behavioral Spec; every error scenario rewritten to assert STABLE SUBSTRINGS
     (field prefix + core phrase + offending value), not whole strings.
  3. [class 14] POST /save test: DROPPED from the handlers bead (SavedDir is a const,
     handler-level save test pollutes cwd or fails) — SaveExpr/ListSaved covered by a new
     `saved` bead scenario block + pin, both against t.TempDir(). fractalviz precedent.
  4. [class 11/5] ListSaved error contract: SWALLOW — every os.ReadDir failure returns
     (nil, nil); the error return is reserved and always nil in v1. Spec + signature
     comment + pin + a scenario for the dir-is-a-file case all updated. -->

