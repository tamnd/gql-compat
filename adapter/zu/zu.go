// Package zu drives the zu embedded property-graph database through its
// persistent JSON-lines shell.
//
// zu offers three ways in: a one-shot `zu query`, a C API, and `zu shell
// --format jsonl`, a long-lived process that reads one request per line and
// writes one response per line. The shell is the right one to measure
// through. A one-shot invocation pays process start, file open, catalog read,
// and a cold plan cache on every statement, and the resulting number would
// describe the operating system's fork path rather than the database. The
// shell pays those once, which is also how a real embedding uses it.
//
// Ingest is the adapter's awkward part, and worth explaining. zu's bulk
// loader, `zu copy`, takes a two-column edge list and nothing else, so a
// fixture loaded that way keeps its topology and loses every label and every
// property — which is to say the whole corpus becomes unrunnable. zu's other
// documented ingest route is `zu convert file.db file.zu1`, reading a SQLite
// database in zu's own schema, and that route carries labels, node properties
// and typed edges. The adapter writes that SQLite file directly (see
// sqlite.go) rather than reducing every fixture to an edge list.
//
// What the route cannot carry is worked around nowhere: one label per node,
// no edge properties, no nulls or lists, and no edge between two different
// labels, since zu binds a rel table to a single node table. The list is not written down here. It comes off the binary, from
// `zu conformance --declare --format json`, which renders the same tables as
// the conformance.toml checked into the zu repository. Each of those is a limit of
// zu's loader rather than of its query engine, and each shows up in the report
// as a named skip rather than as a failure. The skip list is a finding in its
// own right: it is the distance between what zu can evaluate and what zu can
// be given.
package zu

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/rows"
)

func init() { adapter.Register("zu", New) }

// Driver runs zu out of a binary on disk.
type Driver struct {
	binary string
	caps   adapter.Capabilities
}

// declaration is what `zu conformance --declare --format json` writes. It is
// the same content as the conformance.toml checked into the zu repository,
// rendered from the same tables, minus the per-flag reasons, which exist for
// a person reading the file and would be a trap for anything matching on
// them.
type declaration struct {
	Engine struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"engine"`
	Data         map[string]bool `json:"data"`
	Capabilities map[string]bool `json:"capabilities"`
	Notes        []string        `json:"notes"`
}

// New builds a zu driver. The binary defaults to whatever `zu` resolves to on
// PATH; point Binary at a build tree's target/release/zu to measure a change.
//
// The capabilities come off the binary rather than out of this file. They are
// facts about the engine, they change in the commit that changes the engine,
// and the reviewer of that commit is the one who knows whether they moved. As
// long as they lived here the two drifted, and a stale "no" is expensive in a
// way a stale "yes" is not: it turns a case the engine would now pass into a
// silent skip, which reads as coverage.
func New(opts adapter.Options) (adapter.Driver, error) {
	bin := opts.Binary
	if bin == "" {
		bin = "zu"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("zu: cannot find binary %q: %w", bin, err)
	}
	// New takes no context, and printing a constant string should not need
	// one, but a binary that wedges here would wedge driver construction
	// with no way out. Ten seconds is far past generous for the work and
	// well short of a person deciding the harness has hung.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, resolved, "conformance", "--declare", "--format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("zu: %s cannot declare its capabilities, which every "+
			"zu since 0.0.1 can: run `%s conformance --declare` to see why: %w", resolved, resolved, err)
	}
	caps, err := parseDeclaration(out)
	if err != nil {
		return nil, fmt.Errorf("zu: %s: %w", resolved, err)
	}
	return &Driver{binary: resolved, caps: caps}, nil
}

// parseDeclaration turns the engine's declaration into capabilities, and
// refuses anything it cannot account for.
//
// The two vocabularies are checked against each other in both directions. A
// name zu declares that this harness does not know is a typo or a capability
// nobody taught the fixtures about, and a name the harness knows that zu is
// silent on is the drift this whole arrangement exists to catch. Either way
// the run should stop rather than proceed on a half-known engine, because
// both mistakes come out the other end as skips and a skip looks like a
// decision.
func parseDeclaration(raw []byte) (adapter.Capabilities, error) {
	var d declaration
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return adapter.Capabilities{}, fmt.Errorf("declaration is not the shape this harness reads: %w", err)
	}
	caps := adapter.Capabilities{
		Data:  make(map[fixture.Capability]bool, len(d.Data)),
		Notes: d.Notes,
	}
	known := make(map[string]bool, len(fixture.AllCapabilities))
	for _, c := range fixture.AllCapabilities {
		known[string(c)] = true
	}
	for name, ok := range d.Data {
		if !known[name] {
			return adapter.Capabilities{}, fmt.Errorf("declares a data capability %q this harness has no fixtures for", name)
		}
		caps.Data[fixture.Capability(name)] = ok
	}
	for _, c := range fixture.AllCapabilities {
		if _, ok := d.Data[string(c)]; !ok {
			return adapter.Capabilities{}, fmt.Errorf("says nothing about %q, and not declared is not no", c)
		}
	}
	// The engine flags are a fixed set rather than a map, so each is named
	// once here and a missing one is as loud as a wrong one.
	flags := map[string]*bool{
		"gqlstatus":           &caps.GQLStatus,
		"parameters":          &caps.Parameters,
		"transactions":        &caps.Transactions,
		"multiple-statements": &caps.MultipleStatements,
		"isolated":            &caps.Isolated,
	}
	for name, into := range flags {
		v, ok := d.Capabilities[name]
		if !ok {
			return adapter.Capabilities{}, fmt.Errorf("says nothing about the %q flag", name)
		}
		*into = v
	}
	for name := range d.Capabilities {
		if _, ok := flags[name]; !ok {
			return adapter.Capabilities{}, fmt.Errorf("declares an engine flag %q this harness does not read", name)
		}
	}
	return caps, nil
}

// Name identifies the adapter.
func (d *Driver) Name() string { return "zu" }

// Version asks the binary what it is.
func (d *Driver) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, d.binary, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Capabilities repeats what the binary declared at New.
//
// Everything absent is absent because zu's loader cannot express it, not
// because the query engine could not evaluate it: zu's own openCypher subset
// passes scenarios over data this adapter has no way to hand it. When zu
// grows an ingest path that takes the rest, those flags flip in the zu repo
// and a block of currently skipped cases starts producing verdicts without a
// line of this adapter changing, which is the point of reading them from
// there.
func (d *Driver) Capabilities() adapter.Capabilities { return d.caps }

// Open starts nothing yet; sessions own their processes.
func (d *Driver) Open(ctx context.Context, workdir string) (adapter.Session, error) {
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, err
	}
	return &session{driver: d, workdir: workdir, path: filepath.Join(workdir, "graph.zu1")}, nil
}

// Close releases driver-level state, of which there is none.
func (d *Driver) Close() error { return nil }

type session struct {
	driver  *Driver
	workdir string
	path    string

	mu   sync.Mutex
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	errs *bytes.Buffer
	// load is the conversion process while one is running. The shell is down
	// for the whole of a load, so without this the sampler would find no
	// process to watch and the ingest row — the one measurement the load
	// exists to produce — would come back empty.
	load *exec.Cmd

	closed bool
}

// Load writes the fixture into a database laid out the way zu's own SQLite
// engine lays one out, asks zu to convert it into a fresh zu1 file, then
// restarts the shell so the new file is the one being served.
//
// The conversion is the only route into zu that carries labels and properties:
// `zu copy` takes a two-column edge list, which would reduce every fixture to
// its topology. Both the wall time reported here and the disk the report
// measures cover the conversion and not the SQLite write, because the SQLite
// file is scaffolding this harness put there and not something zu would pay
// for in use.
func (s *session) Load(ctx context.Context, fx *fixture.Fixture) (adapter.LoadStats, error) {
	if err := s.stopShell(); err != nil {
		return adapter.LoadStats{}, err
	}
	if err := os.RemoveAll(s.path); err != nil {
		return adapter.LoadStats{}, err
	}

	// The staging file goes beside the graph rather than in it, and is removed
	// before the measurement that follows: leaving it in the data directory
	// would put SQLite's bytes into zu's disk figure.
	stage := filepath.Join(s.workdir, "stage.db")
	if err := os.RemoveAll(stage); err != nil {
		return adapter.LoadStats{}, err
	}
	if err := writeFixtureDB(ctx, stage, fx); err != nil {
		return adapter.LoadStats{}, fmt.Errorf("zu: staging fixture %s: %w", fx.Name, err)
	}

	cmd := exec.CommandContext(ctx, s.driver.binary, "convert", stage, s.path)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf

	started := time.Now()
	err := cmd.Start()
	if err == nil {
		// Published while it runs so the sampler, which asks the session for a
		// pid on every tick, measures the conversion rather than the gap where
		// the shell used to be.
		s.mu.Lock()
		s.load = cmd
		s.mu.Unlock()

		err = cmd.Wait()

		s.mu.Lock()
		s.load = nil
		s.mu.Unlock()
	}
	wall := time.Since(started)
	out := buf.Bytes()
	for _, junk := range []string{stage, stage + "-wal", stage + "-shm"} {
		_ = os.RemoveAll(junk)
	}
	if err != nil {
		return adapter.LoadStats{}, fmt.Errorf("zu convert: %w: %s", err, strings.TrimSpace(string(out)))
	}

	stats := adapter.LoadStats{
		Nodes:      len(fx.Nodes),
		Edges:      len(fx.Edges),
		EngineWall: wall,
		Detail:     strings.TrimSpace(string(out)),
	}
	// Asked after the conversion and before the shell comes back up, so it
	// reads a file nothing is writing to, and outside the timed section above,
	// so the ingest figures do not carry it.
	stats.SchemaBytes, stats.AllocUnit = s.storeLayout(ctx)
	if err := s.startShell(); err != nil {
		return stats, err
	}
	return stats, nil
}

// storeLayout asks zu how much of the store it just wrote is fixed by the shape
// of the database rather than by the graph, and how much the store grows by at
// a time.
//
// The harness divides a store size by a graph to get bits per edge, and zu's
// store has a floor of four 256 KiB blocks that exists whether the graph has a
// million edges in it or one. Most of the conformance corpus is smaller than
// that floor, so the run of 2026-08-12 published nine densities for nine stores
// of identical size and 29 360 128 bits per edge for a graph with one edge in
// it. Subtracting a number zu itself computes is the fix; guessing at it from
// an empty store measured once per run is what the harness did instead, and it
// is a proxy for this.
//
// A failure here is not a load failure. The store is written and the fixture is
// loaded; what is missing is a breakdown, and a harness that refused a load
// because it could not have one would trade a measurement for a metric. Zero
// comes back and the density falls through to the empty-store test, which is
// where every other engine already is.
func (s *session) storeLayout(ctx context.Context) (schema, unit int64) {
	cmd := exec.CommandContext(ctx, s.driver.binary, "stat", s.path, "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return 0, 0
	}
	var layout struct {
		BlockSize   int64 `json:"block_size"`
		SchemaBytes int64 `json:"schema_bytes"`
		FreeBytes   int64 `json:"free_bytes"`
	}
	if err := json.Unmarshal(out, &layout); err != nil {
		return 0, 0
	}
	// The free list is neither schema nor graph: it is space the store holds
	// and is not using. It counts as fixed, because charging a graph for blocks
	// a previous graph released would make a density depend on what was loaded
	// before it, and the whole reason the density is computed from the store's
	// size rather than from its growth is to keep run order out of the figure.
	return layout.SchemaBytes + layout.FreeBytes, layout.BlockSize
}

// startShell starts the shell. It takes no context on purpose: the process it
// starts must outlive the load's deadline, which the caller already applied to
// the conversion.
func (s *session) startShell() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startShellLocked()
}

// startShellLocked starts the shell process. The caller holds s.mu.
func (s *session) startShellLocked() error {
	if s.cmd != nil {
		return nil
	}
	// The shell outlives any single statement's context, so it is started
	// against the background: a per-statement deadline must abort the read,
	// not kill the process the next statement needs.
	cmd := exec.Command(s.driver.binary, "shell", s.path, "--format", "jsonl") //nolint:noctx // deliberate: see above.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	s.errs = &bytes.Buffer{}
	cmd.Stderr = s.errs
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("zu shell: %w", err)
	}
	s.cmd, s.in, s.out = cmd, stdin, bufio.NewReaderSize(stdout, 1<<20)
	return nil
}

func (s *session) stopShell() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopLocked()
}

func (s *session) stopLocked() error {
	if s.cmd == nil {
		return nil
	}
	// A quit frame lets the shell flush and exit cleanly, which keeps the
	// file's epoch consistent for the disk measurement that follows.
	if s.in != nil {
		_, _ = io.WriteString(s.in, "{\"op\":\"quit\"}\n")
		_ = s.in.Close()
	}
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
	s.cmd, s.in, s.out = nil, nil, nil
	return nil
}

// frame is the shell's response envelope. Exactly one of the three shapes is
// populated per line.
//
// GQLStatus is present on every successful reply and carries the completion
// condition, 00000 or 00001 when the statement had no projection. Failure is
// present only when the engine raised a condition: a protocol fault, a
// malformed frame or an unknown op, sets Error alone and deliberately claims
// no code. That is the distinction the runner needs, so it is read off the
// presence of the object rather than guessed from the message.
type frame struct {
	GQLStatus string              `json:"gqlstatus"`
	Columns   []string            `json:"columns"`
	Rows      [][]json.RawMessage `json:"rows"`
	Notices   []diagnostic        `json:"notices"`
	Failure   *diagnostic         `json:"failure"`
	Error     string              `json:"error"`
	Bye       bool                `json:"bye"`
	// Text is what the explain frames answer with, and nothing else uses.
	Text string `json:"text"`
}

// diagnostic is one GQLSTATUS record: the standard's code and text in fields
// of their own, and zu's message apart from them, so nothing here has to parse
// prose to grade a condition.
type diagnostic struct {
	GQLStatus string `json:"gqlstatus"`
	Condition string `json:"condition"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
}

// Exec sends one query frame and reads one response line.
func (s *session) Exec(ctx context.Context, stmt string, params map[string]any) (*adapter.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req := map[string]any{"op": "query", "q": stmt}
	if len(params) > 0 {
		req["params"] = params
	}
	line, err := s.roundTrip(ctx, req)
	if err != nil {
		return nil, err
	}
	return decode(line)
}

// Explain asks the shell for the plan without running the statement.
//
// The parameters are dropped rather than forwarded, and the shell's explain
// frame does not accept them: zu compiles on the statement text and the schema
// alone, so there is nothing for a binding to change. An engine whose planner
// reads parameter values would forward them here, which is why the interface
// carries them.
//
// A statement zu cannot compile comes back as a *Failure, the same as it would
// from Exec. The runner drops it, which is right: a case that did not parse has
// no plan, and the report already says that in the outcome column.
func (s *session) Explain(ctx context.Context, stmt string, _ map[string]any) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	line, err := s.roundTrip(ctx, map[string]any{"op": "explain", "q": stmt})
	if err != nil {
		return "", err
	}
	var f frame
	if err := json.Unmarshal(line, &f); err != nil {
		return "", fmt.Errorf("zu: malformed explain response %q: %w", strings.TrimSpace(string(line)), err)
	}
	if f.Error != "" {
		fail := &adapter.Failure{Message: f.Error}
		if f.Failure != nil {
			fail.GQLStatus = f.Failure.GQLStatus
		}
		return "", fail
	}
	return f.Text, nil
}

// roundTrip writes one frame and reads the one line that answers it. The
// caller holds s.mu.
func (s *session) roundTrip(ctx context.Context, req map[string]any) ([]byte, error) {
	if s.cmd == nil {
		if err := s.startShellLocked(); err != nil {
			return nil, err
		}
	}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := s.in.Write(append(line, '\n')); err != nil {
		return nil, s.fatal(fmt.Errorf("zu: write: %w", err))
	}

	type read struct {
		line []byte
		err  error
	}
	ch := make(chan read, 1)
	go func() {
		b, err := s.out.ReadBytes('\n')
		ch <- read{b, err}
	}()

	select {
	case <-ctx.Done():
		// The shell is single-threaded over one pipe, so a statement that
		// outran its deadline has left a response nobody will read. The only
		// safe recovery is to discard the process.
		_ = s.stopLocked()
		return nil, &adapter.Failure{Timeout: true, Fatal: true, Message: ctx.Err().Error()}
	case r := <-ch:
		if r.err != nil {
			return nil, s.fatal(fmt.Errorf("zu: read: %w (stderr: %s)", r.err, strings.TrimSpace(s.errs.String())))
		}
		return r.line, nil
	}
}

func (s *session) fatal(err error) error {
	_ = s.stopLocked()
	return &adapter.Failure{Fatal: true, Message: err.Error()}
}

func decode(line []byte) (*adapter.Result, error) {
	var f frame
	if err := json.Unmarshal(line, &f); err != nil {
		return nil, fmt.Errorf("zu: malformed response %q: %w", strings.TrimSpace(string(line)), err)
	}
	if f.Error != "" {
		// A protocol fault leaves Failure nil and the status empty, which is
		// honest: those are not conditions the standard defines, and the
		// runner falls back to matching the message at the lower confidence
		// the report labels.
		fail := &adapter.Failure{Message: f.Error}
		if f.Failure != nil {
			fail.GQLStatus = f.Failure.GQLStatus
		}
		return nil, fail
	}
	if f.Bye {
		return nil, errors.New("zu: shell closed")
	}
	t := &rows.Table{Columns: f.Columns, Rows: make([][]any, 0, len(f.Rows))}
	var size int64
	for _, r := range f.Rows {
		row := make([]any, len(r))
		for i, raw := range r {
			size += int64(len(raw))
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, fmt.Errorf("zu: malformed cell: %w", err)
			}
			row[i] = rows.Normalize(numeric(raw, v))
		}
		t.Rows = append(t.Rows, row)
	}
	return &adapter.Result{Table: t, Bytes: size, GQLStatus: f.GQLStatus}, nil
}

// numeric recovers integer identity that encoding/json throws away. zu writes
// node ids and counts as bare integers, and a comparison against an expected
// 4 must not fail because the driver widened it to 4.0 and the case asked for
// strict types.
func numeric(raw json.RawMessage, v any) any {
	f, ok := v.(float64)
	if !ok {
		return v
	}
	s := string(bytes.TrimSpace(raw))
	if strings.ContainsAny(s, ".eE") {
		return f
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	return f
}

// Reset drops the graph. zu keeps one file, so removing it and letting the
// next Load rebuild is both the simplest reset and the most complete one.
func (s *session) Reset(ctx context.Context) error {
	if err := s.stopShell(); err != nil {
		return err
	}
	return os.RemoveAll(s.path)
}

// PID is whichever process is currently doing the engine's work: the loader
// during a load, the shell the rest of the time, and nothing in between.
func (s *session) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range [2]*exec.Cmd{s.load, s.cmd} {
		if c != nil && c.Process != nil {
			return c.Process.Pid
		}
	}
	return 0
}

// DataDir is the directory holding the graph file.
func (s *session) DataDir() string { return s.workdir }

// Close stops the shell.
func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.stopLocked()
}
