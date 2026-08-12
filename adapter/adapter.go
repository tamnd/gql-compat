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
	// Plan, when the engine offers one cheaply, is its own rendering of how
	// it executed the statement. It is never compared, only recorded, so a
	// surprising latency in the report has something attached to it.
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
