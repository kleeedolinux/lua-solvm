// Lua pattern match functions for Go
package pm

import (
	"fmt"
	"math"
	"sync"
	"time"
)

const EOS = -1
const _UNKNOWN = -2

var (
	matchDataPool = sync.Pool{
		New: func() interface{} {
			return &MatchData{
				captures: make([]uint32, 0, 32),
			}
		},
	}
	scannerPool = sync.Pool{
		New: func() interface{} {
			return &scanner{
				State: scannerState{
					Pos:     0,
					started: false,
				},
			}
		},
	}
)

/* Error {{{ */

type Error struct {
	Pos     int
	Message string
}

func newError(pos int, message string, args ...interface{}) *Error {
	if len(args) == 0 {
		return &Error{pos, message}
	}
	return &Error{pos, fmt.Sprintf(message, args...)}
}

func (e *Error) Error() string {
	switch e.Pos {
	case EOS:
		return fmt.Sprintf("%s at EOS", e.Message)
	case _UNKNOWN:
		return fmt.Sprintf("%s", e.Message)
	default:
		return fmt.Sprintf("%s at %d", e.Message, e.Pos)
	}
}

/* }}} */

/* MatchData {{{ */

type MatchData struct {
	// captured positions
	// layout
	// xxxx xxxx xxxx xxx0 : caputured positions
	// xxxx xxxx xxxx xxx1 : position captured positions
	captures []uint32
}

func newMatchState() *MatchData {
	m := matchDataPool.Get().(*MatchData)
	m.captures = m.captures[:0]
	return m
}

func (st *MatchData) addPosCapture(s, pos int) {
	if s+1 >= len(st.captures) {
		newCap := len(st.captures) * 2
		if newCap < s+2 {
			newCap = s + 2
		}
		newCaptures := make([]uint32, newCap)
		copy(newCaptures, st.captures)
		st.captures = newCaptures
	}
	st.captures[s] = (uint32(pos) << 1) | 1
	st.captures[s+1] = (uint32(pos) << 1) | 1
}

func (st *MatchData) setCapture(s, pos int) uint32 {
	if s >= len(st.captures) {
		newCap := len(st.captures) * 2
		if newCap < s+1 {
			newCap = s + 1
		}
		newCaptures := make([]uint32, newCap)
		copy(newCaptures, st.captures)
		st.captures = newCaptures
	}
	v := st.captures[s]
	st.captures[s] = (uint32(pos) << 1)
	return v
}

func (st *MatchData) restoreCapture(s int, pos uint32) { st.captures[s] = pos }

func (st *MatchData) CaptureLength() int { return len(st.captures) }

func (st *MatchData) IsPosCapture(idx int) bool { return (st.captures[idx] & 1) == 1 }

func (st *MatchData) Capture(idx int) int { return int(st.captures[idx] >> 1) }

/* }}} */

/* scanner {{{ */

type scannerState struct {
	Pos     int
	started bool
}

type scanner struct {
	src   []byte
	State scannerState
	saved scannerState
}

func newScanner(src []byte) *scanner {
	sc := scannerPool.Get().(*scanner)
	sc.src = src
	sc.State.Pos = 0
	sc.State.started = false
	return sc
}

func (sc *scanner) Length() int { return len(sc.src) }

func (sc *scanner) Next() int {
	if !sc.State.started {
		sc.State.started = true
		if len(sc.src) == 0 {
			sc.State.Pos = EOS
		}
	} else {
		sc.State.Pos = sc.NextPos()
	}
	if sc.State.Pos == EOS {
		return EOS
	}
	return int(sc.src[sc.State.Pos])
}

func (sc *scanner) CurrentPos() int {
	return sc.State.Pos
}

func (sc *scanner) NextPos() int {
	if sc.State.Pos == EOS || sc.State.Pos >= len(sc.src)-1 {
		return EOS
	}
	if !sc.State.started {
		return 0
	}
	return sc.State.Pos + 1
}

func (sc *scanner) Peek() int {
	cureof := sc.State.Pos == EOS
	ch := sc.Next()
	if !cureof {
		if sc.State.Pos == EOS {
			sc.State.Pos = len(sc.src) - 1
		} else {
			sc.State.Pos--
			if sc.State.Pos < 0 {
				sc.State.Pos = 0
				sc.State.started = false
			}
		}
	}
	return ch
}

func (sc *scanner) Save() { sc.saved = sc.State }

func (sc *scanner) Restore() { sc.State = sc.saved }

/* }}} */

/* bytecode {{{ */

type opCode int

const (
	opChar opCode = iota
	opMatch
	opTailMatch
	opJmp
	opSplit
	opSave
	opPSave
	opBrace
	opNumber
)

type inst struct {
	OpCode   opCode
	Class    class
	Operand1 int
	Operand2 int
}

/* }}} */

/* classes {{{ */

type class interface {
	Matches(ch int) bool
}

type dotClass struct{}

func (pn *dotClass) Matches(ch int) bool { return true }

type charClass struct {
	Ch int
}

func (pn *charClass) Matches(ch int) bool { return pn.Ch == ch }

type singleClass struct {
	Class int
}

func (pn *singleClass) Matches(ch int) bool {
	ret := false
	switch pn.Class {
	case 'a', 'A':
		ret = 'A' <= ch && ch <= 'Z' || 'a' <= ch && ch <= 'z'
	case 'c', 'C':
		ret = (0x00 <= ch && ch <= 0x1F) || ch == 0x7F
	case 'd', 'D':
		ret = '0' <= ch && ch <= '9'
	case 'l', 'L':
		ret = 'a' <= ch && ch <= 'z'
	case 'p', 'P':
		ret = (0x21 <= ch && ch <= 0x2f) || (0x3a <= ch && ch <= 0x40) || (0x5b <= ch && ch <= 0x60) || (0x7b <= ch && ch <= 0x7e)
	case 's', 'S':
		switch ch {
		case ' ', '\f', '\n', '\r', '\t', '\v':
			ret = true
		}
	case 'u', 'U':
		ret = 'A' <= ch && ch <= 'Z'
	case 'w', 'W':
		ret = '0' <= ch && ch <= '9' || 'A' <= ch && ch <= 'Z' || 'a' <= ch && ch <= 'z'
	case 'x', 'X':
		ret = '0' <= ch && ch <= '9' || 'a' <= ch && ch <= 'f' || 'A' <= ch && ch <= 'F'
	case 'z', 'Z':
		ret = ch == 0
	default:
		return ch == pn.Class
	}
	if 'A' <= pn.Class && pn.Class <= 'Z' {
		return !ret
	}
	return ret
}

type setClass struct {
	IsNot   bool
	Classes []class
}

func (pn *setClass) Matches(ch int) bool {
	for _, class := range pn.Classes {
		if class.Matches(ch) {
			return !pn.IsNot
		}
	}
	return pn.IsNot
}

type rangeClass struct {
	Begin class
	End   class
}

func (pn *rangeClass) Matches(ch int) bool {
	switch begin := pn.Begin.(type) {
	case *charClass:
		end, ok := pn.End.(*charClass)
		if !ok {
			return false
		}
		return begin.Ch <= ch && ch <= end.Ch
	}
	return false
}

type BitmapClass struct {
	bitmap *CharBitmap
	isNot  bool
}

func NewBitmapClass() *BitmapClass {
	return &BitmapClass{
		bitmap: NewCharBitmap(),
	}
}

func (bc *BitmapClass) Matches(ch int) bool {
	if ch < 0 || ch > 255 {
		return false
	}
	result := bc.bitmap.Test(byte(ch))
	if bc.isNot {
		return !result
	}
	return result
}

func (bc *BitmapClass) AddChar(ch byte) {
	bc.bitmap.Set(ch)
}

func (bc *BitmapClass) AddRange(start, end byte) {
	for ch := start; ch <= end; ch++ {
		bc.bitmap.Set(ch)
	}
}

func (bc *BitmapClass) SetNot(not bool) {
	bc.isNot = not
}

/* }}} */

// patterns {{{

type pattern interface{}

type singlePattern struct {
	Class class
}

type seqPattern struct {
	MustHead bool
	MustTail bool
	Patterns []pattern
}

type repeatPattern struct {
	Type  int
	Class class
}

type posCapPattern struct{}

type capPattern struct {
	Pattern pattern
}

type numberPattern struct {
	N int
}

type bracePattern struct {
	Begin int
	End   int
}

// }}}

/* parse {{{ */

func parseClass(sc *scanner, allowset bool) class {
	ch := sc.Next()
	switch ch {
	case '%':
		return &singleClass{sc.Next()}
	case '.':
		if allowset {
			return &dotClass{}
		}
		return &charClass{ch}
	case '[':
		if allowset {
			return parseClassSet(sc)
		}
		return &charClass{ch}
	case EOS:
		panic(newError(sc.CurrentPos(), "unexpected EOS"))
	default:
		return &charClass{ch}
	}
}

func parseClassSet(sc *scanner) class {
	bc := NewBitmapClass()
	if sc.Peek() == '^' {
		bc.SetNot(true)
		sc.Next()
	}

	isrange := false
	var start byte
	for {
		ch := sc.Peek()
		switch ch {
		case EOS:
			panic(newError(sc.CurrentPos(), "unexpected EOS"))
		case ']':
			if isrange {
				bc.AddChar('-')
			}
			sc.Next()
			return bc
		case '-':
			if isrange {
				bc.AddChar('-')
				isrange = false
			} else {
				sc.Next()
				isrange = true
				start = byte(sc.Next())
				continue
			}
		default:
			if isrange {
				end := byte(ch)
				bc.AddRange(start, end)
				isrange = false
			} else {
				bc.AddChar(byte(sc.Next()))
			}
		}
	}
}

func parsePattern(sc *scanner, toplevel bool) *seqPattern {
	pat := &seqPattern{}
	if toplevel {
		if sc.Peek() == '^' {
			sc.Next()
			pat.MustHead = true
		}
	}
	for {
		ch := sc.Peek()
		switch ch {
		case '%':
			sc.Save()
			sc.Next()
			switch sc.Peek() {
			case '0':
				panic(newError(sc.CurrentPos(), "invalid capture index"))
			case '1', '2', '3', '4', '5', '6', '7', '8', '9':
				pat.Patterns = append(pat.Patterns, &numberPattern{sc.Next() - 48})
			case 'b':
				sc.Next()
				pat.Patterns = append(pat.Patterns, &bracePattern{sc.Next(), sc.Next()})
			default:
				sc.Restore()
				pat.Patterns = append(pat.Patterns, &singlePattern{parseClass(sc, true)})
			}
		case '.', '[', ']':
			pat.Patterns = append(pat.Patterns, &singlePattern{parseClass(sc, true)})
		case ')':
			if toplevel {
				panic(newError(sc.CurrentPos(), "invalid ')'"))
			}
			return pat
		case '(':
			sc.Next()
			if sc.Peek() == ')' {
				sc.Next()
				pat.Patterns = append(pat.Patterns, &posCapPattern{})
			} else {
				ret := &capPattern{parsePattern(sc, false)}
				if sc.Peek() != ')' {
					panic(newError(sc.CurrentPos(), "unfinished capture"))
				}
				sc.Next()
				pat.Patterns = append(pat.Patterns, ret)
			}
		case '*', '+', '-', '?':
			sc.Next()
			if len(pat.Patterns) > 0 {
				spat, ok := pat.Patterns[len(pat.Patterns)-1].(*singlePattern)
				if ok {
					pat.Patterns = pat.Patterns[0 : len(pat.Patterns)-1]
					pat.Patterns = append(pat.Patterns, &repeatPattern{ch, spat.Class})
					continue
				}
			}
			pat.Patterns = append(pat.Patterns, &singlePattern{&charClass{ch}})
		case '$':
			if toplevel && (sc.NextPos() == sc.Length()-1 || sc.NextPos() == EOS) {
				pat.MustTail = true
			} else {
				pat.Patterns = append(pat.Patterns, &singlePattern{&charClass{ch}})
			}
			sc.Next()
		case EOS:
			sc.Next()
			goto exit
		default:
			sc.Next()
			pat.Patterns = append(pat.Patterns, &singlePattern{&charClass{ch}})
		}
	}
exit:
	return pat
}

type iptr struct {
	insts   []inst
	capture int
}

func compilePattern(p pattern, ps ...*iptr) []inst {
	var ptr *iptr
	toplevel := false
	if len(ps) == 0 {
		toplevel = true
		ptr = &iptr{[]inst{inst{opSave, nil, 0, -1}}, 2}
	} else {
		ptr = ps[0]
	}
	switch pat := p.(type) {
	case *singlePattern:
		ptr.insts = append(ptr.insts, inst{opChar, pat.Class, -1, -1})
	case *seqPattern:
		for _, cp := range pat.Patterns {
			compilePattern(cp, ptr)
		}
	case *repeatPattern:
		idx := len(ptr.insts)
		switch pat.Type {
		case '*':
			ptr.insts = append(ptr.insts,
				inst{opSplit, nil, idx + 1, idx + 3},
				inst{opChar, pat.Class, -1, -1},
				inst{opJmp, nil, idx, -1})
		case '+':
			ptr.insts = append(ptr.insts,
				inst{opChar, pat.Class, -1, -1},
				inst{opSplit, nil, idx, idx + 2})
		case '-':
			ptr.insts = append(ptr.insts,
				inst{opSplit, nil, idx + 3, idx + 1},
				inst{opChar, pat.Class, -1, -1},
				inst{opJmp, nil, idx, -1})
		case '?':
			ptr.insts = append(ptr.insts,
				inst{opSplit, nil, idx + 1, idx + 2},
				inst{opChar, pat.Class, -1, -1})
		}
	case *posCapPattern:
		ptr.insts = append(ptr.insts, inst{opPSave, nil, ptr.capture, -1})
		ptr.capture += 2
	case *capPattern:
		c0, c1 := ptr.capture, ptr.capture+1
		ptr.capture += 2
		ptr.insts = append(ptr.insts, inst{opSave, nil, c0, -1})
		compilePattern(pat.Pattern, ptr)
		ptr.insts = append(ptr.insts, inst{opSave, nil, c1, -1})
	case *bracePattern:
		ptr.insts = append(ptr.insts, inst{opBrace, nil, pat.Begin, pat.End})
	case *numberPattern:
		ptr.insts = append(ptr.insts, inst{opNumber, nil, pat.N, -1})
	}
	if toplevel {
		if p.(*seqPattern).MustTail {
			ptr.insts = append(ptr.insts, inst{opSave, nil, 1, -1}, inst{opTailMatch, nil, -1, -1})
		}
		ptr.insts = append(ptr.insts, inst{opSave, nil, 1, -1}, inst{opMatch, nil, -1, -1})
	}
	return ptr.insts
}

/* }}} parse */

/* VM {{{ */

// Simple recursive virtual machine based on the
// "Regular Expression Matching: the Virtual Machine Approach" (https://swtch.com/~rsc/regexp/regexp2.html)
func recursiveVM(src []byte, insts []inst, pc, sp int, ms ...*MatchData) (bool, int, *MatchData) {
	var m *MatchData
	if len(ms) == 0 {
		m = newMatchState()
	} else {
		m = ms[0]
	}
redo:
	inst := insts[pc]
	switch inst.OpCode {
	case opChar:
		if sp >= len(src) || !inst.Class.Matches(int(src[sp])) {
			return false, sp, m
		}
		pc++
		sp++
		goto redo
	case opMatch:
		return true, sp, m
	case opTailMatch:
		return sp >= len(src), sp, m
	case opJmp:
		pc = inst.Operand1
		goto redo
	case opSplit:
		if ok, nsp, _ := recursiveVM(src, insts, inst.Operand1, sp, m); ok {
			return true, nsp, m
		}
		pc = inst.Operand2
		goto redo
	case opSave:
		s := m.setCapture(inst.Operand1, sp)
		if ok, nsp, _ := recursiveVM(src, insts, pc+1, sp, m); ok {
			return true, nsp, m
		}
		m.restoreCapture(inst.Operand1, s)
		return false, sp, m
	case opPSave:
		m.addPosCapture(inst.Operand1, sp+1)
		pc++
		goto redo
	case opBrace:
		if sp >= len(src) || int(src[sp]) != inst.Operand1 {
			return false, sp, m
		}
		count := 1
		for sp = sp + 1; sp < len(src); sp++ {
			if int(src[sp]) == inst.Operand2 {
				count--
			}
			if count == 0 {
				pc++
				sp++
				goto redo
			}
			if int(src[sp]) == inst.Operand1 {
				count++
			}
		}
		return false, sp, m
	case opNumber:
		idx := inst.Operand1 * 2
		if idx >= m.CaptureLength()-1 {
			panic(newError(_UNKNOWN, "invalid capture index"))
		}
		capture := src[m.Capture(idx):m.Capture(idx+1)]
		for i := 0; i < len(capture); i++ {
			if i+sp >= len(src) || capture[i] != src[i+sp] {
				return false, sp, m
			}
		}
		pc++
		sp += len(capture)
		goto redo
	}
	panic("should not reach here")
}

/* }}} */

/* API {{{ */

type DFATransition struct {
	nextState int
	chars     []byte
}

type DFAState struct {
	transitions []DFATransition
	isAccept    bool
}

type DFA struct {
	states []DFAState
	start  int
}

type PatternResult struct {
	matches []*MatchData
	err     error
}

type PatternCacheEntry struct {
	result    PatternResult
	timestamp int64
}

type PatternCache struct {
	patterns map[string]*PatternCacheEntry
	mu       sync.RWMutex
	maxSize  int
}

var globalPatternCache = &PatternCache{
	patterns: make(map[string]*PatternCacheEntry),
	maxSize:  1000,
}

func (pc *PatternCache) Get(pattern string) (PatternResult, bool) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	if entry, ok := pc.patterns[pattern]; ok {
		return entry.result, true
	}
	return PatternResult{}, false
}

func (pc *PatternCache) Put(pattern string, result PatternResult) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if len(pc.patterns) >= pc.maxSize {
		// Remove oldest entry
		var oldestKey string
		var oldestTime int64 = math.MaxInt64
		for k, v := range pc.patterns {
			if v.timestamp < oldestTime {
				oldestTime = v.timestamp
				oldestKey = k
			}
		}
		delete(pc.patterns, oldestKey)
	}

	pc.patterns[pattern] = &PatternCacheEntry{
		result:    result,
		timestamp: time.Now().UnixNano(),
	}
}

type CharBitmap struct {
	bits [4]uint64
}

func NewCharBitmap() *CharBitmap {
	return &CharBitmap{}
}

func (b *CharBitmap) Set(ch byte) {
	idx := ch / 64
	bit := ch % 64
	b.bits[idx] |= 1 << bit
}

func (b *CharBitmap) Test(ch byte) bool {
	idx := ch / 64
	bit := ch % 64
	return (b.bits[idx] & (1 << bit)) != 0
}

type TrieNode struct {
	children map[byte]*TrieNode
	isEnd    bool
	pattern  string
}

type Trie struct {
	root *TrieNode
}

func NewTrie() *Trie {
	return &Trie{
		root: &TrieNode{
			children: make(map[byte]*TrieNode),
		},
	}
}

func (t *Trie) Insert(pattern string) {
	node := t.root
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		if _, ok := node.children[ch]; !ok {
			node.children[ch] = &TrieNode{
				children: make(map[byte]*TrieNode),
			}
		}
		node = node.children[ch]
	}
	node.isEnd = true
	node.pattern = pattern
}

func (t *Trie) Search(text []byte) []string {
	var matches []string
	for i := 0; i < len(text); i++ {
		node := t.root
		for j := i; j < len(text); j++ {
			ch := text[j]
			if next, ok := node.children[ch]; ok {
				node = next
				if node.isEnd {
					matches = append(matches, node.pattern)
				}
			} else {
				break
			}
		}
	}
	return matches
}

func buildDFA(pattern string) *DFA {
	dfa := &DFA{
		states: make([]DFAState, 0),
		start:  0,
	}

	// Add initial state
	dfa.states = append(dfa.states, DFAState{
		transitions: make([]DFATransition, 0),
		isAccept:    false,
	})

	currentState := 0
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		nextState := len(dfa.states)

		// Add new state
		dfa.states = append(dfa.states, DFAState{
			transitions: make([]DFATransition, 0),
			isAccept:    i == len(pattern)-1,
		})

		// Add transition
		dfa.states[currentState].transitions = append(dfa.states[currentState].transitions, DFATransition{
			nextState: nextState,
			chars:     []byte{ch},
		})

		currentState = nextState
	}

	return dfa
}

func (dfa *DFA) match(text []byte) bool {
	state := dfa.start
	for i := 0; i < len(text); i++ {
		ch := text[i]
		found := false
		for _, t := range dfa.states[state].transitions {
			for _, c := range t.chars {
				if c == ch {
					state = t.nextState
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return false
		}
	}
	return dfa.states[state].isAccept
}

type JumpTable struct {
	badChar    [256]int
	goodSuffix []int
	pattern    []byte
}

func NewJumpTable(pattern string) *JumpTable {
	jt := &JumpTable{
		pattern:    []byte(pattern),
		goodSuffix: make([]int, len(pattern)),
	}

	// Initialize bad character table
	for i := range jt.badChar {
		jt.badChar[i] = len(pattern)
	}
	for i := 0; i < len(pattern)-1; i++ {
		jt.badChar[pattern[i]] = len(pattern) - 1 - i
	}

	// Initialize good suffix table
	lastPrefix := len(pattern)
	for i := len(pattern) - 1; i >= 0; i-- {
		if isPrefix(pattern, i+1) {
			lastPrefix = i + 1
		}
		jt.goodSuffix[i] = lastPrefix + len(pattern) - 1 - i
	}

	// Second case
	for i := 0; i < len(pattern)-1; i++ {
		slen := suffixLength(pattern, i)
		jt.goodSuffix[len(pattern)-1-slen] = len(pattern) - 1 - i + slen
	}

	return jt
}

func isPrefix(pattern string, p int) bool {
	for i, j := p, 0; i < len(pattern); i, j = i+1, j+1 {
		if pattern[i] != pattern[j] {
			return false
		}
	}
	return true
}

func suffixLength(pattern string, p int) int {
	var i, j int
	for i, j = 0, p; j >= 0 && pattern[i] == pattern[j]; i, j = i+1, j-1 {
	}
	return i
}

func (jt *JumpTable) Search(text []byte) []int {
	var matches []int
	n := len(text)
	m := len(jt.pattern)

	for i := m - 1; i < n; {
		j := m - 1
		for j >= 0 && text[i] == jt.pattern[j] {
			i--
			j--
		}
		if j < 0 {
			matches = append(matches, i+1)
			i += m + 1
		} else {
			i += max(jt.badChar[text[i]], jt.goodSuffix[j])
		}
	}
	return matches
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type DFACache struct {
	patterns map[string]*DFA
	mu       sync.RWMutex
}

var globalDFACache = &DFACache{
	patterns: make(map[string]*DFA),
}

func (dc *DFACache) Get(pattern string) (*DFA, bool) {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	dfa, ok := dc.patterns[pattern]
	return dfa, ok
}

func (dc *DFACache) Put(pattern string, dfa *DFA) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.patterns[pattern] = dfa
}

func Find(p string, src []byte, offset, limit int) (matches []*MatchData, err error) {
	defer func() {
		if v := recover(); v != nil {
			if perr, ok := v.(*Error); ok {
				err = perr
			} else {
				panic(v)
			}
		}
	}()

	// Check pattern cache first
	if result, ok := globalPatternCache.Get(p); ok {
		if len(result.matches) > 0 {
			// Filter matches based on offset and limit
			filtered := make([]*MatchData, 0, len(result.matches))
			for _, m := range result.matches {
				if m.Capture(0) >= offset {
					filtered = append(filtered, m)
					if len(filtered) >= limit {
						break
					}
				}
			}
			return filtered, result.err
		}
	}

	// Try Boyer-Moore for simple string patterns
	if isSimplePattern(p) {
		jt := NewJumpTable(p)
		positions := jt.Search(src[offset:])
		if len(positions) > 0 {
			matches = make([]*MatchData, 0, len(positions))
			for _, pos := range positions {
				if len(matches) >= limit {
					break
				}
				m := newMatchState()
				m.addPosCapture(0, offset+pos)
				m.addPosCapture(1, offset+pos+len(p))
				matches = append(matches, m)
			}
			globalPatternCache.Put(p, PatternResult{matches: matches})
			return
		}
	}

	// Try DFA matching
	dfa, ok := globalDFACache.Get(p)
	if !ok {
		dfa = buildDFA(p)
		globalDFACache.Put(p, dfa)
	}

	if dfa.match(src[offset:]) {
		sc := newScanner([]byte(p))
		defer func() {
			sc.src = nil
			scannerPool.Put(sc)
		}()
		pat := parsePattern(sc, true)
		insts := compilePattern(pat)
		matches = []*MatchData{}
		for sp := offset; sp <= len(src); {
			ok, nsp, ms := recursiveVM(src, insts, 0, sp)
			sp++
			if ok {
				if sp < nsp {
					sp = nsp
				}
				matches = append(matches, ms)
			}
			if len(matches) == limit || pat.MustHead {
				break
			}
		}
		globalPatternCache.Put(p, PatternResult{matches: matches})
		return
	}

	// Fall back to original implementation
	sc := newScanner([]byte(p))
	defer func() {
		sc.src = nil
		scannerPool.Put(sc)
	}()
	pat := parsePattern(sc, true)
	insts := compilePattern(pat)
	matches = []*MatchData{}
	for sp := offset; sp <= len(src); {
		ok, nsp, ms := recursiveVM(src, insts, 0, sp)
		sp++
		if ok {
			if sp < nsp {
				sp = nsp
			}
			matches = append(matches, ms)
		}
		if len(matches) == limit || pat.MustHead {
			break
		}
	}
	globalPatternCache.Put(p, PatternResult{matches: matches})
	return
}

func isSimplePattern(p string) bool {
	for _, ch := range p {
		if ch == '*' || ch == '+' || ch == '?' || ch == '[' || ch == ']' || ch == '(' || ch == ')' || ch == '^' || ch == '$' || ch == '.' || ch == '%' {
			return false
		}
	}
	return true
}

/* }}} */
