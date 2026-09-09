package verbs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Cross-bead symbol contamination guard.
//
// ADJUDICATE_NEXT_EXECUTION's execute_revised path rewrites a bead's full_text
// from scratch. Observed live (cron-studio run 1, bead 1 `field`,
// memory/handoff_cronstudio_1): the rewrite "completed" the spec by pulling in
// `type Schedule` and `func Parse` — symbols the design doc assigns to
// schedule.go, owned by a *different* bead that already exists as a scaffolded
// stub. The revised spec then mandates a duplicate definition against the
// scaffold, so EXECUTE can only produce a compile error; muse correctly refuses
// to guess and stalls, and ADJUDICATE misreads the fallout as an execution
// capability problem.
//
// beadConsistencyViolations (the existing execute_revised source-side gate) only
// sees the current bead, so a spec telling the agent to implement another bead's
// symbols in this bead's file passes it clean. This guard supplies the missing
// context: the symbols every *other* bead owns, taken from the scaffold on disk.

// buildSiblingSymbolOwners maps each Go symbol declared in a sibling bead's
// on-disk non-test .go files to a human-readable "title (file.go)" owner label.
// current's own output files and the symbols they declare are excluded, so a
// symbol this bead legitimately owns (including a forward-declared scaffold
// stub) never appears. Best-effort: an unreadable or unparseable sibling file
// contributes nothing, exactly like beadDocAnchors.
func buildSiblingSymbolOwners(folderPath string, beads []beadState, current beadState) map[string]string {
	ownFiles := map[string]bool{}
	for _, f := range current.OutputFiles {
		ownFiles[filepath.Base(f)] = true
	}
	ownSyms := map[string]bool{}
	for _, f := range current.OutputFiles {
		if !isNonTestGoFile(f) {
			continue
		}
		if src, err := os.ReadFile(filepath.Join(folderPath, f)); err == nil {
			for _, s := range goSymbols(string(src)) {
				ownSyms[s] = true
			}
		}
	}

	owners := map[string]string{}
	for _, b := range beads {
		if b.BeadID == current.BeadID {
			continue
		}
		for _, f := range b.OutputFiles {
			if !isNonTestGoFile(f) || ownFiles[filepath.Base(f)] {
				continue
			}
			src, err := os.ReadFile(filepath.Join(folderPath, f))
			if err != nil {
				continue
			}
			for _, sym := range goSymbols(string(src)) {
				if ownSyms[sym] {
					continue
				}
				if _, seen := owners[sym]; seen {
					continue // first owner wins; the label is illustrative
				}
				owners[sym] = fmt.Sprintf("%s (%s)", b.Title, filepath.Base(f))
			}
		}
	}
	return owners
}

func isNonTestGoFile(f string) bool {
	return strings.HasSuffix(f, ".go") && !strings.HasSuffix(f, "_test.go")
}

// crossBeadSymbolContamination reports each sibling-owned symbol that revised's
// full_text instructs the executor to DECLARE — `type X` or `func X`
// declaration form. Restricted to those two forms on purpose: a bead spec that
// quotes a prohibition ("Do NOT put `var templates` anywhere except main.go")
// or names a sibling symbol in prose ("schedule.go calls parseField") must not
// trip the guard, but `type X`/`func X` declaration syntax essentially never
// appears in a spec except as "write this".
func crossBeadSymbolContamination(revised ParsedBead, owners map[string]string) []string {
	var v []string
	for sym, owner := range owners {
		if symbolDeclaredIn(revised.FullText, sym) {
			v = append(v, fmt.Sprintf(
				"revised spec declares %q, which belongs to bead %s — defining it in this "+
					"bead's files collides with the scaffold stub and forces a duplicate-definition "+
					"compile error. Reference the symbol, do not re-declare it.",
				sym, owner))
		}
	}
	sort.Strings(v)
	return v
}

// symbolDeclaredIn reports whether text contains a `type <sym>` or `func <sym>`
// declaration (in prose, a list item, or a fenced code block — the anchor is
// the keyword + name, not the surrounding markup).
func symbolDeclaredIn(text, sym string) bool {
	return regexp.MustCompile(`(?:type|func)\s+` + regexp.QuoteMeta(sym) + `\b`).MatchString(text)
}
