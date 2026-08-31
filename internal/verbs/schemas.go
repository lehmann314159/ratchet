package verbs

import (
	"bytes"
	"encoding/json"
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

// disableThink is passed as Options.Think by every schema-mode verb. The
// chain-of-thought lives in the schema's `reasoning` field, so Ollama's
// separate thinking phase is redundant — and a format grammar suppresses it
// anyway. An explicit think:false avoids wasted thinking tokens on a model
// that would otherwise default it on.
func disableThink() *bool { b := false; return &b }

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
			"items": map[string]any{
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
			},
		}},
		{"ambiguities", stringArray()},
	}},
	{"required", []string{"reasoning", "beads"}},
}
