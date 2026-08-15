package zu

import (
	"strings"
	"testing"

	"github.com/tamnd/gql-compat/fixture"
)

// The declaration zu 0.0.1 writes, copied verbatim from `zu conformance
// --declare --format json`. Copied rather than generated on purpose: these
// tests are about what this parser does with a given text, and a test that
// shells out to whatever zu happens to be on PATH would pass or fail for
// reasons that have nothing to do with the code under it.
const declared = `{"engine":{"name":"zu","version":"0.0.1"},` +
	`"data":{"labels":true,"multi-label":false,"node-properties":true,` +
	`"edge-properties":true,"edge-types":true,"multiple-edge-types":true,` +
	`"multiple-node-labels":false,"temporal-values":true,"list-values":true,` +
	`"null-properties":true,"float-values":true,"boolean-values":true,` +
	`"undirected-edges":true,"self-loops":true,"parallel-edges":true,` +
	`"parallel-edge-properties":false},` +
	`"capabilities":{"gqlstatus":true,"parameters":true,"transactions":false,` +
	`"multiple-statements":true,"isolated":true},` +
	`"notes":["driven through ` + "`zu shell --format jsonl`" + `"]}`

func TestDeclarationBecomesCapabilities(t *testing.T) {
	caps, err := parseDeclaration([]byte(declared))
	if err != nil {
		t.Fatalf("a declaration zu actually writes was rejected: %v", err)
	}
	if len(caps.Data) != len(fixture.AllCapabilities) {
		t.Fatalf("got %d data capabilities, want %d", len(caps.Data), len(fixture.AllCapabilities))
	}
	if !caps.Data[fixture.CapLabels] || !caps.Data[fixture.CapSelfLoops] {
		t.Error("labels and self-loops are declared true and did not arrive true")
	}
	if !caps.Data[fixture.CapFloatValues] || !caps.Data[fixture.CapBooleanValues] {
		t.Error("float-values and boolean-values are declared true and did not arrive true")
	}
	if !caps.Data[fixture.CapUndirectedEdges] {
		t.Error("undirected-edges is declared true and did not arrive true")
	}
	if !caps.Data[fixture.CapEdgeProperties] {
		t.Error("edge-properties is declared true and did not arrive true")
	}
	if caps.Data[fixture.CapParallelEdgeProperties] {
		t.Error("parallel-edge-properties is declared false and arrived true")
	}
	if !caps.GQLStatus || !caps.Parameters || !caps.MultipleStatements || !caps.Isolated {
		t.Error("an engine flag declared true did not arrive true")
	}
	if caps.Transactions {
		t.Error("transactions is declared false and arrived true")
	}
	if len(caps.Notes) != 1 {
		t.Errorf("notes did not survive: %v", caps.Notes)
	}
}

func TestDeclarationRefusesWhatItCannotAccountFor(t *testing.T) {
	// Silence about a capability has to be as loud as a no. It is the shape
	// drift takes when the two repositories part company: nobody deletes a
	// flag, somebody adds one on the harness side and the engine never hears
	// about it, and every fixture needing it turns into a skip.
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a data capability the engine never mentions",
			in:   strings.Replace(declared, `"self-loops":true,`, "", 1),
			want: `not declared is not no`,
		},
		{
			name: "an engine flag the engine never mentions",
			in:   strings.Replace(declared, `"gqlstatus":true,`, "", 1),
			want: `says nothing about the "gqlstatus" flag`,
		},
		{
			name: "a data capability this harness has no fixtures for",
			in:   strings.Replace(declared, `"labels":true`, `"labels":true,"telepathy":true`, 1),
			want: `no fixtures for`,
		},
		{
			name: "an engine flag this harness does not read",
			in:   strings.Replace(declared, `"gqlstatus":true`, `"gqlstatus":true,"telepathy":true`, 1),
			want: `does not read`,
		},
		{
			name: "a top level field this harness does not read",
			in:   strings.Replace(declared, `"engine":{`, `"score":99,"engine":{`, 1),
			want: `not the shape this harness reads`,
		},
		{
			name: "not json at all",
			in:   "usage: zu conformance --declare\n",
			want: `not the shape this harness reads`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseDeclaration([]byte(c.in))
			if err == nil {
				t.Fatal("accepted a declaration it should have refused")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error does not say why: got %q, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestDeclarationCoversEveryCapabilityTheFixturesUse(t *testing.T) {
	// The one assertion that catches the interesting direction. If somebody
	// adds a capability to fixture.AllCapabilities, this fails until zu
	// declares it, rather than quietly skipping every fixture that needs it.
	caps, err := parseDeclaration([]byte(declared))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, c := range fixture.AllCapabilities {
		if _, ok := caps.Data[c]; !ok {
			t.Errorf("zu declares nothing about %q; teach conformance.toml about it", c)
		}
	}
}
