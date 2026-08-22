// Package neo4j drives a Neo4j server over Bolt.
//
// Neo4j is the reference point for this project for two reasons. It is the
// implementation whose GQL conformance is most fully documented — Neo4j
// publishes an appendix naming, feature by feature, which mandatory
// subclauses and which optional feature codes Cypher covers — and it is the
// implementation whose query language GQL most directly grew out of. A
// corpus that Neo4j fails in some place Neo4j's own appendix says it should
// pass is a corpus with a bug, and that is a useful thing to be able to check.
//
// The adapter runs the standard GQL text verbatim, exactly as the zu adapter
// does. Where a case carries a Cypher spelling in its Dialects map, the
// runner may execute that separately in compatibility mode; this adapter is
// not told which mode it is in and never chooses a spelling itself.
package neo4j

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	neo "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/config"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/db"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/dbtype"

	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/rows"
)

func init() { adapter.Register("neo4j", New) }

// Driver holds one Bolt driver, which is a connection pool and is meant to be
// shared across sessions.
type Driver struct {
	driver   neo.Driver
	database string
	uri      string
}

// New connects to a Neo4j server. The defaults match a local install:
// bolt://localhost:7687, user neo4j, database neo4j.
func New(opts adapter.Options) (adapter.Driver, error) {
	uri := opts.URI
	if uri == "" {
		uri = "bolt://localhost:7687"
	}
	user := opts.Username
	if user == "" {
		user = "neo4j"
	}
	database := opts.Database
	if database == "" {
		database = "neo4j"
	}
	d, err := neo.NewDriver(uri, neo.BasicAuth(user, opts.Password, ""),
		func(c *config.Config) {
			// A conformance run issues many small statements in sequence from
			// one goroutine; a large pool would only add idle sockets.
			c.MaxConnectionPoolSize = 8
			// Fetching all records for a small result in one round trip keeps
			// the measured latency about the query rather than about the
			// streaming protocol's chunk size.
			c.FetchSize = neo.FetchAll
		})
	if err != nil {
		return nil, fmt.Errorf("neo4j: %w", err)
	}
	return &Driver{driver: d, database: database, uri: uri}, nil
}

// Name identifies the adapter.
func (d *Driver) Name() string { return "neo4j" }

// Version reports the server's edition and version, which is what a
// conformance claim has to be pinned to.
func (d *Driver) Version(ctx context.Context) (string, error) {
	if err := d.driver.VerifyConnectivity(ctx); err != nil {
		return "", fmt.Errorf("neo4j: %s: %w", d.uri, err)
	}
	s := d.driver.NewSession(ctx, neo.SessionConfig{DatabaseName: d.database})
	defer func() { _ = s.Close(ctx) }()
	// dbms.components() returns one row per component, and since 2025 that is
	// at least two: the kernel and the Cypher language version. Asking for a
	// single record made the version read "unknown: Result contains more than
	// one record" against 2026.07. Every row is kept and joined, because the
	// Cypher version is part of what a conformance claim has to be pinned to:
	// the same kernel speaks Cypher 5 and Cypher 25 and they are not the same
	// language.
	res, err := s.Run(ctx, "CALL dbms.components() YIELD name, versions, edition "+
		"RETURN name AS name, versions AS versions, edition AS edition", nil)
	if err != nil {
		return "", err
	}
	recs, err := res.Collect(ctx)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(recs))
	for _, rec := range recs {
		name, _ := rec.Get("name")
		versions, _ := rec.Get("versions")
		edition, _ := rec.Get("edition")
		part := fmt.Sprint(name) + " " + joinVersions(versions)
		if e := fmt.Sprint(edition); e != "" && e != "<nil>" {
			part += " " + e
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "", errors.New("neo4j: dbms.components() returned no rows")
	}

	// The language the database parses by default is part of the version, and
	// getting it wrong invalidates a whole run, so it is checked here rather
	// than reported and left to the reader. See defaultLanguage.
	if lang := d.defaultLanguage(ctx, s); lang != "" {
		parts = append(parts, "default language "+lang)
		if lang != cypher25 && os.Getenv(anyLanguageEnv) == "" {
			return "", fmt.Errorf("neo4j: database %q parses %s by default, and this corpus is GQL text: the server answers a GQL construct it understands perfectly well with %q and the harness records a syntax error, so the run would measure the language version rather than the engine. Run `ALTER DATABASE %s SET DEFAULT LANGUAGE %s`, or set %s=1 to measure %s deliberately",
				d.database, lang,
				"The query is parsable in `CYPHER 25`, but it is run in `CYPHER 5`",
				d.database, cypher25, anyLanguageEnv, lang)
		}
	}
	return strings.Join(parts, ", "), nil
}

// cypher25 is the language version this adapter expects to measure. Cypher 25
// is the version Neo4j's GQL conformance appendix documents; Cypher 5 predates
// the alignment and rejects a large part of the GQL surface outright.
const cypher25 = "CYPHER 25"

// anyLanguageEnv opts out of the check above, for someone who wants the older
// language measured on purpose. It is an environment variable rather than a
// flag for the same reason the password is: it is not something to leave in a
// shell history as though it were an ordinary run.
const anyLanguageEnv = "GQL_COMPAT_NEO4J_ANY_LANGUAGE"

// defaultLanguage reports the database's default query language, or "" if the
// server is too old to have one.
//
// A server that predates Cypher 25 has no defaultLanguage column and nothing
// to check, because the only language it speaks is the one it speaks. That is
// why this returns no error: every failure here means the same thing, which is
// that there is no language version to report, and the check exists to catch a
// misconfiguration rather than to become a new reason a run cannot start
// against a server that is simply older.
func (d *Driver) defaultLanguage(ctx context.Context, s neo.Session) string {
	res, err := s.Run(ctx, "SHOW DATABASES YIELD name, defaultLanguage "+
		"WHERE name = $name RETURN defaultLanguage AS lang", map[string]any{"name": d.database})
	if err != nil {
		return ""
	}
	recs, err := res.Collect(ctx)
	if err != nil || len(recs) == 0 {
		return ""
	}
	lang, _ := recs[0].Get("lang")
	if lang == nil {
		return ""
	}
	return fmt.Sprint(lang)
}

// joinVersions renders the versions list of one component. It is a list
// because a component can speak more than one version at once, which Cypher
// does, so taking element zero would silently drop half the answer.
func joinVersions(v any) string {
	list, ok := v.([]any)
	if !ok {
		return fmt.Sprint(v)
	}
	out := make([]string, len(list))
	for i, item := range list {
		out[i] = fmt.Sprint(item)
	}
	return strings.Join(out, "/")
}

// Capabilities declares Neo4j's data model.
//
// The one interesting absence is undirected edges. GQL defines them and
// Neo4j's own appendix says it does not implement them; a fixture that needs
// one is therefore skipped here and the report shows the gap against the
// standard rather than pretending a directed edge stands in.
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
			// These two were left out of the map until the run of 2026-08-12,
			// and a missing key reads as false, so the report said Neo4j could
			// not hold a float or a boolean and skipped four cases on the
			// strength of it. Both are mandatory GQL property types and the
			// Cypher manual lists both as supported.
			fixture.CapFloatValues:   true,
			fixture.CapBooleanValues: true,
			// Neo4j stores an absent property rather than a null one; a null
			// assigned by SET removes the property.
			fixture.CapNullProperties:  false,
			fixture.CapUndirectedEdges: false,
		},
		GQLStatus:          true,
		Parameters:         true,
		Transactions:       false,
		MultipleStatements: true,
		Isolated:           true,
		Notes: []string{
			"driven over Bolt with the official Go driver v6",
			"GQLSTATUS is read from the driver's Neo4jError.GqlStatus, available from server 5.23",
			"explicit START TRANSACTION / COMMIT / ROLLBACK are not Cypher statements; GQL feature GT01 is unavailable",
			"undirected relationships are not stored, so fixtures needing them are skipped",
		},
	}
}

// Open returns a session. workdir is unused: the server owns its own storage
// and the harness cannot measure it by walking a directory, which the disk
// columns report as unavailable rather than as zero.
func (d *Driver) Open(ctx context.Context, workdir string) (adapter.Session, error) {
	return &session{driver: d}, nil
}

// Close shuts the pool.
func (d *Driver) Close() error { return d.driver.Close(context.Background()) }

type session struct {
	driver *Driver
	sess   neo.Session
}

func (s *session) open(ctx context.Context) neo.Session {
	if s.sess == nil {
		s.sess = s.driver.driver.NewSession(ctx, neo.SessionConfig{DatabaseName: s.driver.database})
	}
	return s.sess
}

// Load rebuilds the graph from a fixture with parameterised CREATE
// statements, batched.
//
// Node creation is one UNWIND per label set, and edge creation one UNWIND per
// type, both over a parameter list. Issuing one CREATE per row would make the
// ingest measurement a measurement of Bolt round trips; batching makes it a
// measurement of the server, which is the comparable quantity.
func (s *session) Load(ctx context.Context, fx *fixture.Fixture) (adapter.LoadStats, error) {
	if err := s.Reset(ctx); err != nil {
		return adapter.LoadStats{}, err
	}
	sess := s.open(ctx)
	started := time.Now()

	// A key index makes the edge phase's lookups logarithmic rather than a
	// full scan per edge. Without it the edge load on a fixture of any size
	// is quadratic and the number means nothing.
	if _, err := sess.Run(ctx,
		"CREATE INDEX gqlcompat_key IF NOT EXISTS FOR (n:`_GqlCompat`) ON (n.`_key`)", nil); err != nil {
		return adapter.LoadStats{}, err
	}

	byLabels := map[string][]map[string]any{}
	for _, n := range fx.Nodes {
		props, err := boltProps(map[string]any{"_key": n.Key}, n.Props)
		if err != nil {
			return adapter.LoadStats{}, fmt.Errorf("neo4j: node %s: %w", n.Key, err)
		}
		byLabels[labelKey(n.Labels)] = append(byLabels[labelKey(n.Labels)], props)
	}
	for labels, batch := range byLabels {
		stmt := fmt.Sprintf("UNWIND $rows AS r CREATE (n:`_GqlCompat`%s) SET n = r", labels)
		if _, err := sess.Run(ctx, stmt, map[string]any{"rows": batch}); err != nil {
			return adapter.LoadStats{}, fmt.Errorf("neo4j: node load: %w", err)
		}
	}

	byType := map[string][]map[string]any{}
	for _, e := range fx.Edges {
		props, err := boltProps(map[string]any{}, e.Props)
		if err != nil {
			return adapter.LoadStats{}, fmt.Errorf("neo4j: edge %s->%s: %w", e.From, e.To, err)
		}
		byType[e.Type] = append(byType[e.Type], map[string]any{
			"from": e.From, "to": e.To, "props": props,
		})
	}
	for typ, batch := range byType {
		if typ == "" {
			typ = "_EDGE"
		}
		stmt := fmt.Sprintf(
			"UNWIND $rows AS r "+
				"MATCH (a:`_GqlCompat` {`_key`: r.from}), (b:`_GqlCompat` {`_key`: r.to}) "+
				"CREATE (a)-[e:`%s`]->(b) SET e = r.props", typ)
		if _, err := sess.Run(ctx, stmt, map[string]any{"rows": batch}); err != nil {
			return adapter.LoadStats{}, fmt.Errorf("neo4j: edge load: %w", err)
		}
	}

	return adapter.LoadStats{
		Nodes:      len(fx.Nodes),
		Edges:      len(fx.Edges),
		EngineWall: time.Since(started),
		Detail:     fmt.Sprintf("%d label sets, %d edge types", len(byLabels), len(byType)),
	}, nil
}

// boltProps copies a fixture's properties into a parameter map the driver can
// send, turning the fixture's tagged temporal literals into the driver's
// temporal types on the way.
//
// It exists because of a load failure in the run of 2026-08-12: the props were
// copied verbatim and {duration: "P2D"} reached the server as a map, which
// Bolt has no property type for. Two cases errored and the two temporal
// features they cover went unmeasured against an engine that supports both.
// The reading of the literal is fixture's, not this adapter's, so that every
// engine loads the same value.
func boltProps(into, props map[string]any) (map[string]any, error) {
	for k, v := range props {
		t, ok := fixture.AsTemporal(v)
		if !ok {
			into[k] = v
			continue
		}
		conv, err := boltTemporal(t)
		if err != nil {
			return nil, fmt.Errorf("property %s: %w", k, err)
		}
		into[k] = conv
	}
	return into, nil
}

// boltTemporal maps one fixture temporal onto the driver type Bolt carries for
// it. A local kind is sent as the driver's local type rather than as an
// instant, because a timestamp with no zone that arrives as one has silently
// acquired a zone.
func boltTemporal(t fixture.Temporal) (any, error) {
	if t.Kind == fixture.KindDuration {
		d, err := t.Duration()
		if err != nil {
			return nil, err
		}
		return dbtype.Duration{
			Months: int64(d.Months), Days: int64(d.Days),
			Seconds: d.Seconds, Nanos: d.Nanos,
		}, nil
	}
	at, err := t.Time()
	if err != nil {
		return nil, err
	}
	switch t.Kind {
	case fixture.KindDate:
		return dbtype.Date(at), nil
	case fixture.KindLocalTime:
		return dbtype.LocalTime(at), nil
	case fixture.KindTime:
		return dbtype.Time(at), nil
	case fixture.KindLocalDateTime:
		return dbtype.LocalDateTime(at), nil
	case fixture.KindDateTime:
		return at, nil
	default:
		return nil, fmt.Errorf("neo4j: no Bolt type for %s", t.Kind)
	}
}

func labelKey(labels []string) string {
	var b strings.Builder
	for _, l := range labels {
		b.WriteString(":`")
		b.WriteString(strings.ReplaceAll(l, "`", "``"))
		b.WriteString("`")
	}
	return b.String()
}

// Exec runs one statement and converts the result.
func (s *session) Exec(ctx context.Context, stmt string, params map[string]any) (*adapter.Result, error) {
	sess := s.open(ctx)
	res, err := sess.Run(ctx, stmt, params)
	if err != nil {
		return nil, convertError(err)
	}
	recs, err := res.Collect(ctx)
	if err != nil {
		return nil, convertError(err)
	}
	t := &rows.Table{}
	if len(recs) > 0 {
		t.Columns = recs[0].Keys
	} else if keys, err := res.Keys(); err == nil {
		t.Columns = keys
	}
	var size int64
	for _, rec := range recs {
		row := make([]any, len(rec.Values))
		for i, v := range rec.Values {
			row[i] = convertValue(v)
			size += int64(len(rows.Render(row[i])))
		}
		t.Rows = append(t.Rows, row)
	}
	out := &adapter.Result{Table: t, Bytes: size}
	if sum, err := res.Consume(ctx); err == nil && sum != nil {
		// A successful GQL outcome is 00000, or 02000 when the statement
		// produced no rows. The driver does not surface a status for success,
		// so the adapter derives the one the standard specifies.
		if len(recs) == 0 {
			out.GQLStatus = "02000"
		} else {
			out.GQLStatus = "00000"
		}
	}
	return out, nil
}

// convertError turns a driver error into the harness's Failure, keeping the
// GQLSTATUS when the server sent one.
func convertError(err error) error {
	if err == nil {
		return nil
	}
	if n, ok := errors.AsType[*db.Neo4jError](err); ok {
		f := &adapter.Failure{Message: n.Msg}
		if s := n.GqlStatus; s != "" {
			f.GQLStatus = s
		}
		if f.Message == "" {
			f.Message = n.Error()
		}
		f.Diagnostic = diagnosticOf(n.GqlDiagnosticRecord)
		return f
	}
	if strings.Contains(err.Error(), "context deadline exceeded") {
		return &adapter.Failure{Timeout: true, Message: err.Error()}
	}
	return &adapter.Failure{Message: err.Error()}
}

// diagnosticOf reads the driver's diagnostic record into the harness's, taking
// the fields Neo4j actually fills and inventing nothing for the ones it does
// not.
//
// The server sends a map rather than a struct, with the standard's field names
// in upper case and its own additions prefixed by an underscore, so the keys
// here are Neo4j's spelling and not ISO's abbreviations. CURRENT_SCHEMA is the
// one ISO field always present, because the driver supplies a default for it,
// and _position is the place, one-based, where the server had one to give.
//
// Nothing is read as a subject. Neo4j has _status_parameters, whose keys vary
// with the message template, and picking one of them as "the thing this
// condition is about" would be the harness deciding what the engine meant.
// A report that says Neo4j names the schema and the position and not the
// subject is a finding; a report that guesses is not.
func diagnosticOf(rec map[string]any) *adapter.Diagnostic {
	if len(rec) == 0 {
		return nil
	}
	out := &adapter.Diagnostic{}
	if s, ok := rec["CURRENT_SCHEMA"].(string); ok {
		out.Schema = s
	}
	if pos, ok := rec["_position"].(map[string]any); ok {
		out.Line = asInt(pos["line"])
		out.Column = asInt(pos["column"])
	}
	if out.Empty() {
		return nil
	}
	return out
}

// asInt reads a number out of the untyped map the server sends. Bolt integers
// arrive as int64 and JSON-shaped ones as float64, and a position that came
// back as the wrong Go type is a position lost for no reason a reader could
// see.
func asInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// convertValue maps Bolt's types onto the harness's neutral ones.
func convertValue(v any) any {
	switch x := v.(type) {
	case dbtype.Node:
		props := map[string]any{}
		for k, p := range x.Props {
			// The load phase stamps every node with a harness key and label;
			// they are scaffolding and must not appear in a comparison.
			if k == "_key" {
				continue
			}
			props[k] = convertValue(p)
		}
		var labels []string
		for _, l := range x.Labels {
			if l != "_GqlCompat" {
				labels = append(labels, l)
			}
		}
		return rows.Node{Labels: labels, Props: props}
	case dbtype.Relationship:
		props := map[string]any{}
		for k, p := range x.Props {
			props[k] = convertValue(p)
		}
		return rows.Edge{Type: x.Type, Props: props}
	case dbtype.Path:
		// The assertions are checked rather than bare. A driver version that
		// put something unexpected inside a path would otherwise panic here
		// and take down a run that may have been measuring for minutes; a path
		// that came back malformed is a wrong answer, which the comparison
		// will report, and not a reason to lose the other two hundred cases.
		p := rows.Path{Nodes: make([]rows.Node, 0, len(x.Nodes)), Edges: make([]rows.Edge, 0, len(x.Relationships))}
		for _, n := range x.Nodes {
			if node, ok := convertValue(n).(rows.Node); ok {
				p.Nodes = append(p.Nodes, node)
			}
		}
		for _, r := range x.Relationships {
			if edge, ok := convertValue(r).(rows.Edge); ok {
				p.Edges = append(p.Edges, edge)
			}
		}
		return p
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = convertValue(x[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = convertValue(vv)
		}
		return out
	case time.Time:
		// Temporal cases compare against the ISO 8601 text the standard
		// writes, so a temporal value is rendered rather than kept as a Go
		// time whose location an expectation cannot name.
		return x.Format(time.RFC3339Nano)
	case dbtype.Date:
		return x.Time().Format("2006-01-02")
	case dbtype.LocalTime:
		return x.Time().Format("15:04:05.999999999")
	case dbtype.LocalDateTime:
		return x.Time().Format("2006-01-02T15:04:05.999999999")
	case dbtype.Time:
		return x.Time().Format("15:04:05.999999999Z07:00")
	case dbtype.Duration:
		return x.String()
	}
	return rows.Normalize(v)
}

// Reset empties the database. DETACH DELETE in one statement is the fastest
// reset a Cypher session can issue and is bounded by the fixture sizes this
// corpus uses.
func (s *session) Reset(ctx context.Context) error {
	sess := s.open(ctx)
	if _, err := sess.Run(ctx, "MATCH (n) DETACH DELETE n", nil); err != nil {
		return convertError(err)
	}
	return nil
}

// PID is zero: the server is a separate process the harness did not start and
// must not assume it may sample.
func (s *session) PID() int { return 0 }

// DataDir is empty: the server's storage is not reachable from here, and the
// report says so rather than printing a zero.
func (s *session) DataDir() string { return "" }

// Close ends the Bolt session.
func (s *session) Close() error {
	if s.sess == nil {
		return nil
	}
	err := s.sess.Close(context.Background())
	s.sess = nil
	return err
}
