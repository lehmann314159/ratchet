package verbs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
)

// Schema-mode output (docs/schema-mode-reasoning-field.md).
//
// Every schema-mode verb sends Ollama an explicit JSON Schema in the request's
// `format` field instead of the bare string "json". The schema's first property
// is always `reasoning` — an unconstrained string the model fills with its
// chain-of-thought BEFORE it has to emit any structured field. This is how a
// reasoning model reasons under a grammar constraint: the `<think>` channel is
// masked by the JSON grammar, but a string value is not.
//
// Property order matters: Ollama's grammar generates object properties in
// schema declaration order, so `reasoning` must be authored first. A plain
// map[string]any would marshal alphabetically and defeat that, so schemas are
// built from orderedObject where order is load-bearing.

// kv is one ordered key/value pair.
type kv struct {
	K string
	V any
}

// orderedObject marshals to a JSON object with keys in slice order.
type orderedObject []kv

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, pair := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(pair.K)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		val, err := json.Marshal(pair.V)
		if err != nil {
			return nil, err
		}
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// reasoningMaxLen caps the schema `reasoning` string. Ollama's schema→GBNF
// DOES enforce string maxLength (probed 2026-08-31, gemma4:31b and the
// qwen3.6:35b-a3b model that produced a 76KB runaway): the grammar forces the
// string closed at the cap and the model finishes the object cleanly, no
// repeated-chop thrash. 16000 is generous — a genuine deep AUDIT chain-of-
// thought ran ~13-14K chars — while still bounding the pathological blowup
// that broke its own `\` escape and corrupted the JSON.
const reasoningMaxLen = 16000

// reasoningPropertyNamed is the leading chain-of-thought property. Nearly
// every schema-mode verb uses the key "reasoning" (via reasoningProperty);
// CERTIFY_MANIFEST reuses its existing `model_reasoning` field instead.
func reasoningPropertyNamed(name, guidance string) kv {
	return kv{name, map[string]any{
		"type":      "string",
		"maxLength": reasoningMaxLen,
		"description": "Think step by step here FIRST, before filling any field below. " +
			guidance + " This field is required and must not be empty.",
	}}
}

// reasoningProperty returns the leading `reasoning` property shared by most
// schema-mode verbs. guidance is verb-specific: what the model should actually
// work through before committing to the structured fields.
func reasoningProperty(guidance string) kv {
	return reasoningPropertyNamed("reasoning", guidance)
}

func stringArray() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
}

// beadObjectSchema is the JSON Schema for a single ParsedBead, shared by
// DECOMPOSE_SPEC's `beads[]` and RECONCILE_DECOMPOSITION's `updated_bead`.
// execution_budget is intentionally not in `required` — Validate doesn't
// enforce it and DecomposeSpec.Commit clamps it to the project default.
func beadObjectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":            map[string]any{"type": "string"},
			"full_text":        map[string]any{"type": "string"},
			"execution_budget": map[string]any{"type": "integer"},
			"monitor_override": map[string]any{"type": "string", "enum": []string{"honor", "ignore"}},
			"output_files":     stringArray(),
			"exit_criteria":    stringArray(),
		},
		"required": []string{"title", "full_text", "monitor_override", "output_files", "exit_criteria"},
	}
}

// disableThink is passed as Options.Think by every schema-mode verb. The
// chain-of-thought lives in the schema's `reasoning` field, so Ollama's
// separate thinking phase is redundant — and a format grammar suppresses it
// anyway. An explicit think:false avoids wasted thinking tokens on a model
// that would otherwise default it on.
func disableThink() *bool { b := false; return &b }

// logSchemaReasoning records whether a schema-mode verb's `reasoning` field
// was populated. The schema marks it required, but that only enforces key
// presence — an empty one means the model skipped the chain-of-thought,
// worth noticing without discarding an otherwise-valid response.
func logSchemaReasoning(verb, reasoning string, kvpairs ...any) {
	base := append([]any{"reasoning_chars", len(reasoning)}, kvpairs...)
	if strings.TrimSpace(reasoning) == "" {
		slog.Warn(verb+": schema-mode reasoning field is empty", kvpairs...)
		return
	}
	slog.Info(verb+": schema-mode reasoning captured", base...)
}

// DecomposeSpecSchema is the structured-output schema for DECOMPOSE_SPEC.
// Mirrors DecomposeSpecOutput / ParsedBead (internal/verbs/outputs.go).
// `beads` carries minItems:1 so the grammar itself forbids the empty-array
// degeneration seen with reasoning models under bare "json"
// (exprvm-web-muse-test, 2 identical DECOMPOSE failures, beads:[], 2026-08-31).
var DecomposeSpecSchema = orderedObject{
	{"type", "object"},
	{"properties", orderedObject{
		reasoningProperty("Work out the bead boundaries, their dependency order, " +
			"which files each bead owns, and where each Decomposition Notes pin belongs."),
		{"beads", map[string]any{
			"type":     "array",
			"minItems": 1,
			"items":    beadObjectSchema(),
		}},
		{"ambiguities", stringArray()},
	}},
	{"required", []string{"reasoning", "beads"}},
}

// AuditDecompositionSchema — schema-mode for AUDIT_DECOMPOSITION. Mirrors
// AuditDecompositionOutput (internal/verbs/outputs.go). `findings` is required
// as a key (may be empty on no_issues); Validate enforces the "issues_found
// implies non-empty findings" rule that a flat schema can't express.
var AuditDecompositionSchema = orderedObject{
	{"type", "object"},
	{"properties", orderedObject{
		reasoningProperty("Check each bead's spec against the design doc — missing requirements, " +
			"wrong numeric values, dropped Decomposition Notes pins, independence violations, " +
			"and exit criteria that can't actually be run. Work through every bead before deciding."),
		{"findings", map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bead_title":           map[string]any{"type": "string"},
					"issue":                map[string]any{"type": "string"},
					"design_doc_reference": map[string]any{"type": "string"},
				},
				"required": []string{"bead_title", "issue", "design_doc_reference"},
			},
		}},
		{"overall_verdict", map[string]any{"type": "string", "enum": []string{"no_issues", "issues_found"}}},
	}},
	{"required", []string{"reasoning", "findings", "overall_verdict"}},
}

// ReconcileDecompositionSchema — schema-mode for RECONCILE_DECOMPOSITION.
// Mirrors ReconcileDecompositionOutput. `updated_bead` is optional (present
// only for action == "agree_and_fix"); Validate enforces that conditional.
var ReconcileDecompositionSchema = orderedObject{
	{"type", "object"},
	{"properties", orderedObject{
		reasoningProperty("For each AUDIT finding, work out whether it is correct before you " +
			"respond: if you agree, produce the fully corrected bead; if you disagree, state the " +
			"specific reason. Address every finding."),
		{"responses", map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bead_title":        map[string]any{"type": "string"},
					"action":            map[string]any{"type": "string", "enum": []string{"agree_and_fix", "disagree"}},
					"reason":            map[string]any{"type": "string"},
					"already_addressed": map[string]any{"type": "boolean"},
					"updated_bead":      beadObjectSchema(),
				},
				"required": []string{"bead_title", "action", "reason"},
			},
		}},
	}},
	{"required", []string{"reasoning", "responses"}},
}

// REFINE_TESTS_CRITIQUE and REFINE_TESTS_JUDGE are NOT reasoning-first
// schema-mode. Both run the ChatWithTools (run_go_snippet) loop, and the
// tool loop is where schema-mode broke (REFINE_TESTS_WRITE, project 36).
// Reverted pending a deliberate tool-loop re-approach. See
// docs/schema-mode-reasoning-field.md. JUDGE keeps its flat, field-presence-
// only refineTestsJudgeFormatSchema (predates this session).

// --- Phase 3 verbs ---

// SurveySpecSchema — schema-mode for SURVEY_SPEC. Mirrors SurveySpecOutput.
var SurveySpecSchema = orderedObject{
	{"type", "object"},
	{"properties", orderedObject{
		reasoningProperty("Work out the module path, package name, and the full file set — " +
			"every type, function signature, and package var each file must declare per the design doc."),
		{"module", map[string]any{"type": "string"}},
		{"package", map[string]any{"type": "string"}},
		{"files", map[string]any{
			"type":     "array",
			"minItems": 1,
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":         map[string]any{"type": "string"},
					"declarations": map[string]any{"type": "string"},
				},
				"required": []string{"path", "declarations"},
			},
		}},
	}},
	{"required", []string{"reasoning", "module", "package", "files"}},
}

// CertifyManifestSchema — schema-mode for CERTIFY_MANIFEST. The MODEL's
// response carries only model_reasoning / final_decision / feedback;
// CertifyManifest.Run prepends the mechanical preliminary_decision before
// Validate sees it. model_reasoning IS the reasoning field (kept, not
// duplicated) — first, capped.
var CertifyManifestSchema = orderedObject{
	{"type", "object"},
	{"properties", orderedObject{
		reasoningPropertyNamed("model_reasoning", "Check the manifest against the mechanical results "+
			"and the design doc: does every required declaration exist with the right signature, and is "+
			"the preliminary decision correct?"),
		{"final_decision", map[string]any{"type": "string", "enum": []string{"approve", "reject"}}},
		{"feedback", map[string]any{"type": "string"}},
	}},
	{"required", []string{"model_reasoning", "final_decision"}},
}

// AnalyzeExecutionSchema — schema-mode for ANALYZE_EXECUTION. The model emits
// only reasoning + analyzer_interpretation; mechanical_findings is computed in
// AnalyzeExecution.Run and prepended before Validate. The reasoning field is
// where the working-through goes so analyzer_interpretation stays a tight
// hedged summary.
var AnalyzeExecutionSchema = orderedObject{
	{"type", "object"},
	{"properties", orderedObject{
		reasoningProperty("Work through what the execution evidence shows — commands run, files " +
			"written, test output — before you write the hedged interpretation."),
		{"analyzer_interpretation", map[string]any{"type": "string"}},
	}},
	{"required", []string{"reasoning", "analyzer_interpretation"}},
}

// RevisePendingSchema — schema-mode for REVISE_PENDING. Mirrors RevisePendingOutput.
var RevisePendingSchema = orderedObject{
	{"type", "object"},
	{"properties", orderedObject{
		reasoningProperty("For each pending bead, decide whether the just-committed change to an " +
			"earlier bead's spec requires a matching update here — and if so, what."),
		{"revisions", map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bead_title":        map[string]any{"type": "string"},
					"action":            map[string]any{"type": "string", "enum": []string{"update_spec", "no_change"}},
					"updated_full_text": map[string]any{"type": "string"},
				},
				"required": []string{"bead_title", "action"},
			},
		}},
	}},
	{"required", []string{"reasoning", "revisions"}},
}

// REFINE_TESTS_WRITE is deliberately NOT schema-mode: its real output is the
// write_function tool calls, and a reasoning-first schema on every turn let the
// model emit a plan + a completion claim without ever calling write_function
// (project 36 bead 203, 2026-08-31). Tool-primary verbs stay on bare "json".
