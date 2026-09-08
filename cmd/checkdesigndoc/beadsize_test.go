package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A compact but structurally complete design doc: file tree + Data Types with
// `// ---- file.go ----` markers, a Behavioral Specification, Cross-Bead
// Contracts, and a numbered Decomposition Notes list.
const beadSizeDoc = "## Architecture\n\n" +
	"```\n" +
	"app/\n" +
	"├── core.go     — Thing, MakeThing, StepThing\n" +
	"├── mid.go      — Widen\n" +
	"├── side.go     — Sidecar\n" +
	"└── web.go      — Handle1, Handle2, Handle3, Handle4\n" +
	"```\n\n" +
	"## Data Types and Function Signatures\n\n" +
	"```go\n" +
	"// ---- core.go ----\n" +
	"type Thing struct{ N int }\n" +
	"func MakeThing() Thing\n" +
	"func StepThing(t Thing) Thing\n" +
	"// ---- mid.go ----\n" +
	"func Widen(t Thing) Thing\n" +
	"// ---- side.go ----\n" +
	"func Sidecar(t Thing) Thing\n" +
	"// ---- web.go ----\n" +
	"func Handle1(w http.ResponseWriter, r *http.Request)\n" +
	"func Handle2(w http.ResponseWriter, r *http.Request)\n" +
	"func Handle3(w http.ResponseWriter, r *http.Request)\n" +
	"func Handle4(w http.ResponseWriter, r *http.Request)\n" +
	"```\n\n" +
	"## Behavioral Specification\n\n" +
	"### `MakeThing` / `StepThing`\n\nSome behavior.\n\n" +
	"### Handlers\n\nHandler behavior.\n\n" +
	"## Cross-Bead Contracts\n\n" +
	"### core → mid (data-shape)\n\n`Thing` flows.\n\n" +
	"### core → web (data-shape)\n\n`Thing` flows.\n\n" +
	"### mid → web (data-shape)\n\n`Thing` flows.\n\n" +
	"### side → web (data-shape)\n\n`Thing` flows.\n\n" +
	"## Decomposition Notes\n\n" +
	"**Bead dependency order (do not reorder):**\n\n" +
	"1. **core** — `Thing`, `MakeThing`, `StepThing`. No dependencies. Owns `core.go`.\n" +
	"2. **mid** — `Widen`. Depends on bead 1. Owns `mid.go`.\n" +
	"3. **side** — `Sidecar`. Depends on bead 1. Owns `side.go`.\n" +
	"4. **web** — `Handle1`, `Handle2`, `Handle3`, `Handle4`. Depends on beads 1, 2 and 3. Owns `web.go`.\n\n" +
	"**Pins:**\n\n- **Pin — something:** value.\n"

func parseBeads(t *testing.T, doc string) map[string]beadSizeInfo {
	t.Helper()
	notes := extractSection(doc, "Decomposition Notes")
	beads := parseBeadDependencyList(notes)
	if len(beads) == 0 {
		t.Fatal("no beads parsed")
	}
	fileFuncs, symToFile := parseDataTypesFuncs(doc)
	known := map[string]bool{}
	for f := range fileFuncs {
		known[f] = true
	}
	for _, f := range symToFile {
		known[f] = true
	}
	part := parseContractParticipation(doc, beads)
	behavioral := behavioralSubsections(doc)
	out := map[string]beadSizeInfo{}
	for i := range beads {
		resolveBeadFiles(&beads[i], symToFile, known)
		seen := map[string]bool{}
		for _, f := range beads[i].files {
			if seen[f] {
				continue
			}
			seen[f] = true
			beads[i].funcCount += fileFuncs[f]
		}
		beads[i].contractCount = part[beads[i].num]
		beads[i].behavioralLines = maxBehavioralLines(behavioral, beads[i])
		out[beads[i].title] = beads[i]
	}
	return out
}

func TestBeadSize_flagsLargeIntegratedBead(t *testing.T) {
	b := parseBeads(t, beadSizeDoc)

	web := b["web"]
	if web.funcCount != 4 {
		t.Errorf("web funcCount = %d, want 4", web.funcCount)
	}
	if len(web.fanIn) != 3 {
		t.Errorf("web fanIn = %v, want 3 entries", web.fanIn)
	}
	if web.contractCount != 3 {
		t.Errorf("web contractCount = %d, want 3 (core→web, mid→web, side→web)", web.contractCount)
	}
	if !web.flagged() {
		t.Errorf("web should FLAG (funcs=4, fan-in=3, contracts=3 → size high AND integration high)")
	}
}

func TestBeadSize_smallBeadPasses(t *testing.T) {
	b := parseBeads(t, beadSizeDoc)
	if b["mid"].flagged() {
		t.Errorf("mid (1 func) must not flag")
	}
	if b["core"].flagged() {
		t.Errorf("core (fan-in 0, 1 contract) must not flag despite 2 funcs")
	}
}

func TestBeadSize_bigSelfContainedBeadPasses(t *testing.T) {
	// core with many functions but zero fan-in and one contract stays PASS —
	// the AND with integration is what protects `expr`-shaped beads.
	doc := strings.Replace(beadSizeDoc,
		"func StepThing(t Thing) Thing\n",
		"func StepThing(t Thing) Thing\nfunc A(t Thing) Thing\nfunc B(t Thing) Thing\nfunc C(t Thing) Thing\n", 1)
	doc = strings.Replace(doc,
		"1. **core** — `Thing`, `MakeThing`, `StepThing`.",
		"1. **core** — `Thing`, `MakeThing`, `StepThing`, `A`, `B`, `C`.", 1)
	b := parseBeads(t, doc)
	if got := b["core"].funcCount; got < 5 {
		t.Fatalf("core funcCount = %d, want >=5", got)
	}
	if b["core"].flagged() {
		t.Errorf("core must not flag: high size but fan-in 0 / 1 own-side contract")
	}
}

func TestBeadSize_sizingRationaleClearsFlag(t *testing.T) {
	doc := strings.Replace(beadSizeDoc,
		"Depends on beads 1, 2 and 3. Owns `web.go`.",
		"Depends on beads 1, 2 and 3. Owns `web.go`. sizing rationale: four thin handler wrappers, no shared assembly.", 1)
	b := parseBeads(t, doc)
	web := b["web"]
	if !web.sizingRationale {
		t.Fatal("sizing rationale not detected")
	}
	if web.flagged() {
		t.Errorf("web must not flag once a sizing rationale is present")
	}
}

func TestBeadSize_hiddenComplexityNote(t *testing.T) {
	// A hub bead (3 contracts) that declares only 1 function but has a long
	// behavioral subsection → NOTE, not FLAG, not PASS.
	longSpec := "### `Assemble`\n\n" + strings.Repeat("A rule of behavior.\n", 35) + "\n"
	doc := strings.Replace(beadSizeDoc,
		"### `MakeThing` / `StepThing`\n\nSome behavior.\n\n",
		"### `MakeThing` / `StepThing`\n\nSome behavior.\n\n"+longSpec, 1)
	doc = strings.Replace(doc,
		"4. **web** — `Handle1`, `Handle2`, `Handle3`, `Handle4`. Depends on beads 1, 2 and 3. Owns `web.go`.",
		"4. **web** — `Assemble`. Depends on beads 1, 2 and 3. Owns `webthin.go`.", 1)
	// web now owns a file with no declared funcs.
	b := parseBeads(t, doc)
	web := b["web"]
	if web.flagged() {
		t.Errorf("web should not FLAG (only 1 declared func)")
	}
	if !web.hiddenComplexity() {
		t.Errorf("web should NOTE: integration high, funcs<=1, behavioralLines=%d", web.behavioralLines)
	}
}

func TestBeadSize_shortPipelineBeadNoNote(t *testing.T) {
	// studio-shape: fan-in high, 1 function, but a SHORT behavioral subsection.
	doc := strings.Replace(beadSizeDoc,
		"### Handlers\n\nHandler behavior.\n\n",
		"### Handlers\n\nHandler behavior.\n\n### `Studio`\n\nRuns the chain.\n\n", 1)
	doc = strings.Replace(doc,
		"4. **web** — `Handle1`, `Handle2`, `Handle3`, `Handle4`. Depends on beads 1, 2 and 3. Owns `web.go`.",
		"4. **web** — `Studio`. Depends on beads 1, 2 and 3. Owns `studio.go`.", 1)
	b := parseBeads(t, doc)
	if b["web"].hiddenComplexity() {
		t.Errorf("short-spec pipeline bead must not NOTE")
	}
}

func TestBeadSize_skipsDocWithoutNumberedList(t *testing.T) {
	doc := "## Decomposition Notes\n\n- **Pin something to the `game` bead.**\n- **Sequencing:** templates before handlers.\n"
	if got := parseBeadDependencyList(extractSection(doc, "Decomposition Notes")); got != nil {
		t.Errorf("prose-only Decomposition Notes must yield no beads, got %+v", got)
	}
}

func TestBeadSize_lastBeadBodyDoesNotRunToSectionEnd(t *testing.T) {
	// The trailing "**Pins:**" block names symbols from other files; the last
	// bead ("web") must not absorb them.
	doc := strings.Replace(beadSizeDoc,
		"**Pins:**\n\n- **Pin — something:** value.\n",
		"**Pins:**\n\n- **Pin — core:** `MakeThing`, `StepThing`, `Widen` and `core.go`, `mid.go`.\n", 1)
	b := parseBeads(t, doc)
	if got := b["web"].funcCount; got != 4 {
		t.Errorf("web funcCount = %d, want 4 — trailing Pins block leaked in", got)
	}
}

// TestBeadSize_CorpusGate locks the check's behavior against the real design
// docs. Every bead that reached COMPLETE in a baseline must PASS; the known
// oversized/borderline beads must FLAG.
func TestBeadSize_CorpusGate(t *testing.T) {
	type want struct {
		flag map[string]bool // beads expected to FLAG (all others must PASS/NOTE-not-FLAG)
		note map[string]bool // beads expected to NOTE
		skip bool
	}
	cases := map[string]want{
		"fractalviz-design-doc.md":      {flag: map[string]bool{"handlers": true}},
		"lsystem-studio-design-doc.md":  {flag: map[string]bool{"handlers": true}},
		"exprvm-web-design-doc.md":      {flag: map[string]bool{"handlers+templates": true}},
		"exprvm-design-doc.md":          {flag: map[string]bool{}},
		"connect-four-v1-design-doc.md": {skip: true},
		"tictactoe-v1-design-doc.md":    {skip: true},
		"tasklist-design-doc.md":        {skip: true},
		"kafka-sim-design-doc.md":       {skip: true},
		"haiku-generator-design-doc.md": {skip: true},
		"checkers-design-doc.md":        {skip: true},
		"fractal-design-doc.md":         {skip: true},
	}
	dir := filepath.Join("..", "..", "docs", "design-docs")
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			notes := extractSection(string(data), "Decomposition Notes")
			beads := parseBeadDependencyList(notes)
			if w.skip {
				if len(beads) != 0 {
					t.Fatalf("expected SKIP (no numbered list), got %d beads", len(beads))
				}
				return
			}
			b := parseBeads(t, string(data))
			for title, bi := range b {
				gotFlag := bi.flagged()
				if gotFlag != w.flag[title] {
					t.Errorf("bead %q: flagged=%v, want %v (funcs=%d fan-in=%d contracts=%d)",
						title, gotFlag, w.flag[title], bi.funcCount, len(bi.fanIn), bi.contractCount)
				}
				if w.note[title] && !bi.hiddenComplexity() {
					t.Errorf("bead %q: expected hidden-complexity NOTE", title)
				}
			}
		})
	}
}
