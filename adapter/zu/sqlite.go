package zu

import (
	"context"
	"database/sql"
	"encoding/json"
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
	sqliteSchemaVersion = 4           // version 4 adds the table holding the labels a node carries beyond its own
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
// What survives the conversion is what the capability set declares: every
// label a node carries, integer, string, float, boolean and temporal
// properties on nodes and on edges alike, and typed directed edges including
// self-loops and parallel pairs. A node's first label is the table it lives
// in and the rest ride the zu_labels table, which is what zu's converter
// turns into the label word each row carries.
// A rel table names the node table at each of its ends, so an
// edge between two labels crosses as well, as long as every edge of its type
// runs between the same pair. A null does cross, as a SQL NULL in a column whose declared type
// comes from the rows that hold a value, which is the shape zu's converter
// reads a column's validity words back from; a column that is null on every
// row names no type and is refused. A list does cross, as a JSON array in a
// text column whose declared type names the element type, which is the shape
// zu's converter reads one back from.
// Nothing here works around any of that; a fixture needing it is filtered by
// Capabilities before Load is ever called.
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
	if _, err := db.ExecContext(ctx, labelsDDL); err != nil {
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
	`sql TEXT NOT NULL, src_table TEXT, dst_table TEXT, ` +
	`undirected INTEGER NOT NULL DEFAULT 0, UNIQUE (kind, name))`

// labelsDDL is zu's label table, copied from the same place. One row per
// (table, node, label) for every label a node carries beyond the one its
// table is called, which every row of that table carries and none of these
// repeats.
const labelsDDL = `CREATE TABLE IF NOT EXISTS zu_labels (` +
	`tbl TEXT NOT NULL, zrow INTEGER NOT NULL, label TEXT NOT NULL, ` +
	`PRIMARY KEY (tbl, zrow, label)) WITHOUT ROWID`

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
	// The labels each row carries beyond the table's own, indexed the way
	// rows is, and nil when no row of the table carries any. zu holds a
	// word per row for a table that has this at all, so a table nobody
	// gave a second label to should not grow one.
	extra [][]string
}

type relTable struct {
	typ string
	// The node tables the edges of this type leave and arrive at. They
	// are the same table when every edge of the type runs between two
	// nodes of one label, and different tables when it runs between
	// two labels, which zu binds a rel table to as a pair.
	src string
	dst string
	// undirected says the edges here have no direction, which zu records on
	// the table rather than on the edge. A fixture that gives one type both
	// kinds of edge has nowhere to put the second, and is refused.
	undirected bool
	edges      [][2]int64
	// The property columns of the type, derived the same way a node
	// table's are, with one row per edge in the order edges holds them.
	cols  []string
	types []string
	rows  [][]any
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
		if len(n.Labels) > 0 {
			// The first label is the table the node lives in and the rest
			// are bits on its row. Which one is the table matters for what
			// the file looks like and not for what a query answers: a
			// pattern naming any of them finds the node either way. The
			// first is the choice because a fixture writes the labels in
			// an order somebody chose, and the alternative, picking by
			// some property of the label set, would put two nodes written
			// the same way in different tables.
			label = n.Labels[0]
		}
		// An unlabelled node still needs a table to live in. `Node` is not
		// a label the fixture wrote, so a case that asked for it by name
		// would find nothing, which is the correct answer.
		if err := checkIdent(label); err != nil {
			return nil, err
		}
		if len(n.Labels) > 1 {
			for _, extra := range n.Labels[1:] {
				if err := checkIdent(extra); err != nil {
					return nil, err
				}
			}
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
	// The properties of each type's edges, in the same order as its
	// endpoint pairs, so the column derivation and the row it renders
	// line up with the edge they came from.
	propsOf := map[string][]map[string]any{}
	labelsOf := map[string][]string{}
	// A rel table binds to the pair of node tables its edges run
	// between, which the first edge of each type settles; the rest are
	// checked against it by the test below.
	endpointOf := map[string][2]string{}
	// Whether a type's edges have a direction, settled by its first edge for
	// the same reason the endpoints are.
	undirectedOf := map[string]bool{}
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
		// zu binds a rel table to one pair of node tables, so every edge
		// of a type has to run between the same two labels. A fixture
		// that gives one type two different pairs has nowhere to put the
		// second, and is refused rather than half loaded.
		ends := [2]string{labelOf[e.From], labelOf[e.To]}
		if _, seen := byType[typ]; !seen {
			types = append(types, typ)
			endpointOf[typ] = ends
			undirectedOf[typ] = e.Undirected
		}
		if endpointOf[typ] != ends {
			was := endpointOf[typ]
			return nil, fmt.Errorf("edge type %s runs between %s and %s here and between %s and %s elsewhere; zu binds a rel table to one pair of node tables",
				typ, ends[0], ends[1], was[0], was[1])
		}
		if undirectedOf[typ] != e.Undirected {
			return nil, fmt.Errorf("edge type %s has both directed and undirected edges; zu records the direction on the table", typ)
		}
		byType[typ] = append(byType[typ], [2]int64{from, to})
		propsOf[typ] = append(propsOf[typ], e.Props)
		labelsOf[typ] = append(labelsOf[typ], fmt.Sprintf("%s -> %s", e.From, e.To))
	}
	for _, typ := range types {
		names := labelsOf[typ]
		cols, colTypes, rows, err := deriveColumns(propsOf[typ], "edge", func(i int) string { return names[i] })
		if err != nil {
			return nil, err
		}
		plan.relTables = append(plan.relTables, &relTable{
			typ:        typ,
			src:        endpointOf[typ][0],
			dst:        endpointOf[typ][1],
			undirected: undirectedOf[typ],
			edges:      byType[typ],
			cols:       cols,
			types:      colTypes,
			rows:       rows,
		})
	}

	// zu's converter gives a node table its row domain through a rel load, and
	// refuses to convert a node table no rel table touches. A fixture with
	// nodes and no edges is therefore carried by an empty rel table: it adds
	// no edges, no case can match one, and the nodes arrive.
	for _, nt := range plan.nodeTables {
		if slices.ContainsFunc(plan.relTables, func(rt *relTable) bool {
			return rt.src == nt.label || rt.dst == nt.label
		}) {
			continue
		}
		plan.relTables = append(plan.relTables, &relTable{typ: "ZU_COMPAT_EMPTY_" + nt.label, src: nt.label, dst: nt.label})
	}
	return plan, nil
}

// buildNodeTable derives one label's column set from its nodes, and collects
// the labels its rows carry beyond the table's own.
func buildNodeTable(label string, idx []int, nodes []fixture.Node) (*nodeTable, error) {
	props := make([]map[string]any, len(idx))
	for j, i := range idx {
		props[j] = nodes[i].Props
	}
	cols, types, rows, err := deriveColumns(props, "node", func(j int) string { return nodes[idx[j]].Key })
	if err != nil {
		return nil, err
	}
	var extra [][]string
	for j, i := range idx {
		if len(nodes[i].Labels) < 2 {
			continue
		}
		if extra == nil {
			extra = make([][]string, len(idx))
		}
		extra[j] = nodes[i].Labels[1:]
	}
	return &nodeTable{label: label, cols: cols, types: types, rows: rows, extra: extra}, nil
}

// deriveColumns works out one staged table's column set from the properties
// of the elements that will fill it, and renders each element into a row of
// that set.
//
// zu1 property columns are dense and uniformly typed, so a property some
// element lacks, or holds at a different type, cannot be stored. Both are
// refused rather than papered over with a default: a column silently filled
// with zeros would answer aggregate cases with numbers no fixture contains.
// Nodes and edges derive identically, which is why this takes properties
// rather than either of them; `kind` and `describe` are only there so an
// error says which element it is talking about.
func deriveColumns(props []map[string]any, kind string, describe func(int) string) (cols, types []string, rows [][]any, err error) {
	seen := map[string]bool{}
	for _, p := range props {
		for name := range p {
			seen[name] = true
		}
	}
	cols = slices.Sorted(maps.Keys(seen))

	for _, name := range cols {
		if err := checkIdent(name); err != nil {
			return nil, nil, nil, err
		}
		column := ""
		for i, p := range props {
			// An element without the property and one whose property is
			// null are the same thing here: the row holds no value, the
			// column says so in its validity words, and neither row has
			// a type to contribute to the declaration.
			v := p[name]
			if v == nil {
				continue
			}
			k, err := columnKind(v)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("property %q on %s %q: %w", name, kind, describe(i), err)
			}
			// An empty list names no element type, so it takes the one
			// the rest of the column has. A column of nothing but empty
			// lists has none to take and is refused below. An empty list
			// beside a scalar is still a mismatch and falls through to
			// the check under this one.
			if k == anyList && isListKind(column) {
				continue
			}
			if column == anyList && isListKind(k) {
				column = k
				continue
			}
			if column == "" && k == anyList {
				column = anyList
				continue
			}
			if column != "" && column != k {
				return nil, nil, nil, fmt.Errorf("property %q is %s on some %ss and %s on others; a zu1 column is one type",
					name, column, kind, k)
			}
			column = k
		}
		if column == anyList {
			return nil, nil, nil, fmt.Errorf("property %q is an empty list on every %s, which names no element type",
				name, kind)
		}
		if column == "" {
			return nil, nil, nil, fmt.Errorf("property %q is null on every %s, which names no type for its column",
				name, kind)
		}
		types = append(types, column)
	}

	rows = make([][]any, 0, len(props))
	for i, p := range props {
		row := make([]any, len(cols))
		for c, name := range cols {
			v := p[name]
			// A temporal value becomes its count here, where the column
			// type it was checked against is still in hand.
			if tv, ok := fixture.AsTemporal(v); ok {
				count, err := temporalCount(tv)
				if err != nil {
					return nil, nil, nil, fmt.Errorf("property %q on %s %q: %w", name, kind, describe(i), err)
				}
				v = count
			}
			// A list becomes the text its column holds here, for the
			// same reason: this is where the column type it was checked
			// against is still in hand.
			if lv, ok := v.([]any); ok {
				text, err := listText(lv)
				if err != nil {
					return nil, nil, nil, fmt.Errorf("property %q on %s %q: %w", name, kind, describe(i), err)
				}
				v = text
			}
			row[c] = v
		}
		rows = append(rows, row)
	}
	return cols, types, rows, nil
}

// columnKind maps a fixture value to the column type the staged table declares
// it as.
//
// The declaration is not decoration here. zu's converter reads a column's type
// back out of the staged file, and for three of these four the storage class
// carries it: an integer, a double and a byte string are each their own class.
// A boolean is not, because SQLite has no such class and stores one as the
// integers 0 and 1, so BOOLEAN is what tells the converter the column was meant
// as truth values rather than a count that happens to be small. A date and a
// duration are the same problem again and are told apart the same way: each is
// a count stored as an integer, and the declared type is the only place that
// says which count it is.
func columnKind(v any) (string, error) {
	if t, ok := fixture.AsTemporal(v); ok {
		return temporalKind(t)
	}
	switch vv := v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "INTEGER", nil
	case float32, float64:
		return "REAL", nil
	case bool:
		return "BOOLEAN", nil
	case string:
		return "TEXT", nil
	case []byte:
		return "BLOB", nil
	case []any:
		return listKind(vv)
	default:
		return "", fmt.Errorf("value %v of type %T is not a scalar a zu1 property column can hold", v, v)
	}
}

// anyList is the kind an empty list reports, which names a list whose element
// type is not decided yet. A column of them and nothing else has no
// declaration to make and is refused; a column that also holds a list with
// something in it takes the element type from that one.
const anyList = "LIST"

// isListKind says whether a declared column type holds lists, which is how an
// empty list is told apart from a value that merely has no type yet.
func isListKind(kind string) bool {
	return strings.HasSuffix(kind, anyList)
}

// listKind is the declared type of a column holding v.
//
// A zu1 list column holds one element type, and the declaration is the only
// place it is written down, so the elements are checked to agree here. Lists
// of lists are refused: zu's row format holds a count and then words or length
// prefixed bytes, which has no room for a list inside a list.
func listKind(v []any) (string, error) {
	kind := anyList
	for _, e := range v {
		var k string
		switch e.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			k = "INTEGERLIST"
		case float32, float64:
			k = "REALLIST"
		case bool:
			k = "BOOLEANLIST"
		case string:
			k = "TEXTLIST"
		default:
			return "", fmt.Errorf("list element %v of type %T is not one a zu1 list column holds", e, e)
		}
		if kind != anyList && kind != k {
			return "", fmt.Errorf("a list holds %s and %s; a zu1 list column holds one element type",
				kind, k)
		}
		kind = k
	}
	return kind, nil
}

// listText is the JSON array a staged list column holds, which is how a list
// crosses SQLite. The element types are the four Go kinds listKind admits, so
// the marshalling has nothing to decide.
func listText(v []any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("a list property has no JSON form: %w", err)
	}
	return string(raw), nil
}

// temporalKind is the declared type of a column holding t, and the place the
// two zoned kinds are refused.
//
// zu's property lanes carry a count and nothing beside it, so a zoned value
// has nowhere to keep its offset. Storing one as though it were local would
// load a different value than the fixture wrote and then answer cases about
// it, which is the failure worth avoiding here.
func temporalKind(t fixture.Temporal) (string, error) {
	switch t.Kind {
	case fixture.KindDate:
		return "DATE", nil
	case fixture.KindLocalTime:
		return "LOCALTIME", nil
	case fixture.KindLocalDateTime:
		return "LOCALDATETIME", nil
	case fixture.KindDuration:
		d, err := t.Duration()
		if err != nil {
			return "", err
		}
		if d.Months != 0 {
			if d.Days != 0 || d.Seconds != 0 || d.Nanos != 0 {
				return "", fmt.Errorf("duration %q mixes months with days and seconds; "+
					"the two unit groups are two types and a zu1 column holds one", t.Literal)
			}
			return "YEARMONTHDURATION", nil
		}
		return "DURATION", nil
	default:
		return "", fmt.Errorf("a %s carries a zone and a zu1 property column carries a count", t.Kind)
	}
}

// temporalCount is the integer a temporal value is stored as, in the unit its
// declared type names: days for a date, nanoseconds for a time, a datetime and
// a day-time duration, and months for a year-month one.
func temporalCount(t fixture.Temporal) (int64, error) {
	if t.Kind == fixture.KindDuration {
		d, err := t.Duration()
		if err != nil {
			return 0, err
		}
		if d.Months != 0 {
			return int64(d.Months), nil
		}
		return (int64(d.Days)*secondsPerDay+d.Seconds)*nanosPerSecond + int64(d.Nanos), nil
	}
	when, err := t.Time()
	if err != nil {
		return 0, err
	}
	switch t.Kind {
	case fixture.KindDate:
		// A date is a whole number of days and Go's division truncates
		// towards zero, so a date before the epoch has to floor instead.
		secs := when.Unix()
		days := secs / secondsPerDay
		if secs%secondsPerDay != 0 && secs < 0 {
			days--
		}
		return days, nil
	case fixture.KindLocalTime:
		h, m, sec := when.Clock()
		return (int64(h)*3600+int64(m)*60+int64(sec))*nanosPerSecond + int64(when.Nanosecond()), nil
	case fixture.KindLocalDateTime:
		return when.UnixNano(), nil
	default:
		return 0, fmt.Errorf("a %s carries a zone and a zu1 property column carries a count", t.Kind)
	}
}

const (
	secondsPerDay  = 24 * 60 * 60
	nanosPerSecond = 1000 * 1000 * 1000
)

func (t *nodeTable) create(ctx context.Context, tx *sql.Tx) error {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE n_%s (zrow INTEGER PRIMARY KEY", t.label)
	for i, c := range t.cols {
		fmt.Fprintf(&b, ", p_%s %s", c, t.types[i])
	}
	b.WriteString(");")
	return createTable(ctx, tx, "node", t.label, b.String(), nil, false)
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
	if t.extra == nil {
		return nil
	}
	labels, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO zu_labels (tbl, zrow, label) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer func() { _ = labels.Close() }()
	for zrow, set := range t.extra {
		for _, label := range set {
			// A node written with its own table's name twice is one
			// label, and the row carries it because the table does.
			if label == t.label {
				continue
			}
			if _, err := labels.ExecContext(ctx, t.label, int64(zrow), label); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *relTable) create(ctx context.Context, tx *sql.Tx) error {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE r_%s (zrel INTEGER PRIMARY KEY, src INTEGER NOT NULL, dst INTEGER NOT NULL",
		t.typ)
	for i, c := range t.cols {
		fmt.Fprintf(&b, ", p_%s %s", c, t.types[i])
	}
	fmt.Fprintf(&b, ");\nCREATE INDEX r_%[1]s_fwd ON r_%[1]s (src, dst);\nCREATE INDEX r_%[1]s_bwd ON r_%[1]s (dst, src);",
		t.typ)
	return createTable(ctx, tx, "rel", t.typ, b.String(), &[2]string{t.src, t.dst}, t.undirected)
}

func (t *relTable) fill(ctx context.Context, tx *sql.Tx) error {
	if len(t.edges) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf("INSERT INTO r_%s VALUES (NULL, ?, ?%s)",
		t.typ, strings.Repeat(", ?", len(t.cols))))
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for i, e := range t.edges {
		args := append([]any{e[0], e[1]}, t.rows[i]...)
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

// createTable runs a table's DDL and records it in the catalogue, which is
// where `zu convert` looks to find out what the file holds. The DDL text is
// stored verbatim beside the entry because that is what zu's own writer
// stores; the converter reads the kind, the name and the endpoints.
func createTable(ctx context.Context, tx *sql.Tx, kind, name, ddl string, endpoints *[2]string, undirected bool) error {
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("creating %s table %s: %w", kind, name, err)
	}
	var src, dst any
	if endpoints != nil {
		src, dst = endpoints[0], endpoints[1]
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO zu_catalog (kind, name, sql, src_table, dst_table, undirected) VALUES (?, ?, ?, ?, ?, ?)",
		kind, name, ddl, src, dst, undirected)
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
