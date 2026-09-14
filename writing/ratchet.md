*This is the light, orienting piece. Five deep dives follow it: [the architecture and model fleet](https://claude.ai/code/artifact/79e0d9bd-aa5b-4b84-ac34-690c4e1d9f31) in detail, [the wire-level mechanics of talking to the fleet](https://claude.ai/code/artifact/059f0cd4-6cb7-4de1-ac32-6dd5413add1a) over Ollama, [the craft of writing specs](https://claude.ai/code/artifact/3638f7a6-2456-4d16-b69c-852b0756ad93) precise enough for small models, [the workflow that turns rough prose into one of those specs](https://claude.ai/code/artifact/f8159d21-e0b1-4529-8aab-d79de63f597b), and [a full account of the recent burn-in](https://claude.ai/code/artifact/46056184-99b7-4196-a475-170ad8d0f586). Where a claim below deserves more evidence than a paragraph can carry, that's where it's headed.*

## A primer: what changes when the model isn't Claude

If you've used Claude Code, you already have intuitions about what an AI coding agent can do: hand it a loosely-specified task, let it read the codebase, let it think for a while, and it will generally figure out what you meant. It asks reasonable follow-up questions. When it hits an ambiguity, it makes a sensible judgment call instead of falling over. When it's done, it's usually actually done.

Ratchet doesn't get to assume any of that, because Ratchet doesn't run on Claude. It runs on a fleet of small, open-weight language models — in the 24–35 billion parameter range — hosted locally via [Ollama](https://ollama.com) on a machine we own. That choice is deliberate: it's cheap, it's private, and it's a genuinely interesting question whether models an order of magnitude smaller than the frontier can be made reliable through *process* rather than raw capability. But it means every convenient assumption above is false.

A few concrete ways this shows up:

- **Ambiguity doesn't get resolved, it gets guessed at — badly, and inconsistently between runs.** Tell Claude "trim trailing whitespace" and it infers `\n` handling correctly from context nine times out of ten. Tell a 30B model the same thing across a hundred functions and you'll get a mix of interpretations, silently, with no signal that anything went wrong.
- **Structured-output constraints and reasoning fight each other.** Several of the models in the fleet are "reasoning" models that think in a separate stream before answering. Forcing a strict JSON grammar onto their output can suppress that thinking phase entirely — one core model in the pipeline, we discovered, had never once successfully emitted a tool call under a JSON-format constraint, across 139 recorded calls. Not a prompting problem. A structural one.
- **They get stuck.** A model can loop on the same failed compile error for turn after turn, or burn its entire response budget "thinking" without ever producing an answer, in a way a frontier model essentially never does in practice.
- **They lie about success, not maliciously — they just don't know.** A model that writes a file and reports success has no way to independently confirm the file landed on disk with the right bytes. It has to be checked.

None of this is a knock on the models — they're good at what they're asked to do *narrowly*. But it means Ratchet can't be one long agentic conversation the way Claude Code is. It has to be an assembly line: many small, single-purpose model calls, each one narrow enough that a small model can reliably do it, wrapped in mechanical checks that never trust a model's own account of what happened. Almost everything described below is a consequence of that one constraint.

## What Ratchet actually is

Ratchet takes a **design doc** — a plain-English specification of a piece of software — and turns it into working, tested Go code, autonomously, with no human in the loop unless something goes wrong.

It does this by breaking the design doc into **beads**: small, independently verifiable units of work, each with its own spec, its own test, and its own pass/fail exit criteria. A small utility project might be four or five beads; a genuinely integrated system can run well past a dozen. Each bead moves through a fixed pipeline — write a test, review the test, implement against it, check the result, decide what happens next — and the whole project advances bead by bead until every one has succeeded or the project needs a human's attention.

Everything is backed by a SQLite database that records every job, every model call, every decision, and every state transition. That matters more than it sounds: it means Ratchet is auditable rather than a black box, and it means recovery from a stuck project is a matter of querying a table, not guessing from scrollback. Live projects run as a daemon; a small web UI shows anything that needs human review (called an **escalation**) and lets an operator requeue or roll it back.

## The model fleet

Ratchet doesn't use one model for everything — it uses a roster, with different models cast into different roles, chosen the way you'd cast actors: not "who's best overall" but "who's right for this specific part, and does casting them create an actual second opinion where one is needed."

A few of the standing cast, as of the last qualification pass:

- **`muse-glimmer`** writes both the tests and the implementation code.
- **`qwen3:32b`** critiques the tests muse writes — deliberately a different model, so the reviewer isn't reviewing its own work.
- **`qwen3.6:35b-a3b`**, a fast mixture-of-experts model, judges the critique and adjudicates what happens after an implementation attempt.

That casting isn't guesswork — it comes from a homegrown benchmarking harness (`qualify-model`) that replays real historical decisions from a live project against a candidate model and grades the result: does the generated test actually compile, pass against a known-good implementation, and catch hand-planted bugs? Does the candidate's judgment call agree with what a human later confirmed was correct?

The results are genuinely counter-intuitive in places. The fast MoE model turned out to be excellent at *adjudication* — deciding what to do given clean evidence — agreeing with the incumbent's verdict on every single test case, four to five times faster. But put that same model on *critique* — open-ended bug-hunting with no evidence handed to it — and it collapsed to a near-0% catch rate, essentially rubber-stamping everything. The lesson generalized: **give a fast small model a well-defined judgment call and it's often excellent; ask it to go looking for problems open-endedly and it's not.** That single distinction ended up shaping a lot of what came later, including the current top fix-priority (more on that below).

## The verbs

Each step in the pipeline is called a **verb** — a single, narrow model call with one job. Roughly, in order:

1. **SURVEY / DECOMPOSE** — read the design doc, decide what already exists in the codebase, and break the spec into beads.
2. **AUDIT / RECONCILE** — a *second* model reviews the proposed decomposition for gaps or ordering problems before any code gets written, and the two negotiate a fix if it finds one.
3. Then, per bead: **WRITE** the test → **CRITIQUE** the test → **JUDGE** the critique (approve, or send it back for another WRITE cycle) → **EXECUTE** the implementation against the now-approved test → **ANALYZE / COMPRESS** what happened → **ADJUDICATE** the next move: try again as-is, revise the spec and retry, reject the test itself, declare success, or escalate.

The reason this is a chain of small verbs instead of one big "build this bead" agent loop comes straight back to the primer: no single call in that chain asks a small model to hold more context or make a bigger judgment than it can reliably handle. And several of the mechanical gates aren't judgment calls at all — a claimed "success" gets checked by literally running the bead's exit-criteria command against the files on disk before it's accepted. The model's word alone is never sufficient.

That preference for a mechanical fact over a model's opinion isn't limited to one gate — it runs through the whole pipeline as a habit. Wherever something can be established directly — did the file compile, does the test assert this exact string, did the exit-criteria command actually pass — the framework checks it and doesn't ask a model to judge it. Inference gets spent only where nothing mechanical can substitute for it. (One of the burn-in's biggest findings, below, is essentially a measurement of how much of one expensive verb's job turned out to belong in the first category rather than the second.)

The chain of small verbs buys more than smaller judgment calls, too — it buys smaller prompts. WRITE and CRITIQUE don't see the whole design doc, just the slice relevant to their one bead; a dedicated step exists purely to compress an execution's history down before handing it to the verb that decides what happens next. A frontier model can often afford to have everything in view and sort out what matters itself. A fleet of small models can't be trusted to do that sorting reliably, so the framework does the sorting for them, upstream, every time.

One more split worth knowing about, since it's easy to assume all of the above is Go-specific plumbing — it isn't, and the two halves are kept apart on purpose. Each verb's prompt is built from a generic system prompt (role framing, structural rules, output schema) with nothing language-specific in it, plus a separate, swappable file carrying the actual language mechanics: stub-body syntax, module-file format, compile-time-assertion conventions. Only Go is wired up today, but the split exists specifically so that isn't permanent — the generic half is meant to survive a second target language; the language-specific half is exactly the part that would need rewriting.

## Typical workflows

The common path is: write a design doc, start a project, and check back later. There's no log-tailing involved — progress is a query against the database (`projects.status`, how many beads have succeeded), and the daemon runs unattended.

When a bead gets stuck badly enough — a compiling test that stays broken, a model that keeps failing the same way past its retry budget — the job **escalates**, and the project waits for a human. The recovery playbook is deliberately narrow: never patch the database, never hand-edit a test to force it green, never resume a project mid-stream by nudging its state. Either **requeue** it (try the same thing again, useful for transient failures) or **rewind** the bead to a clean starting point and let the pipeline redo the work honestly. The philosophy is "touch a project once" — patching a live run to rescue it just teaches you less about the actual defect and leaves a project in a state nobody else can reason about.

There's also a second, already-working workflow for iterating on an already-completed project: clone it, swap in a revised design doc, and Ratchet diffs the new spec against the old bead-by-bead, re-running only what actually changed and leaving the rest untouched. That mechanism is solid today; it's one working piece of a much larger, still-early direction — teaching Ratchet to extend and debug existing code in general, not just re-run a project against a revised version of its own doc — described below.

## Crafting a design doc

If there's one skill that determines whether a Ratchet project succeeds, it's writing the design doc — and it's a genuinely different skill than writing a spec for Claude.

Claude fills ambiguity gaps with good judgment. A 30B model fills them with *a* judgment, inconsistently, run to run. So the craft that's developed here is about removing every gap a small model could misinterpret: worked examples with the exact computed number, rather than "compute this correctly"; construction forms pinned verbatim (does this data type get built as a pointer or a value? spelled out, not implied); exact literal strings required in a test, not paraphrasable requirements; caps on how much ground any single bead is allowed to cover, so no bead asks a model to hold too much at once.

There's tooling built around this discipline specifically — a design-doc-drafting skill and a companion checker that flags ambiguity classes mechanically (unpinned constants, contradictory prose, missing worked examples) before a doc ever reaches the pipeline. One of the burn-in's more vivid findings, below, is exactly the failure mode this tooling exists to catch: a doc that called the same construct "empty" in one sentence and "not empty" a few lines later, and derailed a live run until the contradiction was fixed.

## History, briefly

Ratchet started with the games — Connect Four, tic-tac-toe, a Go board (goban) — small, well-understood domains built specifically to *find* bugs in the framework, not to be interesting apps in themselves. They worked: an early top-down audit turned up roughly twenty issues in prompt design and verb logic.

From there the work moved into a run of numbered fixes and framework upgrades, mostly driven by watching real projects fail and asking why: a tool-loop that could spiral into reasoning it could never escape; the JSON-grammar-vs-reasoning conflict mentioned in the primer; a proper model-qualification harness so model swaps were evidence-based instead of guesswork; increasingly sophisticated ways to tell "this bead is making progress slowly" apart from "this bead is thrashing and needs to escalate."

Alongside that, a "decomposition framework" and a "precision chain" of design-doc improvements landed — bead-size limits, a doc-quality lint, the pinning discipline described above — aimed at the single largest recurring defect class across the whole project's history: ambiguity that compression (turning a careful spec into terse bead prose) had quietly dropped or inverted.

All of that converged, in September 2026, into a deliberate validation exercise: the burn-in.

## The burn-in

A burn-in, in this context, means: stop improving the framework for a while, and instead run it, seriously and honestly, against a batch of real design docs to find out whether it actually works — with a hard rule that nothing gets fixed inline. Every bug gets logged and the run continues (or escalates) as-is, so the resulting evidence describes the framework as it actually behaves, not as it behaves once you've been nudging it along.

The numbers: **nine design docs, run twice each with tie-breakers where the two runs disagreed — twenty-three total project runs.** Fourteen completed cleanly. Nine escalated (39%) — meaning they hit a wall a human needed to look at. **Zero showstoppers** — nothing that indicated the framework was fundamentally broken. And notably, **zero repeated root causes**: every escalation across 23 runs traced back to a genuinely different underlying mechanism, not the same bug recurring.

What sets this burn-in apart from a normal bug hunt is what happened after the runs finished: every one of the 14 named findings was re-examined a second time, from scratch, against raw evidence — actual log lines, actual git diffs, actual database rows — rather than trusting the write-up's own prose. The result: nothing was found to be simply wrong. A handful of findings gained a sharper or corrected root cause on the second pass, but the overall record held up. **Verdict: the framework is safe to build on going forward** — the recovery machinery (retries, escalation, human review) reliably catches what goes wrong, rather than silently producing bad output.

## A few surprising results

- **The most expensive step in the entire pipeline barely does anything.** CRITIQUE — the step where a model reviews a freshly-written test before implementation starts — accounted for **32.4% of all compute across the burn-in**, more than the actual code-writing step. And it returned zero findings on **78% of its calls**. It's not that CRITIQUE never catches anything real; a close read of one bead's four review cycles found that most of what it *did* catch was mechanically checkable in the first place — "does the test actually assert on this exact string the spec requires?" — the kind of thing a cheap literal-string check could catch without ever invoking a model. That finding reframed the project's top priority overnight: not chasing more individual bugs, but redesigning the review step to be a cheap mechanical filter with model reasoning reserved for what's actually ambiguous.

- **A model's diagnosis was completely correct — and it still made the wrong call.** In one escalation, the adjudicating model correctly identified that a test file was calling methods that belonged to a different unit of work entirely. That's the right diagnosis. But its playbook for responding treated the situation as a *timeout* (too much scope, narrow it) rather than a *stall* (the test itself was unreachable, so send it back for revision) — and it turned out the prompt simply had no instructions at all for the stall case, only the timeout case. The model didn't malfunction; it improvised sensibly with a legitimately incomplete playbook. That's a sharper and more useful finding than "the model made a mistake" — it points at a specific missing paragraph, not a model swap.

- **A suspected bug turned out to be a deliberately tested feature.** One escalation looked at first like the planning step inventing an unauthorized extra unit of work. Tracing it back to the actual test suite found a test explicitly asserting that exact behavior was *correct* — extra, supplementary units of work are allowed by design. The initially-proposed fix would have quietly reversed a considered decision. Caught before anyone touched the code, purely by insisting on primary evidence over a plausible-sounding narrative.

- **A design doc contradicted itself in adjacent sentences, and the framework caught it live.** One spec described an edge case as having "an empty body" in one paragraph and, a few lines later, described the same case as recognizably non-empty. A model hit the contradiction mid-run, correctly stalled, and escalated. The eventual fix, verified against the actual commit, was a two-line prose correction — the underlying logic had been right all along; only the English describing it was self-contradictory. It's a small story, but it's the cleanest possible demonstration of why the design-doc-precision work above isn't academic.

## The current frontier

The burn-in didn't just validate the framework — it produced a ranked list of what to fix next, and the ranking is itself informative:

1. **CRITIQUE's cost-and-reliability profile**, per the finding above — the clearest, best-evidenced priority the whole exercise produced.
2. **A quieter structural risk surfaced along the way**: the model that judges a test's critique and the model that adjudicates what happens after an implementation attempt are *currently the same model*. The framework goes out of its way to keep the test-writer and the test-reviewer independent — different models, on purpose — but that same independence isn't yet enforced between judge and adjudicator, which matters more than it sounds once you're relying on one of them as a check on the other.
3. A handful of smaller, cheap-to-fix gaps: a planning step whose stated reasoning sometimes doesn't match what it actually declares in its structured output (mechanically detectable — does every function it talks about actually show up in the output field?); missing guidance for the "stall" case above; and a few single-instance findings each with a specific proposed fix already written down.

## Where it's headed

The frontier above is about making the current thing more reliable. The roadmap is about making it do more: everything Ratchet can do today assumes a *fresh* design doc describing something built from nothing. The next real capability is teaching it to work on top of existing code — extend a finished project's functionality, diagnose and fix a reported defect with no spec at all, or reuse one project's code as a dependency for another.

The interesting constraint on that work is a deliberate one: rather than chase every capability a general coding agent has, the roadmap is explicit about which three things actually matter for this approach, and insists any new capability preserve all three: unconditional mechanical gates that never take a model's word for success, decomposition into independently-verifiable units rather than one long agentic session, and a live database that makes the pipeline itself reproducibly testable. Where a general agent loop genuinely is the better tool for a sub-problem — open-ended exploration of an unfamiliar codebase, say — the plan is to use one, but only as a bounded sub-call whose output still gets checked at the seam, never trusted outright.

## A tour of what it's built

The proof of any of this is in what actually got made. A sample, all generated end-to-end from a design doc with no hand-written code:

- A **fractal visualizer** rendering Mandelbrot, Julia, and Sierpinski sets through a web front end.
- An **L-system generator** — the recursive rewriting systems behind procedurally generated plants and curves.
- A **cron-expression studio** and a **glob-pattern studio** — small interactive tools for building intuition about two notoriously fiddly bits of syntax.
- A trio of **board games** — Connect Four, tic-tac-toe, and a Go board (goban) — the original bug-finding dogfoods, all eventually reaching a fully working, fully tested state.
- A **Kafka simulator** with a live visualization of partitions, key-hash routing, and consumer offsets.
- A **toy expression-language VM** with a web front end, the single most-iterated project in the framework's history and the source of a large fraction of the bugs described above.

None of these are large systems, and that's rather the point: they're complex enough to genuinely stress a pipeline built on small models, small enough that a burn-in can run through nine of them twice over in a few days of unattended compute. Each one that reaches "complete" is a small, concrete answer to the question the whole project is really asking: how far can process and discipline carry you when the model itself isn't the strongest one available?

---

*Next up: [Verbs and the Fleet](https://claude.ai/code/artifact/79e0d9bd-aa5b-4b84-ac34-690c4e1d9f31), a deep dive into the architecture, the verb pipeline, and the model-fleet bakeoffs in full detail; [Format, Think, Turn](https://claude.ai/code/artifact/059f0cd4-6cb7-4de1-ac32-6dd5413add1a), on what it actually takes to talk to an open model over Ollama; [Write for Zero Domain Knowledge](https://claude.ai/code/artifact/3638f7a6-2456-4d16-b69c-852b0756ad93), on the design-doc craft and the precision tooling built around it; [Draft, Check, Sign Off](https://claude.ai/code/artifact/f8159d21-e0b1-4529-8aab-d79de63f597b), on the workflow that turns rough prose into a doc the pipeline can trust; and [The Burn-In](https://claude.ai/code/artifact/46056184-99b7-4196-a475-170ad8d0f586), a full account of the findings.*
