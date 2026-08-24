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
	// And the third register, for the standard's own structure. It is checked
	// against the grammar register above, so an entry that stands behind a rule
	// nobody registered fails here too.
	uncitableSub, err := corpus.UncitableSubclauses(iso.Codes{Catalog: std.Catalog})
	if err != nil {
		return err
	}
	unciteable := corpus.UncitableSubclauseNumbers(uncitableSub)
	citedSubclause := set(std.Suite.CoveredSubclauses())
	for number := range unciteable {
		if citedSubclause[number] {
			return fmt.Errorf("subclause %s is cited by a case and listed as uncitable; one of the two is wrong", number)
		}
	}
	byKind := map[corpus.Kind]int{}
	for _, c := range std.Suite.Cases {
		byKind[c.Kind]++
	}
	conditions := len(std.Suite.CoveredConditions())
	productions := len(std.Suite.CoveredProductions())
	subclauses := len(std.Suite.CoveredSubclauses())
	// A clause heading specifies nothing on its own, so a case cites 19.3 and
	// never 19. Counting the heading as open leaves it open forever and counting
	// it as cited would be a citation nobody wrote, so it is counted apart: a
	// heading is covered when a case cites something beneath it.
	beneath := setOf(std.Catalog.CoveredBeneath(citedSubclause, unciteable))

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
			Beneath          []string            `json:"subclauses_covered_beneath"`
			Unclaimed        []string            `json:"unclaimed_features,omitempty"`
			Unwritable       []corpus.Unwritable `json:"unwritable_features,omitempty"`
			Uncited          []string            `json:"uncited_productions,omitempty"`
			Uncitable        []corpus.Uncitable  `json:"uncitable_productions,omitempty"`

			UncitableSub     []corpus.UncitableSubclause `json:"uncitable_subclauses,omitempty"`
			UncitedSubclause []string                    `json:"uncited_subclauses,omitempty"`
		}
		o := out{
			Cases: std.Suite.Len(), ByKind: byKind, Fixtures: std.Fixtures.Len(),
			Features: len(claimed), FeaturesTotal: len(std.Catalog.Features),
			Conditions: conditions, ConditionsTotal: totalConditions,
			Productions: productions, ProductionsTotal: len(std.Catalog.Productions),
			Subclauses: subclauses, SubclausesTotal: normative,
			Beneath:    sortedNumbers(std.Catalog, beneath),
			Unwritable: unwritable, Uncitable: uncitable, UncitableSub: uncitableSub,
		}
		if *missing {
			o.Unclaimed = unclaimed(std.Catalog, claimed, cannot)
			o.Uncited = uncited(std.Catalog, citedProduction, unreachable)
			o.UncitedSubclause = openSubclauses(std.Catalog, citedSubclause, unciteable, beneath)
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
	fmt.Fprintln(w, "COVERAGE\tCLAIMED\tREGISTERED\tBENEATH\tOPEN\tISO TOTAL")
	row := func(name string, claimed, registered, beneath, total int) {
		under := "-"
		if beneath > 0 {
			under = fmt.Sprint(beneath)
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%d\t%d\n",
			name, claimed, registered, under, total-claimed-registered-beneath, total)
	}
	row("optional features", len(claimed), len(unwritable), 0, len(std.Catalog.Features))
	row("GQLSTATUS codes", conditions, 0, 0, totalConditions)
	row("grammar productions", productions, len(uncitable), 0, len(std.Catalog.Productions))
	row("normative subclauses", subclauses, len(uncitableSub), len(beneath), normative)
	fmt.Fprintf(w, "\nThe totals are ISO's, not the corpus's. A corpus that tested twelve\n"+
		"features should read as twelve of 228, and a claim of full coverage\n"+
		"would mean 228 cases' worth of evidence that does not exist.\n"+
		"\n"+
		"CLAIMED is a case saying it exercises the thing. REGISTERED is the\n"+
		"three registers below, each entry a checked claim that no portable\n"+
		"case can reach it. BENEATH is the clause headings a case cites\n"+
		"something inside: Clause 19 specifies nothing on its own and 19.3 is\n"+
		"one of the things it specifies. OPEN is the work left, and it is the\n"+
		"only one of the four a case can move.\n")
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

	if len(uncitableSub) > 0 {
		fmt.Printf("\nno case can cite %d of the %d normative subclauses:\n",
			len(uncitableSub), normative)
		for _, why := range corpus.SubclauseWhys(uncitableSub) {
			fmt.Printf("\n  because %s:\n", why.Because())
			for _, u := range uncitableSub {
				if u.Why != why {
					continue
				}
				title, _ := iso.Codes{Catalog: std.Catalog}.SubclauseTitle(u.Subclause)
				fmt.Printf("    %-8s %s\n", u.Subclause, title)
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
		fmt.Println("\nnormative subclauses no case cites:")
		for _, number := range openSubclauses(std.Catalog, citedSubclause, unciteable, beneath) {
			s, _ := std.Catalog.Subclause(number)
			fmt.Printf("  %-8s %s\n", s.Number, s.Title)
		}
		// The headings are printed apart rather than left out, because a
		// heading that is covered only from underneath is worth seeing: if a
		// clause is here with one case beneath it, the clause is barely tested
		// and the number alone would not say so.
		fmt.Println("\nclause headings no case cites and every case beneath covers:")
		for _, number := range sortedNumbers(std.Catalog, beneath) {
			s, _ := std.Catalog.Subclause(number)
			fmt.Printf("  %-8s %s\n", s.Number, s.Title)
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

// setOf turns a document-ordered list of subclause numbers back into a set,
// for the two questions below that ask about membership rather than order.
func setOf(numbers []string) map[string]bool {
	out := make(map[string]bool, len(numbers))
	for _, n := range numbers {
		out[n] = true
	}
	return out
}

// sortedNumbers puts a set of subclause numbers into the standard's own order,
// which is document order and not string order: 4.10 comes after 4.9.
func sortedNumbers(cat *iso.Catalog, in map[string]bool) []string {
	out := make([]string, 0, len(in))
	for _, s := range cat.Subclauses {
		if in[s.Number] {
			out = append(out, s.Number)
		}
	}
	return out
}

// openSubclauses is the normative subclauses no case cites, the register does
// not hold, and no case cites anything beneath. It is the work left, and it is
// the list somebody closing M10 reads.
func openSubclauses(cat *iso.Catalog, cited, registered, beneath map[string]bool) []string {
	var out []string
	for _, s := range cat.NormativeSubclauses() {
		if !cited[s.Number] && !registered[s.Number] && !beneath[s.Number] {
			out = append(out, s.Number)
		}
	}
	return out
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
