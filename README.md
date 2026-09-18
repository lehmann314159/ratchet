# Ratchet

An autonomous local coding pipeline. Give it a design document; it produces working, tested code — no cloud APIs, no human in the loop except when it gets genuinely stuck.

Ratchet runs on consumer hardware using [Ollama](https://ollama.com)-served LLMs. All model calls are local.

## How it works

Ratchet decomposes a design document into **beads** — scoped units of work, each with a fixed set of output files and an exit criterion (a shell command that must pass, e.g. `go test -v . -run=TestApplyMove`). Beads execute sequentially; the pipeline doesn't advance until the current bead passes its exit criterion.

The pipeline runs in two phases. Full detail, including every branch and mechanical gate, lives in `docs/ratchet_state_machine.md` (four Mermaid diagrams); this is the short version.

**Bootstrap** (runs once per project, before bead 1):

```
SURVEY_SPEC → VERIFY_MANIFEST → CERTIFY_MANIFEST → DECOMPOSE_SPEC → AUDIT_DECOMPOSITION ⇄ RECONCILE_DECOMPOSITION → (bead 1)
```

- **SURVEY_SPEC** reads the design doc and produces a manifest of files/symbols it declares. **VERIFY_MANIFEST** and **CERTIFY_MANIFEST** cross-check that manifest for internal consistency before decomposition starts (up to 5 rejections before the project full-stops).
- **DECOMPOSE_SPEC** reads the design doc and produces a JSON list of bead specs. Bead count emerges from the model call — it's not predetermined.
- **AUDIT_DECOMPOSITION** cross-reviews the decomposition for structural problems (test beads without test files in `output_files`, invalid exit criteria, etc.); **RECONCILE_DECOMPOSITION** applies its findings, updating individual bead specs without re-decomposing. They loop against each other until convergence (capped, then escalates).

**Per-bead loop** (runs once per bead, up to `--max-attempts` retries):

```
[REFINE_TESTS_WRITE → REFINE_TESTS_CRITIQUE → REFINE_TESTS_JUDGE]* → EXECUTE_BEAD → ANALYZE_EXECUTION → COMPRESS_ANALYSIS → ADJUDICATE_NEXT_EXECUTION → (next bead or escalate)
```

- Beads whose `output_files` include a `*_test.go` file start in a **test-first refine loop**: **REFINE_TESTS_WRITE** drafts the test file, **REFINE_TESTS_CRITIQUE** reviews it, **REFINE_TESTS_JUDGE** approves it or sends it back for another cycle (capped at 5). Beads without a test file skip straight to EXECUTE_BEAD.
- **EXECUTE_BEAD** runs a tool-use loop (`read_file` / `run_command` / `write_file` / `declare_success`) in a sandboxed per-attempt workspace. A concurrent `MONITOR_EXECUTION` watchdog subprocess polls the trace and can SIGTERM/SIGKILL it if the model stalls; a mechanical `progressTracker` also detects stalls (empty-turn streaks, repeated identical tool calls, no new file-content hash since the last checkpoint) independent of the watchdog.
- **ANALYZE_EXECUTION** reads the execution trace and produces structured findings — what was done, what the test output said, what went wrong. **COMPRESS_ANALYSIS** summarizes attempt history with `[NEW]`/`[RECURRING]`/`[RESOLVED]` tags (passthrough on attempts 1–2).
- **ADJUDICATE_NEXT_EXECUTION** decides: `declare_success` (mechanically re-verified against a fresh copy of the files, not taken on trust), `execute_as_is` / `execute_revised` (retry, optionally with a revised spec — itself gated against inventing symbols another bead already owns), `test_reject` (test-first mode: discard the tests and revise the spec), `re_refine` (send back into the REFINE_TESTS loop with the failure as CRITIQUE input), or escalate for human review.

At terminal state, Ratchet writes `traces/bead-{id}-report.md` — a single-document summary of every attempt, spec revision, test result, and ADJUDICATE decision.

## Requirements

- Go 1.23+
- [Ollama](https://ollama.com) with models pulled (see fleet below)
- SQLite (via `modernc.org/sqlite` — no system library required)

Models aren't all resident at once: one-shot verbs pass `keep_alive: 0` so the prior verb's model is dropped before the next loads, but `EXECUTE_BEAD`'s model stays resident concurrently with the `MONITOR_EXECUTION` watchdog's by design, and Ollama's `OLLAMA_MAX_LOADED_MODELS`/`OLLAMA_KEEP_ALIVE` settings control how much else stays cached between dispatches. The reference deployment runs `OLLAMA_MAX_LOADED_MODELS=3` on 119 GiB of unified memory to keep 2–3 of the ~24–35 GB fleet models plus their KV caches resident under load. A single high-VRAM GPU or multiple GPUs sharing memory works; see `internal/ollama/client.go` and `docs/execute-monitor-contention.md` for the tuning story.

## Build

```bash
git clone https://github.com/lehmann314159/ratchet
cd ratchet
go build -o ratchet ./cmd/ratchet
```

## Quick start

**1. Create a project:**
```bash
./ratchet new-project \
  --db=ratchet.db \
  --label=my-project \
  --folder=/path/to/project \
  --design-doc=design_doc.md \
  --fleet=fleet.json \
  --max-attempts=5
```

`--fleet` is a JSON file mapping verb names to model names (see fleet section below). Omit to use compiled-in defaults.

**2. Start the server:**
```bash
./ratchet start \
  --db=ratchet.db \
  --ollama=http://localhost:11434
```

The server runs the orchestrator and a dashboard UI in the same process. `--addr` defaults to `localhost:7474` — open that to watch progress.

**3. Wait.** Ratchet runs autonomously. Check `traces/` for per-bead execution logs and post-execution reports. If a bead escalates, investigate the report and advance or fix manually.

## Other commands

`new-project` and `start` cover the common path; the binary also has:

| Command | Purpose |
|---|---|
| `resume-project` | Un-pause a project halted by a `--pause-after-*` knob. |
| `full-stop-project` | Halt a project and cancel its pending/running jobs for good. |
| `rewind-bead` | Reset a stuck/escalated bead to `pending` for a fresh attempt, with an optional human guidance note. |
| `reanalyze-bead` | Re-run `ANALYZE_EXECUTION` onward for a bead without re-executing it. |
| `save-fixture` | Freeze a project (or a cascade lineage) at its current state as a reusable starting point. |
| `clone-project` | Deep-copy a project or fixture into a new one, optionally swapping in a new design doc (the "cascade iteration" workflow — see `docs/fixtures.md`). |
| `restart-project` | Full-stop, then re-create a project with the same folder/fleet/budget, for restarting after a design-doc or framework fix. |
| `ui` | Run just the dashboard UI against an existing DB, without the orchestrator. |
| `qualify-model` | Offline harness for bakeoff-testing a candidate model against captured verb I/O before promoting it into the fleet (see `docs/fleet-qualification.md`). |
| `monitor`, `execute-bead`, `exec-bakeoff` | Internal subprocesses spawned by the orchestrator/qualify-model; not typically run by hand. |

Run any subcommand with `-h` for its full flag list — `new-project` alone has several beyond the ones shown above (`--budget`, `--language`, `--pause-after-reconcile`, `--pause-after-verb`, `--pause-after-bead`, `--reconcile-self-resolve`).

## Model fleet

The active fleet is set per-project via `--fleet`; omitted verbs (or the whole flag) fall back to the compiled-in defaults in `internal/db/assignments.go`:

```json
{
  "SURVEY_SPEC":                "gemma4:31b",
  "CERTIFY_MANIFEST":           "qwen3:32b",
  "DECOMPOSE_SPEC":             "gemma4:31b",
  "AUDIT_DECOMPOSITION":        "qwen3:32b",
  "RECONCILE_DECOMPOSITION":    "gemma4:31b",
  "EXECUTE_BEAD":               "muse-glimmer:30b-q8_0-dflash",
  "MONITOR_EXECUTION":          "mistral-small3.2:24b",
  "ANALYZE_EXECUTION":          "qwen3:32b",
  "COMPRESS_ANALYSIS":          "mistral-small3.2:24b",
  "ADJUDICATE_NEXT_EXECUTION":  "qwen3.6:35b-a3b",
  "REVISE_PENDING":             "qwen3:32b",
  "REFINE_TESTS_WRITE":         "muse-glimmer:30b-q8_0-dflash",
  "REFINE_TESTS_CRITIQUE":      "qwen3:32b",
  "REFINE_TESTS_JUDGE":         "qwen3.6:35b-a3b"
}
```

| Role | Model | Notes |
|---|---|---|
| SURVEY_SPEC + RECONCILE_DECOMPOSITION + DECOMPOSE_SPEC | gemma4:31b | Fast structured JSON |
| CERTIFY_MANIFEST + AUDIT_DECOMPOSITION + ANALYZE_EXECUTION + REVISE_PENDING + REFINE_TESTS_CRITIQUE | qwen3:32b | Full-doc reasoning / cross-review |
| EXECUTE_BEAD + REFINE_TESTS_WRITE | muse-glimmer:30b-q8_0-dflash | Tool-use loop; reads files before writing; validated for test-writing via bakeoff (`docs/fleet-qualification.md`) |
| MONITOR_EXECUTION + COMPRESS_ANALYSIS | mistral-small3.2:24b | Needs fast inference for 30s watchdog ticks |
| ADJUDICATE_NEXT_EXECUTION + REFINE_TESTS_JUDGE | qwen3.6:35b-a3b | Binary decision + revised spec on retry |

A hand-written fleet file must also satisfy a handful of structural constraints checked at seed/assignment time (`checkModelConstraints` in `internal/db/assignments.go`) — e.g. `DECOMPOSE_SPEC`/`RECONCILE_DECOMPOSITION` must share a model (self-review framing), while `AUDIT_DECOMPOSITION` must differ from `DECOMPOSE_SPEC` and `ANALYZE_EXECUTION` must differ from `EXECUTE_BEAD` (both need an independent reviewer). A violating assignment is rejected, not silently accepted.

## Design doc tips

- Add a `## Decomposition Notes` section with an explicit bead table (titles, output files, exit criteria). DECOMPOSE will transcribe it; AUDIT will find nothing to flag.
- Specify sequential bead dependencies explicitly: "Beads 2–5 all write `game.go` — AUDIT must not flag this as an independence violation."
- Use `go test -v . -run=TestFooBar` as exit criteria for individual beads. DECOMPOSE will automatically pair each source file bead with its test file.
- Lint the doc before running it: `go run ./cmd/checkdesigndoc --doc=design_doc.md --checks=all` runs pins/ambiguity/construction-form/bead-size checks and catches a class of doc-precision problems (verbatim pins dropped on compression, one-reading-per-rule violations, oversized bead surface area) before they turn into a mid-run escalation.

## Architecture notes

The mechanical layer (deterministic, no model call) is where language-specific knowledge lives — Go test file inference, `var _` compile-time assertion checks, `go.mod` detection, exit criterion fixup. The prompt layer is largely language-agnostic. Adding support for a new language means extending the mechanical layer; the FSM and model calls don't change.

Ratchet is currently Go-only. See `internal/guidance/` for language detection and `internal/verbs/mechanical_checks.go` for the fixup pass applied at every commit point.