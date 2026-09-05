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

**2026-09-03 (b313 only, n=3):** see below — superseded by the 2026-09-04 full
run, kept for the raw dead-turn-rate numbers on repeated attempts of one bead.

| model | rubric | lat p50 / p90 | turns | tok/s | dead-turn rate | notes |
|-------|--------|---------------|-------|-------|----------------|-------|
| **muse-glimmer:30b-q8_0-dflash** | **3/3** | 243s / 273s | 2 | 18 | 0 | compiles, covers, passes good, kills 3/3 mutants — every run |
| qwen3.6:35b-a3b | 2/3 | 245s / **807s** | 3 | 64 | **0.67** | 1/3 runs wrote a test that fails vs the correct impl; 2/3 runs hit a `done_reason=length` dead turn; one run spiralled to 13 min |
| gemma4:31b | 0/1 | 592s | 2 | 9 | 0 | compiles + covers but **wrong assertions** (fails vs good impl); 9.9 min, 9 tok/s. An earlier ad-hoc run spiralled past 25 min without finishing — this one completed but still failed the rubric |

**2026-09-04, full run — b313/314/315/316/317/318, n=1 each, post turn-budget fix
(`internal/verbs/refine_tests.go`, see [[project_refine_tests_loop_cost]]).**
`good/` now exists for all 6 beads (only b313 has seeded mutants). Corpus
`qual-corpus-p48`. Grading corrected below for two fixture defects the run
itself exposed and that are now fixed
(`internal/qualify/testdata/qual-mutants/b314/good/parser.go`,
`.../b318/good/handlers.go` — see "Fixture defects found" below); the raw table
in `scratchpad/qual-write-bakeoff/table.txt` predates that fix and undercounts
gemma and muse.

| model | genuinely correct | real defects | catastrophic (silent or timeout) | lat p50 | dead-turn rate | total thinking chars |
|---|---|---|---|---|---|---|
| **muse-glimmer:30b-q8_0-dflash** | **6/6** | 0 | 0 | 332s | 0% | 15,577 |
| gemma4:31b (incumbent) | 3/6 (b314, b315, b316) | 1 (b313 — asserts error *identity*, not content; not a real spec requirement) | 2 (b317, b318 — 30-min ceiling, zero output) | 865s | 0%* | 15,131 |
| qwen3.6:35b-a3b | 3/6 (b313, b315, b316) | 1 (b318 — fragile HTML-substring check against accumulated global history, false-matches leftover content from earlier subtests) | 2 (b314, b317 — **silent**: 4/4 turns zero tool calls, giant thinking-only streams up to 44K chars, fell through to the generic fallback summary with `validation=valid`) | 499s | 67% | 92,474 |

*gemma's failures were categorized as `run_err` timeouts by the harness rather
than its narrower dead-turn metric; behaviorally it's the same
zero-forward-action pathology qwen3.6's 67% captures.

- **muse-glimmer wins outright, cleanly.** Fastest median latency, zero dead
  turns, zero catastrophic failures, and it independently converged (twice,
  once for b316 and once as qwen3.6 also found) on testing the compiler
  *through the real parser* (`NewParser(...).ParseProgram()` → `Compile(...)`)
  rather than hand-constructing AST literals — sidestepping the
  pointer-vs-value construction-form ambiguity that is otherwise a recurring
  defect class in this project (see `docs/decompose-precision-plan.md`).
- **gemma's real record is worse than raw mechanical validity suggested.**
  4/6 runs were "valid" (compiled, no crash) but only 3/6 were actually
  correct — b313's assertion is over-strict on an implementation detail the
  spec never requires. The other two (b317, b318 — the two most complex beads)
  hit the harness's 30-minute per-replay ceiling having written nothing, the
  same single-giant-thinking-stream pathology already noted below.
- **qwen3.6 is fast when it engages, but fails harder and more silently than
  gemma on complex beads.** Its two failures on b314/b317 are **not** loud
  timeouts — the call returns normally with `validation=valid, run_err=""` and
  a generic fallback summary; only inspecting the actual output file reveals
  nothing was written. This is a worse failure signature than a timeout for
  anyone relying on the top-line replay result. Consistent with the 2026-09-03
  b313 dead-turn finding (33% rate there too) — this is a real, recurring
  qwen3.6 weakness on WRITE, not a fluke.
- **Fixture defects found and fixed during this run** (both discovered because
  a genuinely-good test "failed" grading and warranted tracing to root cause,
  not written off as a model mistake):
  - `qual-mutants/b314/good/parser.go` constructed expression-level AST nodes
    (`BinaryExpr`, `NumberExpr`, `VarExpr`, `UnaryExpr`) as **pointers** while
    statement-level nodes (`ExprStmt`, `AssignStmt`, `PrintStmt`) were
    **values** — internally inconsistent, and the design doc's own worked
    example uses values throughout. Fixed to value construction at both
    levels. This is the same pointer-vs-value defect class independently found
    live on exprvm-web-baseline-9's b314 `re_refine`.
  - `qual-mutants/b318/good/handlers.go` set `entry.Output = outBuf.String()`
    without trimming, violating the design doc's explicit "trailing '\n'
    trimmed" rule. Fixed to `strings.TrimSuffix(outBuf.String(), "\n")`. Same
    recurring output-trim ambiguity documented in `project_refine_precision_phase0`'s
    Phase 0 retrospective (the dominant historical defect class across this
    project's whole corpus) — finding it baked into the trusted reference
    implementation itself is a strong confirmation of how sticky that gap is.

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

Cases b313-c1 (approve), b314-c1 / b316-c1 / b320-c1 (revise). n=2. The grammar
sweep ran the two fast models × {schema, omit-format}; gemma × {schema,
omit-format}.

| config | agreement | dead-turn | wall p50 |
|--------|-----------|-----------|----------|
| gemma4:31b + schema (incumbent) | **62%** | 0% | 9 min |
| gemma4:31b + omit-format | 50% | 0% | 9 min |
| qwen3.6:35b-a3b + schema | 50% | **12%** | 1.5 min |
| **qwen3.6:35b-a3b + omit-format** | **62%** | 0% | **1.7 min** |
| mistral + schema | 0/8 (never calls run_go_snippet → hard Run error) | — | — |
| mistral + omit-format | 50% | 0% | 1.2 min |

- **The grammar's effect is model-specific.** gemma *needs* the field-presence
  schema (+12 pts — it omits `summary` without one). qwen3.6 *fights* it (−12
  pts, 12% dead turns). mistral is *blocked* by it (can't emit the tool call).
- **qwen3.6 + omit-format = gemma + schema quality (62%) at 5× the speed**, 0
  dead turns.
- **JUDGE is unreliable regardless of model.** gemma-replay disagreed with
  gemma-*baseline* (same model) — b316-c1 approved 2× vs baseline `revise`;
  b314-c1 split. b316-c1 is one of only 3 corpus cases where JUDGE independently
  caught what CRITIQUE missed — **no model reproduces it, in any config.** JUDGE's
  independent-detection value is ≈ noise.
- b320-c1 (large integration test file): gemma malformed at the turn cap 2×
  under the schema — the loop returns non-verdict `lastContent`. Verb robustness
  gap, tracked separately.

### ADJUDICATE_NEXT_EXECUTION

Cases: p48's 6 adjudication dispatches (b313, b314×2, b315–b317). n=2.

| model | agreement | dead-turn | wall p50 |
|-------|-----------|-----------|----------|
| gemma4:31b (incumbent) | **12/12** | 0% | 230s |
| **qwen3.6:35b-a3b** | **12/12** | 0% | **48s** |
| mistral-small3.2:24b | 0/12 (malformed — empty `trend`) | 0% | 118s |

- **gemma and qwen3.6 agree perfectly** — with the baseline and each other, every
  case, both runs, exact on decision *and* trend. qwen3.6 4–5× faster, and its
  reasoning is genuine (runs `run_go_snippet`, reasons about the result), not
  rubber-stamped.
- **This is the "evidence makes the model's job easy" pattern.** ADJUDICATE is
  handed ANALYZE's `mechanical_findings` + execution termination causes; 5 of 6
  cases are nearly mechanically determined (`declare_success`), 1 is a clear
  `execute_revised`. Contrast CRITIQUE (open-ended detection, no mechanical input)
  where the fast models collapse.
- **Big gap: all 6 cases are easy.** p48 was a clean run — no `re_refine`, no
  `full_stop`, no spirals (the 29-min baseline-7 case). This corpus can't test
  ADJUDICATE's hard decisions. Needs a messy-EXECUTE capture (gemma-EXECUTE
  baseline) for a follow-up bakeoff.
- mistral out for ADJUDICATE too — same empty-required-field malformation.

## Recommendations

### Applied to the default fleet (`internal/db/assignments.go`, 2026-09-03; WRITE 2026-09-04)

| verb | change | why |
|------|--------|-----|
| `ADJUDICATE_NEXT_EXECUTION` | gemma4:31b → **qwen3.6:35b-a3b** | 12/12 agreement, 4–5× faster. Partially validated on hard cases by baseline-9 (2026-09-04): 0–2 min every verdict, no spiral, caught a real pointer/value defect via `re_refine` that CRITIQUE missed. |
| `REFINE_TESTS_JUDGE` | gemma4:31b → **qwen3.6:35b-a3b**, and drop the format schema (`OmitFormat`) | 62% (= gemma) at 5× speed, only without the grammar. Partially validated on hard cases by baseline-9: 5/5 valid on attempt 1, zero malformed, zero dead turns. |
| `REFINE_TESTS_WRITE` | gemma4:31b → **muse-glimmer:30b-q8_0-dflash** | Full 6-bead bakeoff (see above): 6/6 genuinely correct vs gemma's 3/6, including the two beads (b317, b318) that timed out gemma completely. The independence concern noted below (WRITE == EXECUTE) is judged acceptable: CRITIQUE (qwen3:32b) and JUDGE (qwen3.6) remain independent reviewers between WRITE(muse) and EXECUTE(muse), the same structure already accepted for EXECUTE's move off producer-model self-certification ([[project_third_model_decider]]). Also frees qwen3.6 to stay on JUDGE rather than pulling it toward WRITE too, which would collapse WRITE+JUDGE self-review — a more serious independence loss than WRITE+EXECUTE sharing muse. Prerequisite fix: `internal/verbs/refine_tests.go`'s turn-budget bug (a WRITE call that front-loads verification before any write could burn its whole budget and escalate with nothing written) — fixed 2026-09-04 before this bakeoff ran, or the comparison would have been unfair to any candidate with muse's/qwen3.6's/gemma's specific tool-ordering habits. |

### Not changed

- **`REFINE_TESTS_CRITIQUE` stays qwen3:32b.** Nothing beat it (67% catch, 0
  false-pos); qwen3.6 got 0/6, mistral malformed. **This is a negative result —
  CRITIQUE's problem is not the model.** The 13-min cost is 6 forced
  verification turns × a long reasoning stream each. The fix is the
  mechanical-pre-pass redesign (see `docs/` / the CRITIQUE redesign work), not a
  swap.
- ~~`REFINE_TESTS_WRITE` stays gemma4:31b for now~~ — **superseded 2026-09-04**,
  see the Applied table above. `good/` fixtures now exist for b314–b318 (still
  missing seeded mutants beyond b313); that plus the turn-budget fix was enough
  to run the full 6-bead bakeoff and settle this.

### Cross-cutting findings

- **qwen3.6:35b-a3b is a strong fast substitute for the well-evidenced decision
  verbs (JUDGE, ADJUDICATE) — but useless for the detection verb (CRITIQUE).**
  Give a model mechanical evidence and a fast MoE suffices; ask it to find bugs
  open-ended and it rubber-stamps.
- **mistral-small3.2:24b fails every tool-loop verb** — empty required fields
  (JUDGE/ADJUDICATE) or never calls the mandatory tool (JUDGE/CRITIQUE under the
  grammar).
- **The single-giant-thinking-stream / zero-action pathology is not
  gemma-specific.** gemma burned the whole `num_predict` budget thinking on
  turn 1 of a WRITE and spends ~5 min thinking on a JUDGE approve/revise; the
  2026-09-04 WRITE bakeoff found **qwen3.6:35b-a3b does the identical thing**,
  worse in one respect — its failures return `validation=valid` with a generic
  fallback summary rather than an explicit timeout, so the top-line replay
  result gives no indication anything went wrong. Model-agnostic risk class,
  not a single-model quirk; muse-glimmer is the only WRITE candidate that
  hasn't shown it.
- **Ollama reuses the KV cache across tool-loop turns** (`prompt_eval` stays flat
  as history grows) — the tool-loop cost is reasoning generation, not prompt
  re-eval. Weakens the case for an llama.cpp migration.
- **`eval_count`/`eval_duration` count only answer tokens**, not a reasoning
  model's thinking stream (visible only in `total_duration`). The harness now
  derives `thinking_secs` from the difference.

## Tool-loop length-cap spiral fix — smoke-tested, not proven (2026-09-04 night)

**Fix:** `internal/verbs/tool_loop_recovery.go` (PR #3, `1211f36` on `main`,
deployed to the live binary). See [[project_tool_loop_length_cap_fix]] in
memory for the full diagnosis. Summary: a tool-loop turn that hits
`toolLoopNumPredict` (8192) while still reasoning — `done_reason=length`, zero
content, zero tool calls — was previously nudged with the ordinary
mandatory-verification prompt (asking for *more* reasoning, exactly backwards)
and could burn the rest of a job's turn budget on repeats of the same dead
turn. Fix: detect the condition distinctly; 1st occurrence → one retry at 2x
budget with a "stop analyzing, answer now" nudge; 2nd occurrence → drop the
accumulated transcript and force one stripped last-resort turn (fresh
system+user, no tools). A related gap (turn budget exhausted while every turn,
including the last, legitimately called a tool → silent empty return) gets a
forced finalize-only turn instead.

**Harness changes made to validate this** (both are now permanent
`qualify-model` capabilities, not one-off hacks):
- `--ceiling` flag (`internal/qualify/main.go`) — `Replayer.PerRunCeiling`
  (hard cap on one case+run, default 30m) wasn't exposed before. The fix's own
  retry legitimately asks for 2x the per-turn budget, which can exceed 30m on
  the slowest model (qwen3:32b) on its own — the harness's own safety net was
  at risk of mistaking the fix's designed behavior for a hang.
- `--force-num-predict` flag, plumbed through `ollama.ChatOverride.NumPredict`
  (`internal/ollama/override.go`, `client.go`) — deterministically forces
  every turn to hit `done_reason=length` (set it small, e.g. 64) instead of
  waiting for a natural spiral to recur, which is inherently stochastic (see
  results below).

**Live results:**
- **Organic replay of the primary incident — ADJUDICATE case b314 (the exact
  bead/job that spiraled 3/3 times to escalation in baseline-10, job 2014):
  2/2 replay runs now resolve cleanly** (21m35s and 1m17s, both
  `validation=valid run_err=""`), via the first-hit-retry path. No regression
  on ADJUDICATE's already-clean case (`020`, 2/2 valid) either.
- **Organic replay of CRITIQUE's spiral case (b315-c1, baseline-9 bead 315)
  did NOT reproduce the length-cap condition on either of 2 attempts**
  (`033`: 21m42s valid, 0 dead turns; `034`: 22m56s valid, 0 dead turns) —
  confirms no regression on the ordinary path, but ~45 minutes of real
  GPU time spent without exercising the new recovery logic on this verb at
  all. Reasoning-model spirals are genuinely stochastic (the original corpus
  showed the same non-determinism), so this is expected, not a red flag.
- **Deterministic smoke test (`--force-num-predict=64`, CRITIQUE b315-c1,
  qwen3:32b) closed that gap directly, in 1m9s:** a clean 4-call sequence —
  call 1 hits the forced cap (`length`, 1st occurrence) → call 2 recovers with
  real but insufficient-for-CRITIQUE's-separate-coverage-gate content (`stop`,
  142 chars) → call 3 hits the forced cap again (`length`, 2nd occurrence,
  triggers the stripped-final path) → call 4 (the stripped call) returns real
  valid JSON (`stop`, 154 chars), accepted by `Validate`. Both new code paths
  fired and worked, against a real model and real Ollama, exactly as designed.
  Caveat: the harness's `ChatOverride.NumPredict` always wins over the fix's
  own internal 2x-budget bump (by design, for determinism), so this run
  confirms the *nudge* half of the retry mechanism (successfully steering the
  model to a short real answer) but not that a genuine 2x budget increase
  specifically helps a naturally-occurring long spiral converge — that half
  rests on the corpus-read diagnosis (verbose-but-progressing vs.
  genuinely-repeating shapes) plus the 2/2 organic ADJUDICATE success, not on
  this smoke test.
- **Finding B (turn-cap-fallthrough) remains live-unconfirmed** — only
  mechanically covered, via the fake-server unit tests
  (`internal/verbs/tool_loop_recovery_test.go`).

**Verdict:** promising, not proven — deliberately treated as a smoke test, not
exhaustive validation (per-user direction, 2026-09-04: real confidence comes
from running ratchet on real work, not from more replay engineering). No
regressions observed anywhere it was exercised; the primary reported incident
is directly fixed; the harder CRITIQUE-side organic reproduction and Finding B
remain open, to be picked up (if at all) by real usage in
`exprvm-web-baseline-11` rather than further synthetic replay.

## Live validation via exprvm-web-baseline-11 (2026-09-04/05) — CONFIRMED

Both gaps left open above are now closed. See [[handoff_exprvm_web_baseline_11]]
and [[project_tool_loop_length_cap_fix]] for full detail.

- **CRITIQUE-side organic reproduction: happened twice, both recovered clean.**
  Bead 314 (parser) job 2003 turn 6/7 and bead 315 (env) job 2015 turn 2/6 both
  hit real, naturally-occurring `done_reason=length` — no forcing, no replay.
  Both recovered without escalation (bead 315's via a single strike-1 retry;
  bead 314's composed the strike-1 path with **Finding B's
  turn-cap-fallthrough path in the same call**, since the length-cap turn was
  also the loop's last allotted turn — the first live exercise of that path).
  **Finding B is no longer live-unconfirmed.**
- **Emergent, bigger result**: root-caused *why* CRITIQUE spirals at all —
  `qwen3:32b` has never emitted a real tool call in the entire corpus history
  (0/139 calls, all four corpora), a structural `format:"json"`-grammar vs.
  `<tool_call>`-tag-syntax conflict, not a behavioral/prompt-compliance issue.
  Full detail in [[project_critique_tool_call_json_grammar_block]] — this
  directly contradicts `docs/format-json-tool-turn.md`'s existing assumption
  that CRITIQUE is "not currently broken," and is the likely actual driver of
  most of what this section characterized as "CRITIQUE reasoning spirals."
  Proposed fix (`Options.OmitFormat` on CRITIQUE's tool-turn, same pattern as
  WRITE/EXECUTE_BEAD) is queued for the next framework conversation, ahead of
  a baseline-12.
- Run was deliberately full-stopped at 4/9 beads clean (not a wedge) once this
  finding made continuing less valuable than reallocating the night to the fix
  + a fresh validation baseline.

**Updated verdict:** the tool-loop length-cap fix itself is now **confirmed**,
not just promising — 2/2 real spirals recovered, 0 escalations, both recovery
code paths exercised live. The remaining open work shifted from "does the
recovery mechanism work" (yes) to "why does CRITIQUE need it so often" (now
answered, with a fix queued).
