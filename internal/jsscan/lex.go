// Package jsscan is a small JavaScript lexer, just enough to find URLs in
// scripts without being fooled by comments, escaped slashes, regex literals or
// string concatenation. It is not a parser: it never builds a tree and it
// recovers from anything it doesn't understand by moving on one byte.
package jsscan

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Kind is a token class.
type Kind uint8

const (
	Ident Kind = iota + 1
	String
	Template
	Number
	Regex
	Punct
)

func (k Kind) String() string {
	switch k {
	case Ident:
		return "ident"
	case String:
		return "string"
	case Template:
		return "template"
	case Number:
		return "number"
	case Regex:
		return "regex"
	case Punct:
		return "punct"
	}
	return "?"
}

// Token is one lexical token. For String it holds the decoded value. For
// Template it holds the static text with each ${...} replaced by "{}", and
// Dynamic reports whether there were any substitutions.
type Token struct {
	Kind    Kind
	Value   string
	Dynamic bool
	Offset  int
}

// keywords after which a '/' starts a regex rather than a division.
var regexAfter = map[string]bool{
	"return": true, "typeof": true, "case": true, "do": true, "else": true,
	"in": true, "instanceof": true, "new": true, "delete": true, "void": true,
	"throw": true, "yield": true, "await": true, "of": true,
}

type lexer struct {
	src  string
	pos  int
	out  []Token
	subs [][]Token // token streams from inside template substitutions
}

// Lex splits src into token streams. The first stream is the top level; the
// rest are the expressions inside template literal substitutions, each lexed
// on its own so they don't interleave with the surrounding tokens.
func Lex(src string) [][]Token {
	l := &lexer{src: src}
	l.run(false)
	return append([][]Token{l.out}, l.subs...)
}

// run lexes until end of input or, inside a substitution, until the closing
// brace that balances it.
func (l *lexer) run(inSubst bool) {
	depth := 0
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			l.pos++
		case c == '/' && l.peek(1) == '/':
			l.skipLine()
		case c == '/' && l.peek(1) == '*':
			l.skipBlock()
		case c == '"' || c == '\'':
			l.lexString(c)
		case c == '`':
			l.lexTemplate()
		case c == '/':
			if l.regexAllowed() {
				l.lexRegex()
			} else {
				l.emit(Punct, "/", l.pos)
				l.pos++
			}
		case isIdentStart(c):
			l.lexIdent()
		case c >= '0' && c <= '9' || (c == '.' && isDigit(l.peek(1))):
			l.lexNumber()
		case c == '{':
			depth++
			l.emit(Punct, "{", l.pos)
			l.pos++
		case c == '}':
			if inSubst && depth == 0 {
				l.pos++
				return
			}
			depth--
			l.emit(Punct, "}", l.pos)
			l.pos++
		case c >= utf8.RuneSelf:
			// Non-ASCII outside a string: identifiers in some bundles, or
			// junk. Either way it's not something we look for.
			_, n := utf8.DecodeRuneInString(l.src[l.pos:])
			l.pos += n
		default:
			l.emit(Punct, string(c), l.pos)
			l.pos++
		}
	}
}

func (l *lexer) emit(k Kind, v string, off int) {
	l.out = append(l.out, Token{Kind: k, Value: v, Offset: off})
}

func (l *lexer) peek(n int) byte {
	if l.pos+n < len(l.src) {
		return l.src[l.pos+n]
	}
	return 0
}

func (l *lexer) skipLine() {
	for l.pos < len(l.src) && l.src[l.pos] != '\n' {
		l.pos++
	}
}

func (l *lexer) skipBlock() {
	end := strings.Index(l.src[l.pos+2:], "*/")
	if end < 0 {
		l.pos = len(l.src)
		return
	}
	l.pos += end + 4
}

// regexAllowed decides whether a '/' here opens a regex literal, from the
// previous token. This is the classic heuristic; it's wrong for `a++ /b/`
// style code, which doesn't turn up in practice.
func (l *lexer) regexAllowed() bool {
	if len(l.out) == 0 {
		return true
	}
	prev := l.out[len(l.out)-1]
	switch prev.Kind {
	case Ident:
		return regexAfter[prev.Value]
	case Punct:
		return prev.Value != ")" && prev.Value != "]"
	default:
		return false
	}
}

func (l *lexer) lexString(q byte) {
	start := l.pos
	l.pos++
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == q:
			l.pos++
			l.emit(String, b.String(), start)
			return
		case c == '\\':
			l.pos++
			l.escape(&b)
		case c == '\n':
			// Unterminated string. Emit what we have and resync on the newline.
			l.emit(String, b.String(), start)
			return
		default:
			b.WriteByte(c)
			l.pos++
		}
	}
	l.emit(String, b.String(), start)
}

// escape decodes one escape sequence; l.pos is just past the backslash.
func (l *lexer) escape(b *strings.Builder) {
	if l.pos >= len(l.src) {
		return
	}
	c := l.src[l.pos]
	l.pos++
	switch c {
	case 'n':
		b.WriteByte('\n')
	case 't':
		b.WriteByte('\t')
	case 'r':
		b.WriteByte('\r')
	case 'b':
		b.WriteByte('\b')
	case 'f':
		b.WriteByte('\f')
	case 'v':
		b.WriteByte('\v')
	case '0':
		b.WriteByte(0)
	case '\r':
		if l.pos < len(l.src) && l.src[l.pos] == '\n' {
			l.pos++
		}
	case '\n':
		// line continuation
	case 'x':
		if r, ok := l.hex(2); ok {
			b.WriteRune(r)
		} else {
			b.WriteByte('x')
		}
	case 'u':
		if l.pos < len(l.src) && l.src[l.pos] == '{' {
			end := strings.IndexByte(l.src[l.pos:], '}')
			if end > 1 && end <= 7 {
				if n, err := strconv.ParseUint(l.src[l.pos+1:l.pos+end], 16, 32); err == nil {
					b.WriteRune(rune(n))
					l.pos += end + 1
					return
				}
			}
			b.WriteByte('u')
			return
		}
		if r, ok := l.hex(4); ok {
			b.WriteRune(r)
		} else {
			b.WriteByte('u')
		}
	default:
		b.WriteByte(c) // \/ \" \' \\ and anything else: the character itself
	}
}

func (l *lexer) hex(n int) (rune, bool) {
	if l.pos+n > len(l.src) {
		return 0, false
	}
	v, err := strconv.ParseUint(l.src[l.pos:l.pos+n], 16, 32)
	if err != nil {
		return 0, false
	}
	l.pos += n
	return rune(v), true
}

func (l *lexer) lexTemplate() {
	start := l.pos
	l.pos++
	var b strings.Builder
	dynamic := false
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '`':
			l.pos++
			l.out = append(l.out, Token{Kind: Template, Value: b.String(), Dynamic: dynamic, Offset: start})
			return
		case c == '\\':
			l.pos++
			l.escape(&b)
		case c == '$' && l.peek(1) == '{':
			l.pos += 2
			dynamic = true
			b.WriteString("{}")
			// Lex the substitution as its own stream so a fetch() inside it is
			// still seen, without splicing its tokens into this one.
			inner := &lexer{src: l.src, pos: l.pos}
			inner.run(true)
			l.pos = inner.pos
			l.subs = append(l.subs, inner.out)
			l.subs = append(l.subs, inner.subs...)
		default:
			b.WriteByte(c)
			l.pos++
		}
	}
	l.out = append(l.out, Token{Kind: Template, Value: b.String(), Dynamic: dynamic, Offset: start})
}

func (l *lexer) lexRegex() {
	start := l.pos
	l.pos++
	inClass := false
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\\':
			l.pos += 2
			continue
		case c == '[':
			inClass = true
		case c == ']':
			inClass = false
		case c == '/' && !inClass:
			l.pos++
			for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
				l.pos++ // flags
			}
			l.emit(Regex, l.src[start:l.pos], start)
			return
		case c == '\n':
			// Not a regex after all. Treat the slash as division and carry on
			// from just after it.
			l.pos = start + 1
			l.emit(Punct, "/", start)
			return
		}
		l.pos++
	}
	l.pos = start + 1
	l.emit(Punct, "/", start)
}

func (l *lexer) lexIdent() {
	start := l.pos
	for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
		l.pos++
	}
	l.emit(Ident, l.src[start:l.pos], start)
}

func (l *lexer) lexNumber() {
	start := l.pos
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if isIdentPart(c) || c == '.' {
			l.pos++
			continue
		}
		if (c == '+' || c == '-') && (l.src[l.pos-1] == 'e' || l.src[l.pos-1] == 'E') &&
			!strings.HasPrefix(strings.ToLower(l.src[start:l.pos]), "0x") {
			l.pos++
			continue
		}
		break
	}
	l.emit(Number, l.src[start:l.pos], start)
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// Literals returns the contents of every string and template literal in src,
// one per line. Running text extractors over this instead of the raw file
// skips comments, which in bundled libraries are mostly author credits and
// licence links.
func Literals(src []byte) string {
	var b strings.Builder
	for _, toks := range Lex(string(src)) {
		for _, t := range toks {
			if t.Kind == String || t.Kind == Template {
				b.WriteString(t.Value)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}
