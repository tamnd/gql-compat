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

// Unwritable is one optional feature no portable case can be written for.
//
// The corpus asks every engine the same question, so a case is only worth
// having where the standard supplies the words to ask it in. A handful of
// grammar rules are written as "!! See the Syntax Rules." rather than as an
// expansion, and a feature that hangs off one of those has no spelling the
// standard owns. Writing a case for it would measure agreement with this
// project instead of agreement with ISO, and an engine that implements the
// feature perfectly under its own syntax would fail.
//
// This is a claim about the standard, not about any engine and not about the
// state of the corpus. It does not move a denominator, it is not a skip, and
// no result is derived from it: what it changes is that the feature stops
// appearing in the list of work nobody has done.
type Unwritable struct {
	// Feature is the ISO optional feature code, one of the 228.
	Feature string `yaml:"feature" json:"feature"`
	// Production is the grammar rule the feature reaches, and the rule whose
	// right-hand side is the see-the-rules marker. It is what makes the entry
	// checkable rather than an opinion.
	Production string `yaml:"production" json:"production"`
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
	// SeeTheRules reports whether the grammar declines to expand this rule.
	SeeTheRules(name string) bool
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
		case !known.SeeTheRules(u.Production):
			return nil, fmt.Errorf("%s: <%s> is not a rule the grammar leaves to the implementer",
				where, u.Production)
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
