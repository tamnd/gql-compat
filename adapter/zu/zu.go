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
// The adapter's honest limitation is ingest. As of this writing zu's only
// bulk loader takes a two-column edge list, so the engine can hold topology
// and integer node keys and nothing else. That is declared in Capabilities
// rather than worked around, and every case needing labels or properties is
// skipped with the missing capability named. The skip count is the finding.
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
// Everything absent here is absent because the bulk loader cannot express it,
// not because the query engine could not evaluate it. When zu grows a loader
// that takes labels and properties, these flags flip and a large block of
// currently skipped cases starts producing verdicts without a line of this
// adapter changing.
func (d *Driver) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Data: map[fixture.Capability]bool{
			// An edge list gives every node one implicit label and every edge
			// one implicit type, and preserves self-loops and parallel pairs
			// as written.
			fixture.CapSelfLoops:     true,
			fixture.CapParallelEdges: true,
		},
		GQLStatus:          false,
		Parameters:         true,
		Transactions:       false,
		MultipleStatements: true,
		Isolated:           true,
		Notes: []string{
			"driven through `zu shell --format jsonl`, one long-lived process per session",
			"bulk load is an edge list only, so labels, properties, and typed edges are unavailable",
			"errors carry no GQLSTATUS; condition cases fall back to message matching",
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

	closed bool
}

// Load writes the fixture as an edge list and asks zu to copy it into a fresh
// file, then restarts the shell so the new file is the one being served.
func (s *session) Load(ctx context.Context, fx *fixture.Fixture) (adapter.LoadStats, error) {
	if err := s.stopShell(); err != nil {
		return adapter.LoadStats{}, err
	}
	if err := os.RemoveAll(s.path); err != nil {
		return adapter.LoadStats{}, err
	}

	// zu identifies nodes by unsigned integer. Fixture keys are strings, and
	// the corpus writes them as small decimal numbers precisely so that the
	// identity a case asserts on survives into an engine with no property
	// store. A key that is not a number is a fixture this engine cannot hold,
	// and saying so is better than inventing a mapping the expectations would
	// not know about.
	edgesPath := filepath.Join(s.workdir, "edges.txt")
	f, err := os.Create(edgesPath)
	if err != nil {
		return adapter.LoadStats{}, err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	for i, e := range fx.Edges {
		from, err1 := strconv.ParseUint(e.From, 10, 64)
		to, err2 := strconv.ParseUint(e.To, 10, 64)
		if err1 != nil || err2 != nil {
			_ = f.Close()
			return adapter.LoadStats{}, fmt.Errorf(
				"zu: edge %d names non-numeric keys (%q -> %q); this engine has no property store to hold string keys",
				i, e.From, e.To)
		}
		fmt.Fprintf(w, "%d %d\n", from, to)
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return adapter.LoadStats{}, err
	}
	if err := f.Close(); err != nil {
		return adapter.LoadStats{}, err
	}

	// zu's loader derives the node count from the highest endpoint it sees,
	// so a node whose key exceeds every endpoint simply does not arrive. That
	// would make MATCH (n) RETURN count(n) disagree with the fixture and the
	// case would be scored a conformance failure for a loader limitation.
	// Refusing the fixture outright puts the finding where it belongs.
	if n := isolatedTail(fx); n >= 0 {
		return adapter.LoadStats{}, fmt.Errorf(
			"zu: fixture %s has node %q above every edge endpoint; an edge-list load cannot carry it",
			fx.Name, fx.Nodes[n].Key)
	}

	started := time.Now()
	cmd := exec.CommandContext(ctx, s.driver.binary, "copy", edgesPath, s.path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return adapter.LoadStats{}, fmt.Errorf("zu copy: %w: %s", err, strings.TrimSpace(string(out)))
	}
	stats := adapter.LoadStats{
		Nodes:      len(fx.Nodes),
		Edges:      len(fx.Edges),
		EngineWall: time.Since(started),
		Detail:     strings.TrimSpace(string(out)),
	}
	if err := s.startShell(ctx); err != nil {
		return stats, err
	}
	return stats, nil
}

// isolatedTail returns the index of the first node whose numeric key is above
// every edge endpoint, or -1 when every node will survive an edge-list load.
func isolatedTail(fx *fixture.Fixture) int {
	var highest uint64
	seen := false
	for _, e := range fx.Edges {
		for _, k := range [2]string{e.From, e.To} {
			if v, err := strconv.ParseUint(k, 10, 64); err == nil {
				if !seen || v > highest {
					highest, seen = v, true
				}
			}
		}
	}
	for i, n := range fx.Nodes {
		v, err := strconv.ParseUint(n.Key, 10, 64)
		if err != nil {
			continue
		}
		if !seen || v > highest {
			return i
		}
	}
	return -1
}

func (s *session) startShell(ctx context.Context) error {
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
type frame struct {
	Columns []string            `json:"columns"`
	Rows    [][]json.RawMessage `json:"rows"`
	Error   string              `json:"error"`
	Bye     bool                `json:"bye"`
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
		// zu reports no GQLSTATUS, so the status field stays empty and the
		// runner scores the case on the message alone, at the lower
		// confidence the report labels.
		return nil, &adapter.Failure{Message: f.Error}
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
	return &adapter.Result{Table: t, Bytes: size}, nil
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

// PID is the shell process the sampler watches.
func (s *session) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
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
