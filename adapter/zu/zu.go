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
// What the route cannot carry is declared in Capabilities and worked around
// nowhere: one label per node, no edge properties, no doubles, booleans,
// nulls, lists or temporals, and no edge between two different labels, since
// zu binds a rel table to a single node table. Each of those is a limit of
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
}

// New builds a zu driver. The binary defaults to whatever `zu` resolves to on
// PATH; point Binary at a build tree's target/release/zu to measure a change.
func New(opts adapter.Options) (adapter.Driver, error) {
	bin := opts.Binary
	if bin == "" {
		bin = "zu"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("zu: cannot find binary %q: %w", bin, err)
	}
	return &Driver{binary: resolved}, nil
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

// Capabilities declares what zu's storage can hold today.
//
// Everything absent here is absent because the loader cannot express it, not
// because the query engine could not evaluate it — zu's own openCypher subset
// passes scenarios over data this adapter has no way to hand it. When zu grows
// an ingest path that takes the rest, these flags flip and a block of
// currently skipped cases starts producing verdicts without a line of this
// adapter changing.
func (d *Driver) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Data: map[fixture.Capability]bool{
			// A node table is a label and a rel table is an edge type, so both
			// arrive, along with several rel tables in one graph.
			fixture.CapLabels:            true,
			fixture.CapNodeProperties:    true,
			fixture.CapEdgeTypes:         true,
			fixture.CapMultipleEdgeTypes: true,
			// The converter reads a rel table's endpoints straight through, so
			// an edge to itself and a second edge over the same ordered pair
			// both survive.
			fixture.CapSelfLoops:     true,
			fixture.CapParallelEdges: true,

			// A node lives in exactly one node table, and a rel table binds to
			// one node table at both ends, so a second label — on a node or in
			// a graph — has nowhere to go.
			fixture.CapMultiLabel:         false,
			fixture.CapMultipleNodeLabels: false,
			// The converter carries a rel's endpoints and drops its columns.
			fixture.CapEdgeProperties: false,
			// zu1 property columns are dense and uniformly integer or string:
			// the loader refuses a null, a double, a boolean and anything
			// structured, by name, at convert time.
			fixture.CapNullProperties: false,
			fixture.CapFloatValues:    false,
			fixture.CapBooleanValues:  false,
			fixture.CapListValues:     false,
			fixture.CapTemporalValues: false,
			// Every rel table is directed.
			fixture.CapUndirectedEdges: false,
		},
		GQLStatus:          true,
		Parameters:         true,
		Transactions:       false,
		MultipleStatements: true,
		Isolated:           true,
		Notes: []string{
			"driven through `zu shell --format jsonl`, one long-lived process per session",
			"loaded through `zu convert`, which reads a SQLite database in zu's schema; " +
				"`zu copy` was not used because an edge list carries no labels or properties",
			"the shell evaluates a read-only subset — MATCH, WHERE, CALL, UNWIND, WITH, RETURN — " +
				"so every case that writes is answered with a parse error rather than a skip",
			"results and engine-raised failures carry GQLSTATUS; a protocol fault, a malformed " +
				"frame or an unknown op, reports no code and is scored on the message",
		},
	}
}

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
	if err := s.startShell(); err != nil {
		return stats, err
	}
	return stats, nil
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
	if s.cmd == nil {
		if err := s.startShellLocked(); err != nil {
			return nil, err
		}
	}

	req := map[string]any{"op": "query", "q": stmt}
	if len(params) > 0 {
		req["params"] = params
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
		return decode(r.line)
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
