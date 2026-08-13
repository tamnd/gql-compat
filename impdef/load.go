package impdef

import (
	_ "embed"
	"fmt"

	"github.com/goccy/go-yaml"
)

//go:embed probes.yaml
var embedded []byte

// file is the on-disk shape of a probe document.
type file struct {
	Probes []*Probe `yaml:"probes"`
}

// Load reads a probe document and validates it against the ISO catalogue.
//
// Loading is all-or-nothing for the same reason the corpus loader is: a probe
// citing an item number nobody checked would put a claim about the standard in
// a document a vendor is invited to paste into a conformance statement.
func Load(data []byte, known KnownItems) (*Set, error) {
	var f file
	if err := yaml.UnmarshalWithOptions(data, &f, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("probes: %w", err)
	}
	return New(f.Probes, known)
}

// LoadEmbedded loads the probes that ship with this package.
func LoadEmbedded(known KnownItems) (*Set, error) { return Load(embedded, known) }

// Embedded returns the shipped probe document verbatim, for a caller who wants
// to extend it rather than replace it.
func Embedded() []byte { return append([]byte(nil), embedded...) }
