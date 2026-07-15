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

## Stage 2 — Decomposition: DECOMPOSE_SPEC / AUDIT_DECOMPOSITION / RECONCILE_DECOMPOSITION — AUDITED 2026-07-14/15

Design doc + survey doc → bead specs, with a model debate loop.

- [x] `internal/verbs/decompose_spec.go` — read in full (Run/Validate/Commit/
      commitRedecompose). `forwardFileReferenceChecks`'s documented scope limit
      (subdirectory paths only) confirmed still accurate, not re-litigated — it's an
      intentional recall/precision tradeoff, not a bug.
- [x] `internal/verbs/audit_decomposition.go` — read in full. No new bugs found in
      this file itself; the "AUDIT re-raises identical findings" pattern is fully
      handled downstream by `isRepeatDisagreement` (RECONCILE side) — did not find a
      cheaper prompt-level fix worth the churn; the mechanical tie-break is already
      the more reliable of the two options (per this audit's own Method: prefer
      mechanical ground truth over prompt-level hoping).
- [x] `internal/verbs/reconcile_decomposition.go` — read in full, including
      `isRepeatDisagreement`'s call site in `Commit`. **Found the bug described
      below** (round_number) plus a second, more severe one in `applyFixes`.
- [x] `internal/verbs/mechanical_checks.go` — read in full (650 lines, every
      function). No new defeat scenarios found beyond the documented ones;
      `goFixBeadSpec`'s vacuous-pass guard picks the *first* test file in
      `output_files` when a bead owns more than one and the exit criterion lacks
      `-run` — low-severity (the grep pattern is generic, `'func Test'`, not a
      specific name, so picking the "wrong" file rarely matters) — noted, not fixed.
- [x] `internal/verbs/inputs.go` — read `latestAuditCritique`/`loadDebateHistory` and
      every other query helper in the file. **`loadDebateHistory` is the sibling the
      checklist asked about**: it loads *every* `audit_reconcile_rounds` row for the
      project with no `outcome` filter, feeding both AUDIT's and RECONCILE's prompts
      — see the round_number finding below for why this matters.
- [x] Cross-check: `round_number` collisions — **confirmed NOT cosmetic, found live
      in production data, two independent mechanisms:**

  **Bug 1 — round_number is not a single authoritative sequence.**
  `DecomposeSpec.commitRedecompose` numbers redecompose rows via
  `COUNT(outcome='redecompose')+1` (its own 1,2,3... sequence); RECONCILE's `Commit`
  numbers real rounds via `COUNT(outcome!='redecompose')+1` (a separate 1,2,3...
  sequence, since last session's round-cap fix). Any project with at least one of
  each collides at `round_number=1`. **Verified in the live DB**: project 98
  (checkers-v8) has two rows with `(project_id=98, round_number=1)` — one
  `outcome='redecompose'`, one `outcome='disagreed_continuing'`.

  **Bug 2 — COUNT-based numbering is not self-healing, and collides between two
  *real* rounds too.** Project 97 (checkers-v7) has two rows with
  `(project_id=97, round_number=2, outcome='escalated')`, from two different
  `handoff_attempts` on the *same* `RECONCILE_DECOMPOSITION` job (attempt 1 at
  18:09:58, attempt 2 at 19:36:26 — nearly 1.5h apart, i.e. a manual requeue of the
  escalated job, not an automatic retry). Attempt 1's round_number=2 was itself a
  relic of the pre-fix code (which counted the redecompose row too); attempt 2's
  fresh COUNT-based computation (now fixed, counting only the 1 real row from
  attempt 1) landed on the same number by coincidence. This proves COUNT-based
  "next round number" inherits and perpetuates any historical mislabeling instead of
  self-correcting — `MAX(round_number)+1` would not have this property.

  **Downstream impact, confirmed by reading (not yet observed live)**:
  `loadDebateHistory` has no `outcome` filter, so both collisions are rendered
  verbatim into the "Previous Debate History" section of both AUDIT's and
  RECONCILE's own prompts — two `### Round 1` (or `### Round 2`) headers back to
  back, one of them a `redecompose` row whose `reconciliation` column is `''`, so
  `formatReconcileResponses` renders it as an empty "Reconcile Response" block
  attributed to a round that was never actually reconciled.

  **FIXED 2026-07-15**: added `nextRoundNumber` (`internal/verbs/inputs.go`) —
  `SELECT COALESCE(MAX(round_number),0)+1 FROM audit_reconcile_rounds WHERE
  project_id=?`, a single project-wide sequence spanning every row regardless of
  outcome. Both `DecomposeSpec.commitRedecompose` and
  `ReconcileDecomposition.Commit` now use it for the stored `round_number`,
  while keeping their existing (unchanged, still correct) COUNT-based counters
  for the redecompose-cap and round-cap comparisons — display numbering and
  cap-counting are now two explicitly separate concerns instead of one
  conflated variable. Tests added:
  `TestReconcileDecompositionCommitRoundNumberAfterRedecompose` and
  `TestDecomposeSpecCommitRedecomposeRoundNumberAfterRealRound` (mirror cases),
  both reproducing the exact production collisions found above.

  **Bug 3 (found investigating Bug 1/2, more severe, broader than Stage 2) —
  `ReconcileDecomposition.applyFixes`'s bead lookup
  (`WHERE json_extract(br.full_text,'$.title') = ?`) is not defended by `Validate()`.**
  `Validate` only checks `UpdatedBead != nil`, never that `updated_bead.title`
  matches an existing bead. A title typo/case-drift/rename in the model's own output
  — a well-formed JSON response that passes `Validate` — causes `applyFixes` to hit
  `sql.ErrNoRows`, which `Commit` returns as an error. Traced all the way through
  `internal/orchestrator/dispatch.go`: a `Commit()` error rolls back the *entire*
  transaction (losing every `agree_and_fix` update in the batch, not just the bad
  one), and — this is the broader, Stage-7-scope part — **the job is left in
  `status='running'` with no automatic recovery**. `tick()`
  (`internal/orchestrator/orchestrator.go`) just logs and moves to the next poll;
  `claimNextJob` only claims `'pending'` jobs; the only reset path is
  `resetStaleRunning`, which runs once at daemon startup. In steady state, any
  handler's `Commit()` error silently wedges the orchestrator's single execution
  slot until a human notices and restarts the daemon. Checked the live DB: no job is
  currently stuck this way (`SELECT ... WHERE status='running'` → empty), so this
  hasn't visibly bitten anyone yet — verified by tracing the code path, not by a
  caught incident. **FIXED 2026-07-15, both halves**: RECONCILE's `Validate` now
  caches the current project's bead titles (`h.knownTitles`, populated in `Run`,
  same pattern already used for `lastCritique`/`lastRoundsSoFar`/`lastHistory`)
  and rejects an `agree_and_fix` whose `updated_bead.title` doesn't match any of
  them — a title mismatch is now a normal malformed-output retry instead of a
  hard `Commit()` error. Additionally fixed the broader mechanism in
  `internal/orchestrator/dispatch.go` (pulled forward from Stage 7, since it was
  found here and the fix is small and self-contained): a `handler.Commit()`
  error is now caught, recorded as a failed attempt via the new
  `recordCommitFailure` helper (reusing the exact strike/tolerance math already
  computed for a `Validate` failure), and the job is moved to `failed_retry` or
  `escalated` — never left stuck in `'running'`. Tests added:
  `internal/verbs/validate_test.go` (new case reproducing the title-mismatch
  input), `internal/orchestrator/dispatch_test.go` (new file — first tests for
  the `orchestrator` package — covering both the under-tolerance retry and the
  at-tolerance escalation paths of `recordCommitFailure`).
- [x] Test coverage check (Method item 6): decent coverage exists
      (`mechanical_checks_test.go`, `debate_test.go`, `commit_test.go`) for the
      individual pieces added last session (`forwardFileReferenceChecks`,
      `isRepeatDisagreement`, sequential 2-round debate progression) but none of it
      exercises a redecompose-then-real-round interleaving, a requeue-after-
      escalation retry, or a title-mismatched `agree_and_fix` — i.e. it covers the
      happy path of round progression, not the failure modes just found.

**Session log (2026-07-14/15):** Three real bugs found (two are one root cause —
round_number numbering — plus the applyFixes title-validation gap that surfaced a
broader orchestrator-wide recovery gap). Verified by reproduction: live-DB queries
against project 97 and 98's actual `audit_reconcile_rounds`/`handoff_jobs`/
`handoff_attempts` rows, and a full code trace of the Commit-error path through
`dispatch.go`/`orchestrator.go`/`queue.go`. User chose "fix all 3, including the
orchestrator gap" — all fixed and tested same day (`go build ./...`, `go vet ./...`,
`go test ./...` all clean), including the first-ever tests for the `orchestrator`
package. Left uncommitted pending user review, per standing practice.

## Stage 3 — Test refinement: REFINE_TESTS_WRITE / REFINE_TESTS_CRITIQUE / REFINE_TESTS_JUDGE — AUDITED 2026-07-14

Test-first mode's WRITE → CRITIQUE → JUDGE cycle.

- [x] **Fixed the test-clobbering bug**: `internal/verbs/refine_tests.go:317-327`,
      `RefineTestsWrite.Run`'s `cid == 1` branch called `splice.Assemble(pkg, funcs)`
      using only the current bead's own written functions, discarding `originalSrc`
      entirely — silently deleted any prior bead's functions already in a shared test
      file. **Fix**: the branch condition is now "is `originalSrc` empty" rather than
      "is `cid == 1`" — a fresh path still assembles from scratch, but any path with
      existing content (whether from this bead's own earlier cycle, or a prior bead
      sharing the file) now splices via `splice.Replace` instead. Test added:
      `internal/splice/splice_test.go` (`TestSharedFileClobber`) reproducing the exact
      checkers-v8 case (bead A's TestFoo surviving bead B's cycle-1 TestBar write).
- [x] `internal/splice/splice.go` audited (`Assemble`, `Replace`, `FuncMap`,
      `detectImports`) — **found and fixed two more bugs, same "asymmetry" class**:
      `Replace` never re-ran import detection (only `Assemble` did), so any revision
      — same-bead cid>1, or the newly-fixed cross-bead splice path — that added or
      dropped a package dependency left the import block stale, with no way for the
      model to fix it (`write_function` only supplies function bodies, never
      imports); verified by reproduction, both directions. Separately, `detectImports`'s
      package whitelist was missing common packages, most notably `reflect`. **Fixed**:
      added `syncImports` (rebuilds the import block from scratch via `detectImports`
      against the post-edit file, mirroring what `Assemble` already did), wired into
      `Replace`; expanded the whitelist (`reflect`, `path`, `html/template`,
      `math/big`, `crypto/sha256`, `crypto/md5`, `encoding/base64`, `encoding/hex`,
      `runtime`, `flag`). Test added: `TestReplaceSyncsImports` (adds a needed import,
      then removes it once unused, confirming both directions).
- [x] `internal/verbs/refine_tests.go` CRITIQUE/JUDGE handlers read in full — no new
      bugs. Confirmed still holding, not regressed: the goban bead-568 grounding-rule
      prompt fix (`refineTestsWriteSystemPrompt`/`refineTestsCritiqueSystemPrompt`) is
      still present; JUDGE genuinely has no cross-cycle memory of its own prior
      verdicts (mitigated by the prompt fix, not by adding real memory — by design).
      The known cosmetic bug (`prompts.go:364`'s fill-in-the-blank CRITIQUE summary
      template, noted in `[[project_checkers]]`) is still present, still low-severity
      (doesn't affect the `all_correct`/`findings` fields JUDGE and downstream logic
      actually use) — left unfixed, not urgent.
- [x] Retroactive check across past `COMPLETE` projects, now that the fix exists to
      compare against: **confirmed real, live data corruption in two of the five
      `COMPLETE` projects.** Of the 5, only 3 had beads sharing a test file
      (othello-v3-f, chess-v1, goban-v2); of those, only chess-v1 and goban-v2 wrote
      the shared file via `REFINE_TESTS_WRITE` (othello-v3-f's shared files went
      through plain `EXECUTE_BEAD`, a different path, unaffected) — and both hit the
      bug. **chess-v1 (project 87), bead 536 (`ai-evaluation`)**: its own exit
      criterion requires `TestEvaluate` in `ai_test.go`; bead 537 (`ai-search`, same
      file, cycle 1) clobbered it — `ai_test.go` on disk today has only
      `TestBestMove`. **goban-v2 (project 91), beads 565 (`group-and-liberties`) and
      566 (`placement-and-pass`)**: both share `game_test.go` with bead 567
      (`valid-moves`, cycle 1), which clobbered both — 10 required test functions
      across the two beads are gone; `game_test.go` on disk today has only
      `TestValidMoves`. All three beads are marked `succeeded` and were never
      revisited; re-running each bead's own exit criterion against current on-disk
      state fails for all three. **Decision (user, 2026-07-14): leave both projects
      as-is** — they're finished/archival; this is recorded as a known historical
      data-integrity gap, no remediation performed (no manual test-file restoration,
      no DB patches). tasklist-v1 and chess-v3 have no shared test files, so were
      never exposed to this bug regardless of mode.

**Session log (2026-07-14):** One long-known bug fixed (test-clobbering) plus two
more of the identical "asymmetry" shape found auditing the surrounding code
(`Replace` not syncing imports; whitelist gaps) — all three fixed and tested same
session (`go build ./...`, `go vet ./...`, `go test ./...` all clean). Retroactive
project check surfaced confirmed corruption in 2 of 5 `COMPLETE` projects
(3 beads total) predating the fix — documented, left unremediated per user decision.

## Stage 4 — Bead execution: EXECUTE_BEAD / MONITOR_EXECUTION — AUDITED 2026-07-14

Subprocess-based; not a normal one-shot verb (`Run`/`Validate`/`Commit`) — special-cased
in the orchestrator. This is where the model actually writes code.

- [x] `internal/execution/bead.go` — read in full (the ChatWithTools loop, tool
      dispatch, budget/soft-stop handling, message-building helpers). **Found and
      confirmed by reproduction (standalone unit test, then deleted — not a
      permanent regression test yet, pending fix approval): a pure-test bead
      (`output_files` consisting entirely of `*_test.go` — 72 such beads exist in
      the live `ratchet.db`, e.g. `integration_test.go`-only integration beads) that
      reaches a retry (`execute_revised`) with its test file already on disk trips
      `isTestsLockedMode`.** That heuristic was designed for the REFINE_TESTS
      test-first shape (impl bead retrying against pre-certified, separately-owned
      test files) — for a pure-test bead it misfires: the bead's *own* file is
      mistaken for someone else's pre-certified, locked file. Reproduced via
      `isTestsLockedMode(dir, []string{"integration_test.go"})` = `true` when that
      file exists on disk; `runExecuteBeadReal`'s `expectedFiles` computation for
      the `testsLocked` branch then filters to *non*-test files, leaving
      `expectedFiles` empty (`nil`) since the bead has no impl files at all.
      `buildBeadUserMsg` produces a **self-contradictory prompt**: the "Output
      Files" section says "You may ONLY write to: `integration_test.go`" directly
      followed by a "Tests Locked" section saying "Do NOT write to
      `integration_test.go` under any circumstances... Write ONLY the
      implementation files listed in Output Files above" — but there are no
      implementation files, so nothing is legally writable. If the model then
      calls `write_file` without a `path` argument (an existing, already-handled
      failure mode elsewhere in this file), `buildMissingPathWarning(expectedFiles)`
      indexes `expectedFiles[0]` unconditionally and **panics** (`index out of
      range [0] with length 0`), crashing the execute-bead subprocess without ever
      writing `termination_cause` — which is exactly the infra-failure precondition
      that then hits the `dispatch.go` bug below, silently stranding the bead.
      Checked live DB: no bead has yet hit the exact trigger (a pure-test bead's
      test file surviving on disk into a second execution against the same or a
      later revision) — 72 pure-test beads exist but none has had 2+ executions
      with the file already present going in, so this hasn't caused an observed
      production failure yet. Same shape as Stage 1's stub-purity finding: reachable
      by construction, not yet observed live. **FIXED 2026-07-14**: `isTestsLockedMode`
      now requires at least one non-test file present in `output_files` before it
      will consider tests "locked" — a pure-test bead can never legitimately be in
      that mode, so it now falls through to the normal (non-contradictory) message
      path, letting the model revise its own test file on retry as intended.
      Defense-in-depth: `buildMissingPathWarning` no longer indexes `expectedFiles[0]`
      unconditionally — an empty slice now degrades to a generic warning message
      instead of panicking, in case some future bead shape reaches this some other
      way. Tests added: `TestIsTestsLockedMode_PureTestBeadNeverLocks` (reproduces
      this exact bug and confirms no contradictory prompt), 
      `TestIsTestsLockedMode_ImplBeadStillLocks` (confirms the intended REFINE_TESTS
      case still locks correctly), `TestBuildMissingPathWarning_EmptyExpectedFilesDoesNotPanic`.
      Separately, lower-severity and not fixed: the no-write-warning retry (fires
      once when the model declares done with zero tool calls and zero writes) falls
      through to `termination_cause='success'` if the model ignores the warning and
      again produces zero tool calls on the very next turn. Traced downstream:
      `ANALYZE_EXECUTION`'s `checkOutputFiles` independently stats every output file
      regardless of `termination_cause`, so this doesn't appear to change any actual
      ADJUDICATE decision — it's a cosmetic mislabel in the trace/analysis text
      ("Termination cause: success" for a run that wrote nothing), not a correctness
      bug. Left as a low-priority note.
- [x] `internal/execution/window.go` — read in full (`RunExecutionWindow`,
      `handleInfraFailure`, `finalizeExecution`, kill helpers). **Found and confirmed
      by reproduction (real compiled `ratchet` binary + real file-backed DB,
      throwaway test then deleted) a severe bug spanning this file and
      `internal/orchestrator/dispatch.go`: dispatch.go silently clobbers
      handleInfraFailure's job-status decision.** `dispatch()`'s `EXECUTE_BEAD`
      branch is: `if err := execution.RunExecutionWindow(...); err != nil {
      ...failed_retry...; return err }` then, unconditionally,
      `UPDATE handoff_jobs SET status = 'complete' WHERE id = ?`. But
      `handleInfraFailure` (called from inside `RunExecutionWindow` when
      execute-bead crashes before writing any `termination_cause`, e.g. SQLITE_BUSY
      during its startup `db.Open`) already writes the *same* job's status itself —
      `'pending'` to retry when under `infraFailureCap` (3), `'escalated'` when at
      cap — and returns `nil` on a successful write. Because `RunExecutionWindow`
      then also returns `nil`, `dispatch()` takes the success branch and overwrites
      whatever `handleInfraFailure` just wrote back to `'complete'`, unconditionally,
      with no status guard. Net effect: **any infra failure permanently and
      silently strands the bead** — marked `'complete'` with no `ANALYZE_EXECUTION`
      ever enqueued (window.go correctly skips that on the infra-failure path, so
      there's no other job left to pick the bead back up), and even the
      `infraFailureCap` escalation path is invisible to a human since `'escalated'`
      also gets overwritten to `'complete'`. Reproduced live: built the real
      `ratchet` binary, seeded a project with no `EXECUTE_BEAD` model assignment
      (so the real execute-bead's startup JOIN returns `sql.ErrNoRows` and it exits
      before writing anything — the same shape as the documented SQLITE_BUSY
      scenario), called `RunExecutionWindow` for real, confirmed
      `infra_failure=1`/`handoff_jobs.status='pending'` immediately after, then
      replicated `dispatch.go`'s exact tail logic verbatim and watched it flip to
      `'complete'`. Cross-checked the live `ratchet.db`: 8 beads show 2 consecutive
      `infra_failure=1` executions in the July 4–10 window; all eventually reached
      `status='succeeded'`, meaning recovery in practice required something other
      than the built-in retry path (manual restart/rewind per the session-55
      "Operational finding" in `[[project_ratchet]]`) — consistent with, though not
      conclusive proof of, this bug biting in production. **FIXED 2026-07-14**:
      extracted the `EXECUTE_BEAD` completion write into
      `completeExecuteBeadJob(ctx, d, jobID)` (`internal/orchestrator/dispatch.go`,
      alongside `recordCommitFailure` — same "small, directly-testable helper"
      pattern) and added `AND status = 'running'` to its `UPDATE`. On the normal
      completion path the job is still `'running'` (nothing else touches it), so it
      still fires exactly as before; on the infra-failure path the job is already
      `'pending'`/`'escalated'`, so the guarded write now affects zero rows instead
      of clobbering it. Tests added (`internal/orchestrator/dispatch_test.go`):
      `TestCompleteExecuteBeadJob_DoesNotClobberInfraFailureRetry` (both the
      `'pending'` and `'escalated'` cases — reproduces the exact bug), 
      `TestCompleteExecuteBeadJob_CompletesRunningJob` (confirms the normal path is
      unaffected). Also added direct coverage for `handleInfraFailure` itself
      (`internal/execution/execution_test.go`), which had zero tests before this
      session: `TestHandleInfraFailure_UnderCapRetriesJob`,
      `TestHandleInfraFailure_AtCapEscalates`.
      `recoverOrphanedExecutions`/`resetStaleRunning` (orchestrator.go/queue.go) were
      also read in full as part of tracing this — they handle the *different*
      scenario of the whole orchestrator process crashing/restarting mid-execution,
      and are unaffected by this bug (correctly gated on `status = 'running'`).
- [x] `internal/execution/monitor.go` — read in full. **Root-caused the checklist's
      known gap.** The only mechanical (non-model) FIRE check in this file is
      `isWriteFileStall` (trace byte-size unchanged for 24 consecutive ~30s ticks
      *and* the last trace line is a bare `[TURN N]` marker) — this catches exactly
      one failure mode, "model frozen mid-generation," and nothing else. The two
      "Explicit loop patterns" rules stated in `monitorSystemPrompt`
      (1: identical `run_command` stdout/stderr twice with no intervening
      `write_file` → FIRE; 2: the literal string `write_file requires a 'path'`
      appearing 2+ times → FIRE) have **zero mechanical backstop** — both are
      enforced purely by `mistral-small3.2:24b` (the weakest model in the fleet)
      correctly parsing and applying a prompt rule against raw trace text.
      `monitor.go` never calls `internal/trace.Parse`, even though that package
      already produces exactly the structured data (`CommandResult{Command, Stdout,
      Stderr, Turn}`) needed to check rule 1 mechanically, and is already used for
      this purpose by `ANALYZE_EXECUTION` — the capability exists in the codebase,
      it's just not wired into the live watchdog. This directly explains the
      checkers-v8 bead 627 attempt 2 gap noted below: a repeated identical
      `grep ... && echo Pass || echo Fail` self-check (always exit 0, so it never
      even touches `isWriteFileStall`'s trace-growth check, since a run_command +
      result pair appends to the trace every tick) depends entirely on the model
      correctly applying rule 1 from prose, with no fallback if it doesn't. This is
      the same class of gap as Stage 5's `declare_success` finding: a model's own
      narrative feeds a consequential decision (FIRE/NO_FIRE) with no mechanical
      ground truth consulted for two rules that are, in fact, mechanically checkable.
      **FIXED 2026-07-14**: added `mechanicalLoopPatternCheck` (mirrors
      `isWriteFileStall`'s placement — a pre-check run before the model call), using
      `trace.Parse`'s `CommandResult`/`WriteFileResult` lists to detect both
      documented rules directly: rule 1 walks commands and successful writes in
      chronological (turn) order, tracking a per-signature (command+stdout+stderr)
      "seen" set that's cleared on every successful write, firing on a repeat; rule
      2 is a literal `strings.Count` of the exact missing-path error text (matching
      the string `toolWriteFile` actually returns) reaching 2+. Test added:
      `internal/execution/monitor_test.go` (`TestMechanicalLoopPatternCheck`) —
      covers both rules firing, both rules *not* firing when the mitigating
      condition holds (different output; an intervening write; only one
      occurrence), and the checkers-v8 bead 627 scenario specifically (the exact
      `grep ... && echo Pass || echo Fail` self-check pattern).
      Original known-gap note, now explained rather than open: a repeated identical
      self-check command (`grep ... && echo Pass || echo Fail`, always exit 0) did
      NOT trigger a MONITOR fire in checkers-v8 bead 627 attempt 2, even though the
      printed stdout was identical ("Fail") both times with no intervening write —
      confirmed this is a real gap (no mechanical enforcement of the loop-pattern
      rule), not working-as-intended.
- [x] `internal/execution/tools.go` — read in full. `safePath` correctly blocks `../`
      traversal and absolute-path escapes for `write_file`/`read_file` (verified by
      reasoning through `filepath.Join`+`Clean`+prefix-check against both defeat
      forms). `run_command`'s arbitrary `bash -c` execution is intentional (running
      build/test commands is the model's job), not a bug. Cross-checked the exact
      error string `toolWriteFile` returns for a missing path
      (`"write_file requires a 'path' argument"`) against `bead.go`'s
      `strings.Contains` check that sets `missingPathDetected` — strings match
      exactly, no drift. No bugs found.
- [x] `internal/execution/prompts.go` — read in full (`executeBeadSystemPrompt` +
      `monitorSystemPrompt`). Cross-checked the EXECUTE prompt's stated rules
      (Output Files as write permission, read-before-write on an existing file,
      the `ls`-based Output-Files-exist check before declaring done) against
      `bead.go`'s actual behavior — consistent, no drift found beyond the
      `isTestsLockedMode` bug already noted above (which is a code-logic bug, not a
      prompt/code mismatch). `monitorSystemPrompt`'s loop-pattern rules are covered
      under `monitor.go` above.
- [x] Test coverage check (Method item 6): `execution_test.go` covers only
      DB-plumbing helpers (`writeMonitorFired`, `writeTerminationCause`,
      `readMonitorFired`) and pure string logic (`parseDecision`, `stubLine`);
      `smoke_test.go` covers the SIGTERM contract and the monitor-fire two-stage
      kill. **Zero coverage existed for either bug found this session**:
      `isTestFirstMode`/`isTestsLockedMode`/`buildBeadUserMsg`/
      `buildMissingPathWarning` (the contradiction+panic bug) and
      `handleInfraFailure` (the dispatch.go clobbering bug) had no tests at all —
      not even a happy-path test of `handleInfraFailure` in isolation. Both
      throwaway verification tests written this session were deleted after
      confirming the bugs (per standing practice: propose before applying a fix);
      real regression tests should be added alongside whichever fix the user
      approves.

**Session log (2026-07-14):** Two real, confirmed-by-reproduction bugs found (the
`isTestsLockedMode` pure-test-bead contradiction+panic in `bead.go`, and the
`dispatch.go`/`handleInfraFailure` job-status clobbering spanning
`execution/window.go` and `orchestrator/dispatch.go`), plus one root-caused,
previously-open known gap (MONITOR's loop-pattern rules have no mechanical
backstop). All three verified by real reproduction this session (built binary +
real DB for the dispatch bug; standalone unit test for the panic bug), not just
static reading — throwaway test files deleted after verification, no permanent
code changes made yet. Cross-checked live `ratchet.db` for both bugs: the panic
bug hasn't been triggered live yet (72 pure-test beads exist, none has hit the
exact retry-with-file-on-disk precondition); the dispatch-clobbering bug has
suggestive-but-not-conclusive live evidence (8 beads with repeated infra failures
in July 4–10, all eventually recovered via what appears to be manual intervention
rather than the built-in retry path). No fixes applied — pending user decision, per
standing practice ([[feedback_propose_before_apply]]).

## Stage 5 — Analysis & judgment: ANALYZE_EXECUTION / COMPRESS_ANALYSIS / ADJUDICATE_NEXT_EXECUTION — AUDITED 2026-07-14

The largest and highest-stakes stage (`adjudicate_next_execution.go` alone is 1166
lines). One real bug had already been found and fixed here before this stage's audit
(`declare_success` trusting narrative over mechanical ground truth) — the rest of the
file got the same scrutiny this session, not just that one code path.

- [x] `internal/verbs/analyze_execution.go` — read in full. No new bugs found in the
      mechanical-findings generation itself. **Known, not-yet-fixed display bug,
      confirmed still present**: `internal/trace/findings.go`'s "All Commands Run"
      section suppresses stdout whenever `ExitCode == 0` — this is what let a
      `cmd && echo Pass || echo Fail` self-check (always exits 0) hide its own "Fail"
      output from the analyzer model. The `declare_success` mechanical gate makes
      this moot for that one decision path; it's still live for every other decision
      this stage's analyzer narrative feeds. Left unfixed — general finding, not
      re-chased this session. **Minor, lower-confidence note (not fixed, not verified
      live)**: `checkUndeclaredFiles`'s caller at line 73 swallows
      `loadCurrentBeads`'s error (`allBeads, _ := ...`); on a DB error there, every
      file in the project folder would be flagged "undeclared" in the findings text.
      Requires a transient DB failure at exactly that moment to trigger — flagged for
      visibility, not treated as a confirmed bug.
- [x] `internal/verbs/compress_analysis.go` — read in full (`sanitizeJSON`,
      `escapeControlCharsInStrings`, `injectResolvedTags`, `synthesizeMinimalEntry`).
      Traced the two-pass JSON sanitizer and the RESOLVED-tag substring matching
      against `checkGoTestOutput`'s real `go test` output format — consistent, no
      defeat scenario found.
- [x] `internal/verbs/adjudicate_next_execution.go` — every decision branch and every
      note-injection helper read in full. Two confirmed bugs, both reproduced with
      throwaway tests before fixing, both now fixed, tested, and left uncommitted:

  **Bug 1 — `checkConsistency` is negation-blind, producing false-positive
  "malformed" rejections of correct ADJUDICATE output.** It flags a contradiction
  whenever a "counterpart phrase" appears anywhere in the reasoning text, with no
  check for negation. A model correctly declaring `execution_capability_problem`
  while reasoning *"This is not a spec problem, it's clearly an execution capability
  problem..."* was rejected, because `"spec problem"` is a literal substring of
  `"not a spec problem"`. The code had already patched one instance of exactly this
  shape (a bare `"bead specification is"` phrase was deleted with a comment
  explaining the false-positive it caused) but that was a point deletion, not a
  general fix — the same defeat pattern remained live for most of the ~20 other
  phrases across both `bead_problem`/`execution_capability_problem` lists.
  Reproduced with a standalone test before fixing. Not yet observed live (checked
  `handoff_attempts` in the live DB: zero `consistency check failed` rows exist),
  but trivially reachable by construction — same bar as other stages' findings.
  **FIXED 2026-07-14**: added `firstUnnegatedMatch`/`containsNegationCue` — before
  flagging a matched phrase, scans the 24 characters immediately preceding it for a
  negation cue (`not `, `no `, `never `, contractions like `isn't`/`doesn't`/`can't`,
  etc.) and skips the match if one is found. Verified the original true-positive
  fixture (Exp-5 GLM contradiction, unnegated "the specification is clear") still
  fires correctly. Test added: `TestAdjudicateConsistencyNegatedPhraseFixture`
  (`adjudicate_smoke_test.go`), reproducing the exact false-positive found.

  **Bug 2 (more severe) — the rewind-lineage filter (`currentLineage`) has been dead
  code since the commit that introduced it, because a sibling fix in that same
  commit eliminated its only detection signal.** `currentLineage` inferred a rewind
  boundary from `revision_number` failing to exceed the running max in creation
  order. But commit `4fafc23` (2026-07-13) — the *same* commit — also changed every
  `bead_revisions` insert site (ADJUDICATE's `execute_revised`/`test_reject`,
  REVISE_PENDING, RECONCILE's `applyFixes`) to number new revisions via a bead-wide
  `MAX(revision_number)+1`, specifically to avoid numbering collisions with stale
  post-rewind rows. Since `rewind-bead` itself never inserts a `bead_revisions` row
  (confirmed by reading `internal/project/rewind.go`: it only repoints
  `current_revision_id` back to revision 1's id), every revision inserted after a
  rewind is guaranteed to have a `revision_number` strictly greater than any prior
  row — the exact condition `currentLineage`'s loop requires to declare "not a
  rewind boundary." Reproduced with a standalone test mirroring realistic post-fix
  data: `currentLineage` returned all 4 rows (full pre-rewind history included)
  instead of the intended 2. This directly explains the "ADJUDICATE lineage-leak
  wrinkle" flagged as still-open in the goban-v2 project memory — root-caused here.
  Net effect: ADJUDICATE and COMPRESS_ANALYSIS have been seeing the full pre-rewind
  revision history and execution/analysis records as if still live for every rewound
  bead since 2026-07-13 (confirmed 8 rewound beads exist in the live DB across
  projects 86/87/88/90/91, though all predate the 07-13 fix so their *own* data
  happens to still be filterable by the old heuristic — the exposure is to any
  rewind from 07-13 onward, none of which has landed yet). **FIXED 2026-07-14**:
  added a persisted `beads.rewound_at` TIMESTAMP column (schema.sql +
  `columnMigrations`, migrates existing DBs automatically — verified against a copy
  of the live `ratchet.db`). `rewind-bead` now sets it in the same `UPDATE` that
  resets `current_revision_id`/`execution_attempts_override`. `loadBeadRevisionLog`
  now loads it and `currentLineage` filters on `revision_number == 1 ||
  created_at >= rewound_at` instead of the dead revision-number heuristic — durable
  across any number of rewinds on the same bead (lifting the old "only catches the
  first rewind" documented limitation too, since `rewound_at` is overwritten fresh
  on every rewind rather than inferred from data written for an unrelated purpose).
  Tests added: `internal/verbs/inputs_test.go` (new file) —
  `TestCurrentLineageFiltersPostRewind`/`TestCurrentLineageNeverRewound` (pure-logic)
  and `TestLoadBeadRevisionLogFiltersPostRewind` (end-to-end against a real
  in-memory DB, seeding revisions through the same bead-wide `MAX+1` pattern and the
  same rewind `UPDATE` shape production code uses).
  **Known, not-yet-fixed finding (separate, general)**: the `recurringTestFailureNote`
  fast path concludes "the test assertions are logically impossible" on a recurring
  failure without checking *why* the test keeps failing — the checkers-v6 incident
  (real cause was a Go template scoping bug, not an impossible assertion) shows it
  can't distinguish "implementation produces wrong values" from "implementation
  throws a runtime/template error." Not re-chased this session.
- [x] Cross-check: `verifyExitCriteriaMechanically`'s wiring into `declare_success`
      traced end-to-end against `atExecutionCap` — when the mechanical gate fails and
      the bead is at its attempt cap, `atExecutionCap` correctly escalates the job
      (same code path `execute_as_is`/`execute_revised`/`test_reject` already use);
      when under cap, the bead is correctly reset to `pending` and re-enqueued. No
      bug found; matches the existing unit test's claim, now also confirmed by trace.
- [x] Cross-check: `regenerateAPICheckTest`'s self-healing runs *after* the
      `declare_success` mechanical gate has already passed and after the bead is
      marked `succeeded`, so it cannot invalidate a decision that already committed.
      The only residual risk is generic disk/tx non-atomicity (a file write inside a
      `Commit()` that isn't rolled back if a later statement in the same `Commit`
      fails) — shared by every other `Commit()` in this codebase that does disk I/O
      (`report.WriteBead`, `os.Remove` in `test_reject`, etc.), not specific to this
      pairing. No bug found.
- [x] Test coverage check (Method item 6): `analyze_execution.go` and
      `compress_analysis.go` had **zero** dedicated tests before this session — same
      pattern as other stages before their audits, left as-is since no bug was found
      in either file's own logic. `adjudicate_next_execution.go` had
      `adjudicate_next_execution_test.go` (`verifyExitCriteriaMechanically`) and
      `adjudicate_smoke_test.go` (two `checkConsistency` fixtures — one true-positive,
      one true-negative) but neither exercised the negation-blindness false-positive
      or any rewind-lineage scenario; both gaps now closed by the tests added above.

**Session log (2026-07-14):** Two real, confirmed-by-reproduction bugs found and
fixed (negation-blind consistency check; dead rewind-lineage filter — the latter
traced to an unintended interaction between two fixes in the same prior commit, and
root-causing a previously-flagged-but-unexplained goban-v2 memory item). Both
verified: standalone reproduction test before the fix, passing regression test after,
plus a full `go build ./...`/`go vet ./...`/`go test ./...` pass and a live-DB
migration dry-run (copied the real `ratchet-projects/ratchet.db`, ran a real compiled
binary against it, confirmed `rewound_at` added cleanly with existing data intact).
Left uncommitted pending user review, per standing practice.

## Stage 6 — Cross-bead handoff: REVISE_PENDING, rewind-bead, bead lifecycle — AUDITED 2026-07-14

- [x] `internal/verbs/revise_pending.go` — read in full (Run/Validate/Commit).
      The trimmed context (trigger bead's non-test output files only, not full
      project history) still checks out — no evidence of a real miss. The
      "dispatch next pending bead" query (`id > triggerBeadID AND status =
      'pending'`) correctly does *not* need to also catch a lower-ID bead that
      was independently rewound mid-flight — rewind-bead enqueues that bead's
      own REFINE_TESTS_WRITE job directly, so the two mechanisms don't need to
      overlap. Minor, not fixed: `revisionMap` is keyed by bead title with no
      uniqueness validation anywhere upstream (title isn't even a DB column);
      a model emitting two identical bead titles would silently collide. Low
      probability, and shared by AUDIT/RECONCILE's own title-keyed maps too —
      not specific to this file, not chased further.
- [x] `internal/project/rewind.go` — **found and fixed a severe, confirmed bug**
      (full writeup below). Also confirmed the "no --to flag, no mid-flight
      corrected revision" design decision still holds and wasn't touched. The
      goban `execute_revised`-regenerating-stale-content wrinkle referenced
      here was already root-caused and fixed in Stage 5 (dead lineage filter,
      see `[[project_audit_stage5]]`) — this stage found a *different* bug in
      the same file, described below.
- [x] `internal/project/restart.go`, `internal/project/fullstop.go`,
      `internal/project/resume.go` — read in full. No bugs found.
      `resume-project`'s "first bead" query has no `status='pending'` filter
      (unlike the UI's equivalent handler, `internal/ui/handlers.go`'s
      `handleResumeProject`), but this is currently safe by construction:
      `paused` status is *only* ever set by `RECONCILE_DECOMPOSITION` before
      any bead has executed, so every bead is still `pending` at that point.
      Flagged as drift between two independent implementations of the same
      operation — worth collapsing into one shared helper if Stage 8 (UI & CLI)
      finds more of this pattern, not urgent on its own. `fullStopProject`
      only resets `status='pending'` beads to `full_stopped`; a bead caught
      mid-`executing` keeps that stale status forever after — inert in
      practice since a `full_stopped` project is never dispatched again, but
      cosmetically wrong. Not fixed (low value, no observed harm).

**Bug found — `rewind-bead` discarded structural spec fixes, not just corrupted
prose, sending affected beads into a silent infinite retry loop.**

`rewindBead` reset a bead's *entire* spec (title, prose, output_files,
exit_criteria, execution_budget, monitor_override) to bead_revisions
`revision_number = 1` — literally `DECOMPOSE_SPEC`'s raw, pre-any-fixup output.
But `output_files`/`exit_criteria` routinely gain *permanent structural
corrections* after revision 1, before rewind is ever relevant:
`RECONCILE_DECOMPOSITION`'s mechanical `goFixBeadSpec`
(`internal/verbs/mechanical_checks.go`) appends a derived test filename to
`output_files` whenever a bead has a `go test` exit criterion but no declared
test file — confirmed live in the production DB (`ratchet-projects/ratchet.db`):
bead 261 gained `main_test.go` and bead 264 gained `handlers_test.go`, both at
revision 2 via `RECONCILE_DECOMPOSITION`, neither present at revision 1.
`ADJUDICATE_NEXT_EXECUTION`'s "workspace repair" pattern
(`prompts.go`: "if the trace shows writes to files outside output_files, name
those files explicitly in the revised spec with a cleanup instruction") does
the same thing later in a bead's life.

Reverting to revision 1 silently threw those fixes away. Concretely: a bead
rewound after gaining a RECONCILE-added test file loses that file from its
spec entirely; `refine_tests.go`'s `RefineTestsWrite.Run` hard-errors
(`"no test file paths for bead %d"`) whenever `output_files` has no `_test.go`
entry; and `dispatch.go` treated *any* `handler.Run` error as a transient
infra failure — reset straight back to `'pending'` with **zero** attempt
recorded, zero strike counted, no cap. The job would retry the identical,
permanently-failing call forever, completely invisible to any dashboard or
escalation view. Not yet observed live (beads 261/264 haven't been rewound),
but fully loaded — the next `rewind-bead` on either would trigger it.

**Fix (2026-07-14, both parts confirmed by reproduction, user chose option
"prose from revision 1 / output_files+exit_criteria from the pre-rewind
current revision" for the design question, "minimal" for the dispatch fix):**

1. `internal/project/rewind.go` — `rewindBead` now loads *both* revision 1 and
   the bead's current (pre-rewind) revision, and inserts a merged spec as a
   fresh `bead_revisions` row (bead-wide `MAX(revision_number)+1`, same
   pattern every other insert site already uses): `full_text`, `title`,
   `execution_budget`, `monitor_override` from revision 1 (the clean baseline
   rewind is meant to restore); `output_files`, `exit_criteria` from the
   current revision (preserving structural fixes). The on-disk cleanup step
   (test-file deletion, `WriteScaffoldStubs`) now runs against this merged
   `output_files`, so a RECONCILE/ADJUDICATE-added file is correctly reset
   instead of orphaned. Needed a new CHECK-constraint migration
   (`migrateBeadRevisionVerbsRewind` in `internal/db/db.go`, plus
   `schema.sql`) since `bead_revisions.created_by_verb` only allowed the four
   pipeline verb names — added `'REWIND_BEAD'`. Verified against a live copy
   of `ratchet-projects/ratchet.db`: row count unchanged (744 before/after),
   new schema correct, a `REWIND_BEAD` insert now accepted. Also extracted the
   previously `os.Exit`-only logic into a testable `rewindBead` function
   (mirroring the existing `fullStopProject`/`RunFullStopProjectMain` split) —
   this file had zero test coverage before this session (Method item 6).
   Updated `currentLineage`'s comment in `internal/verbs/inputs.go`, which had
   documented the old "rewind repoints at revision 1 rather than inserting a
   fresh row" behavior as the reason revision 1 is always kept in the lineage
   view — that invariant no longer holds structurally, but the lineage filter
   still works correctly (the new merged row's `created_at` satisfies the
   `>= rewound_at` clause on its own), so this was a comment fix, not a logic
   change.
2. `internal/orchestrator/dispatch.go` — added `recordRunFailure`, mirroring
   the existing `recordCommitFailure` pattern exactly: a `handler.Run` error
   now gets the same strike/tolerance accounting as a malformed `Validate`
   result (an attempt row is written with `validation_result = "run_error:
   ..."`, counted by the existing `strikeCount` query), so a Run() error that
   recurs identically on every retry escalates once `tolerance` (2) is
   exceeded instead of looping forever with no record. A genuinely transient
   error still just retries — only a *repeating* failure escalates. Left
   `EXECUTE_BEAD`'s own separate Run-error path (top of `dispatch()`)
   untouched — it already has its own `infraFailureCap` handling inside
   `RunExecutionWindow` for the common case (see Stage 4), and generalizing
   further wasn't part of the confirmed reproduction chain; flagged for
   whoever does the full Stage 7 pass. Also didn't touch the model-warmup
   failure paths just above (model lookup, Ollama warmup) — same reasoning,
   narrower/likely-more-transient case, kept the fix minimal per the user's
   choice.

Tests added: `internal/project/rewind_test.go`
(`TestRewindBead_PreservesOutputFilesAddedAfterRevision1` — seeds exactly the
revision-1/revision-2 split found live, rewinds, and asserts the merged spec
keeps `game_test.go`, the prose still reverts to revision 1's, and the stale
on-disk test file is actually deleted rather than orphaned;
`TestRewindBead_AlreadySucceededErrors`); `internal/orchestrator/dispatch_test.go`
(`TestRecordRunFailure_UnderTolerance`, `TestRecordRunFailure_EscalatesAtTolerance`).
`go build ./...`, `go vet ./...`, `go test ./...` all clean. Left uncommitted
pending user review, per standing practice.

## Stage 7 — Orchestrator: job queue, dispatch, recovery — AUDITED 2026-07-15

- [x] `internal/orchestrator/queue.go` — read in full, including
      `recoverOrphanedExecutions`/`resetStaleRunning`/`strikeCount`/
      `commitAttempt`. `claimNextJob`'s FIFO-on-`created_at` dispatch: confirmed
      still NOT bead-ID-ordered, and confirmed intentional after tracing
      `rewind-bead`'s interaction with it — a rewound bead's fresh job gets
      `created_at = now` (rewind time), which sorts it *behind* any already
      in-flight later bead's job, not ahead. That means an in-flight later
      bead's own job chain finishes (or fails) before the rewound earlier
      bead's job is picked up, rather than being interrupted mid-execution —
      traced through every `INSERT INTO handoff_jobs` call site (all insert at
      most one job per call with the enqueue-time `now`, so no same-tick
      ordering ambiguity exists in practice either). No additional problem
      found beyond the already-documented "out-of-order relative to bead ID"
      behavior, which turns out to be safe by construction. Also traced
      `recoverOrphanedExecutions`'s `termination_cause = 'success'` placeholder
      (used because the schema's CHECK constraint has no dedicated "crashed"
      value) all the way through `analyze_execution.go`/
      `adjudicate_next_execution.go` — confirmed intentional and safe for the
      decision pipeline (an infra-failure/orphan row can never get an
      `analyses` row, since `finalizeExecution` — the only place that enqueues
      `ANALYZE_EXECUTION` — is never reached on that path), but found the
      placeholder **was** leaking into human-facing display: see the
      observability fix below.
- [x] `internal/orchestrator/dispatch.go` — every branch read in full. **Partial
      credit from Stage 2's audit**: the `handler.Commit()`-error-wedges-the-job
      gap (generic to every verb) was fixed 2026-07-15 — see Stage 2's
      `applyFixes` entry and `recordCommitFailure`. **Partial credit from Stage
      4's audit**: `completeExecuteBeadJob`'s `status = 'running'` guard, fixed
      2026-07-14 — see Stage 4's `window.go` entry. **Confirmed and fixed the
      bug flagged for this stage in Stage 6's log**, plus two siblings of the
      same shape found auditing the rest of `dispatch()`:

  **Bug — three separate failure paths in `dispatch()` had zero strike/tolerance
  accounting**, unlike every other failure path in this file (`recordRunFailure`
  for `handler.Run`, `recordCommitFailure` for `handler.Commit`,
  `completeExecuteBeadJob`'s guard for the infra-failure race):
  1. Model-lookup failure before warmup (missing/unreadable
     `verb_model_assignments` row) — `UPDATE ... SET status = 'pending'`
     unconditionally, no attempt written.
  2. Ollama warmup failure (`oc.Warmup`) — same.
  3. `EXECUTE_BEAD`'s `RunExecutionWindow` returning an error — `UPDATE ...
     SET status = 'failed_retry'` unconditionally, no attempt written. This is
     exactly the gap Stage 6's log flagged and deliberately deferred:
     *"generalizing further wasn't part of the confirmed reproduction chain;
     flagged for whoever does the full Stage 7 pass."*

  None of the three ever counted a strike or could escalate. A **non-transient**
  failure — Ollama unreachable or a required model never pulled (fails every
  warmup, forever), a persistent OS-level issue preventing `execute-bead` from
  starting (disk full creating the trace file, fork/exec resource exhaustion),
  or a bead whose `current_revision_id` query fails — would retry the same job
  forever on the orchestrator's single execution slot, 2 seconds apart, with no
  attempt record and no escalation ever firing, invisible to a human beyond log
  spam. Checked the live DB (`ratchet-projects/ratchet.db`): no job is
  currently stuck this way; found one genuine, recent orphan-recovery event
  (execution 631, bead 630, same day) that happened to resolve cleanly on
  retry — same "reachable by construction, not yet observed live" bar as
  several other fixed bugs in this audit. **FIXED 2026-07-15**: all three paths
  now route through the existing `recordRunFailure` helper (wrapping the
  model-lookup and warmup errors with distinguishing context —
  `"model lookup: %w"` / `"ollama warmup for model %s: %w"` — so the persisted
  `validation_result` stays diagnostic even without a separate log line), so a
  persisting failure now escalates after `tolerance` (2) like every other
  failure class. `recordRunFailure`'s own log line now also includes
  `bead_id`, since callers no longer log it themselves before delegating.
  Tests added (`internal/orchestrator/dispatch_test.go`):
  `TestDispatch_ModelLookupFailureAppliesStrikeAccounting`,
  `TestDispatch_OllamaWarmupFailureAppliesStrikeAccounting` (real HTTP call to
  an unreachable address — no mock needed, connection-refused is immediate),
  `TestDispatch_ExecuteBeadRunErrorAppliesStrikeAccounting` (a bead with no
  `current_revision_id`, so `RunExecutionWindow` fails on its first query with
  no subprocess ever spawned) — all three exercise `dispatch()` itself end to
  end, not just the already-tested `recordRunFailure` helper in isolation.
- [x] `internal/orchestrator/orchestrator.go`, `internal/orchestrator/lock.go` —
      read in full. Advisory locking (`acquireLock`/`releaseLock`) and the
      poll loop (`tick`) are correct: the `INSERT ... ON CONFLICT ... WHERE
      heartbeat_at < ?` upsert is a single atomic statement, so two instances
      racing to acquire at once cannot both win (SQLite's write lock
      serializes them, and the loser's conditional `UPDATE` affects zero
      rows). **Found one real gap, lower likelihood than the dispatch.go bug
      but genuine**: `runHeartbeat`'s heartbeat write was owner-scoped
      (`WHERE id = 1 AND owner = ?`) but nothing ever checked whether it
      actually still held the lock — if this instance's heartbeat lapsed past
      `lockStaleAfter` (60s) while the process was still alive (a long
      scheduler/GC stall, not a crash — the crash case is already handled by
      `resetStaleRunning` at the *next* startup), another orchestrator
      instance could steal the lock via `acquireLock` while the first kept
      right on dispatching. `claimNextJob`'s atomic per-row `UPDATE` prevents
      two instances from claiming the *same* job, but nothing stops them from
      concurrently working *different* jobs of the same project — a
      split-brain violating the package's documented "orchestrator is the
      only process that writes to the DB" invariant. Needs an actual >60s
      stall of a live process to trigger, not just a crash — narrower than
      the dispatch.go bug, but the same "assumed invariant with no
      continuous enforcement" shape as this audit's other findings. **FIXED
      2026-07-15**: extracted the per-tick write into `heartbeatTick` (returns
      whether `owner` still holds the lock); `runHeartbeat` now stops and sets
      a `*atomic.Bool` when the write affects zero rows, and `Run`'s main loop
      checks that flag every iteration, returning an error instead of
      continuing to dispatch under a lock it no longer holds. Tests added
      (`internal/orchestrator/lock_test.go`, new file — first tests for
      `lock.go`): `TestAcquireLock_RejectsFreshLock`,
      `TestAcquireLock_StealsStaleLock`, `TestHeartbeatTick_ReportsLockLoss`
      (reproduces the exact steal-out-from-under scenario).
- [x] Cross-check, surfaced by tracing `queue.go`'s `termination_cause =
      'success'` placeholder (see above): **found a real observability gap,
      outside this stage's own files but directly caused by it** —
      `internal/ui/queries.go` (`queryBeadDetail`) and `internal/report/bead.go`
      (`queryExecutions`) both display raw `termination_cause` with no
      `infra_failure` check, so a crashed/orphaned execution renders as plain
      `success` in the bead detail page and the audit report — indistinguishable
      from a real completion to a human operator (confirmed live: execution 631
      for bead 630 renders this way today). **FIXED 2026-07-15**: both queries
      now also select `infra_failure` and override the displayed cause to the
      synthetic label `"infra_failure"` when set (display-only — the
      underlying schema/CHECK constraint and decision-pipeline behavior are
      untouched). Added a `.status.infra_failure` CSS rule
      (`internal/ui/templates/layout.html`) so it's visually distinct too.
      Verified against a copy of the live DB: the query correctly relabels
      execution 631 while leaving every genuine `success`/`timeout`/
      `monitor_terminated` row unchanged.
- [x] Test coverage check (Method item 6): `dispatch_test.go` already covered
      `recordRunFailure`/`recordCommitFailure`/`completeExecuteBeadJob` in
      isolation from prior stages, but nothing exercised `dispatch()` itself
      for any of the three newly-fixed paths, and `lock.go` had zero tests at
      all before this session — both gaps closed by the tests listed above.

**Session log (2026-07-15):** One real bug (three call sites, same root
shape) confirmed by construction and fixed — the EXECUTE_BEAD half was
explicitly flagged as deferred-to-Stage-7 in Stage 6's log, and the two
warmup-failure siblings were found auditing the rest of `dispatch()` with the
same method. One lower-likelihood but genuine lock-liveness gap in
`lock.go`/`orchestrator.go` also found and fixed. One observability gap
(crashed executions displayed as `success` in the UI/report) found while
tracing `queue.go`'s orphan-recovery placeholder value, fixed alongside.
`queue.go`'s FIFO ordering checked and confirmed intentional, no bug. All
fixes verified: `go build ./...`, `go vet ./...`, `go test ./...` all clean;
new regression tests for every fix, including first-ever tests for
`lock.go`; the UI/report query changes verified directly against a copy of
the live `ratchet-projects/ratchet.db` (execution 631, bead 630 — a real
orphan-recovery event from earlier today). Left uncommitted pending user
review, per standing practice.

## Stage 8 — UI & CLI — AUDITED 2026-07-15

- [x] `internal/ui/handlers.go` — every handler read in full. Confirmed escalation
      detail, requeue, requeue-with-budget, grant-attempts, close are used directly
      today via `curl` against the sanctioned endpoints (no CLI equivalent exists at
      the job/escalation level — only at the project level, where `resume-project`/
      `full-stop-project`/`restart-project`/`rewind-bead` already have CLI
      subcommands). Not treating the missing job-level CLI equivalents as a bug —
      that's the documented, sanctioned workflow — but found two real bugs auditing
      the handlers themselves:

  **Bug 1 — arbitrary file read via `GET /trace?path=<anything>`.**
  `handleTrace` did `os.ReadFile(r.URL.Query().Get("path"))` with zero validation
  and served the content back — `path=/etc/passwd` (or any file the process can
  read) worked. `queries.go` already had a `queryTracePath(ctx, d, execID)` helper
  — resolving a trace path from the DB by execution ID — but it was **dead code,
  never called anywhere**, strongly suggesting the safe by-ID design was the
  original intent and the handler/template just never got wired to it. Default
  bind is `localhost:7474` (safe default, confirmed in `cmd/ratchet/main.go`), but
  `-addr` can point this at any interface with no code-level guard, at which point
  this becomes a real unauthenticated arbitrary-file-disclosure endpoint. Checked
  for a matching XSS risk in the same code path (trace content, raw model output)
  — none found; every template in this package renders through Go's html/template
  default auto-escaping, no `template.HTML` casts anywhere. **FIXED 2026-07-15**:
  route changed from `GET /trace` (query param) to `GET /trace/{id}` (execution
  ID path segment); `handleTrace` now resolves the path server-side via the
  previously-dead `queryTracePath`, so a request can only ever read a path this
  application itself wrote to the `executions` table. `bead_detail.html`'s trace
  link updated to `/trace/{{.ID}}`. Verified live against a copy of the real DB:
  `GET /trace?path=/etc/passwd` now 404s (no route matches), `GET /trace/635`
  serves the real trace content, `GET /trace/9999999` 404s cleanly.

  **Bug 2 — four job-mutation handlers had no status guard, unlike their
  project-level siblings in the same file.** `handleRequeue`,
  `handleRequeuWithBudget`, `handleGrantAttempts`, `handleClose` all did
  `UPDATE handoff_jobs ... WHERE id = ?` with no check that the job was still
  `'escalated'` — while `handleCloseProject`/`handleResumeProject`/
  `handleRemoveProject` in the exact same file already guard on status
  (`WHERE id = ? AND status IN (...)`). `queryEscalatedJobByID` (backing the
  escalation detail page) also has no status filter, so a stale browser tab left
  open on that page, or a duplicate/retried `curl` POST (the actual documented
  workflow), can requeue/close/grant-attempts on a job that's since resolved or
  is currently `'running'` under the orchestrator — racing the orchestrator's own
  writes with zero coordination (`commitAttempt` itself also has no status
  guard). Same "later writer clobbers earlier writer" shape as several bugs
  already fixed earlier in this audit. **FIXED 2026-07-15**: all four now guard
  the status transition (`WHERE id = ? AND status = 'escalated'`, checked via
  `RowsAffected`), returning 409 if the job has moved on. For the two handlers
  with multiple writes (`requeue-with-budget` inserts a new `bead_revisions` row;
  `grant-attempts` bumps `beads.execution_attempts_override`), the whole
  operation is now wrapped in one transaction with the guarded status UPDATE as
  the *final* write, so a conflict rolls back the side effects too instead of
  partially applying them to a job that's moved on — matching the transaction
  pattern the project-level handlers in this same file already used. Tests added
  (`internal/ui/handlers_test.go`, new file — first tests for the `ui` package):
  one success + one conflict case per handler, plus two tests specifically
  confirming the conflict path rolls back the new revision / the attempts
  override rather than partially applying it.
- [x] `internal/ui/queries.go` — every query read in full. All parameterized
      (`?` placeholders throughout) — no SQL injection surface from any
      client-supplied value. No other bugs found.
- [x] `internal/ui/server.go` — routes, template cache, `Run`. All mutating routes
      are POST-only (confirmed via `routes()`) — no GET-triggerable destructive
      action reachable by a crawler or a bare link. No auth on any route (by
      design — single-user local tool), mitigated by the `localhost` default bind;
      noted as a lower-priority hardening item if `-addr` is ever pointed at a
      non-loopback interface, not fixed (out of scope without a stated multi-user
      threat model).
- [x] `cmd/ratchet/main.go` — subcommand dispatch read in full. Confirmed `start`
      runs the orchestrator and UI as goroutines in one process sharing one
      `*db.DB` (Go's `database/sql` is safe for concurrent use; SQLite serializes
      writers) — this resolves what first looked like a "two processes both
      writing to the DB" concern against `orchestrator.go`'s package-doc claim
      that the orchestrator is the only DB writer: it's one process, not two, so
      no cross-process race exists here. The real concurrency risk was Bug 2
      above (two goroutines racing the *same* job row with no coordination), not
      a process-level one. Confirmed project-level CLI/UI parity (resume,
      full-stop, restart, rewind-bead all have both); job-level escalation
      actions are intentionally HTTP-only.
- [x] Test coverage check (Method item 6): `internal/ui` had **zero** test files
      before this session. Now covered for both fixed bugs via
      `internal/ui/handlers_test.go` (11 new tests, first-ever for this package).

## Stage 9 — Shared infrastructure — AUDITED 2026-07-15

- [x] `internal/db/` — `db.go`'s six ad-hoc `migrateX` rename+recreate+copy+drop
      migrations, plus the two `columnMigrations`/`applyTableMigrations` seed
      functions, read in full. **Confirmed no test had ever exercised any migration's
      actual rename+recreate+copy+drop body**: `TestSchemaIdempotent` only opens a
      fresh `:memory:` DB, where `schema.sql` already matches every migration's target
      shape, so every migration's own guard (`if strings.Contains(createSQL, ...) {
      return nil }`) short-circuits as a no-op before the risky part ever runs. Built
      six legacy-shaped fixture DBs (one per migration: `bead_revisions` ×2,
      `test_refinements`, `projects`, `audit_reconcile_rounds`, `adjudications`) and
      ran each migration directly. Five preserve data correctly, including the FK from
      `beads.current_revision_id` surviving the `legacy_alter_table` rename dance.
      **One does intentionally discard data**: `migrateTestRefinementsVerbs` drops any
      row using the old `REFINE_TESTS_A`/`REFINE_TESTS_B` verb names (its own comment
      says so) rather than migrating them forward to the new three-verb names — checked
      this isn't a live-data risk: `REFINE_TESTS_A/B` was introduced and replaced by
      the three-verb design within the same development arc (`82339ca` →`47030cc`),
      before any of the real chess/goban/checkers projects existed. Not fixed (correct,
      intentional, harmless), but now covered — no bug found here, but this was a real
      test-coverage gap for a genuinely risky pattern (schema rewrites on a live DB),
      so all six migrations now have permanent regression tests:
      `internal/db/migrations_test.go` (new file).
- [x] `internal/db/assignments.go` — `checkModelConstraints`'s 5 pairwise rules (doc
      comment above `SetVerbModelAssignment` still says "four" — stale, the 5th
      `REFINE_TESTS_WRITE != REFINE_TESTS_CRITIQUE` rule was added later without
      updating it; cosmetic, not fixed). Confirmed `SetVerbModelAssignment` is dead
      code in production (only called from `db_test.go`; no CLI/UI wiring exists to
      reassign a verb's model after project creation) — its check-then-write is not
      transactional, which would be a real race if ever wired to a concurrent-capable
      caller, but isn't reachable today. Not fixed (nothing to fix without inventing
      the reassignment feature itself), flagged for whoever wires that up.
- [x] `internal/splice/` — read in full. **Confirmed and fixed a real bug**:
      `detectImports` matched any bare `x.Y` selector against its known-package table
      by identifier name alone, with no check for whether `x` was the actual imported
      package or a local variable/parameter that merely shares a package's short name
      (e.g. `url := resp.Header.Get(...)` then `url.Path`-shaped access — extremely
      plausible in the HTTP-handler test code this project's own web-app projects
      exercise). That silently added a spurious import (`"net/url"` in the example),
      which fails to compile with "imported and not used" — an error `write_function`'s
      model has no way to connect back to its own code, since it's never shown an
      import block and never writes one (`Do not include package declarations or
      imports` is a hardcoded tool-description constraint). Reproduced directly before
      fixing. Not yet observed live (`test_refinements` has 86 real `REFINE_TESTS_WRITE`
      rows in the live DB, none from HTTP-handler-testing beads that would trigger this
      shape), same "hasn't bitten yet, but will" bar as Stage 1's stub-purity gap.
      **FIXED 2026-07-15**: added `declaredNames`, which walks the file collecting every
      identifier bound by a function param/result, `var`/`const` decl, `:=`, or range
      clause, and excludes those from `detectImports`'s candidate package matches (a
      package identifier can never simultaneously be locally declared in the same
      scope, so this exclusion is sound, not just heuristic). Verified genuine package
      usage (`url.Parse(...)`) is still detected correctly. Test added:
      `TestSyncImportsIgnoresShadowedLocalVar` (`splice_test.go`).
- [x] `internal/trace/parse.go` + `findings.go` — read in full. No new parser crashes
      or defeat scenarios found beyond the already-known, already-flagged stdout-
      suppression display gap (Stage 5: `GenerateMechanicalFindings`'s "All Commands
      Run" section shows no output for any `ExitCode == 0` command, which is a syntactic
      proxy that a `cmd && echo Pass || echo Fail` self-check defeats since it always
      exits 0). Re-confirmed still present, still **not fixed** — this session
      determined it's a real but narrower exposure than it first sounds: the "Exit
      Criteria" section (which actually feeds the mechanical `declare_success` gate)
      already shows full output unconditionally regardless of exit code; only the
      secondary "All Commands Run" narrative summary — covering ad-hoc, non-criterion
      commands — suppresses it. Left as an open design question for the user (verbosity
      vs. visibility trade-off, not a single obviously-correct mechanical fix) rather
      than silently deciding — see session log.
      Robustness check: `bufio.Scanner`'s 1MB per-line buffer would silently truncate-
      as-if-EOF on a single trace line exceeding that (`scanner.Err()` is never checked)
      — plausible for a project like `png-stego` printing a large base64 blob on one
      line with no embedded newlines. Not fixed: this is the same graceful-truncation
      behavior the whole format already relies on for a genuinely killed/timed-out
      execution, so treating an oversized line the same way is consistent, not a new
      failure mode — flagged for visibility, not treated as a bug.
- [x] `internal/report/bead.go` + `project.go` — read in full, including every query.
      **Confirmed and fixed a real bug**: `lastTestResult` labeled any "Last run:" line
      not ending in `exit 0` as a definitive `FAIL`, including the truncated case
      (execution killed/timed out mid-command, so its exit criterion's own run never
      completed — `trace.parseResultLines` defaults `ExitRaw` to `"(truncated)"` when no
      `exit: ` line was ever seen, per `TestParse_Truncated`). That mislabeled a
      killed-mid-test execution as a definitive test failure in the human-facing bead
      report, when the true state is unknown. **FIXED 2026-07-15**: added a check for
      the `(truncated)` suffix before falling through to `FAIL`, returning
      `"unknown (truncated)"` instead. Tests added: `internal/report/bead_test.go` (new
      file — first tests for this package) covering the truncated case plus the
      existing PASS/FAIL/not-run paths.
      Investigated and **ruled out** (verified against real `project_id=98` data before
      concluding, per standing practice) a suspected double-count in `WriteProject`'s
      header line: `nEscalated` (from `handoff_jobs.status='escalated'`) looked like it
      could double-count against `nStopped`/`nNeverRan` (from `beads.status`) for the
      same bead. Confirmed this can't happen: `fullStopProject` only flips
      `beads.status` to `full_stopped` for beads still `pending`; the one bead whose
      escalation triggered the stop stays `status='executing'` forever (its own status
      is never touched), so it's exclusively counted via `nEscalated`, never double
      counted. No bug.
- [x] `internal/guidance/` — read in full (86 lines). No bug found. Confirmed
      `survey_spec.go` correctly uses the DB-stored `project.Language` (not filesystem
      `Detect`) for the one verb that runs before any file exists on disk; confirmed
      `scaffoldGoProject` (which writes `go.mod`) runs synchronously inside
      `VERIFY_MANIFEST` — before `CERTIFY_MANIFEST` and everything after it — so every
      other verb's `InjectForVerbPath` filesystem-based `Detect` call correctly finds
      `go.mod` already on disk by the time it runs. Zero test coverage before and after
      (no bug found, same precedent as Stage 5's `analyze_execution.go`/
      `compress_analysis.go` — tests added alongside fixes, not speculatively).
- [x] `internal/ollama/client.go` — read in full. **Confirmed and fixed a severe,
      broad-blast-radius bug**: `ExtractJSON` found the end of a fenced JSON block by
      searching for the *last* `` ``` `` in the entire raw string. Any trailing
      commentary after the real closing fence that itself contained a code fence —
      e.g. the model quoting a failing test or a code snippet as part of its own
      explanation, entirely plausible model behavior — made that trailing fence "win":
      everything up to it, including the prose/code in between, got swept into what
      was returned as "JSON" and failed `json.Unmarshal`. `ExtractJSON` is called from
      essentially every JSON-handoff verb in the system (grep: 20+ call sites across
      `decompose_spec.go`, `reconcile_decomposition.go`, `adjudicate_next_execution.go`,
      `refine_tests.go`, `survey_spec.go`, etc.) — this is exactly the class of bug the
      whole audit initiative exists to catch: a framework parsing defect that would
      have burned attempts and potentially escalated jobs as if the model had produced
      bad output, when the model's output was fine. Reproduced directly before fixing.
      **FIXED 2026-07-15**: replaced the fence-marker search with a structural scan —
      find the first `{`/`[` (after skipping the opening fence's marker line, if
      fenced) and walk forward tracking brace/bracket depth while honoring quoted
      strings and escapes, returning the matching close. This sidesteps the ambiguity
      in both directions: a JSON string value containing `` ``` `` no longer risks
      truncating the payload early, and trailing prose/fences after the real JSON no
      longer risk swallowing it whole — markdown fences are never searched for as the
      terminator at all, only the real JSON structure is. Verified against every real
      historical `handoff_attempts` row in the live DB with `validation_result='valid'`
      (1,671 rows): 0 regressions — every one still parses correctly under the new
      implementation. Tests added: `internal/ollama/client_test.go` (new file — first
      tests for this package), including the exact trailing-fence defeat scenario,
      embedded-backtick-in-string-value, nested braces, array-top-level, and truncated-
      input cases.

**Session log (2026-07-15):** Three real, confirmed-by-reproduction bugs found and
fixed: the `splice` import-shadowing false-positive, the `report` truncated-run
mislabeled as FAIL, and the `ollama.ExtractJSON` trailing-fence defeat (the most
severe of the three — broadest blast radius of any bug found in this audit, since
nearly every verb in the system routes through it). One long-standing, already-known
design question (Stage 5's stdout-suppression gap) was re-confirmed but deliberately
left to the user rather than silently resolved, since the right trade-off (show more
output vs. keep the narrative summary compact) isn't dictated by any spec doc. One
real test-coverage gap closed without an accompanying bug (`internal/db` migrations).
One suspected bug (`report` double-counting) was investigated and ruled out against
real data rather than assumed. All fixes verified: `go build ./...` / `go vet ./...` /
`go test ./...` clean, plus the `ollama` fix specifically replayed against 1,671 real
historical model outputs from the live `ratchet-projects/ratchet.db`. Left uncommitted
pending user review, per standing practice.

## Stage 10 — Retroactive check across past "COMPLETE" projects — DONE 2026-07-14 (folded into Stage 3), extended 2026-07-15 for Stage 9

Not a code-audit stage — a data-audit stage, only meaningful once Stage 3's fix
landed. Performed as part of Stage 3's own retroactive-check item rather than as a
separate pass — see Stage 3 above for the full findings. Corrected the project list
below against the live DB first: `othello-v3-e` (project 47) is `full_stopped`, not
`COMPLETE` — only `othello-v3-f` (project 48) qualifies. Full `COMPLETE` list per
`sqlite3 ratchet.db "SELECT id,label,status FROM projects WHERE status='complete';"`:
othello-v3-f (48), tasklist-v1 (49), chess-v1 (87), chess-v3 (89), goban-v2 (91).

- [x] chess-v3 (project 89) — no beads share a test file; not exposed to this bug.
- [x] goban-v2 (project 91) — **corrupted**: beads 565/566 clobbered by bead 567 in
      shared `game_test.go`; both beads' own exit criteria now fail. See Stage 3.
- [x] othello-v3-f (project 48) — beads share `game_test.go`/`handlers_test.go`, but
      wrote them via `EXECUTE_BEAD` (test-first `REFINE_TESTS_WRITE` mode wasn't used
      for these beads) — a different write path, unaffected. All expected functions
      from every sharing bead confirmed present on disk.
- [x] tasklist-v1 (project 49) — no beads share a test file; not exposed to this bug.
- [x] chess-v1 (project 87) — **corrupted**: bead 536 clobbered by bead 537 in shared
      `ai_test.go`; bead 536's own exit criterion now fails. See Stage 3.

**Follow-up 2026-07-15 — retroactive check for the three Stage 9 fixes.** Extended
this stage to cross-check `ollama.ExtractJSON`'s trailing-fence bug, `splice`'s
import-shadowing bug, and `report.lastTestResult`'s truncated-mislabel bug against
real historical data in `ratchet-projects/ratchet.db` and its trace/log/report
artifacts, rather than treating Stage 9 as closed on code-reading and reproduction
alone.

- [x] **`ollama.ExtractJSON`** — replayed old vs. new implementation against every
      `handoff_attempts.raw_output` row (1,527 rows) belonging to the 11 verbs whose
      `Validate` actually pipes `rawOutput` through `ExtractJSON` (excludes
      `VERIFY_MANIFEST` and `REFINE_TESTS_WRITE`, both of which re-marshal a Go
      struct server-side and `json.Unmarshal` it directly — confirmed by reading
      each `Validate` body, not assumed; an initial unscoped pass had wrongly
      flagged a `REFINE_TESTS_WRITE` row as "rescued by the fix" before this
      correction). **Zero real historical outcomes changed** either direction (no
      row rescued, no regression) — the bug is real and reproducible but never
      actually fired in production to date.
- [x] **`splice` import-shadowing** — grepped the real `ratchet.log` orchestrator
      log for every `REFINE_TESTS_WRITE: compile failed` line (14 total) looking
      for "imported and not used" (the bug's signature). Found 2 real instances
      (beads 570, 571, both 2026-07-12, both in goban-v2) — but traced both to a
      *different*, already-fixed root cause: pre-Stage-3, `splice.Replace` never
      called `syncImports` at all, so a revision that stopped using a package left
      its now-stale import in place. Both predate Stage 3's 2026-07-14 fix and
      self-healed on the next turn once redone under fixed code; neither is
      explained by (or was still live for) this session's shadowing fix. The other
      2 `REFINE_TESTS_WRITE` compile failures found in the same grep pass
      (beads 557, 629 — "declared and not used") are genuine model bugs, unrelated
      to `splice` entirely, correctly caught by the compile gate. One further
      escalation found in the same log sweep (bead 583, `TestNewGame redeclared`)
      was already root-caused and fixed same-day in commit `4fafc23`
      (mechanically-owned `api_check_test.go` renamed to `do_not_use_this_test.go`
      to stop exactly this class of model/framework name collision) — not a new
      finding. No live evidence of the variable-shadowing false-positive this
      session actually fixed.
- [x] **`report.lastTestResult`** truncated-mislabel — scanned all 115 real
      `traces/bead-*-report.md` files on disk across every `ratchet-projects/*`
      project for an Attempt-History row where Termination was
      `timeout`/`monitor_terminated`/`monitor_force_killed` and Last Test Result
      was `FAIL` (38 such rows). Cross-checked each against the underlying
      `analyses.mechanical_findings` text in the live DB (28 of the 38 execution
      IDs still resolve there; the other 10 belong to superseded report files from
      the same folder path being reused across earlier dev-iteration project rows,
      not evidence of anything). **Zero** had a `(truncated)` "Last run:" line —
      every one of the 28 verified `FAIL` labels was a genuine completed exit
      criterion (`exit exit status 1`) in an execution that was killed for
      unrelated reasons afterward. Bug confirmed real and reproducible, never yet
      triggered in a real report.

All three Stage 9 fixes: real, reproducible, currently latent in production —
consistent with this audit's recurring "hasn't bitten yet, but will" pattern (same
bar as Stage 1's stub-purity gap). No retroactive remediation needed for any of the
115 existing bead reports or historical handoff attempts.

---

## Log

- 2026-07-14: file created, staged, not yet started. Agreed with user: deep audit
  (verify by reproduction per the Method section above), staged by FSM verb
  boundaries. checkers-v8 (project 98) still running unattended in parallel;
  this audit is separate work, not blocked on it finishing.
- 2026-07-14: Stage 3 done. Fixed the known test-clobbering bug plus two more
  bugs of the same shape found auditing `internal/splice/splice.go`
  (`Replace` not syncing imports; `detectImports` whitelist gaps) — all three
  fixed and tested. Retroactive check (folded in Stage 10) found confirmed,
  live corruption in 2 of 5 `COMPLETE` projects (chess-v1 bead 536, goban-v2
  beads 565/566) predating the fix — user decided to leave both as-is
  (archival, no remediation). See `[[project_audit_stage3]]` memory.
- 2026-07-14: Stage 4 done. Two real bugs found and confirmed by reproduction
  (real compiled binary + real DB for one; standalone unit test for the other;
  throwaway test files deleted after verification, no permanent changes made):
  (1) `dispatch.go`'s unconditional `status='complete'` write after
  `RunExecutionWindow` returns clobbers `handleInfraFailure`'s own
  pending/escalated status write, silently stranding any bead hit by an infra
  failure (spans `execution/window.go` and `orchestrator/dispatch.go`, cross-
  referenced into Stage 7); (2) a pure-test bead (output_files entirely
  `*_test.go`, 72 exist in the live DB) retrying with its test file already on
  disk trips `isTestsLockedMode`, producing a self-contradictory prompt and an
  empty `expectedFiles` slice that panics `buildMissingPathWarning` if the model
  ever calls `write_file` without a path. Also root-caused the standing "MONITOR
  didn't fire on a repeated self-check" gap: the two documented loop-pattern
  rules have zero mechanical backstop, purely model-narrative-driven (same class
  as Stage 5's `declare_success` finding). User chose "fix all 3 now" — all
  three fixed and tested same day (`go build ./...`, `go vet ./...`, `go test
  ./...` and the real-subprocess smoke tests all clean), with new regression
  tests for each including two functions (`handleInfraFailure`,
  `completeExecuteBeadJob`) that had zero prior coverage. Left uncommitted
  pending user review, per standing practice. See `[[project_audit_stage4]]`
  memory.
- 2026-07-14: Stage 6 done. One severe bug found and fixed, confirmed by
  reproduction (unit tests exercising the exact revision-1/revision-2 split
  found live in `ratchet-projects/ratchet.db`, plus the CHECK-constraint
  migration verified against a real DB copy): `rewind-bead` reverted a bead's
  entire spec — including `output_files`/`exit_criteria`, not just prose — to
  `DECOMPOSE_SPEC`'s raw revision 1, discarding permanent structural fixes
  `RECONCILE_DECOMPOSITION`/`ADJUDICATE_NEXT_EXECUTION` had applied since (a
  missing test file added to `output_files`, confirmed live for beads
  261/264). Combined with `dispatch.go` treating every `handler.Run` error as
  a no-strike-counted, no-cap infra retry, a bead rewound after losing its
  test-file declaration would loop forever on `REFINE_TESTS_WRITE`'s "no test
  file paths" error, invisibly, with no escalation. User chose "prose from
  revision 1, output_files/exit_criteria from the pre-rewind current
  revision" for the rewind design question and "minimal" (strike/tolerance
  parity with `recordCommitFailure`, not a full transient-vs-deterministic
  error taxonomy) for the dispatch fix. Both fixed and tested
  (`go build ./...`, `go vet ./...`, `go test ./...` all clean); new coverage
  for a file (`rewind.go`) that had zero tests before this session. Left
  uncommitted pending user review, per standing practice. See
  `[[project_audit_stage6]]` memory.
- 2026-07-15: Stage 7 done. Confirmed and fixed the bug Stage 6 explicitly
  deferred here: three failure paths in `dispatch.go` (model-lookup,
  Ollama-warmup, and `EXECUTE_BEAD`'s `RunExecutionWindow` error) had zero
  strike/tolerance accounting — a persistent, non-transient failure in any of
  them retried forever with no attempt recorded and no escalation, on the
  orchestrator's single execution slot. All three now route through
  `recordRunFailure`. Also found and fixed a lower-likelihood but genuine gap
  in `lock.go`: `runHeartbeat` never checked whether it still held the lock,
  so a >60s scheduler stall on a live process (not a crash) could let a
  second orchestrator instance start concurrently — added lock-loss detection
  that stops the main loop. Tracing `queue.go`'s `recoverOrphanedExecutions`
  surfaced one more real gap outside this stage's own files: the UI and
  report layers displayed a crashed/orphaned execution's placeholder
  `termination_cause='success'` at face value, indistinguishable from a real
  completion — fixed by checking `infra_failure` in both display queries.
  `queue.go`'s FIFO-by-`created_at` ordering was checked against `rewind-bead`
  and confirmed intentional, no bug. All fixes verified (`go build ./...`,
  `go vet ./...`, `go test ./...` all clean; UI/report query changes checked
  directly against a copy of the live `ratchet-projects/ratchet.db`). New
  tests for every fix, including first-ever coverage for `lock.go`. Left
  uncommitted pending user review, per standing practice. See
  `[[project_audit_stage7]]` memory.
- 2026-07-15: Stage 8 done. Two real bugs found in `internal/ui/handlers.go`
  and fixed: (1) `GET /trace?path=` was an unauthenticated arbitrary file
  read (no validation on the client-supplied path) — a dead, never-called
  `queryTracePath` helper already existed for the safe by-execution-ID design,
  suggesting this was an unfinished wiring rather than an intentional
  trade-off; fixed by switching the route to `GET /trace/{id}` and resolving
  the path server-side. (2) `handleRequeue`/`handleRequeuWithBudget`/
  `handleGrantAttempts`/`handleClose` had no status guard before mutating a
  job — unlike the project-level handlers in the same file, which already
  guard on status — so a stale escalation page or a duplicate/retried `curl`
  POST (the documented actual workflow) could requeue/close/grant-attempts on
  a job that had already resolved or was currently `running` under the
  orchestrator, racing its writes with zero coordination; fixed by adding a
  `WHERE status = 'escalated'` guard (checked via `RowsAffected`, 409 on
  conflict), wrapping the two multi-write handlers in a transaction so a
  conflict rolls back the side effects too. Also confirmed: no SQL injection
  surface in `queries.go` (fully parameterized); all mutating routes are
  POST-only, no GET-triggerable destructive action; `cmd/ratchet start` runs
  orchestrator+UI as goroutines in one process sharing one `*db.DB` (not two
  processes — resolves what first looked like a cross-process DB-writer
  concern); project-level CLI/UI parity confirmed, job-level escalation
  actions are intentionally HTTP-only. All fixes verified: `go build ./...`,
  `go vet ./...`, `go test ./...` clean; live smoke test against a copy of
  the real `ratchet-projects/ratchet.db` (old vulnerable route now 404s, new
  by-ID route serves real trace content, bead-detail page links correctly).
  New tests: `internal/ui/handlers_test.go`, first-ever coverage for the `ui`
  package (11 tests). Left uncommitted pending user review, per standing
  practice. See `[[project_audit_stage8]]` memory.
