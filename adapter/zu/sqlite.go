package zu

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	// The driver is modernc's translation of SQLite into Go rather than a cgo
	// binding, so the adapter still builds and cross-compiles with CGO_ENABLED=0
	// like the rest of the module.
	_ "modernc.org/sqlite"

	"github.com/tamnd/gql-compat/fixture"
)

// zu's SQLite engine claims files with these two pragmas. `zu convert` reads
// them before anything else and refuses a file that carries a different
// nonzero application id, so a fixture database has to claim them too.
const (
	sqliteApplicationID = 0x005A_5531 // the ASCII bytes "ZU1"
	sqliteSchemaVersion = 2           // version 2 records rel endpoints in the catalogue
)

// writeFixtureDB writes fx into path as a database laid out the way zu's own
// SQLite engine lays one out, so that `zu convert path out.zu1` can read it.
//
// This is the adapter's ingest path, and it exists because zu's edge-list
// loader carries no labels and no properties, which leaves nothing in the
// corpus runnable. zu's own SQLite schema is small, documented in
// docs/05-storage-sqlite.md, and already the format `zu convert` reads, so
// writing it is a smaller and more honest coupling than teaching the harness
// to fabricate zu1 segments. zu detects a third-party writer by schema hash
// and opens such a file read-only, which is all a conversion needs.
//
// What survives the conversion is what the capability set declares: one label
// per node, integer and string node properties, and typed directed edges
// including self-loops and parallel pairs. Edge properties do not — zu's
// converter reads only the endpoints of a rel table — and neither do doubles,
// booleans, nulls, lists or temporals, which its property loader refuses
// outright. Nothing here works around any of that; a fixture needing it is
// filtered by Capabilities before Load is ever called.
func writeFixtureDB(ctx context.Context, path string, fx *fixture.Fixture) error {
	plan, err := planFixture(fx)
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"PRAGMA application_id = %d; PRAGMA user_version = %d;",
		sqliteApplicationID, sqliteSchemaVersion)); err != nil {
		return fmt.Errorf("claiming the file for zu: %w", err)
	}
	if _, err := db.ExecContext(ctx, catalogDDL); err != nil {
		return err
	}

	// One transaction for the whole load. A hundred thousand nodes inserted
	// with autocommit would fsync a hundred thousand times, and the ingest
	// number the report publishes would be measuring SQLite's journal rather
	// than zu's loader.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, t := range plan.nodeTables {
		if err := t.create(ctx, tx); err != nil {
			return err
		}
	}
	for _, t := range plan.relTables {
		if err := t.create(ctx, tx); err != nil {
			return err
		}
	}
	for _, t := range plan.nodeTables {
		if err := t.fill(ctx, tx); err != nil {
			return err
		}
	}
	for _, t := range plan.relTables {
		if err := t.fill(ctx, tx); err != nil {
			return err
		}
	}

	// planFixture refuses everything it cannot place, so a plan that was built
	// at all covers the whole fixture. Checking it here is what keeps that true
	// as the planner grows: a graph quietly written short would still load, and
	// would then be measured and scored as though it were the fixture, which is
	// the one failure mode of this path that produces a plausible wrong number
	// rather than an error.
	if nodes, edges := plan.counts(); nodes != len(fx.Nodes) || edges != len(fx.Edges) {
		return fmt.Errorf("staged %d nodes and %d edges for a fixture of %d and %d",
			nodes, edges, len(fx.Nodes), len(fx.Edges))
	}
	return tx.Commit()
}

// catalogDDL is zu's catalogue table, copied from crates/zu-sqlite. `id` is a
// rowid alias rather than a bare rowid because it is the table id the query
// layer binds against, and a VACUUM must not renumber it.
const catalogDDL = `CREATE TABLE IF NOT EXISTS zu_catalog (` +
	`id INTEGER PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, ` +
	`sql TEXT NOT NULL, src_table TEXT, dst_table TEXT, UNIQUE (kind, name))`

type fixturePlan struct {
	nodeTables []*nodeTable
	relTables  []*relTable
}

// nodeTable is one label's worth of nodes. zrow — the row's position in the
// table — is the node id zu uses, and rows are numbered in fixture order so
// that an edge can name its endpoints before either has been written.
type nodeTable struct {
	label string
	cols  []string
	types []string
	rows  [][]any
}

type relTable struct {
	typ      string
	endpoint string
	edges    [][2]int64
}

// planFixture works out the schema before any of it is written, because a
// fixture this route cannot carry must be refused whole rather than half
// loaded into a file the next case would then query.
func planFixture(fx *fixture.Fixture) (*fixturePlan, error) {
	zrow := make(map[string]int64, len(fx.Nodes))
	// labelOf answers what table a node key lives in. It is a map rather than a
	// scan because the edge loop below asks it twice per edge: on a hundred
	// thousand nodes and a hundred thousand edges, a scan is ten billion
	// comparisons, and staging perf-path-100k spent nineteen seconds in it
	// while the SQLite writes it was blamed for took a quarter of one.
	labelOf := make(map[string]string, len(fx.Nodes))
	byLabel := map[string][]int{}
	labels := []string{}
	for i, n := range fx.Nodes {
		label := "Node"
		switch len(n.Labels) {
		case 0:
			// An unlabelled node still needs a table to live in. `Node` is not
			// a label the fixture wrote, so a case that asked for it by name
			// would find nothing, which is the correct answer.
		case 1:
			label = n.Labels[0]
		default:
			return nil, fmt.Errorf("node %q carries %d labels; zu gives a node exactly one table",
				n.Key, len(n.Labels))
		}
		if err := checkIdent(label); err != nil {
			return nil, err
		}
		if _, seen := byLabel[label]; !seen {
			labels = append(labels, label)
		}
		labelOf[n.Key] = label
		zrow[n.Key] = int64(len(byLabel[label]))
		byLabel[label] = append(byLabel[label], i)
	}

	plan := &fixturePlan{}
	for _, label := range labels {
		t, err := buildNodeTable(label, byLabel[label], fx.Nodes)
		if err != nil {
			return nil, err
		}
		plan.nodeTables = append(plan.nodeTables, t)
	}

	byType := map[string][][2]int64{}
	// A rel table binds to the node table its edges run between, which the
	// first edge of each type settles; the rest are checked against it by the
	// cross-label test below.
	endpointOf := map[string]string{}
	types := []string{}
	for _, e := range fx.Edges {
		typ := "EDGE"
		if e.Type != "" {
			typ = e.Type
		}
		if err := checkIdent(typ); err != nil {
			return nil, err
		}
		from, okFrom := zrow[e.From]
		to, okTo := zrow[e.To]
		if !okFrom || !okTo {
			return nil, fmt.Errorf("edge %s -> %s names a node the fixture does not define", e.From, e.To)
		}
		// zu binds a rel table to a single node table, so an edge whose
		// endpoints carry different labels has nowhere to go. Capabilities
		// declares multiple-node-labels unsupported for exactly this reason;
		// the check is here so the reason is legible if that ever slips.
		fl, tl := labelOf[e.From], labelOf[e.To]
		if fl != tl {
			return nil, fmt.Errorf("edge %s -> %s crosses labels %s and %s; zu binds a rel table to one node table",
				e.From, e.To, fl, tl)
		}
		if _, seen := byType[typ]; !seen {
			types = append(types, typ)
			endpointOf[typ] = fl
		}
		byType[typ] = append(byType[typ], [2]int64{from, to})
	}
	for _, typ := range types {
		plan.relTables = append(plan.relTables, &relTable{
			typ:      typ,
			endpoint: endpointOf[typ],
			edges:    byType[typ],
		})
	}

	// zu's converter gives a node table its row domain through a rel load, and
	// refuses to convert a node table no rel table touches. A fixture with
	// nodes and no edges is therefore carried by an empty rel table: it adds
	// no edges, no case can match one, and the nodes arrive.
	for _, nt := range plan.nodeTables {
		if slices.ContainsFunc(plan.relTables, func(rt *relTable) bool { return rt.endpoint == nt.label }) {
			continue
		}
		plan.relTables = append(plan.relTables, &relTable{typ: "ZU_COMPAT_EMPTY_" + nt.label, endpoint: nt.label})
	}
	return plan, nil
}

// buildNodeTable derives one label's column set from its nodes.
//
// zu1 property columns are dense and uniformly integer or string, so a
// property some node of the label lacks, or holds at a different type, cannot
// be stored. Both are refused rather than papered over with a default: a
// column silently filled with zeros would answer aggregate cases with numbers
// no fixture contains.
func buildNodeTable(label string, idx []int, nodes []fixture.Node) (*nodeTable, error) {
	t := &nodeTable{label: label}
	seen := map[string]bool{}
	for _, i := range idx {
		for name := range nodes[i].Props {
			seen[name] = true
		}
	}
	t.cols = slices.Sorted(maps.Keys(seen))

	for _, name := range t.cols {
		if err := checkIdent(name); err != nil {
			return nil, err
		}
		kind := ""
		for _, i := range idx {
			v, ok := nodes[i].Props[name]
			if !ok {
				return nil, fmt.Errorf("node %q has no property %q; zu1 property columns are dense",
					nodes[i].Key, name)
			}
			k, err := columnKind(v)
			if err != nil {
				return nil, fmt.Errorf("property %q on node %q: %w", name, nodes[i].Key, err)
			}
			if kind != "" && kind != k {
				return nil, fmt.Errorf("property %q is %s on some nodes and %s on others; a zu1 column is one type",
					name, kind, k)
			}
			kind = k
		}
		t.types = append(t.types, kind)
	}

	for _, i := range idx {
		row := make([]any, len(t.cols))
		for c, name := range t.cols {
			row[c] = nodes[i].Props[name]
		}
		t.rows = append(t.rows, row)
	}
	return t, nil
}

// columnKind maps a fixture value to the only two column types zu1's property
// loader accepts.
func columnKind(v any) (string, error) {
	switch v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "INTEGER", nil
	case string:
		return "TEXT", nil
	default:
		return "", fmt.Errorf("value %v of type %T is neither an integer nor a string", v, v)
	}
}

func (t *nodeTable) create(ctx context.Context, tx *sql.Tx) error {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE n_%s (zrow INTEGER PRIMARY KEY", t.label)
	for i, c := range t.cols {
		fmt.Fprintf(&b, ", p_%s %s", c, t.types[i])
	}
	b.WriteString(");")
	return createTable(ctx, tx, "node", t.label, b.String(), nil)
}

func (t *nodeTable) fill(ctx context.Context, tx *sql.Tx) error {
	if len(t.rows) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf("INSERT INTO n_%s VALUES (?%s)",
		t.label, strings.Repeat(", ?", len(t.cols))))
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for zrow, row := range t.rows {
		args := append([]any{int64(zrow)}, row...)
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func (t *relTable) create(ctx context.Context, tx *sql.Tx) error {
	ddl := fmt.Sprintf(
		"CREATE TABLE r_%[1]s (zrel INTEGER PRIMARY KEY, src INTEGER NOT NULL, dst INTEGER NOT NULL);\n"+
			"CREATE INDEX r_%[1]s_fwd ON r_%[1]s (src, dst);\nCREATE INDEX r_%[1]s_bwd ON r_%[1]s (dst, src);",
		t.typ)
	return createTable(ctx, tx, "rel", t.typ, ddl, &[2]string{t.endpoint, t.endpoint})
}

func (t *relTable) fill(ctx context.Context, tx *sql.Tx) error {
	if len(t.edges) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf("INSERT INTO r_%s VALUES (NULL, ?, ?)", t.typ))
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, e := range t.edges {
		if _, err := stmt.ExecContext(ctx, e[0], e[1]); err != nil {
			return err
		}
	}
	return nil
}

// createTable runs a table's DDL and records it in the catalogue, which is
// where `zu convert` looks to find out what the file holds. The DDL text is
// stored verbatim beside the entry because that is what zu's own writer
// stores; the converter reads the kind, the name and the endpoints.
func createTable(ctx context.Context, tx *sql.Tx, kind, name, ddl string, endpoints *[2]string) error {
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("creating %s table %s: %w", kind, name, err)
	}
	var src, dst any
	if endpoints != nil {
		src, dst = endpoints[0], endpoints[1]
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO zu_catalog (kind, name, sql, src_table, dst_table) VALUES (?, ?, ?, ?, ?)",
		kind, name, ddl, src, dst)
	return err
}

// checkIdent applies zu's identifier rule, which is the ASCII one: a letter or
// underscore, then letters, digits and underscores. Everything the adapter
// writes goes into SQL by interpolation rather than by placeholder — SQLite
// takes no parameter in a table name — so this is the only thing standing
// between a fixture label and the schema.
func checkIdent(name string) error {
	if name == "" {
		return errors.New("empty identifier")
	}
	for i, r := range name {
		ok := r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(i > 0 && r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("identifier %q is not one zu accepts: %s",
				name, "ASCII letters, digits and underscore, not starting with a digit")
		}
	}
	return nil
}

// counts reports what the plan will actually write, which writeFixtureDB
// checks against what the fixture asked for before committing.
func (p *fixturePlan) counts() (nodes, edges int) {
	for _, t := range p.nodeTables {
		nodes += len(t.rows)
	}
	for _, t := range p.relTables {
		edges += len(t.edges)
	}
	return nodes, edges
}
