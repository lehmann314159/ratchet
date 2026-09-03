package main

import "fmt"

type TokenType int

const (
	TokenNumber TokenType = iota
	TokenIdent
	TokenPlus
	TokenMinus
	TokenStar
	TokenSlash
	TokenLParen
	TokenRParen
	TokenAssign
	TokenPrint
	TokenNewline
	TokenEOF
)

type Token struct {
	Type TokenType
	Text string
}

type Lexer struct {
	source string
	pos    int
}

func NewLexer(source string) *Lexer {
	return &Lexer{source: source, pos: 0}
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isLetterOrUnderscore(b byte) bool {
	return isLetter(b) || b == '_'
}

func isLetterOrDigitOrUnderscore(b byte) bool {
	return isLetter(b) || isDigit(b) || b == '_'
}

func (l *Lexer) Next() (Token, error) {
	// 1. Skip spaces/tabs
	for l.pos < len(l.source) && (l.source[l.pos] == ' ' || l.source[l.pos] == '\t') {
		l.pos++
	}
	if l.pos >= len(l.source) {
		return Token{Type: TokenEOF, Text: ""}, nil
	}

	ch := l.source[l.pos]

	// 2. Newline
	if ch == '\n' {
		l.pos++
		return Token{Type: TokenNewline, Text: "\n"}, nil
	}

	// 3. Digits
	if isDigit(ch) {
		start := l.pos
		for l.pos < len(l.source) && isDigit(l.source[l.pos]) {
			l.pos++
		}
		text := l.source[start:l.pos]
		return Token{Type: TokenNumber, Text: text}, nil
	}

	// 4. 'print' keyword
	if l.pos+5 <= len(l.source) && l.source[l.pos:l.pos+5] == "print" {
		nextPos := l.pos + 5
		if nextPos == len(l.source) || !isLetterOrDigitOrUnderscore(l.source[nextPos]) {
			l.pos += 5
			return Token{Type: TokenPrint, Text: "print"}, nil
		}
	}

	// 5. Identifiers
	if isLetterOrUnderscore(ch) {
		start := l.pos
		l.pos++
		for l.pos < len(l.source) && isLetterOrDigitOrUnderscore(l.source[l.pos]) {
			l.pos++
		}
		text := l.source[start:l.pos]
		return Token{Type: TokenIdent, Text: text}, nil
	}

	// 6. Single characters
	switch ch {
	case '+':
		l.pos++
		return Token{Type: TokenPlus, Text: "+"}, nil
	case '-':
		l.pos++
		return Token{Type: TokenMinus, Text: "-"}, nil
	case '*':
		l.pos++
		return Token{Type: TokenStar, Text: "*"}, nil
	case '/':
		l.pos++
		return Token{Type: TokenSlash, Text: "/"}, nil
	case '(':
		l.pos++
		return Token{Type: TokenLParen, Text: "("}, nil
	case ')':
		l.pos++
		return Token{Type: TokenRParen, Text: ")"}, nil
	case '=':
		l.pos++
		return Token{Type: TokenAssign, Text: "="}, nil
	}

	// 7. EOF already handled
	// 8. Error - MUTANT: advances position past the offending character.
	l.pos++
	return Token{}, fmt.Errorf("unexpected character: %c", ch)
}
