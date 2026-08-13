package grammar

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/tamnd/gql-compat/corpus"
)

// Case turns a statement into something the runner can execute.
//
// It is a corpus.Case of the generated kind, which exists so the runner does
// not need a second execution path for this. What the case does not have is
// the thing every other case has: it cites no clause and asserts no outcome
// beyond acceptance. The expectation is `accept`, because the published grammar
// admits the statement, but the judgement the runner applies to a generated
// case is not the ordinary accept judgement and does not live here. See
// runner.judgeGenerated.
//
// Productions carries the derivation path, which is what makes a rejection
// readable. It also satisfies the corpus rule that a case must claim something
// from the standard, and the claim is true: these productions were used.
func Case(s Statement) *corpus.Case {
	name := "a statement the published grammar admits"
	if len(s.Path) > 1 {
		name = "a statement the published grammar admits, through <" + s.Path[len(s.Path)-1] + ">"
	}
	return &corpus.Case{
		ID:          s.ID(),
		Name:        name,
		Kind:        corpus.KindGenerated,
		Productions: s.Path,
		Query:       s.Text,
		Expect:      corpus.Expect{Kind: corpus.ExpectAccept},
		Tags:        []string{"generated"},
		// One execution. A generated statement is not a measurement of
		// anything: repeating it seven times would put a latency distribution
		// on a statement nobody chose, in a section that is about syntax.
		Repeat: 1,
		Source: fmt.Sprintf("grammar walk, seed %d", s.Seed),
	}
}

// Cases turns a walk into cases, dropping duplicates.
//
// The walk repeats itself: the grammar has productions with one alternative and
// no options, and two descents that reach one write the same text. Sending the
// same statement twice would double a lead's weight in a section whose whole
// value is that each line is a different question.
func Cases(statements []Statement) []*corpus.Case {
	seen := map[string]bool{}
	out := make([]*corpus.Case, 0, len(statements))
	for _, s := range statements {
		if s.Text == "" || seen[s.Text] {
			continue
		}
		seen[s.Text] = true
		out = append(out, Case(s))
	}
	return out
}

// Fingerprint identifies a statement by its text, so that a lead can be named
// in a file without pasting a statement into it. The seed and index would be
// the obvious key and are the wrong one: they change when the generator
// changes, and the statement is the thing that was reviewed.
func Fingerprint(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:8])
}

// Promoted is the list of leads that have been dealt with.
//
// A seeded walk is the same walk every time, which is what makes it
// reproducible and also means a lead nobody removed comes back on every run
// forever. Promotion is the removal: a lead that survives review is rewritten
// by hand as a case that cites a clause, and its fingerprint is written here so
// the walk stops reporting it. A lead that review rejects is written here too,
// with the reason, because "we looked at this and it is not a defect" is worth
// recording exactly once rather than rediscovering monthly.
type Promoted struct {
	// Entries are keyed by fingerprint.
	Entries []PromotedEntry `yaml:"promoted" json:"promoted"`
	byPrint map[string]PromotedEntry
}

// PromotedEntry is one lead's disposition.
type PromotedEntry struct {
	// Fingerprint is Fingerprint(statement).
	Fingerprint string `yaml:"fingerprint" json:"fingerprint"`
	// Statement is the reduced text, kept so a reader of this file can see what
	// was decided without running anything.
	Statement string `yaml:"statement" json:"statement"`
	// Case is the hand-written case id the lead became, empty when review
	// decided there was nothing to write.
	Case string `yaml:"case,omitempty" json:"case,omitempty"`
	// Note is why. Required when Case is empty, because a lead dismissed
	// without a reason is a lead nobody can re-examine.
	Note string `yaml:"note,omitempty" json:"note,omitempty"`
	// Engine is the adapter the lead came from, since a syntax error is one
	// engine's opinion and not the language's.
	Engine string `yaml:"engine,omitempty" json:"engine,omitempty"`
}

// LoadPromoted reads the promotion list. A missing file is an empty list and
// not an error: a project that has never promoted a lead is the normal state.
func LoadPromoted(fsys fs.FS, path string) (*Promoted, error) {
	data, err := fs.ReadFile(fsys, path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Promoted{byPrint: map[string]PromotedEntry{}}, nil
	}
	if err != nil {
		// A file that exists and cannot be read is not the same thing. Treating
		// it as empty would silently reopen every lead review has closed.
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ParsePromoted(data, path)
}

// ParsePromoted decodes and checks a promotion list. The path is only used to
// name the file in an error.
func ParsePromoted(data []byte, path string) (*Promoted, error) {
	p := &Promoted{byPrint: map[string]PromotedEntry{}}
	if err := yaml.UnmarshalWithOptions(data, p, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, e := range p.Entries {
		if e.Fingerprint == "" {
			return nil, fmt.Errorf("%s: an entry has no fingerprint", path)
		}
		if e.Case == "" && e.Note == "" {
			return nil, fmt.Errorf("%s: entry %s names no case and gives no reason", path, e.Fingerprint)
		}
		if want := Fingerprint(e.Statement); e.Statement != "" && want != e.Fingerprint {
			return nil, fmt.Errorf("%s: entry %s does not match its statement, whose fingerprint is %s", path, e.Fingerprint, want)
		}
		p.byPrint[e.Fingerprint] = e
	}
	sort.Slice(p.Entries, func(i, j int) bool { return p.Entries[i].Fingerprint < p.Entries[j].Fingerprint })
	return p, nil
}

// Has reports whether a statement has already been dealt with.
func (p *Promoted) Has(text string) bool {
	if p == nil {
		return false
	}
	_, ok := p.byPrint[Fingerprint(text)]
	return ok
}

// Len reports how many leads have been dealt with.
func (p *Promoted) Len() int {
	if p == nil {
		return 0
	}
	return len(p.Entries)
}
