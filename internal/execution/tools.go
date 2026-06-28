package execution

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ratchet/internal/ollama"
)

// toolDefinitions returns the three tools available to the EXECUTE_BEAD agent.
func toolDefinitions() []ollama.Tool {
	return []ollama.Tool{
		{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        "write_file",
				Description: "Create or overwrite a file in the project directory. Path must be relative to the project root.",
				Parameters: ollama.ToolParameters{
					Type: "object",
					Properties: map[string]ollama.ToolProperty{
						"path":    {Type: "string", Description: "File path relative to project root"},
						"content": {Type: "string", Description: "File content to write"},
					},
					Required: []string{"path", "content"},
				},
			},
		},
		{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        "read_file",
				Description: "Read a file from the project directory.",
				Parameters: ollama.ToolParameters{
					Type: "object",
					Properties: map[string]ollama.ToolProperty{
						"path": {Type: "string", Description: "File path relative to project root"},
					},
					Required: []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        "run_command",
				Description: "Run a shell command with the project directory as the working directory. Returns stdout, stderr, and exit code.",
				Parameters: ollama.ToolParameters{
					Type: "object",
					Properties: map[string]ollama.ToolProperty{
						"command": {Type: "string", Description: "Shell command to execute"},
					},
					Required: []string{"command"},
				},
			},
		},
	}
}

// executeTool dispatches a ToolCall and returns the result as a string suitable
// for feeding back to the model as a tool message.
func executeTool(ctx context.Context, tc ollama.ToolCall, projectFolder string) string {
	name := tc.Function.Name
	args := tc.Function.Arguments

	switch name {
	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		return toolWriteFile(path, content, projectFolder)
	case "read_file":
		path, _ := args["path"].(string)
		return toolReadFile(path, projectFolder)
	case "run_command":
		command, _ := args["command"].(string)
		return toolRunCommand(ctx, command, projectFolder)
	default:
		return fmt.Sprintf("error: unknown tool %q", name)
	}
}

// safePath resolves relPath within projectFolder and rejects path traversal.
func safePath(relPath, projectFolder string) (string, error) {
	abs := filepath.Join(projectFolder, relPath)
	clean := filepath.Clean(projectFolder)
	if abs != clean && !strings.HasPrefix(abs, clean+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes project folder", relPath)
	}
	return abs, nil
}

func toolWriteFile(path, content, projectFolder string) string {
	if path == "" {
		return "error: write_file requires a 'path' argument specifying the filename (e.g. path=\"game.go\"); no path was provided"
	}
	abs, err := safePath(path, projectFolder)
	if err != nil {
		return "error: " + err.Error()
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Sprintf("error: create directories: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return fmt.Sprintf("error: write file: %v", err)
	}
	return fmt.Sprintf("ok: wrote %d bytes to %s", len(content), path)
}

func toolReadFile(path, projectFolder string) string {
	abs, err := safePath(path, projectFolder)
	if err != nil {
		return "error: " + err.Error()
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Sprintf("error: read file: %v", err)
	}
	return string(b)
}

func toolRunCommand(ctx context.Context, command, projectFolder string) string {
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "bash", "-c", command)
	cmd.Dir = projectFolder
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	var sb strings.Builder
	if stdout.Len() > 0 {
		sb.WriteString("stdout:\n")
		sb.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("stderr:\n")
		sb.WriteString(stderr.String())
	}
	if sb.Len() > 0 {
		sb.WriteByte('\n')
	}
	if err != nil {
		sb.WriteString("exit: " + err.Error())
	} else {
		sb.WriteString("exit: 0")
	}
	return sb.String()
}
