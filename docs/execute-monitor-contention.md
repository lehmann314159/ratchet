# EXECUTE_BEAD stream stalls under concurrent-model GPU contention

**Status:** partial fix landed (branch `fix/execute-monitor-contention`, 2026-09-07).
The MONITOR redesign below is deliberately deferred — it wants careful design work
and live validation, not a rushed change.

## Symptom

`lsystem-demo-run` bead 2 (`grammar`) escalated. On each of three EXECUTE attempts,
muse-glimmer's Ollama stream stopped delivering tokens mid-generation (~1400–3500
tokens into a write turn, no `done`), 3× → PR #9's `streamIdleTimeout` (3m) fired →
`execute-bead` exited with no `termination_cause` → `consecutive_infra_failures`
cap 3 → `ESCALATION — execute-bead crashed at startup too many times` (wrong: it
stalled 8–14 min in, not at startup).

## What the gx10 evidence actually shows (2026-09-07)

Ollama config (`/etc/systemd/system/ollama.service.d/override.conf`):

```
OLLAMA_KEEP_ALIVE=-1
OLLAMA_MAX_LOADED_MODELS=3
```

So the earlier hypothesis ("Ollama's default 5-min keep_alive keeps the prior
verb's model resident") is **wrong** — keep_alive is *infinite*. Up to three
models stay pinned forever; Ollama churns them only reactively via memory-fit
checks. The journal for this run is full of load/evict/reload thrash during
bootstrap + bead 1 (gemma4 loaded at `-c 262144` = 57.9 GiB, muse at `-c 131072`,
etc.).

During the actual grammar-bead stalls (execs 2/3/4, 22:35–23:22 MDT):

- **Two models resident and pinned** — muse (EXECUTE_BEAD, llama-server
  `-c 40960`) + mistral-small3.2 (MONITOR_EXECUTION, `-c 16384`). No eviction, no
  reload, `free_swap` full throughout. They time-share the single GB10 GPU.
- **The stall is in token *delivery*, not generation or eviction.** For exec 3,
  at the moment PR #9's watchdog fired (23:08:00), llama-server task 8513 was
  *still emitting muse tokens* — `n_gen` climbed 2624 → 4978 at a steady ~8.5 t/s
  straight through the window where ratchet received *zero* chunks (~23:04 →
  23:08). Ollama logged that request `200 | 10m16s` and only cancelled the slot
  because ratchet disconnected. The model was never evicted or frozen; Ollama's
  HTTP stream to the ratchet client stopped flushing for >3 minutes while the
  backend kept working.
- **MONITOR is a major aggravator.** `callMonitorModel` used the non-streaming
  `Chat` path with **no `num_predict` cap**. Under GPU contention mistral-small3.2
  generated **4081 tokens over 11m39s** for a single FIRE/NO_FIRE decision
  (journal task 3277), competing head-to-head with the muse turn doing the real
  work the entire time. Solo, muse does ~18.6 t/s; during this it was ~8.5.
- A 2-model repro (long muse stream + concurrent mistral loop) reproduced the
  throughput collapse (muse ~6–8 t/s, a 2000-token mistral call taking 7+ min)
  but **not** the multi-minute delivery stall in ~3 min of streaming — the stall
  needs the deeper conditions (large multi-turn tool-loop context + llama.cpp's
  118-MiB context-checkpointing every 8192 tokens + a very long concurrent
  mistral generation). It is *not* "any two concurrent models".

## Root cause

EXECUTE_BEAD (muse) and MONITOR_EXECUTION (mistral) run as two concurrent Ollama
models on one GB10 GPU by design. Under that contention an uncapped 10+ minute
MONITOR generation runs alongside the muse turn, and Ollama's serve/proxy layer
stops delivering muse chunks to the ratchet client for minutes at a time even
though the backend is still generating. PR #9's 3-minute idle watchdog then kills
a live-but-crawling generation. The exact mechanism inside Ollama's streaming
path (HTTP head-of-line blocking across two concurrent streams is the leading
guess) is not pinned down — but model eviction is ruled out.

## What landed (safe subset — branch `fix/execute-monitor-contention`)

1. **Cap MONITOR generation** — `monitorNumPredict = 256` on `callMonitorModel`.
   Its output is two short lines; an uncapped degenerate call is the leading
   cause of the contention. `internal/execution/monitor.go`.
2. **`keep_alive: 0` on one-shot `Chat()` verbs** — `ollama.Options.KeepAlive`,
   defaulted to 0 in `Chat()` (every `Chat()` caller is a sequential one-shot
   handoff verb: SURVEY / CERTIFY / DECOMPOSE / AUDIT / RECONCILE / ANALYZE /
   COMPRESS / REVISE). MONITOR overrides with `ollama.KeepResident()` (−1) so it
   isn't reloaded every polling tick. Drops residency so a finished verb's model
   isn't pinned crowding out EXECUTE. `internal/ollama/client.go`.
3. **PR #9 EXECUTE-subprocess gap** — `execute-bead` exits `execExitTransient`
   (75) when `ollama.IsTransient(err)`; `RunExecutionWindow` maps that to
   `handleTransientExecFailure`, which marks the execution `infra_failure`,
   resets the bead to pending, and returns an `ollama.ErrStreamIdle`-wrapped
   error. The dispatcher's `recordRunFailure` → `recordTransientRunFailure`
   (bounded by `transientRetryCap` = 5, no verbTolerance strike, no "crashed at
   startup"). `internal/execution/{bead,window}.go`.
4. **Stale `client.go` comment fixed** — the "one model is resident at a time"
   rationale for `defaultNumCtx` was false; replaced with the real deployment
   constraints.

## Deferred — the MONITOR redesign (needs thoughtful work + live validation)

Kept here so none of it is lost:

- **`parseDecision` can no longer read the model's verdict.** Under
  `format:"json"` (which `Chat()` applies by default) mistral-small3.2 emits
  `{"DECISION": "NO_FIRE", "REASON": "…"}`, but `parseDecision` looks for a line
  *starting with* `DECISION:` and so always falls through to its NO_FIRE default.
  MONITOR's model-judgment FIRE path is currently dead; only the mechanical
  checks (`isWriteFileStall`, `mechanicalLoopPatternCheck`) still fire. The
  `num_predict` cap does not make this worse, but a real fix (parse the JSON
  shape, or drop `format` for the monitor and parse plain text, or move MONITOR
  to schema-mode) should be deliberate — resurrecting FIRE changes run behavior.
- **Slower cadence** — raise `monitorPollInterval` from 30s so MONITOR competes
  for the GPU far less often during an EXECUTE turn.
- **Smaller MONITOR model** — move off mistral-small3.2:24b to a 3–4B model.
  Needs a fleet-assignment change + a qualification check that it still catches
  the loop patterns the prompt targets.
- **Serialize MONITOR with EXECUTE** — only run a MONITOR tick when the EXECUTE
  stream is idle / between tool turns, so the two never contend. Most invasive;
  changes the execution-window subprocess model.
- **Right-size `defaultNumCtx` per verb** — 40960 for every verb is generous;
  EXECUTE_BEAD's tool loop accumulates the most, SURVEY/CERTIFY/DECOMPOSE are
  small one-shots. `MonitorNumCtx` (16384) is already a carve-out.
- **Raise `streamIdleTimeout`** (PR #9) — 3 minutes assumed "unambiguously dead",
  but the backend legitimately crawls at ~5 t/s under contention. A larger bound
  (or a backend-liveness probe) would stop killing live generations. Left alone
  for now because capping MONITOR should remove most of the contention.
- **`keep_alive` for the tool-loop reviewers** (CRITIQUE / JUDGE / ADJUDICATE,
  the `ChatWithTools` path) — they still leave their model pinned. Needs
  per-loop, not per-turn, keep_alive handling (0 on every turn would reload the
  model each turn).
