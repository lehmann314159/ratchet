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
	"strings"
)

// InjectForVerbPath appends verb-specific language guidance to a system prompt.
// It detects the language from folderPath, then loads go-<verb-slug>.md (e.g.
// go-execute-bead.md for EXECUTE_BEAD). If the file is absent or no language
// is detected, the prompt is returned unchanged. guidanceDir overrides the
// default directory (exe-dir/guidance/); pass "" to use the default.
func InjectForVerbPath(systemPrompt, folderPath, verb, guidanceDir string) string {
	lang := Detect(folderPath)
	return InjectForVerb(systemPrompt, lang, verb, guidanceDir)
}

// InjectForVerb appends verb-specific language guidance to a system prompt.
// It loads <language>-<verb-slug>.md (e.g. go-survey-spec.md for SURVEY_SPEC).
// If the file is absent, the prompt is returned unchanged. guidanceDir overrides
// the default directory (exe-dir/guidance/); pass "" to use the default.
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

func loadForVerb(language, verb, guidanceDir string) string {
	dir := guidanceDir
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return ""
		}
		dir = filepath.Join(filepath.Dir(exe), "guidance")
	}

	verbSlug := strings.ToLower(strings.ReplaceAll(verb, "_", "-"))
	data, err := os.ReadFile(filepath.Join(dir, language+"-"+verbSlug+".md"))
	if err != nil {
		return ""
	}
	return string(data)
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
