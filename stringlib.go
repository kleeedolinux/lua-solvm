package lua

import (
	"fmt"
	"strings"
	"sync"

	"github.com/kleeedolinux/lua-solvm/pm"
)

const emptyLString LString = LString("")

var (
	stringInternCache sync.Map
	stringBuilderPool sync.Pool
	byteBufferPool    sync.Pool
)

func init() {
	stringBuilderPool = sync.Pool{
		New: func() interface{} {
			return new(strings.Builder)
		},
	}
	byteBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 0, 1024)
		},
	}
}

func internString(s string) LString {
	if cached, ok := stringInternCache.Load(s); ok {
		return cached.(LString)
	}
	ls := LString(s)
	stringInternCache.Store(s, ls)
	return ls
}

func OpenString(L *LState) int {
	mod := L.RegisterModule(StringLibName, strFuncs).(*LTable)
	gmatch := L.NewClosure(strGmatch, L.NewFunction(strGmatchIter))
	mod.RawSetString("gmatch", gmatch)
	mod.RawSetString("gfind", gmatch)
	mod.RawSetString("__index", mod)
	L.G.builtinMts[int(LTString)] = mod
	L.Push(mod)
	return 1
}

var strFuncs = map[string]LGFunction{
	"byte":    strByte,
	"char":    strChar,
	"dump":    strDump,
	"find":    strFind,
	"format":  strFormat,
	"gsub":    strGsub,
	"len":     strLen,
	"lower":   strLower,
	"match":   strMatch,
	"rep":     strRep,
	"reverse": strReverse,
	"sub":     strSub,
	"upper":   strUpper,
}

func strByte(L *LState) int {
	str := L.CheckString(1)
	start := L.OptInt(2, 1) - 1
	end := L.OptInt(3, -1)
	l := len(str)
	if start < 0 {
		start = l + start + 1
	}
	if end < 0 {
		end = l + end + 1
	}

	if L.GetTop() == 2 {
		if start < 0 || start >= l {
			return 0
		}
		L.Push(LNumber(str[start]))
		return 1
	}

	start = intMax(start, 0)
	end = intMin(end, l)
	if end < 0 || end <= start || start >= l {
		return 0
	}

	bytes := make([]LNumber, end-start)
	for i := start; i < end; i++ {
		bytes[i-start] = LNumber(str[i])
	}
	for _, b := range bytes {
		L.Push(b)
	}
	return len(bytes)
}

func strChar(L *LState) int {
	top := L.GetTop()
	bytes := make([]byte, top)
	for i := 1; i <= top; i++ {
		bytes[i-1] = uint8(L.CheckInt(i))
	}
	L.Push(internString(string(bytes)))
	return 1
}

func strDump(L *LState) int {
	L.RaiseError("GopherLua does not support the string.dump")
	return 0
}

func strFind(L *LState) int {
	str := L.CheckString(1)
	pattern := L.CheckString(2)
	if len(pattern) == 0 {
		L.Push(LNumber(1))
		L.Push(LNumber(0))
		return 2
	}
	init := luaIndex2StringIndex(str, L.OptInt(3, 1), true)
	plain := false
	if L.GetTop() == 4 {
		plain = LVAsBool(L.Get(4))
	}

	if plain {
		pos := strings.Index(str[init:], pattern)
		if pos < 0 {
			L.Push(LNil)
			return 1
		}
		L.Push(LNumber(init+pos) + 1)
		L.Push(LNumber(init + pos + len(pattern)))
		return 2
	}

	mds, err := pm.Find(pattern, []byte(str), init, 1)
	if err != nil {
		L.RaiseError(err.Error())
	}
	if len(mds) == 0 {
		L.Push(LNil)
		return 1
	}
	md := mds[0]
	L.Push(LNumber(md.Capture(0) + 1))
	L.Push(LNumber(md.Capture(1)))
	for i := 2; i < md.CaptureLength(); i += 2 {
		if md.IsPosCapture(i) {
			L.Push(LNumber(md.Capture(i)))
		} else {
			L.Push(internString(str[md.Capture(i):md.Capture(i+1)]))
		}
	}
	return md.CaptureLength()/2 + 1
}

func strFormat(L *LState) int {
	str := L.CheckString(1)
	args := make([]interface{}, L.GetTop()-1)
	top := L.GetTop()
	for i := 2; i <= top; i++ {
		args[i-2] = L.Get(i)
	}
	npat := strings.Count(str, "%") - strings.Count(str, "%%")
	L.Push(internString(fmt.Sprintf(str, args[:intMin(npat, len(args))]...)))
	return 1
}

func strGsub(L *LState) int {
	str := L.CheckString(1)
	pat := L.CheckString(2)
	L.CheckTypes(3, LTString, LTTable, LTFunction)
	repl := L.CheckAny(3)
	limit := L.OptInt(4, -1)

	mds, err := pm.Find(pat, []byte(str), 0, limit)
	if err != nil {
		L.RaiseError(err.Error())
	}
	if len(mds) == 0 {
		L.SetTop(1)
		L.Push(LNumber(0))
		return 2
	}

	builder := stringBuilderPool.Get().(*strings.Builder)
	defer stringBuilderPool.Put(builder)
	builder.Reset()
	builder.Grow(len(str) + len(mds)*10)

	offset := 0
	for _, match := range mds {
		start, end := match.Capture(0), match.Capture(1)
		builder.WriteString(str[offset:start])

		switch lv := repl.(type) {
		case LString:
			builder.WriteString(string(lv))
		case *LTable:
			idx := 0
			if match.CaptureLength() > 2 {
				idx = 2
			}
			var value LValue
			if match.IsPosCapture(idx) {
				value = L.GetTable(lv, LNumber(match.Capture(idx)))
			} else {
				value = L.GetField(lv, str[match.Capture(idx):match.Capture(idx+1)])
			}
			if !LVIsFalse(value) {
				builder.WriteString(LVAsString(value))
			}
		case *LFunction:
			L.Push(lv)
			nargs := 0
			if match.CaptureLength() > 2 {
				for i := 2; i < match.CaptureLength(); i += 2 {
					if match.IsPosCapture(i) {
						L.Push(LNumber(match.Capture(i)))
					} else {
						L.Push(internString(str[match.Capture(i):match.Capture(i+1)]))
					}
					nargs++
				}
			} else {
				L.Push(internString(str[match.Capture(0):match.Capture(1)]))
				nargs++
			}
			L.Call(nargs, 1)
			ret := L.reg.Pop()
			if !LVIsFalse(ret) {
				builder.WriteString(LVAsString(ret))
			}
		}
		offset = end
	}
	builder.WriteString(str[offset:])

	L.Push(internString(builder.String()))
	L.Push(LNumber(len(mds)))
	return 2
}

type strMatchData struct {
	str     string
	pos     int
	matches []*pm.MatchData
}

func strGmatchIter(L *LState) int {
	md := L.CheckUserData(1).Value.(*strMatchData)
	str := md.str
	matches := md.matches
	idx := md.pos
	md.pos += 1
	if idx == len(matches) {
		return 0
	}
	L.Push(L.Get(1))
	match := matches[idx]
	if match.CaptureLength() == 2 {
		L.Push(internString(str[match.Capture(0):match.Capture(1)]))
		return 1
	}

	for i := 2; i < match.CaptureLength(); i += 2 {
		if match.IsPosCapture(i) {
			L.Push(LNumber(match.Capture(i)))
		} else {
			L.Push(internString(str[match.Capture(i):match.Capture(i+1)]))
		}
	}
	return match.CaptureLength()/2 - 1
}

func strGmatch(L *LState) int {
	str := L.CheckString(1)
	pattern := L.CheckString(2)
	mds, err := pm.Find(pattern, []byte(str), 0, -1)
	if err != nil {
		L.RaiseError(err.Error())
	}
	L.Push(L.Get(UpvalueIndex(1)))
	ud := L.NewUserData()
	ud.Value = &strMatchData{str, 0, mds}
	L.Push(ud)
	return 2
}

func strLen(L *LState) int {
	str := L.CheckString(1)
	L.Push(LNumber(len(str)))
	return 1
}

func strLower(L *LState) int {
	str := L.CheckString(1)
	bts := []byte(str)
	for i := 0; i < len(bts); i++ {
		if bts[i] >= 'A' && bts[i] <= 'Z' {
			bts[i] += 32
		}
	}
	L.Push(internString(string(bts)))
	return 1
}

func strMatch(L *LState) int {
	str := L.CheckString(1)
	pattern := L.CheckString(2)
	offset := L.OptInt(3, 1)
	l := len(str)
	if offset < 0 {
		offset = l + offset + 1
	}
	offset--
	if offset < 0 {
		offset = 0
	}

	mds, err := pm.Find(pattern, []byte(str), offset, 1)
	if err != nil {
		L.RaiseError(err.Error())
	}
	if len(mds) == 0 {
		L.Push(LNil)
		return 0
	}
	md := mds[0]
	nsubs := md.CaptureLength() / 2
	switch nsubs {
	case 1:
		L.Push(internString(str[md.Capture(0):md.Capture(1)]))
		return 1
	default:
		for i := 2; i < md.CaptureLength(); i += 2 {
			if md.IsPosCapture(i) {
				L.Push(LNumber(md.Capture(i)))
			} else {
				L.Push(internString(str[md.Capture(i):md.Capture(i+1)]))
			}
		}
		return nsubs - 1
	}
}

func strRep(L *LState) int {
	str := L.CheckString(1)
	n := L.CheckInt(2)
	if n <= 0 {
		L.Push(emptyLString)
	} else if n == 1 {
		L.Push(internString(str))
	} else {
		builder := stringBuilderPool.Get().(*strings.Builder)
		defer stringBuilderPool.Put(builder)
		builder.Reset()
		builder.Grow(len(str) * n)
		for i := 0; i < n; i++ {
			builder.WriteString(str)
		}
		L.Push(internString(builder.String()))
	}
	return 1
}

func strReverse(L *LState) int {
	str := L.CheckString(1)
	bts := []byte(str)
	l := len(bts)
	out := make([]byte, l)
	for i := 0; i < l; i++ {
		out[i] = bts[l-i-1]
	}
	L.Push(internString(string(out)))
	return 1
}

func strSub(L *LState) int {
	str := L.CheckString(1)
	start := luaIndex2StringIndex(str, L.CheckInt(2), true)
	end := luaIndex2StringIndex(str, L.OptInt(3, -1), false)
	l := len(str)
	if start >= l || end < start {
		L.Push(emptyLString)
	} else {
		L.Push(internString(str[start:end]))
	}
	return 1
}

func strUpper(L *LState) int {
	str := L.CheckString(1)
	bts := []byte(str)
	for i := 0; i < len(bts); i++ {
		if bts[i] >= 'a' && bts[i] <= 'z' {
			bts[i] -= 32
		}
	}
	L.Push(internString(string(bts)))
	return 1
}

func luaIndex2StringIndex(str string, i int, start bool) int {
	if start && i != 0 {
		i -= 1
	}
	l := len(str)
	if i < 0 {
		i = l + i + 1
	}
	i = intMax(0, i)
	if !start && i > l {
		i = l
	}
	return i
}

//
