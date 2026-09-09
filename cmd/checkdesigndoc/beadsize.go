package main

// ---------------------------------------------------------------------------
// bead-size check
// ---------------------------------------------------------------------------
//
// Flags a "## Decomposition Notes" bead that is BOTH large (owns many
// functions) AND heavily integrated (depends on several prior beads, or
// participates in several Cross-Bead Contracts). That combination is the shape
// that makes the EXECUTE model (muse-glimmer) design the whole bead in its head
// and never write — the lsystem `grammar` monolith (ParseSystem + 5 helpers,
// ~150 lines, integrating the expr bead) escalated five from-scratch runs on
// exactly this spiral, and hand-splitting it into three expr-sized sub-beads
// (grammar-modules / grammar-rules / grammar-system) fixed it — every sub-bead
// then ran one-shot. See docs/decomposition-framework-plan.md and
// memory/handoff_lsystem_run_6.
//
// Neither signal alone is a problem: `expr` is a big (~230-line) bead that
// one-shots every run because its fan-in is zero, and `grammar-modules` is in
// four contracts but is two small functions. The product is the spiral zone.
//
// Thresholds calibrated against every bead of fractalviz (8/8 clean), lsystem
// run 6 (13/13 clean, including the three grammar sub-beads), exprvm-web (the
// merged handlers+templates bead spiralled — baseline-14/15), and exprvm:
//
//	size high        = >= 4 owned functions/methods
//	integration high = fan-in >= 3 prior beads, OR named in >= 3 Cross-Bead Contract blocks
//	FLAG             = size high AND integration high
//
// Report-only, like every other check here. A flagged bead is cleared by a
// "sizing rationale:" (or "sizing note:") phrase in its Decomposition Notes
// bullet — one sentence saying why the surface area is acceptable, or the
// bead gets split.
//
// A separate NOTE (advisory, not a flag) fires on a heavily-integrated bead
// that the doc under-specifies — <=1 declared function but a 30+ line
// behavioral subsection. The pre-split lsystem `grammar` monolith is exactly
// this: Data Types listed only `ParseSystem`, so the func count is 1 and the
// FLAG rule can't see it, but its 45-line spec describes three parsers' worth
// of behavior. The fix the NOTE asks for — list the unexported helper
// signatures in Data Types — makes the real surface area visible, after which
// the FLAG rule catches it normally.

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	beadSizeFuncThreshold     = 4
	beadSizeFanInThreshold    = 3
	beadSizeContractThreshold = 3
	// A heavily-integrated bead that declares <=1 function but whose behavioral
	// subsection runs past this many lines is under-specified — the pre-split
	// lsystem `grammar` bead declared only `ParseSystem` but its 45-line spec
	// described three parsers' worth of behavior. Emit a NOTE (not a flag) so
	// the author lists the helper signatures and the real size becomes visible.
	beadSizeHiddenComplexityLines = 30
	// A bead with a small function count but a very long behavioral subsection is
	// a single hard translator doing too much (cron-studio `field` — 3 chained
	// parsers, 113 behavioral lines, stalled EXECUTE; the pre-split
	// grammar.ParseSystem) — a doc-side split candidate even when integration is
	// low, which is the case hiddenComplexity (integration-hub-gated) misses.
	// DELIBERATELY conservative: 70 clears every bead that reached COMPLETE in a
	// baseline (highest non-flagged: lsystem `render` at 48) and catches the
	// known staller. It is a placeholder to be calibrated against burn-in
	// flag-vs-outcome data, NOT hand-tuned against the current corpus
	// (memory/project_burn_in_freeze B3b).
	beadSizeHeavyBehaviorLines = 70
)

type beadSizeInfo struct {
	num             int
	title           string
	files           []string // owned *.go files (non-test)
	funcCount       int
	fanIn           []int    // distinct other bead numbers this bullet references
	contractCount   int      // distinct Cross-Bead Contract blocks naming this bead
	sizingRationale bool     // bullet contains a "sizing rationale:" escape-hatch note
	pendingSyms     []string // backticked identifiers from the bullet, for file resolution
	behavioralLines int      // longest Behavioral Specification subsection for this bead
}

// hiddenComplexity: an integrated bead the doc under-specifies — <=1 declared
// function but a long behavioral subsection. Advisory, not a flag.
func (b beadSizeInfo) hiddenComplexity() bool {
	return b.integrationHigh() && b.funcCount <= 1 &&
		b.behavioralLines >= beadSizeHiddenComplexityLines
}

// heavyBehavioralSpec: few declared functions but a very long behavioral
// subsection — a single hard translator doing too much (cron-studio `field`,
// the pre-split grammar.ParseSystem) that stalls EXECUTE. Distinct from
// hiddenComplexity, which also requires the bead to be an integration hub.
// Advisory, not a flag.
func (b beadSizeInfo) heavyBehavioralSpec() bool {
	return !b.flagged() && !b.sizingRationale && !b.hiddenComplexity() &&
		b.funcCount >= 1 && b.funcCount <= 3 &&
		b.behavioralLines >= beadSizeHeavyBehaviorLines
}

// noted reports whether the bead draws any advisory NOTE.
func (b beadSizeInfo) noted() bool {
	return b.hiddenComplexity() || b.heavyBehavioralSpec()
}

func (b beadSizeInfo) sizeHigh() bool { return b.funcCount >= beadSizeFuncThreshold }
func (b beadSizeInfo) integrationHigh() bool {
	return len(b.fanIn) >= beadSizeFanInThreshold || b.contractCount >= beadSizeContractThreshold
}
func (b beadSizeInfo) flagged() bool {
	return b.sizeHigh() && b.integrationHigh() && !b.sizingRationale
}

func reportBeadSize(w *os.File, path, content string) {
	fmt.Fprintln(w, "== bead-size ==")
	fmt.Fprintln(w, "Decomposition Notes beads that are both large (>=4 owned functions) and heavily")
	fmt.Fprintln(w, "integrated (>=3 prior-bead dependencies or >=3 Cross-Bead Contracts) — the shape")
	fmt.Fprintln(w, "the EXECUTE model spirals on. Each hit needs a split, or a one-line")
	fmt.Fprintln(w, "\"sizing rationale:\" note in the bullet. Report-only; a clean scan is not a")
	fmt.Fprintln(w, "guarantee. See docs/decomposition-framework-plan.md.")
	fmt.Fprintln(w)

	notes := extractSection(content, "Decomposition Notes")
	if notes == "" {
		fmt.Fprintln(w, "SKIPPED: no \"## Decomposition Notes\" section.")
		return
	}
	beads := parseBeadDependencyList(notes)
	if len(beads) == 0 {
		fmt.Fprintln(w, "SKIPPED: no numbered bead-dependency list under \"## Decomposition Notes\"")
		fmt.Fprintln(w, "(expected \"1. **bead-name** — ...\" / \"1. **bead-name**: ...\" items). This doc")
		fmt.Fprintln(w, "predates that convention, or lays its decomposition out as prose or a table.")
		return
	}

	fileFuncs, symToFile := parseDataTypesFuncs(content)
	behavioral := behavioralSubsections(content)
	knownFiles := map[string]bool{}
	for f := range fileFuncs {
		knownFiles[f] = true
	}
	for _, f := range symToFile {
		knownFiles[f] = true
	}
	contractParticipation := parseContractParticipation(content, beads)

	for i := range beads {
		resolveBeadFiles(&beads[i], symToFile, knownFiles)
		seen := map[string]bool{}
		for _, f := range beads[i].files {
			if seen[f] {
				continue
			}
			seen[f] = true
			beads[i].funcCount += fileFuncs[f]
		}
		beads[i].contractCount = contractParticipation[beads[i].num]
		beads[i].behavioralLines = maxBehavioralLines(behavioral, beads[i])
	}

	anyFlag, anyNote := false, false
	for _, b := range beads {
		verdict := "PASS"
		if b.flagged() {
			verdict = "FLAG"
			anyFlag = true
		} else if b.sizeHigh() && b.integrationHigh() && b.sizingRationale {
			verdict = "PASS (sizing rationale noted)"
		} else if b.noted() {
			verdict = "NOTE"
			anyNote = true
		}
		fanIn := "0"
		if len(b.fanIn) > 0 {
			parts := make([]string, len(b.fanIn))
			for i, n := range b.fanIn {
				parts[i] = strconv.Itoa(n)
			}
			fanIn = fmt.Sprintf("%d {%s}", len(b.fanIn), strings.Join(parts, ","))
		}
		fmt.Fprintf(w, "  bead %-2d %-20s  funcs=%d  fan-in=%s  contracts=%d  -> %s\n",
			b.num, b.title, b.funcCount, fanIn, b.contractCount, verdict)
		switch {
		case b.flagged():
			fmt.Fprintf(w, "       files: %s\n", strings.Join(b.files, ", "))
			fmt.Fprintln(w, "       split it (cf. run-6: grammar -> grammar-modules / grammar-rules /")
			fmt.Fprintln(w, "       grammar-system, each with its own behavioral subsection + contracts),")
			fmt.Fprintln(w, "       or add a \"sizing rationale:\" note to the bullet.")
		case b.hiddenComplexity():
			fmt.Fprintf(w, "       under-specified: %d-line behavioral subsection but Data Types lists only\n", b.behavioralLines)
			fmt.Fprintf(w, "       %d function(s) for this bead. If it needs unexported helpers, list their\n", b.funcCount)
			fmt.Fprintln(w, "       signatures in Data Types so the real surface area is visible to this check.")
		case b.heavyBehavioralSpec():
			fmt.Fprintf(w, "       dense spec: %d declared function(s) but a %d-line behavioral subsection.\n", b.funcCount, b.behavioralLines)
			fmt.Fprintln(w, "       A single hard translator doing this much (glob->regexp, chained parsers)")
			fmt.Fprintln(w, "       has stalled EXECUTE before — split it doc-side into named fragments, or add")
			fmt.Fprintln(w, "       a \"sizing rationale:\" note if it is genuinely irreducible.")
		}
	}
	fmt.Fprintln(w)
	switch {
	case anyFlag:
		fmt.Fprintf(w, "%d bead(s) flagged, %d note(s).\n", countBy(beads, beadSizeInfo.flagged), countBy(beads, beadSizeInfo.noted))
	case anyNote:
		fmt.Fprintf(w, "0 beads flagged, %d note(s) — see NOTE lines above.\n", countBy(beads, beadSizeInfo.noted))
	default:
		fmt.Fprintln(w, "0 beads flagged. Still worth an eyeball — this counts functions and")
		fmt.Fprintln(w, "dependencies, it does not judge whether one function is doing too much.")
	}
}

func countBy(beads []beadSizeInfo, pred func(beadSizeInfo) bool) int {
	n := 0
	for _, b := range beads {
		if pred(b) {
			n++
		}
	}
	return n
}

// --- Decomposition Notes numbered list ------------------------------------

var (
	// A numbered bead item: "1. **grammar-modules** — ..." or "3. **vm**: ...".
	// The title is the first bold run; the body is everything up to the next
	// "^N. " item or a blank line followed by a non-indented line.
	beadItemRe = regexp.MustCompile(`(?m)^(\d+)\.\s+\*\*([^*]+)\*\*`)
	// The list ends at the first non-item block: a blank line then a bold
	// sub-heading ("**Integration bead...**", "**Pins...**") or a "## " heading.
	listEndRe = regexp.MustCompile(`(?m)^\s*$\n(?:\*\*|## |\x60\x60\x60)`)
	// "beads 2, 3, 4, 5" / "bead 1" / "beads 1 and 4" / "(bead 6)" / "bead 2's".
	beadRefRe = regexp.MustCompile(`(?i)\bbeads?\s+([0-9][0-9,\s&and-]*)`)
	beadNumRe = regexp.MustCompile(`\d+`)
	// "Owns `handlers.go`" / "owns `game.go` and `game_test.go`". Requires the
	// keyword — a bullet also mentions other beads' files in passing.
	ownsFileRe        = regexp.MustCompile("(?i)owns?\\s+`([A-Za-z0-9_/]+\\.go)`")
	sizingRationaleRe = regexp.MustCompile(`(?i)sizing (rationale|note)\s*:`)
	backtickIdentRe   = regexp.MustCompile("`([^`]+)`")
)

func parseBeadDependencyList(notes string) []beadSizeInfo {
	locs := beadItemRe.FindAllStringSubmatchIndex(notes, -1)
	if len(locs) < 2 {
		return nil
	}
	// Cap the last item's body: everything after the numbered list (Integration
	// bead prose, Pins) is not part of any bead's spec.
	listEnd := len(notes)
	if e := listEndRe.FindStringIndex(notes[locs[len(locs)-1][1]:]); e != nil {
		listEnd = locs[len(locs)-1][1] + e[0]
	}
	var beads []beadSizeInfo
	for i, m := range locs {
		num, _ := strconv.Atoi(notes[m[2]:m[3]])
		title := collapseWhitespace(notes[m[4]:m[5]])
		bodyStart := m[1]
		bodyEnd := listEnd
		if i+1 < len(locs) {
			bodyEnd = locs[i+1][0]
		}
		body := notes[bodyStart:bodyEnd]

		b := beadSizeInfo{num: num, title: title}
		b.sizingRationale = sizingRationaleRe.MatchString(body)

		// fan-in: every bead number referenced in the body, minus self.
		fanSet := map[int]bool{}
		for _, rm := range beadRefRe.FindAllStringSubmatch(body, -1) {
			for _, ns := range beadNumRe.FindAllString(rm[1], -1) {
				if n, err := strconv.Atoi(ns); err == nil && n != num {
					fanSet[n] = true
				}
			}
		}
		for n := range fanSet {
			b.fanIn = append(b.fanIn, n)
		}
		sort.Ints(b.fanIn)

		// owned files: explicit "Owns `x.go`" / any `x.go` named in the bullet.
		fileSet := map[string]bool{}
		for _, fm := range ownsFileRe.FindAllStringSubmatch(body, -1) {
			f := fm[1]
			if strings.HasSuffix(f, "_test.go") || f == "go.mod" || f == "do_not_use_this_test.go" {
				continue
			}
			fileSet[f] = true
		}
		b.files = keysSorted(fileSet)

		// Fallback symbols for docs whose bullets name symbols, not files
		// (exprvm / exprvm-web). Only the LEADING symbol list counts — the run
		// of backticked identifiers before the first sentence break — never the
		// "Takes a `*Program` (bead 2's type)" dependency mentions after it.
		lead := body
		if p := firstSentenceBreak(body); p >= 0 {
			lead = body[:p]
		}
		for _, sm := range backtickIdentRe.FindAllStringSubmatch(lead, -1) {
			s := strings.TrimSpace(sm[1])
			if !strings.HasSuffix(s, ".go") {
				b.pendingSyms = append(b.pendingSyms, s)
			}
		}
		beads = append(beads, b)
	}
	return beads
}

// resolveBeadFiles fills in b.files when the bullet named symbols, not files.
// Priority: explicit "Owns `x.go`" (already set) > title match (`vm` -> vm.go,
// `handlers+templates` -> handlers.go + templates.go, `cli`/`main` -> main.go) >
// the leading symbol list mapped through the Data Types file markers.
func resolveBeadFiles(b *beadSizeInfo, symToFile map[string]string, knownFiles map[string]bool) {
	if len(b.files) > 0 {
		return
	}
	have := map[string]bool{}
	add := func(f string) {
		if f != "" && knownFiles[f] && !have[f] {
			have[f] = true
			b.files = append(b.files, f)
		}
	}
	for _, part := range strings.Split(b.title, "+") {
		t := normalizeContractName(part) // lowercased, de-punctuated, trailing-s stripped
		switch t {
		case "cli", "main", "entrypoint":
			add("main.go")
		default:
			add(t + ".go")
			add(t + "s.go") // trailing-s was stripped
		}
	}
	if len(b.files) == 0 {
		for _, s := range b.pendingSyms {
			if f, ok := symToFile[normalizeSym(s)]; ok {
				add(f)
			}
		}
	}
	sort.Strings(b.files)
}

// --- Data Types Go block --------------------------------------------------

var (
	dataTypesFileMarkerRe = regexp.MustCompile(`(?m)^//\s*-+\s*([A-Za-z0-9_/]+\.go)\s*-+`)
	funcDeclRe            = regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*\(`)
	methodDeclRe          = regexp.MustCompile(`(?m)^func\s+\(\s*\w+\s+\*?(\w+)\s*\)\s*([A-Za-z_]\w*)\s*\(`)
	typeDeclRe            = regexp.MustCompile(`(?m)^type\s+([A-Za-z_]\w*)\b`)
	constDeclRe           = regexp.MustCompile(`(?m)^const\s+([A-Za-z_]\w*)\b`)
	varDeclRe             = regexp.MustCompile(`(?m)^var\s+([A-Za-z_]\w*)\b`)
)

// parseDataTypesFuncs walks the "## Data Types and Function Signatures" Go
// block(s), attributing each `func` / `func (recv)` declaration to the file
// named by the nearest preceding `// ---- file.go ----` marker.
//
// Returns:
//   - fileFuncs: file -> count of function+method declarations
//   - symToFile: symbol name -> file (for func/type/const/var), so a bead bullet
//     that names symbols instead of a file can still be resolved
func parseDataTypesFuncs(content string) (fileFuncs map[string]int, symToFile map[string]string) {
	fileFuncs = map[string]int{}
	symToFile = map[string]string{}

	section := extractSection(content, "Data Types and Function Signatures")
	if section == "" {
		return
	}
	for _, block := range fenceRe.FindAllString(section, -1) {
		lines := strings.Split(block, "\n")
		current := ""
		for _, line := range lines {
			if m := dataTypesFileMarkerRe.FindStringSubmatch(line); m != nil {
				current = m[1]
				continue
			}
			if m := methodDeclRe.FindStringSubmatch(line); m != nil {
				if current != "" {
					fileFuncs[current]++
					symToFile[normalizeSym("(*"+m[1]+")."+m[2])] = current
					symToFile[normalizeSym(m[2])] = current
				}
				continue
			}
			if m := funcDeclRe.FindStringSubmatch(line); m != nil {
				if current != "" {
					fileFuncs[current]++
					symToFile[normalizeSym(m[1])] = current
				}
				continue
			}
			for _, re := range []*regexp.Regexp{typeDeclRe, constDeclRe, varDeclRe} {
				if m := re.FindStringSubmatch(line); m != nil && current != "" {
					if _, taken := symToFile[normalizeSym(m[1])]; !taken {
						symToFile[normalizeSym(m[1])] = current
					}
				}
			}
		}
	}
	return
}

// --- Behavioral Specification subsections --------------------------------

type behavioralSub struct {
	heading string
	lines   int
}

var behavioralHeadingRe = regexp.MustCompile(`(?m)^### (.+?)[ \t]*$`)

// behavioralSubsections returns every "### " subsection of the "## Behavioral
// Specification" section with its heading and line count.
func behavioralSubsections(content string) []behavioralSub {
	section := extractSection(content, "Behavioral Specification")
	if section == "" {
		return nil
	}
	locs := behavioralHeadingRe.FindAllStringSubmatchIndex(section, -1)
	var out []behavioralSub
	for i, m := range locs {
		end := len(section)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out = append(out, behavioralSub{
			heading: section[m[2]:m[3]],
			lines:   strings.Count(section[m[1]:end], "\n"),
		})
	}
	return out
}

// headingSubject returns the part of a behavioral-subsection heading that names
// what the subsection defines — the text before the first heading separator
// (" — ", " – ", " -- ", " - ") or the "(" of a signature. Identifiers that
// appear only after that point (argument types in `Compile(node Node)`, prose)
// are references to other beads' symbols, not ownership markers, and must not
// attribute the subsection's length to those beads.
func headingSubject(h string) string {
	cut := len(h)
	for _, sep := range []string{" — ", " – ", " -- ", " - ", "("} {
		if i := strings.Index(h, sep); i >= 0 && i < cut {
			cut = i
		}
	}
	return h[:cut]
}

// maxBehavioralLines returns the largest line count among behavioral
// subsections whose heading names one of the bead's owned symbols, one of its
// owned files' stems, or the bead itself ("... grammar-modules bead ...").
func maxBehavioralLines(subs []behavioralSub, b beadSizeInfo) int {
	stems := map[string]bool{}
	for _, part := range strings.Split(b.title, "+") {
		stems[normalizeContractName(part)] = true
	}
	for _, f := range b.files {
		stems[normalizeContractName(strings.TrimSuffix(f, ".go"))] = true
	}
	max := 0
	for _, s := range subs {
		h := normalizeContractName(headingSubject(s.heading))
		hit := false
		for stem := range stems {
			if stem != "" && strings.Contains(h, stem) {
				hit = true
				break
			}
		}
		if !hit {
			for _, sym := range b.pendingSyms {
				if n := normalizeContractName(sym); n != "" && strings.Contains(h, n) {
					hit = true
					break
				}
			}
		}
		if hit && s.lines > max {
			max = s.lines
		}
	}
	return max
}

// --- Cross-Bead Contract participation -----------------------------------

var contractArrowRe = regexp.MustCompile(`\s*(?:→|->)\s*`)

// parseContractParticipation counts, per bead number, the distinct
// "### <producers> → <consumers>" blocks in "## Cross-Bead Contracts" that name
// that bead (or one of its `+`-joined title aliases) on either side.
func parseContractParticipation(content string, beads []beadSizeInfo) map[int]int {
	out := map[int]int{}
	section := extractSection(content, "Cross-Bead Contracts")
	if section == "" {
		return out
	}

	alias := map[string]int{} // normalized bead-name alias -> bead number
	for _, b := range beads {
		for _, a := range strings.Split(b.title, "+") {
			alias[normalizeContractName(a)] = b.num
		}
	}

	for _, blk := range splitContractBlocks(section) {
		heading := blk.heading
		if i := strings.Index(heading, "("); i >= 0 {
			heading = heading[:i]
		}
		if !contractArrowRe.MatchString(heading) {
			continue
		}
		hit := map[int]bool{}
		for _, side := range contractArrowRe.Split(heading, -1) {
			for _, name := range regexp.MustCompile(`[+,/]`).Split(side, -1) {
				if n, ok := alias[normalizeContractName(name)]; ok {
					hit[n] = true
				}
			}
		}
		for n := range hit {
			out[n]++
		}
	}
	return out
}

// --- helpers ------------------------------------------------------------

var sentenceBreakRe = regexp.MustCompile(`\.(\s|$)`)

// firstSentenceBreak returns the index of the first sentence-ending period in s,
// or -1. Used to isolate a bead bullet's leading owned-symbol list from the
// dependency prose that follows it.
func firstSentenceBreak(s string) int {
	if loc := sentenceBreakRe.FindStringIndex(s); loc != nil {
		return loc[0]
	}
	return -1
}

func normalizeSym(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// normalizeContractName lowercases, drops backticks/spaces/hyphens, and strips a
// trailing "s" so "handler" and "handlers" match, and "grammar-modules" matches
// "grammar modules".
func normalizeContractName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("`", "", " ", "", "-", "", "_", "").Replace(s)
	s = strings.TrimSuffix(s, "s")
	return s
}

func keysSorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
