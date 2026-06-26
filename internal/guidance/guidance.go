// Package guidance provides language-specific prompt guidance injected into verb
// system prompts when a project's language can be detected from the workspace.
//
// Guidance files are loaded at runtime from a "guidance/" directory located
// alongside the ratchet binary (e.g. ratchet-projects/guidance/go.md). This
// means guidance can be edited without rebuilding — restart ratchet to pick up
// changes. If a file is missing or unreadable, the verb receives no guidance
// (silent no-op, not an error).
package guidance

import (
	"os"
	"path/filepath"
)

// Inject appends language-specific guidance to a system prompt and returns the
// result. If no language can be detected from folderPath, or the guidance file
// is missing, the prompt is returned unchanged.
func Inject(systemPrompt, folderPath string) string {
	g := load(folderPath)
	if g == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\n## Language-Specific Guidance\n\n" + g
}

func load(folderPath string) string {
	lang := detect(folderPath)
	if lang == "" {
		return ""
	}
	content, err := os.ReadFile(guidanceFilePath(lang))
	if err != nil {
		return ""
	}
	return string(content)
}

// guidanceFilePath returns the path to the guidance file for the given language.
// Files live in a "guidance/" directory alongside the running binary.
func guidanceFilePath(lang string) string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "guidance", lang+".md")
}

func detect(folderPath string) string {
	if exists(filepath.Join(folderPath, "go.mod")) {
		return "go"
	}
	if exists(filepath.Join(folderPath, "requirements.txt")) ||
		exists(filepath.Join(folderPath, "setup.py")) ||
		exists(filepath.Join(folderPath, "pyproject.toml")) {
		return "python"
	}
	if exists(filepath.Join(folderPath, "composer.json")) {
		return "php"
	}
	if exists(filepath.Join(folderPath, "package.json")) {
		return "javascript"
	}
	if exists(filepath.Join(folderPath, "Cargo.toml")) {
		return "rust"
	}
	return ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
