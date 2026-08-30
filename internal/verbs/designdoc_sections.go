package verbs

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ratchet/internal/db"
)

// designDocExcerptHeader prefixes the excerpt wherever it is handed to a
// downstream verb. It establishes the excerpt as authoritative over the bead
// spec — the bead spec is DECOMPOSE's summary and can lose detail; this text is
// straight from the design doc.
const designDocExcerptHeader = "The bead specification above is a summary produced by DECOMPOSE and may omit or blur " +
	"detail. The excerpts below are quoted directly from the project's design document and are AUTHORITATIVE: " +
	"where they and the bead spec disagree on a domain rule or a required behavior, these govern. A test may " +
	"assert only what the design document or the bead spec actually requires — where an output is described " +
	"loosely (\"returns an error\", \"Err is non-empty\"), assert that property, not an incidental string or " +
	"format that nothing pins.\n\n"

// loadDesignDocExcerptForBead reads the project's design doc and returns the
// bead-relevant excerpt (see loadDesignDocSectionsForBead), already prefixed
// with designDocExcerptHeader. Returns "" on any failure or when the doc has no
// recognizable structure — callers then simply omit the excerpt input, exactly
// as before this existed.
func loadDesignDocExcerptForBead(ctx context.Context, d *db.DB, projectID int64, bead *beadState) string {
	doc, err := loadDesignDoc(ctx, d, projectID)
	if err != nil {
		return ""
	}
	project, err := loadProject(ctx, d, projectID)
	if err != nil {
		return ""
	}
	body := loadDesignDocSectionsForBead(doc, project.FolderPath, bead)
	if body == "" {
		return ""
	}
	return designDocExcerptHeader + body
}

// The design doc (design_doc.md) is loaded whole by DECOMPOSE_SPEC / AUDIT /
// RECONCILE / SURVEY, but the downstream verbs — REFINE_TESTS_{WRITE,CRITIQUE,
// JUDGE} and ADJUDICATE_NEXT_EXECUTION — historically saw only the bead spec,
// DECOMPOSE's lossy summary of it. When DECOMPOSE blurred a rule (the
// exprvm-web-v2 bead 117 "(if compiled)" Bytecode-clause incident), nothing
// downstream could recover it even though the authoritative file was sitting
// unread in the project folder. This file assembles the slice of the design
// doc relevant to one bead so those verbs can be handed it as an explicit,
// authoritative input alongside the bead spec.
//
// Whole-doc would be simpler but the fleet's binding context window is
// qwen3:32b's 40960 tokens (REFINE_TESTS_CRITIQUE, ANALYZE_EXECUTION);
// gemma4:31b is 262144 and mistral-small3.2:24b 131072. A 57KB doc (~16K
// tokens) on top of the bead spec, the current test file, and the impl context
// would crowd that window. Extraction keeps the high-value sections (test
// scenarios, decomposition notes, the type/signature reference, the cross-bead
// contracts this bead participates in, and the behavioral rules naming this
// bead's own symbols) and drops the rest.

// designDocExcerptBudget bounds the assembled excerpt. ~30KB ≈ ~9K tokens,
// which alongside the bead spec, the current test file, and the impl context
// still fits the fleet's tightest window (qwen3:32b, 40960 tokens).
const designDocExcerptBudget = 30 * 1024

// prioritySections lists the doc's "## " sections most-load-bearing first. The
// assembler adds them in this order and stops when the budget is reached,
// dropping a whole section rather than truncating one mid-rule. "Domain-Specific
// Test Scenarios" and "Decomposition Notes" lead because they are exactly the
// content DECOMPOSE has been observed to compress lossily (the exprvm-web-v2
// bead 117 Bytecode-clause incident). "Data Types and Function Signatures"
// trails because it is the most redundant with the .go files already on disk.
var prioritySections = []string{
	"Domain-Specific Test Scenarios",
	"Decomposition Notes",
	"Behavioral Specification", // filtered to this bead's symbols — see below
	"Data Types and Function Signatures",
}

var (
	mdBoldLeadInRe = regexp.MustCompile(`(?m)^\*\*`)
	// backtickSigRe matches a bold lead-in that wraps a Go-ish signature in
	// backticks, e.g. **`(*Parser).ParseProgram() (*Program, error)`** — used
	// to tell "this block defines a specific function" from "this block states
	// a cross-cutting rule".
	backtickSigRe = regexp.MustCompile("^\\*\\*`[^`]*`\\*\\*")
)

// extractMarkdownSection (a "## <heading>" section body up to the next "## "
// heading) lives in mechanical_checks.go and is reused here.

type mdSubsection struct {
	heading string // "### ..." line text without the leading "### "; "" for the preamble
	body    string // text under the heading, up to the next "### " or end of section
}

// splitSubsections breaks a section body into "### heading" / body pairs. Text
// before the first "### " is returned with an empty heading.
func splitSubsections(section string) []mdSubsection {
	subRe := regexp.MustCompile(`(?m)^### +(.+?)[ \t]*$`)
	locs := subRe.FindAllStringSubmatchIndex(section, -1)
	if len(locs) == 0 {
		return []mdSubsection{{heading: "", body: strings.TrimSpace(section)}}
	}
	var out []mdSubsection
	if pre := strings.TrimSpace(section[:locs[0][0]]); pre != "" {
		out = append(out, mdSubsection{heading: "", body: pre})
	}
	for i, m := range locs {
		heading := section[m[2]:m[3]]
		bodyStart := m[1]
		bodyEnd := len(section)
		if i+1 < len(locs) {
			bodyEnd = locs[i+1][0]
		}
		out = append(out, mdSubsection{
			heading: heading,
			body:    strings.TrimSpace(section[bodyStart:bodyEnd]),
		})
	}
	return out
}

// splitBoldBlocks breaks flat prose (the Behavioral Specification's shape: a
// run of paragraphs each opening with a **bold** lead-in) into blocks, each
// starting at a "^**" line and running to the next one.
func splitBoldBlocks(section string) []string {
	locs := mdBoldLeadInRe.FindAllStringIndex(section, -1)
	if len(locs) == 0 {
		return []string{strings.TrimSpace(section)}
	}
	var out []string
	if pre := strings.TrimSpace(section[:locs[0][0]]); pre != "" {
		out = append(out, pre)
	}
	for i, m := range locs {
		end := len(section)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if b := strings.TrimSpace(section[m[0]:end]); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// goSymbols returns the top-level func, method, type, and const names declared
// in src. Best-effort: a file that only partially parses still yields the
// declarations that were read before the error.
func goSymbols(src string) []string {
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "", src, 0)
	if f == nil {
		return nil
	}
	seen := map[string]bool{}
	var add func(string)
	add = func(name string) {
		if name != "" && name != "_" && !seen[name] {
			seen[name] = true
		}
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			add(d.Name.Name)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					add(s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						add(n.Name)
					}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// beadDocAnchors returns the strings that mark a design-doc passage as relevant
// to this bead: the basenames of its non-test output files (e.g. "parser.go")
// and the Go symbols declared in those files as they currently exist on disk
// (present at least as scaffold stubs by the time REFINE_TESTS / ADJUDICATE
// run).
func beadDocAnchors(folderPath string, outputFiles []string) []string {
	seen := map[string]bool{}
	var anchors []string
	addAnchor := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			anchors = append(anchors, s)
		}
	}
	for _, f := range outputFiles {
		if !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") {
			continue
		}
		base := filepath.Base(f)
		addAnchor(base)
		addAnchor(strings.TrimSuffix(base, ".go")) // "parser" as well as "parser.go"
		src, err := os.ReadFile(filepath.Join(folderPath, f))
		if err != nil {
			continue
		}
		for _, sym := range goSymbols(string(src)) {
			addAnchor(sym)
		}
	}
	return anchors
}

// mentionsAny reports whether text contains any of the anchors as a
// word-ish substring (bounded so "parse" doesn't match "ParseProgram" only by
// accident, but "parser.go" and "ParseProgram" both match cleanly).
func mentionsAny(text string, anchors []string) bool {
	for _, a := range anchors {
		if len(a) < 3 {
			continue
		}
		if strings.Contains(text, a) {
			return true
		}
	}
	return false
}

// loadDesignDocSectionsForBead assembles the design-doc excerpt relevant to one
// bead. Returns "" if doc is empty or contains none of the expected section
// headings (an unstructured or missing doc — the caller then simply omits the
// excerpt input, same as before this existed).
func loadDesignDocSectionsForBead(doc, folderPath string, bead *beadState) string {
	if strings.TrimSpace(doc) == "" {
		return ""
	}
	anchors := beadDocAnchors(folderPath, bead.OutputFiles)

	var b strings.Builder
	wrote := false
	// section adds "## title\n\n<body>" only if it still fits the budget;
	// returns false when it was dropped for space.
	section := func(title, body string) bool {
		body = strings.TrimSpace(body)
		if body == "" {
			return false
		}
		chunk := fmt.Sprintf("## %s\n\n%s\n\n", title, body)
		if b.Len()+len(chunk) > designDocExcerptBudget {
			return false
		}
		b.WriteString(chunk)
		wrote = true
		return true
	}

	// Cross-Bead Contracts (this bead's) goes first and unconditionally — it is
	// small once filtered and is the single most relevant thing for a bead whose
	// tests exercise another bead's output (the exprvm-web bead 117 case).
	if contracts := extractMarkdownSection(doc, "Cross-Bead Contracts"); contracts != "" {
		var kept []string
		for _, sub := range splitSubsections(contracts) {
			if sub.heading == "" {
				continue // drop the scene-setting preamble; save the budget
			}
			if mentionsAny(sub.heading+"\n"+sub.body, anchors) {
				kept = append(kept, "### "+sub.heading+"\n\n"+sub.body)
			}
		}
		if len(kept) > 0 {
			section("Cross-Bead Contracts (this bead's)", strings.Join(kept, "\n\n"))
		}
	}

	for _, title := range prioritySections {
		body := extractMarkdownSection(doc, title)
		if body == "" {
			continue
		}
		if title == "Behavioral Specification" {
			// No "###" structure — a run of **bold-lead-in** paragraphs. Keep
			// the blocks naming one of this bead's symbols/files, plus the
			// cross-cutting rule blocks (lead-in is not itself a `Signature()`,
			// e.g. "**Left-associativity, precedence, and unary minus** ...").
			var kept []string
			for _, blk := range splitBoldBlocks(body) {
				if mentionsAny(blk, anchors) || !backtickSigRe.MatchString(blk) {
					kept = append(kept, blk)
				}
			}
			if len(kept) == 0 {
				continue
			}
			section("Behavioral Specification (this bead's)", strings.Join(kept, "\n\n"))
			continue
		}
		section(title, body)
	}

	if !wrote {
		return ""
	}
	return strings.TrimSpace(b.String())
}
