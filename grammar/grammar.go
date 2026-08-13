// Package grammar walks the published GQL grammar and produces statements
// from it.
//
// The corpus is hand-written and every case in it cites a clause, which is what
// makes it reviewable, and that does not change. But 814 productions cannot be
// covered by hand, and a walk of the published BNF reaches constructs no person
// would think to write. What comes out is a lead and never a conformance
// result: nothing generated cites a clause, and a claim that cites nothing is
// the one thing this project refuses to make.
//
// Two limits are worth knowing before reading anything this package produces.
// The first is that the grammar describes syntax and nothing else, so a
// statement it admits may still be meaningless: it can reference a variable
// nothing bound, or compare a duration to a string. An engine is right to
// refuse those, which is why a rejection is only ever interesting when the
// engine says the rejection was a *syntax* error, and why the runner will not
// judge a generated statement at all against an engine that reports no
// GQLSTATUS. The second is leaves.go: ISO defines 23 of its productions in
// prose rather than in BNF, all of them lexical, and this package supplies
// tokens for those by hand. Everything reachable only through a prose rule this
// package has no token for is unreachable, and Coverage says how much of the
// grammar that is.
package grammar

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Kind is the shape of one node of a production's right-hand side.
type Kind string

const (
	// Seq is an ordered sequence, every element of which is written.
	Seq Kind = "seq"
	// Alt is a choice of exactly one element.
	Alt Kind = "alt"
	// Opt is ISO's [ ... ]: the child sequence, or nothing.
	Opt Kind = "opt"
	// Repeat is ISO's trailing "...": the child, one or more times. The
	// grammar writes it as a sibling that follows what it repeats, and the
	// parser turns it back into a node that contains it, because a repetition
	// with no operand cannot be walked.
	Repeat Kind = "repeat"
	// Ref names another production.
	Ref Kind = "ref"
	// Word is a keyword, written as ISO spells it.
	Word Kind = "word"
	// Symbol is a terminal symbol: punctuation, or a character named by code
	// point.
	Symbol Kind = "symbol"
	// Prose is "!! See the Syntax Rules", the marker ISO uses for the 23
	// productions it defines in words instead of in BNF. It cannot be
	// expanded, only substituted for, which is what leaves.go is.
	Prose Kind = "prose"
)

// Node is one element of a production's right-hand side.
type Node struct {
	Kind Kind
	// Text is the keyword for Word and the terminal for Symbol.
	Text string
	// Name is the production named by a Ref.
	Name string
	// Children are the elements of a Seq, the choices of an Alt, the
	// contents of an Opt, and the single repeated element of a Repeat.
	Children []*Node
}

// Rule is one BNFdef: a production name and the right-hand side that defines
// it.
type Rule struct {
	Name string
	Body *Node
}

// Grammar is every production of ISO/IEC 39075's published BNF, as a tree.
//
// The iso package parses the same artifact and keeps only what each production
// mentions, because that is all a citation check needs. This one keeps the
// shape, because a generator that has lost the difference between a sequence
// and a choice cannot produce a statement.
type Grammar struct {
	Rules  []*Rule
	byName map[string]*Rule
}

// Parse reads the grammar artifact.
func Parse(r io.Reader) (*Grammar, error) {
	dec := xml.NewDecoder(r)
	// The artifact carries its own DTD and no external entities. Refusing to
	// resolve anything is both correct and the safe default.
	dec.Strict = false
	dec.Entity = xml.HTMLEntity

	g := &Grammar{byName: map[string]*Rule{}}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the grammar: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "BNFdef" {
			continue
		}
		name := attr(start, "name")
		if name == "" {
			return nil, errors.New("a BNFdef has no name")
		}
		body, err := parseBody(dec)
		if err != nil {
			return nil, fmt.Errorf("production <%s>: %w", name, err)
		}
		rule := &Rule{Name: name, Body: body}
		if _, dup := g.byName[name]; dup {
			return nil, fmt.Errorf("production <%s> is defined twice", name)
		}
		g.byName[name] = rule
		g.Rules = append(g.Rules, rule)
	}
	if len(g.Rules) == 0 {
		return nil, errors.New("the grammar artifact defines no productions")
	}
	return g, nil
}

// Rule returns the production with a name, angle brackets optional.
func (g *Grammar) Rule(name string) (*Rule, bool) {
	r, ok := g.byName[strings.Trim(name, "<>")]
	return r, ok
}

// Len reports how many productions the grammar defines.
func (g *Grammar) Len() int { return len(g.Rules) }

// parseBody reads one <rhs> and returns the node it defines.
//
// A right-hand side is either a list of <alt> children, which is a choice, or a
// flat sequence of elements, which is a sequence. Both forms come back as one
// node so that nothing downstream has to know which was written.
func parseBody(dec *xml.Decoder) (*Node, error) {
	depth := 1
	var rhs *Node
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "rhs" {
				depth++
				continue
			}
			n, err := parseSeq(dec, "rhs")
			if err != nil {
				return nil, err
			}
			rhs = n
		case xml.EndElement:
			depth--
		}
	}
	if rhs == nil {
		return nil, errors.New("no right-hand side")
	}
	return collapse(rhs), nil
}

// parseSeq reads the children of one element up to its close, returning them
// as a sequence, or as a choice when the children are <alt>s.
func parseSeq(dec *xml.Decoder, closing string) (*Node, error) {
	seq := &Node{Kind: Seq}
	var alts []*Node
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "alt":
				n, err := parseSeq(dec, "alt")
				if err != nil {
					return nil, err
				}
				alts = append(alts, collapse(n))
			case "opt", "group":
				n, err := parseSeq(dec, t.Name.Local)
				if err != nil {
					return nil, err
				}
				n = collapse(n)
				if t.Name.Local == "opt" {
					n = &Node{Kind: Opt, Children: []*Node{n}}
				}
				seq.Children = append(seq.Children, n)
			case "BNF":
				seq.Children = append(seq.Children, &Node{Kind: Ref, Name: attr(t, "name")})
				if err := skip(dec, t); err != nil {
					return nil, err
				}
			case "kw":
				s, err := content(dec, t)
				if err != nil {
					return nil, err
				}
				seq.Children = append(seq.Children, &Node{Kind: Word, Text: strings.ToUpper(s)})
			case "terminalsymbol":
				s, err := content(dec, t)
				if err != nil {
					return nil, err
				}
				if u := attr(t, "unicode"); s == "" && u != "" {
					s, err = codePoints(u)
					if err != nil {
						return nil, err
					}
				}
				seq.Children = append(seq.Children, &Node{Kind: Symbol, Text: s})
			case "repeat":
				// The repeat marker follows what it repeats. Wrapping the
				// preceding sibling is what turns ISO's notation into a tree.
				if n := len(seq.Children); n > 0 {
					last := seq.Children[n-1]
					seq.Children[n-1] = &Node{Kind: Repeat, Children: []*Node{last}}
				}
				if err := skip(dec, t); err != nil {
					return nil, err
				}
			case "seeTheRules":
				seq.Children = append(seq.Children, &Node{Kind: Prose})
				if err := skip(dec, t); err != nil {
					return nil, err
				}
			case "allAltsFrom", "bold":
				// allAltsFrom points at another part of the standard, which
				// this artifact does not carry; bold is typography.
				if err := skip(dec, t); err != nil {
					return nil, err
				}
			default:
				if err := skip(dec, t); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if t.Name.Local != closing {
				continue
			}
			if len(alts) > 0 {
				// A right-hand side written as <alt>s may still carry a
				// trailing seeTheRules of its own, which is not a choice.
				if len(seq.Children) > 0 {
					alts = append(alts, collapse(seq))
				}
				return &Node{Kind: Alt, Children: alts}, nil
			}
			return seq, nil
		}
	}
}

// collapse removes the sequence wrapper from a sequence of one, which keeps
// the trees small enough to read in a failure message.
func collapse(n *Node) *Node {
	if (n.Kind == Seq || n.Kind == Alt) && len(n.Children) == 1 {
		return n.Children[0]
	}
	return n
}

// codePoints renders a terminalsymbol given by code point rather than by
// content, e.g. unicode="007E,005B".
func codePoints(s string) (string, error) {
	var b strings.Builder
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseInt(part, 16, 32)
		if err != nil {
			return "", fmt.Errorf("terminal code point %q: %w", part, err)
		}
		b.WriteRune(rune(n))
	}
	return b.String(), nil
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func content(dec *xml.Decoder, start xml.StartElement) (string, error) {
	var s string
	if err := dec.DecodeElement(&s, &start); err != nil {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func skip(dec *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local == "BNF" || start.Name.Local == "repeat" ||
		start.Name.Local == "seeTheRules" || start.Name.Local == "allAltsFrom" {
		// Empty elements: the decoder still owes an end token unless the
		// artifact wrote them self-closing, and Skip consumes either.
		return dec.Skip()
	}
	return dec.Skip()
}

// Refs returns every production a node's subtree names, deduplicated and in
// grammar order.
func (n *Node) Refs() []string {
	seen := map[string]bool{}
	var out []string
	var walk func(*Node)
	walk = func(x *Node) {
		if x == nil {
			return
		}
		if x.Kind == Ref && !seen[x.Name] {
			seen[x.Name] = true
			out = append(out, x.Name)
		}
		for _, c := range x.Children {
			walk(c)
		}
	}
	walk(n)
	return out
}

// Prose reports whether a subtree contains a production ISO defines in words.
func (n *Node) Prose() bool {
	if n == nil {
		return false
	}
	if n.Kind == Prose {
		return true
	}
	for _, c := range n.Children {
		if c.Prose() {
			return true
		}
	}
	return false
}

// ProseRules returns the names of the productions ISO defines in prose rather
// than in BNF, sorted. Each one needs a token in leaves.go or everything that
// reaches it is unreachable.
func (g *Grammar) ProseRules() []string {
	var out []string
	for _, r := range g.Rules {
		if r.Body.Prose() {
			out = append(out, r.Name)
		}
	}
	sort.Strings(out)
	return out
}
