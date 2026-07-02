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

// InjectForVerb appends verb-specific and language-specific guidance to a
// system prompt. For SURVEY_SPEC it loads <language>-survey.md; for all other
// verbs it loads <language>.md. Falls back to the base language file when the
// verb-specific file is absent. guidanceDir overrides the default directory
// (exe-dir/guidance/); pass "" to use the default.
func InjectForVerb(systemPrompt, language, verb, guidanceDir string) string {
	if language == "" {
		return systemPrompt
	}
	g := loadForVerb(language, verb, guidanceDir)
	if g == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\n## Language-Specific Guidance\n\n" + g
}

func load(folderPath string) string {
	lang := Detect(folderPath)
	if lang == "" {
		return ""
	}
	content, err := os.ReadFile(guidanceFilePath(lang))
	if err != nil {
		return ""
	}
	return string(content)
}

func loadForVerb(language, verb, guidanceDir string) string {
	dir := guidanceDir
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return ""
		}
		dir = filepath.Join(filepath.Dir(exe), "guidance")
	}

	// For SURVEY_SPEC, try the verb-specific file first (e.g. go-survey.md).
	if verb == "SURVEY_SPEC" {
		verbFile := filepath.Join(dir, language+"-survey.md")
		if data, err := os.ReadFile(verbFile); err == nil {
			return string(data)
		}
	}

	// Fall back to the base language file (e.g. go.md).
	data, err := os.ReadFile(filepath.Join(dir, language+".md"))
	if err != nil {
		return ""
	}
	return string(data)
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

// Detect returns the programming language detected from folderPath by inspecting
// language marker files, or "" if no language can be identified.
func Detect(folderPath string) string {
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
