# Fleet qualification — REFINE_TESTS + ADJUDICATE bakeoff

**Status:** harness built (`ratchet qualify-model`), matrix in progress
**Branch:** `refine-bakeoff`
**Corpus:** `~/Documents/ratchet-projects/qual-corpus-p48/` (project 48 = exprvm-web
baseline-8, captured 2026-09-02 with `--capture-verb-io`; immutable copy at
`ratchet-projects/snapshots/qual-corpus-p48.db`)
**Plan:** `docs/refine-adjudicate-bakeoff-plan.md` (this doc is its step 6 output)

## What the harness does

`ratchet qualify-model` replays a captured verb dispatch against a candidate
model by calling the verb's real `Run()`, then `Validate()`, **never `Commit()`**.

- **Input state** = the per-dispatch `db.sqlite` (a whole-DB `VACUUM INTO`
  snapshot taken at dispatch time) + `folder/` copy. Per run the harness copies
  both, patches `verb_model_assignments(project, verb, candidate)` and
  `projects.folder_path`, reconstructs the `*db.HandoffJob` from `meta.json`,
  and runs the handler. The live `ratchet.db` is never opened.
- **Fidelity assert** — the replay's first model call's `messages[]` must equal
  the captured `call-001.json` `messages[]` (modulo model name). A mismatch
  means the verb's prompt-building drifted from the corpus; the run is flagged.
  `--fidelity-only` checks this for every case with a single (aborted-after-one-
  call) replay.
- **Timing/token stats** come from the `ollama.CallRecorder` hook (already
  parsed into `CallRecord` — `eval_count`, `eval_duration`, `prompt_eval_*`,
  `done_reason`), plus `Run()` wall-clock. No ollama client return-path change.
- **Grammar-constraint sweep** — `--omit-format` drops the `format` grammar
  (json string / flat schema) from every model call, and `--think on|off`
  overrides the reasoning flag, both via a harness-only `ollama.ChatOverride`
  context value (nothing in the pipeline installs one). This lets a candidate
  reasoning model be evaluated on the tool-loop verbs (CRITIQUE / JUDGE /
  ADJUDICATE all hard-code `format:"json"` on every turn, which suppresses a
  reasoning model's separate thinking phase). Run the matrix once per config;
  the config is recorded in `config.txt` and the table title.

## Per-verb grading

| verb | rubric | baseline |
|------|--------|----------|
| `REFINE_TESTS_WRITE` | generated test **compiles** ∧ **passes** the known-good impl ∧ **kills ≥1** hand-authored mutant; + required-`Test*`-func coverage | mutation fixtures in `internal/qualify/testdata/qual-mutants/b<bead>/` |
| `REFINE_TESTS_CRITIQUE` | catch-rate on known-bad test files, false-positive rate on known-good; **latency is the headline** | label = what the real JUDGE decided that cycle (`approved`→good, `revise`→bad), from `corpus.db` |
| `REFINE_TESTS_JUDGE` | verdict agreement + schema-valid rate + latency | `test_refinements.decision` in `corpus.db` |
| `ADJUDICATE_NEXT_EXECUTION` | verdict agreement + **`dead_turn_rate`** (done_reason=length, no content, no tool call — the spiral signature) + **`latency_p90`** | `adjudications.decision` in `corpus.db`, ordered by row id == captured dispatch order |

Disagreement with the gemma baseline is **flagged for review, not auto-scored as
wrong** — a stronger model may improve on it.

### Mutant fixtures

`ratchet qualify-model scaffold-mutants --corpus … --artifact …` extracts each
WRITE bead's known-good impl from the baseline artifact tarball into
`b<bead>/good/`. Mutants (`b<bead>/m1_*/`, `m2_*/`, …) are hand-authored — one
realistic defect per dir (off-by-one, wrong operator, dropped nil-check).

Authored so far:
- **b313 (lexer)** — 3 mutants: number scan drops last digit; error path advances
  position; newline emits `TokenEOF`.
- b314–b318 — `good/` scaffolded, mutants TODO.
- b320/b321 (integration beads) — no impl files, mutation grading N/A
  (compile + coverage only).

## Candidate pools

- **WRITE**: `gemma4:31b` (incumbent), `muse-glimmer:30b-q8_0-dflash`, `qwen3.6:35b-a3b`
- **CRITIQUE**: `qwen3:32b` (incumbent / latency target), `qwen3.6:35b-a3b`, `mistral-small3.2:24b` — ×{format=json, format=omitted}
- **JUDGE / ADJUDICATE**: `gemma4:31b` (incumbent), `qwen3.6:35b-a3b`, `mistral-small3.2:24b` — ×{format=json, format=omitted}

## Running the matrix

```
BOX=http://192.168.50.241:11434
CORPUS=~/Documents/ratchet-projects/qual-corpus-p48
OUT=~/Documents/ratchet-projects/qual-results

ratchet qualify-model --corpus $CORPUS --verb REFINE_TESTS_WRITE \
  --models gemma4:31b,muse-glimmer:30b-q8_0-dflash,qwen3.6:35b-a3b \
  --cases b313-c1 --runs 3 --ollama $BOX --out $OUT/write

ratchet qualify-model --corpus $CORPUS --verb REFINE_TESTS_CRITIQUE \
  --models qwen3:32b,qwen3.6:35b-a3b,mistral-small3.2:24b \
  --cases b313-c1,b314-c1,b316-c1,b317-c1 --runs 2 --ollama $BOX --out $OUT/critique-json
ratchet qualify-model --corpus $CORPUS --verb REFINE_TESTS_CRITIQUE --omit-format \
  --models qwen3:32b,qwen3.6:35b-a3b,mistral-small3.2:24b \
  --cases b313-c1,b314-c1,b316-c1,b317-c1 --runs 2 --ollama $BOX --out $OUT/critique-omitfmt

# JUDGE, ADJUDICATE similarly
```

Sequential; one model in box VRAM at a time; `--unload` (default on) nudges each
out when done. Budget it like the exec-bakeoff — ~1 day of box time for the
full matrix, mostly CRITIQUE.

## Results

Matrix launched 2026-09-03 05:05 MT → `~/Documents/ratchet-projects/qual-results/`.
Sections below fill in as it completes. Harness-validation ("thin") runs from
before the matrix are recorded inline.

### Fidelity

Every replay so far (`CRITIQUE`, `WRITE`, `JUDGE`, `ADJUDICATE`) rebuilt a
byte-identical prompt to its capture — `fidelity match=true`. The corpus is not
stale against `refine-bakeoff` HEAD.

### REFINE_TESTS_WRITE

b313 (lexer) only — the one bead with authored mutants. n=3.

| model | rubric | lat p50 / p90 | turns | tok/s | dead-turn rate | notes |
|-------|--------|---------------|-------|-------|----------------|-------|
| **muse-glimmer:30b-q8_0-dflash** | **3/3** | 243s / 273s | 2 | 18 | 0 | compiles, covers, passes good, kills 3/3 mutants — every run |
| qwen3.6:35b-a3b | 2/3 | 245s / **807s** | 3 | 64 | **0.67** | 1/3 runs wrote a test that fails vs the correct impl; 2/3 runs hit a `done_reason=length` dead turn; one run spiralled to 13 min |
| gemma4:31b | 0/1 | 592s | 2 | 9 | 0 | compiles + covers but **wrong assertions** (fails vs good impl); 9.9 min, 9 tok/s. An earlier ad-hoc run spiralled past 25 min without finishing — this one completed but still failed the rubric |

- **muse-glimmer wins WRITE** on this bead: 3/3 correct, covering,
  mutation-killing tests; stable 2-turn loop; no dead turns. It also answers the
  plan's open "test quality unknown" for muse — the tests are genuinely good,
  not just syntactically valid. On b313 the incumbent **gemma went 0/1** (wrong
  assertions, 9.9 min) and qwen3.6 2/3.
- **qwen3.6** is fast *when it works* but has a ~33% wrong-test rate here (the
  recurring bug: it doesn't know `Next()` auto-skips leading whitespace, so it
  calls `Next()` twice and asserts on the wrong token) plus a real spiral risk.
- Caveat: b313 is the simplest bead. b314–b318 need mutants before this
  generalizes — but the muse/qwen3.6 split is consistent with the EXECUTE_BEAD
  bakeoff.

### REFINE_TESTS_CRITIQUE

**Labelling correction (2026-09-03):** the grader first keyed the good/bad label
on the *next JUDGE* decision. That's wrong — JUDGE catches things CRITIQUE
doesn't. `CritiqueLabel` now needs the baseline CRITIQUE's own `all_correct` to
agree with JUDGE:
- **bad** = baseline CRITIQUE flagged ∧ JUDGE revised — a real defect CRITIQUE catches → b314-c1, b320-c1, b321-c1
- **good** = both clean → b317-c1, b314-c2, …
- **ambiguous** = they disagree (b313-c1: CRITIQUE flagged, JUDGE overrode; b316-c1: only JUDGE caught) — recorded, not scored.

**`format:json` config, corrected labels** — bad: b314-c1, b320-c1, b321-c1;
good: b317-c1, b314-c2. n=2 each.

| model | catch (real defects) | false-pos | rubric | lat p50 | **thinking/turn** | turns | dead-turn |
|-------|----------------------|-----------|--------|---------|-------------------|-------|-----------|
| **qwen3:32b** (incumbent) | **4/6** — b320 2/2, b321 2/2, b314-c1 0/2 | **0/4** | 80% | **804s (13.4 min)** | 40–230s | 6 (cap, every run) | 0 |
| qwen3.6:35b-a3b | **0/6** (one self-contradicting output) | 0/4 (+1 malformed) | 40% | 173s | shorter | 3.5 | **30%** |
| mistral-small3.2:24b | 0/6 | — | 0% | 128s | ~none | 6 | 0 |

**Read:**
- **qwen3:32b actually catches ~67% of real defects with zero false positives** —
  better than the incumbent's reputation. Its one blind spot is b314-c1, a parser
  *spec-interpretation* defect ("test asserts an error for single-newline input,
  but the spec allows it") — it verified the runtime behavior and still concluded
  the assertion was fine. That class needs judgment no model in the pool has.
- **qwen3.6 is disqualified on judgment** — 0/6 on real defects, 30% dead-turn
  rate, occasionally emits `all_correct:true` with "1 problem found" in the same
  summary. Fast and useless.
- **mistral is unusable with `format:json`** — 10/10 malformed (empty summary).
  The `--omit-format` run tests whether the grammar is the cause.

**Cost is the reasoning stream, not the loop mechanics.** `eval_count` only
counts answer tokens; the corpus per-turn stats show qwen3:32b thinks 40–230s per
turn (1.5k–9.7k chars) then emits ~130 answer tokens in ~14s. Ollama's KV cache
*is* reused across turns (prompt_eval stays ~1–3s as history grows). So the
13-min cost = 6 forced verification turns × a long think each. The lever is
**fewer forced turns** (the mechanical-pre-pass redesign), not a model swap or a
grammar change.

### REFINE_TESTS_JUDGE

| model | case | baseline | verdict | agree | wall |
|-------|------|----------|---------|-------|------|
| qwen3.6:35b-a3b | b314-c1 | revise | approved | **no** | 85s |

qwen3.6 approved a test gemma sent back for revision — flagged for manual review,
not scored wrong outright. Full matrix in progress.

### ADJUDICATE_NEXT_EXECUTION

| model | case | baseline | verdict | agree | dead turns | wall |
|-------|------|----------|---------|-------|-----------|------|
| qwen3.6:35b-a3b | b314.0 / b314.1 | execute_revised | execute_revised | yes | 0 | ~80s |

Full matrix in progress.

## Recommendations

_TBD — pending matrix completion._

Early reads: gemma4:31b's thinking-spiral pathology is not WRITE-specific
(reproduced here on the first WRITE turn); qwen3.6:35b-a3b is fast on every verb
but its judgment needs the full matrix before trusting it anywhere.
