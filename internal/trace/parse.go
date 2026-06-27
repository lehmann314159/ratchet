package trace

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// CommandResult holds the outcome of one run_command tool call.
type CommandResult struct {
	Turn     int
	Command  string
	Stdout   string
	Stderr   string
	ExitRaw  string // verbatim text after "exit: "; "0" or "exit status N"
	ExitCode int    // 0 on success; N from "exit status N"; -1 if trace truncated
}

// ParsedTrace is the structured result of parsing an execution trace.
type ParsedTrace struct {
	// TerminationMarker is the payload of [terminated: X], "success" for
	// [done — no further tool calls], or "" if the trace is truncated.
	TerminationMarker string
	Commands          []CommandResult
}

const (
	runCommandPrefix = "[tool: run_command map[command:"
	resultMarker     = "[result]"
	doneMarker       = "[done \xe2\x80\x94 no further tool calls]" // em-dash
)

// Parse parses raw trace bytes and returns a ParsedTrace.
func Parse(data []byte) ParsedTrace {
	var pt ParsedTrace
	var pendingCmd string
	var inResult bool
	var resultLines []string
	var currentTurn int

	finalize := func() {
		if pendingCmd == "" || !inResult {
			pendingCmd = ""
			inResult = false
			resultLines = nil
			return
		}
		cr := CommandResult{
			Turn:    currentTurn,
			Command: pendingCmd,
		}
		parseResultLines(resultLines, &cr)
		pt.Commands = append(pt.Commands, cr)
		pendingCmd = ""
		inResult = false
		resultLines = nil
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case isTurnMarker(line):
			finalize()
			currentTurn = parseTurnNumber(line)

		case strings.HasPrefix(line, runCommandPrefix):
			finalize()
			pendingCmd = extractRunCommand(line)

		case strings.HasPrefix(line, "[tool: "):
			// write_file or read_file — discard any pending run_command state
			finalize()
			pendingCmd = "" // already cleared by finalize, but explicit

		case line == resultMarker:
			if pendingCmd != "" {
				inResult = true
				resultLines = nil
			}

		case strings.HasPrefix(line, "[terminated: "):
			finalize()
			pt.TerminationMarker = strings.TrimSuffix(strings.TrimPrefix(line, "[terminated: "), "]")
			return pt

		case line == doneMarker:
			finalize()
			pt.TerminationMarker = "success"
			return pt

		default:
			if inResult {
				resultLines = append(resultLines, line)
			}
		}
	}

	finalize()
	return pt
}

func isTurnMarker(line string) bool {
	if !strings.HasPrefix(line, "[TURN ") || !strings.HasSuffix(line, "]") {
		return false
	}
	inner := line[len("[TURN ") : len(line)-1]
	_, err := strconv.Atoi(inner)
	return err == nil
}

func parseTurnNumber(line string) int {
	inner := line[len("[TURN ") : len(line)-1]
	n, _ := strconv.Atoi(inner)
	return n
}

// extractRunCommand strips the prefix and trailing "]]" to get the bare command.
// The format is: [tool: run_command map[command:CMD]]
// We strip the last "]]" because the outer "[...]" and inner "map[...]" each
// contribute one closing bracket.
func extractRunCommand(line string) string {
	s := strings.TrimPrefix(line, runCommandPrefix)
	// Strip closing "]]" from the end (outer ] closes tool line, inner ] closes map)
	if strings.HasSuffix(s, "]]") {
		s = s[:len(s)-2]
	}
	return s
}

const maxOutputBytes = 500

// parseResultLines fills cr.Stdout, cr.Stderr, cr.ExitRaw, cr.ExitCode
// from the accumulated result lines.
func parseResultLines(lines []string, cr *CommandResult) {
	cr.ExitCode = -1 // default: truncated / not recorded
	cr.ExitRaw = "(truncated)"

	const (
		sNone   = 0
		sStdout = 1
		sStderr = 2
	)
	state := sNone
	var stdout, stderr strings.Builder

	for _, line := range lines {
		switch {
		case line == "stdout:":
			state = sStdout
		case line == "stderr:":
			state = sStderr
		case strings.HasPrefix(line, "exit: "):
			raw := strings.TrimPrefix(line, "exit: ")
			cr.ExitRaw = raw
			if raw == "0" {
				cr.ExitCode = 0
			} else {
				// "exit status N"
				if after, ok := strings.CutPrefix(raw, "exit status "); ok {
					n, err := strconv.Atoi(strings.TrimSpace(after))
					if err == nil {
						cr.ExitCode = n
					} else {
						cr.ExitCode = 1 // non-zero but unparseable
					}
				} else {
					cr.ExitCode = 1
				}
			}
			state = sNone
		default:
			switch state {
			case sStdout:
				if stdout.Len() > 0 {
					stdout.WriteByte('\n')
				}
				stdout.WriteString(line)
			case sStderr:
				if stderr.Len() > 0 {
					stderr.WriteByte('\n')
				}
				stderr.WriteString(line)
			}
		}
	}

	cr.Stdout = truncate(stdout.String(), maxOutputBytes)
	cr.Stderr = truncate(stderr.String(), maxOutputBytes)
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n[truncated]"
}
