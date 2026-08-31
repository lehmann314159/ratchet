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

// reasoningProperty returns the leading `reasoning` property shared by every
// schema-mode verb. guidance is verb-specific: what the model should actually
// work through before committing to the structured fields.
func reasoningProperty(guidance string) kv {
	return kv{"reasoning", map[string]any{
		"type": "string",
		"description": "Think step by step here FIRST, before filling any field below. " +
			guidance + " This field is required and must not be empty.",
	}}
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

// RefineTestsCritiqueSchema — schema-mode for REFINE_TESTS_CRITIQUE. Mirrors
// RefineTestsCritiqueOutput. Only `summary` is enforced by Validate; the
// schema keeps the reasoning-first shape and pins the field set. This verb
// runs through ChatWithTools (a run_go_snippet loop) — the schema applies to
// the final content turn, tool-call turns produce no content and are
// unaffected (same as REFINE_TESTS_JUDGE).
var RefineTestsCritiqueSchema = orderedObject{
	{"type", "object"},
	{"properties", orderedObject{
		reasoningProperty("Go through each Test* function against the bead spec and the design-doc " +
			"excerpts: is every asserted value correct, and does any test assert MORE than either " +
			"source requires? Note what you verified with run_go_snippet."),
		{"findings", stringArray()},
		{"verified_functions", stringArray()},
		{"all_correct", map[string]any{"type": "boolean"}},
		{"summary", map[string]any{"type": "string"}},
	}},
	{"required", []string{"reasoning", "summary"}},
}
