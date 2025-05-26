package lua

import (
	"fmt"
	"strings"
)

const (
	VarArgHasArg   uint8 = 1
	VarArgIsVarArg uint8 = 2
	VarArgNeedsArg uint8 = 4
)

type DbgLocalInfo struct {
	Name    string
	StartPc int
	EndPc   int
}

type DbgCall struct {
	Name string
	Pc   int
}

type FunctionProto struct {
	SourceName         string
	LineDefined        int
	LastLineDefined    int
	NumUpvalues        uint8
	NumParameters      uint8
	IsVarArg           uint8
	NumUsedRegisters   uint8
	Code               []uint32
	Constants          []LValue
	FunctionPrototypes []*FunctionProto

	DbgSourcePositions []int
	DbgLocals          []*DbgLocalInfo
	DbgCalls           []DbgCall
	DbgUpvalues        []string

	stringConstants []string
}

/* Upvalue {{{ */

type Upvalue struct {
	next   *Upvalue
	reg    *registry
	index  int
	value  LValue
	closed bool
}

func (uv *Upvalue) Value() LValue {
	if uv.closed || uv.reg == nil {
		return uv.value
	}
	return uv.reg.array[uv.index]
}

func (uv *Upvalue) SetValue(value LValue) {
	if uv.closed || uv.reg == nil {
		uv.value = value
		return
	}
	uv.reg.array[uv.index] = value
	if uv.index >= uv.reg.top {
		uv.reg.top = uv.index + 1
	}
}

func (uv *Upvalue) Close() {
	if !uv.closed && uv.reg != nil {
		uv.value = uv.reg.array[uv.index]
		uv.closed = true
		uv.reg = nil
	}
}

func (uv *Upvalue) IsClosed() bool {
	return uv.closed || uv.reg == nil
}

func UpvalueIndex(i int) int {
	return GlobalsIndex - i
}

/* }}} */

/* FunctionProto {{{ */

func newFunctionProto(name string) *FunctionProto {
	return &FunctionProto{
		SourceName:         name,
		LineDefined:        0,
		LastLineDefined:    0,
		NumUpvalues:        0,
		NumParameters:      0,
		IsVarArg:           0,
		NumUsedRegisters:   2,
		Code:               make([]uint32, 0, 256),
		Constants:          make([]LValue, 0, 64),
		FunctionPrototypes: make([]*FunctionProto, 0, 32),
		DbgSourcePositions: make([]int, 0, 256),
		DbgLocals:          make([]*DbgLocalInfo, 0, 32),
		DbgCalls:           make([]DbgCall, 0, 256),
		DbgUpvalues:        make([]string, 0, 32),
		stringConstants:    make([]string, 0, 64),
	}
}

func (fp *FunctionProto) String() string {
	return fp.str(1, 0)
}

func (fp *FunctionProto) str(level int, count int) string {
	var builder strings.Builder
	indent := strings.Repeat("  ", level-1)

	builder.WriteString(fmt.Sprintf("%v; function [%v] definition (level %v)\n", indent, count, level))
	builder.WriteString(fmt.Sprintf("%v; %v upvalues, %v params, %v stacks\n",
		indent, fp.NumUpvalues, fp.NumParameters, fp.NumUsedRegisters))

	for reg, linfo := range fp.DbgLocals {
		builder.WriteString(fmt.Sprintf("%v.local %v ; %v\n", indent, linfo.Name, reg))
	}

	for reg, upvalue := range fp.DbgUpvalues {
		builder.WriteString(fmt.Sprintf("%v.upvalue %v ; %v\n", indent, upvalue, reg))
	}

	for reg, conzt := range fp.Constants {
		builder.WriteString(fmt.Sprintf("%v.const %v ; %v\n", indent, conzt.String(), reg))
	}

	builder.WriteString("\n")

	protono := 0
	for no, code := range fp.Code {
		inst := opGetOpCode(code)
		if inst == OP_CLOSURE {
			builder.WriteString("\n")
			builder.WriteString(fp.FunctionPrototypes[protono].str(level+1, protono))
			builder.WriteString("\n")
			protono++
		}
		builder.WriteString(fmt.Sprintf("%v[%03d] %v (line:%v)\n",
			indent, no+1, opToString(code), fp.DbgSourcePositions[no]))
	}

	builder.WriteString(fmt.Sprintf("%v; end of function\n", indent))
	return builder.String()
}

/* }}} */

/* LFunction {{{ */

func newLFunctionL(proto *FunctionProto, env *LTable, nupvalue int) *LFunction {
	return &LFunction{
		IsG:       false,
		Env:       env,
		Proto:     proto,
		GFunction: nil,
		Upvalues:  make([]*Upvalue, nupvalue, nupvalue),
	}
}

func newLFunctionG(gfunc LGFunction, env *LTable, nupvalue int) *LFunction {
	return &LFunction{
		IsG:       true,
		Env:       env,
		Proto:     nil,
		GFunction: gfunc,
		Upvalues:  make([]*Upvalue, nupvalue, nupvalue),
	}
}

func (fn *LFunction) LocalName(regno, pc int) (string, bool) {
	if fn.IsG || fn.Proto == nil {
		return "", false
	}

	locals := fn.Proto.DbgLocals
	for i := 0; i < len(locals); i++ {
		linfo := locals[i]
		if pc < linfo.StartPc {
			continue
		}
		if pc >= linfo.EndPc {
			continue
		}
		regno--
		if regno == 0 {
			return linfo.Name, true
		}
	}
	return "", false
}

/* }}} */
