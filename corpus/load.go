package corpus

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/tamnd/gql-compat/fixture"
)

// file is the on-disk shape of a corpus document. Fixtures and cases share a
// file so a small feature's graph sits next to the cases that use it, and a
// large shared graph can live in its own.
type file struct {
	Fixtures []*fixture.Fixture `yaml:"fixtures"`
	Cases    []*Case            `yaml:"cases"`
	// Defaults are applied to every case in the file that leaves the field
	// unset. Nothing else inherits: a case's claims are always its own.
	Defaults struct {
		Kind    Kind     `yaml:"kind"`
		Fixture string   `yaml:"fixture"`
		Tags    []string `yaml:"tags"`
	} `yaml:"defaults"`
}

// Load reads every .yaml file under root, validates what it finds against the
// ISO catalogue, and returns the suite and its fixtures.
//
// Loading is all-or-nothing. A corpus with one malformed case is a corpus
// whose totals cannot be trusted, and a run that silently dropped a file
// would report a conformance percentage over a denominator nobody chose.
func Load(root fs.FS, known KnownCodes) (*Suite, *fixture.Set, error) {
	var (
		cases    []*Case
		fixtures []*fixture.Fixture
		paths    []string
	)
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml":
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	// Walk order is already lexical, but sorting makes that a promise rather
	// than a property of the filesystem: case ids collide deterministically.
	sort.Strings(paths)

	for _, path := range paths {
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		var f file
		if err := yaml.UnmarshalWithOptions(data, &f, yaml.Strict()); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		fixtures = append(fixtures, f.Fixtures...)
		for _, c := range f.Cases {
			c.Source = path
			if c.Kind == "" {
				c.Kind = f.Defaults.Kind
			}
			if c.Fixture == "" {
				c.Fixture = f.Defaults.Fixture
			}
			c.Tags = mergeTags(f.Defaults.Tags, c.Tags)
			cases = append(cases, c)
		}
	}

	fxs, err := fixture.NewSet(fixtures)
	if err != nil {
		return nil, nil, err
	}
	suite, err := NewSuite(cases, known)
	if err != nil {
		return nil, nil, err
	}
	// Every case that names a fixture must name one that exists, or the run
	// would report skips that are really typos.
	for _, c := range suite.Cases {
		if c.Fixture == "" {
			continue
		}
		if _, ok := fxs.Get(c.Fixture); !ok {
			return nil, nil, fmt.Errorf("%s: case %s names unknown fixture %q", c.Source, c.ID, c.Fixture)
		}
	}
	return suite, fxs, nil
}

func mergeTags(defaults, own []string) []string {
	if len(defaults) == 0 {
		return own
	}
	seen := make(map[string]bool, len(defaults)+len(own))
	out := make([]string, 0, len(defaults)+len(own))
	for _, t := range append(append([]string{}, defaults...), own...) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
