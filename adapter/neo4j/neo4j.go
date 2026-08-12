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
	"maps"
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
	res, err := s.Run(ctx, "CALL dbms.components() YIELD name, versions, edition "+
		"RETURN name + ' ' + versions[0] + ' ' + edition AS v", nil)
	if err != nil {
		return "", err
	}
	rec, err := res.Single(ctx)
	if err != nil {
		return "", err
	}
	v, _ := rec.Get("v")
	return fmt.Sprint(v), nil
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
		props := map[string]any{"_key": n.Key}
		maps.Copy(props, n.Props)
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
		props := map[string]any{}
		maps.Copy(props, e.Props)
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
		return f
	}
	if strings.Contains(err.Error(), "context deadline exceeded") {
		return &adapter.Failure{Timeout: true, Message: err.Error()}
	}
	return &adapter.Failure{Message: err.Error()}
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
