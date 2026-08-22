// Package iso decodes the ISO/IEC 39075 digital artifacts into typed
// catalogues the rest of the harness indexes tests against.
//
// Nothing here interprets the standard. It reads what ISO published and
// hands back the codes verbatim, so that when a test claims to cover feature
// GQ08 the name and description attached to GQ08 in every report came from
// the standard's own artifact and not from someone's memory of it.
package iso

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/tamnd/gql-compat/iso/artifacts"
)

// SourceURL is the ISO Standards Maintenance Portal directory the embedded
// artifacts were fetched from. Every report carries it, because a conformance
// claim indexed against codes nobody can look up is not checkable.
const SourceURL = artifacts.SourceURL

// Family groups feature codes by the letters that prefix them. ISO does not
// name the families in the artifact, but it assigns them consistently, and a
// report that says "GV: 12 of 52" is more useful than one that lists 228
// codes flat.
type Family struct {
	Prefix string
	Name   string
}

// families are the fifteen prefixes present in features.xml. The names are
// this project's summaries of what the codes in each prefix cover, derived
// from the descriptions ISO ships; they are labels for reports, not
// normative text.
var families = []Family{
	{"G", "Graph pattern matching"},
	{"GA", "Advanced pattern and projection"},
	{"GB", "Lexical elements"},
	{"GC", "Catalog and object management"},
	{"GD", "Data modification"},
	{"GE", "Expressions and operators"},
	{"GF", "Built-in functions"},
	{"GG", "Graph types and schemas"},
	{"GH", "Host and binding"},
	{"GL", "Literals"},
	{"GP", "Procedures and calls"},
	{"GQ", "Query statement composition"},
	{"GS", "Schema and session management"},
	{"GT", "Transactions"},
	{"GV", "Value types"},
}

// Feature is one optional GQL feature, exactly as features.xml defines it.
type Feature struct {
	Code        string `json:"code"`
	Description string `json:"description"`
	// Family is the letter prefix of Code: "GQ" for GQ08, "G" for G002.
	Family string `json:"family"`
}

// Subclass is one GQLSTATUS subclass under a class, e.g. 22G03 is class 22
// subclass G03.
type Subclass struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Class is one GQLSTATUS class: a category letter, a two-character code, and
// the subclasses ISO defines beneath it.
type Class struct {
	Category   string     `json:"category"`
	Code       string     `json:"code"`
	Name       string     `json:"name"`
	Subclasses []Subclass `json:"subclasses"`
}

// Categories maps the single-letter category in conditions.xml to the word
// the artifact's DTD gives it.
var Categories = map[string]string{
	"S": "successful completion",
	"N": "no data",
	"W": "warning",
	"I": "informational",
	"X": "exception",
}

// Behaviour is one implementation-defined or implementation-dependent item.
type Behaviour struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// Production is one BNF rule from the grammar artifact.
type Production struct {
	// Name is the production name without angle brackets: "match statement".
	Name string `json:"name"`
	// Predicative marks the rules ISO flags as predicative, i.e. defined by
	// prose rules rather than by an expansion.
	Predicative bool `json:"predicative"`
	// References are the production names this rule's right-hand side names,
	// deduplicated and sorted.
	References []string `json:"references"`
	// Keywords are the reserved and non-reserved words the right-hand side
	// spells out, deduplicated and sorted.
	Keywords []string `json:"keywords"`
	// Terminals are the punctuation and symbol terminals the rule names.
	Terminals []string `json:"terminals"`
	// RequiresKeyword is true when every alternative of the right-hand side
	// spells at least one of Keywords at a position the grammar does not make
	// optional. It is the machine-checkable half of "a case that uses this
	// rule says one of these words": <match statement> requires MATCH, while
	// <is or colon> does not require IS because a colon will do and <element
	// variable declaration> does not require TEMP because the grammar puts it
	// in brackets. False here is not "no keywords", it is "the rule can be
	// reached without saying one".
	RequiresKeyword bool `json:"requires_keyword,omitempty"`
	// SeeTheRules marks a rule the grammar declines to expand. ISO writes
	// "!! See the Syntax Rules." where the alternatives would go, which is the
	// grammar saying the syntax is somebody else's to define: the lexer's, in
	// the case of <identifier start>, another standard's, in the case of
	// <SQL-datetime literal>, and the implementation's, in the case of
	// <external object reference>. Nothing generated from the grammar can
	// produce one of these, so a harness that draws its inputs from the BNF
	// needs to know which rules it cannot reach.
	SeeTheRules bool `json:"see_the_rules,omitempty"`
}

// Subclause is one numbered clause or subclause of the standard.
//
// Mandatory features have no feature code — ISO assigns codes only to the 228
// optional ones — so the subclause number is the only stable handle a claim
// about mandatory behaviour has. That is what this type is for.
type Subclause struct {
	// Number is the dotted clause number, e.g. "14.4".
	Number string `json:"number"`
	// Title is the standard's own heading, e.g. "<match statement>".
	Title string `json:"title"`
	// Depth is 1 for a clause, 2 for a subclause, and so on.
	Depth int `json:"depth"`
	// Normative is false for the clauses that specify nothing an
	// implementation can conform to: scope, references, terms, notation. A
	// case that cites one of those is citing the wrong thing.
	Normative bool `json:"normative"`
}

// Catalog is every artifact decoded, which is the whole normative surface
// this harness can measure an implementation against.
type Catalog struct {
	Features                []Feature    `json:"features"`
	Classes                 []Class      `json:"classes"`
	ImplementationDefined   []Behaviour  `json:"implementation_defined"`
	ImplementationDependent []Behaviour  `json:"implementation_dependent"`
	Productions             []Production `json:"productions"`
	Subclauses              []Subclause  `json:"subclauses"`

	byFeature    map[string]Feature
	byProduction map[string]Production
	bySubclause  map[string]Subclause
	byBehaviour  map[string]Behaviour
	statuses     map[string]string
	reserved     map[string]bool
}

// Load decodes the embedded artifacts. It is deterministic and does no I/O,
// so callers can treat a failure here as a build problem rather than a
// runtime one.
func Load() (*Catalog, error) {
	c := &Catalog{}
	var err error
	if c.Features, err = parseFeatures(artifacts.Features); err != nil {
		return nil, fmt.Errorf("features.xml: %w", err)
	}
	if c.Classes, err = parseConditions(artifacts.Conditions); err != nil {
		return nil, fmt.Errorf("conditions.xml: %w", err)
	}
	if c.ImplementationDefined, err = parseBehaviours(artifacts.ImplementationDefined, "impDef"); err != nil {
		return nil, fmt.Errorf("implementation-defined.xml: %w", err)
	}
	if c.ImplementationDependent, err = parseBehaviours(artifacts.ImplementationDependent, "unDef"); err != nil {
		return nil, fmt.Errorf("implementation-dependent.xml: %w", err)
	}
	if c.Productions, err = parseGrammar(artifacts.GrammarXML); err != nil {
		return nil, fmt.Errorf("gql.bnf.xml: %w", err)
	}
	if c.Subclauses, err = parseSubclauses(artifacts.Subclauses); err != nil {
		return nil, fmt.Errorf("subclauses.txt: %w", err)
	}
	c.index()
	return c, nil
}

// MustLoad is Load for package-level initialisation, where a malformed
// embedded artifact means the binary should not have been built.
func MustLoad() *Catalog {
	c, err := Load()
	if err != nil {
		panic("iso: " + err.Error())
	}
	return c
}

func (c *Catalog) index() {
	c.byFeature = make(map[string]Feature, len(c.Features))
	for _, f := range c.Features {
		c.byFeature[f.Code] = f
	}
	c.byProduction = make(map[string]Production, len(c.Productions))
	for _, p := range c.Productions {
		c.byProduction[p.Name] = p
	}
	c.bySubclause = make(map[string]Subclause, len(c.Subclauses))
	for _, s := range c.Subclauses {
		c.bySubclause[s.Number] = s
	}
	c.statuses = map[string]string{}
	for _, cl := range c.Classes {
		for _, sc := range cl.Subclasses {
			c.statuses[cl.Code+sc.Code] = cl.Name + ": " + sc.Name
		}
	}
	c.byBehaviour = make(map[string]Behaviour, len(c.ImplementationDefined)+len(c.ImplementationDependent))
	for _, b := range c.ImplementationDefined {
		c.byBehaviour[b.Code] = b
	}
	for _, b := range c.ImplementationDependent {
		c.byBehaviour[b.Code] = b
	}
	c.indexReserved()
}

// Feature returns the feature with the given code.
func (c *Catalog) Feature(code string) (Feature, bool) {
	f, ok := c.byFeature[code]
	return f, ok
}

// Production returns the grammar rule with the given name, angle brackets
// stripped.
func (c *Catalog) Production(name string) (Production, bool) {
	p, ok := c.byProduction[strings.Trim(name, "<>")]
	return p, ok
}

// Referrers returns the rules whose right-hand sides name this one, sorted.
//
// It is the grammar read backwards, and it answers the only question that can
// tell you a rule is out of reach: a rule with no referrer is a start symbol,
// and a rule whose every referrer is itself unreachable is unreachable too.
func (c *Catalog) Referrers(name string) []string {
	name = strings.Trim(name, "<>")
	var out []string
	for _, p := range c.Productions {
		if slices.Contains(p.References, name) {
			out = append(out, p.Name)
		}
	}
	sort.Strings(out)
	return out
}

// LeftToTheImplementation reports whether the standard hands this rule's
// spelling to the implementer rather than to a reader of the grammar.
//
// Two things have to be true and neither is enough on its own. The grammar has
// to decline to expand the rule, which it writes as "!! See the Syntax Rules.",
// and one of ISO's two lists of implementation-defined and
// implementation-dependent items has to name the rule in its description.
//
// Unexpanded alone is not enough. <newline> and <whitespace> are unexpanded and
// every query ever written is full of both, so a case can cite them perfectly
// honestly. Named alone is not enough either: the lists name <value expression>
// and <non-delimited identifier>, which the grammar expands in full. It is the
// two together that are the standard saying, in two places and in two
// documents, that what this rule matches is nobody's to write down but the
// implementer's.
func (c *Catalog) LeftToTheImplementation(name string) bool {
	p, ok := c.Production(name)
	if !ok || !p.SeeTheRules {
		return false
	}
	// The lists write a rule name in guillemets where the grammar writes it in
	// angle brackets, so the needle is built rather than matched loosely: a
	// substring search for the bare name would find <graph name> inside <graph
	// type name> and call the wrong rule implementation-defined.
	needle := "‹" + p.Name + "›"
	for _, list := range [][]Behaviour{c.ImplementationDefined, c.ImplementationDependent} {
		for _, b := range list {
			if strings.Contains(b.Description, needle) {
				return true
			}
		}
	}
	return false
}

// Behaviour returns the implementation-defined or implementation-dependent
// item with the given code, from either list.
//
// The two lists are searched together because the codes do not collide and a
// caller citing IA015 almost never cares which of the two documents it came
// from until it has found it. Which list it came from is IsDefined's answer.
func (c *Catalog) Behaviour(code string) (Behaviour, bool) {
	b, ok := c.byBehaviour[strings.ToUpper(strings.TrimSpace(code))]
	return b, ok
}

// IsDefined reports whether the code names an implementation-defined item,
// which is the one of the two lists Clause 24.5.2 obliges an implementer to
// write down. An implementation-dependent item carries no such obligation and
// answers false here, as does a code neither list holds.
func (c *Catalog) IsDefined(code string) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, b := range c.ImplementationDefined {
		if b.Code == code {
			return true
		}
	}
	return false
}

// Subclause returns the clause or subclause with the given number.
func (c *Catalog) Subclause(number string) (Subclause, bool) {
	s, ok := c.bySubclause[strings.TrimSpace(number)]
	return s, ok
}

// NormativeSubclauses returns the subclauses a conformance case can sensibly
// cite: the ones that specify behaviour, not the front matter. It is the
// denominator the report divides mandatory coverage by.
func (c *Catalog) NormativeSubclauses() []Subclause {
	var out []Subclause
	for _, s := range c.Subclauses {
		if s.Normative {
			out = append(out, s)
		}
	}
	return out
}

// Status describes a five-character GQLSTATUS code, e.g. "22G03". The bool
// reports whether ISO defines it; an implementation returning an undefined
// code is itself a conformance observation.
func (c *Catalog) Status(code string) (string, bool) {
	s, ok := c.statuses[strings.ToUpper(code)]
	return s, ok
}

// Families returns the feature families in code order together with how many
// features each holds.
func (c *Catalog) Families() []struct {
	Family
	Count int
} {
	counts := map[string]int{}
	for _, f := range c.Features {
		counts[f.Family]++
	}
	out := make([]struct {
		Family
		Count int
	}, 0, len(families))
	for _, fam := range families {
		out = append(out, struct {
			Family
			Count int
		}{fam, counts[fam.Prefix]})
	}
	return out
}

// Codes adapts a Catalog to the bool-returning lookups a corpus validator
// needs. It exists so neither package has to import the other: the corpus
// stays a data model with no opinion about where codes come from, and this
// package stays a decoder with no opinion about tests.
type Codes struct{ *Catalog }

// Feature reports whether the code is one of the 228 ISO optional features.
func (c Codes) Feature(code string) bool {
	_, ok := c.Catalog.Feature(code)
	return ok
}

// Production reports whether the name is a rule in the ISO grammar.
func (c Codes) Production(name string) bool {
	_, ok := c.Catalog.Production(name)
	return ok
}

// SeeTheRules reports whether the grammar leaves this rule unexpanded, writing
// "!! See the Syntax Rules." in place of a right-hand side.
func (c Codes) SeeTheRules(name string) bool {
	p, ok := c.Catalog.Production(name)
	return ok && p.SeeTheRules
}

// Names reports whether this rule's right-hand side names a thing of this kind
// directly, either the rule for its name or the rule for a reference to one.
//
// Directly is the point. Almost every rule in the grammar reaches almost every
// other one, a value expression being able to hold a nested query and a nested
// query being able to hold anything, so reachability says nothing about what a
// rule is for. What a rule spells in its own right-hand side does.
//
// The kind is written the way the grammar writes the thing rather than the
// rule, "graph type" and not "graph type name".
func (c Codes) Names(production, kind string) bool {
	p, ok := c.Catalog.Production(production)
	if !ok {
		return false
	}
	kind = strings.Trim(kind, "<>")
	return slices.Contains(p.References, kind+" name") ||
		slices.Contains(p.References, kind+" reference")
}

// Creates reports whether the grammar spells a statement that puts a thing of
// this kind into the catalog.
//
// ISO names its catalog-modifying statements after what they make, <create
// schema statement>, <create graph statement>, <create graph type statement>,
// so the question is answerable by name. A kind with no create statement is a
// kind no GQL program can bring into existence: something outside the language
// has to have put it there, and a portable case cannot count on it being
// there or on what it is called.
//
// The kind is written the way the grammar writes the thing rather than the
// rule, "graph type" and not "graph type name".
func (c Codes) Creates(kind string) bool {
	_, ok := c.Catalog.Production("create " + strings.Trim(kind, "<>") + " statement")
	return ok
}

// Status reports whether the code is a GQLSTATUS ISO defines.
func (c Codes) Status(code string) bool {
	_, ok := c.Catalog.Status(code)
	return ok
}

// Subclause reports whether the number names a clause that specifies
// behaviour an implementation can conform to.
func (c Codes) Subclause(number string) bool {
	s, ok := c.Catalog.Subclause(number)
	return ok && s.Normative
}

// Item returns the standard's own description of an implementation-defined or
// implementation-dependent item, from either list.
func (c Codes) Item(code string) (string, bool) {
	b, ok := c.Behaviour(code)
	return b.Description, ok
}

// Defined reports whether the code is on the implementation-defined list, the
// one Clause 24.5.2 obliges an implementer to write down.
func (c Codes) Defined(code string) bool { return c.IsDefined(code) }

// Keywords returns every distinct keyword the grammar spells out, sorted.
// The reserved-word tests draw their inputs from this list.
func (c *Catalog) Keywords() []string {
	seen := map[string]bool{}
	for _, p := range c.Productions {
		for _, k := range p.Keywords {
			seen[k] = true
		}
	}
	return sortedKeys(seen)
}

// Reserved reports whether the word may not be spelled as a
// <regular identifier>, comparing without regard to case.
//
// Subclause 21.3 puts this rule in the Syntax Rules rather than in the
// productions, so a harness that generates or checks statements has to apply
// it itself: the grammar alone will happily let a case call a column `start`,
// and every conforming engine will refuse the statement.
//
// The <reserved word> production names <pre-reserved word> rather than
// repeating its forty words, so both are read. ISO has given those forty no
// meaning yet and an engine may well admit them; a case in this corpus still
// avoids them, because a case is not the place to find out which engines do.
func (c *Catalog) Reserved(word string) bool {
	return c.reserved[strings.ToUpper(strings.TrimSpace(word))]
}

// ReservedWords returns every word Reserved answers true for, sorted and
// upper cased.
func (c *Catalog) ReservedWords() []string { return sortedKeys(c.reserved) }

func (c *Catalog) indexReserved() {
	c.reserved = map[string]bool{}
	for _, name := range []string{"reserved word", "pre-reserved word"} {
		p, ok := c.Production(name)
		if !ok {
			continue
		}
		for _, k := range p.Keywords {
			c.reserved[strings.ToUpper(k)] = true
		}
	}
}

// --- decoding -------------------------------------------------------------

// frontMatter are the clauses that specify nothing to conform to. Clause 24 is
// about conformance rather than a thing to conform to, so a case citing it
// would be citing the rules of the game as a move in it.
var frontMatter = map[string]bool{"1": true, "2": true, "3": true, "5": true, "24": true}

func parseSubclauses(data []byte) ([]Subclause, error) {
	var out []Subclause
	seen := map[string]bool{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		number, title, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("line %d: no tab between number and title: %q", i+1, line)
		}
		number, title = strings.TrimSpace(number), strings.TrimSpace(title)
		if number == "" || title == "" {
			return nil, fmt.Errorf("line %d: empty number or title", i+1)
		}
		if seen[number] {
			return nil, fmt.Errorf("line %d: duplicate subclause %s", i+1, number)
		}
		seen[number] = true
		clause, _, _ := strings.Cut(number, ".")
		out = append(out, Subclause{
			Number:    number,
			Title:     title,
			Depth:     strings.Count(number, ".") + 1,
			Normative: !frontMatter[clause],
		})
	}
	if len(out) == 0 {
		return nil, errors.New("no subclauses")
	}
	return out, nil
}

func parseFeatures(data []byte) ([]Feature, error) {
	var doc struct {
		Features []struct {
			Code        string `xml:"code"`
			Description string `xml:"description"`
		} `xml:"feature"`
	}
	if err := decode(data, &doc); err != nil {
		return nil, err
	}
	out := make([]Feature, 0, len(doc.Features))
	for _, f := range doc.Features {
		code := strings.TrimSpace(f.Code)
		out = append(out, Feature{
			Code:        code,
			Description: collapse(f.Description),
			Family:      familyOf(code),
		})
	}
	if len(out) == 0 {
		return nil, errors.New("no <feature> elements")
	}
	return out, nil
}

// familyOf takes the leading letters of a feature code. ISO codes are letters
// followed by digits, so "GQ08" is family GQ and "G002" is family G.
func familyOf(code string) string {
	for i, r := range code {
		if r >= '0' && r <= '9' {
			return code[:i]
		}
	}
	return code
}

func parseConditions(data []byte) ([]Class, error) {
	var doc struct {
		Classes []struct {
			Category   string `xml:"category,attr"`
			Code       string `xml:"code,attr"`
			Name       string `xml:"name,attr"`
			Subclasses []struct {
				Code string `xml:"code,attr"`
				Name string `xml:"name,attr"`
			} `xml:"subclass"`
		} `xml:"class"`
	}
	if err := decode(data, &doc); err != nil {
		return nil, err
	}
	out := make([]Class, 0, len(doc.Classes))
	for _, cl := range doc.Classes {
		c := Class{Category: cl.Category, Code: cl.Code, Name: cl.Name}
		for _, sc := range cl.Subclasses {
			c.Subclasses = append(c.Subclasses, Subclass{Code: sc.Code, Name: sc.Name})
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("no <class> elements")
	}
	return out, nil
}

func parseBehaviours(data []byte, element string) ([]Behaviour, error) {
	// The two artifacts differ only in the element name they repeat, so one
	// streaming pass keyed on that name reads both.
	dec := newDecoder(data)
	var out []Behaviour
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != element {
			continue
		}
		var b struct {
			Code        string `xml:"code"`
			Description string `xml:"description"`
		}
		if err := dec.DecodeElement(&b, &start); err != nil {
			return nil, err
		}
		out = append(out, Behaviour{
			Code:        strings.TrimSpace(b.Code),
			Description: collapse(b.Description),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no <%s> elements", element)
	}
	return out, nil
}

// parseGrammar walks bnf.xml and records, per production, the other
// productions it names and the literal words and symbols it spells out. The
// nesting of alt/opt/group carries precedence the harness does not need, so
// the walk flattens it: what the grammar tests want to know is which rules
// and which words exist, not how they associate.
func parseGrammar(data []byte) ([]Production, error) {
	dec := newDecoder(data)
	var out []Production
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "BNFdef" {
			continue
		}
		p := Production{}
		for _, a := range start.Attr {
			switch a.Name.Local {
			case "name":
				p.Name = a.Value
			case "predicative":
				p.Predicative = a.Value == "yes"
			}
		}
		side := rhs{refs: map[string]bool{}, kws: map[string]bool{}, terms: map[string]bool{}}
		if err := walkRHS(dec, &side); err != nil {
			return nil, err
		}
		p.References = sortedKeys(side.refs)
		p.Keywords = sortedKeys(side.kws)
		p.Terminals = sortedKeys(side.terms)
		p.SeeTheRules = side.seeTheRules
		p.RequiresKeyword = side.requiresKeyword()
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("no <BNFdef> elements")
	}
	return out, nil
}

// rhs is what one walk of a right-hand side collects.
type rhs struct {
	refs, kws, terms map[string]bool
	// seeTheRules is set by the <seeTheRules/> element, the artifact's spelling
	// of "!! See the Syntax Rules.".
	seeTheRules bool
	// alts counts the top-level <alt> elements and keyworded counts the ones
	// holding a keyword outside every <opt>. A rule with no <alt> at all is
	// one alternative, which is why alts starts at zero and requiresKeyword
	// reads it as one.
	alts, keyworded int
	// optDepth is how many <opt> elements deep the walk currently is, and
	// altKeyword records whether the alternative being walked has yet spelled
	// a keyword the grammar does not make optional.
	optDepth   int
	altKeyword bool
}

// requiresKeyword reports whether every alternative of the walked rule spells
// one of the rule's own keywords at a non-optional position.
func (r *rhs) requiresKeyword() bool {
	alts, keyworded := r.alts, r.keyworded
	if alts == 0 {
		// No <alt>: the whole right-hand side is the one alternative, and the
		// walk left its verdict in altKeyword.
		alts = 1
		if r.altKeyword {
			keyworded = 1
		}
	}
	return keyworded == alts
}

// walkRHS consumes tokens up to the end of the element the decoder has just
// entered, collecting every <BNF> reference, <kw> keyword, and
// <terminalsymbol> it passes.
//
// It also tracks two things the flat lists cannot carry: which <alt> elements
// are the rule's own alternatives rather than an inner group's, and whether
// the walk is inside an <opt>. Those two are what turn "this rule mentions
// MATCH somewhere" into "a case using this rule has to say MATCH".
func walkRHS(dec *xml.Decoder, side *rhs) error {
	depth := 1
	// rhsDepth is the depth at which <rhs> was seen, so an <alt> one deeper is
	// one of the rule's own alternatives and an <alt> further in is not.
	rhsDepth := -1
	var opts []int
	finish := func() {
		if side.alts > 0 && side.altKeyword {
			side.keyworded++
		}
	}
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			here := depth
			depth++
			switch t.Name.Local {
			case "rhs":
				rhsDepth = here
			case "alt":
				if here == rhsDepth+1 {
					finish()
					side.alts++
					side.altKeyword = false
				}
			case "opt":
				opts = append(opts, here)
				side.optDepth++
			}
			switch t.Name.Local {
			case "BNF", "allAltsFrom":
				for _, a := range t.Attr {
					if a.Name.Local == "name" {
						side.refs[a.Value] = true
					}
				}
			case "seeTheRules":
				side.seeTheRules = true
			case "kw":
				if s, err := text(dec, t); err == nil {
					depth--
					if s != "" {
						side.kws[strings.ToUpper(s)] = true
						if side.optDepth == 0 {
							side.altKeyword = true
						}
					}
				}
			case "terminalsymbol":
				if s, err := text(dec, t); err == nil {
					depth--
					if s != "" {
						side.terms[s] = true
					}
				}
			}
		case xml.EndElement:
			depth--
			if n := len(opts); n > 0 && opts[n-1] == depth {
				opts = opts[:n-1]
				side.optDepth--
			}
		}
	}
	finish()
	return nil
}

func text(dec *xml.Decoder, start xml.StartElement) (string, error) {
	var s string
	if err := dec.DecodeElement(&s, &start); err != nil {
		return "", err
	}
	return collapse(s), nil
}

// newDecoder returns a decoder that tolerates the artifacts' inline DTDs and
// their non-UTF8-declared entities. The files are ISO's own and are read
// only from the embedded copy, never from the network.
func newDecoder(data []byte) *xml.Decoder {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity
	return dec
}

func decode(data []byte, v any) error { return newDecoder(data).Decode(v) }

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
