# REFINE_TESTS_CRITIQUE redesign — mechanical pre-pass

**Status:** design — 2026-09-03. Not implemented.
**Branch:** off `main` after `refine-bakeoff` merges (the qualify-model harness +
fleet switch land first).
**Motivating data:** `docs/fleet-qualification.md` — the CRITIQUE bakeoff.
**Related:** the noise-tolerant re_refine loop (item 2 — companion doc TBD; make
CRITIQUE+JUDGE a cheap filter, lower/sharpen the ADJUDICATE `re_refine`
threshold, add a cheaper targeted re_refine path that skips CRITIQUE/JUDGE). This
doc is item 1. The two are designed together but implemented in order.

## Problem

The bakeoff produced a **negative result**: no candidate model beats the
incumbent `qwen3:32b` at CRITIQUE (67% real-defect catch, 0 false positives).
qwen3.6 got 0/6; mistral produced malformed output every run. **CRITIQUE's
problem is not the model.**

Two things are wrong with the verb as designed:

1. **Cost.** ~13 min/run. The incumbent hits the 6-turn snippet-verification cap
   on *every* run and spends 40–230s per turn on a reasoning stream (invisible in
   `eval_count` — it only shows in `total_duration`). Ollama's KV cache is reused
   across turns, so this is generation cost, not prompt re-eval. 6 forced
   verification turns × a long think each.

2. **Blind spot.** The incumbent reliably catches *concrete* defects (wrong
   expected values, error-string mismatches — verified via `run_go_snippet`) but
   misses *spec-interpretation* defects ("the test asserts an error for
   single-newline input, but the spec allows it" — b314-c1, 0/4 across both
   bakeoff runs). No model in the pool catches that class.

From a corpus analysis of 36 real CRITIQUE catches across all projects in
`corpus.db`: **~2/3 are mechanically or execution-detectable** —
nil-return→panic (~5), stub-returns-zero confirmed by `run_go_snippet` (~13),
assertion on a spec-silent field (~6). Only ~8–10 are genuine spec-contradiction
needing a model. CRITIQUE currently has an LLM slowly rediscover, through a
forced tool loop, things a 30-second deterministic pass would surface.

CRITIQUE is also **the primary test-defect detector** in the pipeline (of 27
corpus cycles JUDGE sent back, CRITIQUE had flagged 24; JUDGE independently
caught 3), so it can't just be removed — it has to get cheaper and keep its
catch rate.

## Design

Split CRITIQUE into a **mechanical pre-pass** and a **bounded model turn**. Same
pipeline position, same output schema (`RefineTestsCritiqueOutput`:
`findings[]`, `verified_functions[]`, `all_correct`, `summary`).

### 1. Mechanical pre-pass

Runs in the project folder as it exists at CRITIQUE dispatch: the test file (from
WRITE) is present, the current bead's impl files are scaffold stubs, prior beads'
impls are real, and the package compiles.

| step | how | produces |
|------|-----|----------|
| **compile** | `go test -c -o /dev/null .` in the folder | compile errors in the test file |
| **run** | `go test -run <required funcs> ./... -count=1` | per-subtest pass/fail, panic traces, failure messages |
| **spec cross-check** | parse the test AST for asserted literal values / referenced constants / struct field names; compare against tokens present in the bead spec `full_text` | assertions on identifiers/values the spec never names |
| **setup consistency** | test-AST analysis: which subtests in the file touch which package-level vars / call which setup helpers | one subtest inits state a sibling in the same file dereferences without initializing (the panic-before-assertion pattern) |

### The crux: stub-failure vs test-defect

At CRITIQUE time *everything* fails against the stub. The pre-pass classifies
each failure:

| signal | classification | seed finding? |
|--------|----------------|---------------|
| compile error in the test file | test defect | **yes**, high confidence |
| panic (nil deref, index OOB) before any assertion runs | bad assumption / missing setup | **yes** |
| one subtest has setup a sibling lacks → sibling panics | inconsistent setup | **yes** |
| `expected <specific value>, got <zero value>` | expected stub behavior | **no** |
| — *and* that expected value contradicts the spec | wrong expected value | **yes** |
| `expected A, got B`, B non-zero | stub produced something unexpected | **flag for the model**, not a seed |

Verified against corpus b316-c1: scaffold `Compile(){return nil,nil}` → every
subtest fails "expected N instructions, got 0", no panics, compiles clean →
pre-pass emits **no seeds** ("nothing mechanically wrong"), matching the baseline
CRITIQUE's `all_correct: true`. The pre-pass stays quiet on a clean test.

### 2. Reshaped model turn

**Input:** bead spec + design-doc excerpt + test file + **the seed findings with
evidence** (`TestX/y panics: nil deref at line N`, trace attached).

**Prompt (shape):**
> Here are N mechanically-detected concerns, each with evidence. For each:
> confirm it is a real defect, or explain why it is a false alarm. Then do one
> open-ended pass for spec-contradiction defects the checks can't see — a
> plausible-but-wrong expected value, an assertion that contradicts what the
> spec allows. Output `findings` + `verified_functions` + `all_correct` +
> `summary`.

- `run_go_snippet` still available for the model to verify a specific runtime
  claim — but **not mandatory per-subtest** (the pre-pass already ran the
  tests). One optional verification, not 6 forced turns.
- The mechanical seeds carry their own evidence, so the model auto-confirms most
  of them rather than re-deriving. The model's real work is the
  spec-contradiction residual.

### JUDGE consumption

Unchanged interface — JUDGE still receives CRITIQUE's raw output. But the seed
findings arrive with mechanical proof attached, which feeds the
noise-tolerant-loop work (item 2): JUDGE can auto-ratify a proven finding
instead of deliberating.

## Model re-bakeoff

The bakeoff disqualified qwen3.6/mistral for *open-ended* CRITIQUE. Bounded
"verify these N seeds + one focused pass" is a different, easier task — re-run
the CRITIQUE bakeoff against the redesigned verb before concluding the model
choice. Candidates: qwen3:32b (incumbent), qwen3.6:35b-a3b, mistral-small3.2:24b,
plus the `--omit-format` sweep.

## Grading

Against the **current** `qual-corpus-p48` — no recapture needed. The redesigned
`Run()` consumes the same inputs (spec, doc, prior impls, test file, scaffold
impls), all captured. Metrics:
- same catch-rate / false-positive on the labelled cases (bad: b314-c1, b320-c1,
  b321-c1; good: b317-c1, b314-c2)
- **new:** per mechanical seed, did the model correctly confirm/reject it
- latency, turns, `thinking_secs`

Fidelity assert **disabled for CRITIQUE** — the prompt legitimately changes.

## Open questions / risks

- **Spec cross-check is the hard mechanical piece.** "What values does the spec
  pin down" from free-ish `full_text` is fuzzy. Start conservative: only flag
  asserted constants/fields/values *entirely absent* from the spec text; a false
  negative here just means the model does that work, same as today. Expand once
  the conservative version is validated.
- **Setup-consistency check needs real test-AST analysis** (which subtests touch
  which package vars). Bounded but non-trivial. Could be phase 2.
- **Does any check need the real impl?** So far no — stub + spec suffices for
  compile / panic / spec-cross-check. If a check turns out to need it, that's an
  end-to-end recapture, not a corpus problem.
- **Panic-against-stub ambiguity.** A test that panics because the stub returns
  nil *might* be fine against the real impl. The pre-pass surfaces the panic +
  location and lets the model judge "incomplete stub (fine) vs bad test
  assumption (defect)" — it does not auto-fail.

## Phasing

| # | step | gate |
|---|------|------|
| 1 | mechanical pre-pass: compile + run + classify (skip spec-cross-check + setup-consistency) | on corpus b316-c1 emits 0 seeds; on a known-panic case emits the panic seed |
| 2 | reshaped prompt + loop; drop the mandatory per-subtest gate | replays a corpus CRITIQUE case, valid output, model confirms the seeds |
| 3 | spec cross-check (conservative) + setup-consistency | catches b314-c1's class on ≥1 case, or documented why not |
| 4 | re-bakeoff the model choice against the new verb | pick a model |
| 5 | (with item 2 done) one fresh baseline capture — end-to-end `re_refine` leakage | leakage ≤ current, loop cost down |
