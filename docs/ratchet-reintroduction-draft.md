# Ratchet: a directed, converging agent loop for small local models

*The pattern, and its Go application at v0.4.*

*Draft for review. Bracketed items are open. Planned work is out of scope. Factual statements are traced to source in the companion ledger.*

Ratchet names two things in this paper. The pattern is a directed, converging agent loop for small local models. The application is a Go program that implements the pattern for one task, turning a design document into tested Go code. The version number belongs to the application. This paper describes the pattern, then the application as tagged at v0.4 (commit `d92f807`), what building it around small models taught, and what a 23-run validation of it found.

The code is at <https://github.com/lehmann314159/ratchet>. Other, unrelated projects share the name.

## Glossary

- **Ratchet (the pattern).** A directed, converging agent loop: a framework-fixed sequence of narrow steps, mechanical checks where a check can be written, independent review where it cannot, and success decided by an external check. Defined in section 2.
- **Ratchet (the application).** The Go program that implements the pattern for one task. It is what carries the version number, and every measurement in this paper is of it.
- **Design document.** The plain-English specification of a program. It is the only input a person writes.
- **Bead.** A scoped unit of work with a spec, a fixed list of output files, and exit criteria.
- **Exit criteria.** Shell commands that must pass for a bead to count as done, for example `go test -v . -run=TestApplyMove`.
- **Verb.** One model call with one job and a defined output.
- **Fleet.** The set of local models assigned to verbs.
- **Pin.** A bullet in the design document that names a bead and lists literal values that must appear in its spec.
- **Escalation.** A job or project stops and waits for a person.

## 1. Why go lower

At the frontier, everything is large. The models handle ambiguity well, and a coding agent supplies the handshake with the model, manages context, and keeps state between sessions. It is also expensive. Going lower takes three forms: more modest frontier models, mid-tier models, and models that run on consumer hardware. Each is less capable than the frontier, to different degrees. The application works at the low end of that range. At run time it makes no cloud calls. The reference deployment is one machine with 119 GiB of unified memory serving models of roughly 24 to 35 billion parameters through Ollama.

**Premise and scope.** The premise of this paper is that frontier models can do this task with much less structure than Ratchet supplies. The author has seen them do it. That is an observation from his own use, not a result, and the paper does not test it. It makes no comparison with a frontier model and no claim about cost or quality relative to one. What it describes is what it took to make smaller models reliable enough to finish projects.

Two earlier projects frame the design. Geoffrey Huntley came up with the Ralph Wiggum loop and named it in mid-2025. Its core is to run an agent in a loop against a check that cannot lie, such as a test, a linter, or a type checker, until the check passes. Each iteration starts a fresh agent that reads the current plan and code from files, does one task, commits, and exits. Steve Yegge released Gas Town on January 1, 2026, as an orchestrator that coordinates many coding agents at once. It is written in Go and gives agents roles such as Mayor and Polecats. Its work units, called beads, are tracked in git so that work survives agent restarts.

Both address the same three questions: how far a person can step back, how much structure the process needs, and how the process knows it has succeeded. The Ralph loop keeps the structure minimal and puts success in an external check. Gas Town puts structure into roles and persistent work items across many agents. The Ratchet pattern puts it into a fixed sequence of narrow steps chosen by the framework, with success decided by commands that must pass on disk.

## 2. What Ratchet is, and what it is not

**The pattern.** As a pattern, Ratchet has five properties:

- A framework, not a model, fixes the sequence of narrow steps. A model's output can only select among branches the framework defines.
- Each step is done by a model chosen for that step.
- Where a check can be written mechanically, a mechanical check replaces a model's judgment. Where it cannot, a second model, different from the first, reviews.
- Success is decided by an external check, never by the model's own claim of completion.
- When a cap is reached, the process stops and asks a person.

The pattern says nothing about programming language, database, or model server.

**Mechanical checks and inference.** The application separates two kinds of work. A mechanical check is code that reads artifacts and returns a fact: a file exists, a package compiles, a symbol is declared, an exit criterion passes on disk. It has no opinion and gives the same answer every time. An inference is a model's reading of something: a review finding, a judgment that a test is sound, an interpretation of why an attempt failed. It can be wrong and can differ between runs.

The application keeps them apart in three ways. Facts and interpretation are recorded in separate fields. Inferences that steer the pipeline pass through mechanical gates before they take effect. And a mechanical check is used in place of a model wherever one can be written. The boundary is not always clean. The word match that flags "standard movement rules" is deterministic, but what it finds is a suspicion for a person to look at.

**The application.** The application is the pattern realized for one task. It turns a design document into tested Go code. It breaks the document into beads. The term is from Steve Yegge's Beads, a git-backed issue tracker that gives coding agents persistent memory across sessions, whose name reflects issues chained together by dependencies like beads on a string. In the application a bead is a row in a database, not a file in git.

Each bead goes through a fixed chain of verbs that writes tests first and then code. Fourteen verbs have model assignments, and several checks make no model call. Several of the steps that write have a reviewer on a different model: the survey, the decomposition, the tests, and the implementation. In the test-writing loop a judge approves a test or sends it back. One bead's results feed the next. When a cap is reached, the pipeline stops and asks a person. State lives in a SQLite database.

The application is not a general coding agent. It builds from a design document, and Go is the only language it currently supports. The pattern is tied to neither. Nothing starts from a change request or a bug report. Changing a finished project means editing its document and re-running the beads whose specs changed. It does not run unattended indefinitely, since stopping for a person is part of the design. It is not a measured substitute for a frontier agent.

## 3. The case against

Is Ratchet a terrible idea? Four objections are each paired with an answer. The objections are to the approach, and the evidence comes from the application. The record shows how far each one holds.

1. **Objection: judgment is the point, and bigger models have better judgment. Answer: mechanical checks, where possible, can replace judgment.**
   The paper does not dispute the second half. The answer concerns what to do when a larger model is not the choice: move judgment into checks where a check can be written. Exit criteria checked on disk, the manifest checks, and the gates on adjudication decisions do not depend on a model. Where no mechanical check exists, the pipeline still relies on model review. In the one comparison of defect-finding in tests, the incumbent caught 4 of 6 known defects and the other two candidates caught none. Only local models were tried.
2. **Objection: coordinating several small models is fiddly. Answer: building it yourself improves knowledge.**
   The fiddliness is documented in section 6. Five model families needed different handling of tool calls and output constraints, and only two or three models can be resident at once. That documented knowledge is what building it produced.
3. **Objection: time is sometimes more important than money. Answer: that wisdom pays dividends when working with more expensive systems.**
   A four-bead project took about three hours. The 23 burn-in runs took about 74 hours of wall-clock time, one project at a time. The transfer claim has not been tested against a frontier system. The likeliest candidates to carry over are the design-document discipline in section 5 and the habit of checking a claimed success against the disk.
4. **Objection: harnesses and coding agents are improving at a furious pace. Answer: if an organization's AI knowledge is T-shaped, that distributed expertise lifts everyone.**
   This paper makes no forecast and compares against no current agent. The second half is a claim about organizations, not about the system, and it is not measured here.

As section 1 says, this paper makes no comparison with a frontier model. The first answer has the most direct support in the record. The other three are judgments about learning and organizations.

## 4. The application's machinery, by phase

Each verb's result is stored as a row in a SQLite database, and the next verb is queued when the previous one commits.

**Bootstrap** runs once per project. `SURVEY_SPEC` reads the design document and returns a manifest of the files and declarations (types, function signatures) it calls for. It runs under a JSON schema and has no tools. `VERIFY_MANIFEST` is the one verb that makes no model call. It writes the scaffold, stub files generated from the manifest, and runs six mechanical checks:

- Every listed file exists.
- No behavioral test files were created.
- The package compiles.
- The API-check file carries compile-time assertions for the exported symbols.
- Stub bodies contain no control flow.
- No package-level identifier is given conflicting types across files.

`CERTIFY_MANIFEST`, run on a different model from the survey, approves or rejects. A rejection returns to `SURVEY_SPEC` with feedback, and a fifth rejection stops the project.

`DECOMPOSE_SPEC` turns the document and manifest into bead specs, and the bead count is not fixed in advance. Before anything proceeds, a mechanical check rejects a decomposition that references a file before the bead that creates it, or that merges or drops a bead the document's own numbered list calls for. `DECOMPOSE_SPEC` gets three attempts. `AUDIT_DECOMPOSITION`, on a different model, reviews the result. If it finds problems, `RECONCILE_DECOMPOSITION` revises the individual specs, and the two loop for at most two rounds before the project waits for a person.

**Tests.** A bead whose output files include a Go test file starts in a test-first loop. `REFINE_TESTS_WRITE` drafts the test with two tools. One writes a test function, which the framework splices into the file. The other compiles and runs a short Go snippet, so a claim about the language or standard library can be checked instead of reasoned about. `REFINE_TESTS_CRITIQUE`, on a different model from the writer, reviews the file. `REFINE_TESTS_JUDGE` approves it or sends it back, for at most five cycles.

**Implementation.** `EXECUTE_BEAD` runs a coding agent with three tools (`write_file`, `read_file`, `run_command`) in a temporary per-attempt workspace. It may write only the files the bead lists, and only those are copied back when the attempt ends. An attempt counts as a success only if the exit criteria pass when checked on disk. The model's statement that it is finished does not count.

Two mechanisms end a stalled attempt. A progress tracker inside the loop counts a turn as productive only if a write leaves an in-scope file at content it has not held before in that attempt. It ends the attempt on a streak of empty turns, three identical tool calls in a row, or no forward progress at a 12-minute checkpoint. A hard ceiling is set at 45 minutes. Separately, `MONITOR_EXECUTION` is a watchdog subprocess that polls the trace file about every 30 seconds. It fires on mechanical checks first, either a trace that stops growing for about 12 minutes mid-write or a repeated-action pattern. A small model's FIRE or NO_FIRE call comes after those. It can terminate the running attempt.

After an attempt, `ANALYZE_EXECUTION` gathers mechanical findings (parsed trace, compile and test output, undeclared or stray files). A separate model call adds an interpretation, kept in its own field. `COMPRESS_ANALYSIS` condenses the attempt history and tags each failure class new, recurring, or resolved. `ADJUDICATE_NEXT_EXECUTION` then decides: declare success, retry as is, retry with a revised spec, reject the tests, send the bead back to the test loop, or stop. Its decisions are gated. A declared success re-runs the exit criteria against a copy of the files. A revised spec is checked for invented symbols another bead owns, and if it fails, the decision falls back to retrying the unrevised spec. When a bead succeeds, `REVISE_PENDING` adjusts the next pending bead's spec against the code now on disk.

Five model-independence rules are enforced in code when a fleet is assigned. The same model must run `DECOMPOSE_SPEC` and `RECONCILE_DECOMPOSITION`. `AUDIT_DECOMPOSITION` must differ from `DECOMPOSE_SPEC`, `EXECUTE_BEAD` from `ANALYZE_EXECUTION`, `CERTIFY_MANIFEST` from `SURVEY_SPEC`, and `REFINE_TESTS_WRITE` from `REFINE_TESTS_CRITIQUE`.

**Language.** The pattern does not depend on a language. The application does, and the two kinds of work adapt differently. On the inference side, the application detects a project's language and appends language-specific guidance to a verb's system prompt. Detection looks first for marker files such as `go.mod` or `Cargo.toml` and, before any file exists, falls back to the extensions of the files the beads will write. Guidance is one Markdown file per language and verb, loaded at run time, so it can be edited without a rebuild. On the mechanical side, adapting takes code. Scaffolding, the bead-spec checks, the automatic spec repairs, the compile check, the stub scan, test-function splicing, and the snippet runner are written for Go, and scaffolding rejects any other language. Detection recognizes six languages, but Go is the only language currently supported, and guidance exists for five Go verbs.

Other languages can be added as the project expands. On the inference side that takes a guidance file per verb. On the mechanical side it takes new code for each of the pieces above.

Every path that stops a job or project for human review is documented in a twelve-row table in `docs/ratchet_state_machine.md`. At a terminal state, a per-bead report is written summarizing every attempt, spec revision, and decision.

## 5. The design document as the interface

The design document is the only input a person writes. `SURVEY_SPEC`, `DECOMPOSE_SPEC`, `AUDIT_DECOMPOSITION`, and `RECONCILE_DECOMPOSITION` read it whole. The verbs that test and implement a bead do not. They read the bead spec, which `DECOMPOSE_SPEC` wrote as a summary of the document, plus a bead-scoped excerpt of the document itself. That structure explains most of what the document has to do.

It has seven sections. Three are always present and four are conditional.

1. **Overview** states what the project does, its runtime model, what is out of scope, and every parameter a model could guess, such as board size or limits.
2. **Architecture** (conditional) says which file owns which function, including "do NOT put X in Y" lines. The project is a single flat package, and `CERTIFY_MANIFEST` rejects any source file in a subdirectory.
3. **Data Types and Function Signatures** covers every type, constant, function, and package variable any bead touches. It includes verbatim `var _` assertion lines for exported symbols, which are checked against a generated file.
4. **Behavioral Specification** says what each function does, not how, and which groups can be tested independently.
5. **Domain-Specific Test Scenarios** (conditional) gives exact positions or values with the arithmetic shown, and names the wrong answer.
6. **Cross-Bead Contracts** (conditional) records each interface one bead produces and another consumes, verbatim, covering every return variant.
7. **Decomposition Notes** (conditional) holds a numbered bead list and pin bullets.

Three mechanisms tie the document to the pipeline in code.

- **The bead list is enforced.** `DECOMPOSE_SPEC` must emit one bead per numbered entry and cannot merge or drop one. It may add a bead.
- **Pins are re-injected.** Each pin bullet names a bead and lists literal values that must appear in that bead's spec. The pin text is re-appended verbatim whenever the spec is written or rewritten: at decomposition, at reconciliation, on a revised retry, and on a rewind. Prose elsewhere in the document does not survive this reliably. `DECOMPOSE_SPEC` can keep a rule and drop the worked values that prove it. Pins are matched by bead title, so a revision that renames a bead loses them. That happened once in the burn-in, as part of one stopped run.
- **The excerpt is capped.** The excerpt given to the test-writing, critique, judge, and adjudication verbs is limited to about 30 KB. That is sized to the smallest context window in the fleet, 40,960 tokens. Sections are added in priority order: test scenarios, decomposition notes, behavioral rules that name the bead's own symbols, then signatures. A section that does not fit is dropped whole and never cut mid-rule. The excerpt opens with a statement that it outranks the bead spec where the two disagree.

The author is fluent in the domain and the implementer, a 24 to 35 billion parameter model, is not. Rereading the document does not close that gap, because the author sees a correct description. The design guide records concrete cases:

- A breadth-first flood fill described only by name, where the natural wrong implementation adds the starting cell twice.
- An unexported helper used in a worked example but never declared, so the scaffold had no stub for it and every test-writing attempt failed the same way.
- A form value containing `+` that a handler decodes as a space.
- A rule stated precisely and then restated with a relative gloss ("up the board"), which a later revision echoed back incorrectly.

An ambiguity checklist of 18 classes records patterns like these, each added after an incident. Five are mechanically detectable:

- Relative-direction language with no coordinate example (class 1).
- A formula with no computed number (class 2).
- A reference to a value defined elsewhere instead of the literal (class 6).
- A `main.go` spec without a literal `package main` (class 7).
- A category phrase standing in for its rules, such as "standard movement rules" (class 17).

The class 17 check matches words like standard, normal, usual, conventional, or typical next to a rules or behavior noun. The reasoning is that a reviewer with the author's domain fluency reads such a phrase as complete. In the one recorded experiment, a fresh reviewer read the whole chess document and raised six other issues but not the line "Knight, Bishop, Rook, Queen, King: standard movement rules." A second reviewer read the rewrite, with every movement rule spelled out, and raised nothing on it. That is a single pair of reviews, and it tested reviewers, not the models that implement beads.

Checking has three parts. `checkdesigndoc` produces a mechanical report covering pins against worked examples, the five ambiguity classes, construction form, and bead size. Every result is a site to look at, not a pass or fail. A fresh subagent then reviews the document with only the checklist and deliberately not the mechanical report, so it forms its own findings. The two are reconciled, with every class 17 hit carried forward whatever the reviewer said. A person then decides each item, and a waiver is recorded in the document as a comment.

Drafting is a separate step. It requires every worked example to be verified by running a script in the same session, with the command and result cited in a comment. It also requires gaps to be listed as open questions instead of filled with a plausible value. This is the pipeline's own decompose, audit, and reconcile shape applied one layer earlier. It is design-time work done with Claude, and everything after the document is accepted runs on the local fleet.

The bead-size check flags a bead that owns four or more functions and either depends on three or more earlier beads or appears in three or more contracts. Those thresholds come from one project. The lsystem `grammar` bead stopped repeated from-scratch runs until it was split into three.

Checked documents still failed. The four documents written for the burn-in were reviewed before any run. The reviewers found 20 issues, about 15 of them imprecise prose and 4 of them bugs shared with the reference implementations the pinned values had been computed from. Three of the four reviewers independently raised the same new class, 18, the order in which simultaneously failing input checks are reported. Even so, the glob-studio document contained two contradictory statements about empty character classes. It survived a six-finding check pass and stopped a run until the prose was corrected. The tasklist document was left unchecked as a regression anchor. It said a completed task shows "a done indicator (e.g. class `done` or a checked symbol)". That is class 10, and one tasklist run spent five test-writing cycles on what the phrase meant.

## 6. What small models taught us

Many of the early failures were in the interface between the framework and the model, and few were about coding ability. Four kinds recurred.

**The tool-call handshake differs by model.** A tool call is supposed to arrive in a structured field of the response. `qwen2.5-coder:32b-instruct` writes well-formed calls as bare JSON in the message text, so Ollama reports no calls and the loop saw an empty turn. `qwen3:32b`'s template requires the literal text `<tool_call>...</tool_call>` in the message. Ollama can apply a grammar that forces every generated token to form valid JSON, and the framework applied it to every call. That grammar forbids the `<` character. Across 139 captured CRITIQUE calls in four corpora, the model never emitted a tool call once. After the grammar was dropped on tool-calling turns, the next full run showed 7 tool calls in 28 turns and no length-cap spirals. `nemotron-cascade-2:30b-a3b` reports tool support in Ollama's metadata and passed a trivial probe. On a real task, Ollama's own parser then threw an XML error on a large file argument. A model's advertised capability is not evidence. Only a real multi-turn exchange with a realistic payload is.

**Structured output and reasoning interfere.** Reasoning models produce a thinking phase before answering. Under the JSON grammar, `glm-4.7-flash` on `SURVEY_SPEC` generated about 70,000 tokens until a 60-minute client timeout. `muse-glimmer` on `DECOMPOSE_SPEC` returned a 63 KB response with an empty `beads` array, twice, about 17 minutes each. For verbs whose output is a structured result, the fix was a JSON schema whose first property is a free-text `reasoning` field, so the model thinks inside the constraint. The same muse call then took one attempt, about 2 minutes, and returned nine beads. The schema had its own runaway. `qwen3.6:35b-a3b` produced 76 KB of repeating text and broke its own JSON, so the reasoning field is capped at 16,000 characters, which Ollama enforces. The reverse also occurred. On a verb whose output is tool calls, the schema let a model write a plan and claim completion without calling the tool. Those verbs run with no format constraint.

**A larger budget or a larger model did not fix a scope problem.** One parser bead in the L-system project repeatedly stopped from-scratch runs. A bakeoff ran the real `EXECUTE_BEAD` loop, with all watchdogs, on that bead: 31 runs across seven models. Runs were scored by how many of 35 test checkpoints passed before the first failure. None passed. The best run reached 32. The 80-billion-parameter coder model matched the 30-billion one at a median of 26. Raising the turn limit from 50 to 120 lowered the best coder's three results from 12, 32, and 26 to 19, 19, and 0. One run spent 122 turns and 21 writes and broke a type shared with other beads. The two reasoning models never committed code. The fix was in the design document. The bead was split into three, each ran in one attempt, and the project completed 13 of 13 beads. The same bakeoff exposed a framework bug: an attempt that stopped calling tools after a write was recorded as a success without checking the exit criteria, which happened in 2 of 11 runs by models that wrote code. This is one bead with three runs per cell.

**Reports from the model and from the counters are unreliable.** In a test-writing comparison across six beads, one run each, `muse-glimmer` produced 6 correct tests. The incumbent `gemma4:31b` produced 3, and on two beads it hit the 30-minute ceiling with nothing written. `qwen3.6:35b-a3b` produced 3. Its two failures came back as valid results with a generic summary, and only the missing file showed that nothing was written. Ollama's token counters count only answer tokens. In captured CRITIQUE calls the answer took about 14 seconds per turn and thinking took 40 to 230 seconds, visible only in total duration.

Only `muse-glimmer` runs at 8-bit precision. The rest of the fleet runs the Ollama library default, 4-bit. No comparison of 4-bit and 8-bit versions of the same model has been run. A blanket 8-bit sweep was rejected on reasoning, because it costs roughly 1.7 to 1.9 times the memory per model by the project's estimate and the blockers found so far were structural.

These findings came from `qualify-model`, which replays a captured verb call against a candidate model. It runs the verb's real code up to the commit step, without committing, and first checks that the rebuilt prompt is byte-identical to the captured one. Grading is per verb. `REFINE_TESTS_WRITE` is graded on whether the test compiles, passes the known-good implementation, and fails on a planted bug. `REFINE_TESTS_CRITIQUE` is graded on catch rate against known-bad tests and false positives against known-good ones. The judge and adjudication verbs are graded on agreement with the historical decision and on the rate of turns that end with no content and no tool call. Samples are small: critique used 6 runs on known-bad tests and 4 on known-good, and adjudication used six captured cases, all easy. Labels come from decisions the pipeline itself made, so the grades measure agreement with it.

Each incident maps to a mechanism in the framework. A tool-loop turn is capped at 8,192 tokens, with a two-stage recovery when a turn ends without content. A stalled attempt is decided by exit criteria on disk, and a timeout is treated as a signal to narrow the scope. Context size is set explicitly at 40,960 tokens, `qwen3:32b`'s native maximum and the smallest window in the fleet, which fixes the excerpt budget in section 5. Every model call can be captured for replay. Framework bugs are investigated apart from project runs, and each document is run at least twice.

## 7. What the v0.4 application has been through

v0.4 is the commit (`d92f807`) at which the application was frozen before a long validation run. The run was meant to show behavior across many varied inputs. A single reproduction per bug undercounts real defects and also undercounts the false-positive rate of the gates added to catch them. The rules were set in advance. The model fleet and quantizations did not change, no bug was fixed while the run was in progress, and every finding was logged and the run continued. The freeze would break only for a defect that blocked a majority of runs or made the results unanalysable.

The corpus was nine design documents. Four had been run before (fractalviz, lsystem-studio, cron-studio, tasklist). One, glob-studio, had a known document defect that had been fixed. Four were written for this run to break the mix of parsers and web apps in earlier tests (decimal, toml-mini, retry-engine, gapbuffer). Each document ran twice, with a third run when the first two disagreed. That made 23 project runs over about 74 hours of wall-clock time, one at a time. The projects ranged from 4 to 13 beads.

13 of the 23 runs reached complete and 10 were stopped for human review. Completed runs by document: decimal 2 of 3, toml-mini 2 of 3, retry-engine 2 of 3, gapbuffer 1 of 2, glob-studio 1 of 3, fractalviz 1 of 3, lsystem-studio 1 of 2, cron-studio 1 of 2, tasklist 2 of 2. One of the completed runs, glob-studio's second, was interrupted at 9 of 12 beads and continued from a saved fixture against a corrected document. It counts once, as a completed run with a mid-run document fix.

Most of the stops came from defects in the pipeline or in a model's behavior. The others were a bead the fleet found hard, a contradiction in a design document, and a bug in generated code that the pipeline caught. No two stops shared a cause. That can mean the recovery machinery is catching a different failure each time, or that the failure surface is wide enough for ten runs to barely sample it. This corpus cannot tell the two apart.

Compute was measured as job-active time, 82.1 hours summed. That is an upper bound, because retries overlap. The three test-writing steps (WRITE, CRITIQUE, JUDGE) took 53.0% of it. CRITIQUE alone took 32.4%, more than EXECUTE_BEAD at 20.2%. The bootstrap verbs took 15.9%. Of 151 EXECUTE_BEAD attempts, 118 succeeded and 33 stalled.

By the pre-set threshold there were no showstoppers. That threshold is high, and 10 of the 23 runs stopped, so the two should be read together.

What the run does not show:

- By design, there is no comparison with a frontier model on the same documents.
- CRITIQUE returned no findings on 78% of parseable captures, but no ground truth exists to say whether those were correct.
- CRITIQUE and JUDGE agreed on about half of CRITIQUE's findings. The log found cases where each was wrong, so agreement is not correctness.
- The documents were small and were written and checked with the framework's own design-doc tooling.
- The deployed binary was built from the tagged commit with a modified working tree, and what was modified is not recorded in the material reviewed here.

## Two musings

Two questions occurred to the author while building the application. Neither is tested here, and neither is a claim of this paper.

The first is whether models lie along a spectrum from frontier to open, with the amount of structure a model needs to finish the same work increasing along it. The application sits at the high-structure end, and everything measured here comes from that end.

The second is whether a structured loop of this kind prevents or softens a class of errors found in AI-generated code, whatever the model's size.

## References

- Ratchet application (Go, tagged v0.4): <https://github.com/lehmann314159/ratchet>
- Ralph Wiggum loop: <https://www.dreamhost.com/blog/ralph-wiggum/>, <https://www.i-scoop.eu/ralph-wiggum-prompting/>, <https://www.codecentric.de/en/knowledge-hub/blog/the-ralph-wiggum-loop-autonomous-code-generation-with-a-fresh-context>
- Gas Town: <https://heise.de/-11178824>, <https://pkg.go.dev/github.com/steveyegge/gastown>
- Beads: <https://www.morphllm.com/beads-agent-memory>, <https://github.com/steveyegge/beads>
