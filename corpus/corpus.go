// Package corpus is the test model: what a conformance case is, what it
// claims to cover, and what counts as passing it.
//
// A case is deliberately not a program. It is a fixture name, one statement
// written in standard GQL, and an expectation. The harness never asks an
// engine to run something the standard does not define, and it never asks a
// case to know which engine is running it. Where an engine has a documented
// non-standard spelling of the same thing, that spelling lives in the case's
// Dialects map and is measured separately, so a report can say both "this
// engine does not accept the GQL form" and "this engine can express the
// meaning" without conflating them.
package corpus

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Kind is what part of the standard a case draws on. It decides which
// scoreboard the case lands in, not how it runs.
type Kind string

const (
	// KindMandatory covers a mandatory subclause of ISO/IEC 39075. Failing
	// one of these means an implementation cannot claim GQL conformance at
	// all, because conformance to the mandatory features is not optional.
	KindMandatory Kind = "mandatory"
	// KindOptional covers one of the 228 optional features in features.xml.
	// Failing one costs that feature and nothing else.
	KindOptional Kind = "optional"
	// KindCondition covers a GQLSTATUS class or subclass from conditions.xml:
	// the engine must not merely reject bad input, it must reject it with the
	// code the standard names.
	KindCondition Kind = "condition"
	// KindGrammar covers a production from the BNF artifact, usually as an
	// accept-or-reject pair. These are the cheapest cases to run and the ones
	// that separate a parser from a language.
	KindGrammar Kind = "grammar"
	// KindPerformance carries no pass criterion beyond completing. It exists
	// so the metrics tables have workloads with known shapes in them.
	KindPerformance Kind = "performance"
	// KindGenerated is a statement a walk of the BNF produced, and it cites
	// nothing. Every other kind names a clause, a feature, a code or a
	// production the standard defines, and a person decided the case was worth
	// writing. A generated statement has neither: the walk knows it is well
	// formed and knows nothing about what it means.
	//
	// So it is not in AllKinds, it is not in a scoreboard the other five share,
	// and it is not in any pass rate. It is a lead. What it produces goes to a
	// person, who either writes a case that cites a clause or does not, and the
	// corpus stays the record either way. Load refuses this kind for the same
	// reason: a generated case on disk would be a claim nobody checked.
	KindGenerated Kind = "generated"
)

// AllKinds lists the kinds a corpus file may declare, in report order.
// KindGenerated is deliberately absent; see its documentation.
var AllKinds = []Kind{KindMandatory, KindOptional, KindCondition, KindGrammar, KindPerformance}

// LargeTag marks a case whose fixture is big enough to measure storage density
// on and too big to run every time. A store has to outweigh the engine's own
// preallocation by an order of magnitude before bits/edge describes an encoding
// rather than a floor, and a fixture that large costs minutes to ingest.
//
// Cases carrying it are excluded unless a run asks for them, which is what
// keeps the default run short enough for CI and keeps the density figures
// reachable at all. It is the only tag the harness gives a meaning to.
const LargeTag = "large"

// ExpectKind is how a case's outcome is judged.
type ExpectKind string

const (
	// ExpectRows compares the returned table against Rows.
	ExpectRows ExpectKind = "rows"
	// ExpectEmpty requires a successful result with no rows.
	ExpectEmpty ExpectKind = "empty"
	// ExpectError requires failure, optionally with a specific GQLSTATUS.
	ExpectError ExpectKind = "error"
	// ExpectAccept requires only that the statement was accepted, which is
	// what a grammar case about syntax alone needs.
	ExpectAccept ExpectKind = "accept"
	// ExpectReject requires only that the statement was refused. A parser
	// that accepts what the grammar forbids is not implementing the grammar.
	ExpectReject ExpectKind = "reject"
)

// Expect is the criterion applied to a statement's outcome.
type Expect struct {
	Kind ExpectKind `yaml:"kind" json:"kind"`

	// Columns, when set, must equal the result's column names in order.
	Columns []string `yaml:"columns" json:"columns,omitempty"`
	// Rows is the expected table. Values are compared after normalisation,
	// so an engine returning 1 where another returns 1.0 is not marked wrong
	// for a difference the standard does not require.
	Rows [][]any `yaml:"rows" json:"rows,omitempty"`
	// Unordered compares Rows as a multiset. Set it unless the statement has
	// an ORDER BY; GQL does not promise an order without one, and a harness
	// that demands one is testing an accident.
	Unordered bool `yaml:"unordered" json:"unordered,omitempty"`

	// GQLStatus is the five-character code from conditions.xml the engine
	// should report. Empty means any failure satisfies an ExpectError case.
	GQLStatus string `yaml:"gqlstatus" json:"gqlstatus,omitempty"`
	// AlsoGQLStatus lists the other codes ISO permits for the same statement.
	// It is not a way to soften a case. It exists because a few violations are
	// catchable at two different moments and the standard gives each moment its
	// own code: a node pattern naming more labels than the implementation
	// allows is 42010 when the engine reads the statement and 22G0P when it
	// builds the node, and nothing in the statement decides which. A case that
	// insisted on one would be scoring engines on when they look rather than on
	// what they found. Where ISO does name a single code, this stays empty.
	AlsoGQLStatus []string `yaml:"also_gqlstatus" json:"also_gqlstatus,omitempty"`
	// ErrorContains, for engines that report no GQLSTATUS, is a substring the
	// message should hold. It is weaker evidence and is reported as such.
	ErrorContains string `yaml:"error_contains" json:"error_contains,omitempty"`

	// Diagnostic, when set, is what the record beside the status has to
	// carry. It is how GA08 is tested: the feature is not the code, which
	// GQLStatus already grades, but the record the standard attaches to it.
	Diagnostic *ExpectDiagnostic `yaml:"diagnostic" json:"diagnostic,omitempty"`
}

// ExpectDiagnostic is the assertion on a diagnostic record, in the fields ISO
// subclause 23.2 names.
//
// Only the fields set are checked, and each is checked for equality rather
// than for presence, because a record with a subject that names the wrong
// thing is worse than one with no subject: the first sends a client to
// underline a token the statement got right.
//
// There is deliberately no way to assert an excerpt or a message here. Those
// are the engine's own prose and no two engines write the same sentence, so an
// assertion on them would be scoring the wording rather than the record.
type ExpectDiagnostic struct {
	// Subject is the name the condition is about, spelled the way the query
	// spelled it.
	Subject string `yaml:"subject" json:"subject,omitempty"`
	// SubjectKind is what sort of thing that name is: graph, schema, label,
	// property, variable, type or function.
	SubjectKind string `yaml:"subject_kind" json:"subject_kind,omitempty"`
	// Schema is the schema the statement ran in, which for a case run against
	// a fresh session is the root the standard opens in.
	Schema string `yaml:"schema" json:"schema,omitempty"`
	// Position requires that the record point at some token. The line and the
	// column themselves are not asserted: two engines that both point at the
	// right word disagree about which character it starts at as soon as one of
	// them counts a keyword the other folded.
	Position bool `yaml:"position" json:"position,omitempty"`
}

// SubjectKinds is what an ExpectDiagnostic may ask a subject to be, which is
// the list ISO 39075 subclause 23.2 draws its subject fields from.
var SubjectKinds = []string{"graph", "schema", "label", "property", "variable", "type", "function"}

// Scale is how a limit case is sized from the engine's declaration.
//
// The query holds the placeholder "<<scale>>" once, and it is replaced by Each
// repeated as many times as it takes to pass the declared maximum, joined by
// Between, with "<<n>>" inside Each standing for the one-based number of the
// repetition. Nothing in GQL is spelled with a doubled angle bracket, so the
// placeholders cannot collide with the statement around them.
//
// A statement built this way is large, tens of thousands of characters against
// an engine with a generous limit, which is exactly why the corpus writes the
// template and not the statement. It also means the report records the
// template and the count rather than the text, since the two reproduce it
// exactly and the text would be most of the report.
type Scale struct {
	// Kind is which kind of graph element the limit belongs to, for the
	// implementation-defined items ISO writes "for each kind of graph
	// element". Empty for an item that has one value.
	Kind string `yaml:"kind" json:"kind,omitempty"`
	// Each is the text of one repetition, with "<<n>>" for its number.
	Each string `yaml:"each" json:"each"`
	// Between joins two repetitions. Empty is allowed: a repeated label is
	// written straight onto the one before it.
	Between string `yaml:"between" json:"between,omitempty"`
	// Over is how far past the declared maximum the statement goes. It
	// defaults to one, which is the number that tests the limit rather than
	// something comfortably beyond it, and a case only writes it out where an
	// engine needs more than one unit to notice.
	Over int `yaml:"over" json:"over,omitempty"`
}

// Key is the name an adapter declares this case's limit under: the
// implementation-defined item, and the kind of element where ISO gives the
// item a value per kind.
func (s *Scale) Key(item string) string {
	if s.Kind == "" {
		return item
	}
	return item + "/" + s.Kind
}

// Units is how many repetitions a declared maximum of max calls for.
func (s *Scale) Units(max int) int {
	over := s.Over
	if over < 1 {
		over = 1
	}
	return max + over
}

// Expand builds the replacement text for a declared maximum of max.
func (s *Scale) Expand(max int) string {
	var b strings.Builder
	for i := 1; i <= s.Units(max); i++ {
		if i > 1 {
			b.WriteString(s.Between)
		}
		b.WriteString(strings.ReplaceAll(s.Each, "<<n>>", strconv.Itoa(i)))
	}
	return b.String()
}

// Placeholder is what a scaled query holds where the repetitions go.
const Placeholder = "<<scale>>"

// Case is one conformance test.
type Case struct {
	// ID is stable and hierarchical: kind/family/feature/name. Reports,
	// baselines, and skip lists all key on it, so it must never be reused for
	// a different test.
	ID string `yaml:"id" json:"id"`
	// Name is a sentence describing what the case establishes.
	Name string `yaml:"name" json:"name"`
	Kind Kind   `yaml:"kind" json:"kind"`

	// Features are ISO optional feature codes from features.xml.
	Features []string `yaml:"features" json:"features,omitempty"`
	// Subclauses are mandatory subclause numbers, e.g. "14.4". ISO assigns
	// mandatory features no code, so the subclause is the only handle.
	Subclauses []string `yaml:"subclauses" json:"subclauses,omitempty"`
	// Productions are BNF production names from the grammar artifact.
	Productions []string `yaml:"productions" json:"productions,omitempty"`
	// Conditions are GQLSTATUS codes from conditions.xml.
	Conditions []string `yaml:"conditions" json:"conditions,omitempty"`
	// Requires are optional feature codes the case's syntax needs but does not
	// test. A condition case often has to reach its error through an optional
	// construct — there is no mandatory way to write a negative LIMIT — and
	// against an engine that documents the feature as absent the case would
	// report a syntax error where the corpus meant to report a status code.
	// Naming the dependency here lets such an engine be skipped, and skipping
	// is visible in the report where a mis-attributed failure would not be.
	Requires []string `yaml:"requires" json:"requires,omitempty"`

	// Fixture names the graph. Empty means the case needs no data, which is
	// true of most grammar cases.
	Fixture string `yaml:"fixture" json:"fixture,omitempty"`
	// Setup runs before Query, in order, and its results are discarded. Use
	// it for the write statements a read case depends on, never for data a
	// fixture could carry.
	Setup []string `yaml:"setup" json:"setup,omitempty"`
	// Query is the statement under test, in standard GQL.
	Query string `yaml:"query" json:"query"`
	// Parses is a control statement for a condition case: the same syntax the
	// query uses, in a form the engine should accept. It is run only when the
	// query failed on the code the engine named, and it separates two failures
	// a single code mismatch cannot tell apart. An engine that never parsed the
	// statement reports a syntax error rather than the specified condition, and
	// reading that as a diagnostic miss blames the wrong part of the engine. If
	// the control is refused too, the engine cannot parse the shape at all and
	// the condition was never reachable; if it is accepted, the shape is fine
	// and the wrong code is exactly what it looks like.
	Parses string `yaml:"parses" json:"parses,omitempty"`
	// Limit names the ISO 24.5.2 implementation-defined item whose value
	// decides whether this case's condition can be raised at all.
	//
	// Sixteen of ISO's sixty-eight GQLSTATUS codes are limit conditions: a node
	// carrying more labels than the implementation supports, a record with more
	// fields, a string longer than the type admits. ISO fixes the code and
	// leaves the threshold to the implementation, which means a case asking for
	// sixty-four labels is asking a question with two correct answers. An engine
	// whose maximum is thirty-two must raise the code; an engine with no maximum
	// must accept the statement, and failing it for that would be scoring it
	// against a number the standard never set.
	//
	// So a case with a limit set is not failed for a statement the engine took.
	// It is skipped, and the skip records that the engine's limit is at least
	// what the case asked for, which is a measurement of the item and the only
	// one available. The code is still asserted when the engine does refuse.
	// Where the item is a number, Scale sizes the statement from it instead of
	// guessing, and the skip is reached only by an engine that declares none.
	Limit string `yaml:"limit" json:"limit,omitempty"`
	// Unless names an optional feature whose presence makes this case's
	// condition unreachable.
	//
	// Some conditions are not thresholds but absences. ISO says an engine that
	// does not support two graphs in one transaction shall raise 25G04, which
	// is a requirement on engines without feature GT03 and says nothing at all
	// about engines with it. The same shape covers 22G04 against GA04, 22G13
	// against GQ17, and 25G02 against GP18: in each of them the engine can only
	// raise the code by not having the feature.
	//
	// It is written apart from Limit because the two are skipped for different
	// reasons and a reader who cannot tell them apart draws the wrong
	// conclusion. A limit skip says nothing was learned about the code and the
	// number is the engine's to choose. An unless skip says the code is not
	// merely untested but unreachable on this engine as built, and the report
	// should not go on suggesting that a threshold somewhere would reach it.
	Unless string `yaml:"unless" json:"unless,omitempty"`
	// Scale builds the statement at the size the engine's own declared limit
	// makes it, instead of at a size the corpus guessed.
	//
	// A limit case without this is a case that can only ever be skipped once
	// the engine's maximum is above whatever number the case wrote down. That
	// is the whole reason zu skipped four property limit codes for months: the
	// case asked for sixty-four properties, zu holds four thousand and ninety
	// six, and the run recorded that the condition was not reachable when what
	// it had actually measured was that the guess was low.
	//
	// So the number comes from the engine. An adapter that declares a value
	// for this case's Limit gets a statement one unit past it and is expected
	// to raise the code; an adapter that declares none is skipped as before,
	// because an engine with no maximum genuinely cannot raise the condition
	// and failing it would be scoring it against a number the standard never
	// set.
	Scale *Scale `yaml:"scale" json:"scale,omitempty"`
	// Unprovokable says, in prose, why no statement a client can send raises
	// this case's condition, and takes the case out of every run.
	//
	// Two of ISO's sixty-eight codes are about what the client does not know.
	// 08007 is the connection dying while a transaction is being resolved, and
	// 40003 is a statement whose completion is unknown after a rollback. Both
	// are raised by the loss of the channel the answer would have come back on,
	// so provoking one means killing the engine or the socket at a chosen
	// instant, and observing it means trusting whatever the driver reports
	// about a connection that is gone.
	//
	// The case is still written, still names its code, and still counts toward
	// the corpus's coverage of the condition surface, because the alternative is
	// a corpus that is silent about two codes and a reader who cannot tell
	// silence from an oversight. What it never does is produce a verdict: the
	// runner skips it before the engine is touched, and the skip carries this
	// text. A code nobody can raise from a client is a fact about the code, and
	// the honest report of it is a skip that says so.
	Unprovokable string `yaml:"unprovokable" json:"unprovokable,omitempty"`
	// Params binds named parameters, for the cases about parameters. A value
	// is whatever YAML wrote, and the adapter hands it to the engine in the
	// engine's own encoding.
	//
	// Two values have no YAML shape, and the standard has features about both:
	// a graph parameter is GE04 and a binding table parameter is GE05. So a
	// value written as a map holding one key that begins with a dollar sign is
	// a reference rather than a record. "$graph" takes a graph reference the
	// way a statement writes one, which is a path or one of the words that
	// name a graph without naming it, and "$table" takes an object with a
	// columns array and a rows array of arrays. An engine whose wire cannot
	// carry a reference fails the case, which is the answer those two features
	// are asking for, and no engine has to be taught the convention to be
	// measured by it.
	Params map[string]any `yaml:"params" json:"params,omitempty"`

	Expect Expect `yaml:"expect" json:"expect"`

	// Dialects holds an engine's own documented spelling of the same meaning,
	// keyed by adapter name. It is never used to judge conformance. It is run
	// in compatibility mode so a report can distinguish "cannot do this" from
	// "does this, differently".
	Dialects map[string]string `yaml:"dialects" json:"dialects,omitempty"`

	// Tags are free-form selectors for -run filters: "read", "write",
	// "recursive", "temporal", and so on. LargeTag is the one tag the runner
	// itself acts on.
	Tags []string `yaml:"tags" json:"tags,omitempty"`

	// Repeat overrides the run-wide repetition count for cases whose timing
	// is the point, or whose cost makes the default wasteful.
	Repeat int `yaml:"repeat" json:"repeat,omitempty"`
	// Mutating marks a case whose statements change the graph, so the runner
	// reloads the fixture before it instead of sharing a loaded one.
	Mutating bool `yaml:"mutating" json:"mutating,omitempty"`
	// Restore asks the runner to put the fixture back between the timed
	// repetitions as well as before the first, which is the only way a
	// non-idempotent statement gets a distribution instead of one cold sample:
	// every execution is then the first application to the same graph.
	//
	// Unset means yes, for a mutating case that has a fixture. Set it to false
	// where the successive applications are the measurement, as in a write case
	// whose point is how the cost moves as the graph grows.
	Restore *bool `yaml:"restore" json:"restore,omitempty"`
	// TimeoutMS overrides the run-wide per-statement timeout.
	TimeoutMS int `yaml:"timeout_ms" json:"timeout_ms,omitempty"`

	// Source records the file the case was read from, for error messages.
	Source string `yaml:"-" json:"source,omitempty"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(?:[-/][a-z0-9.]+)*$`)

// Validate rejects a case the runner could not interpret unambiguously. It is
// strict on purpose: a case that claims a feature code the standard does not
// define would inflate a conformance score with a test of nothing.
func (c *Case) Validate(known KnownCodes) error {
	where := c.ID
	if where == "" {
		where = "case in " + c.Source
	}
	if c.ID == "" {
		return fmt.Errorf("%s: missing id", where)
	}
	if !idPattern.MatchString(c.ID) {
		return fmt.Errorf("%s: id must be lowercase slash-separated", where)
	}
	if c.Name == "" {
		return fmt.Errorf("%s: missing name", where)
	}
	if c.Query == "" {
		return fmt.Errorf("%s: missing query", where)
	}
	switch c.Kind {
	case KindMandatory, KindOptional, KindCondition, KindGrammar, KindPerformance, KindGenerated:
	default:
		return fmt.Errorf("%s: unknown kind %q", where, c.Kind)
	}
	switch c.Expect.Kind {
	case ExpectRows:
		if len(c.Expect.Columns) == 0 {
			return fmt.Errorf("%s: rows expectation needs columns", where)
		}
		for i, r := range c.Expect.Rows {
			if len(r) != len(c.Expect.Columns) {
				return fmt.Errorf("%s: expected row %d has %d values, %d columns declared",
					where, i, len(r), len(c.Expect.Columns))
			}
		}
	case ExpectEmpty, ExpectAccept, ExpectReject:
	case ExpectError:
		if c.Expect.GQLStatus != "" && !known.Status(c.Expect.GQLStatus) {
			return fmt.Errorf("%s: GQLSTATUS %q is not defined in conditions.xml",
				where, c.Expect.GQLStatus)
		}
	default:
		return fmt.Errorf("%s: unknown expect kind %q", where, c.Expect.Kind)
	}

	// A case must claim at least one thing from the standard, or it is not a
	// conformance case and belongs in the performance kind.
	claims := len(c.Features) + len(c.Subclauses) + len(c.Productions) + len(c.Conditions)
	if claims == 0 && c.Kind != KindPerformance {
		return fmt.Errorf("%s: claims no feature, subclause, production, or condition", where)
	}
	for _, f := range c.Features {
		if !known.Feature(f) {
			return fmt.Errorf("%s: %q is not an ISO feature code", where, f)
		}
	}
	for _, f := range c.Requires {
		if !known.Feature(f) {
			return fmt.Errorf("%s: required %q is not an ISO feature code", where, f)
		}
	}
	for _, p := range c.Productions {
		if !known.Production(p) {
			return fmt.Errorf("%s: <%s> is not a production in the ISO grammar", where, p)
		}
	}
	for _, s := range c.Conditions {
		if !known.Status(s) {
			return fmt.Errorf("%s: GQLSTATUS %q is not defined in conditions.xml", where, s)
		}
	}
	for _, s := range c.Subclauses {
		if !known.Subclause(s) {
			return fmt.Errorf("%s: %q is not a subclause of ISO/IEC 39075 that specifies behaviour", where, s)
		}
	}
	if c.Kind == KindMandatory && len(c.Subclauses) == 0 {
		return fmt.Errorf("%s: a mandatory case must name the subclause it covers", where)
	}
	if c.Kind == KindOptional && len(c.Features) == 0 {
		return fmt.Errorf("%s: an optional case must name the feature it covers", where)
	}
	if c.Restore != nil {
		if !c.Mutating {
			return fmt.Errorf("%s: restore is for a mutating case, and this one changes nothing", where)
		}
		if *c.Restore && c.Fixture == "" {
			return fmt.Errorf("%s: restore needs a fixture to restore", where)
		}
	}
	// A control statement only means something where a code mismatch is the
	// failure it disambiguates. On any other case it would run a second
	// statement nobody could read a result from.
	if c.Parses != "" && (c.Kind != KindCondition || c.Expect.Kind != ExpectError) {
		return fmt.Errorf("%s: parses is for a condition case expecting an error, and this is %s expecting %s",
			where, c.Kind, c.Expect.Kind)
	}
	if c.Limit != "" {
		if c.Expect.Kind != ExpectError {
			return fmt.Errorf("%s: limit excuses an engine that accepted the statement, and this case expects %s",
				where, c.Expect.Kind)
		}
		// The item is checked against the implementation-defined list rather
		// than the wider behaviour catalogue, because an excuse is only owed
		// where 24.5.2 obliges the implementer to have written the threshold
		// down. A threshold the standard left implementation-dependent is one
		// nobody has to state, and a case cannot be waived on it.
		if !known.Defined(c.Limit) {
			return fmt.Errorf("%s: %q is not an implementation-defined item of ISO/IEC 39075 24.5.2", where, c.Limit)
		}
	}
	if c.Unless != "" {
		if c.Expect.Kind != ExpectError {
			return fmt.Errorf("%s: unless excuses an engine that accepted the statement, and this case expects %s",
				where, c.Expect.Kind)
		}
		if !known.Feature(c.Unless) {
			return fmt.Errorf("%s: %q is not a feature of ISO/IEC 39075, and only a feature can make a condition unreachable by being present",
				where, c.Unless)
		}
		// Requiring a feature and being excused by it are opposite claims, and
		// a case making both would be skipped whichever way the engine
		// answered.
		if slices.Contains(c.Requires, c.Unless) {
			return fmt.Errorf("%s: %q is both required and excusing, so no engine could ever be judged on this case",
				where, c.Unless)
		}
		if c.Limit != "" {
			return fmt.Errorf("%s: the case names both a limit and an excusing feature, and a reader cannot be told which one the skip measured",
				where)
		}
	}
	if s := c.Scale; s != nil {
		// The size comes from the engine's declaration of this case's limit,
		// so a case that names no limit has nothing to scale from.
		if c.Limit == "" {
			return fmt.Errorf("%s: scale sizes a statement from a declared limit, and this case names none", where)
		}
		if !known.Defined(c.Limit) {
			return fmt.Errorf("%s: scale sizes a statement from %q, which is not an implementation-defined item of ISO 24.5.2 and so is not a number any engine declares",
				where, c.Limit)
		}
		if !strings.Contains(c.Query, Placeholder) {
			return fmt.Errorf("%s: the query has no %s for the repetitions to go in", where, Placeholder)
		}
		if s.Each == "" {
			return fmt.Errorf("%s: scale repeats nothing", where)
		}
		// Without the counter every repetition is the same text, which for a
		// property set or a label set is one item written many times rather
		// than many items.
		if !strings.Contains(s.Each, "<<n>>") {
			return fmt.Errorf("%s: scale repeats %q, which has no <<n>> and so writes one thing over and over", where, s.Each)
		}
		if s.Kind != "" && s.Kind != "node" && s.Kind != "edge" {
			return fmt.Errorf("%s: %q is not a kind of graph element", where, s.Kind)
		}
		if s.Over < 0 {
			return fmt.Errorf("%s: scale of %d units past the maximum is under it", where, s.Over)
		}
	}
	if c.Unprovokable != "" && (c.Kind != KindCondition || c.Expect.Kind != ExpectError) {
		return fmt.Errorf("%s: unprovokable withdraws a condition case from the run, and this is %s expecting %s",
			where, c.Kind, c.Expect.Kind)
	}
	if d := c.Expect.Diagnostic; d != nil {
		if c.Expect.Kind != ExpectError {
			return fmt.Errorf("%s: a diagnostic record belongs to a condition, and this case expects %s",
				where, c.Expect.Kind)
		}
		if (d.Subject == "") != (d.SubjectKind == "") {
			return fmt.Errorf("%s: a subject and its kind are asserted together or not at all", where)
		}
		if d.SubjectKind != "" && !slices.Contains(SubjectKinds, d.SubjectKind) {
			return fmt.Errorf("%s: %q is not one of the subject kinds %s names: %s",
				where, d.SubjectKind, "ISO 39075 subclause 23.2", strings.Join(SubjectKinds, ", "))
		}
		if *d == (ExpectDiagnostic{}) {
			return fmt.Errorf("%s: the diagnostic assertion asks for nothing", where)
		}
	}
	for _, s := range c.Expect.AlsoGQLStatus {
		if c.Expect.Kind != ExpectError || c.Expect.GQLStatus == "" {
			return fmt.Errorf("%s: also_gqlstatus is a second answer to a case that specifies a first one", where)
		}
		if !known.Status(s) {
			return fmt.Errorf("%s: GQLSTATUS %q is not defined in conditions.xml", where, s)
		}
		if s == c.Expect.GQLStatus {
			return fmt.Errorf("%s: %q is listed as an alternative to itself", where, s)
		}
	}
	return nil
}

// KnownCodes is the slice of the ISO catalogue validation needs. It is an
// interface so corpus does not import iso, which keeps the case model usable
// in tests that build a catalogue by hand.
type KnownCodes interface {
	Feature(code string) bool
	Production(name string) bool
	Status(code string) bool
	// Subclause reports whether the number names a clause of the standard that
	// specifies behaviour. Front matter and the conformance clause itself are
	// not such clauses, and a case citing one is mis-filed.
	Subclause(number string) bool
	// Defined reports whether the code names an item on ISO's
	// implementation-defined list, which is what a case's `limit` may cite.
	Defined(code string) bool
}

// Suite is a validated, ordered set of cases.
type Suite struct {
	Cases []*Case
	byID  map[string]*Case
}

// NewSuite validates and indexes cases, rejecting duplicate ids.
func NewSuite(cases []*Case, known KnownCodes) (*Suite, error) {
	s := &Suite{byID: make(map[string]*Case, len(cases))}
	for _, c := range cases {
		if err := c.Validate(known); err != nil {
			return nil, err
		}
		if prev, dup := s.byID[c.ID]; dup {
			return nil, fmt.Errorf("duplicate case id %q (%s and %s)", c.ID, prev.Source, c.Source)
		}
		s.byID[c.ID] = c
		s.Cases = append(s.Cases, c)
	}
	sort.Slice(s.Cases, func(i, j int) bool { return s.Cases[i].ID < s.Cases[j].ID })
	return s, nil
}

// Get returns the case with an id.
func (s *Suite) Get(id string) (*Case, bool) {
	c, ok := s.byID[id]
	return c, ok
}

// Len reports the case count.
func (s *Suite) Len() int { return len(s.Cases) }

// Filter returns the cases matching every constraint given. A zero Selector
// selects everything.
func (s *Suite) Filter(sel Selector) []*Case {
	var out []*Case
	for _, c := range s.Cases {
		if sel.matches(c) {
			out = append(out, c)
		}
	}
	return out
}

// Selector narrows a run. Each field is ANDed; within a field, any match
// counts.
type Selector struct {
	// IDPattern is a regular expression against the case id.
	IDPattern *regexp.Regexp
	// Kinds limits to these kinds.
	Kinds []Kind
	// Features limits to cases claiming one of these ISO feature codes.
	Features []string
	// Tags limits to cases carrying one of these tags.
	Tags []string
	// SkipTags excludes cases carrying any of these tags.
	SkipTags []string
}

func (sel Selector) matches(c *Case) bool {
	if sel.IDPattern != nil && !sel.IDPattern.MatchString(c.ID) {
		return false
	}
	if len(sel.Kinds) > 0 && !slices.Contains(sel.Kinds, c.Kind) {
		return false
	}
	if len(sel.Features) > 0 && !overlaps(sel.Features, c.Features) {
		return false
	}
	if len(sel.Tags) > 0 && !overlaps(sel.Tags, c.Tags) {
		return false
	}
	if len(sel.SkipTags) > 0 && overlaps(sel.SkipTags, c.Tags) {
		return false
	}
	return true
}

func overlaps(want, have []string) bool {
	for _, w := range want {
		for _, h := range have {
			if strings.EqualFold(w, h) {
				return true
			}
		}
	}
	return false
}

// CoveredFeatures returns every ISO feature code the suite claims, sorted.
// The gap between this and the 228 in features.xml is the corpus's own
// coverage debt, and the report prints it rather than hiding it.
func (s *Suite) CoveredFeatures() []string {
	seen := map[string]bool{}
	for _, c := range s.Cases {
		for _, f := range c.Features {
			seen[f] = true
		}
	}
	return sortedKeys(seen)
}

// CoveredSubclauses returns every mandatory subclause the suite claims.
func (s *Suite) CoveredSubclauses() []string {
	seen := map[string]bool{}
	for _, c := range s.Cases {
		for _, x := range c.Subclauses {
			seen[x] = true
		}
	}
	return sortedKeys(seen)
}

// CoveredConditions returns every GQLSTATUS the suite asserts on.
func (s *Suite) CoveredConditions() []string {
	seen := map[string]bool{}
	for _, c := range s.Cases {
		for _, x := range c.Conditions {
			seen[x] = true
		}
		if c.Expect.GQLStatus != "" {
			seen[c.Expect.GQLStatus] = true
		}
	}
	return sortedKeys(seen)
}

// CoveredProductions returns every grammar production the suite claims.
func (s *Suite) CoveredProductions() []string {
	seen := map[string]bool{}
	for _, c := range s.Cases {
		for _, x := range c.Productions {
			seen[x] = true
		}
	}
	return sortedKeys(seen)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
