# Design: Model qualification harness

**Status:** Draft — 2026-08-31
**Author:** drafted with Claude Code
**Depends on:** schema-mode rollout complete (docs/schema-mode-reasoning-field.md)
— the harness scores output against each verb's I/O contract, which is still
changing while Phases 3–5 land.

---

## 1. Problem

Fleet decisions are made blind. When a candidate model is pulled, the only way
to learn whether it works for a verb is to wire it into a live project and watch
— which is slow, consumes a real run, and surfaces failures one at a time, deep
into a cascade.

The 2026-08-31 fleet-modernization session is the case study. Across four
projects (31–34) we discovered, one live run at a time:

| Model | Verb | Failure |
|---|---|---|
| glm-4.7-flash | SURVEY_SPEC | runaway generation, ~70K tokens, never terminated |
| muse-glimmer | DECOMPOSE (bare json) | degenerate empty `beads:[]`, 2× ~17 min |
| muse-glimmer | RECONCILE (schema) | malformed JSON, `{", "responses":…` — 5 bad attempts across the cascade |
| qwen3.6:35b-a3b | AUDIT (schema) | 76KB runaway `reasoning` string, self-corrupted its own `\` escape |
| Qwen3.8-27B | (probe) | leaked `</think>` into the content field |
| muse-glimmer | DECOMPOSE (schema) | works — but P33 vs P34 gave different integration-file structure, same model/doc/temp |

Every one of these is mechanically detectable in minutes with a fixed input and
a few metrics. None needed a live project to find.

**Goal:** a repeatable harness that scores any model against ratchet's verbs
for **speed, stability, and variability**, producing a verb × model matrix that
drives fleet construction — including the recurring "split fleet" question
(thorough model on DECOMPOSE, fast model on the speed-sensitive verbs).

---

## 2. Approach

**Replay recorded verb inputs against the candidate — do not run projects.**

Each verb's `Run(ctx, d, oc, job)` builds its prompt entirely from DB + folder
state, then calls `oc.Chat` / `oc.ChatWithTools`. The harness:

1. Loads a **fixture** (negative-ID project + folder snapshot) captured at the
   exact state where the target verb is dispatched.
2. Constructs the `handoff_jobs` row that verb expects.
3. Calls `handler.Run()` **N times** against a real `ollama.Client` pointed at
   the box, with the candidate model assigned to that verb.
4. Runs `handler.Validate()` on each output. **Never calls `Commit`** (or calls
   it inside a transaction that is always rolled back) — the N runs must be
   independent and must not advance the fixture.
5. Records timing, token counts, validity, output, and the separated
   `thinking` field per run.

This reuses the real prompt-building and validation logic — no reimplementation,
no drift between what the harness tests and what production does.

### Why fixtures, not captured message arrays

Capturing the raw `[]ollama.Message` would require instrumenting every verb.
Fixtures already exist (`save-fixture` / `clone-project`, negative IDs,
`fixtureScopedTables`), already snapshot project + beads + revisions + folder,
and are the same mechanism the stress-test lineage uses. One fixture captured
mid-run covers several verbs: a post-DECOMPOSE fixture runs AUDIT directly, and
with a synthetic critique row also runs RECONCILE.

### Fixture set (initial)

| Fixture | State | Verbs it can drive |
|---|---|---|
| `qual-pre-decompose` | project + design_doc.md + survey.md, no beads | SURVEY_SPEC, DECOMPOSE_SPEC |
| `qual-post-decompose` | beads written, pre-AUDIT | AUDIT_DECOMPOSITION, CERTIFY_MANIFEST |
| `qual-pre-reconcile` | beads + one recorded AUDIT critique (issues_found) | RECONCILE_DECOMPOSITION |
| `qual-pre-execute` | one bead + revision + folder stubs | EXECUTE_BEAD, REFINE_TESTS_WRITE |
| `qual-pre-refine-review` | bead + a written test file | REFINE_TESTS_CRITIQUE, REFINE_TESTS_JUDGE |
| `qual-pre-adjudicate` | execution + analysis rows for a timed-out attempt | ADJUDICATE_NEXT_EXECUTION |

Built once from a real `exprvm-web` run (exhaustively specifiable, known-good,
already the schema-mode validation vehicle), checked in under
`testdata/qual-fixtures/` or kept in the fixtures DB.

---

## 3. Metrics

Per `(verb, model)` over N runs:

### Speed
- `latency_p50`, `latency_p90` — wall-clock `Run()` duration
- `gen_tokens_p50`, `tok_per_sec` — from Ollama's response (`eval_count`,
  `eval_duration`); prompt-eval vs generation split
- `warmup_cost` — cold-load time (first call after model swap)

### Stability
- `first_try_valid_rate` — fraction of runs whose output passes `Validate` with
  no retry
- `malformed_json_rate` — `Validate` returns a JSON parse error
- `runaway_rate` — output bytes > K × the median good-output size for that verb
  (catches the 70K/76K blowups; K≈4)
- `think_leak` — `<think>` / `</think>` / `<|eot|>` / reasoning-preamble text
  present in the content channel
- `empty_degenerate_rate` — valid JSON but a load-bearing array is empty
  (`beads:[]`, `responses:[]`)
- `escalation_rate` — would the dispatch strike counter (tolerance = 2) have
  escalated this verb before a valid output

### Variability (structured-output verbs)
- `structural_spread` — across the N valid outputs: variance in bead count /
  findings count / response count; set-difference of output_files assignments;
  a normalized structural hash (shape, not prose)
- Quantifies the muse P33-vs-P34 wobble that we only noticed by eye

### Not in v1: semantic quality
`Validate`-passing ≠ good. A terse-but-valid decomposition (muse) and a
thorough one (gemma) both pass. True quality grading needs a per-verb rubric and
a judge (a stronger local model, or Claude via API, scoring a sampled subset).
Deferred to v2 — v1 ships structural metrics only, plus the raw outputs saved
for manual spot-review.

---

## 4. CLI

`cmd/qualify-model`, following `cmd/checkdesigndoc`'s shape.

```
ratchet qualify-model \
  --model muse-glimmer:30b-q8_0-dflash \
  --verbs DECOMPOSE_SPEC,AUDIT_DECOMPOSITION,RECONCILE_DECOMPOSITION \
  --runs 5 \
  --ollama http://192.168.50.241:11434 \
  --tier full \
  --out report.json
```

- `--tier smoke` — trivial-JSON probe + 1 run each of DECOMPOSE / AUDIT /
  RECONCILE. ~2 min. Rejects obviously-broken models before spending an hour.
- `--tier full` — the 5 high-risk verbs (DECOMPOSE, AUDIT, RECONCILE,
  EXECUTE_BEAD, ADJUDICATE) × N. ~1 hr/model on the current box.
- `--verbs` — explicit override.

Output: `report.json` (raw) + a markdown table to stdout:

```
verb                     model              p50     tok/s  valid%  malform%  runaway%  spread
DECOMPOSE_SPEC           muse-glimmer:30b   2m10s   19.8   100     0         0         0.31
RECONCILE_DECOMPOSITION  muse-glimmer:30b   1m30s   20.1   33      50        0         —
AUDIT_DECOMPOSITION      qwen3.6:35b-a3b    6m00s   24.0   33      67        33        —
```

A `qualify-fleet` mode takes a fleet JSON and reports the matrix for every
assignment plus a constraint check (the 5 cross-review rules).

---

## 5. Implementation notes

- **Isolation:** each run gets a fresh copy of the fixture DB (SQLite file copy
  is cheap) or a savepoint rolled back after `Validate`. Folder state is
  read-only for the review verbs; EXECUTE_BEAD/REFINE_WRITE write files, so
  those need a per-run folder copy.
- **Model assignment:** the harness writes a `verb_model_assignments` row for
  the candidate before calling `Run` (or a Run signature that takes an explicit
  model override — cleaner, small refactor).
- **Timeouts:** reuse `handoffClientTimeout` (60 min) but also record a
  per-run soft cap (e.g. 3× the verb's known-good p90) and count overruns as a
  `runaway`/`stall` — don't wait the full hour on a hung model.
- **Token metrics:** `ollama.Chat` currently returns only the content string.
  Add an optional stats return (or a `*Stats` out-param) carrying
  `eval_count` / `eval_duration` / `prompt_eval_*` — small client change,
  useful beyond the harness.
- **ChatWithTools verbs** (CRITIQUE, JUDGE, ADJUDICATE, EXECUTE_BEAD): the
  harness measures the whole tool loop; report turn count and tool-call count
  as extra columns.
- **Determinism caveat:** temperature is 0.3 fleet-wide and not currently
  settable to 0 (`client.go`'s `if opts.Temperature > 0`). The variability
  metric is measuring real sampling spread at 0.3 — which is what production
  sees. A `--temp` flag on the harness would let us separate "model is
  inherently variable" from "0.3 is too high for this verb".

---

## 6. Sequencing

1. **Finish schema-mode (Phases 3–5).** The harness scores against each verb's
   Validate + expected output shape; both are still moving.
2. **Client stats return** — the token-metrics refactor. Independently useful.
3. **Build the fixture set** from one clean `exprvm-web` run.
4. **`cmd/qualify-model` v1** — structural metrics, smoke + full tiers.
5. **Re-qualify the current fleet + the 2026-08-31 candidates** (muse-glimmer,
   glm-4.7-flash, qwen3.6:35b-a3b, nemotron-cascade-2, qwen3:30b-a3b) — first
   real matrix. Sanity check: it must reproduce the failures we already know
   (muse RECONCILE malformed, qwen3.6 AUDIT runaway, glm SURVEY runaway).
6. **v2: rubric-graded quality** on a sampled subset, per verb.

Time-box each step. This is infrastructure for the fleet, not a program in its
own right.

---

## 7. Open questions

- Fixture staleness: verb prompts reference design-doc structure; when
  `design_doc_guide.md` changes, the qual fixtures need rebuilding. Same
  problem the stress-test fixtures already have — accept it, document the
  rebuild step.
- Is one design doc (`exprvm-web`) enough, or does the matrix need a second
  domain (a game, an image task) to catch domain-sensitivity? Start with one,
  add if a model passes exprvm-web but fails a real project.
- Where do reports live — checked in as a running record (like
  `docs/audit-followups.md`), or ephemeral? Lean: check in a
  `docs/fleet-qualification.md` table, update on each re-qual.
- Should `qualify-fleet` gate `new-project` (warn if an assignment scores
  below threshold)? Probably a follow-up once we trust the metrics.
