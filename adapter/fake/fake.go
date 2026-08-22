// Package fake is an engine that does not exist, for testing the harness.
//
// Every other adapter measures a database. This one measures the measuring:
// it answers whatever it is told to answer, so a test can assert that a wrong
// row produces a failure, that a GQLSTATUS mismatch produces a failure naming
// both codes, that a missing capability produces a skip and not a failure, and
// that the report renders all of it. None of those assertions can be made
// against a real engine, because a real engine is the thing under test and its
// answers are what the run is trying to find out.
//
// It is exported rather than kept in a test file because the same need arises
// outside this module. Somebody writing an adapter for a fourth engine wants
// to see what the harness does with each outcome before wiring up a driver,
// and somebody consuming the report format wants a report to consume without
// installing a database.
package fake

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/rows"
)

func init() { adapter.Register("fake", newFromOptions) }

// Answer is what the engine will do when it is given a particular statement.
// Exactly one of Table and Failure is meaningful; a zero Answer is a statement
// that succeeded and returned no table, which is what a write does.
type Answer struct {
	Table   *rows.Table
	Failure *adapter.Failure
	// Bytes is the reported wire size, so a throughput figure has a numerator.
	Bytes int64
	// Latency is how long this statement pretends to take, overriding the
	// driver's default. A test of the latency summary needs statements whose
	// durations differ by more than the scheduler's noise.
	Latency time.Duration
}

// Config builds a driver. The zero value is an engine with every data
// capability, no GQLSTATUS, and no scripted answers, which succeeds silently
// at everything — useful for exercising the metrics path and useless for
// anything else.
type Config struct {
	// Name is what the driver calls itself. It defaults to "fake"; a test
	// comparing two engines' reports needs two different names.
	Name string
	// Capabilities is the declared surface. A zero Data map is replaced by one
	// with every capability set, because the common case is a test that is not
	// about capabilities at all.
	Capabilities adapter.Capabilities
	// Answers is keyed by the statement text with leading and trailing space
	// removed. Statements not in it get Default.
	Answers map[string]Answer
	// Respond answers a statement Answers does not name, and returning false
	// falls through to Default.
	//
	// It exists because not every caller knows the statements in advance. The
	// grammar walk writes its own and then shrinks them, so a test of the
	// reducer has to describe an engine as a rule about text rather than as a
	// list: refuse anything containing this construct, accept the rest.
	Respond func(stmt string) (Answer, bool)
	// Default is the answer for an unscripted statement.
	Default Answer
	// Explain, when set, makes the session an adapter.Explainer answering with
	// what it returns. Left nil the session does not implement the interface at
	// all, which is not the same as implementing it and returning nothing: the
	// runner distinguishes the two and only one of them is the common case.
	Explain func(stmt string) (string, error)
	// Latency is the default per-statement delay.
	Latency time.Duration
	// LoadLatency is the per-thousand-elements delay during an ingest, so that
	// a large generated fixture costs more to load than a small one and the
	// ingest measurement has a shape to it.
	LoadLatency time.Duration
	// LoadFails, when set, decides an ingest's fate by fixture name. It is how
	// a test reaches the engine that refuses a graph it cannot hold, which is
	// an ordinary answer from an engine and not a fault in the harness.
	LoadFails func(fixture string) error
	// BytesPerNode is how many bytes the session writes to its data directory
	// per loaded node. It exists so the disk measurement has something real to
	// measure: the harness reads the directory, so a fake that wrote nothing
	// would make every disk figure zero and hide a bug in the reader.
	BytesPerNode int
	// BytesPerLabel is how many bytes the session writes per distinct label and
	// edge type in the fixture, before any row. It is the engine that keeps a
	// file per label, which is the whole reason a density floor has to be per
	// fixture: with this set, two fixtures of the same size have different
	// floors and a run that used one number for both would be wrong about one
	// of them.
	BytesPerLabel int
	// SchemaLoadable, when set, makes the session an adapter.SchemaLoader, so
	// the harness can ask it for a fixture's shape on its own and weigh it. Left
	// false the session does not implement the interface at all, which is the
	// position most engines are in and has to stay reachable in a test.
	SchemaLoadable bool
	// SchemaLoadFails, when set, decides by fixture name whether the shape load
	// refuses. A floor that could not be measured is a note in the report and
	// not a failed run, and that is worth a test of its own.
	SchemaLoadFails func(fixture string) error
	// Version is what the driver reports as the engine version.
	Version string
	// FailVersion makes Version return an error, to check that a run survives
	// an engine that will not identify itself.
	FailVersion bool
}

// New builds a driver from a Config.
func New(cfg Config) adapter.Driver {
	if cfg.Name == "" {
		cfg.Name = "fake"
	}
	if cfg.Version == "" {
		cfg.Version = "fake 0.0.0"
	}
	if cfg.Capabilities.Data == nil {
		all := make(map[fixture.Capability]bool, len(fixture.AllCapabilities))
		for _, c := range fixture.AllCapabilities {
			all[c] = true
		}
		cfg.Capabilities.Data = all
	}
	return &driver{cfg: cfg}
}

// newFromOptions is the registry entry point. The registry passes strings, so
// only the handful of settings that can be expressed as one are reachable
// through it; a test wanting scripted answers calls New.
func newFromOptions(opts adapter.Options) (adapter.Driver, error) {
	cfg := Config{
		Name:    opts.Get("name", "fake"),
		Version: opts.Get("version", ""),
	}
	if opts.Get("gqlstatus", "") == "true" {
		cfg.Capabilities.GQLStatus = true
	}
	return New(cfg), nil
}

type driver struct{ cfg Config }

func (d *driver) Name() string                       { return d.cfg.Name }
func (d *driver) Capabilities() adapter.Capabilities { return d.cfg.Capabilities }
func (d *driver) Close() error                       { return nil }

func (d *driver) Version(context.Context) (string, error) {
	if d.cfg.FailVersion {
		return "", os.ErrNotExist
	}
	return d.cfg.Version, nil
}

func (d *driver) Open(_ context.Context, workdir string) (adapter.Session, error) {
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, err
	}
	s := &session{cfg: d.cfg, dir: workdir}
	switch {
	case d.cfg.Explain != nil && d.cfg.SchemaLoadable:
		return &explainingShaped{explaining: &explaining{session: s}}, nil
	case d.cfg.Explain != nil:
		return &explaining{session: s}, nil
	case d.cfg.SchemaLoadable:
		return &shaped{session: s}, nil
	}
	return s, nil
}

// explaining is a session that can also be asked for a plan. It is a separate
// type because implementing an interface is a property of the type and not of
// the value, and a fake that always had the method could not stand in for the
// engines that do not.
type explaining struct{ *session }

func (s *explaining) Explain(_ context.Context, stmt string, _ map[string]any) (string, error) {
	s.mu.Lock()
	s.explains++
	s.mu.Unlock()
	return s.cfg.Explain(strings.TrimSpace(stmt))
}

type session struct {
	cfg Config
	dir string

	mu     sync.Mutex
	closed bool
	// calls counts statements, which is what a test asserting that warmups
	// were suppressed on a mutating case has to look at.
	calls int
	// explains counts plan requests separately, because the whole point of
	// asking for one outside the timed series is that it is not a statement,
	// and a test of that has to be able to see the difference.
	explains int
}

// Calls reports how many statements this session has been given, warmups
// included. A test that the runner does not warm up a mutating case cannot
// observe that from the report — the report says how many warmups were
// intended — so it observes it here.
func (s *session) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Explains reports how many times this session was asked for a plan. A test
// that the plan is fetched once per case, and not once per repetition, has
// nowhere else to look.
func (s *session) Explains() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.explains
}

func (s *session) Load(ctx context.Context, fx *fixture.Fixture) (adapter.LoadStats, error) {
	built, err := fx.Materialize()
	if err != nil {
		return adapter.LoadStats{}, err
	}
	if s.cfg.LoadFails != nil {
		if err := s.cfg.LoadFails(fx.Name); err != nil {
			return adapter.LoadStats{}, err
		}
	}
	elements := len(built.Nodes) + len(built.Edges)
	if s.cfg.LoadLatency > 0 {
		if err := sleep(ctx, time.Duration(elements)*s.cfg.LoadLatency/1000); err != nil {
			return adapter.LoadStats{}, err
		}
	}
	if err := s.writeShape(built); err != nil {
		return adapter.LoadStats{}, err
	}
	if s.cfg.BytesPerNode > 0 {
		blob := make([]byte, len(built.Nodes)*s.cfg.BytesPerNode)
		path := filepath.Join(s.dir, "graph.dat")
		if err := os.WriteFile(path, blob, 0o644); err != nil {
			return adapter.LoadStats{}, err
		}
	}
	return adapter.LoadStats{
		Nodes:  len(built.Nodes),
		Edges:  len(built.Edges),
		Detail: "no engine; the fixture was counted and discarded",
	}, nil
}

// writeShape writes what this engine pretends a fixture's shape costs, which is
// a fixed number of bytes for every distinct label and edge type in it.
func (s *session) writeShape(built *fixture.Fixture) error {
	if s.cfg.BytesPerLabel <= 0 {
		return nil
	}
	kinds := map[string]bool{}
	for _, n := range built.Nodes {
		for _, l := range n.Labels {
			kinds[l] = true
		}
	}
	for _, e := range built.Edges {
		kinds[e.Type] = true
	}
	blob := make([]byte, len(kinds)*s.cfg.BytesPerLabel)
	return os.WriteFile(filepath.Join(s.dir, "schema.dat"), blob, 0o644)
}

// shaped is a session that can be given a fixture's shape on its own. Like
// explaining above it is a separate type, because a fake that always had the
// method could not stand in for the engines whose adapters cannot be asked.
type shaped struct{ *session }

// LoadSchema writes what the shape costs and nothing else, and removes any
// graph a previous load left, so that what is on disk afterwards is a store
// this fixture could be loaded into with no further shape being created.
func (s *shaped) LoadSchema(_ context.Context, fx *fixture.Fixture) (adapter.LoadStats, error) {
	built, err := fx.Materialize()
	if err != nil {
		return adapter.LoadStats{}, err
	}
	if s.cfg.SchemaLoadFails != nil {
		if err := s.cfg.SchemaLoadFails(fx.Name); err != nil {
			return adapter.LoadStats{}, err
		}
	}
	if err := os.Remove(filepath.Join(s.dir, "graph.dat")); err != nil && !os.IsNotExist(err) {
		return adapter.LoadStats{}, err
	}
	if err := s.writeShape(built); err != nil {
		return adapter.LoadStats{}, err
	}
	return adapter.LoadStats{Detail: "no engine; the shape was written and the rows were not"}, nil
}

// explainingShaped is both of the above, for a test that wants an engine with
// plans and a measurable floor. Go decides what a value implements from its
// type, so a session that has to have two optional methods has to be a type
// that has both.
type explainingShaped struct{ *explaining }

func (s *explainingShaped) LoadSchema(ctx context.Context, fx *fixture.Fixture) (adapter.LoadStats, error) {
	return (&shaped{session: s.session}).LoadSchema(ctx, fx)
}

func (s *session) Exec(ctx context.Context, stmt string, _ map[string]any) (*adapter.Result, error) {
	s.mu.Lock()
	closed := s.closed
	s.calls++
	s.mu.Unlock()
	if closed {
		// A real engine given a statement on a closed session returns a
		// transport error, and the runner discards the session and rebuilds it.
		// The fake behaves the same way so that path is reachable in a test.
		return nil, &adapter.Failure{Fatal: true, Message: "session is closed"}
	}

	ans, ok := s.cfg.Answers[strings.TrimSpace(stmt)]
	if !ok && s.cfg.Respond != nil {
		ans, ok = s.cfg.Respond(strings.TrimSpace(stmt))
	}
	if !ok {
		ans = s.cfg.Default
	}
	delay := ans.Latency
	if delay == 0 {
		delay = s.cfg.Latency
	}
	if delay > 0 {
		if err := sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
	if ans.Failure != nil {
		// Returned by value copy: the runner reads the failure and a shared
		// pointer would let one case's judgement scribble on the script.
		f := *ans.Failure
		return nil, &f
	}
	return &adapter.Result{Table: ans.Table, Bytes: ans.Bytes}, nil
}

func (s *session) Reset(context.Context) error {
	if s.cfg.Capabilities.Isolated {
		return os.RemoveAll(filepath.Join(s.dir, "graph.dat"))
	}
	return adapter.ErrUnsupported
}

// PID is this process. An in-process engine is watched through the harness's
// own process, and the fake is as in-process as an engine gets.
func (s *session) PID() int { return os.Getpid() }

func (s *session) DataDir() string { return s.dir }

func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// sleep waits, or returns early if the run was cancelled. A fake that ignored
// the context would make every timeout test hang for the timeout.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
