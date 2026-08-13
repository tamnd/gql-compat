package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/iso"
)

// cmdValidate loads a corpus and reports what it covers.
//
// Loading is the validation: corpus.Load rejects a case that cites a feature
// code, production, GQLSTATUS, or subclause the vendored artifacts do not
// define, so a corpus that loads has already had every ISO reference in it
// checked. What this command adds is the other direction — what the corpus
// does not cover — which no load error can tell you, because a missing case is
// not an error in any case.
func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: gql-compat validate [flags]

Loads a corpus, checking every ISO reference in it, and prints coverage
against the standard's own denominators.

`)
		fs.PrintDefaults()
	}
	var (
		corpusIn = fs.String("corpus", "", "directory of case files; empty uses the embedded corpus")
		asJSON   = fs.Bool("json", false, "emit JSON instead of a table")
		missing  = fs.Bool("missing", false, "list the features, conditions and subclauses no case claims")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	std, err := loadStandard(*corpusIn)
	if err != nil {
		// A load failure here is the validation failing, and its message
		// already names the file, the case, and the bad reference.
		return err
	}

	claimed := map[string]bool{}
	for _, f := range std.Suite.CoveredFeatures() {
		claimed[f] = true
	}
	byKind := map[corpus.Kind]int{}
	for _, c := range std.Suite.Cases {
		byKind[c.Kind]++
	}
	conditions := len(std.Suite.CoveredConditions())
	productions := len(std.Suite.CoveredProductions())
	subclauses := len(std.Suite.CoveredSubclauses())

	totalConditions := 0
	for _, c := range std.Catalog.Classes {
		totalConditions += len(c.Subclasses)
	}
	normative := len(std.Catalog.NormativeSubclauses())

	if *asJSON {
		type out struct {
			Cases            int                 `json:"cases"`
			ByKind           map[corpus.Kind]int `json:"by_kind"`
			Fixtures         int                 `json:"fixtures"`
			Features         int                 `json:"features_claimed"`
			FeaturesTotal    int                 `json:"features_total"`
			Conditions       int                 `json:"conditions_claimed"`
			ConditionsTotal  int                 `json:"conditions_total"`
			Productions      int                 `json:"productions_claimed"`
			ProductionsTotal int                 `json:"productions_total"`
			Subclauses       int                 `json:"subclauses_claimed"`
			SubclausesTotal  int                 `json:"normative_subclauses_total"`
			Unclaimed        []string            `json:"unclaimed_features,omitempty"`
		}
		o := out{
			Cases: std.Suite.Len(), ByKind: byKind, Fixtures: std.Fixtures.Len(),
			Features: len(claimed), FeaturesTotal: len(std.Catalog.Features),
			Conditions: conditions, ConditionsTotal: totalConditions,
			Productions: productions, ProductionsTotal: len(std.Catalog.Productions),
			Subclauses: subclauses, SubclausesTotal: normative,
		}
		if *missing {
			o.Unclaimed = unclaimed(std.Catalog, claimed)
		}
		return writeJSON(o)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%d cases loaded; every ISO reference in them resolves.\n\n", std.Suite.Len())
	for _, k := range corpus.AllKinds {
		fmt.Fprintf(w, "%s\t%d\n", k, byKind[k])
	}
	fmt.Fprintf(w, "fixtures\t%d\n", std.Fixtures.Len())
	fmt.Fprintln(w)
	fmt.Fprintln(w, "COVERAGE\tCLAIMED\tISO TOTAL")
	fmt.Fprintf(w, "optional features\t%d\t%d\n", len(claimed), len(std.Catalog.Features))
	fmt.Fprintf(w, "GQLSTATUS codes\t%d\t%d\n", conditions, totalConditions)
	fmt.Fprintf(w, "grammar productions\t%d\t%d\n", productions, len(std.Catalog.Productions))
	fmt.Fprintf(w, "normative subclauses\t%d\t%d\n", subclauses, normative)
	fmt.Fprintf(w, "\nThe totals are ISO's, not the corpus's. A corpus that tested twelve\n"+
		"features should read as twelve of 228, and a claim of full coverage\n"+
		"would mean 228 cases' worth of evidence that does not exist.\n")
	if err := w.Flush(); err != nil {
		return err
	}

	if *missing {
		fmt.Println("\nfeature codes no case claims:")
		for _, code := range unclaimed(std.Catalog, claimed) {
			f, _ := std.Catalog.Feature(code)
			fmt.Printf("  %-6s %s\n", code, f.Description)
		}
		// The other two denominators are worth the same treatment. A reader
		// who wants to close the gap needs the names of what is open, and
		// counting down from 68 or 317 by hand is how a corpus ends up with
		// two cases for one code and none for the next.
		haveCondition := set(std.Suite.CoveredConditions())
		fmt.Println("\nGQLSTATUS codes no case asserts:")
		for _, cl := range std.Catalog.Classes {
			for _, sc := range cl.Subclasses {
				if code := cl.Code + sc.Code; !haveCondition[code] {
					fmt.Printf("  %-6s %s: %s\n", code, cl.Name, sc.Name)
				}
			}
		}
		haveSubclause := set(std.Suite.CoveredSubclauses())
		fmt.Println("\nnormative subclauses no case cites:")
		for _, s := range std.Catalog.NormativeSubclauses() {
			if !haveSubclause[s.Number] {
				fmt.Printf("  %-8s %s\n", s.Number, s.Title)
			}
		}
	}
	return nil
}

func set(codes []string) map[string]bool {
	m := make(map[string]bool, len(codes))
	for _, c := range codes {
		m[c] = true
	}
	return m
}

func unclaimed(cat *iso.Catalog, claimed map[string]bool) []string {
	var out []string
	for _, f := range cat.Features {
		if !claimed[f.Code] {
			out = append(out, f.Code)
		}
	}
	sort.Strings(out)
	return out
}
