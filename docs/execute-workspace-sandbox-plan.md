# EXECUTE_BEAD workspace sandbox — build plan

**Status:** IMPLEMENTED 2026-09-05 (branch `fix/execute-workspace-sandbox`).
All three open questions resolved as recommended (copy-back = `expectedFiles`;
`go.mod`/`go.sum` changes discarded + noted; stale EXECUTE-prompt paragraph
trimmed in this PR). `resetWorkDir` reused as-is rather than renamed, to keep
`bakeoff.go` untouched.
**Origin:** `memory/project_execute_workspace_hygiene`, root-caused during
`exprvm-web-baseline-13` bead 319
(`~/Documents/ratchet-projects/qual-corpus-baseline-13-REPORT.md`; failure trace
`exprvm-web-baseline-13/traces/bead-319-attempt-3.log` lines ~1032–1783).
**Blocks:** a clean full-pipeline baseline for `docs/decompose-precision-plan.md`
(baseline-14). This is the last known structural blocker — baseline-13 reached
bead 7/10, the furthest any `exprvm-web` run has gone, and stopped only here.
**Separate-conversation note:** framework work
(`memory/feedback_separate_framework_from_project_runs`) — its own conversation.

## The bug (mechanically verified, baseline-13 bead 319)

`EXECUTE_BEAD` (`muse-glimmer:30b-q8_0-dflash`) has `write_file` + `run_command`
tools pointed at the **live project folder** (`internal/execution/tools.go`,
`executeTool(ctx, tc, folderPath)` where `folderPath` = `projects.folder_path`).
Mid-execution the model got confused about whether `html/template` escapes `+`
and used those tools to drop scratch programs into the project root:

```
write_file → debug_test.go               (a _test.go file → compiled into the test binary)
write_file → test_escape.go … test_escape5.go   (each: package main { func main() ... })
run_command → go run test_escape.go
```

The files persist after the attempt returns. Every later exit-criteria run then
fails at **compile** (`main redeclared in this block` / `FAIL [build failed]`),
even though `handlers.go` itself was correct by attempt 5. Nothing in the
framework mechanically sweeps non-manifest `.go` artifacts, and
`execute_revised`'s only levers — bead spec prose and the exit-criteria string —
both failed (model ignored / wrote more strays; ADJUDICATE emitted a broken
`rm -f .`). 5 attempts, 4 `execute_revised`, no recovery.

The prose mitigations added in `f093c1d` (2026-06-26) — EXECUTE prompt
"no other file may be created or modified for any reason" + "clear conflicting
declarations", ADJUDICATE "Workspace repair" — are the first fully-captured run
where **all of them fail at once**. The class is model-agnostic and structural:
any model that decides a repro program is worth writing hits it.

## Root cause

`EXECUTE_BEAD` has unfiltered write access to a shared mutable workspace that
every downstream verb (in-loop exit check, `ANALYZE_EXECUTION`, the next bead's
`REFINE_TESTS`) then trusts. Every fix that isn't "constrain that access" treats
symptoms and has to be repeated at each compile site.

## Fix: sandbox EXECUTE's scratch work in a per-attempt work dir

`runExecuteBeadReal` (`internal/execution/bead.go`) is a subprocess
(`ratchet execute-bead`, spawned by `RunExecutionWindow`). Change: before the
turn loop, seed a temp work dir from the live folder; run the whole loop there;
on **every** exit path copy back only the files the model was allowed to write
this attempt.

### Mechanics

1. **Seed.** `dir, _ := os.MkdirTemp("", "ratchet-execbead-*")`, then copy the
   live folder into it minus the top-level `traces/` subtree (reuse
   `resetWorkDir` from `bakeoff.go` — same package). Record the set of seeded
   relpaths (`execWorkspace.seedSet`).
   - Seeding from the **live folder** (not a pristine snapshot) is correct with
     no VCS in the tree: the live folder already holds every prior bead's code +
     this bead's scaffold stubs + the previous attempt's copied-back output
     files. Strays are structurally impossible because the *previous* attempt's
     copy-back never wrote any (see step 4). Pre-existing strays from before
     this feature ships are still caught by `checkUndeclaredFiles` in
     `ANALYZE_EXECUTION`; not this change's concern.
2. **Point the loop at the sandbox.** Everything model-facing takes `dir`
   instead of `folderPath`: `loadContextFiles`, `isTestFirstMode` /
   `isTestsLockedMode`, `buildBeadUserMsg`, `guidance.InjectForVerbPath`,
   `executeTool`, and the in-loop `execcheck.VerifyExitCriteria`
   (`bead.go:236`). The trace file stays on its absolute path in the live
   folder — unchanged.
3. **GOCACHE / module cache.** No env changes: `run_command` and the exit-check
   inherit `os.Environ()`, so `GOCACHE` / `GOMODCACHE` already point at the
   ambient caches. `go.sum` is seeded; `exprvm-web` is stdlib-only, so no
   network. (If a future project needs a fresh dep, see open question 2.)
4. **Copy back on every exit path.** A `flush()` closure, `defer`-registered
   before `defer ws.cleanup()` so it runs first:
   - Copy each **writable** output file (`expectedFiles` — the this-attempt list
     already computed at `bead.go:154`, i.e. test files in test-first mode, impl
     files in tests-locked mode, all output files otherwise) from the sandbox to
     the live folder, if it exists in the sandbox.
   - Walk the sandbox; any regular file whose relpath was **not** in the seed
     set and **not** in the writable list = a stray the model created. Do not
     copy it. Collect the names.
   - Any seeded file the model modified that is **not** in the writable list
     (sibling `.go`, `go.mod`, `go.sum`, …) — compare bytes against the seed
     copy; if changed, do not copy back, collect the name.
   - Write one trace line:
     `[workspace] discarded N file(s) written/modified outside output_files (not copied to project): <names>`
   - `defer ws.cleanup()` then `os.RemoveAll`s the temp dir.
   - Hard SIGKILL (monitor-fire + grace exceeded) skips defers → partial work
     lost. Acceptable: monitor fired because the work was unproductive, and the
     pre-sandbox behavior on SIGKILL mid-write was a half-written live file,
     which is worse. The budget-timeout hard stop is a `cancel()`, not a kill —
     defers run, partial progress is preserved as the EXECUTE prompt promises.
5. **`ANALYZE_EXECUTION` note (keep regardless).** New
   `checkDiscardedWorkspaceFiles(traceData)` greps the trace for the
   `[workspace] discarded` line and appends it to `mechanicalFindings` as
   `workspace note (non-blocking): …`. Report, not mutation — behavioral signal
   about the model's process. No prompt change: discarded files no longer break
   compile, so ANALYZE won't escalate on them.

### What this also closes

- **EXECUTE modifies a sibling / regenerates `main.go`** (scaffold-adjacent,
  baseline-3/4): copy-back-whitelist discards it automatically, and the note
  itemizes it.
- **`run_command` side effects generally** — a broken `//go:build` tag on a
  real file, a `testdata/` dir, a `go.mod` edit — all contained, none reach the
  live tree.

### Blast radius / non-interactions (audited)

| Path | Interaction |
|---|---|
| `exec-bakeoff` (`bakeoff.go`) | none — `bakeoffRunOne` already runs each (spec,model) in its own `resetWorkDir` copy; it doesn't call `runExecuteBeadReal`. `resetWorkDir` reused unchanged. |
| `rewind-bead` / `clone-project` re-scaffold | none — they prep the live folder before any execution; the next `execute-bead` seeds from that prepped state. |
| verb-io capture | none — `EXECUTE_BEAD` already excluded from capture. |
| MONITOR subprocess | none — reads the trace file in the live folder, never source. |
| `traces/` dir | excluded from the seed → a model `read_file("traces/…")` fails in-sandbox (minor, beneficial). |
| Other verbs | none need sandboxing — audited in `project_execute_workspace_hygiene` ("Do other verbs need sandboxing? No"). EXECUTE is the only verb with model write+exec on a shared surface. |
| Language coupling | none — seed/copy-back/detection are all language-neutral (tree copy + relpath sets + byte compare). No `project.Language` switch needed. `exprvm-web`-style Go projects are the only territory today anyway (`scaffoldProject` errors on non-Go). |

### Regression coverage (new tests, `internal/execution`)

Driven through `runExecuteBeadReal` against an `httptest` fake Ollama, same
harness as `no_write_test.go`:

1. **Stray discarded / next attempt clean** — fake model calls
   `write_file(game.go)` then `write_file(scratch.go, "package main\nfunc main(){}")`.
   Assert: `live/game.go` present, `live/scratch.go` **absent**, trace contains
   `[workspace] discarded 1 file`. Run a second execution; assert its sandbox
   seed (and the live folder) contain no `scratch.go`.
2. **Partial progress survives a timeout** — fake model writes `game.go`, then
   the budget fires (tiny budget) before "done". Assert `live/game.go` has the
   written content after `termination_cause='timeout'`.
3. **Sibling unmodified on copy-back** — seed `live/sibling.go`; fake model
   writes `game.go` and also `write_file(sibling.go, "corrupt")`. Assert
   `live/sibling.go` is byte-identical to the seed and the trace itemizes it.

Plus unit tests on the extracted `seedWorkDir` (rename; existing
`TestResetWorkDirExcludesTraces` moves with it) and the copy-back/detect helper
in isolation.

Full suite + `go vet` green before the merge request.

## Design decisions (resolved at sign-off, 2026-09-05)

1. **Copy-back whitelist = `expectedFiles`** (this-attempt-writable), not full
   `output_files`. Stricter — also discards an illegal write to a
   REFINE_TESTS-locked test file. No downside: a legitimately locked file is
   re-seeded from the live copy each attempt, so its content is never lost.
2. **`go.mod` / `go.sum` changes made in-sandbox are discarded** (preserve the
   "only declared output files" invariant), with a loud `[workspace] discarded`
   line naming them. `exprvm-web` is stdlib-only so this is moot today; if a
   future project legitimately needs `go get` we revisit (declare `go.mod` in a
   bead's `output_files`, or add a dep-manifest step).
3. **Temp-dir leak on SIGKILL** accepted. `os.MkdirTemp` dirs leak if the
   subprocess is hard-killed (same exposure as `run_go_snippet`). A startup
   sweep of stale `ratchet-execbead-*` dirs can be added later if it matters.
4. **Stale EXECUTE-prompt text trimmed in this PR.** The "clear conflicting
   declarations left by a previous attempt" paragraph is impossible once strays
   can't persist; replaced with a short note that out-of-list writes are
   discarded.

## After merge

- PR; update `memory/project_execute_workspace_hygiene` status → IMPLEMENTED.
- `MEMORY.md`: note baseline-14 (clean full-pipeline DECOMPOSE baseline) is
  unblocked.
