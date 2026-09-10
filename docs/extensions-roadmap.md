# Extending ratchet beyond from-scratch pipelines

Started 2026-07-18, after the design-doc rewrite pass on the five stress-test fixtures
(`docs/stress-test-roadmap.md`) and a live demo of the fractal-smoke-2 output. Motivation:
every capability ratchet has today assumes a fresh design doc describing a from-scratch
build. The next class of work is teaching it to build *on top of* prior projects instead
of only building new ones from nothing — reuse another project's code, modify a
project that's already complete, or fix a defect in one. This doc captures the concrete
cases considered, the shared primitives extracted from them, and the sequencing decision.

**Revised 2026-09-10** with a strategic re-frame (general agent loops have absorbed
actor-critic; what still differentiates ratchet) and a concrete build spine: a
two-aspect codebase-analysis front end (`MODEL_CODEBASE` + `DEEP_DIVE`), a
change-request-to-delta verb (`SPECIFY_CHANGE`), the two traversals as verb chains,
and a Claude-baseline / skillification track. The 2026-07-18 case taxonomy and the
separate-app rejection are unchanged and still load-bearing.

Do not implement anything here without a fresh proposal — this is a planning document,
not an approved design. Per [[feedback_propose_before_apply]], each phase below still
needs its own concrete design pass before code changes land, the same way
`docs/stress-test-roadmap.md` Phase C already calls for. The "Rec:" tags in the open
questions are starting positions, not decisions.

---

## Status (2026-09-10)

- **Plan shape agreed at sketch level**, not designed. Two traversals (`extend`,
  `debug`) over the existing verb set, fronted by codebase analysis, built on the
  existing cascade mechanism.
- **Nothing built.** Phase 1 (unowned-files primitive) is the first code.
- **Open questions**: the 2026-09-10 set was resolved in an interactive pass (§ Open
  questions → Resolved) — starting positions, each phase's design pass can revisit.
  Headlines: analysis verbs run as embedded bounded agent loops with mechanical seam
  checks (A2); DEEP_DIVE is a toolset not a verb (C2); regression = full suite for now
  (A3); conceptual model per-run in Phase 2, blessed+pinned by Phase 3 (C1);
  SPECIFY_CHANGE elaboration always-on with an *opt-in* review pause (CR1/CR2) so
  `CHECK_DELTA` carries the safety weight; Phase 3 target is lsystem + color
  directives (V1). Two 2026-07-18 items remain genuinely open (blocked on
  prerequisites).

## Strategic anchor (2026-09-10)

General agent loops now ship actor-critic self-review, so that is no longer a ratchet
differentiator. Three properties still are, and the extend/debug work must preserve
all three or there is no reason to use ratchet over a general loop:

1. **Unconditional mechanical gates** — `VerifyExitCriteriaIsolated`, the
   bead-structure gate, the F-A gate. A general loop decides for itself when it is done.
2. **Decomposition with isolation** — beads as independently-verifiable sandboxed
   units. The long-horizon story.
3. **Reproducible process testing** — fixtures/clones/baselines regression-test the
   pipeline itself.

Everything else is negotiable. Where a general agent loop is genuinely stronger
(open-ended codebase exploration), the intended pattern is to embed one as a *bounded
sub-call whose output is mechanically validated at the seam* — not to trust it, and
not to reimplement it as a single-shot verb.

---

## The five concrete cases

1. **Modify functionality in an existing codebase.** Change chess's text-based board
   and input to a graphical, click-to-move UI, on top of an already-COMPLETE chess
   project.
2. **Fix a reported defect in an existing codebase.** Given a symptom ("castling
   queenside gives the wrong result"), diagnose and repair it — no design doc, no
   behavioral spec to expand.
3. **Reuse an existing codebase as a library/import.** A new project's own code calls
   directly into another project's exported functions (e.g. wrapping the fractal
   library or a set of http-handlers in a new project).
4. **Reuse an existing codebase as an external executable dependency.** A new project
   shells out to another project's *compiled binary* rather than linking its code
   in-process (e.g. the fractal library as a PNG-generating engine behind an HTTP
   server).
5. **Sequential composition via a published interface.** Write a backend project,
   then write a frontend project against the backend's own generated API docs. Not
   really a new case — included as a test of whether the others' generalized
   mechanism also covers it.

## What each case actually needs

Verified before writing this down, not assumed:

- **Case 5 already works today, unmodified.** Confirmed by reading the actual verb
  code (`internal/verbs/decompose_spec.go`, `audit_decomposition.go`): Cross-Bead
  Contract `producer`/`consumer` fields are pure free-text prose. No code anywhere
  parses or validates them against a bead-title/ID registry — DECOMPOSE reads the
  whole design doc as a text blob, AUDIT is prompted to cross-check bead content
  against that prose, but it's LLM judgment, never a structural lookup. Nothing stops
  a frontend project's design doc from citing a backend project's published API docs
  as a contract's producer.

- **Case 1** needs a way to *resurvey real code* instead of SURVEY_SPEC authoring from
  nothing, a delta design doc describing only the intended change, and a DECOMPOSE
  variant scoped to changed files only. This is `docs/stress-test-roadmap.md` Phase
  C's original framing, unchanged. **2026-09-10:** elaborated below — "resurvey real
  code" splits into `MODEL_CODEBASE` + `DEEP_DIVE`; "delta design doc" is now an
  *output* of `SPECIFY_CHANGE` from a free-text change request, not a required input.

- **Case 3** needs one of two alternatives:
  - **(a) Copy pre-vetted source files into the new project's own package.** Simpler —
    both projects already use `package main`, so files copied verbatim need no
    rename, no import statement, no `go.mod` changes at all; they just compile as
    part of the same package as the new handlers/templates/main.go. Requires the
    **unowned-files primitive** below.
  - **(b) True Go module dependency** (`go.mod` `replace` directive). Requires
    renaming the dependency's package away from `main` (Go cannot import a `main`
    package — a language rule, not a ratchet limitation) and teaching SURVEY_SPEC to
    recognize externally-supplied symbols it must not scaffold. Only worth it if the
    same library needs reuse across *multiple* future consumer projects; copy-in
    doesn't scale to that without manual re-copying on every upstream change.

  **Candidate consumer projects for Case 3** (build once the unowned-files
  primitive lands — not before):
  - [ ] fractal library + HTTP wrapper — the original motivating example, a web
        server exposing `GenerateMandelbrot`/`GenerateJulia`/`GenerateSierpinski`.
  - [ ] kafka-sim + visualization partner (added 2026-07-18) — a project that
        leverages the `kafkasim` library to run a live simulation and serves a
        real-time view of it, in the same spirit as the one-off hand-built demo
        artifact (swimlanes per partition, routing by key hash, consumer offset
        progress) but as an actual generated ratchet project, not a scratch script.
        This is deliberately the **second** independent consumer of the
        leverage-by-copy mechanism — see the Case 3a/3b open question below, which
        was explicitly waiting on a second real example before deciding anything.

- **Case 4** needs the dependency project to have an actual CLI entry point first —
  fractal-smoke-2 has no `func main()` today, so *adding one* is itself a Case-1-shaped
  "modify an existing project" problem. Once the dependency is invocable, Case 4 also
  needs a new Cross-Bead Contract flavor for an external-process contract (args, exit
  codes, stdout/file format) and a build-time step ensuring the binary exists before
  the consuming project's beads run.

- **Case 2 does not reduce to decomposition at all.** There is no written behavioral
  spec to expand into beads — the only artifact is a symptom description. It needs
  something upstream of DECOMPOSE that doesn't exist in any form today: reproduce →
  localize → hypothesize root cause → scope a fix. Only the final "patch + verify"
  step resembles EXECUTE_BEAD, and only once a precise, localized, bead-shaped fix
  spec (`output_files` + `exit_criteria`) already exists. **2026-09-10:** this is now
  the `debug` traversal — `REPRODUCE` + `LOCALIZE` verbs, each with a mechanical gate,
  bottoming out at a synthesized bead.

## The shared primitives

Two mechanisms recur across the four cases that need new work (1, 3a, 4's CLI-adding
sub-problem):

**(a) Unowned files.** The pipeline has an unstated invariant — every file in a
project folder belongs to exactly one bead — enforced independently in three
separate places, found by reading the actual code, not assumed:
- `checkNoBehavioralTests` (`internal/verbs/certify_manifest.go:175-196`) walks the
  entire folder for stray `_test.go` files, not scoped to the manifest.
- `wipeGoProject` (`internal/verbs/scaffold_go.go:271-282`), fired on any
  CERTIFY_MANIFEST rejection, unconditionally deletes every `.go`/`go.mod`/`go.sum`
  file in the folder — a normal reject-and-resurvey cycle would destroy pre-existing
  library files, not just a rare edge case.
- `checkUndeclaredFiles` (`internal/verbs/analyze_execution.go:237-294`) flags
  anything on disk absent from every bead's `output_files`, forever, with no
  allowlist mechanism anywhere in the schema today.

  The fix is one canonical "files this project run doesn't own" concept that all
  three consult, mirroring `fixtureScopedTables` in `internal/project/fixture.go` —
  a single named source of truth other code paths reference, not three independent
  ad hoc checks that can silently drift out of sync with each other.

  This primitive serves **both** leverage (Case 3a: files copied in from elsewhere)
  and modify-in-place (Case 1: files already in this project's own folder from a
  prior, already-succeeded bead) — the downstream mechanical handling is identical
  either way; only the provenance of the files differs.

**(b) Resurvey-from-real-code + delta decomposition.** Needed directly by Case 1, and
as a prerequisite for Case 4's "give the dependency a CLI" sub-problem. **2026-09-10:**
now designed in the "Codebase analysis" and "Traversals" sections below — this was
the bigger of the two lifts and the September pass is that dedicated design pass.

---

## Codebase analysis — two aspects (2026-09-10)

The front of both traversals. Deliberately split, because exhaustively analyzing a
whole codebase at implementation fidelity is expensive and error-prone, while a
higher-level model is cheap and the expensive drill-down can then be *targeted*.

The reliability concern: the fleet misreads *specs* routinely (lsystem `grammar`
compression, cron-studio symbol bleed), and reading code to *write* a description is
strictly harder. So neither aspect trusts a model freely — both are structured as
mechanical ground truth + a clearly separated interpretation layer + verification
where a claim is used, the same discipline as ANALYZE_EXECUTION's `mechanical_findings`
vs `analyzer_interpretation` and pin injection's verbatim-not-paraphrase rule.

### Conceptual model — `MODEL_CODEBASE`

A durable mental model, abstracted from implementation detail: components and their
responsibilities, relationships, data flow, key invariants, extension seams. Prose +
light structure. The kind of thing that stays roughly true across implementation
churn — "durable" meaning the mechanical extraction is deterministic and re-derivable
at any time and the schema is stable, not that a stale artifact is kept in sync.

- **Built from**: the mechanical spine — package/module graph, public API surface,
  call graph, type structure, test→code coverage map — as scaffolding, plus bounded
  model narration to synthesize the "what is this system and how is it organized"
  story. Light peeking into code during construction is allowed; the *output* stays
  high-level. The spine is mostly wiring `go/packages` / `go/types` / `callgraph`
  (already available via `golang.org/x/tools@0.33.0`), not new research.
- **Gate** (`VERIFY_MODEL`, model-free): every structural claim in the model is
  consistent with the mechanical spine — named components map to real packages, a
  claimed "X and Y are decoupled" is checked against the call graph.
- **Lifecycle**: semi-durable. Candidate for a persisted, human-blessed artifact
  (checked in, reviewed once, refreshed when the spine drifts materially) — highest
  leverage, easiest for a human to sanity-check, reused across every future
  extend/debug run against that repo. The natural human-review checkpoint.
- **Failure mode**: a wrong *architectural* claim, which poisons everything
  downstream and won't surface until execution. Mitigations: spine cross-check +
  human bless + deep-dive corrections.

### Targeted deep dive — `DEEP_DIVE`

Consults the conceptual model to decide *where* to look, then does
implementation-faithful analysis of just that region.

- **Input**: conceptual model + a specific question ("where does move validation
  happen and what is its exact contract", "trace the castling-queenside path").
- **Output**: grounded findings about the targeted region — real behavior, edge
  cases, the actual contract, gotchas — every finding location-tagged and
  re-checkable. May **execute** (run snippets, read tests, check types).
- **Lifecycle**: ephemeral, question-scoped. Generated on demand, discarded.
- **Feedback loop**: a deep dive that contradicts the conceptual model ("model says
  decoupled; found a shared global") annotates the working model for this run and
  queues a re-bless suggestion — mirrors AUDIT/RECONCILE.
- **Open**: is `DEEP_DIVE` its own FSM verb with an orchestration loop against its
  callers, or a toolset that `SPECIFY_CHANGE` / `LOCALIZE` use inside a bounded agent
  loop? (Open questions, ties to the embedded-loop question.)

## Turning a change request into a delta — `SPECIFY_CHANGE` (2026-09-10)

A change request ("make the board clickable, add undo") is not a delta design doc —
it is ambiguous by construction, and the entire precision chain assumes a doc that
has been through `draft-design-doc` / `checkdesigndoc`. `SPECIFY_CHANGE` bridges that
gap and is the hardest step in the `extend` traversal.

- **Input**: raw change request (free text) + the conceptual model + targeted
  `DEEP_DIVE`s.
- **Output**: a delta design doc at DECOMPOSE-precision — ambiguity classes resolved,
  construction-form pins, worked examples — plus a **mandatory "Interpretation &
  Assumptions" section** and an explicit scope statement.
- This is the **reconciliation layer**: vague intent × existing reality → a precise
  spec, surfacing conflicts explicitly ("you asked for X; the code assumes Y;
  resolving as Z"). Existing code resolves much of the ambiguity a greenfield doc
  must spell out; `SPECIFY_CHANGE` reads that out of the model + deep dives and
  writes it down precisely, flagging the genuinely underdetermined choices as stated
  assumptions rather than silently picking.
- **One path, variable effort.** A polished delta doc in → mostly validate and pass
  through. One sentence in → it does a lot. The gate runs either way.
- **Gate** (`CHECK_DELTA`): `checkdesigndoc --checks=ambiguity,bead-size` on the
  output, plus a scope-diff check (did the elaboration balloon beyond the request —
  the `refine_write_scope_diagnosis` failure shape, one layer up). Per **CR2** this is
  the *primary* automated defense — the user-review pause is opt-in, not mandatory —
  so it has to carry weight. This is the highest-risk seam: the lsystem `grammar`
  compression bug ("head substring before the first `(`" → "head = one ASCII letter")
  is exactly this failure mode, one layer up.
- **Optional user-review pause** before DECOMPOSE (pause-knob, off by default —
  **CR2**). Available for runs where the user wants a checkpoint on the delta doc.
- **Autonomy model**: proceeds with *stated assumptions*, no interactive Q&A
  mid-pipeline (**CR3**). The user corrects by editing the doc — at the opt-in pause,
  or by rewinding.
- `debug` needs no `SPECIFY_CHANGE` — `REPRODUCE` already forces a vague symptom
  down to a concrete failing test.

## The two traversals (2026-09-10)

Both are traversals of the *existing* verb set plus the new verbs above; both build on
the cascade mechanism (see "Reusable machinery"). Neither replaces the greenfield FSM.

### Traversal A — `extend` (Cases 1, 3, 4)

Entry: a completed project (or its fixture) + a **change request** (free text).

```
MODEL_CODEBASE ──► VERIFY_MODEL
      │
      ▼
SPECIFY_CHANGE  (change request + conceptual model + targeted DEEP_DIVEs →
                 delta design doc at DECOMPOSE-precision + Interpretation &
                 Assumptions + scope statement)
      │
      ▼
CHECK_DELTA     (checkdesigndoc --checks=ambiguity,bead-size ; scope-diff vs request)
      │
      ▼
[pause]         optional — user reviews / edits the delta doc (pause-knob, off by default)
      │
      ▼
DECOMPOSE_SPEC  (delta mode: append new beads, flag inherited beads whose contract
                 changed → cascade-style reset; conceptual model + existing code as
                 frozen scaffold; bead-size lint + merge/drop gate + pins unchanged)
      │
      ▼
AUDIT_DECOMPOSITION ──► RECONCILE_DECOMPOSITION      (unchanged)
      │
      ▼
per-bead pipeline (unchanged) + regression gate:
    declare_success also runs the pre-existing suite (go test ./... on the merged
    tree), not just the bead's own exit criteria
```

### Traversal B — `debug` (Case 2)

Entry: a completed project + a symptom string. No design doc.

```
MODEL_CODEBASE ──► VERIFY_MODEL
      │
      ▼
REPRODUCE      (symptom → minimal failing test; gate: fails on current code AT AN
                ASSERTION — not a compile error, not an unrelated panic; if it can't
                be reproduced, escalate — a legitimate terminal state)
      │
      ▼
LOCALIZE       (failing test + conceptual model + targeted DEEP_DIVEs → candidate
                files/functions + root-cause hypothesis, shaped as a partial bead;
                gate: every named symbol exists and is reachable from the test)
      │
      ▼
synthesize bead  (output_files = localized files; exit_criteria = {new test passes,
                  existing suite passes})
      │
      ▼
per-bead pipeline (unchanged)
    CRITIQUE +1 angle: root cause vs symptom mask — does the fix make the GENERAL
    case pass, not only the repro
```

## Reusable machinery (2026-09-10)

- **Cascade mechanism** (`clone-project --design-doc`, `docs/ratchet_state_machine.md`
  §4): already inherits a prior project's code + full bead/execution history,
  re-audits inherited beads against a replaced doc, and re-runs only changed beads.
  ~70% of "build on top of a prior project" plumbing, load-bearing in loop-mode
  today. `extend` = cascade where the doc *grew* and DECOMPOSE *appends* beads; the
  delta lives in `MODEL_CODEBASE` / `SPECIFY_CHANGE` / DECOMPOSE delta-mode, not in
  bootstrap or the queue.
- **Precision machinery**: pin injection (`InjectDesignDocPins`), bead-size lint
  (`checkdesigndoc --checks=bead-size`), the DECOMPOSE merge/drop gate, the
  content-stall watchdog, `VerifyExitCriteriaIsolated`. Delta-decomposition inherits
  all of it unchanged.
- **Static-analysis tooling in-tree**: `go/ast` / `go/parser` / `go/token` already
  used in `internal/splice`, `verify_manifest.go`, `scaffold_go.go`;
  `golang.org/x/tools@0.33.0` already a dependency (→ `go/packages`, `go/types`,
  `callgraph`, `ssa`). The `MODEL_CODEBASE` mechanical spine is mostly wiring, not
  new dependencies.

## Phasing (2026-09-10 — elaborates the 2026-07-18 sequencing below)

| Phase | Deliverable | Validates on |
|---|---|---|
| 1 | Unowned-files primitive | fixture clone, no behavior change |
| 2 | `MODEL_CODEBASE` + `VERIFY_MODEL` (mechanical spine first, then narration) | inspect the conceptual model produced for the lsystem / cron-studio / exprvm-web fixtures |
| 3 | `DEEP_DIVE` + `SPECIFY_CHANGE` + delta-mode DECOMPOSE + regression gate → full `extend` on the cascade path | a real feature-add to a COMPLETE fixture |
| 4 | `REPRODUCE` + `LOCALIZE` → `debug` traversal | a known-root-cause bug (chess castling-queenside, or a seeded bug in a newer app) |

Each phase = its own concrete design pass before code, per [[feedback_propose_before_apply]].
Hand-step each traversal on one real case before wiring it into the queue
(audit-from-DB, per [[feedback_no_pause_after_reconcile]]).

Scope note: the completed fixtures are single-package `main`, ~500–2000 LOC — very
tractable. Prove the mechanism there before ratchet-sized codebases.

## Sequencing decision (2026-07-18)

Agreed order:

1. **Unowned-files primitive** — smallest, already scoped above, serves two cases.
2. **Resurvey + delta decomposition** — bigger, but load-bearing for both Case 1 and
   Case 4.
3. **Case 2 (bug-fix), evaluated empirically, not assumed to need a separate app.**

On the "separate app" question specifically: rejected, in favor of keeping the
orchestrator/model-fleet/trace-logging/UI/DB substrate shared and only letting the
*pipeline type* (the verb sequence, the shape of a "unit of work") differ per case.
Reasoning: that substrate is real accumulated value, not incidental scaffolding — this
session alone paid down a long list of substrate-level bugs (RECONCILE's escalation
tie-break, no-write false positives, budget-merge bugs, orchestrator queue-blocking)
that a forked harness would have to rediscover independently over time, with no
guarantee a fix in one ever reaches the other. That's the bit-rot risk named when this
was raised, and it's judged to outweigh the complexity saved by not forcing Case 2
into ratchet's existing verb/schema conventions. **The 2026-09-10 strategic anchor
reinforces this** — the orchestrator (gates, isolation, reproducibility) *is* the
differentiator now that actor-critic is commoditized.

The reframe that makes this tractable: Case 2's only genuinely novel piece is the
*front end* — turning a symptom ("castling queenside gives the wrong result") into a
localized, scoped fix. Once that localization exists, it is shaped exactly like a
bead (`output_files` + `exit_criteria`), and the existing
EXECUTE_BEAD → REFINE_TESTS → ADJUDICATE machinery may just work on it unchanged. So
the new capability is plausibly a diagnostic verb chain (`REPRODUCE` → `LOCALIZE`)
that terminates by handing off exactly where DECOMPOSE_SPEC would, not a parallel app
with its own queue and schema.

**This reframe is not yet verified.** Before committing to it, prototype the
diagnostic front end against one real case — the chess castling-queenside bug is a
good first test, since its actual root cause is already known from this project's own
history — and check empirically whether it cleanly bottoms out at a normal bead, or
whether the iteration pattern genuinely fights the existing verb/job conventions. If
it fights hard, that's real evidence for reconsidering the separate-app option; if it
doesn't, the duplication was correctly avoided.

## Conceptual-model bakeoff — Claude vs open models (2026-09-10)

`MODEL_CODEBASE` is the best candidate in the plan for frontier-model spend, for
reasons that invert the usual all-local rationale:

- **Low frequency** — once per repo, refreshed on drift. Per-call cost is nearly
  irrelevant here (contrast the REFINE loop at ~64% of pipeline cost).
- **Highest leverage, hardest to verify** — a wrong conceptual model poisons every
  downstream bead and there is no mechanical oracle for "is this architecture
  description correct." Returns to quality are outsized exactly where gating can't
  save you.
- **Widest frontier gap** — whole-codebase synthesis into a coherent architectural
  narrative, long context, iterative exploration: what the 30–35B fleet is weakest at
  (muse spirals on large surface area; CRITIQUE lands zero signal without execution).
- **Clean comparison** — the mechanical spine is deterministic regardless of model,
  so the bakeoff is purely over the narration layer.

How to run it:

1. **Decide the hosted-API question first.** The bakeoff sends the target codebase
   through a hosted model. Fine for ratchet's own dogfooding; a real deployment
   consideration as a general tool. If that is unacceptable even in principle for
   this one verb, skip the bakeoff.
2. **Score on downstream task success, not artifact grading.** No ground-truth model
   to grade against, and JUDGE is coin-flippy. Run the same change requests through
   `SPECIFY_CHANGE` + DECOMPOSE using each candidate's conceptual model; measure
   delta-doc quality and bead outcomes. Add a small human eval.
3. **Test creation and maintenance separately.** Creation (whole codebase, cold) is
   the hardest synthesis — favors frontier most. Maintenance (drift update, small
   context) is more mechanical — a fleet model may suffice. Likely outcome: frontier
   for one-time creation, fleet model for drift updates.
4. **Include a strong open tier** — not just Claude vs current incumbents. The real
   question is *how much* frontier buys here, and whether a larger open model closes
   most of the gap.

Sequencing: a Phase 2 activity — needs `MODEL_CODEBASE` specced and the mechanical
spine built before the narration layer can be baked off. Reuses the existing
qualify-model capture harness (`--capture-verb-io`, `docs/fleet-qualification.md`)
with a Claude adapter (below).

## Skillified verbs — an occasional Claude baseline (2026-09-10)

**Purpose: a diagnostic reference point, run occasionally, not a production backend.**
Every bakeoff so far compares fleet models to each other; there is no end-to-end
frontier baseline. Running the pipeline (chosen verbs, or the whole thing) with Claude
in the verb slot and comparing quality / cost / latency to the local fleet makes the
recurring "is the fleet good enough / where does it fall short" question
([[project_fleet_quant_strategy]], [[project_model_capability_aware_verbs]], every
prior bakeoff) directly answerable. Claude-as-a-production-backend is a *possible
later* use, explicitly not pursued here.

**Why skills specifically: they route the run through a Claude Code plan instead of
per-token API billing.** The orchestrator shells out to the local `claude -p` headless
CLI in the verb's dispatch slot (a small adapter); each verb's instructions live as a
skill file so the invocation is consistent, versioned, and keeps the prompt payload
small. Skills also make verbs invocable standalone (`/ratchet-critique <bead>`) as a
side benefit. A raw-prompt adapter would work too but loses the plan-billing route,
which is the whole point of this framing.

**Because it is occasional by design**, the usual objections soften: reproducibility
matters less (a calibration snapshot, not a pinned production path), and a full-
pipeline run *including* the REFINE loop is desirable — the REFINE comparison is
exactly what's missing. What to avoid is *continuous* use (plan-usage cost, and it
would drift into being a production dependency).

**Frictions to design around:**

- **Nested agent loop.** Claude Code headless brings its own agentic loop + tools
  (Bash/Read/Edit). Natural fit for `EXECUTE_BEAD` (already a loop); heavyweight for
  single-judgment verbs (`CRITIQUE`, `JUDGE`) — constrain hard, force structured
  output, cap turns.
- **Capture harness.** `--capture-verb-io` records Ollama token/timing stats; a
  Claude-backed verb needs a separate telemetry adapter in the qualify-model harness.
- **Skill authoring.** Each verb's prompt + tool-use contract has to be transcribed
  into a skill that a general agent follows faithfully — non-trivial for the verbs
  that currently lean on schema-mode / GBNF grammar constraints the fleet needs but
  Claude does not.

**Sequencing.** First concrete use is the conceptual-model bakeoff above (a few verbs:
`MODEL_CODEBASE`, then `SPECIFY_CHANGE` + `DECOMPOSE_SPEC` to score it downstream).
Expand to a full-pipeline baseline run once those skills exist and the telemetry
adapter is in place.

---

## Open questions

### Resolved 2026-09-10 (interactive pass — revisitable in each phase's design pass)

**Architecture**
- **A1. `extend` base** — *decide after Phase 2.* Cascade (`clone-project
  --design-doc`) is the leading candidate — it already inherits code + full history —
  but the choice between reusing it and a fresh bootstrap path is deferred until
  `MODEL_CODEBASE` exists and the concrete shape of a delta run is visible.
- **A2. Embedded general-agent-loop sub-calls** — *allowed, with mechanical seam
  validation.* `MODEL_CODEBASE` / `SPECIFY_CHANGE` / `LOCALIZE` / `DEEP_DIVE` run as
  bounded agent loops; every output is mechanically checked before anything downstream
  trusts it. These are the first non-uniform verbs in ratchet; the seam check is what
  preserves the gate discipline.
- **A3. Regression gate** — *full `go test ./...` on the merged tree at every
  `declare_success`, for now.* Move to an affected-subset optimization only if
  measured cost justifies it.

**Codebase analysis**
- **C1. Conceptual-model lifecycle** — *per-run for Phase 2; add bless + pin before
  Phase 3.* Regenerating each run is fine for inspecting `MODEL_CODEBASE` output
  against the fixtures. Persisted + human-blessed + drift-refreshed is the target once
  `extend` consumes the model for real work (amortization + human checkpoint).
- **C2. `DEEP_DIVE` form** — *a toolset the consuming verbs call inside their bounded
  loop*, not its own FSM verb. Consistent with A2. Promote to a verb later only if
  trace observability demands it.
- **C3. v1 conceptual-model depth** — *package/component graph, responsibilities,
  public contracts, call graph, test→code map.* Data-flow tracing and automatic
  invariant inference deferred to v2. Starting scope, not a commitment — the Phase 2
  fixture inspection is expected to tell us whether to pull data-flow forward.
- **C4. Deep-dive → model corrections** — *annotate this run's working copy; surface
  the contradiction; never auto-write-back.* A pinned model's contradiction goes to a
  human for re-bless. Mirrors the AUDIT/RECONCILE "raise, don't silently apply" pattern.
- **C5. `MODEL_CODEBASE` form** — *new verb*, not a `--from-code` mode on SURVEY_SPEC.
  Inputs / prompt / validation diverge too far for a flag.

**Change-request handling**
- **CR1. Elaboration path** — *always-on, one path.* Every `extend` run goes through
  `SPECIFY_CHANGE`; a polished delta doc passes through with light validation, a
  one-liner gets full elaboration. No separate mode, no user choice.
- **CR2. User-review pause** — *optional pause-knob, off by default.* Autonomous runs
  don't stop after `SPECIFY_CHANGE`. Consequence: `CHECK_DELTA` (checkdesigndoc
  ambiguity + bead-size + scope-diff vs the original request) is now the *primary*
  automated defense against a misread intent — weight it accordingly in the
  `SPECIFY_CHANGE` design pass. The pause knob is there for runs where the user wants
  the checkpoint.
- **CR3. Clarification** — *stated assumptions, never interactive.* `SPECIFY_CHANGE`
  writes its assumptions into the "Interpretation & Assumptions" section; the user
  corrects by editing the doc (at the opt-in pause, or by rewinding). No mid-run Q&A
  channel.
- **CR4. Delta-doc format** — *freeform + gate for the prototype; structured delta
  mode later.* Start with freeform prose validated by the ambiguity/bead-size/scope
  checks. Add a structured delta-doc format + native `checkdesigndoc` support once the
  shape is proven.

**Claude baseline / skillification**
- **S1. Skill-authoring fidelity** — *investigate during the first skillification.*
  `MODEL_CODEBASE` (new verb, no schema-mode baggage) is first regardless; learn what
  the schema-mode/GBNF verbs need when `DECOMPOSE_SPEC` comes up, rather than
  pre-committing to a shim.
- **S2. First verbs to skillify** — *`MODEL_CODEBASE`, then `SPECIFY_CHANGE` +
  `DECOMPOSE_SPEC`* — enough to run the conceptual-model bakeoff and score it
  downstream. Full-pipeline baseline (incl. `REFINE_TESTS_*`) is the eventual target,
  not the start.
- **S3. Telemetry adapter** — *match the existing `--capture-verb-io` field schema
  where the metrics line up (tokens, wall time, turn count); finalize in the
  skillification design pass.* Claude-native extras (cache hits, thinking tokens) can
  ride alongside without forking the harness's downstream consumers.

**Validation**
- **V1. Phase 3 target** — *lsystem run-6 (13/13, `v0.3`) + "add color directives to
  the L-system language."* Touches the parser and the renderer — a multi-bead delta
  against a well-understood codebase.
- **V2. Verb naming** — *decide at implementation.* Not load-bearing. Working names in
  this doc: `MODEL_CODEBASE`, `DEEP_DIVE`, `SPECIFY_CHANGE`, `REPRODUCE`, `LOCALIZE`,
  and delta decomposition as a mode on `DECOMPOSE_SPEC`.

### Still open

- **Case 3a vs 3b** (copy-in vs true module dependency) [2026-07-18]: no decision.
  Compare the fractal-http and kafka-sim-visualization candidates once both exist,
  rather than deciding off one example. Don't decide until both are built.
- **Where the unowned-files list lives** (new `projects` column vs a separate table)
  [2026-07-18]: deferred to its own proposal (Phase 1 design pass).

---

**How to apply**: when resuming this thread, update this doc directly rather than
re-deriving the case taxonomy or the traversal design from scratch — same convention
as `docs/stress-test-roadmap.md`. [[project_roadmap]] and
[[project_extensions_roadmap]] memory should carry only a one-line pointer here.
