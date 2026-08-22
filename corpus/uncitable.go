package corpus

import (
	_ "embed"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

//go:embed uncitable.yaml
var uncitableYAML []byte

// Why is why no case can cite a grammar production. There are three, each with
// a check a machine can make, and a fourth means finding another fact about the
// grammar a program can verify rather than another way of saying nobody has got
// to it yet.
type Why string

const (
	// Implementers is a rule the grammar declines to expand and one of ISO's
	// two lists of implementation-defined and implementation-dependent items
	// names. What it matches is the implementer's to decide, so a case that
	// spelled one would be spelling this project's guess.
	Implementers Why = "implementers"
	// BehindAnUnwritableFeature is a rule the feature register already names.
	// The feature it spells has no portable case, and a rule reached only by
	// writing that case is reached by nobody.
	BehindAnUnwritableFeature Why = "unwritable-feature"
	// Orphaned is a rule every referrer of which is itself registered. There is
	// no path through the grammar that reaches it without going through
	// something already known to be out of reach.
	Orphaned Why = "orphaned"
)

// Because is the sentence a report writes about a group of entries sharing this
// reason, kept beside the constant so that a fourth cannot be added without
// saying what it means to a reader.
func (w Why) Because() string {
	switch w {
	case Implementers:
		return "the grammar declines to expand the rule and ISO's own list of implementation-defined items names it, so what it matches is the implementer's to decide and a case spelling one would be spelling this project's guess"
	case BehindAnUnwritableFeature:
		return "the rule spells an optional feature the feature register already says no portable case can be written for, so the only way to reach it is to write the case that cannot be written"
	case Orphaned:
		return "every rule that names this one is itself registered, so there is no path through the grammar that reaches it without going through something already out of reach"
	}
	return string(w)
}

// Uncitable is one grammar production no case can cite.
//
// A citation is a claim that the case exercises the rule, which is what makes
// the grammar denominator worth reading and what makes a padded citation worse
// than an honest gap. So the uncited productions split two ways, and only one
// of them is work: a rule nobody has written a case for yet, and a rule no case
// can reach. These are the second kind.
//
// The bar is the one the feature register sets. An entry is not "hard to
// exercise", "no engine implements it", or "the fixture does not have one":
// those are all cases somebody has not written. It is a claim about the shape
// of the grammar, and every one of the three reasons is checked against the
// grammar at load time.
//
// Nothing here changes a denominator. There are still 814 productions and a
// corpus citing 429 of them still reads as 429 of 814. What changes is that
// these stop appearing in the list of work nobody has done.
type Uncitable struct {
	// Production is the rule's name without angle brackets.
	Production string `yaml:"production" json:"production"`
	// Why is which of the three the entry claims.
	Why Why `yaml:"why" json:"why"`
	// Feature is the optional feature code the rule spells. Required by
	// BehindAnUnwritableFeature, which checks it against the feature register,
	// and refused by the other two.
	Feature string `yaml:"feature,omitempty" json:"feature,omitempty"`
	// Note is why this is the end of it rather than a gap. Required: an entry
	// without one is a production somebody gave up on.
	Note string `yaml:"note" json:"note"`
}

// uncitableFile is the on-disk shape of the register.
type uncitableFile struct {
	// Version is the document's schema, so a later change is detected rather
	// than silently misread.
	Version   int         `yaml:"version"`
	Uncitable []Uncitable `yaml:"uncitable"`
}

// ReadUncitable parses a register and checks every claim in it a machine can
// check, against the grammar and against the feature register.
//
// The orphan check is the one worth describing, because it is the one that
// makes the register hold together rather than grow. A rule is an orphan when
// every rule naming it is registered here too, so an orphan entry can only be
// added behind an entry that earned its place some other way, and the register
// cannot creep outward through rules that are in fact reachable. It also means
// the entries are checked in two passes: the set has to exist before any
// membership question about it can be asked.
func ReadUncitable(data []byte, known KnownGrammar, features []Unwritable) ([]Uncitable, error) {
	var f uncitableFile
	if err := yaml.UnmarshalWithOptions(data, &f, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("uncitable: %w", err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("uncitable: version %d is not one this build reads", f.Version)
	}
	registered := map[string]bool{}
	for i, u := range f.Uncitable {
		where := fmt.Sprintf("uncitable entry %d", i+1)
		if u.Production != "" {
			where = "uncitable <" + u.Production + ">"
		}
		switch {
		case u.Production == "":
			return nil, fmt.Errorf("%s: no production, so there is nothing to check", where)
		case registered[u.Production]:
			return nil, fmt.Errorf("%s: listed twice", where)
		case !known.Production(u.Production):
			return nil, fmt.Errorf("%s: not a rule in the ISO/IEC 39075 grammar", where)
		case strings.TrimSpace(u.Note) == "":
			return nil, fmt.Errorf("%s: no note saying why no case can cite it", where)
		}
		registered[u.Production] = true
	}
	// Which feature the register says is unwritable, by the rule it names, so a
	// BehindAnUnwritableFeature entry is checked against the other register
	// rather than against a repetition of it.
	unwritableAt := map[string][]string{}
	for _, u := range features {
		unwritableAt[u.Production] = append(unwritableAt[u.Production], u.Feature)
	}
	for _, u := range f.Uncitable {
		where := "uncitable <" + u.Production + ">"
		if u.Why != BehindAnUnwritableFeature && u.Feature != "" {
			return nil, fmt.Errorf("%s: a %s entry names no feature, its claim being about the grammar rather than about one feature",
				where, u.Why)
		}
		switch u.Why {
		case Implementers:
			if !known.LeftToTheImplementation(u.Production) {
				return nil, fmt.Errorf("%s: the grammar expands this rule, or no implementation-defined item names it, so its spelling is not the implementer's",
					where)
			}
		case BehindAnUnwritableFeature:
			switch {
			case u.Feature == "":
				return nil, fmt.Errorf("%s: no feature, so there is no register entry to stand behind", where)
			case !slices.Contains(unwritableAt[u.Production], u.Feature):
				return nil, fmt.Errorf("%s: the feature register does not say %s hangs off this rule and cannot be written",
					where, u.Feature)
			}
		case Orphaned:
			from := known.Referrers(u.Production)
			if len(from) == 0 {
				return nil, fmt.Errorf("%s: no rule in the grammar names it, so it is a start symbol and not an orphan", where)
			}
			for _, r := range from {
				if !registered[r] {
					return nil, fmt.Errorf("%s: <%s> names it and is not registered, so a case that cites <%s> reaches it",
						where, r, r)
				}
			}
		case "":
			return nil, fmt.Errorf("%s: no reason, so the entry claims nothing a machine can check", where)
		default:
			return nil, fmt.Errorf("%s: %q is not a reason this build knows", where, u.Why)
		}
	}
	out := append([]Uncitable(nil), f.Uncitable...)
	sort.Slice(out, func(i, j int) bool { return out[i].Production < out[j].Production })
	return out, nil
}

// Uncitables returns the register that ships with this package, checked against
// the grammar and against the feature register that ships beside it.
func Uncitables(known KnownGrammar) ([]Uncitable, error) {
	features, err := Unwritables(known)
	if err != nil {
		return nil, err
	}
	return ReadUncitable(uncitableYAML, known, features)
}

// UncitableDocument returns the shipped register verbatim, for a caller who
// wants to extend it rather than replace it.
func UncitableDocument() []byte { return append([]byte(nil), uncitableYAML...) }

// UncitableProductions is the rule names of a register, as a set.
func UncitableProductions(us []Uncitable) map[string]bool {
	m := make(map[string]bool, len(us))
	for _, u := range us {
		m[u.Production] = true
	}
	return m
}

// Whys is the distinct reasons a register gives, in the order they are first
// met, so that anything printed from it comes out the same twice running.
func Whys(us []Uncitable) []Why {
	var out []Why
	seen := map[Why]bool{}
	for _, u := range us {
		if !seen[u.Why] {
			seen[u.Why] = true
			out = append(out, u.Why)
		}
	}
	return out
}
