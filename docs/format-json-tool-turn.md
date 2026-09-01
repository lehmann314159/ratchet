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

## Not changed

REFINE_TESTS_CRITIQUE (`qwen3:32b`), REFINE_TESTS_JUDGE (`gemma4:31b`), and
ADJUDICATE_NEXT_EXECUTION (`gemma4:31b`) all parse their final turn's `Content`
as JSON via `ollama.ExtractJSON` → `json.Unmarshal`. All three run on reasoning
models with thinking ON — the working config — and are not currently broken.
`ExtractJSON.escapeRawControlCharsInStrings` already repairs the
unescaped-control-char class that `format:"json"` was originally added to
prevent, applied unconditionally in every Validate path, so dropping the grammar
there is feasible — but it is deferred to the work that moves those verbs onto
non-reasoning models (gated on the model-qualification harness), where dropping
it becomes mandatory rather than optional.

## Follow-on

With native tool-calling no longer required for WRITE, `devstral-small-2` and
`mistral-small3.2:24b` become candidates for REFINE_TESTS_WRITE — both
non-reasoning, so the gemma4:31b thinking-stream runaway (exprvm-web baseline
bead 224) is structurally impossible. devstral's test code was weak in the one
trial observed (every subtest just "parses, 1 statement", no structural
asserts); qualify properly before switching. Avoid the qwen family for WRITE
(blind-spot concern vs `qwen3:32b` on CRITIQUE).
