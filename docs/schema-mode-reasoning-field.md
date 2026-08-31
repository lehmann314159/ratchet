# Design: Schema-mode output with a leading `reasoning` field

**Status:** Draft — 2026-08-31
**Author:** drafted with Claude Code
**Motivates:** fleet modernization (replacing the slow gemma4:31b / qwen3:32b
generation fleet with faster models, all of which are reasoning models)

---

## 1. Problem

Every verb call goes through `ollama.Client.Chat` / `ChatWithTools`, which
hardcode `format: "json"` — Ollama's loose JSON mode, enforced with
GBNF grammar-constrained decoding. This is **mutually exclusive with
reasoning-model thinking**:

- The JSON grammar masks every token outside valid-JSON syntax from token 1.
  `<think>` is not valid JSON, so a reasoning model cannot emit its reasoning
  block.
- Measured on `muse-glimmer:30b-q8_0-dflash` / Ollama 0.33.2:
  - `think: true` + `format: "json"` → thinking separates into the
    `message.thinking` field (951 chars) but `message.content` comes back as
    `{}` — **empty JSON body**.
  - `think` omitted + `format: "json"` (ratchet's current `Chat()` path) → the
    model thinks by default, can't emit it, and degenerates into
    structurally-valid-but-empty JSON. This is the real
    `exprvm-web-muse-test` DECOMPOSE failure: 2/2 attempts, 63 KB output,
    `beads: []`, ~17 min each.
  - `think: false` + `format` → separately buggy in recent Ollama
    (ollama/ollama#15260): the format mask is deferred until an
    end-of-thinking token that a `think:false` template never emits, so the
    format constraint is silently ignored and the model returns plain text.

The current gemma4:31b / qwen3:32b fleet only works because:
- `gemma4:31b` is not a reasoning model, and
- `qwen3:32b`'s (older-style) template defers the grammar mask until after
  `</think>` — think first, close, then emit JSON.

Newer fast models — `gemma4` (newer builds), `glm-4.7-flash`, `muse-glimmer`,
`Qwen3.8` / `qwen35` arch — all have templates that break under this. Fleet
modernization of the JSON verbs is blocked until ratchet stops depending on
that template quirk.

Full analysis and the measured matrix: memory
`project_format_json_vs_reasoning_models`.

Related latent bug regardless of this design: ratchet's `think` plumbing is
broken today. `Chat()` sets no `think` field; `ChatWithTools()` sets
`"think": false` inside the `options` map, where Ollama ignores it (it is a
top-level request field). Confirmed no-op by direct probe.

---

## 2. Root idea

Ollama's structured-output support accepts a **full JSON Schema** in the
`format` field (not just the string `"json"`). The schema's `required` array
is enforced at generation time — confirmed live (Ollama 0.30.6): a field the
model's own reasoning never planned to emit still appeared because the schema
required it. `ollama.Options.Format` already carries this; **no verb uses it
today** — they all pass `nil` and get the bare `"json"` string.

The fix: for each verb, define an explicit JSON Schema whose **first property
is `reasoning` (a required string)**, followed by the verb's real output
fields. The model does its chain-of-thought *inside* the JSON, as a string
value — grammar-legal — before it has to commit to any structured decision.

This:
- keeps grammar-constrained decoding (retains the JSON-escaping-corruption
  protection that `format:"json"` was added for — connect-four-v1 ADJUDICATE,
  2026-08-26),
- works with every reasoning model (no dependency on template mask-deferral),
- gives a genuine CoT benefit (reasoning precedes the decision fields), and
- makes the reasoning inspectable and loggable per attempt.

---

## 3. Goals / non-goals

**Goals**
- Every JSON verb produces valid, non-degenerate structured output when run on
  a reasoning model.
- No regression on the current non-reasoning / qwen3 fleet.
- Reasoning captured (logged, optionally persisted) per attempt.
- Keep the existing `Run` → `Validate` → `Commit` dispatch contract unchanged.

**Non-goals**
- Changing which verbs exist or what they decide.
- EXECUTE_BEAD's tool-calling loop — separate path (`internal/execution/bead.go`),
  separate follow-up (see §7).
- Prompt-engineering the reasoning quality. This is a plumbing + schema change;
  prompt tuning is downstream.
- Picking the new fleet. That decision waits on this landing.

---

## 4. Design

### 4.1 Client changes (`internal/ollama/client.go`)

1. **Add a top-level `Think *bool`** to `chatRequest` and the `ChatWithTools`
   request struct. Marshal as `"think"` (omitempty). Populate from a new
   `Options.Think *bool`.
   - Default (nil): omit the field — preserve current behavior for callers
     that don't opt in.
   - With schema-mode verbs we will pass `Think: ptr(false)`: we do not want
     a separate thinking pass at all; the reasoning lives in the schema.
     (Belt-and-suspenders — the schema alone is sufficient, but an explicit
     `think:false` avoids wasted thinking tokens on models that would
     otherwise default-on. Watch ollama#15260: if `think:false` + schema
     misbehaves on a target model, fall back to omitting `think` and rely on
     the schema.)

2. **Add `Thinking string` to `Message`** (`json:"thinking,omitempty"`) and
   capture it in both `Chat` and the `ChatWithTools` streaming loop
   (`chunk.Message.Thinking`). Log it; do not feed it downstream. This makes
   the "did the model think, and was it discarded" question answerable and is
   needed to debug any target model that ignores `think:false`.

3. **`Options.Format`** already exists and already flows to the request.
   No client change needed beyond verbs starting to set it.

4. `ExtractJSON` stays as-is — defensive. Under a strict schema the model
   should emit a bare object, but the `<think>`/fence stripping is cheap
   insurance and harmless on clean input.

### 4.2 Schema definitions (`internal/verbs/schemas.go`, new)

One exported `map[string]any` per verb, built once. Shape:

```go
var decomposeSpecSchema = map[string]any{
    "type": "object",
    "properties": map[string]any{
        "reasoning": map[string]any{
            "type": "string",
            "description": "Think step by step here before filling the fields below: " +
                "the bead boundaries, ordering, and which design-doc pins go where.",
        },
        "beads":       beadArraySchema,   // mirrors []ParsedBead
        "ambiguities": stringArraySchema,
    },
    "required": []string{"reasoning", "beads"},
}
```

Property order in the marshaled schema is the generation order Ollama
follows, so `reasoning` must be authored first. (Go map iteration is
unordered — build these with an ordered helper, or emit them as
`json.RawMessage` written in the intended order. A small
`orderedObject(pairs ...kv)` helper is the least error-prone.)

`reasoning` is **required** so the model can't skip it, and it is a plain
string so the model's CoT is unconstrained prose inside that one value.

### 4.3 Verb changes

Each verb's `Run` changes from:

```go
return oc.Chat(ctx, model, msgs, nil)
```

to:

```go
return oc.Chat(ctx, model, msgs, &ollama.Options{
    Format: verbs.DecomposeSpecSchema,
    Think:  ptr(false),
})
```

`Validate` changes: unmarshal into a struct that has the extra
`Reasoning string \`json:"reasoning"\`` field, then ignore it (or stash it —
see §4.4). The existing field validation is otherwise unchanged. In several
verbs the output struct *already* has a reasoning-ish field
(`CertifyManifestOutput.ModelReasoning`,
`AnalyzeExecutionOutput.AnalyzerInterpretation`,
`ReconcileResponse.Reason`) — those stay; the new top-level `reasoning` is
distinct (whole-response CoT vs. per-item justification) and comes first.

### 4.4 Persisting reasoning (optional, recommended)

`handoff_attempts.raw_output` already stores the full response, so the
reasoning is durable without a schema change. For UI visibility, add a
nullable `reasoning` column to `handoff_attempts` (or surface it by parsing
`raw_output` in the UI layer — no migration). Prefer the parse-in-UI route
first; add the column only if it's needed for querying.

---

## 5. Per-verb inventory

| Verb | Output struct | Schema complexity | Notes |
|---|---|---|---|
| SURVEY_SPEC | `SurveySpecOutput` | medium (nested files/declarations) | runaway seen on glm — first to fix |
| VERIFY_MANIFEST | `VerifyManifestOutput` | — | **model-free** (`models.go`: excluded from `AllVerbs` / `verb_model_assignments`). No schema. |
| CERTIFY_MANIFEST | `CertifyManifestOutput` | low | already has `model_reasoning`; reorder |
| DECOMPOSE_SPEC | `DecomposeSpecOutput` | **high** (bead array) | the confirmed failure; highest value |
| AUDIT_DECOMPOSITION | `AuditDecompositionOutput` | medium (findings array) | reasoning-heavy verb — big CoT win |
| RECONCILE_DECOMPOSITION | `ReconcileDecompositionOutput` | high (responses + optional updated_bead) | |
| ANALYZE_EXECUTION | `AnalyzeExecutionOutput` | low (2 strings) | |
| COMPRESS_ANALYSIS | `CompressAnalysisOutput` | low | on mistral (not a reasoning model) — low priority |
| REVISE_PENDING | `RevisePendingOutput` | medium | |
| REFINE_TESTS_WRITE | `RefineTestsWriteOutput` | medium | |
| REFINE_TESTS_CRITIQUE | `RefineTestsCritiqueOutput` | medium (findings) | reasoning-heavy — CoT win |
| REFINE_TESTS_JUDGE | `RefineTestsJudgeOutput` | medium | prior missing-`summary` bug (cf#15260 note in code) — schema `required` fixes that too |
| ADJUDICATE_NEXT_EXECUTION | `AdjudicateNextExecutionOutput` | **high** (decision + optional revised bead) + tool loop | uses `ChatWithTools`; do last |
| MONITOR_EXECUTION | (monitor.go) | low | mistral, tight loop, `MonitorNumCtx` — likely leave on bare `"json"` |

Verbs that will keep bare `"json"`: MONITOR_EXECUTION and COMPRESS_ANALYSIS
(both on `mistral-small3.2:24b`, not a reasoning model, latency-sensitive).
Everything else gets a schema.

---

## 6. Implementation plan (phased)

**Phase 0 — client plumbing. ✅ DONE (2026-08-31, uncommitted on loop-mode).**
`Think *bool` added to `Options` + both request structs, marshaled top-level
as `think` (omitempty → nil omits the field, historical behavior preserved).
The stale `options: {"think": false}` in `ChatWithTools` (a confirmed Ollama
no-op) removed. `Message.Thinking` field added; captured in `Chat` (from the
response) and `ChatWithTools` (accumulated from stream chunks), logged at Info
with char counts, never fed downstream. 6 new tests in `client_test.go`
(think omitted by default / sent top-level / not leaked into options / for
both call paths; thinking field discarded by Chat; thinking accumulated by
ChatWithTools). Full suite green.

**Phase 1 — one verb end to end: DECOMPOSE_SPEC.**
Code + unit tests: DONE (2026-08-31, uncommitted).
- `internal/verbs/schemas.go` — `orderedObject`/`kv` marshaler (JSON object with
  slice-order keys), `reasoningProperty()`, `disableThink()`, `DecomposeSpecSchema`
  (`reasoning` first; `beads` with `minItems:1` — grammar-level guard against the
  empty-array degeneration).
- `decompose_spec.go` `Run` passes `&ollama.Options{Format: DecomposeSpecSchema,
  Think: disableThink()}`. `Validate` captures `out.Reasoning`, logs its presence.
- `outputs.go` `DecomposeSpecOutput.Reasoning` field.
- `prompts.go` decompose output section rewritten: `reasoning` is the first field.
- 3 new tests (schema `reasoning`-first + `minItems`; Validate accepts reasoning;
  Run sends a schema object + `think:false`, not bare `"json"`). Full suite green.

LIVE VALIDATION: PASS (2026-08-31, project 33 exprvm-web-schema-test,
muse-glimmer:30b-q8_0-dflash fleet). The same model that failed DECOMPOSE 2/2
on project 32 under bare "json" (63KB → beads:[], ~17min each):
- 1 attempt, ~2 min, 8KB output, Validate=valid, 9 beads (matches the known-
  good structure), reasoning field populated with genuine CoT (1239 chars).
- Ollama HONORS schema property order — `reasoning` was authored first and
  filled before any structured field (risk #3 resolved).
- AUDIT_DECOMPOSITION (qwen3:32b, still bare "json") returned `no_issues` on
  the FIRST pass — no RECONCILE round, cascade converged. Matches the gemma4
  fleet's best runs (v7). Project paused at --pause-after-reconcile.
- Minor: muse-glimmer leaks a trailing `<|eot|>` token; ExtractJSON's
  brace-matching strips it. Candidate for the strip list.
Risk #1 (schema fixes degeneration, no new under-fill) resolved.

**Phase 2 — the reasoning-heavy reviewers: AUDIT, REFINE_TESTS_CRITIQUE,
RECONCILE. ✅ CODE DONE + committed 6d8dab4 (2026-08-31).**
`AuditDecompositionSchema` / `ReconcileDecompositionSchema` /
`RefineTestsCritiqueSchema`; extracted `beadObjectSchema()` (shared by
DECOMPOSE.beads[] + RECONCILE.updated_bead); `logSchemaReasoning()` helper;
`injectMechanicalFindings` re-marshal carries `reasoning` through; CRITIQUE
schema on the ChatWithTools loop. 9 new tests, full suite green.

LIVE VALIDATION — MIXED, and it reframed the whole effort:
- **Project 35 (compiled-default fleet: gemma4:31b DECOMPOSE, qwen3:32b AUDIT
  — same fleet as baseline runs 28–30, only the code changed): CLEAN WIN.**
  gemma DECOMPOSE 5.4 min / 1 attempt / clean (baseline was 9–11 min) —
  ~2× faster, reasoning field a genuine CoT naming all 3 pins verbatim.
  qwen3:32b AUDIT 1.4 min / 1 attempt / clean. Cascade converged first pass.
- **Project 34 (muse DECOMPOSE, qwen3.6:35b-a3b AUDIT/reviewers): FRAGILE.**
  qwen3.6:35b-a3b AUDIT: 2 malformed-JSON attempts (~6 min each), attempt 1 a
  76KB runaway `reasoning` string that broke its own `\` escape; valid on
  attempt 3. muse RECONCILE (round 2): 2 malformed (`{", "responses":…` — botched
  the leading `reasoning` key on the nested schema), valid on attempt 3, one
  strike short of escalation. Whole cascade ~31 min, 5 malformed attempts.
  But the review was DEEPER — P34's AUDIT caught a real integration-test-file
  deviation that P33's qwen3:32b bare-json AUDIT (`no_issues`) missed.

**Conclusion:** schema-mode is a clear win for the CURRENT fleet (faster,
equally stable, inspectable reasoning) and qwen3:32b's older template handles
the schema-mode reviewer verbs cleanly. The newer fast reasoning models
(muse-glimmer, qwen3.6:35b-a3b, glm-4.7-flash) remain fragile under schema-mode
on the complex verbs — malformed JSON and runaway `reasoning` strings. The
near-term payoff is "speed up the existing fleet", NOT "unlock the fast models"
— that still needs the models to mature or needs a runaway guard (see risk #8).

**Phase 3 — the rest** (SURVEY, CERTIFY, ANALYZE, REVISE_PENDING,
REFINE_TESTS_WRITE/JUDGE).

**Phase 4 — ADJUDICATE** (`ChatWithTools` + schema; the tool loop makes this
the fiddliest — the final turn's content must match the schema while
intermediate tool-call turns don't).

**Phase 5 — fleet swap.** With schema-mode landed, re-run the
`glm-4.7-flash` and `muse-glimmer` fleet tests (projects 31/32 style) and
decide the new fleet.

Each phase: rebuild `-o ratchet ./cmd/ratchet/`, deploy to the live daemon,
validate on a `clone-project` / fresh `new-project` run, keep the change
committed on `loop-mode`.

---

## 7. Risks and open questions

1. **Does a strict schema hurt output quality vs. loose `"json"`?**
   Grammar constraint on nested arrays can push a model toward under-filling
   (emit `[]` and move on). Mitigation: `required` on the load-bearing arrays
   (`beads`, `findings`, `responses`), and `minItems` where a floor is known.
   Phase 1 is the go/no-go on this.

2. **`think: false` + schema on a given target model** (ollama#15260 territory).
   Per-model risk. Fallback: omit `think`, rely on schema alone. The
   `Message.Thinking` logging tells us which models need which.

3. **Property ordering.** Ollama follows schema property order for generation;
   Go maps don't preserve order. Must use an ordered emitter. Low risk, but a
   silent bug if missed (`reasoning` ends up last → no CoT benefit).
   *Resolved in Phase 1 live validation — Ollama does honor the order.*

8. **Runaway `reasoning` string** (found in Phase 2 live). The schema constrains
   the field's *type* (string) but not its *length* — a reasoning model that
   won't stop filled it to 76KB and eventually emitted an invalid `\` escape,
   corrupting the JSON. Candidate mitigation: `maxLength` on `reasoningProperty`.
   UNTESTED — Ollama's schema→GBNF may or may not enforce string `maxLength`.
   Worth a targeted probe before Phase 5: if it works, it de-risks the fast
   models significantly.

9. **Malformed leading key on complex nested schemas** (found in Phase 2 live).
   muse-glimmer botched emitting the `reasoning` key first on the RECONCILE
   schema (nested `updated_bead`): `{", "responses":…`. Model-specific
   grammar-adherence degradation. The retry mechanism recovers it (tolerance 2)
   but barely. Not seen on the flatter DECOMPOSE/AUDIT schemas.

4. **ADJUDICATE tool loop.** The schema applies to the *final* answer turn.
   Intermediate `run_go_snippet` tool-call turns must not be schema-checked.
   Needs care in `ChatWithTools` — possibly only set `format` on the request
   once tools are exhausted, or accept that tool-call turns naturally have
   empty content and the schema only bites on the final turn. Prototype in
   Phase 4.

5. **Context budget.** A verbose `reasoning` string consumes output tokens
   inside the same generation. For DECOMPOSE (already the largest output) this
   competes with `defaultNumCtx` (40960). May need a per-verb `NumCtx` bump or
   a `reasoning` length hint in the schema description.

6. **`Validate` feedback loop.** When `Validate` rejects and the job retries,
   the model currently re-reasons from scratch. Consider feeding the prior
   `reasoning` + the validation error back on retry (out of scope here, but
   the schema makes it possible).

7. *(resolved)* VERIFY_MANIFEST is model-free — no schema needed.

---

## 8. Testing

- **Unit:** schema builders emit ordered, valid JSON Schema; `Validate`
  accepts output with and without `reasoning`; `Think` marshals top-level.
- **Client integration** (against live Ollama, gated like the existing
  `format:"json"` + tools coexistence test): for each target reasoning model,
  `think:false` + schema returns a non-empty object matching the schema.
- **End-to-end per phase:** a real project run on the fixture, reasoning-model
  fleet, compared bead-by-bead / finding-by-finding against a `qwen3:32b`
  baseline on the same fixture.
- **Regression:** full existing suite green at every phase; a
  `qwen3:32b` + `gemma4:31b` fleet run still completes (schema-mode must not
  regress the models that work today).

---

## 9. Alternatives considered

| Alternative | Why not |
|---|---|
| **Status quo** — JSON verbs stay on non-reasoning / qwen3-style models | Caps fleet modernization; the fast models are all reasoning models. Leaves ratchet dependent on an Ollama template quirk that a future version may drop. |
| **Two-stage: free-form think, then a cheap JSON-ify call** | 2× calls per verb, 2× latency, new failure mode (the JSON-ify step misrepresents the reasoning). Higher complexity than schema-mode for no extra safety. |
| **Drop `format` on the reasoning verbs, rely on `ExtractJSON` + retry** | Re-opens the JSON-escaping-corruption bug on exactly the big-free-text verbs (ADJUDICATE `reasoning_text`, DECOMPOSE bead `full_text`) that `format:"json"` was added to fix. A text-repair pass was already tried and rejected as unsafe. |
| **Set `think:false` top-level, keep bare `"json"`** | ollama#15260: format mask silently dropped → plain-text output. Strictly worse. |
| **Wait for Ollama to fix think+format coexistence upstream** | Unbounded timeline; the coexistence is "mutually exclusive by design" per the GBNF constraint, not a bug they've committed to fixing. |

---

## 10. Decision needed

- Approve the schema-mode direction and the phased plan?
- Persist reasoning: parse-in-UI (no migration) vs. add `handoff_attempts.reasoning`?
- Phase 1 target models: `muse-glimmer` + `glm-4.7-flash`, or also pull
  `nemotron-cascade-2:30b-a3b`?
