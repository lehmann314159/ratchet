package main

import (
	"bytes"
	"net/http"
	"strings"
)

type HistoryEntry struct {
	Input    string
	Output   string
	Err      string
	Bytecode string
}

type PageData struct {
	History []HistoryEntry
}

func HandleIndex(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	data := PageData{History: history}
	mu.Unlock()
	RenderIndex(w, data)
}

func HandleEval(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()

	if err := r.ParseForm(); err != nil {
		// ignore parse error
	}
	input := r.FormValue("input")

	entry := HistoryEntry{Input: input}

	if input == "" {
		entry.Err = "input was empty"
		history = append(history, entry)
		RenderRepl(w, PageData{History: history})
		return
	}

	parser := NewParser(input)
	prog, err := parser.ParseProgram()
	if err != nil {
		entry.Err = err.Error()
		entry.Bytecode = ""
		history = append(history, entry)
		RenderRepl(w, PageData{History: history})
		return
	}

	stmt := prog.Statements[0]
	cp, err := Compile(stmt, env)
	if err != nil {
		entry.Err = err.Error()
		entry.Bytecode = ""
		history = append(history, entry)
		RenderRepl(w, PageData{History: history})
		return
	}

	bytecodeLines := Disassemble(cp, env)
	bytecodeText := strings.Join(bytecodeLines, "\n")
	entry.Bytecode = bytecodeText

	vm := NewVM()
	outBuf := &bytes.Buffer{}
	err = vm.Run(cp, env, outBuf)
	if err != nil {
		entry.Err = err.Error()
	} else {
		entry.Output = strings.TrimSuffix(outBuf.String(), "\n")
	}

	history = append(history, entry)
	RenderRepl(w, PageData{History: history})
}
