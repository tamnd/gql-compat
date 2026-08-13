package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	gqlcompat "github.com/tamnd/gql-compat"
	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/runner"
)

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: gql-compat list [what] [flags]

  cases      the corpus, filtered by the same flags "run" takes (default)
  fixtures   the graphs the cases run against
  adapters   the engines this binary can drive

`)
		fs.PrintDefaults()
	}
	var (
		pattern  = fs.String("run", "", "regular expression over case ids")
		corpusIn = fs.String("corpus", "", "directory of case files; empty uses the embedded corpus")
		asJSON   = fs.Bool("json", false, "emit JSON instead of a table")
		long     = fs.Bool("l", false, "include the ISO references each case claims")
		large    = fs.Bool("large", false, "include the cases whose fixtures are big enough to measure storage density on")
	)
	var kinds, features, tags, skipTags stringList
	fs.Var(&kinds, "kind", "limit to a kind (repeatable)")
	fs.Var(&features, "feature", "limit to cases claiming an ISO feature code (repeatable)")
	fs.Var(&tags, "tag", "limit to cases carrying a tag (repeatable)")
	fs.Var(&skipTags, "skip-tag", "exclude cases carrying a tag (repeatable)")

	what := "cases"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		what, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if what == "adapters" {
		for _, n := range adapter.Registered() {
			fmt.Println(n)
		}
		return nil
	}

	std, err := loadStandard(*corpusIn)
	if err != nil {
		return err
	}

	switch what {
	case "fixtures":
		return listFixtures(std, *asJSON)
	case "cases":
		sel, err := runner.ParseSelector(*pattern, kinds, features, tags, skipTags, *large)
		if err != nil {
			return err
		}
		return listCases(std.Suite.Filter(sel), *asJSON, *long)
	}
	return fmt.Errorf("unknown list target %q; want cases, fixtures, or adapters", what)
}

func listCases(cases []*corpus.Case, asJSON, long bool) error {
	if asJSON {
		return writeJSON(cases)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, c := range cases {
		fmt.Fprintf(w, "%s\t%s\t%s", c.Kind, c.ID, c.Name)
		if long {
			fmt.Fprintf(w, "\t%s", strings.Join(references(c), " "))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "\n%d case(s)\n", len(cases))
	return w.Flush()
}

// references renders what a case claims about the standard, which is the
// column a reader checking coverage actually wants: not the query, but which
// clause and which feature the query is evidence about.
func references(c *corpus.Case) []string {
	out := make([]string, 0, len(c.Features)+len(c.Subclauses)+len(c.Conditions)+len(c.Requires))
	out = append(out, c.Features...)
	for _, s := range c.Subclauses {
		out = append(out, "§"+s)
	}
	out = append(out, c.Conditions...)
	for _, r := range c.Requires {
		out = append(out, "needs:"+r)
	}
	return out
}

func listFixtures(std *gqlcompat.Standard, asJSON bool) error {
	names := std.Fixtures.Names()
	if asJSON {
		type row struct {
			Name        string   `json:"name"`
			Description string   `json:"description,omitempty"`
			Nodes       int      `json:"nodes"`
			Edges       int      `json:"edges"`
			Requires    []string `json:"requires,omitempty"`
			Generated   bool     `json:"generated,omitempty"`
		}
		var out []row
		for _, n := range names {
			f, _ := std.Fixtures.Get(n)
			built, err := f.Materialize()
			if err != nil {
				return err
			}
			r := row{Name: f.Name, Description: f.Description,
				Nodes: len(built.Nodes), Edges: len(built.Edges), Generated: f.Generated != nil}
			for _, c := range built.RequiredList() {
				r.Requires = append(r.Requires, string(c))
			}
			out = append(out, r)
		}
		return writeJSON(out)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FIXTURE\tNODES\tEDGES\tREQUIRES")
	for _, n := range names {
		f, _ := std.Fixtures.Get(n)
		// Materialising a generated fixture here is the point of the listing:
		// a shape and a node count in YAML says nothing about how many edges a
		// power-law graph with that seed actually has.
		built, err := f.Materialize()
		if err != nil {
			return err
		}
		var reqs []string
		for _, c := range built.RequiredList() {
			reqs = append(reqs, string(c))
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\n", f.Name, len(built.Nodes), len(built.Edges), strings.Join(reqs, ", "))
	}
	return w.Flush()
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
