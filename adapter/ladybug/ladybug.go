// Package ladybug drives LadybugDB through its command-line shell.
//
// LadybugDB is the MIT-licensed continuation of Kùzu, whose team was hired
// away and whose repository was archived in October 2025. It matters to this
// harness for a reason neither Neo4j nor zu supplies: it is an embedded,
// columnar, disk-based engine with a Cypher front end, so it sits between the
// other two on every axis the report measures — richer than an edge list,
// cheaper than a server.
//
// Two facts about the engine shape this adapter.
//
// The first is that Ladybug's default data model is a typed schema: a node
// label is a table, declared with CREATE NODE TABLE and a primary key, and a
// node belongs to exactly one of them. Under that model `MATCH (a:A:B)` means
// the union of two tables rather than a node carrying two labels, and a GQL
// fixture with a genuinely multi-labelled node cannot be represented at all.
// Ladybug also offers `CREATE GRAPH <name> ANY`, a schemaless graph whose
// nodes carry a label list and a dynamic property map. That is the model this
// adapter uses, because it is the one that can hold a GQL property graph
// without the adapter inventing a schema the standard never asked for.
//
// The second is that the shell has no parameter binding — there is no :param
// command and no placeholder syntax. Rather than inline a case's parameters
// into its text, which would be exactly the rewriting the adapter contract
// forbids, Capabilities reports Parameters as false and the runner skips
// parameterised cases with the reason named.
package ladybug

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/rows"
)

func init() { adapter.Register("ladybug", New) }

const (
	// graphName is the ANY graph every session works inside. Ladybug reserves
	// the name "main" for the schema-typed default graph, so the harness needs
	// one of its own.
	graphName = "gqlcompat"
	// dbExt is the suffix on the database file. The engine puts each graph in
	// a sibling file that keeps it, so graph-000.lbug brings along
	// graph-000.gqlcompat.lbug, and removeStore relies on that shape.
	dbExt = ".lbug"
	// keyProp carries the fixture's node key. An ANY graph has no primary key
	// and no index, so this is how the edge phase and the expectations find a
	// node again; convertValue strips it back out of every result.
	keyProp = "_key"
	// nodeBatch and edgeBatch bound how much statement text is built at once.
	// They trade one round trip against one very large parse.
	nodeBatch = 512
	edgeBatch = 20000
)

// Driver runs Ladybug out of the `lbug` binary.
type Driver struct {
	binary string
}

// New builds a Ladybug driver. The binary defaults to `lbug` on PATH, which is
// the name the shell target installs under.
func New(opts adapter.Options) (adapter.Driver, error) {
	bin := opts.Binary
	if bin == "" {
		bin = "lbug"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("ladybug: cannot find binary %q: %w", bin, err)
	}
	return &Driver{binary: resolved}, nil
}

// Name identifies the adapter.
func (d *Driver) Name() string { return "ladybug" }

// Version asks the binary what it is.
func (d *Driver) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, d.binary, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Capabilities declares what an ANY graph can hold.
//
// The absences are the shell's, not the engine's. Parameters are missing
// because the CLI has no binding syntax; a future adapter built on the Go
// bindings would flip that flag and nothing else here would change.
func (d *Driver) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Data: map[fixture.Capability]bool{
			fixture.CapLabels:                 true,
			fixture.CapMultiLabel:             true,
			fixture.CapNodeProperties:         true,
			fixture.CapEdgeProperties:         true,
			fixture.CapEdgeTypes:              true,
			fixture.CapMultipleEdgeTypes:      true,
			fixture.CapMultipleNodeLabels:     true,
			fixture.CapTemporalValues:         true,
			fixture.CapListValues:             true,
			fixture.CapSelfLoops:              true,
			fixture.CapParallelEdges:          true,
			fixture.CapParallelEdgeProperties: true,
			// An ANY graph's property map holds whatever value it is given, so
			// these two are the same yes as the rest. They were missing from
			// this map, which is not the same as being false, and the runner
			// now refuses a map with a hole in it rather than print the
			// omission as a limitation of the engine.
			fixture.CapFloatValues:   true,
			fixture.CapBooleanValues: true,
			// An ANY graph's property map has no slot for a property whose
			// value is null; writing one is indistinguishable from omitting it.
			fixture.CapNullProperties: false,
			// Cypher relationships are directed and Ladybug stores them that
			// way; the undirected pattern matches both directions but there is
			// no undirected edge to store.
			fixture.CapUndirectedEdges: false,
		},
		GQLStatus:  false,
		Parameters: false,
		// START TRANSACTION / COMMIT / ROLLBACK exist in Ladybug's Cypher, but
		// under the GQL spelling the standard requires they are syntax errors,
		// which is a conformance result rather than a capability.
		Transactions:       false,
		MultipleStatements: true,
		Isolated:           true,
		Notes: []string{
			"driven through the `lbug` shell in jsonlines mode, one short-lived process per exchange, because the shell block-buffers a pipe and demands a terminal handshake under a pty",
			"a process launch is about 57ms and is a floor under every latency reported here; see the report's measurement floor",
			"data is held in a `CREATE GRAPH " + graphName + " ANY` schemaless graph, so nodes keep real label sets and dynamic properties",
			"the shell has no parameter binding, so parameterised cases are skipped rather than inlined",
			"errors carry no GQLSTATUS; condition cases fall back to message matching",
			"the shell's JSON writer cannot print a STRING read out of an ANY graph and dies trying, so those cases are errors and not failures; csv mode prints the same value correctly, which is how the harness knows it is the writer",
			"DROP GRAPH is refused by the engine's own file removal check, so a reset is a new database file rather than a dropped graph",
			"a keyed edge ingest is a scan per edge, because an ANY graph has no index the harness can put a key in; the hundred-thousand-node fixture takes minutes",
		},
	}
}

// Open prepares a session directory. The process starts on first use.
func (d *Driver) Open(ctx context.Context, workdir string) (adapter.Session, error) {
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, err
	}
	return &session{driver: d, workdir: workdir, path: dbPath(workdir, 0)}, nil
}

// Close releases driver-level state, of which there is none.
func (d *Driver) Close() error { return nil }

type session struct {
	driver  *Driver
	workdir string
	path    string

	mu     sync.Mutex
	serial int
	closed bool
	// gen counts resets. Each one moves the session to a database file that
	// has never existed, because that is the only reset this engine honours.
	gen int
	// created records that the ANY graph exists, so the prelude of every
	// later exchange can be two statements rather than three.
	created bool
	// pid is the shell process currently running, or 0. It is atomic rather
	// than guarded by mu because the sampler reads it from another goroutine
	// while Exec holds the lock for the whole call.
	pid atomic.Int64
}

// The shell block-buffers stdout whenever it is not writing to a terminal, so
// a statement sent down a pipe produces no output at all until the buffer
// fills or the process exits, and under a pty it runs a line editor that
// answers cursor-position queries before it will read anything. Neither is a
// transport, so this adapter does not keep a REPL alive. Each exchange is one
// short-lived process reading a script on stdin, and the database on disk is
// what carries state from one to the next.
//
// The cost of that is a process launch per exchange, about 57 ms on the
// machine this was written on, and it is a floor under every latency this
// adapter reports. It is not hidden: Capabilities names it, and the report's
// measurement floor section is where a reader is meant to find it.

// exchange is one script sent to one process: a list of statements, each of
// which gets its own reply.
func (s *session) runLocked(ctx context.Context, stmts []string) ([]*reply, error) {
	if len(stmts) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	markers := make([]string, len(stmts))
	for i, stmt := range stmts {
		s.serial++
		markers[i] = fmt.Sprintf("gqlcompat-eos-%d", s.serial)
		buf.WriteString(terminate(stmt))
		buf.WriteString("\nRETURN '")
		buf.WriteString(markers[i])
		buf.WriteString("' AS __gqlcompat;\n")
	}

	cmd := exec.CommandContext(ctx, s.driver.binary, s.path,
		"-m", "jsonlines", "--no_stats", "--no_progress_bar")
	cmd.Stdin = bytes.NewReader(buf.Bytes())
	var out, errs bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errs
	if err := cmd.Start(); err != nil {
		return nil, &adapter.Failure{Fatal: true, Message: "ladybug: " + err.Error()}
	}
	if p := cmd.Process; p != nil {
		s.pid.Store(int64(p.Pid))
	}
	err := cmd.Wait()
	s.pid.Store(0)
	if err != nil {
		// A cancelled context is the case's timeout, not a broken engine. The
		// process is already gone and the database is on disk, so unlike the
		// REPL this replaced there is nothing to discard and nothing fatal.
		if ctx.Err() != nil {
			return nil, &adapter.Failure{Timeout: true, Message: ctx.Err().Error()}
		}
		// The shell exits non-zero on a statement error as well as on a real
		// failure, and the difference is in the output rather than the code,
		// so a non-zero exit with parseable output is not by itself an error.
		if out.Len() == 0 {
			return nil, &adapter.Failure{Fatal: true, Message: fmt.Sprintf(
				"ladybug: %v (stderr: %s)", err, strings.TrimSpace(errs.String()))}
		}
	}
	// The shell's own crashes go to stderr while its rows go to stdout, so the
	// two have to be read together: the output alone shows a script that
	// stopped, and only stderr says why it stopped.
	return split(bufio.NewReaderSize(bytes.NewReader(out.Bytes()), 1<<20), markers, errs.String())
}

// prelude is what every exchange runs before the caller's statements, to put
// the process inside the ANY graph. It is not free and it is not optional: a
// fresh process opens the schema-typed default graph, and a case that ran
// there would be measured against a model the standard never asked for.
func (s *session) preludeLocked() []string {
	if s.created {
		return []string{"USE GRAPH " + graphName}
	}
	return []string{"CREATE GRAPH " + graphName + " ANY", "USE GRAPH " + graphName}
}

// roundTripLocked runs one statement inside the ANY graph and returns its
// reply. The caller holds s.mu.
func (s *session) roundTripLocked(ctx context.Context, stmt string) (*reply, error) {
	reps, err := s.batchLocked(ctx, []string{stmt})
	if err != nil {
		return nil, err
	}
	return reps[0], nil
}

// batchLocked runs several statements in one process, which is what makes a
// fixture load cost one launch rather than one per batch.
func (s *session) batchLocked(ctx context.Context, stmts []string) ([]*reply, error) {
	pre := s.preludeLocked()
	reps, err := s.runLocked(ctx, append(append([]string{}, pre...), stmts...))
	if err != nil {
		return nil, err
	}
	if len(reps) != len(pre)+len(stmts) {
		return nil, &adapter.Failure{Transport: true, Message: fmt.Sprintf(
			"the shell answered %d of %d statements", len(reps), len(pre)+len(stmts))}
	}
	for i, rep := range reps[:len(pre)] {
		if rep.errText == "" {
			continue
		}
		// CREATE GRAPH is tolerated when the graph is already there, which
		// happens whenever a database outlives the session that made it.
		if i == 0 && !s.created && alreadyExists(rep.errText) {
			continue
		}
		return nil, &adapter.Failure{Fatal: true, Message: fmt.Sprintf(
			"ladybug: %s: %s", pre[i], rep.errText)}
	}
	s.created = true
	return reps[len(pre):], nil
}

func alreadyExists(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "already exists") || strings.Contains(m, "duplicate")
}

// split turns one process's whole output into one reply per marker.
//
// The shell prints rows and errors into a single stream with no framing, so
// the markers a script interleaves between its statements are what put the
// boundaries back. A statement that errors does not stop the script, which is
// why a batch can be read as a list of independent answers.
func split(r *bufio.Reader, markers []string, stderr string) ([]*reply, error) {
	reps := make([]*reply, 0, len(markers))
	cur := &reply{}
	var errLines []string
	next := 0
	truncated := func() error {
		return truncatedOutput(next, len(markers), append(errLines, strings.TrimSpace(stderr)))
	}
	for next < len(markers) {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			return nil, truncated()
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if err != nil {
				return nil, truncated()
			}
			continue
		}
		if bytes.Contains(trimmed, []byte(markers[next])) {
			cur.errText = strings.TrimSpace(strings.Join(errLines, "\n"))
			reps = append(reps, cur)
			cur, errLines, next = &reply{}, nil, next+1
			continue
		}
		if trimmed[0] == '{' {
			obj, derr := decodeObject(trimmed)
			if derr != nil {
				// A line that starts like JSON and is not JSON is the shell
				// saying something the adapter does not model; keeping it as
				// text is better than dropping it.
				errLines = append(errLines, string(trimmed))
				continue
			}
			cur.lines = append(cur.lines, obj)
			continue
		}
		errLines = append(errLines, strings.TrimPrefix(string(trimmed), "Error: "))
	}
	return reps, nil
}

// serializerCrash is what the shell prints, and then dies of, when its JSON
// writer is handed a value it cannot write.
const serializerCrash = "unexpected character, expected a valid root value"

// truncatedOutput explains a script that stopped answering.
//
// Ladybug 0.19.1 cannot print a STRING that came out of an ANY graph in either
// JSON mode. The engine computes the row, the writer then tries to parse the
// value as JSON, fails on the first character and takes the process down, so
// the rest of the script is never run. Every other output mode prints the same
// value correctly, which is what makes this the shell rather than the engine:
// `MATCH (n:T) RETURN n.s` answers "x" in csv mode and kills the process in
// jsonlines mode, from the same store on the same statement.
//
// The distinction is not pedantic. Charged to the engine it reads as a query
// this database cannot answer, which is false. Marked as transport it becomes
// an error, stays out of the pass rate, and is counted where it belongs, in a
// row of the report that says the harness could not read the answer.
func truncatedOutput(done, want int, errLines []string) error {
	if strings.Contains(strings.Join(errLines, "\n"), serializerCrash) {
		return &adapter.Failure{Transport: true, Fatal: false, Message: "the shell's JSON writer " +
			"died on a value the engine computed: a STRING read out of an ANY graph cannot be " +
			"printed in jsonlines mode, though csv mode prints it correctly"}
	}
	return &adapter.Failure{Transport: true, Message: fmt.Sprintf(
		"the shell stopped answering after %d of %d statements: %s",
		done, want, strings.TrimSpace(strings.Join(errLines, "\n")))}
}

// reply is one statement's output, bounded by a sentinel.
type reply struct {
	// lines are the JSON objects the statement produced, one per row, with
	// their column order preserved.
	lines []orderedObject
	// errText is whatever the shell printed that was not a row. The shell
	// writes "Error: <message>" to stdout, in the same stream as results.
	errText string
}

// terminate appends the semicolon the REPL needs to consider a statement
// finished, without touching one that already has it.
func terminate(stmt string) string {
	t := strings.TrimRight(stmt, " \t\r\n")
	if strings.HasSuffix(t, ";") {
		return t
	}
	return t + ";"
}

// Exec runs one statement.
func (s *session) Exec(ctx context.Context, stmt string, params map[string]any) (*adapter.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(params) > 0 {
		return nil, fmt.Errorf("ladybug: %w: the shell has no parameter binding", adapter.ErrUnsupported)
	}
	rep, err := s.roundTripLocked(ctx, stmt)
	if err != nil {
		return nil, err
	}
	if rep.errText != "" {
		return nil, &adapter.Failure{Message: rep.errText}
	}
	return toResult(rep), nil
}

func toResult(rep *reply) *adapter.Result {
	t := &rows.Table{}
	var size int64
	for i, obj := range rep.lines {
		if i == 0 {
			t.Columns = obj.keys
		}
		row := make([]any, len(obj.keys))
		for j, k := range obj.keys {
			raw := obj.values[k]
			size += int64(len(raw))
			row[j] = convertRaw(raw)
		}
		t.Rows = append(t.Rows, row)
	}
	return &adapter.Result{Table: t, Bytes: size}
}

// orderedObject is a JSON object that remembers its key order, which a Go map
// does not and which a column list needs.
type orderedObject struct {
	keys   []string
	values map[string]json.RawMessage
}

func decodeObject(b []byte) (orderedObject, error) {
	obj := orderedObject{values: map[string]json.RawMessage{}}
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return obj, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return obj, errors.New("not an object")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return obj, err
		}
		key, ok := kt.(string)
		if !ok {
			return obj, errors.New("non-string key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return obj, err
		}
		obj.keys = append(obj.keys, key)
		obj.values[key] = raw
	}
	return obj, nil
}

// convertRaw decodes one cell, recovering integer identity that encoding/json
// would widen to a float and recognising the shapes Ladybug uses for nodes and
// relationships.
func convertRaw(raw json.RawMessage) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	if f, ok := v.(float64); ok {
		s := strings.TrimSpace(string(raw))
		if !strings.ContainsAny(s, ".eE") {
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				return i
			}
		}
		return f
	}
	return convertValue(v)
}

// internalKeys are the fields Ladybug attaches to an entity and the one this
// adapter attaches itself. None of them is part of the graph a case asserts
// on, and all of them are removed before comparison.
var internalKeys = map[string]bool{
	"_id": true, "_ID": true,
	"_label": true, "_LABEL": true,
	"_labels": true, "_LABELS": true,
	"_src": true, "_SRC": true,
	"_dst": true, "_DST": true,
	keyProp: true,
}

// convertValue maps a decoded JSON value onto the harness's neutral types.
//
// Ladybug's JSON printer renders a node or a relationship as a plain object
// with its internal fields alongside its properties, so the shape has to be
// recognised rather than typed. The recognition is deliberately narrow: an
// object is only treated as an entity when it carries an internal identifier,
// and a user map that happens to hold a key called "name" stays a map.
func convertValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		labels, isNode := entityLabels(x)
		typ, isEdge := entityType(x)
		switch {
		case isNode:
			return rows.Node{Labels: labels, Props: userProps(x)}
		case isEdge:
			return rows.Edge{Type: typ, Props: userProps(x)}
		}
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = convertValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = convertValue(x[i])
		}
		return out
	}
	return rows.Normalize(v)
}

func entityLabels(m map[string]any) ([]string, bool) {
	if _, hasID := lookup(m, "_id", "_ID"); !hasID {
		return nil, false
	}
	if v, ok := lookup(m, "_labels", "_LABELS"); ok {
		var out []string
		if list, ok := v.([]any); ok {
			for _, l := range list {
				out = append(out, fmt.Sprint(l))
			}
			return out, true
		}
	}
	if v, ok := lookup(m, "_label", "_LABEL"); ok {
		if _, isEdge := lookup(m, "_src", "_SRC"); isEdge {
			return nil, false
		}
		return []string{fmt.Sprint(v)}, true
	}
	return nil, false
}

func entityType(m map[string]any) (string, bool) {
	if _, ok := lookup(m, "_src", "_SRC"); !ok {
		return "", false
	}
	if v, ok := lookup(m, "_label", "_LABEL"); ok {
		return fmt.Sprint(v), true
	}
	return "", true
}

func lookup(m map[string]any, names ...string) (any, bool) {
	for _, n := range names {
		if v, ok := m[n]; ok {
			return v, true
		}
	}
	return nil, false
}

func userProps(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if internalKeys[k] {
			continue
		}
		out[k] = convertValue(v)
	}
	return out
}

// Load rebuilds the graph from a fixture.
//
// Nodes go in as batched CREATE patterns and edges as one UNWIND per type,
// which is what keeps the ingest measurement about the engine rather than
// about the REPL's line rate. An ANY graph has no index on the harness's key
// property, so the edge phase is a scan per batch; batching the whole type
// group into one statement makes that one scan instead of one per edge.
func (s *session) Load(ctx context.Context, fx *fixture.Fixture) (adapter.LoadStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.resetLocked(ctx); err != nil {
		return adapter.LoadStats{}, err
	}
	// The whole load is one script in one process. Under the REPL this
	// replaced the batching existed to save round trips; now it saves process
	// launches, and the difference matters more: a hundred-thousand-node
	// fixture is 196 node statements, which would otherwise be 196 launches
	// and eleven seconds of nothing but opening a database.
	var stmts []string
	var phases []string
	for chunk := range slices.Chunk(fx.Nodes, nodeBatch) {
		var b strings.Builder
		b.WriteString("CREATE ")
		for i, n := range chunk {
			if i > 0 {
				b.WriteString(", ")
			}
			writeNodePattern(&b, n)
		}
		stmts = append(stmts, b.String())
		phases = append(phases, "node")
	}

	byType := map[string][]fixture.Edge{}
	for _, e := range fx.Edges {
		byType[e.Type] = append(byType[e.Type], e)
	}
	types := slices.Sorted(maps.Keys(byType))
	for _, typ := range types {
		for chunk := range slices.Chunk(byType[typ], edgeBatch) {
			stmt, err := edgeStatement(typ, chunk)
			if err != nil {
				return adapter.LoadStats{}, err
			}
			stmts = append(stmts, stmt)
			phases = append(phases, "edge")
		}
	}

	started := time.Now()
	if len(stmts) > 0 {
		reps, err := s.batchLocked(ctx, stmts)
		if err != nil {
			return adapter.LoadStats{}, err
		}
		for i, rep := range reps {
			if rep.errText != "" {
				return adapter.LoadStats{}, fmt.Errorf("ladybug: %s load: %s", phases[i], rep.errText)
			}
		}
	}

	return adapter.LoadStats{
		Nodes:      len(fx.Nodes),
		Edges:      len(fx.Edges),
		EngineWall: time.Since(started),
		Detail:     fmt.Sprintf("%d node batches, %d edge types", (len(fx.Nodes)+nodeBatch-1)/nodeBatch, len(types)),
	}, nil
}

func writeNodePattern(b *strings.Builder, n fixture.Node) {
	b.WriteString("(")
	for _, l := range n.Labels {
		b.WriteString(":")
		writeIdent(b, l)
	}
	b.WriteString(" {")
	writeIdent(b, keyProp)
	b.WriteString(": ")
	writeLiteral(b, n.Key)
	for _, k := range slices.Sorted(maps.Keys(n.Props)) {
		b.WriteString(", ")
		writeIdent(b, k)
		b.WriteString(": ")
		writeLiteral(b, n.Props[k])
	}
	b.WriteString("})")
}

// edgeStatement builds one UNWIND for a group of edges of the same type.
//
// Edges carrying properties are written one pattern per edge instead, because
// an ANY graph's dynamic property map cannot be assigned wholesale from a
// struct and spelling each property out is the only way to set it.
func edgeStatement(typ string, edges []fixture.Edge) (string, error) {
	if typ == "" {
		typ = "_EDGE"
	}
	var b strings.Builder
	if !anyProps(edges) {
		b.WriteString("UNWIND [")
		for i, e := range edges {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("{f: ")
			writeLiteral(&b, e.From)
			b.WriteString(", t: ")
			writeLiteral(&b, e.To)
			b.WriteString("}")
		}
		b.WriteString("] AS r MATCH (a {")
		writeIdent(&b, keyProp)
		b.WriteString(": r.f}), (z {")
		writeIdent(&b, keyProp)
		b.WriteString(": r.t}) CREATE (a)-[:")
		writeIdent(&b, typ)
		b.WriteString("]->(z)")
		return b.String(), nil
	}
	for i, e := range edges {
		if i > 0 {
			b.WriteString(";\n")
		}
		b.WriteString("MATCH (a {")
		writeIdent(&b, keyProp)
		b.WriteString(": ")
		writeLiteral(&b, e.From)
		b.WriteString("}), (z {")
		writeIdent(&b, keyProp)
		b.WriteString(": ")
		writeLiteral(&b, e.To)
		b.WriteString("}) CREATE (a)-[:")
		writeIdent(&b, typ)
		b.WriteString(" {")
		for j, k := range slices.Sorted(maps.Keys(e.Props)) {
			if j > 0 {
				b.WriteString(", ")
			}
			writeIdent(&b, k)
			b.WriteString(": ")
			writeLiteral(&b, e.Props[k])
		}
		b.WriteString("}]->(z)")
	}
	return b.String(), nil
}

func anyProps(edges []fixture.Edge) bool {
	for _, e := range edges {
		if len(e.Props) > 0 {
			return true
		}
	}
	return false
}

// writeIdent emits a backtick-quoted identifier, which is how Cypher carries a
// label or property name that is not a bare word.
func writeIdent(b *strings.Builder, name string) {
	b.WriteString("`")
	b.WriteString(strings.ReplaceAll(name, "`", "``"))
	b.WriteString("`")
}

// writeLiteral emits a value as Cypher source. The shell has no parameter
// binding, so a fixture's data has to survive as text; this is the only place
// in the adapter that builds a statement out of values, and it is not used for
// anything a case wrote.
func writeLiteral(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("NULL")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		b.WriteString(quote(x))
	case int:
		b.WriteString(strconv.Itoa(x))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case uint64:
		b.WriteString(strconv.FormatUint(x, 10))
	case float32:
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	case float64:
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case []any:
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeLiteral(b, e)
		}
		b.WriteString("]")
	case map[string]any:
		b.WriteString("{")
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writeIdent(b, k)
			b.WriteString(": ")
			writeLiteral(b, x[k])
		}
		b.WriteString("}")
	default:
		b.WriteString(quote(fmt.Sprint(x)))
	}
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\'':
			b.WriteString(`\'`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// Reset throws the database away and starts a new one.
//
// The obvious reset is DROP GRAPH, and it does not work. Ladybug 0.19.1 gets
// as far as deleting the graph's file and then refuses itself, with "Path
// /tmp/.../graph.gqlcompat.lbug is not within the allowed list of files to be
// removed", and what is left behind is a catalog and a disk that disagree.
// The next process sometimes finds the graph with every row still in it and
// sometimes finds no graph of that name at all. Neither is a reset, and the
// failure is silent in the only way that matters: a run that trusted it
// loaded the social fixture on top of itself and read four KNOWS edges where
// the case expected two, which reads in a report as an engine that cannot
// count rather than a harness that cannot clean up.
//
// So a reset is a new database file. Nothing in a catalog survives a path
// that has never existed, and the old files are removed so the disk figures
// stay about the fixture rather than about every fixture before it. The cost
// is a create and a delete, both of which disappear under the process launch
// that carries them.
func (s *session) Reset(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resetLocked(ctx)
}

func (s *session) resetLocked(ctx context.Context) error {
	old := s.path
	s.gen++
	s.path = dbPath(s.workdir, s.gen)
	s.created = false
	// The new database is opened here rather than left to the first statement
	// of the case, so that creating a store is not billed to a query.
	if _, err := s.batchLocked(ctx, []string{"RETURN 1 AS __gqlcompat_reset"}); err != nil {
		return err
	}
	return removeStore(old)
}

// dbPath names generation n of the session's database.
func dbPath(workdir string, gen int) string {
	return filepath.Join(workdir, fmt.Sprintf("graph-%03d%s", gen, dbExt))
}

// removeStore deletes a database and everything the engine put beside it. A
// graph lives in its own file named after the database and itself, so
// graph-000.lbug is accompanied by graph-000.gqlcompat.lbug, and a write-ahead
// file can outlive the process that wrote it.
func removeStore(path string) error {
	matches, err := filepath.Glob(strings.TrimSuffix(path, dbExt) + "*")
	if err != nil {
		return err
	}
	for _, m := range matches {
		if err := os.RemoveAll(m); err != nil {
			return err
		}
	}
	return nil
}

// PID is the shell process the sampler watches, or 0 between exchanges.
//
// Unlike a server adapter this one does have a process to point at, but it is
// a different process for every statement and it exists only while that
// statement runs. What the sampler sees is therefore the cost of opening the
// database, running the statement and closing it again, which is the honest
// shape of an embedded engine driven this way and not a resident set anyone
// should read as steady state.
func (s *session) PID() int { return int(s.pid.Load()) }

// DataDir is the directory holding the database.
func (s *session) DataDir() string { return s.workdir }

// Close marks the session finished. There is no process to stop: every
// exchange already waited for its own.
func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
