# Full application audit — checklist

Started 2026-07-14. Agreed with the user after a single session (checkers-v7/v8) found
five real bugs in a row via spot-checking rather than trusting job-complete status —
see `[[project_ratchet]]` memory, 2026-07-14 points 7-13, for the incident that
motivated this. This file exists because a long series of incremental, reactive
one-bug-at-a-time fixes has not been matched by any systematic top-down review, and
that gap is structural: a session chasing one bug stops once that bug is fixed, not
once the surrounding code is actually verified correct.

**This is a multi-session effort.** Check items off as they're genuinely verified —
not as they're read. Leave a one-line note next to anything skipped, deferred, or
found-but-not-fixed, so the next session doesn't have to re-derive context.

## Method (apply to every stage, not just once)

A "deep" audit of a stage means more than reading the code and it looking plausible —
that's exactly the level of scrutiny that missed the `declare_success` trust gap and
the test-clobbering bug, both of which "looked fine" until actually exercised. Per
stage:

1. Read every `Run`/`Validate`/`Commit` (or equivalent) in the stage's files in full —
   not just the parts a grep for "TODO" or recent git blame would surface.
2. For every mechanical check or gate: construct a concrete model output that would
   defeat it while looking superficially correct. (This is exactly how the `var (...)`
   block-style assertion and the `grep ... && echo Pass || echo Fail` always-exit-0
   pattern were found — ask "what's the dumbest/most literal way a small local model
   could satisfy the letter of this check while missing the point?")
3. For every place a model's own narrative/interpretation feeds a consequential
   decision (declare_success, converged, approved, etc.): is there mechanical ground
   truth available that isn't being consulted?
4. For every piece of state written by more than one call site (shared files, shared
   DB rows/counters, anything keyed by project rather than by bead/job): can a later
   writer silently destroy an earlier writer's guarantee? (This is the shape of both
   the round-cap bug and the test-clobbering bug — a counter or a file two different
   code paths both touch, only one of which is aware of the other.)
5. Cross-check against real artifacts from actual runs (trace logs, DB rows, on-disk
   project folders) rather than reasoning about the code in the abstract wherever a
   live or recent project provides one.
6. Read the stage's existing unit tests. Do they cover the failure mode actually found
   in production use, or only the happy path / a mocked-up version of the bug?

Findings go through the same discipline as the rest of this project: verify before
confirming ([[feedback_verify_before_confirming]]), propose before applying a fix
([[feedback_propose_before_apply]]), no DB patches to route around a bug
([[feedback_no_db_patches]]).

---

## Stage 1 — Bootstrap: SURVEY_SPEC / VERIFY_MANIFEST / CERTIFY_MANIFEST — AUDITED 2026-07-14

Design doc → file manifest → stub files. First three verbs in the FSM, run once per
project before any bead exists.

- [x] `internal/verbs/survey_spec.go` — read in full (Run/Validate/Commit). No bugs
      found. `Validate` correctly rejects empty module/package/files and any file
      missing path or declarations.
- [x] `internal/verbs/verify_manifest.go` — read in full. **CONFIRMED BUG (not yet
      fixed): the stub-purity AST check was never implemented.** `StubPurityPass` is
      hardcoded `out.StubPurityPass = true` (line 91) with a comment claiming it's
      "guaranteed by mechanical scaffolding" — it isn't. `buildGoFile` writes the
      model's raw `declarations` text to disk verbatim (only prepends package+
      imports); nothing inspects function bodies for control flow. `git log -p`
      confirms this was hardcoded `true` in the original commit that introduced the
      pipeline (`db1d3df`) — never a regression, never built. The originally-designed
      check (see `[[project_ratchet]]`: "blacklist ast.IfStmt/ForStmt/RangeStmt/
      SwitchStmt/TypeSwitchStmt/SelectStmt in any function body") and the loaded
      runtime guidance the SURVEY model actually receives
      (`ratchet-projects/guidance/go-survey.md`: "No if/for/range/switch/select... No
      function calls of any kind") both describe a rule that is prompt-only —
      zero mechanical enforcement exists. Verified against the live DB: **every
      `verify_attempts.stub_purity_pass` row ever written is `1`** (`SELECT DISTINCT
      stub_purity_pass FROM verify_attempts` → single row `1`). Cross-checked real
      SURVEY_SPEC output for the three real completed/active projects (chess-v3/89,
      goban-v2/91, checkers-v8/98) for control-flow tokens in `declarations` — none
      found, so this hasn't caused an observed production failure yet, but there is
      currently no defense if a model ever does over-implement at this stage — which
      is exactly the failure mode ("layout bead over-implementation") this whole
      pipeline was built to eliminate. **FIXED 2026-07-14**: added `checkStubPurity`
      (AST-walks each manifest `.go` file's `FuncDecl` bodies via `ast.Inspect`,
      blacklisting `IfStmt`/`ForStmt`/`RangeStmt`/`SwitchStmt`/`TypeSwitchStmt`/
      `SelectStmt` — matches the originally-designed conservative scope; deliberately
      did *not* also blacklist function calls, since Go call-expression syntax also
      covers legitimate zero-value type conversions and that would need its own
      false-positive analysis). Wired into `VerifyManifest.Run` as check 5 and into
      `CertifyManifest.Run`'s preliminary reject condition; `certifyManifestSystemPrompt`
      now lists stub_purity as check 5 too. Tests added:
      `internal/verbs/verify_manifest_test.go` (pure-stub pass; catches a real `if`-
      based implementation — the literal defeat scenario found; one test per banned
      statement kind; api-check file correctly excluded).
- [x] `internal/verbs/certify_manifest.go` — read in full. Preliminary/model decision
      logic is correct (reject on any of the 4 real mechanical failures; model layer
      reviews structural quality on top). **Confirms the above finding is a two-layer
      gap, not just mechanical**: `certifyManifestSystemPrompt`'s own "Checks
      performed" list only names checks 1-4 (file_presence, no_behavioral_tests,
      compile, api_check) — stub purity isn't mentioned at all, so the CERTIFY model
      isn't even prompted to sanity-check implementation content as part of its
      "structural quality" review.
- [x] `internal/verbs/scaffold_go.go` — read in full. **CONFIRMED and root-caused**
      (memory only had the symptom from the project-96/bead-617 incident): 
      `WriteScaffoldStubs` iterates `manifest.Files` filtering by membership in the
      caller's `needed` set — any `needed` output-file path that is *not* in
      `manifest.Files` (e.g. a non-`.go` file such as `templates/index.html`, or any
      file a bead created that SURVEY never scaffolded) is simply never visited: no
      write, no error, no log. Compounding this, `internal/project/rewind.go`
      (Stage 6) computes its printed `stubbedFiles` list independently — by filtering
      `outputFiles` for non-`_test.go` suffix — rather than checking what
      `WriteScaffoldStubs` actually wrote, so `rewind-bead` prints "impl files
      stubbed: templates/index.html" for a file it silently never touched. Since
      `rewind.go` only `os.Remove`s `_test.go` files (non-test files are never
      deleted, only supposedly "stubbed"), the net effect is: after a rewind, any
      such file retains its exact pre-rewind content — possibly the broken content
      that triggered the rewind — while the CLI output falsely claims it was reset to
      a clean baseline. Root cause lives in Stage 1 (`scaffold_go.go`); user-visible
      failure and misreporting is in Stage 6 (`rewind.go`) — flagging here since this
      is where the fix belongs, cross-referenced from Stage 6's checklist entry.
      **FIXED 2026-07-14**: `WriteScaffoldStubs` now returns `(stubbed, deleted
      []string, err error)`. Manifest-backed files are still reset to their scaffold
      stub (unchanged behavior); any output file *not* in the manifest is now
      `os.Remove`'d instead of silently skipped — the same "no baseline exists, so
      delete" treatment `_test.go` files already got, extended to every file SURVEY
      never scaffolded. `rewind.go` now reports the function's actual return values
      instead of independently recomputing a "stubbed" list by filename filtering, so
      the CLI output can no longer claim success for a file it didn't touch. Verified
      the fix doesn't change the missing-file case used by `restoreMissingScaffolds`
      (deleting an already-absent file is a no-op, `os.IsNotExist` handled). Tests
      added: `internal/verbs/scaffold_go_test.go` (non-manifest file reported+actually
      deleted — reproduces the project-96/bead-617 scenario exactly; manifest file
      still correctly overwritten; missing non-manifest file is not an error).
- [x] Cross-check: downstream assumption confirmed. `decomposeSpecSystemPrompt`
      states as fact to the model: "Stub files are already on disk... Beads fill in
      the logic of existing stubs; they do not create new source files." This is
      exactly the guarantee the missing stub-purity check was supposed to provide
      mechanically — DECOMPOSE currently has no way to detect if SURVEY over-
      implemented, and would generate beads against a false premise if it ever did.
- [x] Test coverage check (Method item 6): was **zero** before this session — no
      `*_test.go` file existed for any of `survey_spec.go`, `verify_manifest.go`,
      `certify_manifest.go`, `scaffold_go.go`. Now covered for the two fixed bugs via
      `verify_manifest_test.go` and `scaffold_go_test.go` (see above); `survey_spec.go`
      and `certify_manifest.go` themselves still have no dedicated tests — no bug was
      found in either file's own logic, so none were added; revisit if this stage is
      touched again.

**Session log (2026-07-14):** Two real, confirmed bugs found. User chose "fix both
now" (see chat) — both fixed, tested (`go build ./...`, `go vet ./...`, `go test
./...` all clean), and left uncommitted in the working tree alongside the prior
session's four uncommitted fixes, pending user review/commit. No DB patches applied.
Verified by reproduction throughout: `git log -p` for the stub-purity hardcode's
origin, live-DB query across all `verify_attempts` rows, and a script cross-checking
three real projects' actual SURVEY_SPEC declarations for control-flow tokens.

## Stage 2 — Decomposition: DECOMPOSE_SPEC / AUDIT_DECOMPOSITION / RECONCILE_DECOMPOSITION

Design doc + survey doc → bead specs, with a model debate loop. Partially exercised
today (three real bugs found and fixed here: bead-ordering forward-references,
round-cap counter sharing, RECONCILE tie-break on verbatim-repeat findings) — but all
three were found reactively, chasing a specific incident, not via a systematic pass.
Treat as unaudited.

- [ ] `internal/verbs/decompose_spec.go` — DECOMPOSE_SPEC, including
      `forwardFileReferenceChecks`'s known scope limit (only catches *subdirectory*
      asset forward-references, not same-directory or non-literal dependencies —
      stated explicitly as a partial fix at the time)
- [ ] `internal/verbs/audit_decomposition.go` — AUDIT_DECOMPOSITION, including the
      recurring pattern observed today where AUDIT re-raises structurally-identical
      findings across rounds even when instructed not to (the tie-break fix handles
      the *symptom*; is there a cheaper prompt-level fix for the *cause*?)
- [ ] `internal/verbs/reconcile_decomposition.go` — RECONCILE_DECOMPOSITION,
      including the new `isRepeatDisagreement` tie-break logic itself (added today —
      audit your own new code with the same scrutiny as everything else)
- [ ] `internal/verbs/mechanical_checks.go` — every mechanical check in this file,
      not just `forwardFileReferenceChecks` and the new repeat-detection helpers
- [ ] `internal/verbs/inputs.go` — `latestAuditCritique`, `loadDebateHistory`, and
      every other shared-counter/shared-query helper this stage depends on (the
      round-cap bug lived here; are there siblings?)
- [ ] Cross-check: `round_number` collisions between mechanical `redecompose` rows and
      real debate rounds (both can now legitimately share round_number=1) — cosmetic
      today, confirm it stays cosmetic as more mechanical gates are potentially added

## Stage 3 — Test refinement: REFINE_TESTS_WRITE / REFINE_TESTS_CRITIQUE / REFINE_TESTS_JUDGE

Test-first mode's WRITE → CRITIQUE → JUDGE cycle. **Contains a known, confirmed,
NOT-YET-FIXED bug** — start here, this stage cannot be marked audited until it's
actually fixed, not just documented.

- [ ] **Fix the test-clobbering bug**: `internal/verbs/refine_tests.go:317-327`,
      `RefineTestsWrite.Run`'s `cid == 1` branch calls `splice.Assemble(pkg, funcs)`
      using only the current bead's own written functions, discarding `originalSrc`
      entirely — silently deletes any prior bead's functions already in a shared test
      file. The `cid > 1` branch (same-bead later cycles) does this correctly via
      `splice.Replace` against `originalSrc`; cycle 1 needs equivalent treatment.
      See `[[project_ratchet]]` 2026-07-14 point 12 for full detail and reproduction.
- [ ] Once fixed: add `internal/splice/splice_test.go` (does not exist yet) with a
      test reproducing the exact checkers-v8 case: bead A writes TestFoo to a fresh
      file, bead B's cycle-1 write must retain TestFoo alongside its own TestBar
- [ ] `internal/splice/splice.go` — audit `Assemble`, `Replace`, `FuncMap`,
      `detectImports` for other merge-vs-replace asymmetries beyond the one just found
- [ ] `internal/verbs/refine_tests.go` — CRITIQUE and JUDGE handlers, including the
      known cosmetic bug (`prompts.go:364`'s fill-in-the-blank summary template
      sometimes echoes both branches verbatim — noted in `[[project_checkers]]`,
      never fixed) and the "JUDGE has no memory of its own prior-cycle verdicts"
      class of bug (goban bead-568 incident, prompt-level fix already applied —
      confirm it's holding, hasn't regressed)
- [ ] Retroactive check (see Stage 9 below too): grep every past `COMPLETE` project's
      shared test files for evidence of the same clobbering pattern

## Stage 4 — Bead execution: EXECUTE_BEAD / MONITOR_EXECUTION

Subprocess-based; not a normal one-shot verb (`Run`/`Validate`/`Commit`) — special-cased
in the orchestrator. This is where the model actually writes code.

- [ ] `internal/execution/bead.go` — the ChatWithTools loop, tool implementations,
      budget/soft-stop handling
- [ ] `internal/execution/window.go` — `RunExecutionWindow`, infra-failure retry cap,
      orphaned-execution recovery on daemon restart
- [ ] `internal/execution/monitor.go` — the parallel watchdog subprocess: FIRE/NO_FIRE
      decision logic, the documented loop-pattern rules (repeated identical
      `run_command` output with no intervening write; missing-path error 2+ times).
      **Known gap observed today, not yet investigated**: a repeated identical
      self-check command (`grep ... && echo Pass || echo Fail`, always exit 0) did
      NOT trigger a MONITOR fire in checkers-v8 bead 627 attempt 2, even though the
      printed stdout was identical ("Fail") both times with no intervening write —
      confirm whether this is a real gap in the loop-pattern rule or working as
      intended for a different reason.
- [ ] `internal/execution/tools.go` — `toolRunCommand` and other tool implementations
- [ ] `internal/execution/prompts.go` — EXECUTE and MONITOR system prompts

## Stage 5 — Analysis & judgment: ANALYZE_EXECUTION / COMPRESS_ANALYSIS / ADJUDICATE_NEXT_EXECUTION

The largest and highest-stakes stage (`adjudicate_next_execution.go` alone is 1166
lines). One real bug found and fixed here today (`declare_success` trusting narrative
over mechanical ground truth) — audit the rest of this file with the same scrutiny,
not just the one code path that was already fixed.

- [ ] `internal/verbs/analyze_execution.go` — mechanical findings generation,
      `checkGoTestCompilation`/`checkGoTestOutput`/`checkUndeclaredFiles`/etc.
      **Known, not-yet-fixed display bug**: `internal/trace/findings.go`'s "All
      Commands Run" section suppresses stdout whenever `ExitCode == 0` — this is what
      let a `cmd && echo Pass || echo Fail` self-check (always exits 0) hide its own
      "Fail" output from the analyzer model. The `declare_success` gate makes this
      moot for that one decision path; it's still live for every other decision this
      stage's analyzer narrative feeds.
- [ ] `internal/verbs/compress_analysis.go` — history compression, NEW/RECURRING/
      [RESOLVED] tagging
- [ ] `internal/verbs/adjudicate_next_execution.go` — every decision branch
      (`execute_as_is`, `execute_revised`, `test_reject`, `re_refine`, `full_stop`,
      `declare_success`), every note-injection helper (`vacuousPassNote`,
      `orientationOnlyNote`, `partialProgressNote`, `stubImplNote`,
      `testFirstCompleteNote`, `missingPathNote`, `recurringTestFailureNote`).
      **Known, not-yet-fixed finding**: the `recurringTestFailureNote` fast path
      concluded "the test assertions are logically impossible" on a recurring
      failure in the checkers-v6 incident when the real cause was a Go template
      scoping bug — it never checks *why* a test keeps failing, so it can't
      distinguish "implementation produces wrong values" from "implementation
      throws a runtime/template error." General finding, not yet fixed.
      **New code from today, not yet independently audited**:
      `verifyExitCriteriaMechanically` and its wiring into the `declare_success`
      case — does the `atExecutionCap` interaction actually surface as an escalation
      correctly when a bead is out of attempts and its own exit criteria still fail?
      (Logic looks right, unit-tested in isolation, but never exercised end-to-end
      against a bead that actually exhausts its attempt cap this way.)
- [ ] Cross-check: does `regenerateAPICheckTest`'s self-healing interact badly with
      anything the `declare_success` gate now does earlier in the same `Commit` call?

## Stage 6 — Cross-bead handoff: REVISE_PENDING, rewind-bead, bead lifecycle

- [ ] `internal/verbs/revise_pending.go` — REVISE_PENDING, including the trimmed
      context (only trigger bead's non-test output files, not full project history —
      confirm this trim hasn't since caused a real miss)
- [ ] `internal/project/rewind.go` — `rewind-bead`'s reset-to-revision-1 behavior;
      confirm it still can't be extended to insert a corrected mid-flight revision
      (deliberately not built — confirm that decision still holds) and that the
      bead-revision lineage-bloat class of bug (goban ADJUDICATE `execute_revised`
      regenerating stale pre-rewind content, noted but not fully chased in
      `[[project_goban]]`) hasn't recurred
- [ ] `internal/project/restart.go`, `internal/project/fullstop.go`,
      `internal/project/resume.go` — the project-lifecycle CLI commands used heavily
      today (`restart-project`, `full-stop-project`)

## Stage 7 — Orchestrator: job queue, dispatch, recovery

- [ ] `internal/orchestrator/queue.go` — `claimNextJob`'s FIFO-on-`created_at`
      dispatch (confirmed today: NOT bead-ID-ordered — `rewind-bead` can make an
      out-of-order bead run ahead of a stuck one; confirm this is still intentional
      and doesn't cause a subtler problem than the one already found)
- [ ] `internal/orchestrator/dispatch.go` — generic verb dispatch, strike/tolerance
      handling, `EXECUTE_BEAD`'s special-casing
- [ ] `internal/orchestrator/orchestrator.go`, `internal/orchestrator/lock.go` —
      main poll loop, advisory locking, `recoverOrphanedExecutions` on startup

## Stage 8 — UI & CLI

- [ ] `internal/ui/handlers.go` — escalation detail, requeue, requeue-with-budget,
      grant-attempts (all used directly today via `curl` against the sanctioned
      endpoints — confirm they're exposed and documented as CLI equivalents too,
      not just reachable via a running `ratchet ui`/`ratchet start` process)
- [ ] `internal/ui/queries.go`, `internal/ui/server.go`
- [ ] `cmd/ratchet/main.go` — subcommand dispatch; confirm every UI-only recovery
      action (requeue, grant-attempts) has a CLI-reachable equivalent, or document
      that it deliberately doesn't

## Stage 9 — Shared infrastructure

- [ ] `internal/db/` — schema, migrations (the ad-hoc `migrateX` rename+recreate+copy+
      drop pattern used repeatedly — confirm no migration has ever silently dropped
      data on a live DB; the `audit_reconcile_rounds.outcome` CHECK migration this
      session was tested against a live DB copy first — confirm that's the norm, not
      an exception)
- [ ] `internal/splice/` — see Stage 3, this is where the fix actually lives
- [ ] `internal/trace/` — `GenerateMechanicalFindings`, the stdout-suppression bug
      (see Stage 5), parsing robustness against malformed/truncated trace files
- [ ] `internal/report/` — `WriteBead`/`WriteProject`, whether report content can
      drift from DB truth (e.g. would report a bead as succeeded after this session's
      `declare_success` bug, before the gate existed?)
- [ ] `internal/guidance/` — language detection, per-language prescriptive doc
      injection
- [ ] `internal/ollama/` — client timeout/retry/streaming behavior, `ExtractJSON`
      robustness against the range of malformed JSON this session's own investigation
      surfaced (markdown-fenced, truncated, etc.)

## Stage 10 — Retroactive check across past "COMPLETE" projects

Not a code-audit stage — a data-audit stage, only meaningful once Stage 3's fix
lands. For each project below, re-run its actual exit criteria for every succeeded
bead that shares an output file with a later bead, against the project's current
on-disk state, and record pass/fail:

- [ ] chess-v3 (project 89)
- [ ] goban-v2 (project 91)
- [ ] othello-v3-e (project 47) / othello-v3-f (project 48)
- [ ] tasklist-v1
- [ ] any other project marked `COMPLETE` in `sqlite3 ratchet.db "SELECT id,label FROM projects WHERE status='complete';"` not already listed here

---

## Log

- 2026-07-14: file created, staged, not yet started. Agreed with user: deep audit
  (verify by reproduction per the Method section above), staged by FSM verb
  boundaries. checkers-v8 (project 98) still running unattended in parallel;
  this audit is separate work, not blocked on it finishing.
