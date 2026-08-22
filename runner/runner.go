// Package runner executes a corpus against an engine and measures everything
// it does while doing it.
//
// The runner owns every part of a measurement that is not the engine. It
// decides repetition counts, warms caches, starts and stops the sampler,
// compares results, and records the outcome — identically for every adapter,
// because a comparison in which each engine timed itself would be a comparison
// of timing code. Adapters only load fixtures and run statements.
//
// Three rules shape what comes out.
//
// A skip is a result. When an engine's declared capabilities cannot hold a
// fixture, the case is not run and not silently dropped; it is recorded with
// the missing capability named, and the report totals skips beside passes so
// that a score can never be improved by refusing more work.
//
// Evidence is graded. An engine that rejects a bad statement with the
// GQLSTATUS the standard specifies has shown more than one that merely
// rejects it, and the runner records which happened rather than treating them
// as the same pass.
//
// Nothing is rewritten. In conformance mode the case's standard GQL text goes
// to the engine unaltered; in compatibility mode the engine's own documented
// spelling goes instead, and the result carries the text that actually ran.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/impdef"
	"github.com/tamnd/gql-compat/iso"
	"github.com/tamnd/gql-compat/metrics"
	"github.com/tamnd/gql-compat/rows"
)

// Config is a run.
type Config struct {
	// Driver is the engine under test.
	Driver adapter.Driver
	// Suite and Fixtures are what to run and what to run it against.
	Suite    *corpus.Suite
	Fixtures *fixture.Set
	// Catalog supplies the ISO denominators for the coverage tables.
	Catalog *iso.Catalog
	// Probes are the implementation-defined observations to take after the
	// cases have run. They are not cases: they carry no expectation, produce no
	// outcome, and touch no total. A nil set skips the phase entirely.
	Probes *impdef.Set
	// Explore is a walk of the published grammar, run after the suite. What it
	// produces is a lead and never a conformance result, and it lands in
	// Report.Exploration rather than among the cases. Nil skips the phase.
	Explore *Explore

	// Mode chooses standard text or the engine's own spelling.
	Mode Mode
	// Select narrows the run.
	Select corpus.Selector
	// SelectorText is the selector as the user wrote it, for the report.
	SelectorText string

	// Repeats is how many timed executions each case gets. The default is 7,
	// an odd number large enough for a median to mean something and small
	// enough that a thousand-case suite finishes.
	Repeats int
	// Warmups run before the timed ones and are discarded. They exist to pay
	// for plan caching and page faults once rather than in the first sample.
	Warmups int
	// Timeout bounds one statement.
	Timeout time.Duration
	// LoadTimeout bounds one fixture load, which is not one statement and can
	// legitimately take much longer. It exists because an ingest that never
	// finishes is a finding about the engine and not a reason for the harness
	// to sit still: Ladybug's schemaless graph has no key index, so the
	// harness's keyed edge ingest is a scan per edge and the hundred-thousand
	// node fixture ran for six minutes without loading. Bounded, that is an
	// error on the cases that needed the fixture, with the reason attached.
	// Unbounded, it is a run that never ends and a report nobody gets.
	LoadTimeout time.Duration
	// SampleInterval is how often the process sampler reads. Below about a
	// millisecond the sampler starts to cost more than it measures.
	SampleInterval time.Duration
	// MutatingBudget is how long a run will spend rebuilding one case's graph
	// so that a non-idempotent statement can be measured more than once. It is
	// a total per case, not a limit per rebuild: a fixture that loads instantly
	// buys the full repetition count and one that takes seconds buys as many as
	// fit. Zero takes the default; a negative value turns restoring off and
	// leaves every mutating case with the single cold sample it had before.
	MutatingBudget time.Duration

	// Challenge runs the cases the engine's own declaration would have
	// skipped, so that a declaration is contradicted by measurement instead of
	// believed.
	//
	// Every skip the declaration causes is a case the report never puts to the
	// engine, and that is the right default: measuring an absence the adapter
	// already reported wastes a run and files the same finding twice. What it
	// cannot catch is the opposite mistake. An engine that declares a
	// capability it in fact has turns real passes into skips, costs nothing at
	// the time, and reads in the report exactly like a limitation. Nothing else
	// in the harness notices, because the cases that would have noticed are the
	// ones that did not run.
	//
	// A challenging run is therefore not a conformance run and its numbers are
	// not comparable with one: it deliberately puts cases to an engine that
	// said it could not take them, and most of them fail or error. What it
	// produces is Report.Declarations, one entry per declared absence the run
	// challenged, and the entries where every case passed are the ones that
	// contradict the engine.
	Challenge bool

	// WorkDir is where engine state goes. Empty means a temporary directory
	// the runner creates and removes.
	WorkDir string
	// KeepWorkDir leaves it behind, for inspecting what an engine wrote.
	KeepWorkDir bool

	// Progress, when set, is called as each case finishes. It is the only
	// output the runner produces; everything else is in the report.
	Progress func(done, total int, r *CaseResult)
}

func (c *Config) applyDefaults() {
	if c.Mode == "" {
		c.Mode = ModeConformance
	}
	if c.Repeats <= 0 {
		c.Repeats = 7
	}
	if c.Warmups < 0 {
		c.Warmups = 0
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	if c.LoadTimeout <= 0 {
		// Ten statements' worth of patience, on the theory that a fixture is
		// many statements and a load that wants more than that is telling the
		// reader something.
		c.LoadTimeout = 10 * c.Timeout
	}
	if c.SampleInterval <= 0 {
		c.SampleInterval = 5 * time.Millisecond
	}
	if c.MutatingBudget == 0 {
		// Five seconds buys a distribution on every fixture the corpus loads in
		// well under a second, and buys nothing at all on the hundred-thousand
		// node one, which is the right answer in both directions.
		c.MutatingBudget = 5 * time.Second
	}
}

// Run executes the suite and returns the report.
//
// It returns an error only when the run itself could not proceed — no engine,
// no workdir. An engine that fails every case produces a report full of
// failures and a nil error, because that is a result and not a malfunction.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	cfg.applyDefaults()
	if cfg.Driver == nil {
		return nil, errors.New("runner: no driver")
	}
	if cfg.Suite == nil {
		return nil, errors.New("runner: no suite")
	}
	if cfg.Catalog == nil {
		return nil, errors.New("runner: no ISO catalogue")
	}
	// The register of features no portable case can be written for is read
	// here rather than beside the summary, because a register that does not
	// load is a coverage table that would quietly read one feature short and
	// this is the last moment that can be said before the engine starts.
	unwritable, err := corpus.Unwritables(iso.Codes{Catalog: cfg.Catalog})
	if err != nil {
		return nil, err
	}

	workdir := cfg.WorkDir
	if workdir == "" {
		dir, err := os.MkdirTemp("", "gql-compat-*")
		if err != nil {
			return nil, err
		}
		workdir = dir
		if !cfg.KeepWorkDir {
			defer func() { _ = os.RemoveAll(dir) }()
		}
	} else if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, err
	}

	started := time.Now()
	rep := &Report{
		Tool:      "gql-compat",
		Schema:    ReportSchema,
		Generated: started,
		Host:      hostInfo(),
		Run: RunInfo{
			Mode:           cfg.Mode,
			Repeats:        cfg.Repeats,
			Warmups:        cfg.Warmups,
			Timeout:        cfg.Timeout,
			LoadTimeout:    cfg.LoadTimeout,
			SampleInterval: cfg.SampleInterval,
			Selector:       cfg.SelectorText,
			Challenge:      cfg.Challenge,
			WorkDir:        workdir,
			Started:        started,
			ISOSource:      iso.SourceURL,
		},
	}

	caps := cfg.Driver.Capabilities()
	// Refuse before the first case rather than publish a report in which
	// silence and refusal look the same. See Capabilities.Undeclared.
	if missing := caps.Undeclared(); len(missing) > 0 {
		return nil, fmt.Errorf("adapter %s declares no answer for %v; a capability left out of the map is reported as unsupported and would be read as a finding about the engine",
			cfg.Driver.Name(), missing)
	}
	rep.Engine = EngineInfo{
		Adapter:          cfg.Driver.Name(),
		Capabilities:     caps,
		DataCapabilities: caps.DataList(),
	}
	// A version the engine would not report is worth recording as unknown
	// rather than aborting: a report that says so is still useful, and the
	// alternative is no report at all when a server is a version behind.
	if v, err := cfg.Driver.Version(ctx); err == nil {
		rep.Engine.Version = strings.TrimSpace(v)
	} else {
		rep.Engine.Version = "unknown: " + err.Error()
	}

	ex := &executor{cfg: &cfg, caps: caps, workdir: workdir}
	defer ex.close()

	// The empty store is measured before any case, because every density figure
	// the report prints is a fraction of it and a fraction whose denominator
	// arrived halfway through the run would apply to half the loads. It is one
	// load of a fixture with nothing in it, and it leaves that fixture in the
	// engine, which is where the write cases want it anyway.
	ex.empty = ex.measureEmpty(ctx)
	rep.Engine.EmptyStore = ex.empty

	// The floor under every latency the run is about to print, measured on the
	// session the empty load just warmed and before any fixture with a graph in
	// it is loaded, so that nothing about the store can be in it.
	rep.Engine.RoundTrip = ex.measureRoundTrip(ctx)

	cases := cfg.Suite.Filter(cfg.Select)
	rep.Cases = make([]CaseResult, 0, len(cases))
	for i, c := range cases {
		if err := ctx.Err(); err != nil {
			break
		}
		r := ex.run(ctx, c)
		rep.Cases = append(rep.Cases, r)
		if cfg.Progress != nil {
			cfg.Progress(i+1, len(cases), &rep.Cases[len(rep.Cases)-1])
		}
	}

	// The walk runs after the suite, so that a hundred statements nobody wrote
	// can never delay or disturb the cases that cite a clause, and before the
	// observations, so that the last thing to touch the session is still the
	// phase that is forbidden to write to it.
	if cfg.Explore != nil && cfg.Explore.Count > 0 && cfg.Explore.Grammar != nil && ctx.Err() == nil {
		rep.Exploration = ex.explore(ctx, cfg.Explore)
	}

	// The observations run last and on the same session, which is why a probe
	// is forbidden to write: whatever it left behind would belong to no case
	// and be nobody's business to clean up. They are inside the run's wall
	// clock, because they cost what they cost, and outside everything else.
	if cfg.Probes.Len() > 0 && ctx.Err() == nil {
		rep.Implementation = ex.observe(ctx, cfg.Probes, cfg.Catalog)
	}

	rep.Run.Finished = time.Now()
	rep.Run.Wall = rep.Run.Finished.Sub(started)
	rep.Totals, rep.Coverage = summarize(cfg.Catalog, unwritable, rep.Cases)
	rep.Declarations = declarations(rep.Cases)
	return rep, nil
}

// executor holds the mutable state one engine's run carries between cases:
// the live session, and which fixture is currently in it.
type executor struct {
	cfg     *Config
	caps    adapter.Capabilities
	workdir string

	sess adapter.Session
	// loaded is the fixture currently in the engine, and dirty says a
	// mutating case has since changed it. Together they are what lets a
	// hundred read-only cases share one ingest instead of paying for a
	// hundred, without ever letting a write leak into the next case.
	loaded string
	dirty  bool
	// seq numbers session directories so a rebuilt session never inherits the
	// previous one's files, which would corrupt the disk measurement.
	seq int
	// failedLoads remembers a fixture the engine could not ingest, so the
	// twelfth case that wants it is not a twelfth wait for the same answer.
	failedLoads map[string]error
	// empty is what this engine's store weighs with no graph in it, measured
	// once before the first case and carried into every load's density gate.
	empty metrics.EmptyStore
	// loadWall is what each fixture cost to ingest, most recently. It is what
	// tells a mutating case how many times its graph can be put back inside the
	// budget, which is a question that can only be answered by having loaded it
	// once already.
	loadWall map[string]time.Duration
}

// FloorStatement is the cheapest question the harness knows how to ask. It
// returns one row of one constant, so an engine that answers it has done a
// parse, a plan, an execution and a round trip and nothing else. It is written
// in standard GQL and is not translated in compatibility mode, because a floor
// measured through a different statement on each engine would not be a floor
// anybody could compare.
const FloorStatement = "RETURN 1 AS n"

// measureRoundTrip times the cheapest statement the engine can answer, warm and
// repeated, as the floor under every latency in the report.
//
// The report prints case latencies down to microseconds and invites two engines
// to be compared on them. What it did not print, until now, is that some of
// every one of those figures is the harness talking to the engine and none of it
// is the engine answering. For zu that is a JSON line down a pipe to a process
// on the same machine; for Neo4j it is Bolt over a socket to a server; the two
// differ by more than several of the cases differ from each other. A reader who
// cannot see the floor cannot tell a query that is fast from a transport that
// is cheap.
//
// It is measured the way a case is measured — same warmups, same repeat count,
// same nearest-rank percentiles — because a floor obtained differently from the
// numbers it sits under is not a floor those numbers can be read against.
//
// An engine that will not answer it gets a note and no floor. That is a real
// possibility and not a defect in this code: RETURN with no preceding MATCH is
// standard GQL that some engines only accept in some positions, and a run that
// stopped over it would lose a whole report to a missing sentence.
func (e *executor) measureRoundTrip(ctx context.Context) metrics.RoundTrip {
	rt := metrics.RoundTrip{
		Statement: FloorStatement,
		Warmups:   e.cfg.Warmups,
		Repeats:   e.cfg.Repeats,
	}
	if rt.Repeats <= 0 {
		rt.Repeats = 1
	}
	sess, err := e.session(ctx)
	if err != nil {
		rt.Note = "opening a session: " + err.Error()
		return rt
	}

	ask := func() (time.Duration, error) {
		qctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
		defer cancel()
		start := time.Now()
		_, err := sess.Exec(qctx, FloorStatement, nil)
		return time.Since(start), err
	}
	for range rt.Warmups {
		if _, err := ask(); err != nil {
			break
		}
	}
	series := &metrics.Series{Warmups: rt.Warmups}
	for range rt.Repeats {
		wall, err := ask()
		if err != nil {
			if f := adapter.AsFailure(err); f != nil && f.Fatal {
				e.discard()
			}
			rt.Note = "the engine would not answer " + FloorStatement + ": " + err.Error()
			return rt
		}
		series.Samples = append(series.Samples, metrics.Sample{Wall: wall, Rows: 1, Cells: 1})
	}
	rt.Stats = series.Summarize()
	rt.OK = rt.Stats.Count > 0
	return rt
}

// EmptyFixture is the fixture the run loads to find out what an engine's store
// weighs empty. It is a corpus fixture rather than one the runner invents so
// that the engine takes the same route into it that every other load takes; a
// store built by a different path would be a different store.
const EmptyFixture = "blank"

// measureEmpty loads a graph with nothing in it and weighs what is left on
// disk.
//
// This is the denominator of the floor check in metrics.Load. An engine that
// preallocates — which is every engine — writes a store of some fixed size
// before it holds anything, and a fixture smaller than that size divides the
// preallocation by itself and calls the answer an encoding. Nine fixtures
// spanning six nodes to 261 632 edges produced a store of exactly the same size
// on 2026-08-12, and the report published nine different densities for it.
//
// Every way this can fail produces a note rather than an error. Not knowing the
// size of the empty store costs the density figures and nothing else, and a run
// that refused to proceed without them would trade a whole report for two
// columns.
func (e *executor) measureEmpty(ctx context.Context) metrics.EmptyStore {
	if e.cfg.Fixtures == nil {
		return metrics.EmptyStore{Note: "the run carries no fixtures"}
	}
	fx, found := e.cfg.Fixtures.Get(EmptyFixture)
	if !found {
		return metrics.EmptyStore{Note: "fixture " + EmptyFixture + " is not defined"}
	}
	if _, err := fx.Materialize(); err != nil {
		return metrics.EmptyStore{Note: "building fixture " + EmptyFixture + ": " + err.Error()}
	}
	if len(fx.Nodes) > 0 || len(fx.Edges) > 0 {
		return metrics.EmptyStore{Note: fmt.Sprintf("fixture %s holds %d nodes and %d edges, so it does not measure an empty store",
			EmptyFixture, len(fx.Nodes), len(fx.Edges))}
	}
	if missing := fx.Missing(e.caps.Data); len(missing) > 0 {
		return metrics.EmptyStore{Note: "the engine cannot hold the empty fixture: " + capList(missing)}
	}

	sess, err := e.session(ctx)
	if err != nil {
		return metrics.EmptyStore{Note: "opening a session: " + err.Error()}
	}
	_, load, err := e.ensureLoaded(ctx, sess, fx)
	if err != nil {
		e.discard()
		return metrics.EmptyStore{Note: "loading " + EmptyFixture + ": " + err.Error()}
	}
	if load == nil {
		return metrics.EmptyStore{Note: "the empty fixture was already loaded, so nothing was measured"}
	}
	if !load.Disk.OK {
		return metrics.EmptyStore{Note: "the engine's store is not on this machine"}
	}
	if load.Disk.BytesAfter <= 0 {
		return metrics.EmptyStore{Wall: load.Wall,
			Note: "the engine wrote nothing for an empty graph, so it has no floor to measure"}
	}
	return metrics.EmptyStore{
		Bytes: load.Disk.BytesAfter,
		Files: load.Disk.Files,
		Wall:  load.Wall,
		OK:    true,
	}
}

func (e *executor) close() {
	if e.sess != nil {
		_ = e.sess.Close()
		e.sess = nil
	}
	_ = e.cfg.Driver.Close()
}

// session returns a live session, opening one if needed.
func (e *executor) session(ctx context.Context) (adapter.Session, error) {
	if e.sess != nil {
		return e.sess, nil
	}
	e.seq++
	dir := filepath.Join(e.workdir, fmt.Sprintf("session-%03d", e.seq))
	s, err := e.cfg.Driver.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	e.sess, e.loaded, e.dirty = s, "", false
	return s, nil
}

// discard throws the session away, which is the recovery from a fatal failure
// and the only reset available to an engine that declares none.
func (e *executor) discard() {
	if e.sess != nil {
		_ = e.sess.Close()
		e.sess = nil
	}
	e.loaded, e.dirty = "", false
}

// run produces one case's result. The return value is named because the
// deferred wall-time stamp below has to reach the caller: a defer that assigns
// to a local after `return r` has copied it writes to nothing.
func (e *executor) run(ctx context.Context, c *corpus.Case) (r CaseResult) {
	r = CaseResult{
		ID:          c.ID,
		Name:        c.Name,
		Kind:        c.Kind,
		Features:    c.Features,
		Subclauses:  c.Subclauses,
		Productions: c.Productions,
		Conditions:  c.Conditions,
		Tags:        c.Tags,
		Fixture:     c.Fixture,
		Mode:        e.cfg.Mode,
		Started:     time.Now(),
		Repeats:     e.repeats(c),
		Warmups:     e.cfg.Warmups,
		Timing:      TimingSeries,
	}
	defer func() { r.Wall = time.Since(r.Started) }()

	// A condition the corpus has declared unreachable from a client is skipped
	// before the engine is touched. Sending the statement anyway would get an
	// ordinary answer to an ordinary statement and invite a reader to score it,
	// when what the case records is that this code is outside the reach of
	// anything a driver can do.
	if c.Unprovokable != "" {
		r.Outcome, r.Skip = Skip, SkipNotProvokable
		r.Reason = c.Unprovokable
		r.WantStatus = c.Expect.GQLStatus
		return r
	}

	stmt, ok := e.statement(c)
	if !ok {
		r.Outcome, r.Skip = Skip, SkipNoDialect
		r.Reason = fmt.Sprintf("no %s spelling is recorded for this case", e.cfg.Driver.Name())
		return r
	}
	r.Statement = stmt

	if e.generatedSkip(c, stmt, &r) {
		return r
	}
	if len(c.Params) > 0 && !e.caps.Parameters {
		if e.declared(&r, SkipParameters,
			"the engine cannot bind named parameters, and inlining them would change the statement",
			"parameters") {
			return r
		}
	}
	if len(c.Setup) > 0 && !e.caps.MultipleStatements {
		if e.declared(&r, SkipSetup,
			"the case needs setup statements and the engine runs one statement at a time",
			"multiple-statements") {
			return r
		}
	}
	// A transaction command sent to an engine with no transaction control
	// would measure the parser's opinion of the keyword and nothing else.
	if slices.Contains(c.Tags, "transaction") && !e.caps.Transactions {
		if e.declared(&r, SkipTransactions,
			"the engine declares no explicit transaction control", "transactions") {
			return r
		}
	}
	if missing := unsupportedBy(e.caps.Unsupported, c.Requires); len(missing) > 0 {
		if e.declared(&r, SkipRequires,
			"the case needs feature "+strings.Join(missing, ", ")+
				", which this engine documents as unsupported", missing...) {
			return r
		}
	}
	// A case that names the condition it expects cannot be judged by an engine
	// that names none. Every engine can refuse a statement, and most of the
	// ones that refuse a division by zero refuse it because they never parsed
	// the division at all. Scoring the refusal as a pass credited zu with
	// supporting 22007, invalid date format, on the strength of a parse error
	// twelve characters earlier — and the coverage table then printed the code
	// as `supported`, which is the strongest word this report has.
	if c.Expect.Kind == corpus.ExpectError && c.Expect.GQLStatus != "" &&
		c.Expect.ErrorContains == "" && !e.caps.GQLStatus {
		if e.declared(&r, SkipNoGQLStatus,
			"the case expects GQLSTATUS "+c.Expect.GQLStatus+
				" and the engine reports no GQLSTATUS, so a refusal proves nothing about the condition",
			"gqlstatus") {
			return r
		}
	}

	var fx *fixture.Fixture
	if c.Fixture != "" {
		f, found := e.cfg.Fixtures.Get(c.Fixture)
		if !found {
			r.Outcome = Error
			r.Reason = "fixture " + c.Fixture + " is not defined"
			return r
		}
		// A generated fixture has to be expanded before its capabilities can
		// be derived: the generator is a description, and what an engine must
		// be able to hold is the graph it produces.
		if _, err := f.Materialize(); err != nil {
			r.Outcome = Error
			r.Reason = "building fixture " + f.Name + ": " + err.Error()
			return r
		}
		if missing := f.Missing(e.caps.Data); len(missing) > 0 {
			if e.declared(&r, SkipCapability,
				"the engine cannot hold this fixture: "+capList(missing),
				capStrings(missing)...) {
				r.Missing = missing
				return r
			}
		}
		fx = f
	}

	sess, err := e.session(ctx)
	if err != nil {
		r.Outcome = Error
		r.Reason = "opening a session: " + err.Error()
		return r
	}

	if fx != nil {
		// ensureLoaded may have to rebuild the session — that is how an engine
		// with no reset gets back to a known graph — so the session it used is
		// the one this case must run on. Keeping the earlier handle would send
		// the statement to a connection that has since been closed.
		reloaded, load, err := e.ensureLoaded(ctx, sess, fx)
		if err != nil {
			e.discard()
			r.Outcome = Error
			r.Reason = "loading fixture " + fx.Name + ": " + err.Error()
			return r
		}
		sess = reloaded
		if load != nil {
			r.Load = load
		}
	}

	// The plan is made after the load, because how many times a mutating case
	// can be repeated depends on what putting its fixture back costs, and this
	// is the first moment the run knows.
	e.plan(c, &r)
	e.execute(ctx, sess, c, fx, stmt, &r)
	if c.Mutating {
		e.dirty = true
	}
	return r
}

// observe takes every probe, in order, and returns what they saw.
//
// The phase has no verdict to reach, so it has no reason to stop early and no
// reason to repeat: each probe is one execution, unwarmed and untimed beyond
// its own wall clock, and a probe the engine refuses is either an answer or a
// silence, never a mark against the engine. That is why this is a separate
// method from run and not a fifth case kind.
//
// A probe that has to reload a fixture pays for the ingest inside its own wall
// time and produces no ingest row. The ingest table is one row per fixture load
// a case triggered, and adding rows to it that no case triggered would put
// loads in a table whose whole use is attributing them.
func (e *executor) observe(ctx context.Context, set *impdef.Set, cat *iso.Catalog) *impdef.Result {
	out := &impdef.Result{
		DefinedTotal:   len(cat.ImplementationDefined),
		DependentTotal: len(cat.ImplementationDependent),
		Observations:   make([]impdef.Observation, 0, set.Len()),
	}
	for _, p := range set.Probes {
		if ctx.Err() != nil {
			break
		}
		out.Observations = append(out.Observations, e.probe(ctx, p))
	}
	return out
}

// probe runs one probe and returns the observation, including the observation
// that nothing could be observed.
func (e *executor) probe(ctx context.Context, p *impdef.Probe) (o impdef.Observation) {
	started := time.Now()
	defer func() { o.Wall = time.Since(started) }()

	var fx *fixture.Fixture
	if p.Fixture != "" {
		if e.cfg.Fixtures == nil {
			return p.Silent(impdef.NoFixture, "the run has no fixtures")
		}
		f, found := e.cfg.Fixtures.Get(p.Fixture)
		if !found {
			return p.Silent(impdef.NoFixture, "fixture "+p.Fixture+" is not defined")
		}
		if _, err := f.Materialize(); err != nil {
			return p.Silent(impdef.NoLoad, err.Error())
		}
		if missing := f.Missing(e.caps.Data); len(missing) > 0 {
			return p.Silent(impdef.NoFixture, capList(missing))
		}
		fx = f
	}

	sess, err := e.session(ctx)
	if err != nil {
		return p.Silent(impdef.NoSession, err.Error())
	}
	if fx != nil {
		reloaded, _, err := e.ensureLoaded(ctx, sess, fx)
		if err != nil {
			e.discard()
			return p.Silent(impdef.NoLoad, err.Error())
		}
		sess = reloaded
	}

	qctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	res, err := sess.Exec(qctx, p.Statement, nil)
	cancel()
	if f := adapter.AsFailure(err); f != nil && f.Fatal {
		e.discard()
	}
	return p.Observe(res, err)
}

func (e *executor) repeats(c *corpus.Case) int {
	if c.Repeat > 0 {
		return c.Repeat
	}
	// A mutating statement is not repeatable by construction: the second run
	// sees the graph the first one left behind. Running one seven times and
	// comparing the last answer measures the seventh application, and for
	// `MATCH (p:Person {name: 'Ada'}) SET p = {name: 'Ada Lovelace'} RETURN …`
	// the seventh application is a MATCH that finds nothing. That failure was
	// published against Neo4j on 2026-08-12 and it was this harness's, not the
	// engine's. Mutating cases run once here, and plan() gives back a
	// distribution where the graph can be put back between executions.
	if c.Mutating {
		return 1
	}
	return e.cfg.Repeats
}

// restores reports whether a case's graph is to be rebuilt between the timed
// executions. Unset means yes for a mutating case with a fixture; a case whose
// successive applications are the measurement sets it to false.
func restores(c *corpus.Case) bool {
	if !c.Mutating || c.Fixture == "" {
		return false
	}
	if c.Restore != nil {
		return *c.Restore
	}
	return true
}

// plan decides how many timed executions a case gets and which treatment
// produced them.
//
// The interesting case is a mutating statement. One cold sample is the only
// honest reading of a graph that only exists once, and it is also a
// distribution of one: the two runs of 2026-08-12 differed by up to 26× on
// exactly those cases, and nothing in either report could say which was the
// engine. Rebuilding the fixture between executions makes every sample the
// first application again, which is the same statement measured the same way as
// many times as the clock allows.
//
// What the clock allows is the whole question, because the rebuild costs an
// ingest. The budget is a total, not a per-restore limit, so a fixture that
// loads in a millisecond gets the full repetition count and one that takes two
// seconds gets as many as fit. A fixture too slow to restore even once leaves
// the case exactly where it was, cold and alone, and says so.
func (e *executor) plan(c *corpus.Case, r *CaseResult) {
	r.Repeats, r.Timing = e.repeats(c), TimingSeries
	if !c.Mutating {
		return
	}
	r.Timing = TimingColdOnce
	if !restores(c) {
		if c.Fixture == "" {
			r.TimingNote = "the case has no fixture, so there is no graph to put back between executions"
		} else {
			r.TimingNote = "the case asks for its executions to land on each other's results"
			r.Repeats, r.Timing = e.repeats(c), TimingSeries
		}
		return
	}

	want := e.cfg.Repeats
	if c.Repeat > 0 {
		want = c.Repeat
	}
	if want <= 1 {
		r.TimingNote = "one execution was asked for"
		return
	}
	if e.cfg.MutatingBudget < 0 {
		r.TimingNote = "the run allows no time for putting a graph back between executions"
		return
	}
	load, known := e.loadWall[c.Fixture]
	if !known || load <= 0 {
		// The fixture is already in the engine from an earlier case and this run
		// never timed the ingest, or the ingest was too fast to time. Either way
		// the restores are affordable.
		r.Repeats, r.Timing = want, TimingRestored
		return
	}
	// The first execution needs no restore; the graph is already fresh.
	affordable := 1 + int(e.cfg.MutatingBudget/load)
	if affordable <= 1 {
		r.TimingNote = fmt.Sprintf("putting %s back costs %s, and the run allows %s per case for it",
			c.Fixture, metrics.Format(load), metrics.Format(e.cfg.MutatingBudget))
		return
	}
	r.Repeats, r.Timing = min(want, affordable), TimingRestored
	if r.Repeats < want {
		r.TimingNote = fmt.Sprintf("%d of %d executions, because putting %s back costs %s and the run allows %s per case for it",
			r.Repeats, want, c.Fixture, metrics.Format(load), metrics.Format(e.cfg.MutatingBudget))
	}
}

// restore puts the fixture back for the next timed execution. It returns the
// session to use, which is a new one whenever the engine has no reset of its
// own, and false when the graph could not be rebuilt at all.
func (e *executor) restore(ctx context.Context, fx *fixture.Fixture) (adapter.Session, bool) {
	e.dirty = true
	sess, err := e.session(ctx)
	if err != nil {
		return nil, false
	}
	reloaded, _, err := e.ensureLoaded(ctx, sess, fx)
	if err != nil {
		e.discard()
		return nil, false
	}
	return reloaded, true
}

// statement picks the text to run. In compatibility mode a case without a
// spelling for this engine has nothing to say and is skipped.
func (e *executor) statement(c *corpus.Case) (string, bool) {
	if e.cfg.Mode != ModeCompat {
		return c.Query, true
	}
	if s, ok := c.Dialects[e.cfg.Driver.Name()]; ok && strings.TrimSpace(s) != "" {
		return s, true
	}
	return "", false
}

// ensureLoaded gets the right fixture into the engine, reloading only when it
// has to, and measures the ingest when it does. It returns the session the
// fixture ended up in, which is not always the one it was given.
func (e *executor) ensureLoaded(ctx context.Context, sess adapter.Session, fx *fixture.Fixture) (adapter.Session, *metrics.Load, error) {
	if e.loaded == fx.Name && !e.dirty {
		return sess, nil, nil
	}
	if err, ok := e.failedLoads[fx.Name]; ok {
		return nil, nil, err
	}
	if e.dirty && !e.caps.Isolated {
		// The engine has no reset, so the only way back to a known graph is a
		// new session. That cost is real and shows up in this case's wall
		// time, which is the honest place for it.
		e.discard()
		s, err := e.session(ctx)
		if err != nil {
			return nil, nil, err
		}
		sess = s
	}
	dir := sess.DataDir()
	disk := metrics.Before(dir)
	sampler := samplerFor(sess, e.cfg.SampleInterval)
	sampler.Start()
	start := time.Now()
	lctx, cancel := context.WithTimeout(ctx, e.cfg.LoadTimeout)
	stats, err := sess.Load(lctx, fx)
	// Asked before the cancel, because after it every load looks cut off. An
	// engine that refused a fixture in a millisecond was being reported as one
	// that ran out of the load timeout, which sent a reader looking for a slow
	// ingest that never happened. It stayed hidden as long as no load failed;
	// the first challenging run produced 107 of them.
	cutOff := errors.Is(lctx.Err(), context.DeadlineExceeded)
	cancel()
	wall := time.Since(start)
	proc := sampler.Stop()
	if err != nil {
		// A load the harness cut off is reported as that and not as whatever
		// the adapter's plumbing said on the way down, because the fixture is
		// named here and the adapter does not know what it was waiting for.
		if cutOff && ctx.Err() == nil {
			err = fmt.Errorf("it did not finish within %s (%d nodes, %d edges)",
				e.cfg.LoadTimeout, len(fx.Nodes), len(fx.Edges))
		}
		// A fixture that could not be loaded will not load for the next case
		// either, and each attempt costs the whole timeout. The first attempt
		// is the measurement; the rest of the cases get its answer.
		if e.failedLoads == nil {
			e.failedLoads = map[string]error{}
		}
		e.failedLoads[fx.Name] = err
		return nil, nil, err
	}
	if stats.Nodes != len(fx.Nodes) || stats.Edges != len(fx.Edges) {
		// An engine that loaded fewer rows than the fixture holds would answer
		// every later case about a different graph, and would look fast doing
		// it.
		return nil, nil, fmt.Errorf("engine loaded %d nodes and %d edges, fixture has %d and %d",
			stats.Nodes, stats.Edges, len(fx.Nodes), len(fx.Edges))
	}

	load := &metrics.Load{
		Wall:       wall,
		EngineWall: stats.EngineWall,
		Nodes:      stats.Nodes,
		Edges:      stats.Edges,
		Process:    proc,
		Disk:       metrics.After(dir, disk),
		EmptyBytes: e.empty.Bytes,
		// What the engine says about its own store, where it says anything. It
		// is not checked against the empty load: the two answer the same
		// question and this one answers it about this store, so a disagreement
		// between them is the empty load being a proxy and not a fault.
		SchemaBytes: stats.SchemaBytes,
		AllocUnit:   stats.AllocUnit,
	}
	load.Compute()
	e.loaded, e.dirty = fx.Name, false
	if e.loadWall == nil {
		e.loadWall = map[string]time.Duration{}
	}
	e.loadWall[fx.Name] = wall
	return sess, load, nil
}

// setup runs a case's setup statements against the session it is given and
// reports whether the case can go on. A setup statement that fails is the
// case's error, because the statement under test never got the graph it was
// written for.
//
// It runs again after every restore. Putting the fixture back undoes the
// setup along with everything else, and a case whose setup created a graph or
// inserted a row would otherwise measure its second execution against a state
// nobody described.
func (e *executor) setup(ctx context.Context, sess adapter.Session, c *corpus.Case, timeout time.Duration, r *CaseResult) bool {
	for _, s := range c.Setup {
		sctx, cancel := context.WithTimeout(ctx, timeout)
		_, err := sess.Exec(sctx, s, nil)
		cancel()
		if err != nil {
			if f := adapter.AsFailure(err); f != nil && f.Fatal {
				e.discard()
			}
			r.Outcome = Error
			r.Reason = "setup statement failed: " + err.Error()
			r.Message = err.Error()
			return false
		}
	}
	return true
}

// execute runs setup, warmups, and the timed repetitions, and judges.
func (e *executor) execute(ctx context.Context, sess adapter.Session, c *corpus.Case, fx *fixture.Fixture, stmt string, r *CaseResult) {
	timeout := e.cfg.Timeout
	if c.TimeoutMS > 0 {
		timeout = time.Duration(c.TimeoutMS) * time.Millisecond
	}

	if !e.setup(ctx, sess, c, timeout, r) {
		return
	}

	dir := sess.DataDir()
	disk := metrics.Before(dir)

	// A warm-up of a mutating statement is not a warm-up: it is an unmeasured
	// write that changes what the measured ones then do. Mutating cases get
	// none, and the result says so rather than reporting a warmup count that
	// did not happen.
	warmups := e.cfg.Warmups
	if c.Mutating {
		warmups = 0
	}
	r.Warmups = warmups
	for range warmups {
		wctx, cancel := context.WithTimeout(ctx, timeout)
		_, err := sess.Exec(wctx, stmt, c.Params)
		cancel()
		if timedOut(err) {
			break
		}
	}

	sampler := e.sampler()
	sampler.Start()

	series := &metrics.Series{Warmups: warmups}
	var last *adapter.Result
	var lastErr error
	for i := range r.Repeats {
		if i > 0 && r.Timing == TimingRestored {
			// Put the graph back so this execution is the first application
			// again. The restore is outside the sample and inside the case's
			// wall time, which is where a cost the harness chose to pay belongs,
			// and the sampler is stopped across it so that an ingest is not
			// charged to the statement's CPU.
			sampler.Stop()
			restored, ok := e.restore(ctx, fx)
			if !ok {
				r.TimingNote = fmt.Sprintf("%d of %d executions; %s could not be put back again",
					len(series.Samples), r.Repeats, fx.Name)
				break
			}
			sess = restored
			// The restore put the graph back and took the setup with it.
			if !e.setup(ctx, sess, c, timeout, r) {
				return
			}
			// An engine with no reset comes back in a new directory, so the
			// disk baseline has to move with it. What the case then reports is
			// the last execution's storage, which is the only one the store on
			// disk at the end belongs to.
			dir = sess.DataDir()
			disk = metrics.Before(dir)
			sampler.Start()
		}
		qctx, cancel := context.WithTimeout(ctx, timeout)
		start := time.Now()
		res, err := sess.Exec(qctx, stmt, c.Params)
		wall := time.Since(start)
		cancel()

		s := metrics.Sample{Wall: wall, Err: err}
		if res != nil && res.Table != nil {
			s.Rows = res.Table.Len()
			s.Cells = res.Table.Cells()
			s.Bytes = res.Bytes
		}
		series.Samples = append(series.Samples, s)
		last, lastErr = res, err

		if f := adapter.AsFailure(err); f != nil && f.Fatal {
			// The session is gone; further repetitions would measure a
			// restart. Stop with what was collected.
			e.discard()
			break
		}
		if timedOut(err) {
			// The verdict is already an error whatever the remaining
			// repetitions do, and each of them costs the whole timeout again.
			// Two cases on which the Ladybug shell waited for a continuation
			// line spent 240s each in a 1138s run of 2026-08-12 for the eight
			// identical timeouts nobody read.
			break
		}
		if err != nil && !expectsFailure(c) {
			// A case that was supposed to return rows and did not will fail
			// identically on every repetition; running six more of them buys
			// nothing but time.
			break
		}
	}

	series.Process = sampler.Stop()
	series.Disk = metrics.After(dir, disk)

	r.Stats = series.Summarize()
	r.Process = series.Process
	r.Disk = series.Disk
	r.Plan = e.explain(ctx, sess, stmt, c.Params, timeout, last)
	judge(c, last, lastErr, e.caps, r)
	e.checkParses(ctx, sess, c, timeout, r)
}

// explain asks the engine how it ran the statement, for the report to print
// beside the latency.
//
// It runs after the timed repetitions and never inside them. Before would warm
// whatever the engine caches about the statement, and a mutating case gets no
// warm-ups precisely so that its first execution pays that cost where a reader
// can see it. It is also outside the sampler, so the work of rendering a plan
// is charged to nothing.
//
// Four things stop it. An engine with no Explainer has nothing to ask, and the
// field stays empty rather than being filled with the harness's own guess. A
// session the run killed cannot be asked, and opening a new one would answer
// about a different graph. An adapter that already filled Result.Plan on the
// ordinary path is believed and not asked again. And a statement the engine
// could not compile has no plan to give, so the error is dropped: the outcome
// column already says the case did not parse, and a second sentence saying it
// again in the plan column would read like a separate defect.
func (e *executor) explain(ctx context.Context, sess adapter.Session, stmt string, params map[string]any, timeout time.Duration, last *adapter.Result) string {
	if last != nil && last.Plan != "" {
		return last.Plan
	}
	if e.sess == nil {
		return ""
	}
	ex, ok := sess.(adapter.Explainer)
	if !ok {
		return ""
	}
	xctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	text, err := ex.Explain(xctx, stmt, params)
	if err != nil {
		if f := adapter.AsFailure(err); f != nil && f.Fatal {
			e.discard()
		}
		return ""
	}
	return text
}

// checkParses runs a condition case's control statement and, if the engine
// refuses that too, turns the case's failure into a skip.
//
// A condition case puts a statement the standard says must be rejected with a
// named code, and reads the code back. That reading is only meaningful if the
// engine parsed the statement: an engine with no syntax for the construct the
// condition is raised through rejects it at the parser, reports a syntax error,
// and is then recorded as having named the wrong condition. Five of Neo4j's
// thirteen condition failures in the run of 2026-08-12 were that, and the
// report attributed a syntax gap to the diagnostic machinery.
//
// The control is the same syntax written so that it should succeed. Refused, it
// says the engine cannot parse the shape at all and the condition was never
// reachable, which is a skip for the same reason SkipRequires is one — the
// missing feature is already counted where it belongs, and counting it again
// here would count it twice. Accepted, the failure stands and is now known to
// be what it looked like.
//
// It runs only after a code mismatch, only in conformance mode — the control is
// standard GQL and says nothing about a dialect spelling — and only once,
// outside the timed series, so it can never move a latency figure.
func (e *executor) checkParses(ctx context.Context, sess adapter.Session, c *corpus.Case, timeout time.Duration, r *CaseResult) {
	if c.Parses == "" || e.cfg.Mode != ModeConformance {
		return
	}
	if r.Outcome != Fail || r.Evidence != EvidenceStatus {
		return
	}
	// A fatal failure took the session with it, and opening a new one here
	// would run the control against a graph the case never saw.
	if e.sess == nil {
		return
	}

	pctx, cancel := context.WithTimeout(ctx, timeout)
	_, err := sess.Exec(pctx, c.Parses, c.Params)
	cancel()

	check := &ParseCheck{Statement: c.Parses, Accepted: err == nil}
	f := adapter.AsFailure(err)
	if f != nil {
		check.GQLStatus, check.Message = f.GQLStatus, f.Message
		if f.Fatal {
			e.discard()
		}
	}
	r.Parse = check

	if check.Accepted {
		r.Reason += "; the engine accepts this syntax, so the code is the defect"
		return
	}
	// A control the harness cut off, or a session that fell over under it, is
	// not evidence that the engine cannot parse the shape. The case keeps its
	// failure and the reader gets the control's own words.
	if f.Timeout || f.Transport {
		r.Reason += "; the control statement did not complete, so this could not be checked"
		return
	}
	r.Outcome, r.Skip = Skip, SkipUnparsed
	r.Reason = "the engine refused the control statement too, so it cannot parse this syntax and the condition was never reachable: " + check.Message
}

// timedOut reports whether the harness cut the statement off rather than the
// engine answering it. It is separate from expectsFailure because a case that
// expects a refusal is still not expecting silence: an engine that never
// replied has not refused anything, and repeating the question only spends the
// timeout again.
func timedOut(err error) bool {
	f := adapter.AsFailure(err)
	return f != nil && f.Timeout
}

func expectsFailure(c *corpus.Case) bool {
	return c.Expect.Kind == corpus.ExpectError || c.Expect.Kind == corpus.ExpectReject
}

// samplerFor watches the engine's process, or the harness's own when the
// engine runs in it. An engine on the far end of a socket has no process here
// to watch, and a zero pid produces a sampler that reports nothing available
// rather than reporting zeros.
// samplerFor watches whatever process the session currently owns, asking it
// again on every tick rather than once at the start. A Load that restarts the
// engine — which is how an adapter with a bulk loader reloads — would
// otherwise be measured through the pid of the process it was about to
// replace, and the ingest row would report the shell's idle cost instead of
// the loader's work.
func samplerFor(sess adapter.Session, interval time.Duration) *metrics.Sampler {
	return metrics.NewSamplerFunc(sess.PID, interval)
}

// sampler watches whichever session the executor holds at each tick, rather
// than the one it held when the window opened. A case that puts its fixture
// back between executions gets a new session out of an engine with no reset,
// and a sampler bound to the old one would follow a closed handle and report
// that nothing was measurable.
func (e *executor) sampler() *metrics.Sampler {
	return metrics.NewSamplerFunc(func() int {
		if e.sess == nil {
			return 0
		}
		return e.sess.PID()
	}, e.cfg.SampleInterval)
}

// judge decides the verdict and records the evidence behind it.
func judge(c *corpus.Case, res *adapter.Result, err error, caps adapter.Capabilities, r *CaseResult) {
	if err != nil {
		f := adapter.AsFailure(err)
		r.Message = f.Message
		r.GotStatus = f.GQLStatus
		r.Diagnostic = f.Diagnostic
		switch {
		case errors.Is(err, adapter.ErrUnsupported):
			r.Outcome, r.Skip = Skip, SkipCapability
			r.Reason = f.Message
			return
		case f.Timeout:
			// A statement the harness cut off proves nothing about the
			// engine's conformance, so it is an error and not a rejection.
			r.Outcome = Error
			r.Reason = "timed out"
			return
		case f.Transport:
			// The plumbing broke, which says nothing about the engine either.
			// The adapter's own words are the reason, because only the
			// adapter knows which part of the plumbing it was.
			r.Outcome = Error
			r.Reason = f.Message
			return
		}
	}

	// A generated statement carries an accept expectation because the grammar
	// admits it, but it is not judged the way a hand-written accept case is.
	// See judgeGenerated for why the rule is narrower.
	if c.Kind == corpus.KindGenerated {
		judgeGenerated(err, r)
		return
	}

	switch c.Expect.Kind {
	case corpus.ExpectReject:
		if err == nil {
			r.Outcome = Fail
			r.Reason = "the statement was accepted; the grammar does not admit it"
			return
		}
		r.Outcome, r.Evidence = Pass, EvidenceRejected
		return

	case corpus.ExpectError:
		if err == nil {
			if c.Limit != "" {
				r.Outcome, r.Skip = Skip, SkipWithinLimit
				r.Reason = fmt.Sprintf(
					"the engine accepted it, so the condition was not reachable here and its limit is at least what this case asked for; ISO leaves that to the implementation (%s)",
					c.Limit)
				return
			}
			r.Outcome = Fail
			r.Reason = "the statement succeeded; the standard requires it to fail"
			return
		}
		judgeError(c, caps, r)
		return

	case corpus.ExpectAccept:
		if err != nil {
			r.Outcome = Fail
			r.Reason = "the engine refused a statement the grammar admits"
			return
		}
		r.Outcome, r.Evidence = Pass, EvidenceAccepted
		return

	case corpus.ExpectEmpty:
		if err != nil {
			r.Outcome = Fail
			r.Reason = "the statement failed: " + r.Message
			return
		}
		if res != nil && res.Table != nil && res.Table.Len() > 0 {
			r.Outcome = Fail
			r.Reason = fmt.Sprintf("expected no rows, got %d", res.Table.Len())
			return
		}
		r.Outcome, r.Evidence = Pass, EvidenceRows
		return

	case corpus.ExpectRows:
		if err != nil {
			r.Outcome = Fail
			r.Reason = "the statement failed: " + r.Message
			return
		}
		if res == nil || res.Table == nil {
			r.Outcome = Fail
			r.Reason = "the statement returned no table"
			return
		}
		want := &rows.Table{Columns: c.Expect.Columns, Rows: c.Expect.Rows}
		opt := rows.Options{
			Unordered:     c.Expect.Unordered,
			StrictColumns: len(c.Expect.Columns) > 0,
		}
		if d := rows.Compare(want, res.Table, opt); d != nil {
			r.Outcome, r.Diff = Fail, d
			r.Reason = d.Reason
			return
		}
		r.Outcome, r.Evidence = Pass, EvidenceRows
		return
	}

	r.Outcome = Error
	r.Reason = "unknown expectation kind " + string(c.Expect.Kind)
}

// judgeError scores a failure that was supposed to happen. What separates a
// strong pass from a weak one here is whether the engine named the condition
// the standard names, or merely declined.
func judgeError(c *corpus.Case, caps adapter.Capabilities, r *CaseResult) {
	r.WantStatus = c.Expect.GQLStatus

	if c.Expect.GQLStatus != "" && caps.GQLStatus && r.GotStatus != "" {
		if r.GotStatus == c.Expect.GQLStatus {
			if reason := diagnosticMiss(c.Expect.Diagnostic, r.Diagnostic); reason != "" {
				r.Outcome, r.Evidence = Fail, EvidenceStatus
				r.Reason = reason
				return
			}
			r.Outcome, r.Evidence = Pass, EvidenceStatus
			return
		}
		if slices.Contains(c.Expect.AlsoGQLStatus, r.GotStatus) {
			r.Outcome, r.Evidence = Pass, EvidenceStatus
			r.Reason = fmt.Sprintf("rejected with GQLSTATUS %s, which the standard permits here alongside %s",
				r.GotStatus, c.Expect.GQLStatus)
			return
		}
		r.Outcome, r.Evidence = Fail, EvidenceStatus
		r.Reason = fmt.Sprintf("rejected with GQLSTATUS %s, the standard specifies %s",
			r.GotStatus, c.Expect.GQLStatus)
		return
	}

	if c.Expect.ErrorContains != "" {
		if strings.Contains(strings.ToLower(r.Message), strings.ToLower(c.Expect.ErrorContains)) {
			r.Outcome, r.Evidence = Pass, EvidenceMessage
			return
		}
		r.Outcome, r.Evidence = Fail, EvidenceMessage
		r.Reason = fmt.Sprintf("the message does not mention %q: %s", c.Expect.ErrorContains, r.Message)
		return
	}

	// The engine declares that it reports GQLSTATUS and then reported none for
	// a condition the standard gives a code. That is a failure of the thing
	// the case tests, not an absence of evidence: the case reached an engine
	// able to answer it and got no answer.
	if c.Expect.GQLStatus != "" && caps.GQLStatus {
		r.Outcome, r.Evidence = Fail, EvidenceStatus
		r.Reason = "rejected without a GQLSTATUS, and the standard specifies " + c.Expect.GQLStatus
		return
	}

	// A case that asked only that the statement fail, and it did. The weakest
	// evidence the harness records, and the report says which passes rest on
	// it. A condition case cannot reach here: one that names a code is skipped
	// before it runs when the engine reports none.
	r.Outcome, r.Evidence = Pass, EvidenceRejected
}

// diagnosticMiss reports why the record the engine attached does not satisfy
// what the case asked of it, or "" when it does.
//
// It runs only after the code matched. A case that asserts a record is still
// first a case about a condition, and an engine that raised the wrong
// condition should be told which one it got wrong rather than told its record
// is missing a field of a status it never reported.
//
// Each field is checked for equality and not for presence, because a record
// naming the wrong thing is worse than a record naming nothing: it sends a
// client to underline a token the statement got right.
func diagnosticMiss(want *corpus.ExpectDiagnostic, got *adapter.Diagnostic) string {
	if want == nil {
		return ""
	}
	if got == nil {
		return "the status carried no diagnostic record, and GA08 asks for one"
	}
	if want.Subject != "" && got.Subject != want.Subject {
		if got.Subject == "" {
			return fmt.Sprintf("the record does not say what the condition is about; the statement named %q", want.Subject)
		}
		return fmt.Sprintf("the record says the condition is about %q, and it is about %q", got.Subject, want.Subject)
	}
	if want.SubjectKind != "" && got.SubjectKind != want.SubjectKind {
		if got.SubjectKind == "" {
			return fmt.Sprintf("the record names a subject and not what sort of thing it is; %q is a %s", want.Subject, want.SubjectKind)
		}
		return fmt.Sprintf("the record calls the subject a %s, and it is a %s", got.SubjectKind, want.SubjectKind)
	}
	if want.Schema != "" && got.Schema != want.Schema {
		if got.Schema == "" {
			return "the record does not say which schema the statement was running in"
		}
		return fmt.Sprintf("the record says the statement ran in schema %q, and it ran in %q", got.Schema, want.Schema)
	}
	if want.Position && got.Line == 0 && got.Column == 0 {
		return "the record points at no place in the statement"
	}
	return ""
}

func capList(caps []fixture.Capability) string {
	return strings.Join(capStrings(caps), ", ")
}

func capStrings(caps []fixture.Capability) []string {
	parts := make([]string, len(caps))
	for i, c := range caps {
		parts[i] = string(c)
	}
	return parts
}

// unsupportedBy returns the features a case requires that the engine documents
// as absent, in the case's own order. It returns all of them rather than the
// first, because a case that needs two missing features is two findings when
// the declaration is challenged and one skip when it is not.
func unsupportedBy(unsupported, requires []string) []string {
	var out []string
	for _, f := range requires {
		if slices.Contains(unsupported, f) {
			out = append(out, f)
		}
	}
	return out
}

// declared applies one of the engine's declared absences to a case, and
// reports whether the case is finished.
//
// An ordinary run marks the case skipped and stops: the engine has already
// said it cannot do this, and putting the statement to it anyway would file
// the same absence a second time under a case name that is about something
// else. A challenging run records what it overrode and lets the case through
// to the engine, which is the only way a declaration that is wrong in the
// generous direction can be caught.
func (e *executor) declared(r *CaseResult, reason SkipReason, why string, claims ...string) bool {
	if !e.cfg.Challenge {
		r.Outcome, r.Skip = Skip, reason
		r.Reason = why
		return true
	}
	r.Challenges = append(r.Challenges, Challenge{Reason: reason, Claims: claims, Note: why})
	return false
}

// hostInfo describes the machine. Every field is best-effort: a container
// without /proc, a locked-down CI runner, or a platform gopsutil does not
// cover leaves fields empty rather than failing the run.
func hostInfo() HostInfo {
	h := goHost()
	if info, err := host.Info(); err == nil {
		h.Kernel = info.KernelVersion
		h.Platform = strings.TrimSpace(info.Platform + " " + info.PlatformVersion)
		h.Hostname = info.Hostname
		h.Containerised = info.VirtualizationRole == "guest"
	}
	if cs, err := cpu.Info(); err == nil && len(cs) > 0 {
		h.CPUModel = strings.TrimSpace(cs[0].ModelName)
		h.CPUMHz = cs[0].Mhz
	}
	if n, err := cpu.Counts(false); err == nil {
		h.CPUCores = n
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		h.MemoryTotal = int64(vm.Total)
	}
	return h
}

// ParseSelector builds a Selector from the command line's flag values.
//
// Large cases are excluded unless large is set, or unless the run asked for the
// tag by name, which is the same request made a different way. The exclusion is
// here rather than in the corpus so that every command reaching for a selection
// gets the same one: a `list` that showed cases a `run` would not execute would
// be describing a different corpus.
func ParseSelector(pattern string, kinds, features, tags, skipTags []string, large bool) (corpus.Selector, error) {
	var sel corpus.Selector
	if pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return sel, fmt.Errorf("bad -run pattern: %w", err)
		}
		sel.IDPattern = re
	}
	for _, k := range kinds {
		kind := corpus.Kind(strings.ToLower(strings.TrimSpace(k)))
		if !validKind(kind) {
			return sel, fmt.Errorf("unknown kind %q", k)
		}
		sel.Kinds = append(sel.Kinds, kind)
	}
	sel.Features = features
	sel.Tags = tags
	sel.SkipTags = skipTags
	if !large && !slices.Contains(tags, corpus.LargeTag) && !slices.Contains(skipTags, corpus.LargeTag) {
		sel.SkipTags = append(sel.SkipTags, corpus.LargeTag)
	}
	return sel, nil
}

func validKind(k corpus.Kind) bool { return slices.Contains(corpus.AllKinds, k) }
