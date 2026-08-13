package zu

import (
	"errors"
	"testing"

	"github.com/tamnd/gql-compat/adapter"
)

// The shell's envelope is a contract between two repositories, so these cases
// are written against literal lines rather than against a round trip through
// the shell. If zu changes the wire shape, this fails here with the actual
// bytes in the message instead of failing as a mysterious conformance
// regression a hundred cases later.
func TestDecodeCarriesTheCompletionCondition(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		line   string
		status string
		rows   int
	}{
		{
			name:   "a statement that projected",
			line:   `{"gqlstatus":"00000","columns":["n"],"rows":[[5]]}`,
			status: "00000",
			rows:   1,
		},
		{
			// An empty binding table is a successful completion, not no
			// data. The adapter reports what zu said rather than deriving a
			// code from the row count, which would grade this file instead
			// of the engine.
			name:   "a statement that matched nothing",
			line:   `{"gqlstatus":"00000","columns":["n"],"rows":[]}`,
			status: "00000",
			rows:   0,
		},
		{
			name:   "a statement with no projection",
			line:   `{"gqlstatus":"00001","columns":[],"rows":[]}`,
			status: "00001",
			rows:   0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := decode([]byte(tc.line))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.GQLStatus != tc.status {
				t.Errorf("GQLStatus = %q, want %q", got.GQLStatus, tc.status)
			}
			if len(got.Table.Rows) != tc.rows {
				t.Errorf("rows = %d, want %d", len(got.Table.Rows), tc.rows)
			}
		})
	}
}

func TestDecodeSeparatesRaisedConditionsFromProtocolFaults(t *testing.T) {
	t.Parallel()

	// A condition the engine raised carries the standard's code, and that is
	// what a condition case grades.
	_, err := decode([]byte(`{"error":"division by zero","failure":{"gqlstatus":"22012",` +
		`"condition":"data exception, division by zero","severity":"exception",` +
		`"message":"division by zero"}}`))
	var fail *adapter.Failure
	if !errors.As(err, &fail) {
		t.Fatalf("want *adapter.Failure, got %T: %v", err, err)
	}
	if fail.GQLStatus != "22012" {
		t.Errorf("GQLStatus = %q, want 22012", fail.GQLStatus)
	}
	if fail.Message != "division by zero" {
		t.Errorf("Message = %q", fail.Message)
	}

	// A protocol fault has no code and must not borrow one. Reporting a
	// GQLSTATUS here would let a malformed frame score as a correctly raised
	// condition.
	_, err = decode([]byte(`{"error":"bad frame"}`))
	if !errors.As(err, &fail) {
		t.Fatalf("want *adapter.Failure, got %T: %v", err, err)
	}
	if fail.GQLStatus != "" {
		t.Errorf("GQLStatus = %q, want empty for a protocol fault", fail.GQLStatus)
	}
}

func TestDecodeKeepsIntegersIntegral(t *testing.T) {
	t.Parallel()

	got, err := decode([]byte(`{"gqlstatus":"00000","columns":["a","b"],"rows":[[4,4.5]]}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v, ok := got.Table.Rows[0][0].(int64); !ok || v != 4 {
		t.Errorf("first cell = %#v, want int64(4)", got.Table.Rows[0][0])
	}
	if v, ok := got.Table.Rows[0][1].(float64); !ok || v != 4.5 {
		t.Errorf("second cell = %#v, want float64(4.5)", got.Table.Rows[0][1])
	}
}
