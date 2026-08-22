package corpus

import (
	_ "embed"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

//go:embed uncitable-subclause.yaml
var uncitableSubclauseYAML []byte

// SubclauseWhy is why no case can cite a normative subclause. There are two,
// and a third means finding another fact about the standard's own structure a
// program can check rather than another way of saying nobody has got to it yet.
type SubclauseWhy string

const (
	// SpellsARegisteredRule is a subclause whose title is the grammar rules it
	// defines and nothing else, where the grammar register already holds every
	// one of them. The rules are out of reach, the subclause is the rules, so
	// the subclause is out of reach with them.
	SpellsARegisteredRule SubclauseWhy = "registered-rule"
	// ImplementationInternal is a subclause of Clause 4, Concepts, whose subject
	// is machinery inside a GQL-implementation or a term the standard states
	// rules elsewhere in, and which the grammar gives a statement no way to name.
	//
	// This is a weaker kind of claim than the one above and the register says so
	// out loud. The first proves uncitability: the subclause is the rules, the
	// rules are unreachable, nothing is left. This one proves less. It proves
	// that the subclause's own subject appears in no rule name in the published
	// grammar, and that any rule the title does name is one a case can write, so
	// that what a statement addresses is those rules and not this. The rest is
	// left to a note a reader has to agree with. What keeps it honest is that a
	// citation always wins: a case citing a registered subclause fails the load,
	// so the day somebody finds a statement that addresses one of these, the
	// entry is what is wrong and the loader says so.
	ImplementationInternal SubclauseWhy = "implementation-internal"
)

// Because is the sentence a report writes about a group of entries sharing this
// reason, kept beside the constant so that a third cannot be added without
// saying what it means to a reader.
func (w SubclauseWhy) Because() string {
	switch w {
	case SpellsARegisteredRule:
		return "the subclause defines grammar rules and nothing else, and the grammar register already holds every one of them, so a case that cannot write the syntax cannot cite the subclause that spells it"
	case ImplementationInternal:
		return "the subclause is in the concepts clause and its subject is machinery inside an implementation or a term the standard states other rules in, named by no rule of the grammar, so what a statement can address is those other rules and never this"
	}
	return string(w)
}

// UncitableSubclause is one normative subclause no case can cite.
//
// It is the smallest of the three registers and meant to stay that way. A
// subclause is a piece of ISO's own table of contents and nearly every one of
// them specifies something a query can be written against, so the honest answer
// to an uncited subclause is almost always a case. Clause headings are not in
// here either: a heading specifies nothing on its own, and the counter can see
// for itself that a case cites something beneath it.
type UncitableSubclause struct {
	// Subclause is the dotted number, e.g. "15.3".
	Subclause string `yaml:"subclause" json:"subclause"`
	// Why is the reason, checked against ISO's own title for the subclause and
	// against the grammar register.
	Why SubclauseWhy `yaml:"why" json:"why"`
	// Object is the thing the subclause is about, written the way ISO's own
	// title writes it. Required by ImplementationInternal and refused by the
	// other reason, which reads its objects out of the title's angle brackets.
	//
	// It is a separate field rather than something derived from the title
	// because the title is a sentence and the subject is a phrase inside it:
	// "Execution context creation and initialization" is about an execution
	// context, and a check that searched the grammar for the whole title would
	// pass anything long enough.
	Object string `yaml:"object" json:"object,omitempty"`
	// Note is why this is the end of it rather than a gap. Required: an entry
	// without one is a subclause somebody gave up on.
	Note string `yaml:"note" json:"note"`
}

// uncitableSubclauseFile is the on-disk shape of the register.
type uncitableSubclauseFile struct {
	// Version is the document's schema, so a later change is detected rather
	// than silently misread.
	Version   int                  `yaml:"version"`
	Uncitable []UncitableSubclause `yaml:"uncitable"`
}

// KnownStandard is the slice of the ISO catalogue this register needs: the
// grammar, so an entry can be checked against the rules it names, and the
// document's structure, so the entry can be read against ISO's own title.
type KnownStandard interface {
	KnownGrammar
	// Subclause reports whether the number names a clause that specifies
	// behaviour an implementation can conform to.
	Subclause(number string) bool
	// SubclauseTitle is the standard's own heading for the number.
	SubclauseTitle(number string) (string, bool)
	// ProductionsNaming is the rules whose names contain the phrase, sorted. An
	// empty answer is the grammar saying no statement addresses the thing.
	ProductionsNaming(phrase string) []string
}

// inAngleBrackets pulls the grammar rules out of a subclause title. ISO titles
// a syntactic subclause with the rules it defines, spelled the way the grammar
// spells them, which is what makes the check below possible at all.
var inAngleBrackets = regexp.MustCompile(`<([^<>]+)>`)

// inClause reports whether a dotted subclause number sits under a clause. The
// dot is what does the work: "4" and "4.2.1" are under Clause 4 and "40" is not.
func inClause(subclause, clause string) bool {
	return subclause == clause || strings.HasPrefix(subclause, clause+".")
}

// ReadUncitableSubclauses parses a register and checks every claim in it a
// machine can check, against the standard's own titles and against the grammar
// register.
//
// The check that matters is the one that keeps this register from growing. An
// entry does not get to say a subclause is out of reach: it says the subclause
// is the grammar rules its title spells, the loader reads ISO's own title to
// find out which those are, and the grammar register has to already hold every
// one of them. So an entry here can only stand behind an entry there, one rule
// left out of the grammar register is enough to refuse this one, and a subclause
// ISO titles in words rather than in rules has nothing to stand behind at all.
//
// The second reason has a check of its own and a weaker one, because there is
// no rule to stand behind. The entry names the thing the subclause is about,
// ISO's own title has to contain that name, the subclause has to be in Clause 4
// where the standard puts the words it states the rest of itself in, and no rule
// of the grammar may have the name in it. That last part is the whole claim: the
// grammar names its rules after what they are, so a thing a statement can address
// has a rule with that thing in its name, and a thing with no such rule is one no
// statement addresses.
func ReadUncitableSubclauses(data []byte, known KnownStandard, rules []Uncitable) ([]UncitableSubclause, error) {
	var f uncitableSubclauseFile
	if err := yaml.UnmarshalWithOptions(data, &f, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("uncitable subclause: %w", err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("uncitable subclause: version %d is not one this build reads", f.Version)
	}
	registeredRule := UncitableProductions(rules)
	seen := map[string]bool{}
	for i, u := range f.Uncitable {
		where := fmt.Sprintf("uncitable subclause entry %d", i+1)
		if u.Subclause != "" {
			where = "uncitable subclause " + u.Subclause
		}
		switch {
		case u.Subclause == "":
			return nil, fmt.Errorf("%s: no subclause, so there is nothing to check", where)
		case seen[u.Subclause]:
			return nil, fmt.Errorf("%s: listed twice", where)
		case !known.Subclause(u.Subclause):
			return nil, fmt.Errorf("%s: not a normative subclause of ISO/IEC 39075", where)
		case strings.TrimSpace(u.Note) == "":
			return nil, fmt.Errorf("%s: no note saying why no case can cite it", where)
		}
		seen[u.Subclause] = true

		switch u.Why {
		case SpellsARegisteredRule:
			if u.Object != "" {
				return nil, fmt.Errorf("%s: the entry names %q as its object, and this reason reads its objects out of the rules in ISO's title", where, u.Object)
			}
			title, _ := known.SubclauseTitle(u.Subclause)
			named := inAngleBrackets.FindAllStringSubmatch(title, -1)
			if len(named) == 0 {
				return nil, fmt.Errorf("%s: ISO titles it %q, which names no grammar rule, so the entry stands behind nothing",
					where, title)
			}
			for _, m := range named {
				rule := m[1]
				switch {
				case !known.Production(rule):
					return nil, fmt.Errorf("%s: its title names <%s>, which is not a rule in the grammar",
						where, rule)
				case !registeredRule[rule]:
					return nil, fmt.Errorf("%s: the grammar register does not hold <%s>, so a case that cites that rule cites this subclause",
						where, rule)
				}
			}
		case ImplementationInternal:
			object := strings.TrimSpace(u.Object)
			title, _ := known.SubclauseTitle(u.Subclause)
			switch {
			case object == "":
				return nil, fmt.Errorf("%s: no object, so there is nothing to look for in the grammar", where)
			case !inClause(u.Subclause, "4"):
				return nil, fmt.Errorf("%s: this reason is for Clause 4, where the standard says what its words mean, and the subclause is somewhere else", where)
			case !strings.Contains(strings.ToLower(title), strings.ToLower(object)):
				return nil, fmt.Errorf("%s: the entry says it is about %q and ISO titles it %q, which does not say that", where, object, title)
			}
			if named := known.ProductionsNaming(object); len(named) > 0 {
				return nil, fmt.Errorf("%s: the grammar names <%s>, so a statement can say %q and a case can cite the subclause that defines it",
					where, named[0], object)
			}
			// A title here may still name rules, because machinery is machinery
			// for something. Those rules have to be ones a case can write: the
			// subclause is then the part behind syntax the corpus reaches, and the
			// syntax is where the case belongs. A title naming a rule the grammar
			// register holds is a different entry, and it belongs under the other
			// reason rather than this one.
			for _, m := range inAngleBrackets.FindAllStringSubmatch(title, -1) {
				rule := m[1]
				switch {
				case !known.Production(rule):
					return nil, fmt.Errorf("%s: its title names <%s>, which is not a rule in the grammar", where, rule)
				case registeredRule[rule]:
					return nil, fmt.Errorf("%s: its title names <%s>, which the grammar register holds, so the entry is about syntax no case can write and belongs under %q",
						where, rule, SpellsARegisteredRule)
				}
			}
		case "":
			return nil, fmt.Errorf("%s: no reason, so the entry claims nothing a machine can check", where)
		default:
			return nil, fmt.Errorf("%s: %q is not a reason this build knows", where, u.Why)
		}
	}
	out := append([]UncitableSubclause(nil), f.Uncitable...)
	sort.Slice(out, func(i, j int) bool { return out[i].Subclause < out[j].Subclause })
	return out, nil
}

// UncitableSubclauses returns the register that ships with this package,
// checked against the standard and against the grammar register beside it.
func UncitableSubclauses(known KnownStandard) ([]UncitableSubclause, error) {
	rules, err := Uncitables(known)
	if err != nil {
		return nil, err
	}
	return ReadUncitableSubclauses(uncitableSubclauseYAML, known, rules)
}

// UncitableSubclauseDocument returns the shipped register verbatim, for a
// caller who wants to extend it rather than replace it.
func UncitableSubclauseDocument() []byte {
	return append([]byte(nil), uncitableSubclauseYAML...)
}

// UncitableSubclauseNumbers is the subclause numbers of a register, as a set.
func UncitableSubclauseNumbers(us []UncitableSubclause) map[string]bool {
	m := make(map[string]bool, len(us))
	for _, u := range us {
		m[u.Subclause] = true
	}
	return m
}

// SubclauseWhys is the distinct reasons a register gives, in the order they are
// first met, so that anything printed from it comes out the same twice running.
func SubclauseWhys(us []UncitableSubclause) []SubclauseWhy {
	var out []SubclauseWhy
	seen := map[SubclauseWhy]bool{}
	for _, u := range us {
		if !seen[u.Why] {
			seen[u.Why] = true
			out = append(out, u.Why)
		}
	}
	return out
}
