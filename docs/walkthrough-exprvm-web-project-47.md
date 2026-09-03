# How Ratchet Works: A Walkthrough of Project 47

*An introduction to the pipeline, told through one real run.*

---

## What this run was

Project 47 (`exprvm-web-baseline-7`) asked Ratchet to build **exprvm-web**: a
small web REPL for an integer expression language — arithmetic, variables,
`print(...)`, bare expressions, no control flow — served over HTTP with HTMX
fragment updates. The finished program is ~15 Go files: a lexer, a
recursive-descent parser, a bytecode compiler + disassembler, a stack VM, an
environment, HTTP handlers, `html/template` views, a `main`, and one test file
per source file plus two integration tests.

The **entire input** to Ratchet was a project folder and a 59 KB design
document. After kickoff there was no human in the loop. The run went start to
finish in **9 hours 22 minutes**, unattended, driving a fleet of five local
models through one Ollama instance.

**Why this run matters:** it is the *first* exprvm-web build ever to reach
`complete`. Seven prior attempts (`exprvm-web-v2` … `v7`, plus baselines 1–6)
all `full_stopped` — every one of them at or before the `handlers-templates`
bead. Project 47 cleared it and shipped a working app: `go build` and
`go test` clean, runs on `:8080`, smoke-tested live.

| | |
|---|---|
| **Beads** | 9 planned, 9 succeeded, 0 escalated, 0 abandoned |
| **Execution attempts** | 11 (10 succeeded, 1 timed out and was retried) |
| **First-attempt beads** | 7 of 9 |
| **Human interventions** | 0 |
| **Infra failures / monitor kills / stalls** | 0 / 0 / 0 |
| **Verb calls needing a re-try for malformed output** | 2 of ~90 |
| **Wall-clock** | 33,710 s (9 h 22 m) |

---

## The fleet

Ratchet does not use one model. Each *verb* (pipeline step) is pinned to the
model that does that job best; the Ollama box holds one model in VRAM at a
time and swaps as the pipeline moves.

| Model | Verbs it ran | Role |
|---|---|---|
| `muse-glimmer:30b-q8_0-dflash` | EXECUTE_BEAD | writes the implementation code |
| `gemma4:31b` | SURVEY_SPEC, DECOMPOSE_SPEC, RECONCILE_DECOMPOSITION, REFINE_TESTS_WRITE, REFINE_TESTS_JUDGE, ADJUDICATE_NEXT_EXECUTION | planning + test authoring + judgment |
| `qwen3:32b` | AUDIT_DECOMPOSITION, CERTIFY_MANIFEST, REFINE_TESTS_CRITIQUE, ANALYZE_EXECUTION, REVISE_PENDING | independent review (deliberately a different family from the producer) |
| `mistral-small3.2:24b` | MONITOR_EXECUTION, COMPRESS_ANALYSIS | fast, latency-sensitive loops |

A guiding rule visible here: **the reviewer is never the producer.** `gemma`
writes tests; `qwen3` critiques them. `muse` writes code; `qwen3` analyzes the
run; `gemma` adjudicates it. No model grades its own homework.

---

## Phase 1 — Bootstrap (once, ~15 minutes)

Before any bead runs, Ratchet turns the design doc into a validated plan:

```
SURVEY_SPEC ──▶ VERIFY_MANIFEST ──▶ CERTIFY_MANIFEST ──▶ DECOMPOSE_SPEC ──▶ AUDIT_DECOMPOSITION ──▶ RECONCILE ⟳
  (gemma, 4m)    (model-free, 3s)     (qwen3, 31s)          (gemma, 6m)         (qwen3, 2m)            (gemma)
```

- **SURVEY_SPEC** reads the design doc and produces a file/symbol manifest
  (`survey.md`): every type, function, and file the project will contain.
- **VERIFY_MANIFEST** is a *mechanical* verb — no model. It checks the manifest
  against the doc structurally.
- **CERTIFY_MANIFEST** is the model's sign-off that the manifest is complete
  and consistent (rejections here loop back to SURVEY; 5 rejections
  `full_stop` the project).
- **DECOMPOSE_SPEC** cuts the work into **beads** — the atomic units Ratchet
  executes. It produced 9, in dependency order:

  `lexer → parser → env → compiler → vm → handlers-templates → cli → integration-persistence → integration-error`

- **AUDIT_DECOMPOSITION** is an independent pass (different model) checking the
  decomposition for correctness, independence, and exit-criteria quality. It
  found issues, which routed to **RECONCILE_DECOMPOSITION**.

### Iteration: RECONCILE round 1 was rejected — by a check, not a model

RECONCILE's first proposed fix tried to strengthen the `env` bead by adding a
required test function `TestNewEnvironment` to the exit criteria. A
**mechanical structural check** — not a model judgment — bounced it:

> bead "env": your change adds a new required test function
> `TestNewEnvironment` to the exit criteria, but the bead prose does not
> describe it — add a sentence stating what `TestNewEnvironment` must test, or
> do not add the criterion.

The rule being enforced: an exit criterion the bead's own prose doesn't
justify is not allowed in. RECONCILE resubmitted with the prose added; round 2
**converged**, and the plan was frozen.

---

## Phase 2 — The per-bead loop

Every bead runs the same chain. Because each bead's output includes a
`*_test.go` file, it enters in **test-first mode**: the tests are written and
adversarially reviewed *before* the implementation is allowed to exist.

```
REFINE_TESTS_WRITE ──▶ REFINE_TESTS_CRITIQUE ──▶ REFINE_TESTS_JUDGE ──┐
       ▲                                                              │ approved
       └──────────────── revise (≤5 cycles) ◀────────────────────────┤
                                                                      ▼
                    EXECUTE_BEAD ──▶ ANALYZE_EXECUTION ──▶ COMPRESS_ANALYSIS ──▶ ADJUDICATE_NEXT_EXECUTION
                         ▲                                                              │
                         │  execute_as_is / execute_revised / re_refine                 │
                         └──────────────────────────────────────────────────────────────┤
                                                                                        ▼
                                                                        declare_success ──▶ REVISE_PENDING ──▶ next bead
```

- **REFINE_TESTS_WRITE** (`gemma`) writes the test functions the bead's exit
  criteria name. The implementation files are stubs; a generated
  `do_not_use_this_test.go` holds symbol references so the partial package
  compiles.
- **REFINE_TESTS_CRITIQUE** (`qwen3`, a *different* model) attacks the tests.
  It runs a mandatory `run_go_snippet` loop — it must actually execute Go to
  verify a behavioral claim, not reason about it — and reports concrete
  defects.
- **REFINE_TESTS_JUDGE** (`gemma`) rules `approved` or `revise`. `revise` loops
  back to WRITE (cap: 5 cycles).
- **EXECUTE_BEAD** (`muse`) is a pure tool-calling loop (`write_file`,
  `read_file`, `run_command`) with a wall-clock budget. It implements the bead
  against the now-frozen tests. A separate **MONITOR_EXECUTION** subprocess
  (`mistral`) watches the trace live and can kill a runaway.
- **ANALYZE_EXECUTION** (`qwen3`) + **COMPRESS_ANALYSIS** (`mistral`) turn the
  raw trace into a structured, compact record.
- **ADJUDICATE_NEXT_EXECUTION** (`gemma`) decides what happens next:
  `declare_success`, `execute_as_is` (retry), `execute_revised` (retry with an
  amended spec), `re_refine` (send the tests back), or `full_stop`. It, too,
  runs a `run_go_snippet` verification loop against the real package.
- **REVISE_PENDING** (`qwen3`) — after a bead succeeds — edits the *downstream*
  beads' specs to reflect what was actually built, then dispatches the next
  bead.

### Per-bead results

| # | Bead | Exec attempts | Spec revisions | Wall | What happened |
|---|---|---|---|---|---|
| 304 | lexer | 1 | 1 | 5 m | clean |
| 305 | parser | **2** | 2 | 37 m | timed out, self-corrected, passed |
| 306 | env | 1 | 2 | 1 m | clean |
| 307 | compiler | 1 | 3 | 11 m | 2 test-refinement cycles |
| 308 | vm | 1 | 2 | 3 m | clean (1 CRITIQUE retry) |
| 309 | handlers-templates | **2** | 3 | 23 m | *the historical killer* — 3 refine cycles, ADJUDICATE bounced it once |
| 310 | cli | 1 | 2 | 3 m | clean |
| 311 | integration-persistence | 1 | 1 | 2 m | clean |
| 312 | integration-error | 1 | 2 | 3 m | clean (1 ADJUDICATE retry) |

"Spec revisions" > 1 without a failed execution means **REVISE_PENDING** edited
that bead's spec as upstream beads landed. Concrete example: after `env`
succeeded, the `compiler` bead's spec gained the line *"The Environment struct
is already defined with `Slots` as `map[string]int` and `Values` as `[]int64`
— do not redeclare these."* — feeding forward the real shape of what existed
so the next executor wouldn't collide with it.

---

## Phase 3 — The iterations and problems worth studying

### Bead 305 (parser): a timeout that fixed itself

The parser is the hardest pure-logic bead. `muse`'s first EXECUTE_BEAD attempt
hit the 900 s budget with `parser.go` still stubbed — it was composing the
whole file before its first `write_file` call and ran out of runway.

ANALYZE fed the trace to ADJUDICATE, which classified it precisely:

- **trend:** `same` (not diverging)
- **bead-spec fit:** `execution_capability_problem` — the *plan* is fine; the
  *execution* needs more room, not a rewrite
- **decision:** `execute_revised`

The revision was deliberately minimal: **one sentence** prepended to the spec
—

> Write each output file as a minimal compiling skeleton FIRST and flesh it
> out in later turns, rather than composing the whole file before the first
> `write_file` call.

— plus the orchestrator **mechanically doubling the budget** to 1800 s. The
second attempt passed in 1179 s.

This is Ratchet's *fail-fast* behavior, and it was softened just before this
run. An earlier version rewrote the entire spec as a prescriptive
implementation guide on a timeout — which made models fixate on the preamble
and spiral. The lighter touch (skeleton hint + more time) converged in **2
executions** where the old behavior on `gemma` took 4.

### Bead 309 (handlers-templates): a green test suite that was still wrong

This exact bead `full_stopped` all seven previous exprvm-web attempts. Here it
took **3 REFINE_TESTS cycles** (JUDGE asked for `revise` twice) tightening
exact assertions — the precise text of `entry.Err`, whether `Output` has a
trailing newline, whether an empty result element should render at all.

`muse`'s first implementation **passed every one of those frozen tests.** But
ADJUDICATE, running its own `run_go_snippet` checks against the real package,
caught **two spec violations the tests had missed**:

1. The template rendered an empty `<div class="error"></div>` on a successful
   assignment. The spec says show *either* `Output` or `Err` — so with
   neither, the element shouldn't appear.
2. The handler held the mutex through template rendering I/O instead of
   copying `history` into a local snapshot and releasing first. The spec
   pinned "local snapshot."

Decision: `execute_revised`. `muse` fixed both against the amended spec; the
second attempt was `declare_success`. This is the mechanism that stops "tests
are green" from being mistaken for "code is correct."

### The 29-minute adjudication

That same ADJUDICATE call on bead 309 took **29 minutes** — against a 3–13
minute norm for the other ten in the run. The cause: one degenerate turn where
`gemma`'s reasoning stream ran to the generation-token cap
(`done_reason: "length"`) without ever emitting a verdict, then recovered on
the next turn. The *verdict quality was fine* — it caught two real bugs — it
was purely wall-clock. A follow-up run (project 48) put 10/10 ADJUDICATE calls
in the 2.7–8.1 minute band, confirming this as an outlier rather than a
systemic pathology.

### What didn't go wrong

Worth stating explicitly, because it's the backdrop the above stands out
against:

- **0 escalations.** No job ever exceeded its retry tolerance.
- **0 infrastructure failures, 0 monitor kills, 0 no-write stalls** across 11
  executions.
- **2 malformed-output retries** in ~90 verb calls — one CRITIQUE, one
  ADJUDICATE — both recovered on the next attempt.
- **7 of 9 beads** succeeded on the first execution attempt.

---

## Phase 4 — Completion

The last bead's ADJUDICATE returned `declare_success` with no pending beads
left, which flips `projects.status` to `complete`. The project folder holds a
working Go web application: `go build ./...` and `go test ./...` pass, and
`go run .` serves the REPL on `:8080`.

---

## Where the 9.4 hours went

| Verb | Calls | Share of wall-clock |
|---|--:|--:|
| REFINE_TESTS_CRITIQUE | 11 | 27.9 % |
| REFINE_TESTS_WRITE | 11 | 23.1 % |
| EXECUTE_BEAD | 11 | 15.7 % |
| ADJUDICATE_NEXT_EXECUTION | 11 | 15.2 % |
| REFINE_TESTS_JUDGE | 11 | 11.2 % |
| REVISE_PENDING | 8 | 1.9 % |
| ANALYZE_EXECUTION | 11 | 1.5 % |
| DECOMPOSE_SPEC | 1 | 1.1 % |
| everything else (COMPRESS, SURVEY, RECONCILE, AUDIT, CERTIFY, VERIFY) | — | 2.4 % |

**The test scaffold, not the coding, is the cost.** REFINE_TESTS
(WRITE + CRITIQUE + JUDGE) is **62 %** of the run. CRITIQUE alone — `qwen3`
grinding the mandatory snippet-verification loop — averages ~15 minutes a
call. With a capable executor model, *writing the implementation* is now one
of the cheaper phases. That is the current optimization target.

---

## What actually made this run succeed

Three framework changes shipped immediately before it (commit `9814a08`):

1. **EXECUTE_BEAD executor: `gemma4:31b` → `muse-glimmer:30b-q8_0-dflash`.**
   `gemma` could not reliably build the `parser` or `handlers-templates` beads
   on this hardware; `muse` cleared both, and was faster on every comparable
   bead.
2. **Per-turn output cap raised 8192 → 16384 tokens.** Load-bearing: `muse`'s
   winning `parser` turn carried more than 41,000 characters of reasoning,
   well over the old ceiling — which would have truncated it mid-file.
3. **Fail-fast softened** (see bead 305): a first timeout now retries with a
   doubled budget and a one-line hint, not a wholesale prescriptive spec
   rewrite.

The prior seven attempts weren't stopped by the pipeline logic — they were
stopped by the executor model. Swap in one that can do the work, and the
scaffold around it — plan, review, execute, adjudicate, feed forward — carries
a 59 KB spec to a working program without a human touching it.
