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

	// The observations run last and on the same session, which is why a probe
	// is forbidden to write: whatever it left behind would belong to no case
	// and be nobody's business to clean up. They are inside the run's wall
	// clock, because they cost what they cost, and outside everything else.
	if cfg.Probes.Len() > 0 && ctx.Err() == nil {
		rep.Implementation = ex.observe(ctx, cfg.Probes, cfg.Catalog)
	}

	rep.Run.Finished = time.Now()
	rep.Run.Wall = rep.Run.Finished.Sub(started)
	rep.Totals, rep.Coverage = summarize(cfg.Catalog, rep.Cases)
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
	}
	defer func() { r.Wall = time.Since(r.Started) }()

	stmt, ok := e.statement(c)
	if !ok {
		r.Outcome, r.Skip = Skip, SkipNoDialect
		r.Reason = fmt.Sprintf("no %s spelling is recorded for this case", e.cfg.Driver.Name())
		return r
	}
	r.Statement = stmt

	if len(c.Params) > 0 && !e.caps.Parameters {
		r.Outcome, r.Skip = Skip, SkipParameters
		r.Reason = "the engine cannot bind named parameters, and inlining them would change the statement"
		return r
	}
	if len(c.Setup) > 0 && !e.caps.MultipleStatements {
		r.Outcome, r.Skip = Skip, SkipSetup
		r.Reason = "the case needs setup statements and the engine runs one statement at a time"
		return r
	}
	// A transaction command sent to an engine with no transaction control
	// would measure the parser's opinion of the keyword and nothing else.
	if slices.Contains(c.Tags, "transaction") && !e.caps.Transactions {
		r.Outcome, r.Skip = Skip, SkipTransactions
		r.Reason = "the engine declares no explicit transaction control"
		return r
	}
	for _, f := range c.Requires {
		if slices.Contains(e.caps.Unsupported, f) {
			r.Outcome, r.Skip = Skip, SkipRequires
			r.Reason = "the case needs feature " + f + ", which this engine documents as unsupported"
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
		r.Outcome, r.Skip = Skip, SkipNoGQLStatus
		r.Reason = "the case expects GQLSTATUS " + c.Expect.GQLStatus +
			" and the engine reports no GQLSTATUS, so a refusal proves nothing about the condition"
		return r
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
			r.Outcome, r.Skip, r.Missing = Skip, SkipCapability, missing
			r.Reason = "the engine cannot hold this fixture: " + capList(missing)
			return r
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

	e.execute(ctx, sess, c, stmt, &r)
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
	// engine's. Mutating cases run once unless the case asks for more, which is
	// how the performance write cases opt in to a distribution.
	if c.Mutating {
		return 1
	}
	return e.cfg.Repeats
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
	cancel()
	wall := time.Since(start)
	proc := sampler.Stop()
	if err != nil {
		// A load the harness cut off is reported as that and not as whatever
		// the adapter's plumbing said on the way down, because the fixture is
		// named here and the adapter does not know what it was waiting for.
		if lctx.Err() != nil && ctx.Err() == nil {
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
	}
	load.Compute()
	e.loaded, e.dirty = fx.Name, false
	return sess, load, nil
}

// execute runs setup, warmups, and the timed repetitions, and judges.
func (e *executor) execute(ctx context.Context, sess adapter.Session, c *corpus.Case, stmt string, r *CaseResult) {
	timeout := e.cfg.Timeout
	if c.TimeoutMS > 0 {
		timeout = time.Duration(c.TimeoutMS) * time.Millisecond
	}

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
			return
		}
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

	sampler := samplerFor(sess, e.cfg.SampleInterval)
	sampler.Start()

	series := &metrics.Series{Warmups: warmups}
	var last *adapter.Result
	var lastErr error
	for range r.Repeats {
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
	judge(c, last, lastErr, e.caps, r)
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

// judge decides the verdict and records the evidence behind it.
func judge(c *corpus.Case, res *adapter.Result, err error, caps adapter.Capabilities, r *CaseResult) {
	if err != nil {
		f := adapter.AsFailure(err)
		r.Message = f.Message
		r.GotStatus = f.GQLStatus
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
			r.Outcome, r.Evidence = Pass, EvidenceStatus
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

func capList(caps []fixture.Capability) string {
	parts := make([]string, len(caps))
	for i, c := range caps {
		parts[i] = string(c)
	}
	return strings.Join(parts, ", ")
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
func ParseSelector(pattern string, kinds, features, tags, skipTags []string) (corpus.Selector, error) {
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
	return sel, nil
}

func validKind(k corpus.Kind) bool { return slices.Contains(corpus.AllKinds, k) }
