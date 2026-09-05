# `format:"json"` breaks native tool-calling on non-thinking models

Root-caused 2026-08-31. Phase 1 fix landed on `loop-mode`.

## Symptom

`ChatWithTools` (`internal/ollama/client.go`) hardcoded `format:"json"` on every
turn. On a **tool turn** — one where the loop wants a `write_function` /
`run_go_snippet` tool call, not a final answer — the loose `"json"` grammar is
incompatible with native tool-calling for any model that does not emit a
separate thinking pass first:

| Config | Behavior on REFINE_TESTS_WRITE tool turn |
| --- | --- |
| `gemma4:31b`, thinking ON | Works. Thinks, then emits a clean `tool_call` separately from satisfying the JSON grammar. The only config the original "coexist without interference" claim was validated against. |
| `gemma4:31b`, `disableThink()` | Emits a ReAct `{"action":...,"action_input":...}` blob as **content**, zero tool calls → completeness gate escalates. |
| `devstral-small-2:latest` (non-reasoning) | Emits the exact `write_function` payload `{"name":"TestParser","body":"func TestParser..."}` as **content**, zero tool calls → escalates. Terminates cleanly (~2.5K tokens), no runaway. |

The grammar forces every sampled token toward JSON-syntactic validity. A model
that would otherwise emit a provider-native `tool_calls` structure instead
satisfies the grammar the only other way it knows: by serializing the call as a
JSON string in the content channel. A reasoning model sidesteps this because its
tool call is emitted after the thinking phase, in a channel the grammar mask
does not constrain the same way.

## Phase 1 fix (WRITE only)

`Options.OmitFormat bool` — when set, `ChatWithTools` drops the `format` field
from the request entirely. Applied at `internal/verbs/refine_tests.go` in the
REFINE_TESTS_WRITE tool loop.

WRITE is the safe case: its real output is the `write_function` tool calls, and
its final turn's `Content` is taken verbatim as a free-text `summary` string
(`json.Marshal(RefineTestsWriteOutput{Summary})` is built in Go). No
model-emitted JSON is ever parsed, so the grammar bought nothing there while
actively breaking the tool loop for non-reasoning models.

## Phase 1b (EXECUTE_BEAD)

Same `OmitFormat` change at `internal/execution/bead.go`. EXECUTE_BEAD is a pure
tool-calling loop (`write_file` / `read_file` / `run_command`); `msg.Content` is
only streamed to the trace and checked for emptiness, never parsed as JSON. Under
the default `format:"json"`, gemma4:31b ran its entire execution budget in a
single non-terminating thinking stream and never emitted a `write_file` call —
`content_chars=0` throughout, `thinking_chars` climbing to the budget wall, one
`[TURN]` marker (exprvm-web-baseline-3 bead 242, execs 198/199, 2026-08-31). The
`num_predict` per-turn cap stays as the runaway bound; thinking stays on (model
default).

## Phase 2 (REFINE_TESTS_JUDGE)

2026-09-03 fleet switch moved JUDGE to `qwen3.6:35b-a3b` and dropped its
schema (`7572b21`). The qualification bakeoff run that day found the grammar
is actively harmful for this model even though JUDGE parses its final turn as
JSON: with `format:"json"` (+ a field-presence schema), qwen3.6 hit
`done_reason=length` dead turns on ~12% of runs and agreed with the gemma
baseline only 50% of the time; with `OmitFormat` it was 8/8 valid, 62%
agreement (matching gemma's own rate), at 5x the speed. `OmitFormat` applied
at `internal/verbs/refine_tests.go`'s REFINE_TESTS_JUDGE tool loop.
`ExtractJSON`'s existing control-char repair covers the class the grammar was
originally meant to prevent, so nothing else needed to change.

## Phase 3 (REFINE_TESTS_CRITIQUE)

Root-caused 2026-09-04/05 during `exprvm-web-baseline-11`
(`project_critique_tool_call_json_grammar_block` in memory): unlike JUDGE,
CRITIQUE's model (`qwen3:32b`) was never merely *harmed* by the grammar — it
was **structurally blocked** from ever calling its tool. `qwen3:32b`'s Ollama
template requires tool calls as literal `<tool_call>...</tool_call>` text in
the content stream, and `<` is not a valid JSON start token under
`format:"json"`'s GBNF grammar. Confirmed via `grep` over every captured
`REFINE_TESTS_CRITIQUE` call across four corpora: **0 non-empty `tool_calls`
out of 139**, CRITIQUE's entire recorded history. Same framework code,
different model-level tool-calling mechanism, is why ADJUDICATE
(`qwen3.6:35b-a3b`, native RENDERER/PARSER tool-calling, unaffected — see
below) never showed this failure on an otherwise-identical `format:"json"` +
`run_go_snippet` wiring.

`OmitFormat` applied at `internal/verbs/refine_tests.go`'s
REFINE_TESTS_CRITIQUE tool loop, same pattern as Phase 1/2 above.
`RefineTestsCritiqueOutput` is a flat object (`findings`/`verified_functions`/
`all_correct`/`summary`) of the same shape already proven to survive
`ExtractJSON` unconstrained for WRITE/JUDGE, so no new parsing risk. Not yet
live-validated — that's `exprvm-web-baseline-12`'s question (a separate
project-run conversation), not this fix's.

## Not changed

ADJUDICATE_NEXT_EXECUTION (`qwen3.6:35b-a3b`) still parses its final turn's
`Content` as JSON via `ollama.ExtractJSON` → `json.Unmarshal`, under the
default `format:"json"`, and is not currently broken. Its Modelfile shows
native `RENDERER qwen3.5` / `PARSER qwen3.5` tool-call handling (confirmed via
`curl .../api/show`) — its tool call lands in a channel the grammar doesn't
constrain the same way, unlike CRITIQUE's text-template model. No change
proposed here.

## Follow-on

With native tool-calling no longer required for WRITE, `devstral-small-2` and
`mistral-small3.2:24b` become candidates for REFINE_TESTS_WRITE — both
non-reasoning, so the gemma4:31b thinking-stream runaway (exprvm-web baseline
bead 224) is structurally impossible. devstral's test code was weak in the one
trial observed (every subtest just "parses, 1 statement", no structural
asserts); qualify properly before switching. Avoid the qwen family for WRITE
(blind-spot concern vs `qwen3:32b` on CRITIQUE).
