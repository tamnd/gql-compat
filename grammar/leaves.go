package grammar

import "sort"

// Leaf is where the walk stops and writes a token of its own.
//
// The grammar goes all the way down to the character: an identifier is an
// identifier start followed by identifier extends, a digit is a standard digit
// or an other digit, and 23 productions at the bottom of that descent are
// defined in prose rather than in BNF. A generator that walked to the bottom
// would have to invent the lexical layer anyway, and would have to invent it
// twice, because it would also have to decide where one token ends and the next
// begins. So the walk stops at the tokens below and writes one of the spellings
// given here.
//
// This is the largest hand-written thing in the package and the honest name for
// it is a curation. Two consequences, both of which the report has to carry:
// the statements are made of a small alphabet of names and values, so a defect
// that only shows on an identifier of a certain shape is not reachable from
// here; and a production that only occurs inside one of these tokens is never
// exercised, which is why Coverage counts them as cut rather than as covered.
type Leaf struct {
	// Rule is the production the walk stops at.
	Rule string
	// Tokens are the spellings, chosen in order by the seeded stream. Keeping
	// more than one matters: an engine that folds case or normalises a literal
	// behaves differently on 'a' and on '', and one spelling would hide it.
	Tokens []string
}

// leaves is the cut, in production order rather than alphabetical, because it
// reads as a description of the lexical layer that way.
var leaves = []Leaf{
	// Names. Everything ISO builds out of <identifier> lands here: a graph
	// name, a label, a property, a binding variable. They are deliberately
	// short and unquoted, and they deliberately overlap, so that a generated
	// statement stands a chance of being semantically meaningful as well as
	// syntactically valid: a MATCH that binds `a` and a RETURN that projects
	// `a` is a statement an engine can actually run.
	{"identifier", []string{"a", "b", "x", "n1"}},
	{"regular identifier", []string{"a", "b", "x", "n1"}},
	{"non-delimited identifier", []string{"a", "b", "x"}},
	{"extended identifier", []string{"a", "b", "x"}},
	{"separated identifier", []string{"a", "b"}},
	{"delimited identifier", []string{"`a`", "`a b`"}},

	// Numbers. The spellings are the ones every engine has to read, and the
	// underscore separator is in the grammar and is worth sending.
	{"unsigned decimal integer", []string{"0", "1", "42", "1_000"}},
	{"unsigned integer", []string{"0", "1", "42"}},
	{"unsigned hexadecimal integer", []string{"0xff"}},
	{"unsigned octal integer", []string{"0o17"}},
	{"unsigned binary integer", []string{"0b101"}},
	{"exact numeric literal", []string{"1", "1.5"}},
	{"approximate numeric literal", []string{"1.5e3"}},
	{"unsigned decimal in common notation", []string{"1.5"}},
	{"unsigned decimal in scientific notation", []string{"1.5e3"}},
	{"digit", []string{"0", "7"}},
	{"standard digit", []string{"0", "7"}},
	{"hex digit", []string{"a", "f", "0"}},
	{"octal digit", []string{"7"}},
	{"binary digit", []string{"1"}},

	// Strings and bytes.
	{"character string literal", []string{"'s'", "\"s\"", "''"}},
	{"single quoted character sequence", []string{"'s'"}},
	{"double quoted character sequence", []string{"\"s\""}},
	{"accent quoted character sequence", []string{"`s`"}},
	{"byte string literal", []string{"X'4a'"}},

	// Temporal values. The strings inside them are spelled out by productions
	// this cut removes, and an engine that reads a date at all reads these.
	{"date string", []string{"'2026-01-01'"}},
	{"time string", []string{"'12:00:00'"}},
	{"datetime string", []string{"'2026-01-01T12:00:00'"}},
	{"duration string", []string{"'P1D'"}},

	// Comments and whitespace are not written at all. A generator that emitted
	// a comment would be testing the lexer's comment handling, which the
	// hand-written corpus does on purpose and with an expectation.
	{"comment", nil},
	{"separator", nil},
	{"whitespace", nil},
}

// leafIndex is the cut as a map, built once.
var leafIndex = func() map[string]Leaf {
	m := make(map[string]Leaf, len(leaves))
	for _, l := range leaves {
		m[l.Rule] = l
	}
	return m
}()

// Leaves returns the cut, sorted by production name, so a caller can print what
// the generator is and is not able to write.
func Leaves() []Leaf {
	out := append([]Leaf(nil), leaves...)
	sort.Slice(out, func(i, j int) bool { return out[i].Rule < out[j].Rule })
	return out
}

// Cut reports whether the walk stops at a production, and with what.
func Cut(rule string) (Leaf, bool) {
	l, ok := leafIndex[rule]
	return l, ok
}
