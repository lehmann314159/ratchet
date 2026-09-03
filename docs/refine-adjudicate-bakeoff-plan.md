# Build plan: verb bakeoffs for the REFINE_TESTS + ADJUDICATE loop

**Status:** plan — 2026-09-02
**Branch:** `refine-bakeoff`
**Parent design:** `docs/model-qualification-harness.md` (the broad matrix vision).
This doc makes that concrete for four verbs and picks the "real `Run()`" route
over standalone replay.
**Motivating data:** `docs/proposed-ideas.md` §7 — on the baseline-7 run
REFINE_TESTS was 62% of wall-clock, `REFINE_TESTS_CRITIQUE` ~15 min/run, and
`ADJUDICATE_NEXT_EXECUTION` spiralled once to 29 min.

## Scope

Bakeoff harness (`cmd/qualify-model`) for:

| verb | model call | currently | what we're testing |
|------|-----------|-----------|--------------------|
| `REFINE_TESTS_WRITE` | tool loop (`write_function`, `run_go_snippet_case`), `OmitFormat` | gemma4:31b | can a candidate write correct+covering tests |
| `REFINE_TESTS_CRITIQUE` | tool loop (`run_go_snippet_case`), plain | qwen3:32b | catch rate on bad tests, false-positive rate on good, **latency** |
| `REFINE_TESTS_JUDGE` | tool loop (`run_go_snippet`), plain | gemma4:31b | verdict agreement + schema validity + latency |
| `ADJUDICATE_NEXT_EXECUTION` | tool loop (`run_go_snippet`), plain | gemma4:31b | verdict agreement + latency + **spiral rate** |

DECOMPOSE/AUDIT/RECONCILE/SURVEY are out of scope here — separate pass, and
they're schema-mode JSON verbs with a different failure surface.

## Non-negotiable: project 47 stays valid

Three layers, in order of reliance:

1. **The harness never opens the live DB.** It operates on a *copy*:
   `cp ratchet-projects/ratchet.db <workdir>/qual.db` once at start. All
   fixture creation, `verb_model_assignments` writes, throwaway projects, and
   `Run()` calls target `qual.db`. `ratchet-projects/ratchet.db` is read once,
   by `cp`, and never again.
2. **`Run()` only, never `Commit()`.** The replay calls `handler.Run(...)` and
   `handler.Validate(...)`; it never calls `Commit`, so even within `qual.db`
   nothing advances.
3. **Immutable snapshots already exist** as disaster recovery:
   `ratchet-projects/snapshots/ratchet-v0.2-p47-baseline.db` (chmod 444) and
   `exprvm-web-baseline-7-artifact.tar.gz` (chmod 444).

`ratchet save-fixture` is **not** usable on project 47 directly — it renumbers
the project in place (source → negative ID, no copy). `clone-project` leaves the
source untouched but resets mid-flight state and re-enqueues jobs. Neither is
needed given layer 1.

## The model-override mechanism (no `Run()` signature change)

Every verb `Run()` resolves its model via
`loadVerbModel(ctx, d, job.ProjectID, verb)` →
`SELECT model FROM verb_model_assignments WHERE project_id=? AND verb=?`.

So the override is just: **write a `verb_model_assignments` row** for
`(throwawayProjectID, verb, candidateModel)` in `qual.db` before calling
`Run()`. The design doc floated a `Run()` signature change; it isn't required.
`dispatch()`'s warmup lookup is bypassed (we call `Run()` directly), so the
harness does its own `oc.Warmup(candidateModel)` once per model.

## Fixture acquisition — the real cost of this route

`Run()` rebuilds its prompt from `qual.db` + the project folder. To exercise the
verb loop faithfully we need `qual.db` + folder in the state that verb saw at
dispatch. Project 47 is **complete**, so its DB holds only end-state; per-cycle
test-file contents are not in the DB (`test_refinements` stores only turn /
verb / changed / summary / decision) — they're only in the write traces.
Reconstructing each dispatch state from the finished DB + trace parsing is
lossy and per-verb fiddly.

**Decision: one fresh instrumented capture run.** Add an orchestrator flag
`--capture-verb-io <dir>`. A shared helper, invoked at each verb's model-call
site, writes per call:

```
<dir>/NNN-<verb>-p<proj>-b<bead>-c<cycle>.json
   { verb, project_id, bead_id, cycle_id, job_row,
     model, messages[], tools[], opts, folder_snapshot: "NNN-....tar.zst" }
<dir>/NNN-....tar.zst          # project folder at that instant
<dir>/NNN-....rows.sql         # the project-scoped DB rows at that instant
```

Then run **project 48 = a fresh `exprvm-web` baseline** with the flag on. This:
- yields a lossless, complete corpus for **all 13 verbs** at once, not just 4;
- doubles as the muse EXECUTE_BEAD reproducibility run (n=2);
- is ~9h unattended, daemon already idle.

Instrumentation cost: one helper + one call line per verb before its
`oc.Chat` / `oc.ChatWithTools` (~13 sites), gated on the flag being set —
zero overhead when off.

The captured `messages[]` is also a **fidelity check**: the replay asserts that
`Run()`'s freshly-derived prompt equals the captured one (modulo model name),
so fixture drift is caught loudly.

## Replay harness — `cmd/qualify-model`

```
ratchet qualify-model \
  --corpus <dir>            # the --capture-verb-io output
  --verb REFINE_TESTS_CRITIQUE \
  --models qwen3:32b,qwen3.6:35b-a3b,mistral-small3.2:24b \
  --runs 5 \
  --cases b305-c1,b307-c2,b309-c1,b309-c2   # or 'all'
  --ollama http://192.168.50.241:11434 \
  --db ratchet-projects/ratchet.db          # copied, never written
  --out scratchpad/qual/<verb>/
```

Per `(verb, model, case, run)`:
1. `resetWorkdir`: untar the case's folder snapshot into `out/<case>/<model>/<run>/`.
2. Load the case's `rows.sql` into a fresh positive-id project in a per-run
   copy of `qual.db`; upsert `verb_model_assignments(project, verb, model)`.
3. Reconstruct the `*db.HandoffJob` from the captured `job_row`.
4. `oc.Warmup(model)` (once per model, not per run).
5. `t0 := now; raw, err := handler.Run(ctx, runDB, oc, job); dt := now-t0`.
6. `validation, parsed := handler.Validate(raw)`.
7. **No `Commit`.** Discard `runDB`.
8. Record metrics (below) + save `raw`, `parsed`, and the full trace.

Reuse from `internal/execution/bakeoff.go`: `resetWorkDir`, `sanitize`,
`unloadModel`, `splitCSV`, the TSV writer, `printBakeoffTable`. New code is the
DB-rows loader, the job reconstruction, and the per-verb grader.

## Client change: stats return

`ollama.Chat` returns `(string, error)` and `ChatWithTools` returns
`(Message, error)` — neither surfaces Ollama's `eval_count` /
`eval_duration` / `prompt_eval_count` / `prompt_eval_duration` /
`total_duration`. Add a `*Stats` accumulator the harness (and, later, the
orchestrator) can read: an optional out-param or a `ChatStats`/`ChatWithToolsStats`
variant. Small, independently useful (per-verb tok/s in production logs).

## Metrics

Common per `(verb, model)` over N runs × M cases:
- `latency_p50`, `latency_p90` — `Run()` wall-clock
- `gen_tok_p50`, `tok_per_sec`, `prompt_tok` — from `*Stats`
- `turns_p50`, `tool_calls_p50` — tool-loop verbs
- `first_try_valid_rate` — `Validate` == "valid", no retry
- `runaway_rate` — output bytes > 4× median good size for the verb
- `dead_turn_rate` — turns with `done_reason=length` and no content/tool-call
  (the ADJUDICATE spiral signature; needs a counter threaded out of the loop)
- warmup cost (cold-load, first call per model)

### Per-verb grading

**`REFINE_TESTS_WRITE`** — mutation-style, the only real correctness signal:
- generated `*_test.go` must **compile** against the case's real package;
- must **pass** against the known-good implementation (project 47's final
  committed impl for that bead — in the artifact tarball);
- must **fail** against ≥1 seeded-bad impl (hand-write 2–3 mutants per bead:
  off-by-one, wrong operator, dropped nil-check — checked in under
  `testdata/qual-mutants/b<bead>/`);
- `covered_exit_criteria` — every `grep -q 'func TestX'` in `exit_criteria`
  present; every named subtest from the bead spec present.
- Score = `compiles ∧ passes_good ∧ kills_≥1_mutant`, plus a coverage %.

**`REFINE_TESTS_CRITIQUE`** — needs a labelled test-file set:
- from the corpus: cases where the real JUDGE next said `approved`
  (test file was good) vs `revise` (had a real defect). baseline-7 gives
  ~11 labelled points; supplement with the seeded mutants (known-bad).
- `catch_rate` = fraction of known-bad inputs where `all_correct=false`;
- `false_positive_rate` = fraction of known-good inputs where
  `all_correct=false`;
- latency is the headline number.

**`REFINE_TESTS_JUDGE`** / **`ADJUDICATE_NEXT_EXECUTION`** — verdict agreement:
- reference verdict = what gemma decided on that exact case in baseline-7
  (`test_refinements.decision` / `adjudications.decision` + `trend` +
  `bead_spec_fit`);
- `agreement_rate` vs reference (exact decision match);
- `schema_valid_rate` (`Validate` passes);
- for ADJUDICATE: `dead_turn_rate` and `latency_p90` are the point of the
  exercise — we already know gemma's verdicts are *correct*, they're just slow.
- Disagreement isn't automatically wrong (a better model might improve on
  gemma) — flag disagreements for manual review, don't score them as failures
  outright.

## Candidate pools

- **WRITE**: muse-glimmer:30b-q8_0-dflash, gemma4:31b, qwen3.6:35b-a3b
- **CRITIQUE** (must ≠ WRITE model, constraint 5): qwen3:32b (incumbent /
  latency target), qwen3.6:35b-a3b, mistral-small3.2:24b
- **JUDGE / ADJUDICATE**: gemma4:31b, qwen3.6:35b-a3b, mistral-small3.2:24b
  (not muse — schema/JSON-final weak spot; not qwen3:32b for JUDGE if it's
  already CRITIQUE)

## Phasing (time-box each)

| # | step | est. | gate |
|---|------|------|------|
| 0 | isolation scaffold: `qual.db` copy, workdir layout, snapshot check | 0.5 d | — |
| 1 | `ollama.*Stats` return | 0.5 d | client tests green |
| 2 | `--capture-verb-io` helper + wire 4 target verbs (+ the rest if cheap) | 1 d | dry-run against a unit fixture shows well-formed JSON |
| 3 | capture run: project 48 baseline w/ flag | ~9 h wall, ~0.5 d attention | project 48 complete; corpus has ≥1 case per target verb; muse n=2 recorded |
| 4 | `cmd/qualify-model`: DB-rows loader + job reconstruction + `Run()` call + fidelity assert | 1.5 d | replays one CRITIQUE case, `Run()` prompt matches capture |
| 5 | per-verb graders + mutant fixtures (`testdata/qual-mutants/`) | 1.5 d | WRITE grader kills a hand-checked mutant |
| 6 | run the matrix; write `docs/fleet-qualification.md` | 0.5 d + model hours | reproduces known facts (muse ok on WRITE-shaped work; gemma ADJUDICATE slow) |

Steps 0–2 and 4–5 are the build; 3 is unattended; 6 is analysis.

## Open questions

- **`--capture-verb-io` on `EXECUTE_BEAD`**: it's subprocess-based
  (`internal/execution`), not a `verbs.Handler` — its capture point is
  different. Include it in step 2 or leave `exec-bakeoff` as the EXECUTE path?
  Lean: leave exec-bakeoff, capture only the `verbs.Handler` verbs.
- **Mutant authorship**: 2–3 per bead by hand is ~20 mutants. Acceptable one
  time. Could later derive from real failed executions in the stress-test
  history.
- **`qual.db` per-run copy cost**: 9 MB × (models × cases × runs) file copies.
  Fine for SQLite; use a savepoint-rollback instead if it's slow.
- **Reference-verdict staleness**: if a verb's prompt changes after capture,
  the fidelity assert fails and the corpus needs recapture. Same fixture-drift
  problem the stress-test fixtures have — document the recapture step.
- Does the matrix need a second design doc (a game) or is exprvm-web enough for
  a first pass? Start with one.
