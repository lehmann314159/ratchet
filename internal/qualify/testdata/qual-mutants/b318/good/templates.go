package main

import (
	"html/template"
	"net/http"
)

const indexTemplate = `<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>REPL</title>
<script src="https://unpkg.com/htmx.org@1.9.10"></script>
<style>
body { font-family: sans-serif; margin: 20px; }
.error { color: red; }
pre { background: #f0f0f0; padding: 5px; white-space: pre-wrap; }
.entry { margin-bottom: 15px; }
</style>
</head>
<body>
{{template "repl" .}}
</body>
</html>`

const replTemplate = `{{define "repl"}}
<div id="repl-container">
{{range .History}}
<div class="entry">
<div class="input">{{.Input}}</div>
{{if .Bytecode}}<pre>{{.Bytecode}}</pre>{{end}}
{{if .Err}}<div class="error">{{.Err}}</div>{{else if .Output}}<div class="output">{{.Output}}</div>{{end}}
</div>
{{end}}
<div id="latest-entry"></div>
<form hx-post="/eval" hx-target="#repl-container" hx-swap="outerHTML" hx-on::htmx:after-swap="document.getElementById('latest-entry').scrollIntoView()">
<input type="text" name="input" autofocus>
<button type="submit">Run</button>
</form>
</div>
{{end}}`

func InitTemplates() *template.Template {
	t := template.New("index")
	var err error
	t, err = t.Parse(indexTemplate)
	if err != nil {
		panic(err)
	}
	t, err = t.Parse(replTemplate)
	if err != nil {
		panic(err)
	}
	return t
}

func RenderIndex(w http.ResponseWriter, data PageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, "index", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func RenderRepl(w http.ResponseWriter, data PageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, "repl", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
