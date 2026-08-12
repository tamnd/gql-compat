package report

import (
	"encoding/xml"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/runner"
)

// JUnit is the root element CI systems look for.
type JUnit struct {
	XMLName  xml.Name    `xml:"testsuites"`
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Errors   int         `xml:"errors,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Time     float64     `xml:"time,attr"`
	Suites   []JUnitCase `xml:"testsuite"`
}

// JUnitCase is one kind's suite. Grouping by kind rather than by file is what
// makes a CI summary say "mandatory: 2 failures" instead of naming a path.
type JUnitCase struct {
	Name       string          `xml:"name,attr"`
	Tests      int             `xml:"tests,attr"`
	Failures   int             `xml:"failures,attr"`
	Errors     int             `xml:"errors,attr"`
	Skipped    int             `xml:"skipped,attr"`
	Time       float64         `xml:"time,attr"`
	Properties []JUnitProperty `xml:"properties>property,omitempty"`
	Cases      []JUnitTest     `xml:"testcase"`
}

// JUnitProperty carries the run's context so a CI artifact is self-describing.
type JUnitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// JUnitTest is one case.
type JUnitTest struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      float64       `xml:"time,attr"`
	Failure   *JUnitFailure `xml:"failure,omitempty"`
	Error     *JUnitFailure `xml:"error,omitempty"`
	Skipped   *JUnitSkipped `xml:"skipped,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

// JUnitFailure is a verdict against the engine.
type JUnitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

// JUnitSkipped is a case the engine was never asked.
type JUnitSkipped struct {
	Message string `xml:"message,attr"`
}

// WriteJUnit renders the report as JUnit XML.
//
// Skips map to <skipped> rather than to passes, which matters: a CI dashboard
// that showed a capability-blocked case as green would be reporting the
// opposite of what the run found. The per-case <system-out> carries the
// statement and its latency summary, so a red build has the evidence attached
// without anyone opening the JSON.
func WriteJUnit(w io.Writer, rep *runner.Report) error {
	suites := map[corpus.Kind]*JUnitCase{}
	order := []corpus.Kind{}

	root := &JUnit{
		Name: "gql-compat/" + rep.Engine.Adapter,
		Time: rep.Run.Wall.Seconds(),
	}

	for i := range rep.Cases {
		c := &rep.Cases[i]
		s, ok := suites[c.Kind]
		if !ok {
			s = &JUnitCase{
				Name: string(c.Kind),
				Properties: []JUnitProperty{
					{Name: "engine", Value: rep.Engine.Adapter},
					{Name: "version", Value: rep.Engine.Version},
					{Name: "mode", Value: string(rep.Run.Mode)},
					{Name: "host", Value: rep.Host.Platform},
					{Name: "iso.source", Value: rep.Run.ISOSource},
				},
			}
			suites[c.Kind] = s
			order = append(order, c.Kind)
		}
		s.Cases = append(s.Cases, junitCase(c))
		s.Tests++
		s.Time += c.Wall.Seconds()
		switch c.Outcome {
		case runner.Fail:
			s.Failures++
		case runner.Error:
			s.Errors++
		case runner.Skip:
			s.Skipped++
		}
	}

	for _, k := range corpus.AllKinds {
		s, ok := suites[k]
		if !ok {
			continue
		}
		root.Suites = append(root.Suites, *s)
		root.Tests += s.Tests
		root.Failures += s.Failures
		root.Errors += s.Errors
		root.Skipped += s.Skipped
	}
	// A kind the corpus invented but AllKinds does not list would otherwise
	// vanish from the CI artifact while still being in the JSON.
	for _, k := range order {
		if validCorpusKind(k) {
			continue
		}
		s := suites[k]
		root.Suites = append(root.Suites, *s)
		root.Tests += s.Tests
		root.Failures += s.Failures
		root.Errors += s.Errors
		root.Skipped += s.Skipped
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(root); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func junitCase(c *runner.CaseResult) JUnitTest {
	t := JUnitTest{
		Name:      c.ID + " — " + c.Name,
		Classname: className(c),
		Time:      c.Stats.Mean.Seconds(),
		SystemOut: systemOut(c),
	}
	switch c.Outcome {
	case runner.Fail:
		t.Failure = &JUnitFailure{
			Message: oneLine(c.Reason),
			Type:    string(c.Evidence),
			Body:    detail(c),
		}
	case runner.Error:
		t.Error = &JUnitFailure{
			Message: oneLine(c.Reason),
			Type:    "harness",
			Body:    detail(c),
		}
	case runner.Skip:
		t.Skipped = &JUnitSkipped{Message: string(c.Skip) + ": " + oneLine(c.Reason)}
	}
	return t
}

// className is the case id minus its last segment, which gives a CI tree that
// groups by feature family instead of listing a thousand flat names.
func className(c *runner.CaseResult) string {
	parts := strings.Split(c.ID, "/")
	if len(parts) <= 1 {
		return string(c.Kind)
	}
	return strings.Join(parts[:len(parts)-1], ".")
}

func systemOut(c *runner.CaseResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "statement: %s\n", oneLine(c.Statement))
	if claims := claimList(c); claims != "" {
		fmt.Fprintf(&b, "claims: %s\n", claims)
	}
	if c.Stats.Count > 0 {
		fmt.Fprintf(&b, "latency: n=%d min=%s p50=%s p99=%s max=%s\n",
			c.Stats.Count, c.Stats.Min, c.Stats.P50, c.Stats.P99, c.Stats.Max)
	}
	if c.Load != nil {
		fmt.Fprintf(&b, "load: %d nodes, %d edges in %s\n", c.Load.Nodes, c.Load.Edges, c.Load.Wall)
	}
	return b.String()
}

func detail(c *runner.CaseResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n", c.Reason, strings.TrimSpace(c.Statement))
	if c.WantStatus != "" || c.GotStatus != "" {
		fmt.Fprintf(&b, "\nexpected GQLSTATUS %s, got %s\n",
			fallback(c.WantStatus, "any failure"), fallback(c.GotStatus, "none"))
	}
	if c.Message != "" {
		fmt.Fprintf(&b, "\nengine: %s\n", c.Message)
	}
	if d := c.Diff; d != nil {
		fmt.Fprintf(&b, "\nrow %d column %d: want %v, got %v\n", d.Row, d.Col, d.Want, d.Got)
	}
	return b.String()
}

func validCorpusKind(k corpus.Kind) bool { return slices.Contains(corpus.AllKinds, k) }
