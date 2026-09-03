package main

import "fmt"

type OpCode int

const (
	OpPushConst OpCode = iota
	OpLoad
	OpStore
	OpAdd
	OpSub
	OpMul
	OpDiv
	OpNeg
	OpPrint
)

type Instruction struct {
	Op      OpCode
	Operand int64
}

type CompiledProgram struct {
	Instructions []Instruction
}

func Compile(stmt Stmt, env *Environment) (*CompiledProgram, error) {
	var instructions []Instruction
	if err := compileStmt(stmt, env, &instructions); err != nil {
		return nil, err
	}
	return &CompiledProgram{Instructions: instructions}, nil
}

func compileStmt(stmt Stmt, env *Environment, out *[]Instruction) error {
	switch s := stmt.(type) {
	case AssignStmt:
		if err := compileExpr(s.Value, env, out); err != nil {
			return err
		}
		if _, ok := env.Slots[s.Name]; !ok {
			slot := len(env.Values)
			env.Slots[s.Name] = slot
			env.Values = append(env.Values, 0)
		}
		slot := env.Slots[s.Name]
		*out = append(*out, Instruction{Op: OpStore, Operand: int64(slot)})
	case PrintStmt:
		if err := compileExpr(s.Value, env, out); err != nil {
			return err
		}
		*out = append(*out, Instruction{Op: OpPrint})
	case ExprStmt:
		if err := compileExpr(s.Value, env, out); err != nil {
			return err
		}
		*out = append(*out, Instruction{Op: OpPrint})
	default:
		return fmt.Errorf("unknown statement type")
	}
	return nil
}

func compileExpr(expr Expr, env *Environment, out *[]Instruction) error {
	switch e := expr.(type) {
	case *NumberExpr:
		*out = append(*out, Instruction{Op: OpPushConst, Operand: e.Value})
	case NumberExpr:
		*out = append(*out, Instruction{Op: OpPushConst, Operand: e.Value})
	case *VarExpr:
		slot, ok := env.Slots[e.Name]
		if !ok {
			return fmt.Errorf("undefined variable %s", e.Name)
		}
		*out = append(*out, Instruction{Op: OpLoad, Operand: int64(slot)})
	case VarExpr:
		slot, ok := env.Slots[e.Name]
		if !ok {
			return fmt.Errorf("undefined variable %s", e.Name)
		}
		*out = append(*out, Instruction{Op: OpLoad, Operand: int64(slot)})
	case *UnaryExpr:
		if err := compileExpr(e.Value, env, out); err != nil {
			return err
		}
		if e.Op != TokenMinus {
			return fmt.Errorf("unsupported unary operator")
		}
		*out = append(*out, Instruction{Op: OpNeg})
	case UnaryExpr:
		if err := compileExpr(e.Value, env, out); err != nil {
			return err
		}
		if e.Op != TokenMinus {
			return fmt.Errorf("unsupported unary operator")
		}
		*out = append(*out, Instruction{Op: OpNeg})
	case *BinaryExpr:
		if err := compileExpr(e.Left, env, out); err != nil {
			return err
		}
		if err := compileExpr(e.Right, env, out); err != nil {
			return err
		}
		var op OpCode
		switch e.Op {
		case TokenPlus:
			op = OpAdd
		case TokenMinus:
			op = OpSub
		case TokenStar:
			op = OpMul
		case TokenSlash:
			op = OpDiv
		default:
			return fmt.Errorf("unsupported binary operator")
		}
		*out = append(*out, Instruction{Op: op})
	case BinaryExpr:
		if err := compileExpr(e.Left, env, out); err != nil {
			return err
		}
		if err := compileExpr(e.Right, env, out); err != nil {
			return err
		}
		var op OpCode
		switch e.Op {
		case TokenPlus:
			op = OpAdd
		case TokenMinus:
			op = OpSub
		case TokenStar:
			op = OpMul
		case TokenSlash:
			op = OpDiv
		default:
			return fmt.Errorf("unsupported binary operator")
		}
		*out = append(*out, Instruction{Op: op})
	default:
		return fmt.Errorf("unknown expression type")
	}
	return nil
}

func Disassemble(cp *CompiledProgram, env *Environment) []string {
	slotToName := make(map[int]string)
	for name, slot := range env.Slots {
		slotToName[slot] = name
	}
	res := make([]string, 0, len(cp.Instructions))
	for _, ins := range cp.Instructions {
		switch ins.Op {
		case OpPushConst:
			res = append(res, fmt.Sprintf("PUSH_CONST %d", ins.Operand))
		case OpLoad:
			name := slotToName[int(ins.Operand)]
			res = append(res, fmt.Sprintf("LOAD %s", name))
		case OpStore:
			name := slotToName[int(ins.Operand)]
			res = append(res, fmt.Sprintf("STORE %s", name))
		case OpAdd:
			res = append(res, "ADD")
		case OpSub:
			res = append(res, "SUB")
		case OpMul:
			res = append(res, "MUL")
		case OpDiv:
			res = append(res, "DIV")
		case OpNeg:
			res = append(res, "NEG")
		case OpPrint:
			res = append(res, "PRINT")
		default:
			res = append(res, "UNKNOWN")
		}
	}
	return res
}
