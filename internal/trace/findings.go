package trace

import (
	"fmt"
	"strings"
)

// GenerateMechanicalFindings produces the mechanical_findings string from
// structured trace data and bead metadata. The result contains no causal
// language by construction — it reports what happened, not why.
func GenerateMechanicalFindings(
	pt ParsedTrace,
	terminationCause string,
	monitorFired, monitorHonored *bool,
	exitCriteria []string,
	outputFileStatus string,
) string {
	var sb strings.Builder

	// Header: termination metadata
	fmt.Fprintf(&sb, "Termination cause: %s\n", terminationCause)
	sb.WriteString("Monitor fired: ")
	switch {
	case monitorFired == nil:
		sb.WriteString("unknown\n")
	case *monitorFired:
		sb.WriteString("yes\n")
		sb.WriteString("Monitor honored: ")
		if monitorHonored == nil {
			sb.WriteString("unknown\n")
		} else if *monitorHonored {
			sb.WriteString("yes (override flag was 'honor')\n")
		} else {
			sb.WriteString("no (override flag was 'ignore')\n")
		}
	default:
		sb.WriteString("no\n")
	}

	// Exit criteria coverage
	if len(exitCriteria) > 0 {
		sb.WriteString("\n## Exit Criteria\n")
		for i, criterion := range exitCriteria {
			fmt.Fprintf(&sb, "\n%d. %s\n", i+1, criterion)
			last := lastRunOf(pt.Commands, criterion)
			if last == nil {
				sb.WriteString("   Not run during this execution.\n")
			} else {
				fmt.Fprintf(&sb, "   Last run: turn %d, exit %s\n", last.Turn, last.ExitRaw)
				if last.Stdout != "" {
					fmt.Fprintf(&sb, "   stdout: %s\n", indentBlock(last.Stdout))
				}
				if last.Stderr != "" {
					fmt.Fprintf(&sb, "   stderr: %s\n", indentBlock(last.Stderr))
				}
				if last.Stdout == "" && last.Stderr == "" {
					sb.WriteString("   (no output)\n")
				}
			}
		}
	}

	// All commands run (chronological) — one line per success, output expanded on failure
	if len(pt.Commands) > 0 {
		sb.WriteString("\n## All Commands Run (chronological)\n")
		for _, cr := range pt.Commands {
			if cr.ExitCode == 0 {
				fmt.Fprintf(&sb, "\nTurn %d: %s → exit 0\n", cr.Turn, cr.Command)
			} else {
				fmt.Fprintf(&sb, "\nTurn %d: %s → %s\n", cr.Turn, cr.Command, cr.ExitRaw)
				if cr.Stdout != "" {
					fmt.Fprintf(&sb, "  stdout:\n%s\n", indentLines(cr.Stdout, "    "))
				}
				if cr.Stderr != "" {
					fmt.Fprintf(&sb, "  stderr:\n%s\n", indentLines(cr.Stderr, "    "))
				}
			}
		}
	} else {
		sb.WriteString("\n## All Commands Run (chronological)\n\nNo commands were run.\n")
	}

	// Output files
	sb.WriteString("\n## Output Files (at analysis time)\n\n")
	sb.WriteString(outputFileStatus)
	sb.WriteByte('\n')

	return sb.String()
}

// lastRunOf returns the last CommandResult whose Command exactly matches
// criterion, or nil if the criterion was never run.
func lastRunOf(cmds []CommandResult, criterion string) *CommandResult {
	for i := len(cmds) - 1; i >= 0; i-- {
		if cmds[i].Command == criterion {
			return &cmds[i]
		}
	}
	return nil
}

func indentBlock(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) == 1 {
		return s
	}
	// Multi-line: put first line on next line with indent
	return "\n    " + strings.Join(lines, "\n    ")
}

func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	return prefix + strings.Join(lines, "\n"+prefix)
}
