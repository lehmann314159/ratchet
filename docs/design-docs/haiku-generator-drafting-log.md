# Haiku Generator — drafting log

How `haiku-generator-prose.md` (≈180 words of loose prose) became
`haiku-generator-design-doc.md` (a 675-line prescriptive design doc, cleared for
`new-project`), on 2026-08-29, via the `draft-design-doc` → `check-design-doc` skill
pair. Kept as a worked example of that workflow.

---

## 0. The input

`haiku-generator-prose.md` — a Go + HTMX web app that generates a 5-7-5 haiku from a
theme + required keyword + optional nature element by calling a local Ollama model,
"if there needs to be a database" use SQLite, and be able to email the haiku to a
named recipient (sender hardcoded).

The prose left a lot open on purpose. It also proposed something ratchet had not built
before: **an app that itself calls a local model and sends email** — two live external
side effects, both of which become "protocol contracts" (the design-doc guide's
highest-risk web-app gap) and both of which need a pinned test strategy so bead tests
never hit a live endpoint.

---

## 1. Up-front decisions (before drafting)

Four gaps were large enough to change the architecture, so they were resolved before a
single section was written rather than surfaced later as open questions:

| question | answer |
|---|---|
| Does it persist anything? | **SQLite** — `haiku` table (history) + `email_send` table (send log) |
| Model unreachable / garbage? | Error fragment, **form re-rendered with inputs preserved**, no retry |
| Email transport? | Write RFC 5322 **`.eml` files** to a `--outbox` dir. No SMTP, no credentials. |
| Validate 5-7-5? | **No.** Display whatever parses to exactly three non-empty lines; anything else is a failure. |

---

## 2. What `draft-design-doc` did

Produced the seven template sections. Chose a 6-file layout
(`generate`/`mail`/`store`/`handlers`/`templates`/`main`), both external dependencies
behind interfaces (`HaikuGenerator`, `Mailer`) with fakes for tests, `OllamaGenerator`
tested only via `httptest`, `FileMailer` tested against `t.TempDir()`, and a `#panel`
swap target wrapping form + result so inputs survive an error (checklist class 4).

### Every worked number was script-verified, not recalled

**`verify_prompt.go`** — the exact prompt builder and the strict response parser:

```go
package main

import "fmt"

func buildPrompt(theme, keyword, nature string) string {
	evoke := ""
	if nature != "" {
		evoke = fmt.Sprintf(" Evoke %s.", nature)
	}
	return fmt.Sprintf(
		"Write a haiku about %s. It must contain the word %q.%s\n"+
			"Respond with exactly three lines and nothing else: "+
			"the first line 5 syllables, the second 7, the third 5.",
		theme, keyword, evoke)
}

// exactly 3 non-empty trimmed lines -> ok; otherwise ok=false (no preamble stripping)
func parseLines(response string) (l [3]string, ok bool) {
	var nonEmpty []string
	start := 0
	for i := 0; i <= len(response); i++ {
		if i == len(response) || response[i] == '\n' {
			line := trimSpace(response[start:i])
			if line != "" {
				nonEmpty = append(nonEmpty, line)
			}
			start = i + 1
		}
	}
	if len(nonEmpty) != 3 {
		return l, false
	}
	copy(l[:], nonEmpty)
	return l, true
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}

func main() {
	fmt.Printf("with nature:\n%s\n\n", buildPrompt("autumn", "leaves", "maple"))
	fmt.Printf("without nature:\n%s\n\n", buildPrompt("the sea", "salt", ""))
	for _, c := range []string{
		"Crimson maple leaves\nScatter on the quiet path\nAutumn wind then still",
		"  Crimson maple leaves \n\n Scatter on the quiet path\nAutumn wind then still \n",
		"Here is your haiku:\nCrimson maple leaves\nScatter on the quiet path\nAutumn wind then still",
		"Crimson maple leaves\nScatter on the quiet path",
	} {
		l, ok := parseLines(c)
		fmt.Printf("ok=%-5v lines=%q  <= %q\n", ok, l, c)
	}
}
```

Output — note the `"Here is your haiku:"` case is `ok=false` (a preamble is not
stripped; it is a failure):

```
ok=true  lines=["Crimson maple leaves" "Scatter on the quiet path" "Autumn wind then still"]
ok=true  lines=["Crimson maple leaves" "Scatter on the quiet path" "Autumn wind then still"]
ok=false lines=["" "" ""]  <= "Here is your haiku:\n..."
ok=false lines=["" "" ""]  <= "Crimson maple leaves\nScatter on the quiet path"
```

**`verify_haiku.go`** — the RFC 5322 `.eml` bytes and the SQLite schema round-trip:

```go
package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Message struct {
	FromAddr, ToName, ToAddr, Subject, Body string
	Date                                    time.Time
}

func formatEML(m Message) string {
	var b strings.Builder
	b.WriteString("From: " + m.FromAddr + "\r\n")
	b.WriteString(fmt.Sprintf("To: %s <%s>\r\n", m.ToName, m.ToAddr))
	b.WriteString("Subject: " + m.Subject + "\r\n")
	b.WriteString("Date: " + m.Date.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(m.Body)
	if !strings.HasSuffix(m.Body, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

func emlFilename(m Message) string {
	safe := strings.NewReplacer("@", "_at_", ".", "_", "/", "_", " ", "_").Replace(m.ToAddr)
	return fmt.Sprintf("%s-%s.eml", m.Date.UTC().Format("20060102T150405Z"), safe)
}

const schema = `
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
`

func main() {
	m := Message{
		FromAddr: "lehmann314159@gmail.com", ToName: "Basho", ToAddr: "basho@example.com",
		Subject: "A haiku for you: autumn",
		Body:    "Crimson maple leaves\nScatter on the quiet path\nAutumn wind then still",
		Date:    time.Date(2026, 8, 29, 17, 30, 0, 0, time.FixedZone("MDT", -6*3600)),
	}
	fmt.Printf("=== %s ===\n%s\n", emlFilename(m), formatEML(m))

	db, _ := sql.Open("sqlite", ":memory:")
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		panic(err)
	}
	res, _ := db.Exec(`INSERT INTO haiku(theme,keyword,nature,line1,line2,line3) VALUES(?,?,?,?,?,?)`,
		"autumn", "leaves", nil, "Crimson maple leaves", "Scatter on the quiet path", "Autumn wind then still")
	id, _ := res.LastInsertId()
	db.Exec(`INSERT INTO email_send(haiku_id,recipient_name,recipient_email) VALUES(?,?,?)`,
		id, "Basho", "basho@example.com")
	var theme, l1, created string
	var nature sql.NullString
	db.QueryRow(`SELECT theme,nature,line1,created_at FROM haiku WHERE id=?`, id).
		Scan(&theme, &nature, &l1, &created)
	fmt.Printf("haiku id=%d theme=%q nature.Valid=%v created_at=%q\n", id, theme, nature.Valid, created)
}
```

Output:

```
=== 20260829T233000Z-basho_at_example_com.eml ===
From: lehmann314159@gmail.com
To: Basho <basho@example.com>
Subject: A haiku for you: autumn
Date: Sat, 29 Aug 2026 17:30:00 -0600
MIME-Version: 1.0
Content-Type: text/plain; charset=utf-8

Crimson maple leaves
Scatter on the quiet path
Autumn wind then still

haiku id=1 theme="autumn" nature.Valid=false created_at="2026-08-29T23:38:35Z"
```

(The `17:30 -0600` date becomes `23:30Z` in the UTC filename — a detail worth pinning.)

### Live Ollama call

A real `POST /api/generate` to the target host locked the response shape:

```
top-level keys: context, created_at, done, done_reason, eval_count, eval_duration,
                load_duration, model, prompt_eval_count, prompt_eval_duration,
                response, thinking, total_duration
response: "Red maple leaves\nGold and orange leaves are turning bright\nUpon the grass below"
done: true   done_reason: stop
```

**Finding:** the response carries a `thinking` field on reasoning models. The doc now
states explicitly that only `response` and `done` are read and `thinking` is ignored —
a reasoning model's chain-of-thought must not be treated as haiku text.

### Second round of open questions (answered interactively)

| # | resolution |
|---|---|
| Strict 3-line parsing vs. chatty models | **Keep strict** for the first cut — a preamble makes the response a failure and the user resubmits |
| `--model` default | `gemma4:31b` — confirmed |
| Subject header injection (long/newline theme) | new `subjectSafe`: strip `\r`, `\n`→space, truncate to **60 runes** + `…` |
| History page contents | show **every** stored field, not just the poem |
| `OLLAMA_HOST` shape | **lenient** — `normalizeEndpoint` prepends `http://` to a scheme-less value, trims all trailing `/` |

`subjectSafe` and `normalizeEndpoint` were then verified with **`verify_haiku2.go`**
(same pattern), including the `//` trailing-slash case and the 60-rune boundary.

---

## 3. What `check-design-doc` did

**Mechanical pass** (`cmd/checkdesigndoc --checks=all`): 0 ambiguity flags across all
five classes; pin counts consistent.

**Independent judgment pass** (fresh subagent, given only the doc + the ambiguity
checklist): **8 findings, all real, all accepted.** The valuable ones were cross-bead
gaps that would have passed every unit test and broken at runtime:

| # | class | gap | fix |
|---|---|---|---|
| 1 | 5 / 13 | Store errors only half-specified. `SaveHaiku` returns `(0, err)` on failure and the `0` would flow into the email form's hidden `haiku_id` → silent wrong-haiku email. | Per-handler store-error branches; save error → panel error (id never used); `ListHaiku` error → 500; `GetHaiku`/`LogEmail` error → email-status error |
| 2 | 10 | Template key strings (`ExecuteTemplate(w, "<name>", …)` vs `{{define "<name>"}}`) never pinned — a producer/consumer contract across a bead boundary. | Pinned table: `index` / `panel` / `email-status` / `history` → handler + view model |
| 3 | 5 / 10 | Three `/generate` outcomes distinguishable only by unpinned `Message` text; handler tests can't assert them. | Six-row fixed-literal message table; success `Message == ""` |
| 4 | 10 | "a confirmation naming the recipient" — name? email? both? | Pinned literal `Emailed to <name> <<email>>.` |
| 5 | general | "trims each" unspecified while `parseLines` is pinned exactly; `TrimSpace` vs a narrow cutset diverge on `"autumn\n"`. | `strings.TrimSpace` everywhere in handlers |
| 6 | 2 | `normalizeEndpoint` "any trailing `/`" — one or all? | `strings.TrimRight` (all); `//` case added to the scenario |
| 7 | 13 | Scenario 6 annotated "(71 runes)"; the string is **72**. Output was still correct — but an unverified annotation in a doc that claims verification. | Corrected to 72 (re-counted) |
| 8 | 4 | `#panel` fragment root `id` for the `outerHTML` swap only implied, never stated. Without `id="panel"` on the fragment root, the 2nd submit has no target. | Stated: outermost element is `<div id="panel">` |
| sub | 5 | `Generate`'s error-trigger list read as exhaustive but omitted a JSON-decode failure. | Added |

Also folded in while there: the four view-model structs
(`panelView`/`haikuView`/`historyView`/`emailStatusView`) are now declared in Data
Types (SURVEY stubs them; tests construct them).

---

## 4. Framework bugs this exercise surfaced (fixed in the ratchet repo)

- **`cmd/checkdesigndoc` pin check** only recognized connect-four's numbered-list
  worked-example style, so `**Scenario A — …:**` headers counted as zero and the pin
  check passed vacuously (commit `c8e91b8`, found on the earlier billing exercise).
- **`cmd/checkdesigndoc` class 2** flagged `go.mod` — `\bmod\b` matched the build-file
  name in every Architecture section (commit `04caf0a`). Changed to `\bmod ` with a
  trailing space; real modulo usage still flags.

---

## 5. Final state

`haiku-generator-design-doc.md` — 675 lines, `checkdesigndoc` clean (0 ambiguity
flags, 7 worked scenarios / 8 pins), cleared for `new-project`, **not yet run**. If it
is run, first use is itself a data point on how the fleet handles an app with two live
external dependencies.
