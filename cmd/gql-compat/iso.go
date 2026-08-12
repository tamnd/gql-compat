package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/tamnd/gql-compat/iso"
)

// cmdISO prints the vendored artifacts.
//
// This is the browsable form of what ISO ships as XML: 228 feature codes, the
// GQLSTATUS classes and subclasses, 800-odd grammar productions, and the
// clause structure. It exists because every claim the corpus makes is a
// reference into one of these tables, and a reader checking a claim should not
// have to parse XML to do it. Nothing here is derived or interpreted — the
// family names are this project's labels and say so — so what is printed can
// be compared line for line against the standard's own artifacts.
func cmdISO(args []string) error {
	fs := flag.NewFlagSet("iso", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: gql-compat iso [what] [flags]

  summary       counts of everything the catalogue holds (default)
  features      the 228 optional feature codes
  families      the feature codes grouped by prefix
  conditions    the GQLSTATUS classes and subclasses
  productions   the BNF production names
  subclauses    the clause and subclause structure
  keywords      the reserved and non-reserved words
  impdef        implementation-defined items
  impdep        implementation-dependent items

`)
		fs.PrintDefaults()
	}
	var (
		asJSON = fs.Bool("json", false, "emit JSON instead of a table")
		grep   = fs.String("grep", "", "case-insensitive substring filter over the whole row")
	)

	what := "summary"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		what, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cat, err := iso.Load()
	if err != nil {
		return err
	}

	switch what {
	case "summary":
		return isoSummary(cat, *asJSON)
	case "features":
		return isoTable(*asJSON, *grep, cat.Features, []string{"CODE", "FAMILY", "DESCRIPTION"},
			func(f iso.Feature) []string { return []string{f.Code, f.Family, f.Description} })
	case "families":
		return isoFamilies(cat, *asJSON)
	case "conditions":
		return isoConditions(cat, *asJSON, *grep)
	case "productions":
		return isoTable(*asJSON, *grep, cat.Productions, []string{"PRODUCTION", "REFERENCES", "KEYWORDS"},
			func(p iso.Production) []string {
				return []string{p.Name, strconv.Itoa(len(p.References)), strings.Join(p.Keywords, " ")}
			})
	case "subclauses":
		return isoTable(*asJSON, *grep, cat.Subclauses, []string{"NUMBER", "NORMATIVE", "TITLE"},
			func(s iso.Subclause) []string { return []string{s.Number, yesNo(s.Normative), s.Title} })
	case "keywords":
		words := cat.Keywords()
		if *asJSON {
			return writeJSON(words)
		}
		for _, w := range words {
			if matches(*grep, w) {
				fmt.Println(w)
			}
		}
		return nil
	case "impdef":
		return isoTable(*asJSON, *grep, cat.ImplementationDefined, []string{"CODE", "DESCRIPTION"},
			func(b iso.Behaviour) []string { return []string{b.Code, b.Description} })
	case "impdep":
		return isoTable(*asJSON, *grep, cat.ImplementationDependent, []string{"CODE", "DESCRIPTION"},
			func(b iso.Behaviour) []string { return []string{b.Code, b.Description} })
	}
	return fmt.Errorf("unknown iso target %q", what)
}

// isoTable renders any slice of catalogue entries through a row function, so
// each target above is one line rather than one printing loop.
func isoTable[T any](asJSON bool, grep string, items []T, header []string, row func(T) []string) error {
	if asJSON {
		if grep == "" {
			return writeJSON(items)
		}
		var kept []T
		for _, it := range items {
			if matches(grep, strings.Join(row(it), " ")) {
				kept = append(kept, it)
			}
		}
		return writeJSON(kept)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(header, "\t"))
	n := 0
	for _, it := range items {
		cells := row(it)
		if !matches(grep, strings.Join(cells, " ")) {
			continue
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
		n++
	}
	fmt.Fprintf(w, "\n%d of %d\n", n, len(items))
	return w.Flush()
}

func isoSummary(cat *iso.Catalog, asJSON bool) error {
	subclasses := 0
	for _, c := range cat.Classes {
		subclasses += len(c.Subclasses)
	}
	normative := len(cat.NormativeSubclauses())

	type summary struct {
		Source                  string `json:"source"`
		Features                int    `json:"features"`
		Families                int    `json:"families"`
		ConditionClasses        int    `json:"condition_classes"`
		ConditionSubclasses     int    `json:"condition_subclasses"`
		Productions             int    `json:"productions"`
		Keywords                int    `json:"keywords"`
		Subclauses              int    `json:"subclauses"`
		NormativeSubclauses     int    `json:"normative_subclauses"`
		ImplementationDefined   int    `json:"implementation_defined"`
		ImplementationDependent int    `json:"implementation_dependent"`
	}
	s := summary{
		Source: iso.SourceURL, Features: len(cat.Features), Families: len(cat.Families()),
		ConditionClasses: len(cat.Classes), ConditionSubclasses: subclasses,
		Productions: len(cat.Productions), Keywords: len(cat.Keywords()),
		Subclauses: len(cat.Subclauses), NormativeSubclauses: normative,
		ImplementationDefined:   len(cat.ImplementationDefined),
		ImplementationDependent: len(cat.ImplementationDependent),
	}
	if asJSON {
		return writeJSON(s)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ISO/IEC 39075:2024 artifacts\t%s\n\n", iso.SourceURL)
	fmt.Fprintf(w, "optional features\t%d\tin %d families\n", s.Features, s.Families)
	fmt.Fprintf(w, "GQLSTATUS codes\t%d\tin %d classes\n", s.ConditionSubclasses, s.ConditionClasses)
	fmt.Fprintf(w, "grammar productions\t%d\t%d keywords\n", s.Productions, s.Keywords)
	fmt.Fprintf(w, "subclauses\t%d\t%d normative\n", s.Subclauses, s.NormativeSubclauses)
	fmt.Fprintf(w, "implementation-defined\t%d\n", s.ImplementationDefined)
	fmt.Fprintf(w, "implementation-dependent\t%d\n", s.ImplementationDependent)
	fmt.Fprintf(w, "\nMandatory features carry no code. What a mandatory case cites is a\n"+
		"subclause number, which is why the subclause table is here beside the\n"+
		"feature table and not below it.\n")
	return w.Flush()
}

func isoFamilies(cat *iso.Catalog, asJSON bool) error {
	fams := cat.Families()
	if asJSON {
		return writeJSON(fams)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FAMILY\tFEATURES\tNAME")
	for _, f := range fams {
		fmt.Fprintf(w, "%s\t%d\t%s\n", f.Prefix, f.Count, f.Name)
	}
	fmt.Fprintf(w, "\nFamily names are this project's summaries of what each prefix covers,\n"+
		"not normative text; ISO gives the prefixes no names.\n")
	return w.Flush()
}

func isoConditions(cat *iso.Catalog, asJSON bool, grep string) error {
	type row struct {
		Code     string `json:"code"`
		Class    string `json:"class"`
		Category string `json:"category"`
		Name     string `json:"name"`
	}
	var rowsOut []row
	for _, c := range cat.Classes {
		category := iso.Categories[c.Category]
		if category == "" {
			category = c.Category
		}
		for _, s := range c.Subclasses {
			rowsOut = append(rowsOut, row{Code: c.Code + s.Code, Class: c.Name, Category: category, Name: s.Name})
		}
	}
	return isoTable(asJSON, grep, rowsOut, []string{"GQLSTATUS", "CATEGORY", "CLASS", "CONDITION"},
		func(r row) []string { return []string{r.Code, r.Category, r.Class, r.Name} })
}

func matches(grep, s string) bool {
	return grep == "" || strings.Contains(strings.ToLower(s), strings.ToLower(grep))
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
