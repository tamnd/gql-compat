package grammar

import (
	"sort"
	"strings"
)

// Still is the question the reducer asks about a smaller statement: does the
// engine still do the thing that made the original worth reporting.
//
// For the harness that thing is a syntax error, and the caller is the runner,
// which sends the candidate to the same session the original ran on. Anything
// the caller can decide will do, which is what makes the reducer testable
// without an engine.
type Still func(candidate string) bool

// Reduce shrinks a statement while the engine keeps doing whatever it was
// doing, and returns the smallest one it found.
//
// A rejected statement of forty tokens tells a reader nothing. The same
// rejection on six tokens tells them which construct is in dispute, and the
// path on the reduced statement names the productions that built those six.
// That is the whole reason the derivation is kept.
//
// Every candidate is produced by taking a decision differently rather than by
// deleting text: an optional part is dropped, a repetition is trimmed, a
// subtree is replaced by the smallest thing its own production admits. So every
// candidate is a statement the published grammar admits, and a candidate the
// engine rejects is rejected for the same kind of reason as the original.
//
// The search is greedy and deterministic. It takes the first candidate that
// still holds, restarts from there, and stops when a full pass finds nothing or
// when it runs out of steps. Greedy is enough here: the candidates are ordered
// largest saving first, so the expensive cuts are tried while they are still
// available.
func Reduce(s Statement, still Still) Statement {
	if s.tree == nil || s.gen == nil || still == nil {
		return s
	}
	// Two caps, because each candidate is a statement someone's engine has to
	// run. maxSteps bounds how many times the statement gets smaller, and
	// maxTries bounds the total asked, which is what a long tail of candidates
	// that all fail would otherwise run up. A lead that stops shrinking early
	// is still a much better lead than the forty-token original.
	const (
		maxSteps = 40
		maxTries = 200
	)

	cur, tries := s.tree, 0
	for range maxSteps {
		improved := false
		for _, cand := range s.gen.candidates(cur) {
			if tries >= maxTries {
				break
			}
			toks, _ := render(cand)
			if len(toks) == 0 {
				continue
			}
			tries++
			if !still(join(toks)) {
				continue
			}
			cur, improved = cand, true
			break
		}
		if !improved {
			break
		}
	}

	out := s.gen.statement(cur)
	out.Index, out.Seed = s.Index, s.Seed
	return out
}

// candidates lists the smaller derivations one step away from d, largest saving
// first. Each is a full copy: the reducer holds on to the ones that work and
// throws the rest away, and sharing structure between them would make the ones
// it throws away change the one it keeps.
func (gen *Generator) candidates(d *deriv) []*deriv {
	type site struct {
		path    []int
		replace *deriv
		saving  int
	}
	var sites []site

	var visit func(x *deriv, path []int)
	visit = func(x *deriv, path []int) {
		if x == nil {
			return
		}
		size := derivSize(x)
		switch {
		case x.node != nil && x.node.Kind == Opt && x.taken:
			// Drop the optional part.
			sites = append(sites, site{path: clonePath(path), replace: &deriv{node: x.node}, saving: size})
		case x.node != nil && x.node.Kind == Repeat && len(x.kids) > 1:
			// Trim the repetition to one.
			trimmed := &deriv{node: x.node, kids: []*deriv{cloneDeriv(x.kids[0])}}
			sites = append(sites, site{path: clonePath(path), replace: trimmed, saving: size - derivSize(trimmed)})
		case x.node != nil && (x.node.Kind == Alt || x.node.Kind == Ref):
			// Replace the subtree with the smallest thing this production
			// admits. When the walk already took the smallest, minimal returns
			// the same shape and the candidate is dropped below.
			if m, ok := gen.minimal(x.node, 0); ok {
				if saving := size - derivSize(m); saving > 0 {
					sites = append(sites, site{path: clonePath(path), replace: m, saving: saving})
				}
			}
		}
		if x.node != nil && x.node.Kind == Opt && !x.taken {
			return
		}
		for i, k := range x.kids {
			visit(k, append(clonePath(path), i))
		}
	}
	visit(d, nil)

	// Largest saving first, and ties broken by position, so the order does not
	// depend on the map iteration of anything.
	sort.SliceStable(sites, func(a, b int) bool {
		if sites[a].saving != sites[b].saving {
			return sites[a].saving > sites[b].saving
		}
		return lessPath(sites[a].path, sites[b].path)
	})

	out := make([]*deriv, 0, len(sites))
	for _, s := range sites {
		out = append(out, replaceAt(d, s.path, s.replace))
	}
	return out
}

// minimal writes the smallest derivation a grammar node admits, taking the
// cheapest alternative every time and breaking ties by grammar order. It takes
// no random numbers, so the reduced statement of a given lead is the same on
// every machine.
func (gen *Generator) minimal(n *Node, depth int) (*deriv, bool) {
	// The bound is a guard against a cost table that disagrees with the tree,
	// which would otherwise recurse forever. Depth 80 is twice the deepest
	// walk the generator itself will take.
	if n == nil || depth > 80 {
		return nil, false
	}
	if c, ok := gen.nodeCost[n]; ok && c >= unreachable {
		return nil, false
	}
	d := &deriv{node: n}
	switch n.Kind {
	case Word, Symbol:
		d.token = n.Text
		return d, true
	case Prose:
		return nil, false
	case Opt:
		return d, true
	case Ref:
		name := strings.Trim(n.Name, "<>")
		if leaf, ok := Cut(name); ok {
			if len(leaf.Tokens) > 0 {
				d.token = leaf.Tokens[0]
			}
			return d, true
		}
		r, ok := gen.g.Rule(name)
		if !ok {
			return nil, false
		}
		body, ok := gen.minimal(r.Body, depth+1)
		if !ok {
			return nil, false
		}
		d.kids = []*deriv{{rule: name, kids: []*deriv{body}}}
		return d, true
	case Seq:
		for _, child := range n.Children {
			k, ok := gen.minimal(child, depth+1)
			if !ok {
				return nil, false
			}
			d.kids = append(d.kids, k)
		}
		return d, true
	case Repeat:
		k, ok := gen.minimal(n.Children[0], depth+1)
		if !ok {
			return nil, false
		}
		d.kids = []*deriv{k}
		return d, true
	case Alt:
		best, bestCost := (*Node)(nil), unreachable
		for _, child := range n.Children {
			if c, ok := gen.nodeCost[child]; ok && c < bestCost {
				best, bestCost = child, c
			}
		}
		if best == nil {
			return nil, false
		}
		k, ok := gen.minimal(best, depth+1)
		if !ok {
			return nil, false
		}
		d.kids = []*deriv{k}
		return d, true
	}
	return nil, false
}

func derivSize(d *deriv) int {
	if d == nil {
		return 0
	}
	n := 1
	if d.node != nil && d.node.Kind == Opt && !d.taken {
		return n
	}
	for _, k := range d.kids {
		n += derivSize(k)
	}
	return n
}

func cloneDeriv(d *deriv) *deriv {
	if d == nil {
		return nil
	}
	c := &deriv{node: d.node, rule: d.rule, token: d.token, taken: d.taken}
	for _, k := range d.kids {
		c.kids = append(c.kids, cloneDeriv(k))
	}
	return c
}

// replaceAt copies d with the node at path swapped for repl.
func replaceAt(d *deriv, path []int, repl *deriv) *deriv {
	if len(path) == 0 {
		return cloneDeriv(repl)
	}
	c := &deriv{node: d.node, rule: d.rule, token: d.token, taken: d.taken}
	for i, k := range d.kids {
		if i == path[0] {
			c.kids = append(c.kids, replaceAt(k, path[1:], repl))
			continue
		}
		c.kids = append(c.kids, cloneDeriv(k))
	}
	return c
}

func clonePath(p []int) []int { return append([]int(nil), p...) }

func lessPath(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}
