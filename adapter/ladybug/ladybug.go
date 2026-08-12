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
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/rows"
)

func init() { adapter.Register("ladybug", New) }

const (
	// graphName is the ANY graph every session works inside. Ladybug reserves
	// the name "main" for the schema-typed default graph, so the harness needs
	// one of its own and drops it on reset.
	graphName = "gqlcompat"
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
			fixture.CapLabels:             true,
			fixture.CapMultiLabel:         true,
			fixture.CapNodeProperties:     true,
			fixture.CapEdgeProperties:     true,
			fixture.CapEdgeTypes:          true,
			fixture.CapMultipleEdgeTypes:  true,
			fixture.CapMultipleNodeLabels: true,
			fixture.CapTemporalValues:     true,
			fixture.CapListValues:         true,
			fixture.CapSelfLoops:          true,
			fixture.CapParallelEdges:      true,
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
			"driven through the `lbug` shell in jsonlines mode, one long-lived process per session",
			"data is held in a `CREATE GRAPH " + graphName + " ANY` schemaless graph, so nodes keep real label sets and dynamic properties",
			"the shell has no parameter binding, so parameterised cases are skipped rather than inlined",
			"errors carry no GQLSTATUS; condition cases fall back to message matching",
		},
	}
}

// Open prepares a session directory. The process starts on first use.
func (d *Driver) Open(ctx context.Context, workdir string) (adapter.Session, error) {
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, err
	}
	return &session{driver: d, workdir: workdir, path: filepath.Join(workdir, "graph.lbug")}, nil
}

// Close releases driver-level state, of which there is none.
func (d *Driver) Close() error { return nil }

type session struct {
	driver  *Driver
	workdir string
	path    string

	mu     sync.Mutex
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	errs   *bytes.Buffer
	serial int
	closed bool
}

// start brings the shell up and puts it inside a fresh ANY graph. The caller
// holds s.mu.
func (s *session) startLocked(ctx context.Context) error {
	if s.cmd != nil {
		return nil
	}
	// The process outlives any single statement's deadline, so it is not
	// started against the caller's context: a slow query must abort the read,
	// not kill the process the next case needs.
	//nolint:noctx // deliberate: see above.
	cmd := exec.Command(s.driver.binary, s.path,
		"-m", "jsonlines", "--no_stats", "--no_progress_bar")
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
		return fmt.Errorf("ladybug: %w", err)
	}
	s.cmd, s.in, s.out = cmd, stdin, bufio.NewReaderSize(stdout, 1<<20)

	// The first exchange also drains the shell's banner, which the sentinel
	// swallows along with anything else printed before it.
	//
	// A reopened database already has the graph, and that is not an error
	// worth failing a run over; anything else is, because a session that is
	// not inside the ANY graph would silently run every case against the
	// schema-typed default one.
	rep, err := s.roundTripLocked(ctx, "CREATE GRAPH "+graphName+" ANY")
	if err != nil {
		_ = s.stopLocked()
		return fmt.Errorf("ladybug: creating graph: %w", err)
	}
	if rep.errText != "" && !alreadyExists(rep.errText) {
		_ = s.stopLocked()
		return fmt.Errorf("ladybug: creating graph: %s", rep.errText)
	}
	rep, err = s.roundTripLocked(ctx, "USE GRAPH "+graphName)
	if err != nil {
		_ = s.stopLocked()
		return fmt.Errorf("ladybug: selecting graph: %w", err)
	}
	if rep.errText != "" {
		_ = s.stopLocked()
		return fmt.Errorf("ladybug: selecting graph: %s", rep.errText)
	}
	return nil
}

func alreadyExists(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "already exists") || strings.Contains(m, "duplicate")
}

func notFound(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "does not exist") || strings.Contains(m, "not found") ||
		strings.Contains(m, "cannot find")
}

func (s *session) stopLocked() error {
	if s.cmd == nil {
		return nil
	}
	if s.in != nil {
		_, _ = io.WriteString(s.in, ":quit\n")
		_ = s.in.Close()
	}
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
	s.cmd, s.in, s.out = nil, nil, nil
	return nil
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

// roundTripLocked sends one statement followed by a unique sentinel and reads
// until the sentinel comes back.
//
// The shell is a REPL: it prints rows and errors to one stream with no framing
// and no end-of-result marker. A sentinel query whose value nothing else can
// produce is what turns that stream back into request/response, and the
// counter is what stops a timed-out statement's late output being read as the
// next statement's answer.
func (s *session) roundTripLocked(ctx context.Context, stmt string) (*reply, error) {
	s.serial++
	marker := fmt.Sprintf("gqlcompat-eof-%d", s.serial)

	var buf bytes.Buffer
	buf.WriteString(terminate(stmt))
	buf.WriteString("\nRETURN '")
	buf.WriteString(marker)
	buf.WriteString("' AS __gqlcompat;\n")
	if _, err := s.in.Write(buf.Bytes()); err != nil {
		return nil, s.fatalLocked(fmt.Errorf("ladybug: write: %w", err))
	}

	type read struct {
		rep *reply
		err error
	}
	ch := make(chan read, 1)
	go func() {
		rep, err := readUntil(s.out, marker)
		ch <- read{rep, err}
	}()

	select {
	case <-ctx.Done():
		// One pipe, one reader: a statement that outran its deadline has left
		// output nobody will consume, and the only safe recovery is to discard
		// the process.
		_ = s.stopLocked()
		return nil, &adapter.Failure{Timeout: true, Fatal: true, Message: ctx.Err().Error()}
	case r := <-ch:
		if r.err != nil {
			return nil, s.fatalLocked(fmt.Errorf("ladybug: read: %w (stderr: %s)",
				r.err, strings.TrimSpace(s.errs.String())))
		}
		return r.rep, nil
	}
}

// readUntil consumes lines up to and including the sentinel row.
func readUntil(r *bufio.Reader, marker string) (*reply, error) {
	rep := &reply{}
	var errLines []string
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if len(line) == 0 {
				return nil, err
			}
			return nil, fmt.Errorf("truncated output before sentinel: %q", string(line))
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if bytes.Contains(trimmed, []byte(marker)) {
			rep.errText = strings.TrimSpace(strings.Join(errLines, "\n"))
			return rep, nil
		}
		if trimmed[0] == '{' {
			obj, err := decodeObject(trimmed)
			if err != nil {
				// A line that starts like JSON and is not JSON is the shell
				// saying something the adapter does not model; keeping it as
				// text is better than dropping it.
				errLines = append(errLines, string(trimmed))
				continue
			}
			rep.lines = append(rep.lines, obj)
			continue
		}
		errLines = append(errLines, strings.TrimPrefix(string(trimmed), "Error: "))
	}
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

func (s *session) fatalLocked(err error) error {
	_ = s.stopLocked()
	return &adapter.Failure{Fatal: true, Message: err.Error()}
}

// Exec runs one statement.
func (s *session) Exec(ctx context.Context, stmt string, params map[string]any) (*adapter.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(params) > 0 {
		return nil, fmt.Errorf("ladybug: %w: the shell has no parameter binding", adapter.ErrUnsupported)
	}
	if err := s.startLocked(ctx); err != nil {
		return nil, err
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
	started := time.Now()

	for chunk := range slices.Chunk(fx.Nodes, nodeBatch) {
		var b strings.Builder
		b.WriteString("CREATE ")
		for i, n := range chunk {
			if i > 0 {
				b.WriteString(", ")
			}
			writeNodePattern(&b, n)
		}
		if err := s.execLoad(ctx, b.String(), "node"); err != nil {
			return adapter.LoadStats{}, err
		}
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
			if err := s.execLoad(ctx, stmt, "edge"); err != nil {
				return adapter.LoadStats{}, err
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

func (s *session) execLoad(ctx context.Context, stmt, phase string) error {
	rep, err := s.roundTripLocked(ctx, stmt)
	if err != nil {
		return err
	}
	if rep.errText != "" {
		return fmt.Errorf("ladybug: %s load: %s", phase, rep.errText)
	}
	return nil
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

// Reset drops the working graph and makes a new one. Dropping is cheaper and
// more complete than deleting rows, and it also discards whatever dynamic
// property columns the previous fixture caused the ANY graph to grow.
func (s *session) Reset(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resetLocked(ctx)
}

func (s *session) resetLocked(ctx context.Context) error {
	if err := s.startLocked(ctx); err != nil {
		return err
	}
	steps := []struct {
		stmt     string
		tolerate func(string) bool
	}{
		{"USE GRAPH main", nil},
		{"DROP GRAPH " + graphName, notFound},
		{"CREATE GRAPH " + graphName + " ANY", alreadyExists},
		{"USE GRAPH " + graphName, nil},
	}
	for _, step := range steps {
		rep, err := s.roundTripLocked(ctx, step.stmt)
		if err != nil {
			return err
		}
		if rep.errText != "" && (step.tolerate == nil || !step.tolerate(rep.errText)) {
			return fmt.Errorf("ladybug: reset (%s): %s", step.stmt, rep.errText)
		}
	}
	return nil
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

// DataDir is the directory holding the database.
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
