# Ratchet

An autonomous local coding pipeline. Give it a design document; it produces working, tested code — no cloud APIs, no human in the loop except when it gets genuinely stuck.

Ratchet runs on consumer hardware using [Ollama](https://ollama.com)-served LLMs. All model calls are local.

## How it works

Ratchet decomposes a design document into **beads** — scoped units of work, each with a fixed set of output files and an exit criterion (a shell command that must pass, e.g. `go test -v . -run=TestApplyMove`). Beads execute sequentially; the pipeline doesn't advance until the current bead passes its exit criterion.

Each bead runs through an eight-verb FSM:

```
DECOMPOSE → AUDIT → RECONCILE → EXECUTE → ANALYZE → COMPRESS → ADJUDICATE → (next bead or escalate)
```

**Decomposition phase** (runs once per project):
- **DECOMPOSE** reads the design doc and produces a JSON list of bead specs. Bead count emerges from the model call — it's not predetermined.
- **AUDIT** cross-reviews the decomposition for structural problems (test beads without test files in `output_files`, invalid exit criteria, etc.).
- **RECONCILE** applies AUDIT's findings, updating individual bead specs without re-decomposing.

**Execution loop** (runs once per bead, up to `--max-attempts` retries):
- **EXECUTE** runs a tool-use loop (`read_file` / `run_command` / `write_file` / `declare_success`). A concurrent MONITOR fires SIGTERM if the model loops without progress.
- **ANALYZE** reads the execution trace and produces structured findings — what was done, what the test output said, what went wrong.
- **COMPRESS** summarizes attempt history with `[NEW]`/`[RECURRING]`/`[RESOLVED]` tags. Passthrough on attempts 1–2.
- **ADJUDICATE** decides: `declare_success`, `execute_revised` (retry with stronger hints via a specificity ratchet), or `declare_escalation` (pause for human intervention).

At terminal state, Ratchet writes `traces/bead-{id}-report.md` — a single-document summary of every attempt, spec revision, test result, and ADJUDICATE decision.

## Requirements

- Go 1.23+
- [Ollama](https://ollama.com) with models pulled (see fleet below)
- SQLite (via `modernc.org/sqlite` — no system library required)

The default fleet requires approximately 55 GB VRAM to hold all three models resident simultaneously. A single high-VRAM GPU or multiple GPUs sharing memory works.

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
  --ollama=http://localhost:11434 \
  --addr=localhost:8080
```

The server runs the orchestrator and a dashboard UI in the same process. Open `http://localhost:8080` to watch progress.

**3. Wait.** Ratchet runs autonomously. Check `traces/` for per-bead execution logs and post-execution reports. If a bead escalates, investigate the report and advance or fix manually.

## Model fleet

The active fleet is set per-project via `--fleet`. Example fleet file:

```json
{
  "DECOMPOSE_SPEC":             "qwen3:32b",
  "RECONCILE_DECOMPOSITION":    "qwen3:32b",
  "AUDIT_DECOMPOSITION":        "gemma4:31b",
  "EXECUTE_BEAD":               "gemma4:31b",
  "MONITOR_EXECUTION":          "mistral-small3.2:24b",
  "ANALYZE_EXECUTION":          "qwen3:32b",
  "COMPRESS_ANALYSIS":          "mistral-small3.2:24b",
  "ADJUDICATE_NEXT_EXECUTION":  "gemma4:31b"
}
```

| Role | Recommended model | Notes |
|---|---|---|
| DECOMPOSE + RECONCILE | qwen3:32b | Strong structured JSON, full-doc reasoning |
| AUDIT | gemma4:31b | Fast cross-checker |
| EXECUTE | gemma4:31b | Tool-use loop; reads files before writing |
| MONITOR | mistral-small3.2:24b | Needs fast inference for 30s ticks |
| ANALYZE | qwen3:32b | Hedged interpretation of trace findings |
| COMPRESS | mistral-small3.2:24b | Rolling summary with NEW/RECURRING/RESOLVED tags |
| ADJUDICATE | gemma4:31b | Binary decision + revised spec on retry |

## Design doc tips

- Add a `## Decomposition Notes` section with an explicit bead table (titles, output files, exit criteria). DECOMPOSE will transcribe it; AUDIT will find nothing to flag.
- Specify sequential bead dependencies explicitly: "Beads 2–5 all write `game.go` — AUDIT must not flag this as an independence violation."
- Use `go test -v . -run=TestFooBar` as exit criteria for individual beads. DECOMPOSE will automatically pair each source file bead with its test file.

## Architecture notes

The mechanical layer (deterministic, no model call) is where language-specific knowledge lives — Go test file inference, `var _` compile-time assertion checks, `go.mod` detection, exit criterion fixup. The prompt layer is largely language-agnostic. Adding support for a new language means extending the mechanical layer; the FSM and model calls don't change.

Ratchet is currently Go-only. See `internal/guidance/` for language detection and `internal/verbs/mechanical_checks.go` for the fixup pass applied at every commit point.