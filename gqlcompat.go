// Package gqlcompat measures how closely a graph database implements
// ISO/IEC 39075:2024, the GQL standard, and what it costs to do so.
//
// ISO publishes no executable conformance test. What it publishes is a set of
// digital artifacts — the grammar, the 228 optional feature codes, the
// GQLSTATUS conditions, the subclause structure — and those artifacts are
// vendored here, unmodified, in the iso package. Every claim this library
// makes about the standard is a reference into one of them, and every
// reference is checked when the corpus loads. A case that cites a production
// the grammar does not define will not load at all.
//
// The package is the front door. Everything under it can be used directly —
// a caller who wants their own corpus, their own adapter, or their own
// reporting reaches past this file — but the three-line version is here:
//
//	std, err := gqlcompat.Load()
//	rep, err := std.Run(ctx, driver, runner.Config{})
//	err = report.Write(os.Stdout, rep, report.FormatMarkdown)
//
// There is no internal/ directory. The CLI in cmd/gql-compat is written
// entirely against these exported APIs, so anything it can do, a program
// importing this module can do too.
package gqlcompat

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"

	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/grammar"
	"github.com/tamnd/gql-compat/impdef"
	"github.com/tamnd/gql-compat/iso"
	"github.com/tamnd/gql-compat/runner"
)

// Standard is a loaded corpus together with the ISO catalogue it was checked
// against.
//
// The two travel together because neither is meaningful alone. The corpus
// without the catalogue is a pile of queries citing clause numbers nobody
// verified; the catalogue without the corpus is a vocabulary with nothing said
// in it. Holding both is also what lets a report state a denominator that came
// from ISO rather than from the suite: 41 of 228 features, not 41 of 41.
type Standard struct {
	// Suite is the cases.
	Suite *corpus.Suite
	// Fixtures is the graphs they run against.
	Fixtures *fixture.Set
	// Catalog is the vendored ISO vocabulary.
	Catalog *iso.Catalog
	// Probes are the questions about behaviour the standard leaves open. They
	// are not cases and are never scored; see the impdef package.
	Probes *impdef.Set
	// Grammar is the published BNF as a tree, ready to be walked. It is not
	// the corpus and a walk of it is not a conformance result; see the grammar
	// package.
	Grammar *grammar.Grammar
	// Promoted is the record of which leads from earlier walks review has
	// already dealt with.
	Promoted *grammar.Promoted
}

// Load returns the corpus embedded in this module.
//
// Embedding rather than reading from disk is what makes a result portable: a
// program that vendors gql-compat runs the same cases the CLI runs, so two
// reports produced by different programs are comparable.
func Load() (*Standard, error) {
	cat, err := iso.Load()
	if err != nil {
		return nil, fmt.Errorf("loading the ISO catalogue: %w", err)
	}
	suite, fixtures, err := corpus.LoadEmbedded(iso.Codes{Catalog: cat})
	if err != nil {
		return nil, fmt.Errorf("loading the embedded corpus: %w", err)
	}
	std, err := newStandard(cat, suite, fixtures)
	if err != nil {
		return nil, err
	}
	for _, name := range std.Probes.Fixtures() {
		if _, ok := fixtures.Get(name); !ok {
			return nil, fmt.Errorf("a shipped probe names unknown fixture %q", name)
		}
	}
	if err := checkProbeLabels(std.Probes, fixtures); err != nil {
		return nil, err
	}
	return std, nil
}

// patternLabel finds the labels a statement writes inside a node or an edge
// pattern.
//
// The colon is anchored to the bracket that opens the pattern so that a
// property map cannot be read as a label: `(v:Datum {n: 1})` names one label
// and the `n:` inside the braces names a property key. Everything else a colon
// can introduce is out of reach of this regexp for the same reason.
var patternLabel = regexp.MustCompile(`[(\[]\s*\w*\s*:\s*([A-Za-z_]\w*)`)

// checkProbeLabels rejects a shipped probe that matches a label its fixture
// does not declare.
//
// A probe has no expectation, so nothing about it fails: a statement that
// matches nothing observes nothing and the report says the engine returned no
// records, which reads exactly like an engine that has no answer to give. Three
// probes shipped in that state after a fixture was renamed, and two of them
// were reported as refusals against every engine measured. Silence is the only
// failure this package has, so the one kind of silence that is our own fault is
// worth refusing to load with.
//
// The check is on the shipped pair only, the way the fixture name check above
// is. A caller running the shipped probes against their own corpus may well
// have a fixture of the same name holding different labels, and that is their
// business rather than a defect in this module.
func checkProbeLabels(probes *impdef.Set, fixtures *fixture.Set) error {
	for _, p := range probes.Probes {
		if p.Fixture == "" {
			continue
		}
		f, ok := fixtures.Get(p.Fixture)
		if !ok {
			continue
		}
		have := declaredLabels(f)
		for _, m := range patternLabel.FindAllStringSubmatch(p.Statement, -1) {
			if !have[m[1]] {
				return fmt.Errorf("probe %s matches label %q, which fixture %q does not declare", p.ID, m[1], f.Name)
			}
		}
	}
	return nil
}

// declaredLabels is every label and edge type a fixture holds, including the
// two a generated graph carries rather than writing out.
func declaredLabels(f *fixture.Fixture) map[string]bool {
	have := map[string]bool{}
	for _, n := range f.Nodes {
		for _, l := range n.Labels {
			have[l] = true
		}
	}
	for _, e := range f.Edges {
		have[e.Type] = true
	}
	if f.Generated != nil {
		have[f.Generated.Label] = true
		have[f.Generated.EdgeType] = true
	}
	return have
}

// LoadFS returns a corpus read from root, checked against the same vendored
// catalogue.
//
// A caller with cases of their own — an engine's own regression suite, a
// vendor's claimed-feature list — gets the same validation the shipped corpus
// gets, which is the point of exposing this rather than the raw loader: there
// is no way to load a case that cites something ISO does not define.
func LoadFS(root fs.FS) (*Standard, error) {
	cat, err := iso.Load()
	if err != nil {
		return nil, fmt.Errorf("loading the ISO catalogue: %w", err)
	}
	suite, fixtures, err := corpus.Load(root, iso.Codes{Catalog: cat})
	if err != nil {
		return nil, fmt.Errorf("loading the corpus: %w", err)
	}
	return newStandard(cat, suite, fixtures)
}

// newStandard attaches the shipped probes to a loaded corpus.
//
// The probes are loaded here rather than beside the corpus because they are not
// cases and the corpus loader must stay unable to load one. What they do share
// is the fixtures, and a probe naming a fixture the corpus does not define
// simply observes nothing: that is a legitimate state for a caller running
// their own cases against the shipped probes, and only a bug when both sides
// shipped together, which is what Load checks and LoadFS does not.
func newStandard(cat *iso.Catalog, suite *corpus.Suite, fixtures *fixture.Set) (*Standard, error) {
	probes, err := impdef.LoadEmbedded(iso.Codes{Catalog: cat})
	if err != nil {
		return nil, fmt.Errorf("loading the implementation-defined probes: %w", err)
	}
	bnf, err := grammar.Load()
	if err != nil {
		return nil, fmt.Errorf("parsing the grammar artifact: %w", err)
	}
	promoted, err := grammar.LoadEmbeddedPromoted()
	if err != nil {
		return nil, fmt.Errorf("loading the promotion list: %w", err)
	}
	return &Standard{
		Suite:    suite,
		Fixtures: fixtures,
		Catalog:  cat,
		Probes:   probes,
		Grammar:  bnf,
		Promoted: promoted,
	}, nil
}

// Run measures one engine against this corpus.
//
// The Suite, Fixtures, and Catalog fields of cfg are supplied from the
// Standard and any values already in them are overwritten — a run against a
// catalogue other than the one the corpus was validated against would produce
// coverage percentages over the wrong denominator. Everything else in cfg is
// the caller's: repeats, warmups, timeout, selector, mode, working directory.
//
// It returns an error only when the run could not start. An engine that fails
// every case yields a full report and a nil error, because that is a
// measurement and not a malfunction.
//
// The grammar walk runs only when the caller asks for it, by setting
// cfg.Explore with a Count above zero. The grammar and the promotion list are
// filled in from the Standard when the caller left them nil, so asking for a
// walk takes a count and nothing else.
func (s *Standard) Run(ctx context.Context, d adapter.Driver, cfg runner.Config) (*runner.Report, error) {
	cfg.Driver = d
	cfg.Suite = s.Suite
	cfg.Fixtures = s.Fixtures
	cfg.Catalog = s.Catalog
	if cfg.Probes == nil {
		cfg.Probes = s.Probes
	}
	if cfg.Explore != nil {
		// Copied rather than filled in place: cfg is the caller's value but
		// Explore is their pointer, and a library that quietly wrote the
		// shipped grammar into a caller's struct would make a second run with
		// the same config mean something different from the first.
		ex := *cfg.Explore
		if ex.Grammar == nil {
			ex.Grammar = s.Grammar
		}
		if ex.Promoted == nil {
			ex.Promoted = s.Promoted
		}
		cfg.Explore = &ex
	}
	return runner.Run(ctx, cfg)
}
