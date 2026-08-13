package grammar

import (
	"bytes"
	_ "embed"

	"github.com/tamnd/gql-compat/iso/artifacts"
)

//go:embed promoted.yaml
var embeddedPromoted []byte

// Load parses the grammar artifact vendored in iso/artifacts.
//
// It is the same file the iso package reads, and reading it twice is
// deliberate: iso keeps what a citation check needs and this package keeps the
// shape a walk needs, and neither should have to carry the other's
// representation. Both are decoded from the one embedded copy, so there is no
// way for a walk and a citation to disagree about what the published grammar
// says.
func Load() (*Grammar, error) { return Parse(bytes.NewReader(artifacts.GrammarXML)) }

// LoadEmbeddedPromoted reads the promotion list that ships with this package.
func LoadEmbeddedPromoted() (*Promoted, error) {
	return ParsePromoted(embeddedPromoted, "promoted.yaml")
}

// EmbeddedPromoted returns the shipped promotion list verbatim, for a caller
// keeping their own alongside it.
func EmbeddedPromoted() []byte { return append([]byte(nil), embeddedPromoted...) }
