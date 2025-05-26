package lua

const defaultArrayCap = 32
const defaultHashCap = 32

type lValueArraySorter struct {
	L      *LState
	Fn     *LFunction
	Values []LValue
}

func (lv lValueArraySorter) Len() int {
	return len(lv.Values)
}

func (lv lValueArraySorter) Swap(i, j int) {
	lv.Values[i], lv.Values[j] = lv.Values[j], lv.Values[i]
}

func (lv lValueArraySorter) Less(i, j int) bool {
	if lv.Fn != nil {
		lv.L.Push(lv.Fn)
		lv.L.Push(lv.Values[i])
		lv.L.Push(lv.Values[j])
		lv.L.Call(2, 1)
		return LVAsBool(lv.L.reg.Pop())
	}
	return lessThan(lv.L, lv.Values[i], lv.Values[j])
}

func newLTable(acap int, hcap int) *LTable {
	if acap < 0 {
		acap = 0
	}
	if hcap < 0 {
		hcap = 0
	}
	tb := &LTable{
		Metatable: LNil,
	}
	if acap > 0 {
		tb.array = make([]LValue, 0, acap)
	}
	if hcap > 0 {
		tb.strdict = make(map[string]LValue, hcap)
	}
	return tb
}

// Len returns length of this LTable without using __len.
func (tb *LTable) Len() int {
	if tb.array == nil {
		return 0
	}
	for i := len(tb.array) - 1; i >= 0; i-- {
		if tb.array[i] != LNil {
			return i + 1
		}
	}
	return 0
}

// Append appends a given LValue to this LTable.
func (tb *LTable) Append(value LValue) {
	if value == LNil {
		return
	}
	if tb.array == nil {
		tb.array = make([]LValue, 0, defaultArrayCap)
	}
	if len(tb.array) == 0 || tb.array[len(tb.array)-1] != LNil {
		tb.array = append(tb.array, value)
		return
	}

	i := len(tb.array) - 2
	for ; i >= 0; i-- {
		if tb.array[i] != LNil {
			break
		}
	}
	tb.array[i+1] = value
}

// Insert inserts a given LValue at position `i` in this table.
func (tb *LTable) Insert(i int, value LValue) {
	if tb.array == nil {
		tb.array = make([]LValue, 0, defaultArrayCap)
	}
	if i > len(tb.array) {
		tb.RawSetInt(i, value)
		return
	}
	if i <= 0 {
		tb.RawSet(LNumber(i), value)
		return
	}

	i--
	if i >= len(tb.array) {
		tb.array = append(tb.array, make([]LValue, i-len(tb.array)+1)...)
	}
	tb.array[i] = value
}

// MaxN returns a maximum number key that nil value does not exist before it.
func (tb *LTable) MaxN() int {
	if tb.array == nil {
		return 0
	}
	for i := len(tb.array) - 1; i >= 0; i-- {
		if tb.array[i] != LNil {
			return i + 1
		}
	}
	return 0
}

// Remove removes from this table the element at a given position.
func (tb *LTable) Remove(pos int) LValue {
	if tb.array == nil || len(tb.array) == 0 {
		return LNil
	}

	i := pos - 1
	larray := len(tb.array)

	if i >= larray || i < 0 {
		return LNil
	}

	oldval := tb.array[i]
	if i == larray-1 {
		tb.array = tb.array[:i]
	} else {
		copy(tb.array[i:], tb.array[i+1:])
		tb.array = tb.array[:larray-1]
	}
	return oldval
}

// RawSet sets a given LValue to a given index without the __newindex metamethod.
// It is recommended to use `RawSetString` or `RawSetInt` for performance
// if you already know the given LValue is a string or number.
func (tb *LTable) RawSet(key LValue, value LValue) {
	switch v := key.(type) {
	case LNumber:
		if isArrayKey(v) {
			if tb.array == nil {
				tb.array = make([]LValue, 0, defaultArrayCap)
			}
			index := int(v) - 1
			alen := len(tb.array)

			if index >= alen {
				if index > alen {
					tb.array = append(tb.array, make([]LValue, index-alen)...)
				}
				tb.array = append(tb.array, value)
			} else {
				tb.array[index] = value
			}
			return
		}
	case LString:
		tb.RawSetString(string(v), value)
		return
	}
	tb.RawSetH(key, value)
}

// RawSetInt sets a given LValue at a position `key` without the __newindex metamethod.
func (tb *LTable) RawSetInt(key int, value LValue) {
	if key < 1 || key >= MaxArrayIndex {
		tb.RawSetH(LNumber(key), value)
		return
	}

	if tb.array == nil {
		tb.array = make([]LValue, 0, defaultArrayCap)
	}

	index := key - 1
	alen := len(tb.array)

	if index >= alen {
		if index > alen {
			tb.array = append(tb.array, make([]LValue, index-alen)...)
		}
		tb.array = append(tb.array, value)
	} else {
		tb.array[index] = value
	}
}

// RawSetString sets a given LValue to a given string index without the __newindex metamethod.
func (tb *LTable) RawSetString(key string, value LValue) {
	if tb.strdict == nil {
		tb.strdict = make(map[string]LValue, defaultHashCap)
	}

	if value == LNil {
		delete(tb.strdict, key)
		return
	}

	tb.strdict[key] = value
}

// RawSetH sets a given LValue to a given index without the __newindex metamethod.
func (tb *LTable) RawSetH(key LValue, value LValue) {
	if s, ok := key.(LString); ok {
		tb.RawSetString(string(s), value)
		return
	}

	if tb.dict == nil {
		tb.dict = make(map[LValue]LValue, defaultHashCap)
	}

	if value == LNil {
		delete(tb.dict, key)
	} else {
		tb.dict[key] = value
	}
}

// RawGet returns an LValue associated with a given key without __index metamethod.
func (tb *LTable) RawGet(key LValue) LValue {
	switch v := key.(type) {
	case LNumber:
		if isArrayKey(v) {
			if tb.array == nil {
				return LNil
			}
			index := int(v) - 1
			if index >= len(tb.array) {
				return LNil
			}
			return tb.array[index]
		}
	case LString:
		if tb.strdict == nil {
			return LNil
		}
		return tb.strdict[string(v)]
	}

	if tb.dict == nil {
		return LNil
	}
	return tb.dict[key]
}

// RawGetInt returns an LValue at position `key` without __index metamethod.
func (tb *LTable) RawGetInt(key int) LValue {
	if tb.array == nil {
		return LNil
	}
	index := key - 1
	if index >= len(tb.array) || index < 0 {
		return LNil
	}
	return tb.array[index]
}

// RawGet returns an LValue associated with a given key without __index metamethod.
func (tb *LTable) RawGetH(key LValue) LValue {
	if s, ok := key.(LString); ok {
		if tb.strdict == nil {
			return LNil
		}
		return tb.strdict[string(s)]
	}

	if tb.dict == nil {
		return LNil
	}
	return tb.dict[key]
}

// RawGetString returns an LValue associated with a given key without __index metamethod.
func (tb *LTable) RawGetString(key string) LValue {
	if tb.strdict == nil {
		return LNil
	}
	return tb.strdict[key]
}

// ForEach iterates over this table of elements, yielding each in turn to a given function.
func (tb *LTable) ForEach(cb func(LValue, LValue)) {
	if tb.array != nil {
		for i, v := range tb.array {
			if v != LNil {
				cb(LNumber(i+1), v)
			}
		}
	}

	if tb.strdict != nil {
		for k, v := range tb.strdict {
			if v != LNil {
				cb(LString(k), v)
			}
		}
	}

	if tb.dict != nil {
		for k, v := range tb.dict {
			if v != LNil {
				cb(k, v)
			}
		}
	}
}

// This function is equivalent to lua_next ( http://www.lua.org/manual/5.1/manual.html#lua_next ).
func (tb *LTable) Next(key LValue) (LValue, LValue) {
	if key == LNil {
		if tb.array != nil {
			for i, v := range tb.array {
				if v != LNil {
					return LNumber(i + 1), v
				}
			}
		}

		if tb.strdict != nil {
			for k, v := range tb.strdict {
				if v != LNil {
					return LString(k), v
				}
			}
		}

		if tb.dict != nil {
			for k, v := range tb.dict {
				if v != LNil {
					return k, v
				}
			}
		}
		return LNil, LNil
	}

	if kv, ok := key.(LNumber); ok && isInteger(kv) && int(kv) >= 0 && kv < LNumber(MaxArrayIndex) {
		index := int(kv)
		if tb.array != nil {
			for i := index; i < len(tb.array); i++ {
				if v := tb.array[i]; v != LNil {
					return LNumber(i + 1), v
				}
			}
		}

		if tb.strdict != nil {
			for k, v := range tb.strdict {
				if v != LNil {
					return LString(k), v
				}
			}
		}

		if tb.dict != nil {
			for k, v := range tb.dict {
				if v != LNil {
					return k, v
				}
			}
		}
	}

	return LNil, LNil
}
