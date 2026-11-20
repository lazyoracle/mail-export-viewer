package msf

import (
	"bytes"
	"errors"
	"strings"
	"unicode/utf8"
)

// Errors returned by Parse.
var (
	ErrNotMork  = errors.New("msf: not a Mork file")
	ErrTooLarge = errors.New("msf: file too large")
)

// Limits on hostile input. Variables so tests can lower them.
var (
	maxInput = 256 << 20
	maxRows  = 4_000_000
	maxAtoms = 4_000_000
)

const (
	maxCells    = 1024    // cells kept per row
	maxAtomLen  = 1 << 20 // longer atom/cell values are truncated
	maxNesting  = 256     // counted nesting of skipped {} / <> groups
	maxMarkLen  = 48      // longest @...@ group marker
	maxScopeLen = 256
)

// Cell is one column/value pair of a row, both resolved to text.
type Cell struct{ Col, Val string }

// Row is one Mork row: its scope (resolved to text), id and cells, with all
// updates to the row merged in file order.
type Row struct {
	Scope string
	ID    uint64
	Cells []Cell
	dead  bool
}

// Get returns the value of column col.
func (r *Row) Get(col string) (string, bool) {
	for _, c := range r.Cells {
		if c.Col == col {
			return c.Val, true
		}
	}
	return "", false
}

// File is a parsed Mork file.
type File struct {
	Rows     []*Row // surviving rows in order of first appearance
	Problems int    // count of tolerated anomalies (garbage, limits hit)
}

type rowKey struct {
	scope string
	id    uint64
}

type parser struct {
	d        []byte
	i        int
	colAtoms map[uint64]string // dictionaries in scope "c" (column names)
	valAtoms map[uint64]string // dictionaries in any other scope (values)
	rows     map[rowKey]*Row
	order    []*Row
	problems int
	inGroup  bool
	undo     []func()
}

// Parse reads a Mork file. It is tolerant: syntax it does not understand is
// skipped (counted in File.Problems), so it fails only when data lacks the
// Mork header or exceeds the size limit. An uncommitted or aborted
// transaction group (@$${id{@ ... @$$}~~}@) is rolled back.
func Parse(data []byte) (*File, error) {
	if len(data) > maxInput {
		return nil, ErrTooLarge
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	if !bytes.Contains(head, []byte("<mdb:mork")) {
		return nil, ErrNotMork
	}
	p := &parser{d: data, colAtoms: map[uint64]string{}, valAtoms: map[uint64]string{}, rows: map[rowKey]*Row{}}
	p.run()
	if p.inGroup {
		p.rollback()
		p.problems++
	}
	f := &File{Problems: p.problems}
	for _, r := range p.order {
		if !r.dead {
			f.Rows = append(f.Rows, r)
		}
	}
	return f, nil
}

func (p *parser) run() {
	for p.i < len(p.d) {
		switch p.d[p.i] {
		case '/':
			p.comment()
		case '<':
			p.i++
			p.dict()
		case '{':
			p.i++
			p.table()
		case '[':
			p.i++
			p.row("")
		case '@':
			p.group()
		default:
			p.i++
		}
	}
}

// comment skips a // or /* */ comment at p.i (or one stray byte).
func (p *parser) comment() {
	if p.i+1 < len(p.d) {
		switch p.d[p.i+1] {
		case '/':
			if j := bytes.IndexByte(p.d[p.i:], '\n'); j >= 0 {
				p.i += j + 1
			} else {
				p.i = len(p.d)
			}
			return
		case '*':
			if j := bytes.Index(p.d[p.i+2:], []byte("*/")); j >= 0 {
				p.i += j + 4
			} else {
				p.i = len(p.d)
			}
			return
		}
	}
	p.i++
}

func (p *parser) skipSpace() {
	for p.i < len(p.d) {
		switch p.d[p.i] {
		case ' ', '\t', '\r', '\n':
			p.i++
		default:
			return
		}
	}
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// hex reads a run of hex digits; ok is false when there is none or it
// overflows 64 bits.
func (p *parser) hex() (v uint64, ok bool) {
	n := 0
	for p.i < len(p.d) {
		h := hexVal(p.d[p.i])
		if h < 0 {
			break
		}
		if n >= 16 {
			ok = false
		} else {
			v = v<<4 | uint64(h)
		}
		n++
		p.i++
	}
	return v, n > 0 && n <= 16
}

// text reads literal text up to (not including) an unescaped byte in stops,
// decoding "$XX" hex bytes, "\c" escapes and "\<newline>" continuations.
// Bytes beyond maxAtomLen are dropped.
func (p *parser) text(stops string) string {
	var b []byte
	for p.i < len(p.d) {
		c := p.d[p.i]
		if strings.IndexByte(stops, c) >= 0 {
			break
		}
		p.i++
		switch c {
		case '\\':
			if p.i >= len(p.d) {
				break
			}
			n := p.d[p.i]
			p.i++
			if n == '\r' && p.i < len(p.d) && p.d[p.i] == '\n' {
				p.i++
				continue
			}
			if n == '\n' || n == '\r' {
				continue
			}
			c = n
		case '$':
			if p.i+1 < len(p.d) {
				if h, l := hexVal(p.d[p.i]), hexVal(p.d[p.i+1]); h >= 0 && l >= 0 {
					c = byte(h<<4 | l)
					p.i += 2
				}
			}
		}
		if len(b) < maxAtomLen {
			b = append(b, c)
		}
	}
	return toUTF8(b)
}

// toUTF8 returns b as UTF-8, reading it as Latin-1 when it is not valid.
func toUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func (p *parser) atom(id uint64, scopeIsCol bool) (string, bool) {
	a, b := p.valAtoms, p.colAtoms
	if scopeIsCol {
		a, b = b, a
	}
	if s, ok := a[id]; ok {
		return s, true
	}
	s, ok := b[id]
	return s, ok
}

// dict parses a dictionary after its '<'. Nested "<...>" are meta dicts
// whose (a=c) sets the atom scope ("c": column names, else values).
func (p *parser) dict() {
	scopeCol := false // a plain dictionary holds values; (a=c) switches to columns
	level := 1
	for p.i < len(p.d) && level > 0 {
		switch p.d[p.i] {
		case '/':
			p.comment()
		case '<':
			if level >= maxNesting {
				p.problems++
			} else {
				level++
			}
			p.i++
		case '>':
			level--
			p.i++
		case '(':
			p.i++
			p.skipSpace()
			start := p.i
			id, ok := p.hex()
			idTok := string(p.d[start:p.i])
			if p.i >= len(p.d) || p.d[p.i] != '=' {
				p.text(")")
				p.i++
				p.problems++
				continue
			}
			p.i++
			val := p.text(")")
			p.i++
			switch {
			case strings.EqualFold(idTok, "a"):
				scopeCol = val == "c"
			case ok:
				p.setAtom(id, val, scopeCol)
			}
		default:
			p.i++
		}
	}
}

func (p *parser) setAtom(id uint64, val string, col bool) {
	m := p.valAtoms
	if col {
		m = p.colAtoms
	}
	old, had := m[id]
	if !had && len(p.colAtoms)+len(p.valAtoms) >= maxAtoms {
		p.problems++
		return
	}
	m[id] = val
	if p.inGroup {
		p.undo = append(p.undo, func() {
			if had {
				m[id] = old
			} else {
				delete(m, id)
			}
		})
	}
}

// oid reads "[-]id[:scope]" where scope is ^hexid or a literal name.
func (p *parser) oid(defScope string) (neg bool, id uint64, scope string, ok bool) {
	p.skipSpace()
	if p.i < len(p.d) && p.d[p.i] == '-' {
		neg = true
		p.i++
	}
	id, ok = p.hex()
	scope = defScope
	if p.i < len(p.d) && p.d[p.i] == ':' {
		p.i++
		if p.i < len(p.d) && p.d[p.i] == '^' {
			p.i++
			start := p.i
			sid, sok := p.hex()
			scope = string(p.d[start:p.i])
			if sok {
				if s, found := p.atom(sid, true); found {
					scope = s
				}
			}
		} else {
			start := p.i
			for p.i < len(p.d) && p.i-start < maxScopeLen && strings.IndexByte(" \t\r\n([{]}<", p.d[p.i]) < 0 {
				p.i++
			}
			scope = string(p.d[start:p.i])
		}
	}
	return neg, id, scope, ok
}

// table parses a table after its '{': rows inside inherit its scope.
func (p *parser) table() {
	_, _, scope, _ := p.oid("")
	for p.i < len(p.d) {
		switch p.d[p.i] {
		case '}':
			p.i++
			return
		case '[':
			p.i++
			p.row(scope)
		case '{':
			p.i++
			p.skipBalanced('{', '}')
		case '<':
			p.i++
			p.dict()
		case '/':
			p.comment()
		default:
			p.i++
		}
	}
}

// skipBalanced skips to the close matching an already consumed open,
// ignoring text inside parentheses. Nesting beyond maxNesting is not
// tracked (a mismatch only makes the skip end late or at EOF).
func (p *parser) skipBalanced(open, close byte) {
	depth := 1
	for p.i < len(p.d) && depth > 0 {
		switch c := p.d[p.i]; c {
		case open:
			if depth < maxNesting {
				depth++
			} else {
				p.problems++
			}
		case close:
			depth--
		case '(':
			p.i++
			p.text(")")
		}
		p.i++
	}
}

// cell parses a cell after its '('. del is set for "-(...)".
func (p *parser) cell() (c Cell, ok bool) {
	p.skipSpace()
	if p.i < len(p.d) && p.d[p.i] == '^' {
		p.i++
		id, hok := p.hex()
		if hok {
			c.Col, ok = p.atom(id, true)
		}
	} else {
		c.Col = p.text("=^)")
		ok = c.Col != ""
	}
	if p.i < len(p.d) {
		switch p.d[p.i] {
		case '=':
			p.i++
			c.Val = p.text(")")
		case '^':
			p.i++
			id, hok := p.hex()
			var found bool
			if hok {
				c.Val, found = p.atom(id, false)
			}
			ok = ok && found
			p.text(")")
		}
	}
	p.i++ // ')'
	if !ok {
		p.problems++
	}
	return c, ok
}

// row parses a row after its '['.
func (p *parser) row(defScope string) {
	neg, id, scope, ok := p.oid(defScope)
	type upd struct {
		c   Cell
		del bool
	}
	var ups []upd
loop:
	for p.i < len(p.d) {
		switch p.d[p.i] {
		case ']':
			p.i++
			break loop
		case '[', '{', '<':
			p.problems++
			break loop
		case '(':
			p.i++
			if c, cok := p.cell(); cok && len(ups) < maxCells*2 {
				ups = append(ups, upd{c: c})
			}
		case '-':
			p.i++
			p.skipSpace()
			if p.i < len(p.d) && p.d[p.i] == '(' {
				p.i++
				if c, cok := p.cell(); cok && len(ups) < maxCells*2 {
					ups = append(ups, upd{c: c, del: true})
				}
			}
		case '/':
			p.comment()
		default:
			p.i++
		}
	}
	if !ok {
		p.problems++
		return
	}
	k := rowKey{scope, id}
	r := p.rows[k]
	if r == nil {
		if len(p.rows) >= maxRows {
			p.problems++
			return
		}
		r = &Row{Scope: scope, ID: id}
		p.rows[k] = r
		p.order = append(p.order, r)
		if p.inGroup {
			p.undo = append(p.undo, func() {
				r.dead = true
				delete(p.rows, k)
			})
		}
	}
	if neg {
		// "[-id ...]" cuts every cell of the row, then applies the cells that follow.
		old := r.Cells
		r.Cells = nil
		if p.inGroup {
			p.undo = append(p.undo, func() { r.Cells = old })
		}
	}
	for _, u := range ups {
		if u.del {
			p.delCell(r, u.c.Col)
		} else {
			p.setCell(r, u.c)
		}
	}
}

func (p *parser) setCell(r *Row, c Cell) {
	for i := range r.Cells {
		if r.Cells[i].Col == c.Col {
			old := r.Cells[i].Val
			r.Cells[i].Val = c.Val
			if p.inGroup {
				p.undo = append(p.undo, func() { r.Cells[i].Val = old })
			}
			return
		}
	}
	if len(r.Cells) >= maxCells {
		p.problems++
		return
	}
	r.Cells = append(r.Cells, c)
	if p.inGroup {
		n := len(r.Cells) - 1
		p.undo = append(p.undo, func() { r.Cells = r.Cells[:n] })
	}
}

func (p *parser) delCell(r *Row, col string) {
	for i := range r.Cells {
		if r.Cells[i].Col != col {
			continue
		}
		old := r.Cells
		r.Cells = append(append([]Cell(nil), old[:i]...), old[i+1:]...)
		if p.inGroup {
			p.undo = append(p.undo, func() { r.Cells = old })
		}
		return
	}
}

// group handles a "@$${id{@" begin, "@$$}id}@" commit or "@$$}~~}@" abort
// marker at p.i.
func (p *parser) group() {
	window := p.d[p.i+1:]
	if len(window) > maxMarkLen+1 {
		window = window[:maxMarkLen+1]
	}
	end := bytes.IndexByte(window, '@')
	if end < 0 {
		p.i++
		return
	}
	m := string(p.d[p.i+1 : p.i+1+end])
	switch {
	case strings.HasPrefix(m, "$${") && strings.HasSuffix(m, "{"):
		if p.inGroup {
			p.problems++
		}
		p.inGroup = true
	case m == "$$}~~}":
		p.rollback()
		p.inGroup = false
	case strings.HasPrefix(m, "$$}") && strings.HasSuffix(m, "}"):
		p.undo = nil
		p.inGroup = false
	default:
		p.i++
		return
	}
	p.i += end + 2
}

func (p *parser) rollback() {
	for i := len(p.undo) - 1; i >= 0; i-- {
		p.undo[i]()
	}
	p.undo = nil
}
