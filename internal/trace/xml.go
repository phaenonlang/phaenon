package trace

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// node is an element or a piece of text of an XML document, held with the
// bytes it was read from, so that whatever a rewrite leaves alone is written
// back as it was read: the layout, the quoting, the namespaces, comments
// and the declaration included.
type node struct {
	// element
	name     string
	attrs    []xml.Attr // decoded, in document order
	children []*node
	start    []byte // the start tag as read; for an empty element, all of it
	end      []byte // the end tag as read; nil for an empty element (<x/>)
	// text: decoded character data
	text   string
	isText bool
	// raw is the text as read, or a comment, a processing instruction, a
	// directive
	raw []byte
	// dirty marks a node whose start tag (or text) is written from attrs
	// (or text) instead of the bytes read.
	dirty bool
}

// parseXML reads a document into its top-level nodes.
func parseXML(data []byte) ([]*node, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	root := &node{}
	stack := []*node{root}
	for {
		from := d.InputOffset()
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		raw := data[from:d.InputOffset()]
		top := stack[len(stack)-1]
		switch tok := tok.(type) {
		case xml.StartElement:
			n := &node{name: qname(tok.Name), start: raw}
			for _, a := range tok.Attr {
				n.attrs = append(n.attrs, xml.Attr{Name: a.Name, Value: strings.Clone(a.Value)})
			}
			top.children = append(top.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) < 2 || top.name != qname(tok.Name) {
				return nil, errors.New("trace: unbalanced XML")
			}
			if len(raw) > 0 {
				top.end = raw
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			top.children = append(top.children, &node{isText: true, text: string(tok), raw: raw})
		default: // comment, processing instruction, directive
			top.children = append(top.children, &node{raw: raw})
		}
	}
	if len(stack) != 1 {
		return nil, errors.New("trace: unclosed XML element")
	}
	return root.children, nil
}

func qname(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

// writeXML writes nodes back.
func writeXML(b *bytes.Buffer, nodes []*node) {
	for _, n := range nodes {
		switch {
		case n.isText && n.dirty:
			b.WriteString(escapeXML(n.text, false))
		case n.isText || n.name == "":
			b.Write(n.raw)
		default:
			if n.dirty {
				b.WriteString("<" + n.name)
				for _, a := range n.attrs {
					b.WriteString(" " + qname(a.Name) + `="` + escapeXML(a.Value, true) + `"`)
				}
				switch {
				case n.end != nil:
					b.WriteString(">")
				case bytes.HasSuffix(n.start, []byte(" />")):
					b.WriteString(" />")
				default:
					b.WriteString("/>")
				}
			} else {
				b.Write(n.start)
			}
			writeXML(b, n.children)
			b.Write(n.end)
		}
	}
}

// escapeXML escapes text as libxml2 serialises it, which is how PHPUnit and
// PHPStan write their reports, so that a rewritten tag reads like its
// neighbours: in an attribute the quote and the whitespace that would
// otherwise be normalised are escaped too.
func escapeXML(s string, attr bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '\r':
			b.WriteString("&#13;")
		case attr && r == '"':
			b.WriteString("&quot;")
		case attr && r == '\n':
			b.WriteString("&#10;")
		case attr && r == '\t':
			b.WriteString("&#9;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// root is the document's element, nil when there is none.
func rootElement(nodes []*node) *node {
	for _, n := range nodes {
		if n.name != "" {
			return n
		}
	}
	return nil
}

func (n *node) attr(name string) (string, bool) {
	for _, a := range n.attrs {
		if qname(a.Name) == name {
			return a.Value, true
		}
	}
	return "", false
}

// setAttr sets an attribute the element has, or adds it.
func (n *node) setAttr(name, value string) {
	for i, a := range n.attrs {
		if qname(a.Name) == name {
			if a.Value != value {
				n.attrs[i].Value = value
				n.dirty = true
			}
			return
		}
	}
	n.attrs = append(n.attrs, xml.Attr{Name: xml.Name{Local: name}, Value: value})
	n.dirty = true
}

func (n *node) removeAttr(name string) {
	for i, a := range n.attrs {
		if qname(a.Name) == name {
			n.attrs = append(n.attrs[:i], n.attrs[i+1:]...)
			n.dirty = true
			return
		}
	}
}

// elements are the element children named name.
func (n *node) elements(name string) []*node {
	var out []*node
	for _, c := range n.children {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

// walk calls f for n and every element under it, parents first, with the
// element's ancestors (the root first).
func walk(n *node, ancestors []*node, f func(n *node, ancestors []*node)) {
	f(n, ancestors)
	inner := append(ancestors[:len(ancestors):len(ancestors)], n)
	for _, c := range n.children {
		if c.name != "" {
			walk(c, inner, f)
		}
	}
}

// textOf is the decoded text directly inside an element.
func (n *node) textOf() string {
	var b strings.Builder
	for _, c := range n.children {
		if c.isText {
			b.WriteString(c.text)
		}
	}
	return b.String()
}

// setText replaces the text directly inside an element with s.
func (n *node) setText(s string) {
	var kept []*node
	for _, c := range n.children {
		if !c.isText {
			kept = append(kept, c)
		}
	}
	n.children = append([]*node{{isText: true, text: s, dirty: true}}, kept...)
}
