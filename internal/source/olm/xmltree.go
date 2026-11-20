package olm

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
)

// Limits on one XML document (hostile input).
const (
	maxNodes = 200000
	maxDepth = 64
)

type attr struct{ name, orig, val string }

// node is one XML element. name is lower-cased local name, orig the
// original spelling.
type node struct {
	name, orig string
	attrs      []attr
	text       string
	kids       []*node
}

func (n *node) attr(suffixes ...string) string {
	for _, s := range suffixes {
		for _, a := range n.attrs {
			if strings.HasSuffix(a.name, s) && a.val != "" {
				return a.val
			}
		}
	}
	return ""
}

// attrOrChild is attr, falling back to the text of a child element whose
// name ends with one of suffixes.
func (n *node) attrOrChild(suffixes ...string) string {
	if v := n.attr(suffixes...); v != "" {
		return v
	}
	for _, s := range suffixes {
		for _, k := range n.kids {
			if strings.HasSuffix(k.name, s) && strings.TrimSpace(k.text) != "" {
				return strings.TrimSpace(k.text)
			}
		}
	}
	return ""
}

var errTooComplex = errors.New("olm: XML too complex")

// parseXML builds the element tree of r. Illegal control characters are
// replaced by spaces and unknown encodings are converted, so that sloppy
// exports still parse. It fails on malformed or over-limit documents.
func parseXML(r io.Reader) (*node, error) {
	d := xml.NewDecoder(&cleanReader{r: r})
	d.Strict = false
	d.CharsetReader = func(label string, in io.Reader) (io.Reader, error) {
		enc, err := htmlindex.Get(label)
		if err != nil {
			return in, nil
		}
		return enc.NewDecoder().Reader(in), nil
	}
	root := &node{}
	stack := []*node{root}
	count := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		top := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			count++
			if count > maxNodes || len(stack) > maxDepth {
				return nil, errTooComplex
			}
			n := &node{name: strings.ToLower(t.Name.Local), orig: t.Name.Local}
			for _, a := range t.Attr {
				n.attrs = append(n.attrs, attr{strings.ToLower(a.Name.Local), a.Name.Local, strings.ToValidUTF8(a.Value, "�")})
			}
			top.kids = append(top.kids, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 1 {
				top.text += string(t)
			}
		}
	}
	if len(stack) != 1 {
		return nil, io.ErrUnexpectedEOF
	}
	if len(root.kids) == 0 {
		return nil, errors.New("olm: no XML elements")
	}
	var fix func(*node)
	fix = func(n *node) {
		n.text = strings.ToValidUTF8(n.text, "�")
		for _, k := range n.kids {
			fix(k)
		}
	}
	fix(root)
	return root, nil
}

// cleanReader replaces bytes XML forbids (C0 controls other than tab, LF,
// CR) by spaces.
type cleanReader struct{ r io.Reader }

func (c *cleanReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	b := p[:n]
	for i, ch := range b {
		if ch < 0x20 && ch != '\t' && ch != '\n' && ch != '\r' {
			b[i] = ' '
		}
	}
	return n, err
}
