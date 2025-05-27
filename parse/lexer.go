package parse

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/kleeedolinux/lua-solvm/ast"
)

const EOF = -1
const whitespace1 = 1<<'\t' | 1<<' '
const whitespace2 = 1<<'\t' | 1<<'\n' | 1<<'\r' | 1<<' '

// DFA state transitions for token matching
type DFAState struct {
	transitions map[int]int
	isAccepting bool
	tokenType   int
}

// Trie node for keyword matching
type TrieNode struct {
	children  map[rune]*TrieNode
	tokenType int
	isEnd     bool
}

// Memoization cache for pattern matching
type PatternCache struct {
	mu    sync.RWMutex
	cache map[string]int
}

var (
	bufferPool = sync.Pool{
		New: func() interface{} {
			return new(bytes.Buffer)
		},
	}

	// Precomputed DFA states for common patterns
	dfaStates = []DFAState{
		{transitions: map[int]int{'=': 1}, isAccepting: false, tokenType: 0},
		{transitions: map[int]int{'=': 2}, isAccepting: true, tokenType: TEqeq},
		{transitions: map[int]int{}, isAccepting: true, tokenType: '='},
	}

	// Keyword trie for fast keyword lookup
	keywordTrie = &TrieNode{
		children: make(map[rune]*TrieNode),
	}

	// Pattern matching cache
	patternCache = &PatternCache{
		cache: make(map[string]int),
	}
)

// Initialize keyword trie
func init() {
	keywords := map[string]int{
		"and": TAnd, "break": TBreak, "do": TDo, "else": TElse, "elseif": TElseIf,
		"end": TEnd, "false": TFalse, "for": TFor, "function": TFunction,
		"if": TIf, "in": TIn, "local": TLocal, "nil": TNil, "not": TNot, "or": TOr,
		"return": TReturn, "repeat": TRepeat, "then": TThen, "true": TTrue,
		"until": TUntil, "while": TWhile, "goto": TGoto,
	}

	for keyword, tokenType := range keywords {
		current := keywordTrie
		for _, ch := range keyword {
			if _, exists := current.children[ch]; !exists {
				current.children[ch] = &TrieNode{
					children: make(map[rune]*TrieNode),
				}
			}
			current = current.children[ch]
		}
		current.isEnd = true
		current.tokenType = tokenType
	}
}

type Error struct {
	Pos     ast.Position
	Message string
	Token   string
}

func (e *Error) Error() string {
	pos := e.Pos
	if pos.Line == EOF {
		return fmt.Sprintf("%v at EOF:   %s\n", pos.Source, e.Message)
	}
	return fmt.Sprintf("%v line:%d(column:%d) near '%v':   %s\n", pos.Source, pos.Line, pos.Column, e.Token, e.Message)
}

func writeChar(buf *bytes.Buffer, c int) { buf.WriteByte(byte(c)) }

func isDecimal(ch int) bool { return '0' <= ch && ch <= '9' }

func isIdent(ch int, pos int) bool {
	return ch == '_' || 'A' <= ch && ch <= 'Z' || 'a' <= ch && ch <= 'z' || isDecimal(ch) && pos > 0
}

func isDigit(ch int) bool {
	return '0' <= ch && ch <= '9' || 'a' <= ch && ch <= 'f' || 'A' <= ch && ch <= 'F'
}

type Scanner struct {
	Pos    ast.Position
	reader *bufio.Reader
	buf    *bytes.Buffer
}

func NewScanner(reader io.Reader, source string) *Scanner {
	return &Scanner{
		Pos: ast.Position{
			Source: source,
			Line:   1,
			Column: 0,
		},
		reader: bufio.NewReaderSize(reader, 4096),
		buf:    bufferPool.Get().(*bytes.Buffer),
	}
}

func (sc *Scanner) Close() {
	if sc.buf != nil {
		sc.buf.Reset()
		bufferPool.Put(sc.buf)
		sc.buf = nil
	}
}

func (sc *Scanner) Error(tok string, msg string) *Error { return &Error{sc.Pos, msg, tok} }

func (sc *Scanner) TokenError(tok ast.Token, msg string) *Error { return &Error{tok.Pos, msg, tok.Str} }

func (sc *Scanner) readNext() int {
	ch, err := sc.reader.ReadByte()
	if err == io.EOF {
		return EOF
	}
	return int(ch)
}

func (sc *Scanner) Newline(ch int) {
	if ch < 0 {
		return
	}
	sc.Pos.Line++
	sc.Pos.Column = 0
	next := sc.Peek()
	if ch == '\n' && next == '\r' || ch == '\r' && next == '\n' {
		sc.reader.ReadByte()
	}
}

func (sc *Scanner) Next() int {
	ch := sc.readNext()
	switch ch {
	case '\n', '\r':
		sc.Newline(ch)
		ch = int('\n')
	case EOF:
		sc.Pos.Line = EOF
		sc.Pos.Column = 0
	default:
		sc.Pos.Column++
	}
	return ch
}

func (sc *Scanner) Peek() int {
	ch := sc.readNext()
	if ch != EOF {
		sc.reader.UnreadByte()
	}
	return ch
}

func (sc *Scanner) skipWhiteSpace(whitespace int64) int {
	ch := sc.Next()
	for ; whitespace&(1<<uint(ch)) != 0; ch = sc.Next() {
	}
	return ch
}

func (sc *Scanner) skipComments(ch int) error {
	if sc.Peek() == '[' {
		ch = sc.Next()
		if sc.Peek() == '[' || sc.Peek() == '=' {
			sc.buf.Reset()
			if err := sc.scanMultilineString(sc.Next(), sc.buf); err != nil {
				return sc.Error(sc.buf.String(), "invalid multiline comment")
			}
			return nil
		}
	}
	for {
		if ch == '\n' || ch == '\r' || ch < 0 {
			break
		}
		ch = sc.Next()
	}
	return nil
}

func (sc *Scanner) scanIdent(ch int, buf *bytes.Buffer) error {
	writeChar(buf, ch)

	// Use Aho-Corasick automaton for keyword matching
	current := keywordAutomaton.root
	matched := false

	for isIdent(sc.Peek(), 1) {
		nextCh := sc.Next()
		writeChar(buf, nextCh)

		// Follow failure links if no direct match
		for current != keywordAutomaton.root {
			if next, exists := current.children[rune(nextCh)]; exists {
				current = next
				if len(current.output) > 0 {
					matched = true
				}
				break
			}
			current = current.fail
		}

		if next, exists := current.children[rune(nextCh)]; exists {
			current = next
			if len(current.output) > 0 {
				matched = true
			}
		}
	}

	if matched {
		return nil
	}

	// Continue scanning if not a keyword
	for isIdent(sc.Peek(), 1) {
		writeChar(buf, sc.Next())
	}
	return nil
}

func (sc *Scanner) scanDecimal(ch int, buf *bytes.Buffer) error {
	writeChar(buf, ch)
	for isDecimal(sc.Peek()) {
		writeChar(buf, sc.Next())
	}
	return nil
}

func (sc *Scanner) scanNumber(ch int, buf *bytes.Buffer) error {
	// Use DFA for number scanning
	state := 0
	writeChar(buf, ch)

	for {
		nextCh := sc.Peek()
		if nextCh == EOF {
			break
		}

		// State transitions based on character type
		switch state {
		case 0: // Initial state
			if isDecimal(nextCh) {
				state = 1
			} else if nextCh == 'x' || nextCh == 'X' {
				state = 2
			} else {
				return nil
			}
		case 1: // Decimal number
			if isDecimal(nextCh) {
				state = 1
			} else if nextCh == '.' {
				state = 3
			} else if nextCh == 'e' || nextCh == 'E' {
				state = 4
			} else {
				return nil
			}
		case 2: // Hex number
			if isDigit(nextCh) {
				state = 2
			} else {
				return nil
			}
		case 3: // After decimal point
			if isDecimal(nextCh) {
				state = 3
			} else if nextCh == 'e' || nextCh == 'E' {
				state = 4
			} else {
				return nil
			}
		case 4: // After exponent
			if nextCh == '+' || nextCh == '-' {
				state = 5
			} else if isDecimal(nextCh) {
				state = 6
			} else {
				return nil
			}
		case 5, 6: // Exponent digits
			if isDecimal(nextCh) {
				state = 6
			} else {
				return nil
			}
		}

		writeChar(buf, sc.Next())
	}
	return nil
}

// Boyer-Moore inspired jump table for pattern matching
type JumpTable struct {
	badChar    [256]int
	goodSuffix []int
	pattern    string
}

func newJumpTable(pattern string) *JumpTable {
	jt := &JumpTable{
		pattern:    pattern,
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

// Bitmap for character set operations
type CharSet struct {
	bits [4]uint64 // 256 bits for ASCII
}

func newCharSet(chars ...int) *CharSet {
	cs := &CharSet{}
	for _, ch := range chars {
		if ch >= 0 && ch < 256 {
			cs.bits[ch/64] |= 1 << (ch % 64)
		}
	}
	return cs
}

func (cs *CharSet) contains(ch int) bool {
	if ch < 0 || ch >= 256 {
		return false
	}
	return (cs.bits[ch/64] & (1 << (ch % 64))) != 0
}

// Precomputed character sets
var (
	identStartSet = newCharSet('_', 'A', 'Z', 'a', 'z')
	identContSet  = newCharSet('_', 'A', 'Z', 'a', 'z', '0', '9')
	digitSet      = newCharSet('0', '9', 'a', 'f', 'A', 'F')
)

func (sc *Scanner) isIdentStart(ch int) bool {
	return identStartSet.contains(ch)
}

func (sc *Scanner) isIdentCont(ch int) bool {
	return identContSet.contains(ch)
}

func (sc *Scanner) isDigit(ch int) bool {
	return digitSet.contains(ch)
}

// Memoized pattern matching
func (pc *PatternCache) get(pattern string) (int, bool) {
	pc.mu.RLock()
	tokenType, exists := pc.cache[pattern]
	pc.mu.RUnlock()
	return tokenType, exists
}

func (pc *PatternCache) set(pattern string, tokenType int) {
	pc.mu.Lock()
	pc.cache[pattern] = tokenType
	pc.mu.Unlock()
}

func (sc *Scanner) scanString(quote int, buf *bytes.Buffer) error {
	// Use Boyer-Moore inspired matching for string scanning
	jt := newJumpTable(string(quote))
	ch := sc.Next()

	for ch != quote {
		if ch == '\n' || ch == '\r' || ch < 0 {
			return sc.Error(buf.String(), "unterminated string")
		}

		if ch == '\\' {
			if err := sc.scanEscape(ch, buf); err != nil {
				return err
			}
		} else {
			writeChar(buf, ch)
		}

		// Use jump table to skip ahead
		nextCh := sc.Peek()
		if nextCh == quote {
			ch = sc.Next()
			break
		}

		// Apply Boyer-Moore skip
		skip := jt.badChar[nextCh]
		if skip > 0 {
			for i := 0; i < skip-1; i++ {
				sc.Next()
			}
		}
		ch = sc.Next()
	}
	return nil
}

func (sc *Scanner) scanEscape(ch int, buf *bytes.Buffer) error {
	ch = sc.Next()
	switch ch {
	case 'a':
		buf.WriteByte('\a')
	case 'b':
		buf.WriteByte('\b')
	case 'f':
		buf.WriteByte('\f')
	case 'n':
		buf.WriteByte('\n')
	case 'r':
		buf.WriteByte('\r')
	case 't':
		buf.WriteByte('\t')
	case 'v':
		buf.WriteByte('\v')
	case '\\':
		buf.WriteByte('\\')
	case '"':
		buf.WriteByte('"')
	case '\'':
		buf.WriteByte('\'')
	case '\n':
		buf.WriteByte('\n')
	case '\r':
		buf.WriteByte('\n')
		sc.Newline('\r')
	default:
		if '0' <= ch && ch <= '9' {
			bytes := make([]byte, 1, 3)
			bytes[0] = byte(ch)
			for i := 0; i < 2 && isDecimal(sc.Peek()); i++ {
				bytes = append(bytes, byte(sc.Next()))
			}
			val, _ := strconv.ParseInt(string(bytes), 10, 32)
			writeChar(buf, int(val))
		} else {
			writeChar(buf, ch)
		}
	}
	return nil
}

func (sc *Scanner) countSep(ch int) (int, int) {
	count := 0
	for ; ch == '='; count++ {
		ch = sc.Next()
	}
	return count, ch
}

func (sc *Scanner) scanMultilineString(ch int, buf *bytes.Buffer) error {
	var count1, count2 int
	count1, ch = sc.countSep(ch)
	if ch != '[' {
		return sc.Error(string(rune(ch)), "invalid multiline string")
	}
	ch = sc.Next()
	if ch == '\n' || ch == '\r' {
		ch = sc.Next()
	}
	for {
		if ch < 0 {
			return sc.Error(buf.String(), "unterminated multiline string")
		} else if ch == ']' {
			count2, ch = sc.countSep(sc.Next())
			if count1 == count2 && ch == ']' {
				return nil
			}
			buf.WriteByte(']')
			buf.WriteString(strings.Repeat("=", count2))
			continue
		}
		writeChar(buf, ch)
		ch = sc.Next()
	}
}

var reservedWords = map[string]int{
	"and": TAnd, "break": TBreak, "do": TDo, "else": TElse, "elseif": TElseIf,
	"end": TEnd, "false": TFalse, "for": TFor, "function": TFunction,
	"if": TIf, "in": TIn, "local": TLocal, "nil": TNil, "not": TNot, "or": TOr,
	"return": TReturn, "repeat": TRepeat, "then": TThen, "true": TTrue,
	"until": TUntil, "while": TWhile, "goto": TGoto}

func (sc *Scanner) Scan(lexer *Lexer) (ast.Token, error) {
redo:
	var err error
	tok := ast.Token{}
	newline := false

	ch := sc.skipWhiteSpace(whitespace1)
	if ch == '\n' || ch == '\r' {
		newline = true
		ch = sc.skipWhiteSpace(whitespace2)
	}

	if ch == '(' && lexer.PrevTokenType == ')' {
		lexer.PNewLine = newline
	} else {
		lexer.PNewLine = false
	}

	sc.buf.Reset()
	tok.Pos = sc.Pos

	switch {
	case isIdent(ch, 0):
		tok.Type = TIdent
		err = sc.scanIdent(ch, sc.buf)
		tok.Str = sc.buf.String()
		if err != nil {
			goto finally
		}
		if typ, ok := reservedWords[tok.Str]; ok {
			tok.Type = typ
		}
	case isDecimal(ch):
		tok.Type = TNumber
		err = sc.scanNumber(ch, sc.buf)
		tok.Str = sc.buf.String()
	default:
		switch ch {
		case EOF:
			tok.Type = EOF
		case '-':
			if sc.Peek() == '-' {
				err = sc.skipComments(sc.Next())
				if err != nil {
					goto finally
				}
				goto redo
			} else {
				tok.Type = ch
				tok.Str = string(rune(ch))
			}
		case '"', '\'':
			tok.Type = TString
			err = sc.scanString(ch, sc.buf)
			tok.Str = sc.buf.String()
		case '[':
			if c := sc.Peek(); c == '[' || c == '=' {
				tok.Type = TString
				err = sc.scanMultilineString(sc.Next(), sc.buf)
				tok.Str = sc.buf.String()
			} else {
				tok.Type = ch
				tok.Str = string(rune(ch))
			}
		case '=':
			if sc.Peek() == '=' {
				tok.Type = TEqeq
				tok.Str = "=="
				sc.Next()
			} else {
				tok.Type = ch
				tok.Str = string(rune(ch))
			}
		case '~':
			if sc.Peek() == '=' {
				tok.Type = TNeq
				tok.Str = "~="
				sc.Next()
			} else {
				err = sc.Error("~", "Invalid '~' token")
			}
		case '<':
			if sc.Peek() == '=' {
				tok.Type = TLte
				tok.Str = "<="
				sc.Next()
			} else {
				tok.Type = ch
				tok.Str = string(rune(ch))
			}
		case '>':
			if sc.Peek() == '=' {
				tok.Type = TGte
				tok.Str = ">="
				sc.Next()
			} else {
				tok.Type = ch
				tok.Str = string(rune(ch))
			}
		case '.':
			ch2 := sc.Peek()
			switch {
			case isDecimal(ch2):
				tok.Type = TNumber
				err = sc.scanNumber(ch, sc.buf)
				tok.Str = sc.buf.String()
			case ch2 == '.':
				writeChar(sc.buf, ch)
				writeChar(sc.buf, sc.Next())
				if sc.Peek() == '.' {
					writeChar(sc.buf, sc.Next())
					tok.Type = T3Comma
				} else {
					tok.Type = T2Comma
				}
			default:
				tok.Type = '.'
			}
			tok.Str = sc.buf.String()
		case ':':
			if sc.Peek() == ':' {
				tok.Type = T2Colon
				tok.Str = "::"
				sc.Next()
			} else {
				tok.Type = ch
				tok.Str = string(rune(ch))
			}
		case '+', '*', '/', '%', '^', '#', '(', ')', '{', '}', ']', ';', ',':
			tok.Type = ch
			tok.Str = string(rune(ch))
		default:
			writeChar(sc.buf, ch)
			err = sc.Error(sc.buf.String(), "Invalid token")
			goto finally
		}
	}

finally:
	tok.Name = TokenName(int(tok.Type))
	return tok, err
}

// yacc interface {{{

type Lexer struct {
	scanner       *Scanner
	Stmts         []ast.Stmt
	PNewLine      bool
	Token         ast.Token
	PrevTokenType int
}

func (lx *Lexer) Lex(lval *yySymType) int {
	lx.PrevTokenType = lx.Token.Type
	tok, err := lx.scanner.Scan(lx)
	if err != nil {
		panic(err)
	}
	if tok.Type < 0 {
		return 0
	}
	lval.token = tok
	lx.Token = tok
	return int(tok.Type)
}

func (lx *Lexer) Error(message string) {
	panic(lx.scanner.Error(lx.Token.Str, message))
}

func (lx *Lexer) TokenError(tok ast.Token, message string) {
	panic(lx.scanner.TokenError(tok, message))
}

func Parse(reader io.Reader, name string) (chunk []ast.Stmt, err error) {
	lexer := &Lexer{NewScanner(reader, name), nil, false, ast.Token{Str: ""}, TNil}
	chunk = nil
	defer func() {
		if e := recover(); e != nil {
			err, _ = e.(error)
		}
		lexer.scanner.Close()
	}()
	yyParse(lexer)
	chunk = lexer.Stmts
	return
}

// }}}

// Dump {{{

func isInlineDumpNode(rv reflect.Value) bool {
	switch rv.Kind() {
	case reflect.Struct, reflect.Slice, reflect.Interface, reflect.Ptr:
		return false
	default:
		return true
	}
}

func dump(node interface{}, level int, s string) string {
	rt := reflect.TypeOf(node)
	if fmt.Sprint(rt) == "<nil>" {
		return strings.Repeat(s, level) + "<nil>"
	}

	rv := reflect.ValueOf(node)
	buf := make([]string, 0, 16)
	switch rt.Kind() {
	case reflect.Slice:
		if rv.Len() == 0 {
			return strings.Repeat(s, level) + "<empty>"
		}
		for i := 0; i < rv.Len(); i++ {
			buf = append(buf, dump(rv.Index(i).Interface(), level, s))
		}
	case reflect.Ptr:
		vt := rv.Elem()
		tt := rt.Elem()
		indicies := make([]int, 0, tt.NumField())
		for i := 0; i < tt.NumField(); i++ {
			if strings.Index(tt.Field(i).Name, "Base") > -1 {
				continue
			}
			indicies = append(indicies, i)
		}
		switch {
		case len(indicies) == 0:
			return strings.Repeat(s, level) + "<empty>"
		case len(indicies) == 1 && isInlineDumpNode(vt.Field(indicies[0])):
			for _, i := range indicies {
				buf = append(buf, strings.Repeat(s, level)+"- Node$"+tt.Name()+": "+dump(vt.Field(i).Interface(), 0, s))
			}
		default:
			buf = append(buf, strings.Repeat(s, level)+"- Node$"+tt.Name())
			for _, i := range indicies {
				if isInlineDumpNode(vt.Field(i)) {
					inf := dump(vt.Field(i).Interface(), 0, s)
					buf = append(buf, strings.Repeat(s, level+1)+tt.Field(i).Name+": "+inf)
				} else {
					buf = append(buf, strings.Repeat(s, level+1)+tt.Field(i).Name+": ")
					buf = append(buf, dump(vt.Field(i).Interface(), level+2, s))
				}
			}
		}
	default:
		buf = append(buf, strings.Repeat(s, level)+fmt.Sprint(node))
	}
	return strings.Join(buf, "\n")
}

func Dump(chunk []ast.Stmt) string {
	return dump(chunk, 0, "   ")
}

// }}

// Aho-Corasick automaton for multi-pattern matching
type ACNode struct {
	children map[rune]*ACNode
	fail     *ACNode
	output   []int
}

type ACAutomaton struct {
	root *ACNode
}

func newACAutomaton() *ACAutomaton {
	return &ACAutomaton{
		root: &ACNode{
			children: make(map[rune]*ACNode),
		},
	}
}

func (ac *ACAutomaton) addPattern(pattern string, tokenType int) {
	current := ac.root
	for _, ch := range pattern {
		if _, exists := current.children[ch]; !exists {
			current.children[ch] = &ACNode{
				children: make(map[rune]*ACNode),
			}
		}
		current = current.children[ch]
	}
	current.output = append(current.output, tokenType)
}

func (ac *ACAutomaton) buildFailureLinks() {
	queue := []*ACNode{ac.root}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for ch, child := range current.children {
			queue = append(queue, child)
			fail := current.fail
			for fail != nil {
				if next, exists := fail.children[ch]; exists {
					child.fail = next
					child.output = append(child.output, next.output...)
					break
				}
				fail = fail.fail
			}
			if fail == nil {
				child.fail = ac.root
			}
		}
	}
}

// Initialize Aho-Corasick automaton for keyword matching
var keywordAutomaton = func() *ACAutomaton {
	ac := newACAutomaton()
	keywords := map[string]int{
		"and": TAnd, "break": TBreak, "do": TDo, "else": TElse, "elseif": TElseIf,
		"end": TEnd, "false": TFalse, "for": TFor, "function": TFunction,
		"if": TIf, "in": TIn, "local": TLocal, "nil": TNil, "not": TNot, "or": TOr,
		"return": TReturn, "repeat": TRepeat, "then": TThen, "true": TTrue,
		"until": TUntil, "while": TWhile, "goto": TGoto,
	}
	for keyword, tokenType := range keywords {
		ac.addPattern(keyword, tokenType)
	}
	ac.buildFailureLinks()
	return ac
}()
