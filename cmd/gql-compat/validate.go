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
		missing  = fs.Bool("missing", false, "list the features, conditions, subclauses and productions no case claims")
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
	// The register of features no portable case can be written for is loaded
	// against the same catalogue, so a wrong entry in it fails here the way a
	// wrong reference in a case does.
	unwritable, err := corpus.Unwritables(iso.Codes{Catalog: std.Catalog})
	if err != nil {
		return err
	}
	cannot := corpus.UnwritableCodes(unwritable)
	for code := range cannot {
		if claimed[code] {
			return fmt.Errorf("%s is claimed by a case and listed as unwritable; one of the two is wrong", code)
		}
	}
	// The same register, for the grammar. It is loaded against the catalogue and
	// against the feature register above, so a wrong entry fails here too.
	uncitable, err := corpus.Uncitables(iso.Codes{Catalog: std.Catalog})
	if err != nil {
		return err
	}
	unreachable := corpus.UncitableProductions(uncitable)
	citedProduction := set(std.Suite.CoveredProductions())
	for name := range unreachable {
		if citedProduction[name] {
			return fmt.Errorf("<%s> is cited by a case and listed as uncitable; one of the two is wrong", name)
		}
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
			Unwritable       []corpus.Unwritable `json:"unwritable_features,omitempty"`
			Uncited          []string            `json:"uncited_productions,omitempty"`
			Uncitable        []corpus.Uncitable  `json:"uncitable_productions,omitempty"`
		}
		o := out{
			Cases: std.Suite.Len(), ByKind: byKind, Fixtures: std.Fixtures.Len(),
			Features: len(claimed), FeaturesTotal: len(std.Catalog.Features),
			Conditions: conditions, ConditionsTotal: totalConditions,
			Productions: productions, ProductionsTotal: len(std.Catalog.Productions),
			Subclauses: subclauses, SubclausesTotal: normative,
			Unwritable: unwritable, Uncitable: uncitable,
		}
		if *missing {
			o.Unclaimed = unclaimed(std.Catalog, claimed, cannot)
			o.Uncited = uncited(std.Catalog, citedProduction, unreachable)
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

	if len(unwritable) > 0 {
		fmt.Printf("\nno portable case can be written for %d of the %d:\n",
			len(unwritable), len(std.Catalog.Features))
		for _, reason := range corpus.Reasons(unwritable) {
			fmt.Printf("\n  because %s:\n", reason.Because())
			for _, u := range unwritable {
				if u.Reason != reason {
					continue
				}
				f, _ := std.Catalog.Feature(u.Feature)
				fmt.Printf("    %-6s %s, at <%s>\n", u.Feature, f.Description, u.Production)
			}
		}
	}

	if len(uncitable) > 0 {
		fmt.Printf("\nno case can cite %d of the %d grammar rules:\n",
			len(uncitable), len(std.Catalog.Productions))
		for _, why := range corpus.Whys(uncitable) {
			fmt.Printf("\n  because %s:\n", why.Because())
			for _, u := range uncitable {
				if u.Why == why {
					fmt.Printf("    <%s>\n", u.Production)
				}
			}
		}
	}

	if *missing {
		fmt.Println("\nfeature codes no case claims:")
		for _, code := range unclaimed(std.Catalog, claimed, cannot) {
			f, _ := std.Catalog.Feature(code)
			fmt.Printf("  %-6s %s\n", code, f.Description)
		}
		// The other three denominators are worth the same treatment. A reader
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
		// The grammar is the largest denominator and was the one this list
		// did not print, which made it the one nobody could work through.
		// The rules the register above accounts for are left out, because
		// the point of this list is the work left and those are not work.
		// A rule the grammar declines to expand is still marked, since it
		// is usually reachable and usually worth citing but is worth a
		// second look before somebody writes a case around it.
		fmt.Println("\ngrammar productions no case cites:")
		for _, name := range uncited(std.Catalog, citedProduction, unreachable) {
			p, _ := std.Catalog.Production(name)
			note := ""
			if p.SeeTheRules {
				note = "   (the grammar declines to expand this one)"
			}
			fmt.Printf("  <%s>%s\n", name, note)
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

// unclaimed is the feature codes no case claims and somebody could still
// write one for. The register is subtracted rather than listed alongside,
// because the point of the list is the work left and those are not work.
func unclaimed(cat *iso.Catalog, claimed, unwritable map[string]bool) []string {
	var out []string
	for _, f := range cat.Features {
		if !claimed[f.Code] && !unwritable[f.Code] {
			out = append(out, f.Code)
		}
	}
	sort.Strings(out)
	return out
}

// uncited is the grammar rules no case cites and somebody could still write one
// for. It is unclaimed's counterpart for the largest of the four denominators
// and subtracts its register for the same reason.
//
// The order is the grammar's rather than alphabetical. A reader working through
// this list is reading down the BNF, and rules that sit next to each other in
// the standard are usually one case's worth of work rather than several.
func uncited(cat *iso.Catalog, cited, uncitable map[string]bool) []string {
	var out []string
	for _, p := range cat.Productions {
		if !cited[p.Name] && !uncitable[p.Name] {
			out = append(out, p.Name)
		}
	}
	return out
}
