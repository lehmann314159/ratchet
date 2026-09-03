package main

import (
	"fmt"
	"strconv"
)

type Program struct {
	Statements []Stmt
}

type Stmt interface {
	isStmt()
}

type AssignStmt struct {
	Name  string
	Value Expr
}

func (AssignStmt) isStmt() {}

type PrintStmt struct {
	Value Expr
}

func (PrintStmt) isStmt() {}

type ExprStmt struct {
	Value Expr
}

func (ExprStmt) isStmt() {}

type Expr interface {
	isExpr()
}

type NumberExpr struct {
	Value int64
}

func (NumberExpr) isExpr() {}

type VarExpr struct {
	Name string
}

func (VarExpr) isExpr() {}

type UnaryExpr struct {
	Op    TokenType
	Value Expr
}

func (UnaryExpr) isExpr() {}

type BinaryExpr struct {
	Op    TokenType
	Left  Expr
	Right Expr
}

func (BinaryExpr) isExpr() {}

type Parser struct {
	lexer   *Lexer
	cur     Token
	peek    Token
	hasPeek bool
}

func NewParser(source string) *Parser {
	return &Parser{lexer: NewLexer(source)}
}

func (p *Parser) ParseProgram() (*Program, error) {
	tok, err := p.lexer.Next()
	if err != nil {
		return nil, err
	}
	p.cur = tok

	// skip leading newlines
	for p.cur.Type == TokenNewline {
		tok, err = p.lexer.Next()
		if err != nil {
			return nil, err
		}
		p.cur = tok
	}
	if p.cur.Type == TokenEOF {
		return nil, fmt.Errorf("empty submission")
	}

	stmt, err := p.parseStatement()
	if err != nil {
		return nil, err
	}

	// exactly one statement, allow single trailing newline
	if p.cur.Type == TokenNewline {
		tok, err = p.lexer.Next()
		if err != nil {
			return nil, err
		}
		p.cur = tok
		if p.cur.Type != TokenEOF {
			return nil, fmt.Errorf("extra tokens after statement")
		}
	} else if p.cur.Type != TokenEOF {
		return nil, fmt.Errorf("extra tokens after statement")
	}

	return &Program{Statements: []Stmt{stmt}}, nil
}

func (p *Parser) advance() error {
	if p.hasPeek {
		p.cur = p.peek
		p.hasPeek = false
		return nil
	}
	tok, err := p.lexer.Next()
	if err != nil {
		return err
	}
	p.cur = tok
	return nil
}

func (p *Parser) peekToken() (Token, error) {
	if p.hasPeek {
		return p.peek, nil
	}
	tok, err := p.lexer.Next()
	if err != nil {
		return Token{}, err
	}
	p.peek = tok
	p.hasPeek = true
	return tok, nil
}

func (p *Parser) parseStatement() (Stmt, error) {
	switch p.cur.Type {
	case TokenPrint:
		if err := p.advance(); err != nil {
			return nil, err
		}
		if p.cur.Type != TokenLParen {
			return nil, fmt.Errorf("expected '(' after print")
		}
		if err := p.advance(); err != nil {
			return nil, err
		}
		expr, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if p.cur.Type != TokenRParen {
			return nil, fmt.Errorf("expected ')' after print expression")
		}
		if err := p.advance(); err != nil {
			return nil, err
		}
		return PrintStmt{Value: expr}, nil
	case TokenIdent:
		nextTok, err := p.peekToken()
		if err != nil {
			return nil, err
		}
		if nextTok.Type == TokenAssign {
			name := p.cur.Text
			if err := p.advance(); err != nil {
				return nil, err
			}
			if p.cur.Type != TokenAssign {
				return nil, fmt.Errorf("expected '=' in assignment")
			}
			if err := p.advance(); err != nil {
				return nil, err
			}
			expr, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			return AssignStmt{Name: name, Value: expr}, nil
		}
		// fall through to expression statement
		fallthrough
	default:
		expr, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		return ExprStmt{Value: expr}, nil
	}
}

func (p *Parser) parseExpression() (Expr, error) {
	left, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	for p.cur.Type == TokenPlus || p.cur.Type == TokenMinus {
		op := p.cur.Type
		if err := p.advance(); err != nil {
			return nil, err
		}
		right, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: op, Left: left, Right: right}
	}
	return left, nil
}

func (p *Parser) parseTerm() (Expr, error) {
	left, err := p.parseFactor()
	if err != nil {
		return nil, err
	}
	for p.cur.Type == TokenStar || p.cur.Type == TokenSlash {
		op := p.cur.Type
		if err := p.advance(); err != nil {
			return nil, err
		}
		right, err := p.parseFactor()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: op, Left: left, Right: right}
	}
	return left, nil
}

func (p *Parser) parseFactor() (Expr, error) {
	switch p.cur.Type {
	case TokenNumber:
		text := p.cur.Text
		val, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("numeric literal overflow")
		}
		if err := p.advance(); err != nil {
			return nil, err
		}
		return &NumberExpr{Value: val}, nil
	case TokenIdent:
		name := p.cur.Text
		if err := p.advance(); err != nil {
			return nil, err
		}
		return &VarExpr{Name: name}, nil
	case TokenMinus:
		if err := p.advance(); err != nil {
			return nil, err
		}
		val, err := p.parseFactor()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{Op: TokenMinus, Value: val}, nil
	case TokenLParen:
		if err := p.advance(); err != nil {
			return nil, err
		}
		expr, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if p.cur.Type != TokenRParen {
			return nil, fmt.Errorf("expected ')'")
		}
		if err := p.advance(); err != nil {
			return nil, err
		}
		return expr, nil
	default:
		return nil, fmt.Errorf("unexpected token %v", p.cur.Type)
	}
}
