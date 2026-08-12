// Package rows is the common currency between an engine and an expectation:
// a table of normalised values, and the rules for deciding whether one table
// is the one a case asked for.
//
// Normalisation exists because the standard fixes what a query means and
// leaves a good deal of how a driver hands it back to the implementation. An
// engine that returns 30 where another returns 30.0, or that spells a node
// with different metadata, has not failed a conformance test; it has a
// different binding. What the standard does fix — the values, their order
// where an ORDER BY demands one, and the column names a projection
// establishes — is what Compare checks, and nothing else.
package rows

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Node is a vertex as an engine returned it.
type Node struct {
	Labels []string       `json:"labels,omitempty"`
	Props  map[string]any `json:"props,omitempty"`
}

// Edge is a relationship as an engine returned it.
type Edge struct {
	Type  string         `json:"type,omitempty"`
	Props map[string]any `json:"props,omitempty"`
}

// Path is an alternating node/edge sequence.
type Path struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Table is a query result.
type Table struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// Len reports the row count.
func (t *Table) Len() int {
	if t == nil {
		return 0
	}
	return len(t.Rows)
}

// Cells reports the total value count, which is what a bytes-per-row or
// values-per-second figure divides by.
func (t *Table) Cells() int {
	if t == nil {
		return 0
	}
	return len(t.Rows) * len(t.Columns)
}

// Options tune how strict a comparison is. The defaults are the loosest
// reading the standard permits; a case tightens them when the point of the
// case is the thing being loosened.
type Options struct {
	// Unordered compares rows as a multiset. Set it for any query without an
	// ORDER BY, because GQL promises no order without one.
	Unordered bool
	// StrictTypes stops 1 from equalling 1.0. Leave it off except in cases
	// about the integer and floating-point types themselves.
	StrictTypes bool
	// StrictColumns requires the engine's column names to equal the expected
	// ones. On by default in Compare; a case about aliasing wants it, and a
	// case about values does not care what the engine called the column.
	StrictColumns bool
	// FloatTolerance is the relative error two floats may differ by. Zero
	// means exact. Trigonometric and logarithmic features need a tolerance;
	// the standard does not specify a precision for them.
	FloatTolerance float64
}

// Diff describes why two tables are not the same. An empty Diff means equal.
type Diff struct {
	// Reason is one sentence naming the first difference found.
	Reason string
	// Row and Col locate it, or are -1 when the difference is about the shape
	// of the table rather than a value in it.
	Row, Col int
	// Want and Got are the two values, rendered.
	Want, Got string
}

func (d *Diff) Error() string {
	if d == nil {
		return ""
	}
	if d.Row >= 0 {
		return fmt.Sprintf("%s at row %d column %d: want %s, got %s", d.Reason, d.Row, d.Col, d.Want, d.Got)
	}
	if d.Want != "" || d.Got != "" {
		return fmt.Sprintf("%s: want %s, got %s", d.Reason, d.Want, d.Got)
	}
	return d.Reason
}

// Compare reports whether got is the table want describes, or the first
// difference if it is not.
func Compare(want, got *Table, opt Options) *Diff {
	if got == nil {
		return &Diff{Reason: "no result returned", Row: -1}
	}
	if opt.StrictColumns && len(want.Columns) > 0 {
		if len(want.Columns) != len(got.Columns) {
			return &Diff{
				Reason: "column count differs", Row: -1,
				Want: strconv.Itoa(len(want.Columns)), Got: strconv.Itoa(len(got.Columns)),
			}
		}
		for i := range want.Columns {
			if want.Columns[i] != got.Columns[i] {
				return &Diff{
					Reason: "column name differs", Row: -1, Col: i,
					Want: want.Columns[i], Got: got.Columns[i],
				}
			}
		}
	}
	if len(want.Rows) != len(got.Rows) {
		return &Diff{
			Reason: "row count differs", Row: -1,
			Want: strconv.Itoa(len(want.Rows)), Got: strconv.Itoa(len(got.Rows)),
		}
	}

	wantRows, gotRows := want.Rows, got.Rows
	if opt.Unordered {
		// Sorting by rendered form is a total order that does not depend on
		// the value types, which is what a multiset comparison over a table
		// holding nulls, nodes, and lists needs.
		wantRows = sortRows(wantRows)
		gotRows = sortRows(gotRows)
	}
	for r := range wantRows {
		if len(wantRows[r]) != len(gotRows[r]) {
			return &Diff{
				Reason: "row width differs", Row: r, Col: -1,
				Want: strconv.Itoa(len(wantRows[r])), Got: strconv.Itoa(len(gotRows[r])),
			}
		}
		for c := range wantRows[r] {
			if !equal(wantRows[r][c], gotRows[r][c], opt) {
				return &Diff{
					Reason: "value differs", Row: r, Col: c,
					Want: Render(wantRows[r][c]), Got: Render(gotRows[r][c]),
				}
			}
		}
	}
	return nil
}

func sortRows(in [][]any) [][]any {
	out := make([][]any, len(in))
	copy(out, in)
	keys := make([]string, len(out))
	for i, r := range out {
		keys[i] = RenderRow(r)
	}
	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
	sorted := make([][]any, len(out))
	for i, j := range idx {
		sorted[i] = out[j]
	}
	return sorted
}

func equal(want, got any, opt Options) bool {
	want, got = Normalize(want), Normalize(got)
	if want == nil || got == nil {
		return want == nil && got == nil
	}
	switch w := want.(type) {
	case int64:
		switch g := got.(type) {
		case int64:
			return w == g
		case float64:
			// An engine may hand back a whole number as a double. Unless the
			// case is about the distinction, that is a binding detail.
			return !opt.StrictTypes && float64(w) == g
		}
		return false
	case float64:
		switch g := got.(type) {
		case float64:
			return floatsEqual(w, g, opt.FloatTolerance)
		case int64:
			return !opt.StrictTypes && floatsEqual(w, float64(g), opt.FloatTolerance)
		}
		return false
	case string:
		g, ok := got.(string)
		return ok && w == g
	case bool:
		g, ok := got.(bool)
		return ok && w == g
	case []any:
		g, ok := got.([]any)
		if !ok || len(w) != len(g) {
			return false
		}
		for i := range w {
			if !equal(w[i], g[i], opt) {
				return false
			}
		}
		return true
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(w) != len(g) {
			return false
		}
		for k, wv := range w {
			gv, present := g[k]
			if !present || !equal(wv, gv, opt) {
				return false
			}
		}
		return true
	case Node:
		g, ok := got.(Node)
		return ok && sameStrings(w.Labels, g.Labels) && equal(toAnyMap(w.Props), toAnyMap(g.Props), opt)
	case Edge:
		g, ok := got.(Edge)
		return ok && w.Type == g.Type && equal(toAnyMap(w.Props), toAnyMap(g.Props), opt)
	case Path:
		g, ok := got.(Path)
		if !ok || len(w.Nodes) != len(g.Nodes) || len(w.Edges) != len(g.Edges) {
			return false
		}
		for i := range w.Nodes {
			if !equal(w.Nodes[i], g.Nodes[i], opt) {
				return false
			}
		}
		for i := range w.Edges {
			if !equal(w.Edges[i], g.Edges[i], opt) {
				return false
			}
		}
		return true
	}
	return Render(want) == Render(got)
}

func floatsEqual(a, b, tol float64) bool {
	if math.IsNaN(a) && math.IsNaN(b) {
		// The standard has no NaN literal, but engines produce one; treating
		// two NaNs as equal is the only reading under which a case about
		// them can be written at all.
		return true
	}
	if a == b {
		return true
	}
	if tol == 0 {
		return false
	}
	scale := math.Max(math.Abs(a), math.Abs(b))
	if scale == 0 {
		return true
	}
	return math.Abs(a-b)/scale <= tol
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func toAnyMap(m map[string]any) any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// Normalize collapses the numeric and container types the various drivers
// produce onto one set: nil, bool, int64, float64, string, []any,
// map[string]any, Node, Edge, Path.
//
// YAML gives ints as int, JSON as float64, Bolt as int64. Without this every
// expectation would have to be written once per driver, which is the same as
// having no expectations.
func Normalize(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case bool, string, int64, float64:
		return x
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case uint:
		return int64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	case float32:
		return float64(x)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = Normalize(x[i])
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out
	case []int64:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = Normalize(vv)
		}
		return out
	case map[any]any:
		// go-yaml can produce these for non-string keys; render the key so a
		// fixture with an integer-keyed map still compares.
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[Render(k)] = Normalize(vv)
		}
		return out
	case Node, Edge, Path:
		return x
	}
	return v
}

// Render writes a value in a stable, comparable text form. It is the sort key
// for unordered comparison and the string a diff prints, so two values that
// Render identically must compare equal.
func Render(v any) string {
	var b strings.Builder
	render(&b, Normalize(v))
	return b.String()
}

// RenderRow renders a whole row.
func RenderRow(r []any) string {
	var b strings.Builder
	b.WriteByte('(')
	for i, v := range r {
		if i > 0 {
			b.WriteByte(',')
		}
		render(&b, Normalize(v))
	}
	b.WriteByte(')')
	return b.String()
}

func render(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		// 'g' with -1 precision round-trips and keeps 1e100 from becoming a
		// hundred-character sort key.
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case string:
		b.WriteString(strconv.Quote(x))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			render(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		b.WriteByte('{')
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(k))
			b.WriteByte(':')
			render(b, x[k])
		}
		b.WriteByte('}')
	case Node:
		b.WriteString("(:")
		labels := append([]string{}, x.Labels...)
		sort.Strings(labels)
		b.WriteString(strings.Join(labels, ":"))
		render(b, toAnyMap(x.Props))
		b.WriteByte(')')
	case Edge:
		b.WriteString("[:")
		b.WriteString(x.Type)
		render(b, toAnyMap(x.Props))
		b.WriteByte(']')
	case Path:
		b.WriteString("<")
		for i := range x.Nodes {
			if i > 0 && i-1 < len(x.Edges) {
				render(b, x.Edges[i-1])
			}
			render(b, x.Nodes[i])
		}
		b.WriteString(">")
	default:
		fmt.Fprintf(b, "%v", x)
	}
}
