# Task List Server — Design Document

## Overview

A minimal HTTP task list server. Users can add named tasks, mark them complete, and
delete them. The UI is a single-page HTML app using HTMX for partial updates — no
full-page reloads after the initial load. Storage is in-memory (no database); the
list resets on server restart. Out of scope: persistence, authentication, multi-user
isolation, priorities, due dates.

**Domain parameters:**
- Tasks are identified by a monotonically increasing integer ID starting at 1
- Task titles are arbitrary non-empty strings; server does no validation beyond requiring
  a non-empty form field
- Toggle flips Done from false to true and back
- Deleted tasks are removed permanently; IDs are not reused
- Server listens on :8080

## Architecture

```
tasklist/
├── go.mod
├── main.go      — var store *TaskStore, var templates *template.Template; func main() only
├── store.go     — Task, TaskStore types; NewTaskStore, Add, All, Toggle, Delete
├── templates.go — TaskView type; InitTemplates, RenderIndex, RenderTaskList
├── handlers.go  — HandleIndex, HandleCreate, HandleToggle, HandleDelete
└── *_test.go    — one test file per source file above, plus integration_test.go
```

All `.go` files use `package main` at the project root — no subdirectories.

**File assignment rules (strict):**
- `main.go` contains exactly: `var store *TaskStore`, `var templates *template.Template`,
  and `func main()`. Nothing else.
- `store.go` contains: `Task`, `TaskStore` type declarations; `NewTaskStore`, `Add`,
  `All`, `Toggle`, `Delete`.
- `templates.go` contains: `TaskView` type; `InitTemplates`, `RenderIndex`,
  `RenderTaskList`. No handler functions.
- `handlers.go` contains: `HandleIndex`, `HandleCreate`, `HandleToggle`,
  `HandleDelete`. No type declarations.
- Do NOT put `TaskView` in `handlers.go` — it belongs in `templates.go`.
- Do NOT put `HandleIndex`, `HandleCreate`, `HandleToggle`, or `HandleDelete` in
  `store.go` or `templates.go`.

## Data Types and Function Signatures

All `.go` source files use `package main`. The module name is `tasklist`.
Requires Go 1.22 (for `ServeMux` pattern parameters and `r.PathValue`).

```go
// store.go
type Task struct {
    ID    int
    Title string
    Done  bool
}

type TaskStore struct {
    // unexported: sync.Mutex + []*Task slice + nextID int
}

func NewTaskStore() *TaskStore
func (s *TaskStore) Add(title string) *Task
func (s *TaskStore) All() []*Task
func (s *TaskStore) Toggle(id int) bool
func (s *TaskStore) Delete(id int) bool

// templates.go
type TaskView struct {
    Tasks []*Task
}

var templates *template.Template

func InitTemplates() *template.Template
func RenderIndex(w http.ResponseWriter, view TaskView)
func RenderTaskList(w http.ResponseWriter, view TaskView)

// handlers.go
var store *TaskStore

func HandleIndex(w http.ResponseWriter, r *http.Request)
func HandleCreate(w http.ResponseWriter, r *http.Request)
func HandleToggle(w http.ResponseWriter, r *http.Request)
func HandleDelete(w http.ResponseWriter, r *http.Request)

// main.go
func main()
```

### Export signatures

```go
var _ func() *TaskStore = NewTaskStore
var _ func(*TaskStore, string) *Task = (*TaskStore).Add
var _ func(*TaskStore) []*Task = (*TaskStore).All
var _ func(*TaskStore, int) bool = (*TaskStore).Toggle
var _ func(*TaskStore, int) bool = (*TaskStore).Delete
var _ func() *template.Template = InitTemplates
var _ func(http.ResponseWriter, TaskView) = RenderIndex
var _ func(http.ResponseWriter, TaskView) = RenderTaskList
var _ func(http.ResponseWriter, *http.Request) = HandleIndex
var _ func(http.ResponseWriter, *http.Request) = HandleCreate
var _ func(http.ResponseWriter, *http.Request) = HandleToggle
var _ func(http.ResponseWriter, *http.Request) = HandleDelete
var _ *TaskStore = store
var _ *template.Template = templates
```

## Behavioral Specification

**`NewTaskStore() *TaskStore`** — allocates and returns a TaskStore with an empty
task list and nextID set to 1.

**`(*TaskStore).Add(title string) *Task`** — creates a Task with the next available
ID and Done=false, appends it to the internal slice, increments nextID, and returns
the new Task. Thread-safe (acquires the mutex).

**`(*TaskStore).All() []*Task`** — returns a shallow copy of the internal slice: a
new slice header holding the same `*Task` pointers stored internally, in insertion
order. The `Task` structs themselves are NOT copied — mutating any field of a
returned `Task` mutates the store's own data. Callers must treat returned Tasks as
read-only for this reason, not merely by convention.

**`(*TaskStore).Toggle(id int) bool`** — finds the Task with the given ID and flips
its Done field. Returns true if found, false if no Task with that ID exists.
Thread-safe.

**`(*TaskStore).Delete(id int) bool`** — removes the Task with the given ID,
preserving the relative order of the remaining tasks (e.g. via
`append(tasks[:i], tasks[i+1:]...)`, not a swap-with-last removal — the latter
would silently reorder subsequent `All()` results). Returns true if found and
removed, false if not found. Thread-safe.

**`InitTemplates() *template.Template`** — parses the inline template strings and
returns the resulting `*template.Template`. Panics if the template source fails to
parse (indicates a programming error). Called once from `main()`.

**`RenderIndex(w http.ResponseWriter, view TaskView)`** — executes the "index" named
template against view and writes the result to w. Used only by HandleIndex.

**`RenderTaskList(w http.ResponseWriter, view TaskView)`** — executes the "task-list"
named template against view and writes the result to w. Used by HandleCreate,
HandleToggle, and HandleDelete to return the updated fragment.

**`var templates *template.Template`** — package-level variable set by `main()` via
`templates = InitTemplates()` before the server starts.

**`var store *TaskStore`** — package-level variable set by `main()` via
`store = NewTaskStore()` before the server starts.

**`HandleIndex`** — serves `GET /`. Renders the full index page with the current
task list.

**`HandleCreate`** — serves `POST /tasks`. Reads the `title` form field; if empty,
returns HTTP 400. Calls `store.Add(title)`, then renders the task-list fragment
via `RenderTaskList`.

**`HandleToggle`** — serves `POST /tasks/{id}/toggle`. Parses `id` from
`r.PathValue("id")`; if not an integer or Toggle returns false, returns HTTP 404.
Renders the task-list fragment via `RenderTaskList`.

**`HandleDelete`** — serves `POST /tasks/{id}/delete`. Parses `id` from
`r.PathValue("id")`; if not an integer or Delete returns false, returns HTTP 404.
Renders the task-list fragment via `RenderTaskList`.

**`main()`** — initializes `store = NewTaskStore()` and `templates = InitTemplates()`,
registers routes on an `http.ServeMux`, and calls `http.ListenAndServe(":8080", mux)`.

**Templates** — two named templates: `"index"` (full HTML page) and `"task-list"`
(the fragment). Templates are inline Go strings in templates.go — no external `.html`
files. The "index" template embeds a call to the "task-list" template via
`{{template "task-list" .}}` so both share a single TaskView render path. Each
task's row element carries `class="done"` when `.Done` is true, and no `done` class
otherwise — this is the literal, required marker (not one of several illustrative
options), since the integration bead asserts on this exact string.

**HTMX wiring** — the "index" template includes the HTMX CDN script in `<head>`:
`<script src="https://unpkg.com/htmx.org@1.9.12"></script>`. The add-task form uses
`hx-post="/tasks" hx-target="#task-list" hx-swap="outerHTML"`. Each task's toggle
button uses `hx-post="/tasks/{{.ID}}/toggle" hx-target="#task-list" hx-swap="outerHTML"`.
Each task's delete button uses `hx-post="/tasks/{{.ID}}/delete" hx-target="#task-list"
hx-swap="outerHTML"`. All three mutations return the "task-list" fragment which
replaces the `#task-list` element in place.

## Cross-Bead Contracts

### task-store → http-handlers (protocol)

- **type**: protocol
- **producer**: task-store (store.go)
- **consumer**: http-handlers (handlers.go, main.go)
- **interface**: `NewTaskStore() *TaskStore`, `(*TaskStore).Add`, `All`, `Toggle`, `Delete`
- **notes**: HandleToggle and HandleDelete parse `{id}` from the URL with
  `r.PathValue("id")` then `strconv.Atoi`. `store` is a package-level `*TaskStore`
  set in `main()` before serving.

### templates → http-handlers (protocol)

- **type**: protocol
- **producer**: templates (templates.go)
- **consumer**: http-handlers (handlers.go, main.go)
- **interface**: `InitTemplates() *template.Template`, `RenderIndex(w, view)`, `RenderTaskList(w, view)`
- **notes**: `templates` is a package-level `*template.Template` set in `main()` via
  `templates = InitTemplates()`. All three render functions use the package-level
  `templates` variable internally. `main()` must call `InitTemplates()` before
  `http.ListenAndServe`.

### http-handlers → templates (data-shape)

- **type**: data-shape
- **producer**: http-handlers (assembles TaskView from store.All())
- **consumer**: templates (templates.go)
- **interface**: `TaskView{Tasks []*Task}`
- **notes**: The "task-list" named template renders a `<div id="task-list">` element
  containing all tasks. This div is the HTMX swap target — it MUST contain every
  piece of state that changes after a mutation (task rows, done/undone styling). Any
  count or status text must also be inside this div, not outside it in the page
  skeleton. Inside `{{range .Tasks}}`, access the loop element's fields directly
  (`.ID`, `.Title`, `.Done`); no `$` prefix needed since TaskView has no other
  fields that need to be accessed inside the range.

## Decomposition Notes

**Required test scenario for task-store bead — ID assignment survives deletion:**
the domain parameters state IDs are monotonically increasing and never reused, but
that rule determines an exact number that must be tested explicitly, not left to
`len(tasks)+1`-style derivation. Worked scenario:
```
s := NewTaskStore()
a := s.Add("A")  // a.ID == 1
b := s.Add("B")  // b.ID == 2
s.Delete(a.ID)   // one task remains: B
c := s.Add("C")  // c.ID == 3, NOT 2
```
After the delete, `len(s.All())` is 1, so an implementation that derives the next ID
from the current slice length (`len(tasks)+1`) would assign `c.ID = 2`, colliding
with the pattern the domain parameters explicitly forbid ("IDs are not reused"). The
`TaskStore` must track `nextID` as its own counter, incremented on every `Add`
regardless of intervening deletes — never derived from `len(tasks)`.

**Template format**: templates.go uses inline Go string literals — no external `.html`
files. `InitTemplates()` calls `template.Must(template.New("").Parse(src))` where
`src` is a constant or var holding the concatenated template definitions. There is no
`templates/` directory. This must be stated in the templates bead spec.

**main.go belongs to http-handlers**: The http-handlers bead owns both `handlers.go`
and `main.go`. `main()` is the only wiring point for `store`, `templates`, and the
`ServeMux` routes.

**Integration bead scenario** (bounded): start an `httptest.NewServer`, POST
`/tasks` with title "Buy milk", assert the response contains "Buy milk", POST
`/tasks/1/toggle`, assert the response contains the substring `class="done"` (the
task-list template renders `class="done"` on a completed task's row element and
omits it otherwise — this is the literal, required marker, not one of several
options), POST `/tasks/1/delete`, assert "Buy milk" no longer appears. The test must
not bind to any fixed port.