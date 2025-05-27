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
	//if uv.IsClosed() {
	if uv.closed || uv.reg == nil {
		return uv.value
	}
	//return uv.reg.Get(uv.index)
	return uv.reg.array[uv.index]
}

func (uv *Upvalue) SetValue(value LValue) {
	if uv.IsClosed() {
		uv.value = value
	} else {
		uv.reg.Set(uv.index, value)
	}
}

func (uv *Upvalue) Close() {
	value := uv.Value()
	uv.closed = true
	uv.value = value
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
		Code:               make([]uint32, 0, 128),
		Constants:          make([]LValue, 0, 32),
		FunctionPrototypes: make([]*FunctionProto, 0, 16),

		DbgSourcePositions: make([]int, 0, 128),
		DbgLocals:          make([]*DbgLocalInfo, 0, 16),
		DbgCalls:           make([]DbgCall, 0, 128),
		DbgUpvalues:        make([]string, 0, 16),

		stringConstants: make([]string, 0, 32),
	}
}

func (fp *FunctionProto) String() string {
	return fp.str(1, 0)
}

func (fp *FunctionProto) str(level int, count int) string {
	indent := strings.Repeat("  ", level-1)
	buf := []string{}
	buf = append(buf, fmt.Sprintf("%v; function [%v] definition (level %v)\n",
		indent, count, level))
	buf = append(buf, fmt.Sprintf("%v; %v upvalues, %v params, %v stacks\n",
		indent, fp.NumUpvalues, fp.NumParameters, fp.NumUsedRegisters))
	for reg, linfo := range fp.DbgLocals {
		buf = append(buf, fmt.Sprintf("%v.local %v ; %v\n", indent, linfo.Name, reg))
	}
	for reg, upvalue := range fp.DbgUpvalues {
		buf = append(buf, fmt.Sprintf("%v.upvalue %v ; %v\n", indent, upvalue, reg))
	}
	for reg, conzt := range fp.Constants {
		buf = append(buf, fmt.Sprintf("%v.const %v ; %v\n", indent, conzt.String(), reg))
	}
	buf = append(buf, "\n")

	protono := 0
	for no, code := range fp.Code {
		inst := opGetOpCode(code)
		if inst == OP_CLOSURE {
			buf = append(buf, "\n")
			buf = append(buf, fp.FunctionPrototypes[protono].str(level+1, protono))
			buf = append(buf, "\n")
			protono++
		}
		buf = append(buf, fmt.Sprintf("%v[%03d] %v (line:%v)\n",
			indent, no+1, opToString(code), fp.DbgSourcePositions[no]))

	}
	buf = append(buf, fmt.Sprintf("%v; end of function\n", indent))
	return strings.Join(buf, "")
}

/* }}} */

/* LFunction {{{ */

func newLFunctionL(proto *FunctionProto, env *LTable, nupvalue int) *LFunction {
	return &LFunction{
		IsG: false,
		Env: env,

		Proto:     proto,
		GFunction: nil,
		Upvalues:  make([]*Upvalue, nupvalue),
	}
}

func newLFunctionG(gfunc LGFunction, env *LTable, nupvalue int) *LFunction {
	return &LFunction{
		IsG: true,
		Env: env,

		Proto:     nil,
		GFunction: gfunc,
		Upvalues:  make([]*Upvalue, nupvalue),
	}
}

func (fn *LFunction) LocalName(regno, pc int) (string, bool) {
	if fn.IsG {
		return "", false
	}
	p := fn.Proto
	for i := 0; i < len(p.DbgLocals) && p.DbgLocals[i].StartPc < pc; i++ {
		if pc < p.DbgLocals[i].EndPc {
			regno--
			if regno == 0 {
				return p.DbgLocals[i].Name, true
			}
		}
	}
	return "", false
}

/* }}} */

type CallNode struct {
	Function *FunctionProto
	Calls    map[*FunctionProto]bool
	CalledBy map[*FunctionProto]bool
}

type CallGraph struct {
	Nodes map[*FunctionProto]*CallNode
}

func NewCallGraph() *CallGraph {
	return &CallGraph{
		Nodes: make(map[*FunctionProto]*CallNode),
	}
}

func (cg *CallGraph) AddFunction(fn *FunctionProto) {
	if _, exists := cg.Nodes[fn]; !exists {
		cg.Nodes[fn] = &CallNode{
			Function: fn,
			Calls:    make(map[*FunctionProto]bool),
			CalledBy: make(map[*FunctionProto]bool),
		}
	}
}

func (cg *CallGraph) AddCall(caller, callee *FunctionProto) {
	cg.AddFunction(caller)
	cg.AddFunction(callee)

	cg.Nodes[caller].Calls[callee] = true
	cg.Nodes[callee].CalledBy[caller] = true
}

func (cg *CallGraph) BuildFromProto(proto *FunctionProto) {
	cg.AddFunction(proto)

	for _, childProto := range proto.FunctionPrototypes {
		cg.AddCall(proto, childProto)
		cg.BuildFromProto(childProto)
	}
}

func (cg *CallGraph) GetCallers(fn *FunctionProto) []*FunctionProto {
	if node, exists := cg.Nodes[fn]; exists {
		callers := make([]*FunctionProto, 0, len(node.CalledBy))
		for caller := range node.CalledBy {
			callers = append(callers, caller)
		}
		return callers
	}
	return nil
}

func (cg *CallGraph) GetCallees(fn *FunctionProto) []*FunctionProto {
	if node, exists := cg.Nodes[fn]; exists {
		callees := make([]*FunctionProto, 0, len(node.Calls))
		for callee := range node.Calls {
			callees = append(callees, callee)
		}
		return callees
	}
	return nil
}

func (cg *CallGraph) FindHotPaths(threshold int) []*FunctionProto {
	hotFuncs := make([]*FunctionProto, 0)
	for _, node := range cg.Nodes {
		if len(node.CalledBy) >= threshold {
			hotFuncs = append(hotFuncs, node.Function)
		}
	}
	return hotFuncs
}

func (cg *CallGraph) FindUnusedFunctions() []*FunctionProto {
	unused := make([]*FunctionProto, 0)
	for _, node := range cg.Nodes {
		if len(node.CalledBy) == 0 && len(node.Calls) > 0 {
			unused = append(unused, node.Function)
		}
	}
	return unused
}

func (cg *CallGraph) FindRecursiveFunctions() []*FunctionProto {
	recursive := make([]*FunctionProto, 0)
	visited := make(map[*FunctionProto]bool)

	var dfs func(fn *FunctionProto, path map[*FunctionProto]bool)
	dfs = func(fn *FunctionProto, path map[*FunctionProto]bool) {
		if path[fn] {
			recursive = append(recursive, fn)
			return
		}
		if visited[fn] {
			return
		}

		visited[fn] = true
		path[fn] = true

		for callee := range cg.Nodes[fn].Calls {
			dfs(callee, path)
		}

		delete(path, fn)
	}

	for _, node := range cg.Nodes {
		if !visited[node.Function] {
			dfs(node.Function, make(map[*FunctionProto]bool))
		}
	}

	return recursive
}

func (cg *CallGraph) Optimize() {
	hotPaths := cg.FindHotPaths(5)
	recursive := cg.FindRecursiveFunctions()

	for _, fn := range hotPaths {
		if fn.NumUsedRegisters > 0 {
			fn.NumUsedRegisters = optimizeRegisterUsage(fn)
		}
	}

	for _, fn := range recursive {
		optimizeRecursiveFunction(fn)
	}
}

func optimizeRegisterUsage(fn *FunctionProto) uint8 {
	usedRegs := make(map[int]bool)
	for _, code := range fn.Code {
		inst := opGetOpCode(code)
		if inst == OP_GETUPVAL || inst == OP_SETUPVAL {
			usedRegs[int(code&0xFF)] = true
		}
	}
	return uint8(len(usedRegs))
}

func optimizeRecursiveFunction(fn *FunctionProto) {
	if fn.NumParameters > 0 {
		fn.NumParameters = optimizeParameterCount(fn)
	}
}

func optimizeParameterCount(fn *FunctionProto) uint8 {
	usedParams := make(map[int]bool)
	for _, code := range fn.Code {
		inst := opGetOpCode(code)
		if inst == OP_GETUPVAL || inst == OP_SETUPVAL {
			usedParams[int(code&0xFF)] = true
		}
	}
	return uint8(len(usedParams))
}
