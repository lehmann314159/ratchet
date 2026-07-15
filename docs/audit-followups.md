# Audit follow-ups

Items found during the Stage 1–10 audit (`docs/audit-checklist.md`) and
deliberately left open — either by explicit user decision, or because they were
low-severity/low-probability enough that fixing them wasn't worth the churn at
the time. None of these are blocking; this is a "come back to this later" list,
not an active bug list. Cross-reference the stage number back into
`docs/audit-checklist.md` for the full original finding and surrounding context.

## Decided — no action needed unless the decision changes

1. **2 of 5 `COMPLETE` projects have confirmed historical data corruption**
   (chess-v1 bead 536's `ai_test.go`; goban-v2 beads 565/566's `game_test.go`,
   both clobbered pre-Stage-3-fix). Stage 3. User explicitly chose to leave both
   as archival — no manual test-file restoration, no DB patches.
2. **`GenerateMechanicalFindings`'s "All Commands Run" section hides
   stdout/stderr for any command exiting 0**, so an ad-hoc
   `cmd && echo Pass || echo Fail` self-check can hide its own "Fail" text from
   the analyst narrative. Stage 5 / 9. User chose "leave as tracked open item"
   when asked directly — the mechanical `declare_success` gate isn't affected
   (a separate section always shows exit-criteria output regardless of exit
   code), only narrative context for other decisions (execute_as_is /
   execute_revised / full_stop). If revisited, the three options considered
   were: (a) always show output regardless of exit code — simplest, more
   verbose; (b) detect the antipattern surgically — scan short stdout for a
   standalone "fail"-shaped token, force it to show even on exit 0 — more
   targeted, needs a defined heuristic; (c) leave as-is (chosen).
3. **No auth on any UI route.** Stage 8. By design (single-user local tool),
   mitigated by the `localhost`-only default bind. Re-confirmed 2026-07-15:
   user chose to leave as documented, no code change — no stated multi-user
   threat model exists to design real auth (or even a bind-address warning)
   against yet.

## Resolved 2026-07-15

Items 3–10 and 12–13 below (original follow-up numbering preserved) were all
fixed in this session — re-verified against current code first, fix approach
agreed with the user before applying (two required an explicit design choice:
item 5's advisory-vs-command wording, item 6's new termination_cause enum
value), then implemented with regression tests. `go build -o ratchet
./cmd/ratchet/`, `go vet ./...`, `go test ./...` all clean. Left uncommitted
pending user review, per standing practice. One-line notes left in the
corresponding `docs/audit-checklist.md` stage sections.

3. ~~`fullStopProject` never resets a bead stuck mid-`executing` to
   `full_stopped`.~~ Stage 6. Fixed: bead-reset `UPDATE` now matches
   `status IN ('pending', 'executing')`.
4. ~~`revisionMap` (REVISE_PENDING) is keyed by bead title, no uniqueness
   check.~~ Stage 6. Fixed at the root: `DecomposeSpec.Validate` now rejects a
   DECOMPOSE output containing two beads with the same title (every bead title
   in the system originates from this one call site, so this also closes the
   identical gap in AUDIT/RECONCILE's own title-keyed maps).
5. ~~`recurringTestFailureNote` concludes "assertions are logically
   impossible" on any recurring failure, without checking why.~~ Stage 6.
   Softened from an unconditional "Action: issue decision=re_refine" command
   to an advisory note: still surfaces the mechanical fact (same subtest
   failing across the last two revising attempts) prominently, but now
   requires the model to name why no correct implementation could satisfy the
   assertion before choosing re_refine, and explicitly permits execute_revised
   if it can name an untried implementation fix — aligned with the softer
   framing the adjacent general REFINE_TESTS note already used.
6. ~~No-write-warning retry can mislabel a zero-output execution as
   `termination_cause='success'`.~~ Stage 4. Fixed: added a new
   `termination_cause='no_write'` enum value (schema migration, same pattern
   as the `rewound_at`/`infra_failure` precedents) written when the warning
   already fired once and the model still produces zero tool calls on the very
   next turn. Confirmed cosmetic before and after — `ANALYZE_EXECUTION`'s own
   file-stat check was never affected — but the trace/UI/report label is now
   accurate instead of misleading.
7. ~~`checkUndeclaredFiles`'s caller swallows a DB error from
   `loadCurrentBeads`.~~ Stage 5. Fixed: a `loadCurrentBeads` error now logs
   and skips the undeclared-files check entirely, instead of silently running
   it against an empty bead list (which would have flagged every file in the
   project folder as undeclared).
8. ~~`bufio.Scanner`'s 1MB per-line trace buffer silently truncates-as-EOF on
   an oversized single line.~~ Stage 9. Visibility fix only, no behavior
   change (per the original finding's own conclusion that the graceful-
   truncation behavior itself is fine): `trace.Parse` now checks
   `scanner.Err()` after the scan loop and `slog.Warn`s if set.
9. ~~`goFixBeadSpec`'s vacuous-pass guard picks the first `*_test.go` file~~
   when a bead owns more than one and the exit criterion lacks `-run`. Stage 2.
   Fixed: the guard now greps every owned test file
   (`grep -q 'func Test' file1 file2 ...`, which grep natively evaluates as
   "match in any listed file") instead of only the first, since a bead with
   real tests in a second file was at risk of a false "nothing written"
   verdict.
10. ~~REFINE_TESTS_CRITIQUE's summary template (`prompts.go:364`) has a
    cosmetic fill-in-the-blank bug.~~ Stage 3. Fixed: reworded the JSON
    example so it states the two outcome branches as explicit conditional
    instructions instead of a single fill-in-the-blank sentence a model could
    echo verbatim.
12. ~~`checkModelConstraints`'s doc comment says "four" constraints; there are
    actually five.~~ Stage 9. Fixed: updated both doc comments
    (`SetVerbModelAssignment`, `SeedVerbModelAssignments`) to five and listed
    the `REFINE_TESTS_WRITE != REFINE_TESTS_CRITIQUE` rule.
13. ~~`SetVerbModelAssignment` is dead code with a non-atomic check-then-write.~~
    Stage 9. Fixed preemptively per the original note's own recommendation:
    the read-check-write now runs inside a single transaction.
