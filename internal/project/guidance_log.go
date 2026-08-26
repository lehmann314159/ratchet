package project

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// guidanceLogHeader marks the start of the append-only human-guidance
// section within a bead's full_text prose. Everything before it is the base
// design prose carried forward from the last pre-ADJUDICATE_NEXT_EXECUTION
// revision (see rewindBead); everything after is a numbered log of notes
// added at rewind time. A later note can mark an earlier one superseded or
// retracted, but never edits or removes its text — the reasoning trail
// across retries stays intact instead of getting silently overwritten the
// way ADJUDICATE_NEXT_EXECUTION's reactive patches used to (that's exactly
// the failure mode rewindBead's prose-revert already exists to undo).
const guidanceLogHeader = "\n\n## Human Guidance Log\n"

// guidanceNote is one entry in a bead's guidance log.
type guidanceNote struct {
	Number    int
	CreatedAt string
	Status    string // "active", "retracted", or "superseded by Note N"
	Text      string
}

var noteHeadingRE = regexp.MustCompile(`(?m)^### Note (\d+) — (.+)$`)

// parseGuidanceLog splits fullText into its base prose and any existing
// guidance notes. Returns fullText unchanged as base with nil notes if no
// guidance log section is present yet — the common case for a bead that has
// never had a human note added.
func parseGuidanceLog(fullText string) (base string, notes []guidanceNote) {
	idx := strings.Index(fullText, guidanceLogHeader)
	if idx == -1 {
		return fullText, nil
	}
	base = fullText[:idx]
	body := fullText[idx+len(guidanceLogHeader):]

	headings := noteHeadingRE.FindAllStringSubmatchIndex(body, -1)
	for i, h := range headings {
		end := len(body)
		if i+1 < len(headings) {
			end = headings[i+1][0]
		}
		chunk := strings.TrimRight(body[h[0]:end], "\n")

		num, err := strconv.Atoi(body[h[2]:h[3]])
		if err != nil {
			continue
		}
		createdAt := body[h[4]:h[5]]

		// chunk = "### Note N — ts\nStatus: ...\n\n<text>"
		lines := strings.SplitN(chunk, "\n", 3)
		status := "active"
		text := ""
		if len(lines) >= 2 {
			status = strings.TrimPrefix(lines[1], "Status: ")
		}
		if len(lines) >= 3 {
			text = strings.TrimSpace(lines[2])
		}
		notes = append(notes, guidanceNote{Number: num, CreatedAt: createdAt, Status: status, Text: text})
	}
	return base, notes
}

// renderGuidanceLog reassembles base prose and notes back into a single
// full_text string, in the same format parseGuidanceLog reads.
func renderGuidanceLog(base string, notes []guidanceNote) string {
	if len(notes) == 0 {
		return base
	}
	var sb strings.Builder
	sb.WriteString(base)
	sb.WriteString(guidanceLogHeader)
	for i, n := range notes {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "### Note %d — %s\nStatus: %s\n\n%s", n.Number, n.CreatedAt, n.Status, n.Text)
	}
	return sb.String()
}

// highestNoteNumber returns the highest note number present, or 0 if none.
func highestNoteNumber(notes []guidanceNote) int {
	highest := 0
	for _, n := range notes {
		if n.Number > highest {
			highest = n.Number
		}
	}
	return highest
}

// applyGuidance appends a new human guidance note (if note is non-empty)
// and/or marks an earlier note superseded or retracted (if supersedes > 0),
// returning the updated full_text and the new note's number (0 if none was
// added). supersedes referencing a note number that doesn't exist is an
// error rather than a silent no-op — a typo'd note number should not leave a
// human believing their retraction took effect when it didn't.
func applyGuidance(fullText, note string, supersedes int, createdAt string) (updatedFullText string, newNoteNumber int, err error) {
	if note == "" && supersedes == 0 {
		return fullText, 0, nil
	}

	base, notes := parseGuidanceLog(fullText)

	if supersedes > 0 {
		found := false
		for i := range notes {
			if notes[i].Number == supersedes {
				found = true
				break
			}
		}
		if !found {
			return "", 0, fmt.Errorf("--supersedes %d does not match any existing guidance note", supersedes)
		}
	}

	newNum := highestNoteNumber(notes) + 1

	if supersedes > 0 {
		status := "retracted"
		if note != "" {
			status = fmt.Sprintf("superseded by Note %d", newNum)
		}
		for i := range notes {
			if notes[i].Number == supersedes {
				notes[i].Status = status
			}
		}
	}

	if note == "" {
		return renderGuidanceLog(base, notes), 0, nil
	}

	notes = append(notes, guidanceNote{
		Number:    newNum,
		CreatedAt: createdAt,
		Status:    "active",
		Text:      note,
	})
	return renderGuidanceLog(base, notes), newNum, nil
}
