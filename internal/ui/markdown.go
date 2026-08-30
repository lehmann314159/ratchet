package ui

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// md renders GitHub-flavored Markdown to HTML. Raw HTML in the source is
// escaped, not passed through (goldmark's default) — the inputs here are
// generated reports and rewind manifests, but a trace excerpt embedded in one
// can contain arbitrary model output, so unsafe passthrough is never enabled.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// renderMarkdown converts src to HTML for direct template embedding. On a
// render error it falls back to the escaped raw text in a <pre> so the page
// still shows the content.
func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "<pre>" + template.HTML(template.HTMLEscapeString(src)) + "</pre>" //nolint:gosec // escaped above
	}
	return template.HTML(buf.String()) //nolint:gosec // goldmark escapes raw HTML by default
}
