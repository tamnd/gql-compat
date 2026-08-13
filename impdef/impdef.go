// Package impdef observes the choices ISO/IEC 39075 leaves to the engine.
//
// The standard does not specify everything. It delegates 117 items to the
// implementation and 20 more to the running system, and the difference between
// the two matters: an implementation-defined item is one the implementer must
// write down (Clause 24.5.2 requires the conformance statement to state them),
// while an implementation-dependent item is one nobody has to document and no
// program may rely on. Clause 24.5.3 then permits extensions, syntax the
// standard does not define, on the same condition: say what they are.
//
// Everything this package produces is an observation and nothing is a verdict.
// An engine that pads character strings for comparison and an engine that does
// not are both conforming; the standard asked a question and each answered it.
// So a probe has no expectation, an observation has no outcome, and the section
// this package renders is excluded from every total, every coverage
// denominator, and the process exit status. The only failure available here is
// silence: a probe that could not be put to the engine renders an em dash,
// under the same rule the metrics tables use, because not observed is not the
// same as none and not the same as unlimited.
//
// No case in the corpus may test either list, and that rule does not change.
// This is reporting. What it is for is the paste-ready artifact in
// WriteStatement: a vendor writing a 24.5.2 conformance statement gets the item
// numbers, the standard's own words for each, and whichever answers this
// harness could observe on their engine.
package impdef

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/rows"
)

// Kind says which of the standard's three delegations a probe answers.
type Kind string

const (
	// Defined is an implementation-defined item: ISO leaves the choice to the
	// implementer and 24.5.2 obliges them to document it.
	Defined Kind = "implementation-defined"
	// Dependent is an implementation-dependent item: ISO leaves the choice to
	// the running system and requires nothing to be written down. An
	// observation of one is a fact about one run on one machine and a program
	// that depends on it is not portable.
	Dependent Kind = "implementation-dependent"
	// Extension is syntax or behaviour the standard does not define at all,
	// permitted by 24.5.3 provided the implementation documents it. An
	// extension probe still cites the item it answers, because "the treatment
	// of language that does not conform to the Formats and Syntax Rules" is
	// itself implementation-defined, IE005.
	Extension Kind = "extension"
)

// AllKinds is the three delegations in the order the report prints them.
var AllKinds = []Kind{Defined, Dependent, Extension}

// Read says what part of the engine's answer is the observation.
//
// The distinction between Cell and Answer is the whole difference between a
// question a refusal answers and a question it does not. Asked whether 'a' and
// 'a  ' compare equal, an engine that refuses the statement has not chosen a
// padding rule, so the observation is silence. Asked what it does with an
// integer overflow, an engine that refuses has answered: it refuses.
type Read string

const (
	// Cell is the first cell of the first record. A refusal observes nothing.
	Cell Read = "cell"
	// Answer is the first cell of the first record, or the fact of a refusal,
	// whichever happened. Both are answers to the probe's question.
	Answer Read = "answer"
	// Sequence is the first cell of every record, in the order the engine
	// returned them, joined with commas. It is how an ordering is observed.
	Sequence Read = "sequence"
	// Count is the number of records.
	Count Read = "count"
	// Accepted is whether the engine took the statement at all. The value it
	// returned is not the observation and is not recorded.
	Accepted Read = "accepted"
)

// Silence is why an observation has no value, in a closed set.
//
// It is closed on purpose. The engine's own words for a refusal go to the JSON
// archive and no further, because a report section whose rule is that it uses
// no verdict vocabulary cannot be made to quote an arbitrary parser's opinion
// of a statement in the middle of a table of choices.
type Silence string

const (
	// Refused is a statement the engine would not run, for a probe whose
	// question a refusal does not answer.
	Refused Silence = "the engine refused the statement"
	// Unanswered is a statement the harness cut off.
	Unanswered Silence = "the engine did not answer within the timeout"
	// Broken is the transport between harness and engine giving way, which
	// says nothing about the engine's choices.
	Broken Silence = "the connection to the engine gave way"
	// Undeclared is the engine declaring in advance that it cannot do this.
	Undeclared Silence = "the engine declares it cannot do this"
	// NoFixture is a probe needing a graph the engine cannot represent.
	NoFixture Silence = "the engine cannot hold the graph the probe needs"
	// NoLoad is that graph failing to load.
	NoLoad Silence = "the graph the probe needs did not load"
	// NoSession is no live connection to ask.
	NoSession Silence = "no session was open to ask"
	// NoRecords is a statement that ran and returned nothing to read.
	NoRecords Silence = "the statement returned no records"
	// NotProbed is an item this harness has no probe for. It is the silence a
	// conformance statement template carries for the items a vendor still has
	// to fill in themselves.
	NotProbed Silence = "this harness has no probe for the item"
)

// Probe is one question put to an engine.
type Probe struct {
	// ID is a stable handle, unique across the set. More than one probe may
	// answer one ISO item, so the item code cannot be the identity.
	ID string `yaml:"id"`
	// Item is the ISO item number this observation answers.
	Item string `yaml:"item"`
	// Kind is which list Item is on, checked against the catalogue at load.
	Kind Kind `yaml:"kind"`
	// Question is the harness's own one-line statement of what is being
	// observed, in the engine's terms rather than the standard's. ISO says
	// "whether to pad character strings for comparison, or not"; the question
	// says which two strings were compared.
	Question string `yaml:"question"`
	// Fixture, when set, is the graph the statement needs.
	Fixture string `yaml:"fixture"`
	// Statement is the GQL put to the engine. It must not modify anything.
	Statement string `yaml:"statement"`
	// Read is which part of the answer is the observation.
	Read Read `yaml:"read"`
	// Note is what a reader needs in order to interpret the value, and for an
	// extension it is where the evidence that the grammar does not define the
	// syntax goes. It is required for an extension and optional otherwise.
	Note string `yaml:"note"`

	// Description is the standard's own words for Item, filled in from the
	// catalogue when the set loads. It is not written in the YAML: a probe
	// that paraphrased the standard would be one more thing to get wrong.
	Description string `yaml:"-"`
}

// mutating names the keywords that write. A probe that ran one would change the
// graph under the case that runs next, and the observation phase deliberately
// carries no reset of its own.
var mutating = regexp.MustCompile(`(?i)\b(INSERT|SET|REMOVE|DELETE|DETACH|CREATE|DROP|ALTER|CALL|COMMIT|ROLLBACK|START|TRUNCATE|USE)\b`)

// quoted matches the string literals and delimited identifiers a keyword scan
// has to ignore, because a probe about string comparison may well contain the
// word SET inside a literal and that is not a write.
var quoted = regexp.MustCompile(`'[^']*'|"[^"]*"|` + "`[^`]*`")

var validReads = []Read{Cell, Answer, Sequence, Count, Accepted}

// Validate checks one probe against the ISO catalogue and the rules above.
func (p *Probe) Validate(known KnownItems) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("probe %q: %s", p.ID, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("a probe has no id")
	}
	if p.Item == "" {
		return bad("no ISO item: an observation nobody can trace to the standard is an anecdote")
	}
	desc, ok := known.Item(p.Item)
	if !ok {
		return bad("item %s is on neither the implementation-defined nor the implementation-dependent list", p.Item)
	}
	p.Description = desc
	switch p.Kind {
	case Defined:
		if !known.Defined(p.Item) {
			return bad("item %s is implementation-dependent, and this probe calls it %s", p.Item, p.Kind)
		}
	case Dependent:
		if known.Defined(p.Item) {
			return bad("item %s is implementation-defined, and this probe calls it %s", p.Item, p.Kind)
		}
	case Extension:
		if !known.Defined(p.Item) {
			return bad("an extension probe must cite an implementation-defined item, and %s is not one", p.Item)
		}
		if strings.TrimSpace(p.Note) == "" {
			return bad("an extension probe needs a note: 24.5.3 permits an extension only if it is documented, and the note is the documentation")
		}
	default:
		return bad("unknown kind %q", p.Kind)
	}
	if strings.TrimSpace(p.Question) == "" {
		return bad("no question: the item's own words are the standard's, and what was actually asked is the harness's to state")
	}
	if strings.TrimSpace(p.Statement) == "" {
		return bad("no statement")
	}
	if !slices.Contains(validReads, p.Read) {
		return bad("unknown read %q", p.Read)
	}
	if kw := mutating.FindString(quoted.ReplaceAllString(p.Statement, "''")); kw != "" {
		return bad("the statement contains %s, and a probe may not write: the observation phase shares its graph with the cases and restores nothing", strings.ToUpper(kw))
	}
	return nil
}

// KnownItems is the slice of the ISO catalogue this package validates against.
// It is an interface for the same reason corpus.KnownCodes is one: the probe
// model stays usable in a test that builds a two-item catalogue by hand.
type KnownItems interface {
	// Item returns the standard's own description of the item.
	Item(code string) (string, bool)
	// Defined reports whether the code is on the implementation-defined list,
	// which is the one 24.5.2 obliges an implementer to document.
	Defined(code string) bool
}

// Set is a validated, ordered collection of probes.
type Set struct {
	Probes []*Probe
	byID   map[string]*Probe
}

// New validates and indexes probes, rejecting duplicate ids.
func New(probes []*Probe, known KnownItems) (*Set, error) {
	s := &Set{byID: make(map[string]*Probe, len(probes))}
	for _, p := range probes {
		if err := p.Validate(known); err != nil {
			return nil, err
		}
		if _, dup := s.byID[p.ID]; dup {
			return nil, fmt.Errorf("duplicate probe id %q", p.ID)
		}
		s.byID[p.ID] = p
		s.Probes = append(s.Probes, p)
	}
	sort.Slice(s.Probes, func(i, j int) bool {
		if s.Probes[i].Item != s.Probes[j].Item {
			return s.Probes[i].Item < s.Probes[j].Item
		}
		return s.Probes[i].ID < s.Probes[j].ID
	})
	return s, nil
}

// Len reports the probe count.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.Probes)
}

// Get returns the probe with an id.
func (s *Set) Get(id string) (*Probe, bool) {
	p, ok := s.byID[id]
	return p, ok
}

// Fixtures returns the fixture names the set needs, so a caller can check them
// against a fixture set the way the corpus loader checks a case's.
func (s *Set) Fixtures() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range s.Probes {
		if p.Fixture == "" || seen[p.Fixture] {
			continue
		}
		seen[p.Fixture] = true
		out = append(out, p.Fixture)
	}
	sort.Strings(out)
	return out
}

// Observation is what one probe saw.
//
// It has no outcome field and never will. The four words a case result can
// carry do not apply to an answer the standard invited the engine to choose,
// and a reader who found one here would reasonably conclude that some of these
// choices are wrong.
type Observation struct {
	ID   string `json:"id"`
	Item string `json:"item"`
	Kind Kind   `json:"kind"`
	// Description is the standard's own words for the item.
	Description string `json:"description"`
	Question    string `json:"question"`
	Statement   string `json:"statement"`
	Fixture     string `json:"fixture,omitempty"`
	Note        string `json:"note,omitempty"`

	// Value is what was observed, empty when nothing was.
	Value string `json:"value,omitempty"`
	// Silence is why nothing was, empty when something was.
	Silence Silence `json:"silence,omitempty"`
	// Detail is the engine's own words about a silence, kept for the archive
	// and deliberately not rendered into the report's prose.
	Detail string `json:"detail,omitempty"`
	// Wall is how long the one execution took. A probe runs once, is not
	// repeated, and is not warmed, so this is a single sample and not a
	// latency: it is here because the run's wall clock has to add up.
	Wall time.Duration `json:"wall_ns"`
}

// Observed reports whether the probe got an answer.
func (o Observation) Observed() bool { return o.Silence == "" }

// Display is the value, or an em dash when there is none. The dash is the same
// one an unavailable metric gets and means the same thing: this was not
// observed, which is neither "none" nor "unlimited".
func (o Observation) Display() string {
	if !o.Observed() {
		return "—"
	}
	if o.Value == "" {
		return "the empty string"
	}
	return o.Value
}

// base is the part of an observation that comes from the probe rather than
// from the engine.
func (p *Probe) base() Observation {
	return Observation{
		ID:          p.ID,
		Item:        p.Item,
		Kind:        p.Kind,
		Description: p.Description,
		Question:    p.Question,
		Statement:   strings.TrimSpace(p.Statement),
		Fixture:     p.Fixture,
		Note:        p.Note,
	}
}

// Silent records that the probe never reached an answer.
func (p *Probe) Silent(why Silence, detail string) Observation {
	o := p.base()
	o.Silence = why
	o.Detail = detail
	return o
}

// Observe turns one execution into an observation.
//
// It is the whole interpretation layer, and it is here rather than in the
// runner so that the runner has no opinion about what an engine's answer means
// and this package has no opinion about sessions.
func (p *Probe) Observe(res *adapter.Result, err error) Observation {
	o := p.base()
	if err != nil {
		f := adapter.AsFailure(err)
		switch {
		case f.Timeout:
			o.Silence = Unanswered
		case f.Transport:
			o.Silence = Broken
		case errors.Is(err, adapter.ErrUnsupported):
			o.Silence = Undeclared
		case p.Read == Accepted || p.Read == Answer:
			// The refusal is the answer.
			o.Value = "refused"
		default:
			o.Silence = Refused
		}
		o.Detail = f.Message
		return o
	}
	if p.Read == Accepted {
		o.Value = "accepted"
		return o
	}
	var t *rows.Table
	if res != nil {
		t = res.Table
	}
	if t == nil || t.Len() == 0 {
		if p.Read == Count {
			o.Value = "0"
			return o
		}
		o.Silence = NoRecords
		return o
	}
	switch p.Read {
	case Count:
		o.Value = strconv.Itoa(t.Len())
	case Sequence:
		parts := make([]string, 0, t.Len())
		for _, r := range t.Rows {
			parts = append(parts, firstCell(r))
		}
		o.Value = strings.Join(parts, ", ")
	default:
		o.Value = firstCell(t.Rows[0])
	}
	return o
}

func firstCell(r []any) string {
	if len(r) == 0 {
		return ""
	}
	return oneLine(rows.Render(r[0]))
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
