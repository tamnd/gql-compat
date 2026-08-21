package corpus

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

//go:embed unwritable.yaml
var unwritableYAML []byte

// Reason is why no portable case can be written for a feature. There are two,
// each with a check a machine can make, and adding a third means finding
// another fact about the standard that a program can verify rather than
// another way of saying the work is hard.
type Reason string

const (
	// Prose is a feature whose grammar rule ISO writes as "!! See the Syntax
	// Rules." rather than as an expansion. The standard describes the feature
	// and leaves its spelling to the implementer, so there are no words two
	// engines could be asked the same question in.
	Prose Reason = "prose"
	// Unnameable is a feature whose syntax the standard does spell, and spells
	// completely, except that the only thing which completes it is the name of
	// a catalog object no GQL statement can create. ISO names its create
	// statements after what they make and there is no create procedure
	// statement, so a case would have to invent a name, and every engine would
	// fail it for the same uninteresting reason.
	Unnameable Reason = "unnameable"
)

// Because is the sentence a report writes about a group of entries sharing
// this reason. It is here so that the markdown, the HTML and the terminal say
// the same thing, and so that a third reason cannot be added without saying
// what it means to a reader.
func (r Reason) Because() string {
	switch r {
	case Prose:
		return "ISO writes the grammar rule the feature hangs off as \"!! See the Syntax Rules.\", so the standard supplies no words to ask two engines the same question in, and a case would test this project's invention rather than the standard"
	case Unnameable:
		return "ISO spells the feature's syntax in full and ends it in the name of a catalog object no GQL statement creates, so a case would have to invent a name the standard does not supply and every engine would fail it for the same uninteresting reason"
	}
	return string(r)
}

// Unwritable is one optional feature no portable case can be written for.
//
// The corpus asks every engine the same question, so a case is only worth
// having where the standard supplies the words to ask it in. Two things stop
// it doing so. A grammar rule may be written as "!! See the Syntax Rules."
// rather than as an expansion, in which case the feature has no spelling the
// standard owns. Or the spelling may be complete and end in the name of
// something no GQL statement can create, in which case the case has to invent
// the name. Either way, writing one would measure agreement with this project
// instead of agreement with ISO, and an engine that implements the feature
// perfectly would fail.
//
// This is a claim about the standard, not about any engine and not about the
// state of the corpus. It does not move a denominator, it is not a skip, and
// no result is derived from it: what it changes is that the feature stops
// appearing in the list of work nobody has done.
type Unwritable struct {
	// Feature is the ISO optional feature code, one of the 228.
	Feature string `yaml:"feature" json:"feature"`
	// Reason is which of the two the entry claims. Empty reads as Prose,
	// which is what the register held before there was a second one.
	Reason Reason `yaml:"reason,omitempty" json:"reason,omitempty"`
	// Production is the grammar rule the feature reaches. For Prose it is the
	// rule whose right-hand side is the see-the-rules marker; for Unnameable
	// it is the rule that reaches the name. It is what makes the entry
	// checkable rather than an opinion.
	Production string `yaml:"production" json:"production"`
	// Object is the kind of catalog object the syntax needs a name for,
	// written as the grammar writes the thing rather than the rule:
	// "procedure", not "procedure name". Required by Unnameable, refused by
	// Prose, which needs no name because it has no syntax.
	Object string `yaml:"object,omitempty" json:"object,omitempty"`
	// Note is where the feature is reachable from and why that is the end of
	// it. Required: an entry without one is a feature somebody gave up on.
	Note string `yaml:"note" json:"note"`
}

// KnownGrammar is the slice of the ISO catalogue the register needs. It is an
// interface for the same reason KnownCodes is: this package holds a data
// model and no opinion about where the standard's own words come from.
type KnownGrammar interface {
	// Feature reports whether the code is one of the 228 optional features.
	Feature(code string) bool
	// Production reports whether the name is a rule in the grammar.
	Production(name string) bool
	// SeeTheRules reports whether the grammar declines to expand this rule.
	SeeTheRules(name string) bool
	// Names reports whether a rule's right-hand side names a thing of this
	// kind directly, by its name or by a reference to one.
	Names(production, kind string) bool
	// Creates reports whether the grammar spells a statement that puts a
	// thing of this kind into the catalog.
	Creates(kind string) bool
}

// unwritableFile is the on-disk shape of the register.
type unwritableFile struct {
	// Version is the document's schema, so a later change is detected rather
	// than silently misread.
	Version    int          `yaml:"version"`
	Unwritable []Unwritable `yaml:"unwritable"`
}

// ReadUnwritable parses a register and checks every claim in it that a machine
// can check.
//
// The checks are the point. An entry names a feature ISO defines and a grammar
// rule ISO writes as see-the-rules, so an entry cannot be added for a feature
// that does have a spelling, and an entry survives a new edition of the
// artifacts only as long as the rule it cites still refuses to expand. The
// half no machine can check, that the cited rule is the feature's only way in,
// is what the note is for.
func ReadUnwritable(data []byte, known KnownGrammar) ([]Unwritable, error) {
	var f unwritableFile
	if err := yaml.UnmarshalWithOptions(data, &f, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("unwritable: %w", err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("unwritable: version %d is not one this build reads", f.Version)
	}
	seen := map[string]bool{}
	for i, u := range f.Unwritable {
		where := fmt.Sprintf("unwritable entry %d", i+1)
		if u.Feature != "" {
			where = "unwritable " + u.Feature
		}
		switch {
		case u.Feature == "":
			return nil, fmt.Errorf("%s: no feature code", where)
		case seen[u.Feature]:
			return nil, fmt.Errorf("%s: listed twice", where)
		case !known.Feature(u.Feature):
			return nil, fmt.Errorf("%s: no such optional feature in ISO/IEC 39075", where)
		case strings.TrimSpace(u.Note) == "":
			return nil, fmt.Errorf("%s: no note saying why no case can be written", where)
		case u.Production == "":
			return nil, fmt.Errorf("%s: no production, so the claim cannot be checked", where)
		}
		if u.Reason == "" {
			u.Reason = Prose
			f.Unwritable[i].Reason = Prose
		}
		switch u.Reason {
		case Prose:
			switch {
			case u.Object != "":
				return nil, fmt.Errorf("%s: a %s entry names no object, its whole claim being that there is no syntax to put one in",
					where, Prose)
			case !known.SeeTheRules(u.Production):
				return nil, fmt.Errorf("%s: <%s> is not a rule the grammar leaves to the implementer",
					where, u.Production)
			}
		case Unnameable:
			switch {
			case u.Object == "":
				return nil, fmt.Errorf("%s: no object, so there is no name to say the standard cannot supply", where)
			case !known.Production(u.Production):
				return nil, fmt.Errorf("%s: <%s> is not a rule in the grammar", where, u.Production)
			case !known.Production(u.Object + " name"):
				return nil, fmt.Errorf("%s: <%s name> is not a rule in the grammar", where, u.Object)
			case !known.Names(u.Production, u.Object):
				return nil, fmt.Errorf("%s: <%s> does not name a %s, so its syntax does not wait on one",
					where, u.Production, u.Object)
			case known.Creates(u.Object):
				return nil, fmt.Errorf("%s: the grammar spells a create %s statement, so a case can make one and name it",
					where, u.Object)
			}
		default:
			return nil, fmt.Errorf("%s: %q is not a reason this build knows", where, u.Reason)
		}
		seen[u.Feature] = true
	}
	out := append([]Unwritable(nil), f.Unwritable...)
	sort.Slice(out, func(i, j int) bool { return out[i].Feature < out[j].Feature })
	return out, nil
}

// Unwritables returns the register that ships with this package.
func Unwritables(known KnownGrammar) ([]Unwritable, error) {
	return ReadUnwritable(unwritableYAML, known)
}

// UnwritableDocument returns the shipped register verbatim, for a caller who
// wants to extend it rather than replace it.
func UnwritableDocument() []byte { return append([]byte(nil), unwritableYAML...) }

// UnwritableCodes is the feature codes of a register, as a set.
func UnwritableCodes(us []Unwritable) map[string]bool {
	m := make(map[string]bool, len(us))
	for _, u := range us {
		m[u.Feature] = true
	}
	return m
}

// Reasons is the distinct reasons a register gives, in the order they are
// first met, so that anything printed from it comes out the same twice
// running.
func Reasons(us []Unwritable) []Reason {
	var out []Reason
	seen := map[Reason]bool{}
	for _, u := range us {
		if !seen[u.Reason] {
			seen[u.Reason] = true
			out = append(out, u.Reason)
		}
	}
	return out
}
