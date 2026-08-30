# Haiku Generator — Design Document

> Produced by the `draft-design-doc` skill from `haiku-generator-prose.md`, then through
> `check-design-doc` (mechanical scan + independent judgment pass + sign-off,
> 2026-08-29). All open questions and all 8 judgment findings resolved — see
> `## Resolved decisions`. Worked examples verified with throwaway scripts (reproduced
> in `haiku-generator-drafting-log.md`) plus one live `/api/generate` call.
> **Cleared for `new-project`** — not yet run.

## Overview

A single-user Go + HTMX web app that generates a 5-7-5 haiku from a theme, a required
keyword, and an optional nature element by calling a locally connected Ollama model.
Generated haiku are stored in SQLite and listed on a history page. The user can "email"
a haiku to a named recipient — which means writing an RFC 5322 `.eml` file into a local
outbox directory (no real SMTP). Every email is logged.

**Runtime model:** long-running HTTP server. `main()` parses flags, opens the store,
starts the server. One process, one user, no auth.

**Domain parameters** (all via flag, each with an env fallback; flag wins):

| flag | env fallback | default | meaning |
|---|---|---|---|
| `--addr` | `HAIKU_ADDR` | `localhost:8080` | HTTP listen address |
| `--ollama` | `OLLAMA_HOST` | `http://localhost:11434` | Ollama base URL (no trailing slash) |
| `--model` | `HAIKU_MODEL` | `gemma4:31b` | Ollama model name |
| `--db` | `HAIKU_DB` | `haiku.db` | SQLite file path |
| `--outbox` | `HAIKU_OUTBOX` | `./outbox` | directory for `.eml` files; created if missing |

Other fixed constants:

- **Sender address:** the literal string `lehmann314159@gmail.com` — a package-level
  `const senderAddr`, used directly, not configurable.
- **Ollama request:** `POST {ollama}/api/generate` with body
  `{"model": <model>, "prompt": <prompt>, "stream": false}`. HTTP client timeout
  **60 seconds**. Any non-200, transport error, or `done` not `true` is a generation
  failure.
- **Haiku shape:** exactly three lines, no syllable validation. A model response is
  usable only if, after splitting on `\n` and trimming each line, **exactly three**
  non-empty lines remain (see Behavioral Specification). Anything else is a generation
  failure. A model preamble (e.g. a leading `"Here is your haiku:"` line) is **not**
  stripped — it makes the response unusable.
- **History page:** newest first, capped at the **50** most recent haiku. Each entry
  shows the three lines **and** the theme, keyword, nature element (or "—" when null),
  and `created_at`.
- **Email subject:** `"A haiku for you: " + subjectSafe(theme)`, where `subjectSafe`
  removes `\r`, replaces `\n` with a space, and truncates to **60 runes** (appending
  `…` if it truncated). Only the subject is capped — the stored theme and the prompt
  use the full value.
- **`--ollama` / `OLLAMA_HOST` normalization:** if the resolved value contains no
  `://`, `http://` is prepended; a trailing `/` is trimmed. So a bare
  `OLLAMA_HOST=192.168.50.241:11434` becomes `http://192.168.50.241:11434`.

**Out of scope:** authentication, editing or deleting saved haiku, more than one
sender, real SMTP delivery, rate limiting, retries on model failure, syllable
counting, CSS beyond a minimal readable layout.

## Architecture

Six source files, one flat `package main`, module name `haiku`. No subdirectories.

```
haiku/
├── go.mod
├── main.go        — flag/env resolution, wiring, http.ListenAndServe. func main() only.
├── generate.go    — Haiku, HaikuGenerator, OllamaGenerator, buildPrompt, parseLines, normalizeEndpoint
├── mail.go        — Message, Mailer, FileMailer, formatEML, emlFilename
├── store.go       — Store, OpenStore, schema, SaveHaiku, GetHaiku, ListHaiku, LogEmail
├── handlers.go    — App, all HTTP handlers, view models, App.Routes
├── templates.go   — InitTemplates and the inline template strings
├── *_test.go      — one per source file above, plus integration_test.go
└── do_not_use_this_test.go — generated
```

**File assignment rules (strict):**
- `main.go` contains exactly: `func main()`. It resolves each flag/env parameter,
  calls `OpenStore`, constructs `NewOllamaGenerator`, `NewFileMailer`, and `App`, then
  `http.ListenAndServe(addr, app.Routes())`. No types, no handler bodies.
- `generate.go` contains the generation concern only: the `Haiku` struct,
  `HaikuGenerator` interface, `OllamaGenerator` and its constructor, and the unexported
  `buildPrompt`, `parseLines`, and `normalizeEndpoint` helpers. No HTTP handlers, no
  DB code.
- `mail.go` contains the mail concern only: `Message`, `Mailer`, `FileMailer` and its
  constructor, and the unexported `formatEML` and `emlFilename` helpers.
- `store.go` contains the persistence concern only: `Store`, `OpenStore`, the `schema`
  const, and the four query methods.
- `handlers.go` contains `App`, the four handler methods, `App.Routes`, the unexported
  view-model structs / `toView` helpers, and the unexported `subjectSafe` helper. No
  template strings, no SQL.
- `templates.go` contains `InitTemplates() *template.Template` and the inline template
  string constants. No handler logic.
- Do NOT put SQL in `handlers.go`. Do NOT put template strings anywhere but
  `templates.go`. Do NOT put HTTP handlers in `generate.go`, `mail.go`, or `store.go`.

All `.go` source files use `package main`. `go.mod` requires
`modernc.org/sqlite` (pure-Go SQLite driver, imported as `_ "modernc.org/sqlite"`,
`sql.Open("sqlite", path)`).

## Data Types and Function Signatures

```go
// ---- generate.go ----

// Haiku is a generated haiku plus the inputs it came from. When returned by a
// HaikuGenerator, ID is 0 and CreatedAt is ""; Store.SaveHaiku fills ID and the
// row's CreatedAt is set by SQLite. Nature is "" when no nature element was given.
type Haiku struct {
    ID        int64
    Theme     string
    Keyword   string
    Nature    string
    Line1     string
    Line2     string
    Line3     string
    CreatedAt string // "2006-01-02T15:04:05Z" (UTC), read back from the DB
}

// HaikuGenerator produces a Haiku from the three inputs. theme and keyword are
// non-empty (the handler validates before calling); nature may be "".
type HaikuGenerator interface {
    Generate(theme, keyword, nature string) (Haiku, error)
}

type OllamaGenerator struct {
    Endpoint string        // normalized: has a scheme, no trailing slash
    Model    string
    Client   *http.Client
}

// NewOllamaGenerator stores normalizeEndpoint(endpoint) and a *http.Client with a
// 60s Timeout.
func NewOllamaGenerator(endpoint, model string) *OllamaGenerator

// normalizeEndpoint prepends "http://" when the value has no "://" and trims a
// trailing "/". Unexported; declared here because generate_test.go tests it.
func normalizeEndpoint(v string) string

// ---- mail.go ----

type Message struct {
    FromAddr string
    ToName   string
    ToAddr   string
    Subject  string
    Body     string
    Date     time.Time
}

type Mailer interface {
    Send(m Message) error
}

type FileMailer struct {
    Dir string
}

func NewFileMailer(dir string) *FileMailer

// ---- store.go ----

type Store struct {
    // unexported *sql.DB
}

func OpenStore(path string) (*Store, error)                    // opens and runs schema
func (s *Store) SaveHaiku(h Haiku) (int64, error)              // returns new row id
func (s *Store) GetHaiku(id int64) (Haiku, error)              // sql.ErrNoRows if absent
func (s *Store) ListHaiku(limit int) ([]Haiku, error)          // newest first
func (s *Store) LogEmail(haikuID int64, name, email string) error

// ---- handlers.go ----

// View models. Unexported, but declared here so SURVEY stubs them and
// handlers_test.go / templates_test.go can construct them directly.
type panelView struct {
    Theme   string
    Keyword string
    Nature  string
    Message string      // "" on success; a fixed literal otherwise
    Haiku   *haikuView  // nil unless generation+save succeeded
}
type haikuView struct {
    ID    int64
    Line1 string
    Line2 string
    Line3 string
}
type historyView struct {
    Items []Haiku
}
type emailStatusView struct {
    OK      bool
    Message string
}

type App struct {
    Gen   HaikuGenerator
    Mail  Mailer
    Store *Store
    // unexported *template.Template
}

func NewApp(gen HaikuGenerator, mail Mailer, store *Store) *App

func (a *App) HandleIndex(w http.ResponseWriter, r *http.Request)    // GET  /
func (a *App) HandleGenerate(w http.ResponseWriter, r *http.Request) // POST /generate
func (a *App) HandleEmail(w http.ResponseWriter, r *http.Request)    // POST /email
func (a *App) HandleHistory(w http.ResponseWriter, r *http.Request)  // GET  /history
func (a *App) Routes() http.Handler

// subjectSafe strips \r, replaces \n with a space, and truncates to 60 runes
// (appending "…" if it truncated). Unexported; declared here because
// handlers_test.go tests it directly (Scenario 6).
func subjectSafe(theme string) string

// ---- templates.go ----

func InitTemplates() *template.Template
```

### Export signatures

```go
var _ func(string, string) *OllamaGenerator = NewOllamaGenerator
var _ func(string, string, string) (Haiku, error) = HaikuGenerator.Generate
var _ func(Message) error = Mailer.Send
var _ func(string) *FileMailer = NewFileMailer
var _ func(string) (*Store, error) = OpenStore
var _ func(*Store, Haiku) (int64, error) = (*Store).SaveHaiku
var _ func(*Store, int64) (Haiku, error) = (*Store).GetHaiku
var _ func(*Store, int) ([]Haiku, error) = (*Store).ListHaiku
var _ func(*Store, int64, string, string) error = (*Store).LogEmail
var _ func(HaikuGenerator, Mailer, *Store) *App = NewApp
var _ func(*App, http.ResponseWriter, *http.Request) = (*App).HandleIndex
var _ func(*App) http.Handler = (*App).Routes
var _ func() *template.Template = InitTemplates
```

## Behavioral Specification

**`buildPrompt(theme, keyword, nature string) string`** (unexported) — returns exactly:

```
Write a haiku about <theme>. It must contain the word "<keyword>".<evoke>
Respond with exactly three lines and nothing else: the first line 5 syllables, the second 7, the third 5.
```

where `<evoke>` is `" Evoke <nature>."` when `nature != ""` and empty otherwise. The
keyword is wrapped in ASCII double quotes. There is a single `\n` between the two
sentences and no trailing newline. Worked values are in Domain-Specific Test Scenarios.

**`parseLines(response string) ([3]string, bool)`** (unexported) — splits `response`
on `\n`, trims leading/trailing spaces, tabs and `\r` from each line, discards empty
lines, and returns the three lines with `ok == true` **only if exactly three non-empty
lines remain**. Fewer or more → `ok == false`. It does **not** take the first three or
last three lines of a longer response.

**`(*OllamaGenerator).Generate(theme, keyword, nature)`** — builds the request body
`{"model": g.Model, "prompt": buildPrompt(...), "stream": false}`, POSTs it to
`g.Endpoint + "/api/generate"` with `Content-Type: application/json`, and decodes the
response. The response is JSON with at least `{"response": string, "done": bool}`; only
those two fields are read. Returns a non-nil error if **any** of: the HTTP request
fails; the status is not 200; the response body is not decodable as JSON; `done` is not
`true`; `parseLines(response)` returns `ok == false`. On success returns
`Haiku{Theme: theme, Keyword: keyword, Nature: nature, Line1: l[0], Line2: l[1],
Line3: l[2]}` (ID 0, CreatedAt "").

**`normalizeEndpoint(v string) string`** (unexported) — if `v` contains no `"://"`,
prepend `"http://"`. Then trim **all** trailing `/` with `strings.TrimRight(v, "/")`
(so `"http://h//"` → `"http://h"`). `NewOllamaGenerator` applies this to its `endpoint`
argument, so `main()` may pass the raw flag/env value straight through. Worked cases in
Scenario 7.

**`subjectSafe(theme string) string`** (unexported) — `strings.ReplaceAll(theme, "\r",
"")`, then `strings.ReplaceAll(_, "\n", " ")`, then: if the result is ≤ 60 runes return
it, else return the first 60 runes + `"…"`. Worked cases in Scenario 6.

**`formatEML(m Message) string`** (unexported) — renders an RFC 5322 message. Header
lines and the blank separator line are terminated with `\r\n`; the body is appended
verbatim and a single trailing `\n` is added if the body does not already end with one.
Headers, in order: `From: <FromAddr>`, `To: <ToName> <<ToAddr>>`,
`Subject: <Subject>`, `Date: <RFC1123Z>`, `MIME-Version: 1.0`,
`Content-Type: text/plain; charset=utf-8`. Exact bytes in Domain-Specific Test
Scenarios.

**`emlFilename(m Message) string`** (unexported) — `<UTC>-<safeAddr>.eml` where `<UTC>`
is `m.Date.UTC().Format("20060102T150405Z")` and `<safeAddr>` is `m.ToAddr` with `@`
replaced by `_at_` and each of `.`, `/`, space replaced by `_`.

**`(*FileMailer).Send(m Message)`** — `os.MkdirAll(fm.Dir, 0o755)`, then writes
`formatEML(m)` to `filepath.Join(fm.Dir, emlFilename(m))` with `0o644`. Returns any
filesystem error. Does not send anything over the network.

**`OpenStore(path string)`** — `sql.Open("sqlite", path)`, `db.Ping()`, then
`db.Exec(schema)` where `schema` is the two `CREATE TABLE IF NOT EXISTS` statements in
Domain-Specific Test Scenarios. Idempotent — safe to call on an existing DB.

**`(*Store).SaveHaiku(h)`** — inserts `theme, keyword, nature, line1, line2, line3`.
`nature` is stored as SQL `NULL` when `h.Nature == ""`, otherwise the string.
`created_at` is left to the column default. Returns `LastInsertId()`.

**`(*Store).GetHaiku(id)`** — selects one row; maps a `NULL` `nature` back to `""`.
Returns `sql.ErrNoRows` when the id is absent.

**`(*Store).ListHaiku(limit)`** — `ORDER BY id DESC LIMIT ?`. Same `NULL` → `""`
mapping. Returns an empty slice (not nil-error) when there are no rows.

**`(*Store).LogEmail(haikuID, name, email)`** — inserts one `email_send` row;
`created_at` defaulted.

**`main()`** — for each of the five parameters, the resolved value is the flag value if
the flag was set, else the env-var value if non-empty, else the default (see the
Overview table). It then calls `OpenStore(db)` (fatal on error),
`NewOllamaGenerator(ollama, model)` (which normalizes the endpoint),
`NewFileMailer(outbox)`, `NewApp(gen, mail, store)`, and
`http.ListenAndServe(addr, app.Routes())`.

**Handlers.** `App.Routes` returns a `*http.ServeMux` with Go 1.22 method patterns:
`"GET /"`, `"POST /generate"`, `"POST /email"`, `"GET /history"`. A request that
doesn't match (wrong method, unknown path) gets the mux's default 404/405. Each handler
renders exactly one template by name (see the handlers→templates contract). All form
values are trimmed with **`strings.TrimSpace`** (the same everywhere; not the narrow
`parseLines` cutset).

The user-facing message strings are **fixed literals** (so handler tests can assert
them):

| situation | string |
|---|---|
| validation: theme or keyword empty | `Theme and keyword are both required.` |
| generation failed (`Generate` returned an error) | `The model couldn't produce a haiku — try again.` |
| save failed (`SaveHaiku` returned an error) | `Couldn't save the haiku — try again.` |
| email sent | `Emailed to <name> <<email>>.` |
| email send failed (`Mailer.Send` returned an error `e`) | `Couldn't send the email — <e>.` |
| bad email input (missing field, unparseable id, haiku not found) | `Couldn't email that haiku — check the recipient and try again.` |

- **`HandleIndex`** — `ExecuteTemplate(w, "index", <zero panelView>)`.
- **`HandleGenerate`** — reads and trims `theme`, `keyword`, `nature`.
  1. If `theme == "" || keyword == ""`: render `"panel"` with the submitted values
     refilled, `Message` = the validation literal, `Haiku` = nil. Do **not** call the
     generator.
  2. Else call `a.Gen.Generate(theme, keyword, nature)`. On error: render `"panel"`
     with values refilled, `Message` = the generation literal, `Haiku` = nil.
  3. Else `id, err := a.Store.SaveHaiku(h)`. On `err`: render `"panel"` with values
     refilled, `Message` = the save literal, `Haiku` = nil.
  4. Else render `"panel"` with values refilled, `Message` = `""`, `Haiku` set (its
     `ID` = `id`, carried as the hidden `haiku_id` in the email sub-form).
- **`HandleEmail`** — reads and trims `haiku_id`, `recipient_name`, `recipient_email`.
  1. If any is empty, or `haiku_id` doesn't parse as a positive int: render
     `"email-status"` with `OK=false` and the bad-input literal. Stop.
  2. `h, err := a.Store.GetHaiku(id)`. On **any** `err` (including `sql.ErrNoRows`):
     render `"email-status"` `OK=false` with the bad-input literal. Stop.
  3. Build `Message{FromAddr: senderAddr, ToName: name, ToAddr: email,
     Subject: "A haiku for you: " + subjectSafe(h.Theme),
     Body: h.Line1 + "\n" + h.Line2 + "\n" + h.Line3, Date: time.Now()}`; call
     `a.Mail.Send(m)`. On error: render `"email-status"` `OK=false` with the
     send-failed literal. Do **not** call `LogEmail`.
  4. Else `a.Store.LogEmail(id, name, email)` — if **that** errors, still render
     `"email-status"` `OK=false` with the send-failed literal (the `.eml` was written
     but the send is not considered complete). On success: render `"email-status"`
     `OK=true` with the sent literal.
- **`HandleHistory`** — `items, err := a.Store.ListHaiku(50)`. On `err`:
  `http.Error(w, "internal error", 500)`. Else `ExecuteTemplate(w, "history",
  historyView{Items: items})`.

**HTMX wiring.** The index page's form has
`hx-post="/generate" hx-target="#panel" hx-swap="outerHTML"`. The `"panel"` template's
**outermost element is `<div id="panel"> … </div>`** — the same `id` as the element it
replaces — so after an `outerHTML` swap the next submit still resolves
`hx-target="#panel"`. That `<div id="panel">` contains the `<form>` (with its
`Theme`/`Keyword`/`Nature` values refilled) **and** the result region, so every
`/generate` response (success, validation error, generation error, save error) carries
the full form state inside the swapped fragment. The email sub-form has
`hx-post="/email" hx-target="#email-status" hx-swap="innerHTML"`; `#email-status` is a
`<span>` inside the haiku result block, rendered by the `"email-status"` template.

## Domain-Specific Test Scenarios

Every expected value below was produced by a throwaway verification script, not written
from memory — the scripts and their output are reproduced in
`haiku-generator-drafting-log.md`. The Ollama request/response shape was also checked
against a live `/api/generate` call to the target host.

**Scenario 1 — prompt with a nature element:**

`buildPrompt("autumn", "leaves", "maple")` returns exactly (one `\n` shown as a line
break, no trailing newline):

```
Write a haiku about autumn. It must contain the word "leaves". Evoke maple.
Respond with exactly three lines and nothing else: the first line 5 syllables, the second 7, the third 5.
```

**Scenario 2 — prompt without a nature element:**

`buildPrompt("the sea", "salt", "")` returns exactly:

```
Write a haiku about the sea. It must contain the word "salt".
Respond with exactly three lines and nothing else: the first line 5 syllables, the second 7, the third 5.
```

No `Evoke` sentence, no double space after the keyword's closing quote+period.

**Scenario 3 — response parsing:**

| `response` | `parseLines` result |
|---|---|
| `"Crimson maple leaves\nScatter on the quiet path\nAutumn wind then still"` | `ok=true`, the 3 lines |
| `"  Crimson maple leaves \n\n Scatter on the quiet path\nAutumn wind then still \n"` | `ok=true`, same 3 lines trimmed (blank line and surrounding spaces dropped) |
| `"Here is your haiku:\nCrimson maple leaves\nScatter on the quiet path\nAutumn wind then still"` | `ok=false` (4 non-empty lines — a preamble is NOT stripped) |
| `"Crimson maple leaves\nScatter on the quiet path"` | `ok=false` (2 non-empty lines) |

Do NOT "fix" a 4-line response by taking the last three. `ok=false` → generation
failure → error fragment.

**Scenario 4 — `.eml` file bytes:**

For `Message{FromAddr:"lehmann314159@gmail.com", ToName:"Basho",
ToAddr:"basho@example.com", Subject:"A haiku for you: autumn",
Body:"Crimson maple leaves\nScatter on the quiet path\nAutumn wind then still",
Date: 2026-08-29 17:30:00 -0600}`, `formatEML` returns exactly these bytes
(`\r\n` after each header and after the blank line; `\n` inside the body; one trailing
`\n`):

```
From: lehmann314159@gmail.com\r\n
To: Basho <basho@example.com>\r\n
Subject: A haiku for you: autumn\r\n
Date: Sat, 29 Aug 2026 17:30:00 -0600\r\n
MIME-Version: 1.0\r\n
Content-Type: text/plain; charset=utf-8\r\n
\r\n
Crimson maple leaves\n
Scatter on the quiet path\n
Autumn wind then still\n
```

and `emlFilename` returns `20260829T233000Z-basho_at_example_com.eml` (the date is
converted to UTC: 17:30 -0600 → 23:30Z).

**Scenario 5 — SQLite schema and round-trip:**

The `schema` constant is exactly:

```sql
CREATE TABLE IF NOT EXISTS haiku (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    theme        TEXT NOT NULL,
    keyword      TEXT NOT NULL,
    nature       TEXT,
    line1        TEXT NOT NULL,
    line2        TEXT NOT NULL,
    line3        TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE TABLE IF NOT EXISTS email_send (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    haiku_id        INTEGER NOT NULL REFERENCES haiku(id),
    recipient_name  TEXT NOT NULL,
    recipient_email TEXT NOT NULL,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
```

After `SaveHaiku(Haiku{Theme:"autumn", Keyword:"leaves", Nature:"", Line1:"Crimson
maple leaves", Line2:"Scatter on the quiet path", Line3:"Autumn wind then still"})`:
`GetHaiku(1)` returns that haiku with `Nature == ""` (stored `NULL`) and `CreatedAt`
matching `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`. After `LogEmail(1, "Basho",
"basho@example.com")`, `email_send` has one row with `haiku_id == 1`.

**Scenario 6 — `subjectSafe`:**

| input `theme` | `subjectSafe` output |
|---|---|
| `"autumn"` | `"autumn"` (unchanged) |
| `"the sea and the sky at the very end of a long grey afternoon in November"` (72 runes) | `"the sea and the sky at the very end of a long grey afternoon…"` (first 60 runes + `…`) |
| `"line one\r\nline two"` | `"line one line two"` (`\r` dropped, `\n` → space) |

So the email header for the middle case is
`Subject: A haiku for you: the sea and the sky at the very end of a long grey afternoon…`.

**Scenario 7 — `normalizeEndpoint`:**

| input | output |
|---|---|
| `"192.168.50.241:11434"` | `"http://192.168.50.241:11434"` |
| `"http://localhost:11434"` | `"http://localhost:11434"` |
| `"http://localhost:11434/"` | `"http://localhost:11434"` |
| `"http://localhost:11434//"` | `"http://localhost:11434"` |
| `"https://ollama.example.com/"` | `"https://ollama.example.com"` |

## Cross-Bead Contracts

### handlers → generator (protocol)

- **type**: protocol
- **producer**: generate
- **consumer**: handlers
- **interface**: `HaikuGenerator.Generate(theme, keyword, nature string) (Haiku, error)`
- **notes**: `HandleGenerate` calls `Generate` only after trimming and confirming
  `theme != "" && keyword != ""`. On a non-nil error it renders the `#panel` fragment
  with the submitted values refilled and a generation-error message — it must **not**
  return HTTP 500 and must **not** save anything. On success it calls
  `Store.SaveHaiku` before rendering. Handler tests use a fake `HaikuGenerator`;
  `OllamaGenerator` is never called against a live server in any test.

### handlers → mailer (protocol)

- **type**: protocol
- **producer**: mail
- **consumer**: handlers
- **interface**: `Mailer.Send(m Message) error`
- **notes**: `HandleEmail` builds `Message` with `FromAddr = senderAddr` (the literal
  `lehmann314159@gmail.com`), `Subject = "A haiku for you: " + subjectSafe(haiku.Theme)`,
  `Body = Line1 + "\n" + Line2 + "\n" + Line3`, `Date = time.Now()`. On `Send` error:
  render `#email-status` error, **do not** call `Store.LogEmail`. On success: call
  `Store.LogEmail`, then render the confirmation. Handler tests inject a `FileMailer`
  pointed at `t.TempDir()` (or a fake that records the `Message`).

### handlers → store (protocol)

- **type**: protocol
- **producer**: store
- **consumer**: handlers
- **interface**: `OpenStore`, `SaveHaiku`, `GetHaiku`, `ListHaiku`, `LogEmail` as
  declared in Data Types and Function Signatures, plus the `Haiku` struct shape.
- **notes**: `SaveHaiku` maps `Nature == ""` to SQL `NULL`; `GetHaiku`/`ListHaiku` map
  it back to `""`. `SaveHaiku` returns `(0, err)` on failure — the handler must **not**
  use the `0`. Handler-side reaction to every store error is spelled out per handler in
  Behavioral Specification (save error → `"panel"` error; `ListHaiku` error → HTTP 500;
  any `GetHaiku` error incl. `sql.ErrNoRows`, and `LogEmail` error → `"email-status"`
  error).

### handlers → templates (data-shape)

- **type**: data-shape
- **producer**: handlers
- **consumer**: templates
- **interface**: `panelView{Theme, Keyword, Nature string; Message string;
  Haiku *haikuView}` and `haikuView{ID int64; Line1, Line2, Line3 string}` and
  `historyView{Items []Haiku}` and `emailStatusView{OK bool; Message string}` — the
  exact struct names/fields SURVEY declares must match what `templates.go` renders.
- **template names.** `InitTemplates` parses one string containing exactly four
  `{{define}}` blocks; each handler calls `tmpl.ExecuteTemplate(w, <name>, <view>)`:

  | handler | template name | view model |
  |---|---|---|
  | `HandleIndex` | `"index"` | `panelView` (zero value) |
  | `HandleGenerate` | `"panel"` | `panelView` |
  | `HandleEmail` | `"email-status"` | `emailStatusView` |
  | `HandleHistory` | `"history"` | `historyView` |

  `"index"` is the full HTML page and renders the panel via `{{template "panel" .}}`
  (it takes a `panelView`, so `.` passes straight through). `"history"` is a full HTML
  page. `"panel"` and `"email-status"` are bare fragments.
- **notes**: The `"panel"` template's outermost element is `<div id="panel">`, wrapping
  the `<form>` **and** the result region; every `/generate` response is a full
  `<div id="panel">` (`hx-swap="outerHTML"`, same `id`) so the form's refilled
  `Theme`/`Keyword`/`Nature` values and the `Message` are always inside the swapped
  fragment (checklist class 4). `"email-status"` renders a `<span>` swapped `innerHTML`
  by `/email`. Templates are inline Go string constants in `templates.go` — no external
  `.html` files, no FuncMap helpers. The history page renders every field of each
  `Haiku` — the three lines, `Theme`, `Keyword`, `Nature`
  (`{{if .Nature}}…{{else}}—{{end}}`), and `CreatedAt` as-is.

### OllamaGenerator → Ollama HTTP (format)

- **type**: format
- **producer**: generate
- **consumer**: Ollama (external)
- **interface**: request `POST {endpoint}/api/generate`, body
  `{"model": string, "prompt": string, "stream": false}`, `Content-Type:
  application/json`. Response: HTTP 200 with a JSON object containing at least
  `{"response": string, "done": bool}`. Only `response` and `done` are read; all other
  fields are ignored. Observed key set from a live `/api/generate` call (glm-4.7-flash,
  2026-08-29): `model, created_at, response, thinking, done, done_reason, context,
  total_duration, load_duration, prompt_eval_count, prompt_eval_duration, eval_count,
  eval_duration`. In particular **`thinking` is ignored** — a reasoning model's
  chain-of-thought must not be treated as haiku text; only `response` is parsed.
- **notes**: client timeout 60s. Non-200, transport error, undecodable JSON body,
  `done != true`, or `parseLines` failure → `Generate` returns an error.
  `OllamaGenerator` bead tests use `httptest.NewServer` returning canned bodies; there
  is no live-Ollama test.

### FileMailer → filesystem (format)

- **type**: format
- **producer**: mail
- **consumer**: filesystem (an `.eml` reader)
- **interface**: the exact RFC 5322 byte layout and filename scheme in Scenario 4.
- **notes**: `\r\n` line endings on headers and the separator; body verbatim + one
  trailing `\n`. Directory auto-created.

## Decomposition Notes

- **Pin Scenarios 1 & 2 (both prompt strings, verbatim, including the "no double
  space / no Evoke sentence" note) into the `generate` bead spec.**
- **Pin Scenario 3's four response-parsing cases into the `generate` bead spec**,
  including "a preamble is NOT stripped" and "do NOT take the last three lines."
- **Pin Scenario 4's exact `.eml` bytes and the `emlFilename` output (with the
  UTC conversion) into the `mail` bead spec.**
- **Pin Scenario 5's `schema` constant verbatim and the `NULL`-nature round-trip into
  the `store` bead spec.**
- **Pin Scenario 6 (`subjectSafe`: 60-rune cap + `\r`/`\n` stripping) into the
  `handlers` bead spec, and Scenario 7 (`normalizeEndpoint`) into the `generate` bead
  spec.**
- **Pin the fixed message-string table** (validation / generation / save / email-sent /
  email-failed / bad-email literals) into the `handlers` bead spec — handler tests
  assert on these exact strings.
- **Pin the template-name table** (`index`/`panel`/`email-status`/`history` → which
  handler, which view model, `"index"` renders `{{template "panel" .}}`) into **both**
  the `handlers` and `templates` bead specs — the name strings cross the bead boundary.
- **Pin the `#panel` swap-target rule** (the `"panel"` template's outermost element is
  `<div id="panel">`, wrapping form + result; `outerHTML` swap keeps the same `id`;
  refilled values live inside it) into both the `handlers` and `templates` bead specs —
  checklist class 4, the most likely silent web-app bug.
- **Sequencing:** the `templates` bead must come before the `handlers` bead — handler
  httptest assertions need real rendered HTML, not a stub template.
- **Integration bead — one bounded scenario:** with a fake `HaikuGenerator` returning
  the three fixed lines from Scenario 4, a `FileMailer` pointed at `t.TempDir()`, and
  `OpenStore(filepath.Join(t.TempDir(), "t.db"))`: (1) `POST /generate` with
  `theme=autumn&keyword=leaves`; assert the response body contains the three lines and
  that `ListHaiku(50)` now returns one row. (2) `POST /email` with that haiku's id,
  `recipient_name=Basho&recipient_email=basho@example.com`; assert an `.eml` file now
  exists in the temp outbox and `email_send` has one row. Do not test model-failure or
  SMTP-failure paths here — those are `generate`/`handlers` unit tests.
- `OllamaGenerator` and `FileMailer` are each their own bead (real impl + httptest /
  tempdir tests). The `generate` bead also owns `buildPrompt`/`parseLines`, tested
  directly.

## Resolved decisions

All open questions resolved 2026-08-29. Nothing outstanding.

**From interactive drafting:**

- **Persistence:** SQLite, two tables — `haiku` (history) and `email_send` (send log).
- **Model failure:** error fragment, form re-rendered with inputs preserved, no retry.
  Config from `--ollama`/`--model` flags with `OLLAMA_HOST`/`HAIKU_MODEL` env fallback.
- **Email:** `.eml` files into `--outbox`, no SMTP, no credentials; sender is a
  hardcoded `const`.
- **Haiku validation:** none — display whatever parses to exactly three non-empty
  lines; anything else is a failure.

**Second round:**

- **Strict 3-line parsing — kept.** A model preamble ("Here is your haiku:") makes the
  response unusable and the user resubmits; no leniency in the first cut. Scenario 3
  case 3 pins this.
- **`--model` default `gemma4:31b`** — confirmed.
- **Subject cap:** `subjectSafe` — `\r` stripped, `\n` → space, truncate to 60 runes
  + `…`. Only the subject is capped. Scenario 6 pins it.
- **History page shows every field** of each stored haiku (lines + theme + keyword +
  nature-or-`—` + timestamp).
- **`OLLAMA_HOST` shape:** lenient — `normalizeEndpoint` prepends `http://` to a
  scheme-less value and trims **all** trailing `/` (`strings.TrimRight`), so a bare
  `host:port` works. Scenario 7 pins it.

**From the check-design-doc judgment pass (all accepted):**

- **Store-error handling** now spelled out per handler: `SaveHaiku` error → `"panel"`
  error (never use the returned `0` id); `ListHaiku` error → HTTP 500; any `GetHaiku`
  error (incl. `sql.ErrNoRows`) or `LogEmail` error → `"email-status"` error.
- **Template names pinned** — `index`/`panel`/`email-status`/`history`, one
  `ExecuteTemplate` call per handler, `"index"` renders `{{template "panel" .}}`.
- **Fixed user-facing message strings** — a six-row literal table; handler tests assert
  them exactly. Success `panelView.Message == ""`.
- **`strings.TrimSpace`** for all handler form values (distinct from `parseLines`'
  narrower cutset).
- **`normalizeEndpoint` trims all trailing `/`** (`strings.TrimRight`), Scenario 7 has
  the `//` case.
- **`"panel"` fragment root** is `<div id="panel">` (same id, survives the `outerHTML`
  swap) — stated outright, not just implied.
- **`Generate` errors** on an undecodable JSON body too (added to the error list).
- **Scenario 6 rune count** corrected 71 → 72 (output was already right).
