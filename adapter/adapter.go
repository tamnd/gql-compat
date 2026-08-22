// Package adapter is the contract an engine has to satisfy to be measured.
//
// The contract is deliberately thin. An adapter loads a fixture, runs a
// statement, and says what its engine's data model can hold. Everything else
// — repetition, timing, sampling, comparison, scoring — belongs to the
// harness and is identical for every engine, because a comparison in which
// each engine brought its own timing code is not a comparison.
//
// Two rules keep the contract honest. An adapter must never rewrite the
// statement it is given: if an engine cannot parse standard GQL, the right
// outcome is a failure that says so, not a translation that hides it. And an
// adapter must report a capability it lacks rather than approximating it: a
// fixture loaded with its multi-label nodes flattened would produce passes
// for a graph nobody asked about.
package adapter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/rows"
)

// Capabilities is what an adapter tells the harness about its engine, before
// any test runs, so the runner can decide what to skip instead of discovering
// it from failures.
type Capabilities struct {
	// Data is the set of fixture capabilities the engine's storage supports.
	Data map[fixture.Capability]bool

	// GQLStatus reports whether the engine returns the five-character status
	// codes conditions.xml defines. When false, condition cases fall back to
	// matching the error text and are scored as weaker evidence, which the
	// report labels rather than quietly averaging in.
	GQLStatus bool
	// Parameters reports whether the engine accepts named parameters.
	Parameters bool
	// Transactions reports whether explicit START TRANSACTION, COMMIT, and
	// ROLLBACK are available, which GQL feature GT01 requires.
	Transactions bool
	// MultipleStatements reports whether a case's Setup list can run.
	MultipleStatements bool
	// Isolated reports whether Reset returns the engine to a pristine state.
	// An engine without it has every mutating case run against a fresh
	// working directory, which is slower and is measured as such.
	Isolated bool

	// Unsupported lists ISO optional feature codes the engine's own
	// documentation says it does not implement. It is never used to excuse a
	// feature case: those still run, so that a claim of absence is verified
	// rather than believed. It is used only to skip a case whose `requires`
	// names the feature — a case that needs the syntax to reach something
	// else, and against this engine would measure the absence twice.
	Unsupported []string

	// Notes records anything about this adapter a reader of the report needs
	// in order to interpret its numbers: a version, a build flag, a known
	// limitation. They are printed verbatim beside the engine's results.
	Notes []string
}

// Has reports whether the engine supports a data capability.
func (c Capabilities) Has(cap fixture.Capability) bool { return c.Data[cap] }

// Undeclared returns the data capabilities this adapter never mentioned, in
// AllCapabilities order.
//
// Data is a map and a missing key reads as false, so an adapter that forgets
// one is indistinguishable from an adapter that has thought about it and said
// no. The difference matters: the second is a finding about the engine and the
// first is a bug in the adapter, and both come out of the report as the same
// word. It has already happened once. The Neo4j adapter shipped without
// float-values or boolean-values in its map and the run of 2026-08-12 skipped
// four cases and printed "no" twice against an engine that supports both.
//
// The rule here is the one 06 §1 applies to metrics: not measured is not zero,
// and not declared is not "no". A caller that gets a non-empty result should
// refuse to run rather than publish the ambiguity.
func (c Capabilities) Undeclared() []fixture.Capability {
	var out []fixture.Capability
	for _, x := range fixture.AllCapabilities {
		if _, ok := c.Data[x]; !ok {
			out = append(out, x)
		}
	}
	return out
}

// DataList returns the supported data capabilities in AllCapabilities order.
func (c Capabilities) DataList() []fixture.Capability {
	var out []fixture.Capability
	for _, x := range fixture.AllCapabilities {
		if c.Data[x] {
			out = append(out, x)
		}
	}
	return out
}

// Result is one statement's outcome, in the harness's own vocabulary.
type Result struct {
	// Table is the rows returned. A statement that returns no table at all —
	// a write, a FINISH — leaves it nil, which is distinct from a table with
	// no rows.
	Table *rows.Table
	// Bytes is how much the result weighed on the wire, where the adapter can
	// know that. Zero means unknown.
	Bytes int64
	// GQLStatus is the code the engine reported for a successful outcome,
	// which for GQL is 00000 on a normal completion and 02000 on no data.
	GQLStatus string
	// Plan is the engine's own rendering of how it ran the statement, for an
	// adapter whose engine hands one back on the ordinary path at no extra
	// cost. Almost none do, and an adapter must not buy one here: see
	// Explainer, which is where the runner looks first and where every
	// adapter in this repository answers from.
	Plan string
}

// Failure is a statement that the engine refused or could not complete. It is
// an ordinary outcome, not an exception: a large part of conformance is
// failing correctly.
type Failure struct {
	// GQLStatus is the five-character code, empty for an engine that does
	// not report one.
	GQLStatus string
	// Message is the engine's own text, kept verbatim for the report.
	Message string
	// Timeout marks a statement the harness cut off rather than one the
	// engine rejected. It never counts as a correct rejection.
	Timeout bool
	// Fatal marks a failure that killed the session, so the runner rebuilds
	// it before the next case instead of running the rest of the suite
	// against a dead process.
	Fatal bool
	// Transport marks a failure of the harness's own plumbing rather than of
	// the engine. A shell that computes the right answer and then cannot
	// print it is not an engine that got the query wrong, and a driver that
	// loses the connection is not an engine that refused the statement.
	//
	// The runner treats it the way it treats a timeout: no answer was
	// obtained, so the case is an error and stays out of the pass rate
	// instead of being charged to the engine. An adapter should set it only
	// when it can tell the difference, and should say in Message what broke,
	// because a reader who cannot see the plumbing has only that sentence.
	Transport bool

	// Diagnostic is the record beside the status, for an engine that
	// produces one. Nil means the engine reported no record, which is a
	// finding about GA08 and not a gap in the harness.
	Diagnostic *Diagnostic
}

// Diagnostic is the record ISO/IEC 39075:2024 subclause 23.2 attaches to a
// GQL-status object, in the fields the standard names.
//
// It is separate from the code because the code says which condition was
// raised and the record says what it was about. An engine can be perfect at
// the first and useless at the second: "42002 invalid reference" with no name
// in it leaves a client no way to underline the offending token except by
// parsing the sentence, and parsing the sentence is what the whole status
// mechanism exists to avoid. GA08 is the feature that asks for both.
//
// Every field is optional, because the standard makes most of them so and
// because a condition raised while the statement ran has no token to point
// at. An empty field means the engine said nothing, never that it said
// nothing was there.
type Diagnostic struct {
	// Subject is the thing the statement named that the condition is about,
	// spelled the way the statement spelled it: a variable, a label, a
	// property, a graph.
	Subject string `json:"subject,omitempty"`
	// SubjectKind is what sort of thing Subject is, as one lower-case word
	// out of graph, schema, label, property, variable, type and function. It
	// is apart from Subject so that asking whether a condition is about a
	// label is one string compared against one word.
	SubjectKind string `json:"subject_kind,omitempty"`
	// Graph and Schema are where the statement was running.
	Graph  string `json:"graph,omitempty"`
	Schema string `json:"schema,omitempty"`
	// Line and Column are the place, one-based, and zero for a condition
	// raised nowhere a token can be pointed at.
	Line   int `json:"line,omitempty"`
	Column int `json:"column,omitempty"`
	// Excerpt is the source line that place falls on, for the client that
	// still has the failure and no longer has the statement.
	Excerpt string `json:"excerpt,omitempty"`
}

// Empty reports whether the record carries nothing at all, which is how an
// adapter that built one out of an engine saying nothing is told apart from
// an adapter that never built one.
func (d *Diagnostic) Empty() bool {
	return d == nil || (d.Subject == "" && d.SubjectKind == "" && d.Graph == "" &&
		d.Schema == "" && d.Line == 0 && d.Column == 0 && d.Excerpt == "")
}

func (f *Failure) Error() string {
	switch {
	case f.Timeout:
		return "timeout: " + f.Message
	case f.GQLStatus != "":
		return f.GQLStatus + ": " + f.Message
	default:
		return f.Message
	}
}

// AsFailure extracts a *Failure from an error chain, or wraps a plain error
// as one so every path through the runner has a status to record.
func AsFailure(err error) *Failure {
	if err == nil {
		return nil
	}
	if f, ok := errors.AsType[*Failure](err); ok {
		return f
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Failure{Timeout: true, Message: err.Error()}
	}
	return &Failure{Message: err.Error()}
}

// ErrUnsupported is returned by an operation an engine genuinely does not
// offer, as opposed to one it offers and got wrong. The runner records the
// two differently and the report never mixes them.
var ErrUnsupported = errors.New("unsupported by this engine")

// Session is one live connection to one graph. Sessions are not safe for
// concurrent use; the runner holds one per worker.
type Session interface {
	// Load puts a fixture into the graph, replacing whatever was there. It
	// returns what the ingest cost, which is a first-class measurement and
	// not overhead.
	Load(ctx context.Context, fx *fixture.Fixture) (LoadStats, error)

	// Exec runs one statement. A statement the engine refuses returns a
	// *Failure, not a Go error about the harness; a broken pipe or a dead
	// process returns an ordinary error and the session is discarded.
	Exec(ctx context.Context, stmt string, params map[string]any) (*Result, error)

	// Reset returns the graph to empty. An adapter whose Capabilities say
	// Isolated is false may return ErrUnsupported, and the runner will
	// rebuild the whole session instead.
	Reset(ctx context.Context) error

	// PID is the operating-system process the sampler should watch, or 0 for
	// an in-process engine, which is watched through the harness's own
	// process instead.
	PID() int

	// DataDir is where the engine keeps this graph's bytes, for the disk
	// measurement. An engine that keeps nothing on disk returns "".
	DataDir() string

	// Close releases the session. It must be safe to call twice.
	Close() error
}

// Explainer is a session whose engine can describe how it would run a
// statement without running it. It is optional; the runner asks for it with a
// type assertion and records nothing when the session does not have it.
//
// It is separate from Exec, rather than a Plan field Exec fills in, because
// the two have to happen at different times. The plan is wanted for every case
// and the harness times every case, so an Exec that also produced a plan would
// either be timing the extra work or timing something the report does not
// describe. Worse, the obvious way for an engine to produce a plan is to
// execute the statement and count, which for a statement that writes would
// apply the write twice. So the runner asks once, after the samples are taken,
// and an adapter that can only answer by executing should not implement this
// at all.
type Explainer interface {
	// Explain returns the engine's own rendering of its plan, in whatever
	// form the engine renders one. It is never compared against anything and
	// never parsed; it is recorded so that a surprising latency in the report
	// has something attached to it.
	//
	// Params are passed because some engines want the shape of the bindings
	// before they will plan. An engine that plans on the statement text alone
	// is free to ignore them.
	//
	// A statement this engine cannot compile returns an error, and the runner
	// drops it: a case that failed to parse has no plan, which the report
	// already says in the outcome column.
	Explain(ctx context.Context, stmt string, params map[string]any) (string, error)
}

// LoadStats is what an adapter can say about an ingest beyond what the
// harness times from outside.
type LoadStats struct {
	// Nodes and Edges are what the adapter believes it loaded. The harness
	// compares them against the fixture and reports a mismatch, because an
	// engine that silently dropped rows would otherwise look fast.
	Nodes, Edges int
	// EngineWall, when the engine reports its own ingest time, is recorded
	// beside the harness's wall time. A large gap is process startup or
	// client-side encoding, and naming it stops it being attributed to
	// storage.
	EngineWall time.Duration
	// SchemaBytes is the part of the store that is fixed by the shape of the
	// database rather than by the graph in it: headers, the catalog, whatever
	// the engine writes before it has been given anything. Zero means the
	// adapter cannot separate the two, and then the harness falls back to
	// weighing an empty store, which is a cruder answer to the same question.
	//
	// It exists because every density figure in the report is a store size
	// divided by a graph, and a store size that is mostly the engine's floor
	// divided by a small graph describes the floor. The run of 2026-08-12
	// published 29 360 128 bits per edge for a one-edge graph and nine
	// different densities for nine stores of identical size, all of them
	// correct arithmetic over the wrong numerator.
	SchemaBytes int64
	// AllocUnit is the smallest amount the store can grow by: a block, a page,
	// an extent. Zero means unknown.
	//
	// A store that rounds up to a unit reports the rounding as encoding. With
	// the unit known the harness can say how much of a density figure is
	// rounding and withhold the figure when that share is too large, instead of
	// publishing a number whose error nobody can bound.
	AllocUnit int64
	// Detail is free text the engine printed about the load, kept for the
	// report.
	Detail string
}

// Driver creates sessions against one engine.
type Driver interface {
	// Name is the adapter's stable identifier, used in ids, filenames, and
	// report columns: "zu", "neo4j", "ladybug".
	Name() string
	// Version is what the engine reports about itself. It is recorded in
	// every report, because a conformance score without a version is a
	// statement about nothing.
	Version(ctx context.Context) (string, error)
	// Capabilities is the engine's declared surface.
	Capabilities() Capabilities
	// Open creates a session with its state under workdir, a directory the
	// harness owns and will measure and then delete.
	Open(ctx context.Context, workdir string) (Session, error)
	// Close releases anything the driver holds across sessions.
	Close() error
}

// Factory builds a driver from the options a user passed on the command line
// or set in a config file.
type Factory func(opts Options) (Driver, error)

// Options is the untyped configuration an adapter receives. Keeping it
// untyped is what lets a third-party adapter live outside this module and
// still be selectable by name.
type Options struct {
	// Binary is a path to the engine's executable, for adapters that drive
	// one.
	Binary string
	// URI, Username, Password, and Database configure a client/server engine.
	URI      string
	Username string
	Password string
	Database string
	// Extra carries adapter-specific settings, e.g. buffer sizes or feature
	// flags, keyed by names the adapter documents.
	Extra map[string]string
}

// Get reads an extra option, returning def when it is unset.
func (o Options) Get(key, def string) string {
	if v, ok := o.Extra[key]; ok && v != "" {
		return v
	}
	return def
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register adds a driver factory under a name. Adapters call it from an init
// function, which is what lets a program select an engine by string without
// importing every engine's client library.
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("adapter: duplicate registration for " + name)
	}
	registry[name] = f
}

// New builds the named driver.
func New(name string, opts Options) (Driver, error) {
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("adapter %q is not registered (have: %v)", name, Registered())
	}
	return f(opts)
}

// Registered lists the adapter names available in this binary, sorted.
func Registered() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
