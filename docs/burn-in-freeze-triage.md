# Burn-in freeze triage

**Purpose.** Decide every open framework item into one of four buckets, land the
"before-freeze" set, cut a `v0.4` tag, then run a long burn-in (many complex
design docs → full pipeline) to get UAT-grade evidence on the work done so far.

**Why now.** The framework-bug tail is a *sampled* surface, not an enumerated one
(`memory/feedback_audit_the_class_not_the_bug`). One fixture repro per bug
undercounts both real defects and the false-positive rate of the gates added to
catch them — the ADJUDICATE cross-bead contamination bug only surfaced on a
from-scratch run of a *new* domain, and the clone we kicked off didn't reproduce
it. Volume + variety is the only way to characterise the stochastic behaviour
(bead-sizing variance, EXECUTE stall spirals, gate FP rates).

**The rule for bucketing.** Land before the freeze only: (a) known correctness
bugs, (b) confirmed low-risk mechanical gates, (c) anything **freeze-neutral** —
`cmd/checkdesigndoc` and skill-guidance changes that improve the docs you author
for the burn-in without touching pipeline behaviour. Defer everything
speculative, tuning-heavy, or dependent on evidence the burn-in itself will
produce — the burn-in is what ranks that work.

Status: **Bucket B COMPLETE (2026-09-09) — all on `main`, none deployed.** Next:
cron-studio run 2 terminates → build once → tag `v0.4` → deploy → burn-in.
Freeze mechanics + burn-in corpus still open — see the sign-off checklist.

---

## Bucket A — already in the freeze (landed + deployed)

Captured by the tag automatically. Listed for completeness.

| Item | Commit(s) |
|---|---|
| qualify-model harness; fleet switch ADJUDICATE+JUDGE→`qwen3.6:35b-a3b` | PR #1 `6685e9f` |
| REFINE_TESTS_WRITE turn-budget + muse-glimmer adopted for WRITE | PR #2 `b50df51` |
| tool-loop length-cap / `toolLoopNumPredict=8192` | PR #3 `1211f36` |
| CRITIQUE `OmitFormat` on the tool-turn (format:json GBNF block) | PR #5 `cba4a89`/`444f629` |
| scaffold-import fix (`SurveyManifestFile.Imports`, `cross_file_type` check) | PR #6 `15f04b1` |
| EXECUTE_BEAD per-attempt sandbox + copy-back whitelist | PR #7 `7aa8c02` |
| EXECUTE progress detection: stall/idle/empty-turn/ceiling watchdogs | PR #8–#11 `008fe80`/`abac583` |
| content-stall watchdog; F-A false-success gate | `7315948` / `72829fd` (**v0.3**) |
| `recoverToolCallsFromContent` (bare-JSON tool calls) | `597989d` |
| decomposition framework: `--checks=bead-size`, bead-merge/drop gate, `draft-design-doc` guide, test-file-name reconcile | `72829fd..2fba9d0` |
| precision chain: multi-pin injection; construction-form check; mechanical pin re-injection (ADJUDICATE `execute_revised` + REWIND) | `b4ae16e` / `a9a2ec3` / `2160833` |
| lsystem grammar bead split (doc-side) + lagging corpus-gate fixtures | `90ca148` |
| **ADJUDICATE cross-bead symbol contamination gate** | `07bf76a` (deployed `a41f10c`, today) |

---

## Bucket B — land before the freeze

### B1. `run_go_snippet` orphan process — **DONE (`4c86df3`, on `main`, not deployed)**

`runGoSnippet`'s 10s timeout SIGKILLs only `go`, not the child `go run` forks.
A blocking snippet (`http.ListenAndServe`, `select{}`, `for{}`) leaks a process
reparented to PID 1 that holds its resources until reboot — one held `:8080` for
~16h and blocked an app from starting. `memory/project_run_go_snippet_orphan_process`.

**Why it must be in the burn-in tag:** many runs → many snippet calls → guaranteed
port/resource leaks that silently corrupt *later* runs in the same burn-in.

**Fix (shipped):** `Setpgid` + `cmd.Cancel` → `syscall.Kill(-pid, SIGKILL)` (mirrors
`internal/execution/tools.go` `toolRunCommand`), `cmd.WaitDelay` so `CombinedOutput`
can't hang on a pipe a grandchild holds open, and `verbs.SweepStaleSnippetDirs` at
orchestrator startup (age-gated dir cleanup only — no process hunt; the group kill
prevents new orphans, a pre-fix orphan is a one-time `pkill`). Tests:
`TestRunGoSnippet_KillsBlockingProcessGroup`, `TestSweepStaleSnippetDirs`.

### B2. Stray `go build .` binary in the live folder — **DONE (`9a4f4d4`, on `main`, not deployed)**

The ADJUDICATE `declare_success` mechanical gate ran `execcheck.VerifyExitCriteria`
against the live folder; an entrypoint bead's `go build .` / `go build -o app .`
criterion drops a multi-MB binary there as a side effect. Observed n=3 (baseline-16,
fractalviz-1, cron-studio run 2). EXECUTE was already fixed by PR #7's sandbox — this
gate was the last live-folder execution of exit criteria.

**Fix (shipped):** new `execcheck.VerifyExitCriteriaIsolated` — copies the folder
(minus `traces/`) to a temp dir, verifies there, discards it; falls back in-place if
the copy fails. The `declare_success` gate is the one caller.
`memory/project_execute_workspace_hygiene`.

### B3. Decomposition-framework v2 follow-ups — **DONE (`1723e9a`, `87d0f8b`, `f5ee725`, on `main`, not deployed)**

Three items from `memory/project_decomposition_framework`.

- **B3a — behavioral-subsection attribution** (`checkdesigndoc`, freeze-neutral). ✅
  `maxBehavioralLines` matched an owned symbol as a substring anywhere in a `###`
  heading, so `### Compile(node Node)…` attributed to whoever owns `Node`. New
  `headingSubject` truncates the heading at its first separator (" — ", " - ") or
  `(` and matches only that. `1723e9a`.

- **B3b — dense-translator NOTE** (`checkdesigndoc`, freeze-neutral). ✅
  New `heavyBehavioralSpec`: ≤3 declared functions + ≥70-line behavioral subsection,
  at any integration level (`hiddenComplexity` only fires for integration hubs).
  **70 is a deliberately conservative placeholder** — clears every bead that reached
  COMPLETE (highest non-flagged: lsystem `render`, 48), catches cron-studio `field`
  (113) + `schedule` (81). Calibrate from burn-in flag-vs-outcome data, not the
  current corpus. CorpusGate now asserts NOTE presence/absence both ways. `87d0f8b`.

- **B3c — EXECUTE-ceiling escalation tag** (runtime — `internal/verbs`, in the frozen
  binary). ✅ `escalateOnRepeatedStall`: if ADJUDICATE already wrote ≥2
  `execute_revised` specs (bead_revisions count) and the bead still stalls, tag it
  `escalation_class=exceeds_execute_ceiling` (slog) + "exceeds the EXECUTE model's
  ceiling; recommend a doc-side structural split" (bead report **Status:** line).
  **Tag only — control flow unchanged.** `f5ee725`.

---

## Bucket C — defer past the burn-in

Not "never" — most of these get built in the first post-freeze batch. The burn-in
ranks them and, for several, supplies the evidence needed to scope them at all.

| Item | Why deferred | Memory |
|---|---|---|
| **CRITIQUE repetition detection** | Confirmed needed (baseline-12): byte-identical verdicts across turns, nudge is a no-op, 28-min job → 3-min. But it changes REFINE_TESTS mid-loop behaviour — the 64%-of-cost hot path and exactly the corpus you don't want to invalidate mid-measurement. **Top C candidate for the first post-freeze batch.** | `project_critique_repetition_detection` |
| **Design-doc precision guidance** | Two-reading-rule + construction-form + surface-area-cap guidance for `draft-design-doc` + `checkdesigndoc`. Largely freeze-neutral (skill text + dev tool), so it *can* proceed in parallel with the burn-in without touching the corpus — but it's a "not started" design effort, and the burn-in will show which precision gaps actually recur. | `project_designdoc_precision_guidance` |
| **Precision chain Phase 3 excerpt-header** | `designdoc_sections.go` loose-by-default header undercuts a pinned exact value. Explicitly a **live-prompt** change, WRITE-replay-gated. Runtime → pre-freeze or post-burn-in; the replay hasn't happened. | `project_decompose_precision` |
| **CRITIQUE spec-contradiction angle** | Mechanical pre-pass abandoned; "need a new angle" — not designed. Needs burn-in evidence to scope. | `project_precision_chain_plan` |
| **Noise-tolerant re_refine loop** | Threshold tuning that "assumes post-CRITIQUE-redesign signal". ADJUDICATE-adjacent; tuning-heavy. | `project_test_action_noise_tolerance` |
| **ADJUDICATE self-corruption misclassification** | Filed today; lower priority (Mike). Repro is lsystem, not cron-studio; the contamination gate removed the cron-studio manifestation. | `project_precision_chain_plan` |
| **Model-capability-aware verbs** | Narrow cases that bit are already patched (`597989d`, `444f629`). The general lookup only bites on **fleet swaps** — freezing the fleet config for the burn-in removes the urgency entirely. | `project_model_capability_aware_verbs` |
| **Fleet Q8-vs-Q4** | Analysis only; blanket upgrade rejected. The one concrete experiment (ADJUDICATE quant bakeoff) is blocked on hardware + a hard-case ADJUDICATE corpus — **which the burn-in produces.** Fleet config is frozen during the burn-in by definition. | `project_fleet_quant_strategy` |
| **REFINE_TESTS loop-cost optimisation** | Umbrella target, not one change (CRITIQUE model/loop, skip-round-trip-on-clean-write, schema-mode critique turn). Burn-in gives fresh muse-WRITE cost numbers to target. | `project_refine_tests_loop_cost` |
| **Behavioral-divergence escalation / post-exec model triage** | Proposed only; part of a larger "synthesis pass" on escalation strategy. Unresolved tension between them. | `project_behavioral_divergence_escalation`, `project_postexec_model_triage` |
| **llama.cpp / llama-swap migration** | Deciding experiment already answered *against* — Ollama does reuse KV cache across tool-loop turns. Deprioritised. | `project_llamacpp_vs_ollama` |

---

## Bucket D — not this cycle (accept as known)

| Item | Disposition |
|---|---|
| Job "elapsed" includes queue-wait (`updated_at − created_at`) | UI display only; workaround known (read `handoff_attempts`). Accept. `project_job_elapsed_time_includes_queue_wait` |
| `remove-project` FK failure under live-daemon contention | Not root-caused; workaround = direct SQL or stop the daemon for bulk deletes. **Relevant to burn-in cleanup** — script project teardown against a stopped daemon. `project_remove_project_fk_concurrency` |
| Third-model decider | Effectively closed — muse WRITE/EXECUTE + qwen3.6 JUDGE/ADJUDICATE + qwen3 CRITIQUE already breaks self-grading; DECOMPOSE can't move (schema-mode). No action. |
| CRITIQUE stub-ambiguity guidance | Already decided: do **not** build. |
| CRITIQUE mechanical pre-pass | Already **abandoned**. |
| `qualify-model --ceiling` / `--force-num-predict` (PR #4, open) | Dev-tool flag, not pipeline. Merge or drop opportunistically; irrelevant to the freeze. |

---

## Freeze mechanics

1. **Precondition:** cron-studio run 2 (project 0 in `qual-corpus-cronstudio-1`)
   reaches a terminal state first — it's a live run on the pre-B binary.
2. **Tag `v0.4`** at `main` after Bucket B lands. Previous tag `v0.3` = `72829fd`.
3. **Fleet config is part of the tag.** Snapshot `internal/db/assignments.go`
   (models per verb) + the per-verb `format:json`/`OmitFormat` flags + quants.
   No model swaps, no quant changes during the burn-in.
4. **Build once** from the tag; deploy to a dedicated burn-in corpus dir, never
   the live `ratchet.db`.
5. **Showstopper policy:** log everything; break the freeze only for a defect
   that blocks a **majority** of runs or makes the corpus unanalysable.
   Everything else → post-burn-in batch. (The first stochastic bug restarting
   the clock is the failure mode to avoid — it just happened with the
   contamination bug.)
6. **Instrument per run:** escalation cause + B3 classification; every mechanical
   gate firing (contamination, construction-form, bead-structure merge/drop,
   bead-criteria consistency, pin re-injection, workspace discard); REFINE_TESTS
   cycle count per bead; EXECUTE stall/timeout/attempt counts; per-verb
   wall-clock; `checkdesigndoc` flags vs actual bead outcome (FP/FN rate on the
   bead-size heuristic).
7. **Corpus target:** N diverse design docs — parsers, web apps, algorithmic
   kernels, data structures, state machines. **Doc authoring is the bottleneck,
   not compute.**

---

## Sign-off checklist

- [x] Bucket B scope — B1, B2, and all of B3 (B3b detector-only, B3c tag-only). *(2026-09-09)*
- [ ] `v0.4` is the right tag name
- [ ] Showstopper threshold ("majority of runs") acceptable
- [ ] Burn-in corpus size / domain list decided
- [ ] Who authors the design docs, and to what standard (`draft-design-doc` as-is?)
