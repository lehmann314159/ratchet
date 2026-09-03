package main

import (
	"fmt"
	"io"
	"strconv"
)

type VM struct{}

func NewVM() *VM { return &VM{} }

func (vm *VM) Run(cp *CompiledProgram, env *Environment, out io.Writer) error {
	stack := make([]int64, 0, len(cp.Instructions))

	for _, ins := range cp.Instructions {
		switch ins.Op {
		case OpPushConst:
			stack = append(stack, ins.Operand)
		case OpLoad:
			slot := int(ins.Operand)
			if slot < 0 || slot >= len(env.Values) {
				return fmt.Errorf("runtime error: invalid slot")
			}
			stack = append(stack, env.Values[slot])
		case OpStore:
			if len(stack) == 0 {
				return fmt.Errorf("runtime error: stack underflow")
			}
			val := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			slot := int(ins.Operand)
			if slot < 0 || slot >= len(env.Values) {
				return fmt.Errorf("runtime error: invalid slot")
			}
			env.Values[slot] = val
		case OpAdd:
			if len(stack) < 2 {
				return fmt.Errorf("runtime error: stack underflow")
			}
			b := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			a := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			stack = append(stack, a+b)
		case OpSub:
			if len(stack) < 2 {
				return fmt.Errorf("runtime error: stack underflow")
			}
			b := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			a := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			stack = append(stack, a-b)
		case OpMul:
			if len(stack) < 2 {
				return fmt.Errorf("runtime error: stack underflow")
			}
			b := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			a := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			stack = append(stack, a*b)
		case OpDiv:
			if len(stack) < 2 {
				return fmt.Errorf("runtime error: stack underflow")
			}
			b := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			a := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if b == 0 {
				return fmt.Errorf("runtime error: division by zero")
			}
			// Go's native int64 division truncates toward zero, matching pinned cases
			stack = append(stack, a/b)
		case OpNeg:
			if len(stack) == 0 {
				return fmt.Errorf("runtime error: stack underflow")
			}
			a := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			stack = append(stack, -a)
		case OpPrint:
			if len(stack) == 0 {
				return fmt.Errorf("runtime error: stack underflow")
			}
			a := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			_, err := out.Write([]byte(strconv.FormatInt(a, 10)))
			if err != nil {
				return err
			}
			_, err = out.Write([]byte("\n"))
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("runtime error: unknown opcode")
		}
	}
	return nil
}
