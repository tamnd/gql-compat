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

// SubclauseWhy is why no case can cite a normative subclause. There is one,
// and a second means finding another fact about the standard's own structure a
// program can check rather than another way of saying nobody has got to it yet.
type SubclauseWhy string

// SpellsARegisteredRule is a subclause whose title is the grammar rules it
// defines and nothing else, where the grammar register already holds every one
// of them. The rules are out of reach, the subclause is the rules, so the
// subclause is out of reach with them.
const SpellsARegisteredRule SubclauseWhy = "registered-rule"

// Because is the sentence a report writes about a group of entries sharing this
// reason, kept beside the constant so that a second cannot be added without
// saying what it means to a reader.
func (w SubclauseWhy) Because() string {
	if w == SpellsARegisteredRule {
		return "the subclause defines grammar rules and nothing else, and the grammar register already holds every one of them, so a case that cannot write the syntax cannot cite the subclause that spells it"
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
}

// inAngleBrackets pulls the grammar rules out of a subclause title. ISO titles
// a syntactic subclause with the rules it defines, spelled the way the grammar
// spells them, which is what makes the check below possible at all.
var inAngleBrackets = regexp.MustCompile(`<([^<>]+)>`)

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
