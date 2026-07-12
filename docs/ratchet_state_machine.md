# Ratchet state machine

Four diagrams, from outermost to innermost:

1. **Project status** — the four values of `projects.status`.
2. **Bootstrap** — runs once per project, before any bead executes.
3. **Per-bead pipeline** — the loop every bead goes through; this is where most of the complexity lives.
4. **Generic job status** — the low-level `handoff_jobs.status` FSM that every verb call goes through underneath diagrams 2 and 3.

Render with a Mermaid-capable viewer (VS Code preview, GitHub, mermaid.live).

## 1. Project status

`projects.status CHECK IN ('active', 'full_stopped', 'complete', 'paused')` — schema.sql:10

```mermaid
stateDiagram-v2
    [*] --> active : new-project
    active --> paused : RECONCILE_DECOMPOSITION converges<br/>with pause_after_reconcile flag set<br/>(reconcile_decomposition.go:191)
    paused --> active : resume-project CLI<br/>(re-dispatches bead 1 only — resume.go:20)
    active --> full_stopped : full_stop decision on any bead (Diagram 3),<br/>or 5 consecutive CERTIFY_MANIFEST rejections,<br/>or full-stop-project CLI
    active --> complete : declare_success on the last<br/>remaining pending bead (Diagram 3)
    full_stopped --> [*]
    complete --> [*]

    note right of active
      Diagrams 2 and 3 both run
      while status = active.
      Diagram 2 runs once, first;
      Diagram 3 repeats per bead.
    end note
```

![Project status diagram](diagrams/1_project_status.png)

## 2. Bootstrap (runs once, before bead 1)

All transitions are automatic (`Commit()` chaining one job into the next) except the two branch points marked with a verb's decision field.

```mermaid
stateDiagram-v2
    [*] --> SURVEY_SPEC
    SURVEY_SPEC --> VERIFY_MANIFEST : unconditional
    VERIFY_MANIFEST --> CERTIFY_MANIFEST : unconditional<br/>(model-free verb)
    CERTIFY_MANIFEST --> DECOMPOSE_SPEC : final_decision = approve
    CERTIFY_MANIFEST --> SURVEY_SPEC : final_decision = reject<br/>(reject count < 5)
    CERTIFY_MANIFEST --> BOOTSTRAP_FAILED : final_decision = reject<br/>(reject count >= 5)
    DECOMPOSE_SPEC --> AUDIT_DECOMPOSITION : unconditional
    AUDIT_DECOMPOSITION --> DISPATCH_BEAD_1 : overall_verdict = no_issues
    AUDIT_DECOMPOSITION --> RECONCILE_DECOMPOSITION : overall_verdict = issues_found
    RECONCILE_DECOMPOSITION --> AUDIT_DECOMPOSITION : outcome = disagreed_continuing<br/>(round < audit_reconcile_round_cap, default 2)
    RECONCILE_DECOMPOSITION --> RECONCILE_ESCALATED : outcome = escalated<br/>(round >= cap)
    RECONCILE_DECOMPOSITION --> DISPATCH_BEAD_1 : outcome = converged,<br/>pause_after_reconcile = false
    RECONCILE_DECOMPOSITION --> PROJECT_PAUSED : outcome = converged,<br/>pause_after_reconcile = true

    BOOTSTRAP_FAILED --> [*] : project.status = full_stopped
    RECONCILE_ESCALATED --> [*] : this job's status = escalated<br/>(human review)
    PROJECT_PAUSED --> [*] : project.status = paused
    DISPATCH_BEAD_1 --> [*] : bead 1 enters Diagram 3
```

![Bootstrap diagram](diagrams/2_bootstrap.png)

Note: `AUDIT_DECOMPOSITION` with `no_issues` skips `RECONCILE_DECOMPOSITION` entirely — reconcile only runs when audit found something to fix.

## 3. Per-bead pipeline

Outer ring is `beads.status CHECK IN ('pending', 'executing', 'succeeded', 'full_stopped')` — schema.sql:38. Everything inside `executing` is the verb chain for one bead; `beads.status` itself doesn't change while looping inside that box (it flips `pending → executing` each time a fresh `EXECUTE_BEAD` job is actually claimed, and back to `pending` between an ADJUDICATE retry decision and the next claim — see `window.go:115` and the `execute_as_is`/`execute_revised`/`test_reject` branches).

```mermaid
stateDiagram-v2
    [*] --> pending : bead row created by<br/>DECOMPOSE_SPEC / RECONCILE_DECOMPOSITION

    pending --> executing : enqueueBeadExecution picks the first verb<br/>from output_files: any *_test.go present?<br/>yes -> REFINE_TESTS_WRITE, no -> EXECUTE_BEAD<br/>(inputs.go:332)

    state executing {
        [*] --> REFINE_TESTS_WRITE : REFINE_TESTS mode
        [*] --> EXECUTE_BEAD : test-first mode

        REFINE_TESTS_WRITE --> REFINE_TESTS_CRITIQUE : compiles OK
        REFINE_TESTS_WRITE --> ESCALATED : still fails to compile<br/>after internal retries (refine_tests.go:525)
        REFINE_TESTS_CRITIQUE --> REFINE_TESTS_JUDGE
        REFINE_TESTS_JUDGE --> EXECUTE_BEAD : decision = approved
        REFINE_TESTS_JUDGE --> REFINE_TESTS_WRITE : decision = revise<br/>(next cycle <= refinementCycleCap, 5)
        REFINE_TESTS_JUDGE --> ESCALATED : decision = revise<br/>(next cycle > cap)

        EXECUTE_BEAD --> EXECUTE_BEAD : infra_failure at startup<br/>(retried, cap 3 consecutive)
        EXECUTE_BEAD --> ESCALATED : infra_failure x3 consecutive<br/>(window.go:293)
        EXECUTE_BEAD --> ANALYZE_EXECUTION : termination_cause recorded<br/>(success / timeout /<br/>monitor_terminated / monitor_force_killed)

        ANALYZE_EXECUTION --> COMPRESS_ANALYSIS
        COMPRESS_ANALYSIS --> ADJUDICATE_NEXT_EXECUTION

        ADJUDICATE_NEXT_EXECUTION --> EXECUTE_BEAD : execute_as_is (under attempt cap)
        ADJUDICATE_NEXT_EXECUTION --> EXECUTE_BEAD : execute_revised<br/>(new bead_revision, under attempt cap)
        ADJUDICATE_NEXT_EXECUTION --> EXECUTE_BEAD : test_reject<br/>(test-first mode only:<br/>deletes test files, revises spec,<br/>under attempt cap)
        ADJUDICATE_NEXT_EXECUTION --> REFINE_TESTS_JUDGE : re_refine<br/>(REFINE_TESTS mode only: injects<br/>guidance as CRITIQUE input, grants a<br/>fresh attempt budget, bypasses attempt cap)
        ADJUDICATE_NEXT_EXECUTION --> ESCALATED : execute_as_is / execute_revised /<br/>test_reject, attempts >= max_execution_attempts
        ADJUDICATE_NEXT_EXECUTION --> ESCALATED : re_refine, next cycle > refinementCycleCap
        ADJUDICATE_NEXT_EXECUTION --> [*] : declare_success
        ADJUDICATE_NEXT_EXECUTION --> [*] : full_stop
    }

    executing --> succeeded : declare_success
    executing --> full_stopped : full_stop
    executing --> pending : rewind-bead CLI<br/>(spec reset to revision 1, test files<br/>deleted, impl files stubbed, fresh<br/>attempt budget, restarts at<br/>REFINE_TESTS_WRITE cycle 1)<br/>callable from ESCALATED too

    succeeded --> [*] : REVISE_PENDING revises other pending<br/>specs, then dispatches the next pending<br/>bead into its own Diagram 3<br/>(or project.status = complete if none left)
    full_stopped --> [*] : cascades every later pending<br/>bead straight to full_stopped too
```

![Per-bead pipeline diagram](diagrams/3_bead_pipeline.png)

**`MONITOR_EXECUTION` is not in this chain.** It's a parallel watchdog subprocess (`ratchet monitor`, spawned alongside `execute-bead` by `RunExecutionWindow`, `window.go:138`) that polls the trace file, asks its own model FIRE/NO_FIRE, and can SIGTERM/SIGKILL the running `EXECUTE_BEAD` process — which is how `termination_cause` becomes `monitor_terminated` or `monitor_force_killed`. It has no `handoff_jobs` row of its own.

## 4. Generic job status (underneath every verb above)

`handoff_jobs.status CHECK IN ('pending', 'running', 'failed_retry', 'escalated', 'complete')` — schema.sql:135. `escalated` and `complete` are the only terminal values for a job row.

```mermaid
stateDiagram-v2
    [*] --> pending : job inserted by the<br/>previous verb's Commit()
    pending --> running : claimNextJob (atomic claim,<br/>queue.go:55)
    running --> complete : handler.Validate succeeds
    running --> failed_retry : handler.Validate fails,<br/>strikes+1 <= tolerance (flat 2, all verbs)
    running --> escalated : handler.Validate fails,<br/>strikes+1 > tolerance
    running --> failed_retry : orchestrator restarts mid-job<br/>(resetStaleRunning, queue.go:157)
    failed_retry --> running : reclaimed by claimNextJob<br/>(same WHERE clause as pending)
    complete --> [*]
    escalated --> [*] : human review required —<br/>UI requeue or rewind-bead
```

![Generic job status diagram](diagrams/4_job_status.png)

`EXECUTE_BEAD` is special-cased in `dispatch.go:26`: it doesn't go through `Run`/`Validate`/`Commit` like other verbs, it calls `RunExecutionWindow` directly, and it doesn't accumulate strikes the same way — its own retry/escalation logic (infra-failure cap, monitor kill) lives inside `window.go` and is drawn separately in Diagram 3. `VERIFY_MANIFEST` is the only other verb skipped for model warmup (it's model-free).

## Escalation points (all 8, i.e. every way a job can reach `escalated` / `full_stopped` outside a normal decision)

| # | Where | Trigger | File:line |
|---|---|---|---|
| 1 | `RECONCILE_DECOMPOSITION` | audit/reconcile round cap reached with an unresolved disagreement | `reconcile_decomposition.go:~204` |
| 2 | `CERTIFY_MANIFEST` | 5 consecutive rejections | `certify_manifest.go:203` |
| 3 | `REFINE_TESTS_WRITE` | test file still fails to compile after internal retries | `refine_tests.go:525` |
| 4 | `REFINE_TESTS_JUDGE` | revise requested after `refinementCycleCap` (5) reached | `refine_tests.go:702` |
| 5 | `EXECUTE_BEAD` (via `window.go`) | 3 consecutive infra-failure crashes at startup | `window.go:293` |
| 6 | `ADJUDICATE_NEXT_EXECUTION` | `execute_as_is`/`execute_revised`/`test_reject` at `max_execution_attempts` | `adjudicate_next_execution.go` `atExecutionCap` |
| 7 | `ADJUDICATE_NEXT_EXECUTION` | `re_refine` past `refinementCycleCap` | `adjudicate_next_execution.go:~884` |
| 8 | any verb (generic) | strikes exceed flat tolerance of 2 on malformed/invalid output | `orchestrator/dispatch.go:~103` |

`rewind-bead` is the sanctioned recovery path for any of these while the bead hasn't succeeded — it resets to `REFINE_TESTS_WRITE` cycle 1 and stubs impl files, so it always discards whatever implementation exists on disk. `resume-project` only ever re-dispatches bead 1, and `full-stop-project` is the manual equivalent of escalation path #6/7 applied project-wide.