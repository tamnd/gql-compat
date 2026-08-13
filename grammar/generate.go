package grammar

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// unreachable is the cost of a subtree that cannot be written at all: it names
// a production the artifact does not define, or it descends into prose no token
// in leaves.go covers. It is large enough to lose every comparison and small
// enough that adding two of them together does not wrap.
const unreachable = math.MaxInt32 / 4

// Statement is one walk of the grammar.
//
// It is not a case. A case cites a clause and carries an expectation, and
// nothing here does either: the walk knows the statement is well formed and
// knows nothing whatever about what it means or what an engine ought to do with
// it. Path is what makes a rejection worth reading, because it says which of
// the 814 productions were on the way down.
type Statement struct {
	// Text is the statement, tokens joined.
	Text string
	// Path is the productions expanded, in the order the walk first entered
	// them. It is the answer to "which part of the grammar is in dispute".
	Path []string
	// Seed and Index locate this statement exactly: the same seed and the same
	// index give the same text on every platform and every run.
	Seed  uint64
	Index int

	// tree is the derivation, kept so Reduce can shrink the statement by
	// undoing choices rather than by deleting characters. A shrinker that cut
	// text would produce something the grammar does not admit, and then the
	// engine's complaint about it would be correct and worthless.
	tree *deriv
	gen  *Generator
}

// ID is a stable name for a statement, used as the case identifier and as the
// key of the promotion list.
func (s Statement) ID() string { return fmt.Sprintf("gen-%016x-%04d", s.Seed, s.Index) }

// Tokens returns the statement's tokens, before joining.
func (s Statement) Tokens() []string {
	if s.tree == nil {
		return nil
	}
	toks, _ := render(s.tree)
	return toks
}

// Generator walks a grammar and writes statements from it.
//
// The walk is a random descent with a budget rather than an enumeration.
// Enumeration is hopeless here: the grammar is deeply recursive and the first
// few thousand statements of any breadth-first order are all variations of the
// same three productions. A budgeted descent reaches further down and, being
// seeded, is still reproducible.
type Generator struct {
	g         *Grammar
	start     string
	maxDepth  int
	minTokens int
	rnd       *rand.Rand
	seed      uint64
	next      int

	ruleCost map[string]int
	nodeCost map[*Node]int
}

// Options configure a Generator. The zero value is usable: it starts at
// <GQL-program> with a depth budget of 40 and discards statements of fewer
// than four tokens.
type Options struct {
	// Start is the production to walk from.
	Start string
	// MaxDepth bounds the descent. It is a budget and not a guarantee of size:
	// a shallow tree can still be wide.
	MaxDepth int
	// MinTokens discards statements shorter than this and walks again.
	//
	// Without it most of the output is the shortest program the grammar
	// admits, because a choice near the root that ends the program is as
	// likely as one that begins a query, and there are only a handful of very
	// short programs. Discarding them costs nothing and is not a bias worth
	// hiding: the walk is not sampling the language uniformly under any
	// reading, and a corpus of a hundred SESSION CLOSEs tests nothing.
	MinTokens int
}

// DefaultStart is the root of a GQL program, which is what an engine is asked
// to execute.
const DefaultStart = "GQL-program"

// NewGenerator prepares a walk of g. It fails if the start production is
// missing from the artifact, or if the walk cannot reach any statement at all
// from it, which is what happens when the cut in leaves.go has a hole in it.
func NewGenerator(g *Grammar, seed uint64, opt Options) (*Generator, error) {
	if opt.Start == "" {
		opt.Start = DefaultStart
	}
	if opt.MaxDepth <= 0 {
		opt.MaxDepth = 40
	}
	if opt.MinTokens <= 0 {
		opt.MinTokens = 4
	}
	if _, ok := g.Rule(opt.Start); !ok {
		return nil, fmt.Errorf("the grammar defines no production <%s>", opt.Start)
	}
	gen := &Generator{
		g:         g,
		start:     strings.Trim(opt.Start, "<>"),
		maxDepth:  opt.MaxDepth,
		minTokens: opt.MinTokens,
		rnd:       rand.New(rand.NewChaCha8(seedBytes(seed))),
		seed:      seed,
	}
	gen.computeCost()
	if gen.ruleCost[gen.start] >= unreachable {
		return nil, fmt.Errorf("no statement can be written from <%s>: every path through it reaches a production with no definition and no token", opt.Start)
	}
	return gen, nil
}

// Seed returns the seed the walk was started with.
func (gen *Generator) Seed() uint64 { return gen.seed }

// Start returns the production the walk begins at.
func (gen *Generator) Start() string { return gen.start }

// seedBytes spreads a seed over the 32 bytes ChaCha8 wants, the same way the
// fixture generator does, so that a seed printed in a report means one thing
// across the project.
func seedBytes(seed uint64) [32]byte {
	var b [32]byte
	for i := range 8 {
		b[i] = byte(seed >> (8 * i))
		b[i+8] = byte(seed>>(8*i)) ^ 0x5a
		b[i+16] = byte(seed>>(8*i)) ^ 0xa5
		b[i+24] = byte(seed>>(8*i)) ^ 0xff
	}
	return b
}

// computeCost finds, for every production and every node, the smallest number
// of tokens it can be written in.
//
// The walk needs this for two reasons. It has to know which alternatives are
// writable at all, because a branch that descends into uncovered prose is a
// dead end and choosing it would leave a hole in the middle of a statement. And
// it has to know which alternative is cheapest, because the grammar is
// recursive and a descent that always chose at random would not reliably come
// back up. The fixed point runs until nothing improves; costs only ever fall,
// and they are bounded below, so it terminates.
func (gen *Generator) computeCost() {
	gen.ruleCost = make(map[string]int, len(gen.g.Rules))
	gen.nodeCost = map[*Node]int{}
	for _, r := range gen.g.Rules {
		gen.ruleCost[r.Name] = unreachable
	}
	for {
		changed := false
		for _, r := range gen.g.Rules {
			if c := gen.cost(r.Body); c < gen.ruleCost[r.Name] {
				gen.ruleCost[r.Name] = c
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

// cost is one relaxation step for a node, reading rule costs as they currently
// stand. It memoises nothing, because the values it reads change between
// rounds; computeCost stores the result per node on the way out instead.
func (gen *Generator) cost(n *Node) int {
	if n == nil {
		return 0
	}
	var c int
	switch n.Kind {
	case Word, Symbol:
		c = 1
	case Prose:
		// Only reachable when a production ISO defines in words has no token
		// of its own in leaves.go. Nothing can be written for it.
		c = unreachable
	case Ref:
		name := strings.Trim(n.Name, "<>")
		if leaf, ok := Cut(name); ok {
			c = min(len(leaf.Tokens), 1)
			break
		}
		if _, ok := gen.g.byName[name]; !ok {
			c = unreachable
			break
		}
		c = gen.ruleCost[name]
	case Opt:
		c = 0
	case Alt:
		c = unreachable
		for _, child := range n.Children {
			if x := gen.cost(child); x < c {
				c = x
			}
		}
	case Repeat:
		c = gen.cost(n.Children[0])
	default: // Seq
		for _, child := range n.Children {
			c += gen.cost(child)
			if c >= unreachable {
				c = unreachable
				break
			}
		}
	}
	if c > unreachable {
		c = unreachable
	}
	gen.nodeCost[n] = c
	return c
}

// Generate writes the next statement. Statements come out in a fixed order for
// a given seed, and Index counts them, so a report can name one and a reader
// can get it back.
func (gen *Generator) Generate() (Statement, error) {
	// The cap on attempts matters: a start production whose every statement is
	// shorter than MinTokens would otherwise walk forever. Keeping the last
	// short statement rather than failing is the right answer, because a short
	// statement is still a statement.
	const attempts = 64
	var tree *deriv
	for range attempts {
		t, err := gen.walk(gen.start, 0)
		if err != nil {
			return Statement{}, err
		}
		tree = t
		if toks, _ := render(t); len(toks) >= gen.minTokens {
			break
		}
	}
	s := gen.statement(tree)
	s.Index = gen.next
	gen.next++
	return s, nil
}

// GenerateN writes n statements.
func (gen *Generator) GenerateN(n int) ([]Statement, error) {
	out := make([]Statement, 0, n)
	for range n {
		s, err := gen.Generate()
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// statement renders a derivation into the thing a report and an engine see.
func (gen *Generator) statement(tree *deriv) Statement {
	toks, path := render(tree)
	return Statement{Text: join(toks), Path: path, Seed: gen.seed, tree: tree, gen: gen}
}

// deriv is one node of a derivation: which grammar node was expanded and how.
//
// It exists so that a statement can be made smaller without being made
// invalid. Every field records a decision the walk took and the reducer can
// take differently.
type deriv struct {
	node *Node
	// rule is the production entered here, empty when this node is inside a
	// production rather than at the top of one.
	rule string
	// token is the text written, for a Word, a Symbol, or a cut Ref.
	token string
	// taken says whether an Opt was written.
	taken bool
	// kids are the children of a Seq, the reps of a Repeat, the single chosen
	// branch of an Alt, the body of a taken Opt, or the expansion of a Ref.
	kids []*deriv
}

// pathLimit stops a deep walk from turning the production path into a wall of
// text in the report. The path is a lead for a reader, not an audit trail.
const pathLimit = 24

// render flattens a derivation into the tokens it writes and the productions it
// went through, outermost first.
func render(d *deriv) (tokens, path []string) {
	seen := map[string]bool{}
	var walk func(*deriv)
	walk = func(x *deriv) {
		if x == nil {
			return
		}
		if x.rule != "" && !seen[x.rule] {
			seen[x.rule] = true
			if len(path) < pathLimit {
				path = append(path, x.rule)
			}
		}
		if x.token != "" {
			tokens = append(tokens, x.token)
		}
		if x.node != nil && x.node.Kind == Opt && !x.taken {
			return
		}
		for _, k := range x.kids {
			walk(k)
		}
	}
	walk(d)
	return tokens, path
}

// walk expands one production.
func (gen *Generator) walk(name string, depth int) (*deriv, error) {
	name = strings.Trim(name, "<>")
	r, ok := gen.g.Rule(name)
	if !ok {
		return nil, fmt.Errorf("the grammar defines no production <%s>", name)
	}
	body, err := gen.expand(r.Body, depth+1)
	if err != nil {
		return nil, err
	}
	return &deriv{rule: name, kids: []*deriv{body}}, nil
}

func (gen *Generator) expand(n *Node, depth int) (*deriv, error) {
	if n == nil {
		return &deriv{}, nil
	}
	// Past the budget the walk stops choosing and takes the cheapest way out of
	// every construct, which is what makes it terminate.
	tight := depth >= gen.maxDepth
	d := &deriv{node: n}
	switch n.Kind {
	case Word, Symbol:
		d.token = n.Text
		return d, nil
	case Prose:
		return nil, errors.New("production defined in prose with no token in leaves.go")
	case Ref:
		name := strings.Trim(n.Name, "<>")
		if leaf, ok := Cut(name); ok {
			if len(leaf.Tokens) > 0 {
				d.token = leaf.Tokens[gen.rnd.IntN(len(leaf.Tokens))]
			}
			return d, nil
		}
		sub, err := gen.walk(name, depth)
		if err != nil {
			return nil, err
		}
		d.kids = []*deriv{sub}
		return d, nil
	case Seq:
		for _, child := range n.Children {
			k, err := gen.expand(child, depth)
			if err != nil {
				return nil, err
			}
			d.kids = append(d.kids, k)
		}
		return d, nil
	case Opt:
		// An optional part is skipped when the budget is spent, and otherwise
		// taken about a third of the time. Taking them more often produces
		// statements that are long without being any more varied.
		if tight || gen.nodeCost[n.Children[0]] >= unreachable || gen.rnd.IntN(3) != 0 {
			return d, nil
		}
		k, err := gen.expand(n.Children[0], depth+1)
		if err != nil {
			return nil, err
		}
		d.taken, d.kids = true, []*deriv{k}
		return d, nil
	case Repeat:
		reps := 1
		if !tight && gen.rnd.IntN(4) == 0 {
			reps = 2
		}
		for range reps {
			k, err := gen.expand(n.Children[0], depth+1)
			if err != nil {
				return nil, err
			}
			d.kids = append(d.kids, k)
		}
		return d, nil
	case Alt:
		child := gen.choose(n, tight)
		if child == nil {
			return nil, errors.New("no writable alternative")
		}
		k, err := gen.expand(child, depth+1)
		if err != nil {
			return nil, err
		}
		d.kids = []*deriv{k}
		return d, nil
	}
	return nil, fmt.Errorf("unknown node kind %q", n.Kind)
}

// choose picks one alternative.
//
// Past the budget it takes the cheapest, and breaks a tie by grammar order
// rather than at random, so that the tail of a deep statement is the same on
// every platform. That is what makes the walk terminate.
//
// Within the budget it picks at random among the writable alternatives,
// weighted by how much each one writes. Uniform choice sounds fairer and is
// worse: at the root of the grammar the alternative that ends the program is
// one of two, so half the statements would be the shortest program the language
// has, and the same thing happens again at every level down. Weighting by cost
// spends the walk on the parts of the grammar that have something in them.
func (gen *Generator) choose(n *Node, tight bool) *Node {
	var writable []*Node
	var weights []int
	total := 0
	best, bestCost := (*Node)(nil), unreachable
	for _, child := range n.Children {
		c, ok := gen.nodeCost[child]
		if !ok || c >= unreachable {
			continue
		}
		writable = append(writable, child)
		weights = append(weights, c+1)
		total += c + 1
		if c < bestCost {
			best, bestCost = child, c
		}
	}
	if tight || len(writable) == 0 {
		return best
	}
	pick := gen.rnd.IntN(total)
	for i, weight := range weights {
		if pick < weight {
			return writable[i]
		}
		pick -= weight
	}
	return writable[len(writable)-1]
}

// join puts the tokens back together.
//
// The rule is deliberately the weakest one that still lexes: a space goes in
// only where leaving it out would run two tokens into one. Anything more
// generous risks putting a space inside a compound terminal such as an edge
// arrow and then reporting the engine's complaint as a finding, which would be
// this package blaming an engine for its own formatting.
func join(tokens []string) string {
	var b strings.Builder
	for i, t := range tokens {
		if t == "" {
			continue
		}
		if i > 0 && b.Len() > 0 && needSpace(b.String(), t) {
			b.WriteByte(' ')
		}
		b.WriteString(t)
	}
	return b.String()
}

func needSpace(left, right string) bool {
	l, _ := utf8.DecodeLastRuneInString(left)
	r, _ := utf8.DecodeRuneInString(right)
	if separable(l) && separable(r) {
		return true
	}
	// Two solidi in a row open a comment and swallow the rest of the line, and
	// a solidus and an asterisk open one that swallows more than that.
	if l == '/' && (r == '/' || r == '*') {
		return true
	}
	if l == '*' && r == '/' {
		return true
	}
	// A dollar sign is asymmetric. Nothing may come between it and the
	// parameter name that follows, because most lexers read the two as one
	// token, but a keyword immediately before it reads as PARAMETER$a, and
	// whether that ends the keyword depends on which characters the engine
	// lets an identifier continue with. A space there costs nothing and
	// removes a way to produce a lead about this harness's rendering.
	return r == '$' && separable(l)
}

// separable reports whether a character can be part of a word, a number or a
// quoted string, and so cannot sit next to another such character without
// changing what the two tokens are.
func separable(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '\'' || r == '"' || r == '`'
}

// Coverage says how much of the grammar a walk from a start production can
// reach, which is the honest bound on what anything generated here proves.
type Coverage struct {
	// Total is every production the artifact defines.
	Total int `json:"total"`
	// Reachable is how many of those a walk from Start can enter.
	Reachable int `json:"reachable"`
	// Cut is how many are never entered because leaves.go writes a token for
	// them instead. They are not reached and not missing; they are replaced.
	Cut int `json:"cut"`
	// Unwritable is how many are reachable in the grammar but cannot be
	// written, because every path through them ends in prose with no token.
	Unwritable []string `json:"unwritable,omitempty"`
	// Start is the production the walk begins at.
	Start string `json:"start"`
}

// Coverage computes the reachable set from the generator's start production.
func (gen *Generator) Coverage() Coverage {
	cov := Coverage{Total: gen.g.Len(), Start: gen.start}
	seen := map[string]bool{}
	var walk func(string)
	walk = func(name string) {
		name = strings.Trim(name, "<>")
		if seen[name] {
			return
		}
		seen[name] = true
		if _, isCut := Cut(name); isCut {
			cov.Cut++
			return
		}
		r, ok := gen.g.Rule(name)
		if !ok {
			return
		}
		if gen.ruleCost[name] >= unreachable {
			cov.Unwritable = append(cov.Unwritable, name)
		}
		for _, ref := range r.Body.Refs() {
			walk(ref)
		}
	}
	walk(gen.start)
	cov.Reachable = len(seen) - cov.Cut
	sort.Strings(cov.Unwritable)
	return cov
}
