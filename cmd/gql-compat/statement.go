package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/tamnd/gql-compat/impdef"
	"github.com/tamnd/gql-compat/iso"
	"github.com/tamnd/gql-compat/runner"
)

// cmdStatement prints the Clause 24.5.2 document a vendor has to write.
//
// This is the only subcommand whose output is meant to be edited. Everything
// else this tool prints is a measurement, and editing a measurement is called
// something else; this prints a template, with every implementation-defined
// item the standard names already in it and with the answers one run happened
// to observe filled in beside the statement that observed them. The vendor
// checks each one, replaces the ones that are wrong for their engine, and
// answers the hundred-odd this harness never asked.
//
// It has no exit status of its own beyond succeeding or failing to write. An
// implementation-defined choice cannot be wrong, so there is nothing here for
// CI to gate on.
func cmdStatement(args []string) error {
	fs := flag.NewFlagSet("statement", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: gql-compat statement [flags] [report.json]

Prints a Clause 24.5.2 conformance statement template in Markdown: every
implementation-defined item ISO/IEC 39075:2024 names, in code order, with the
standard's own description of each, plus the implementation-dependent list and
any extensions observed.

Given a report, the answers that run observed are filled in and each names the
statement that produced it. Given no report, the template comes out empty, which
is a fine way to see what the standard actually asks an implementer to state.

An em dash means this harness observed nothing. It never means none, never
means unlimited, and never means zero.

`)
		fs.PrintDefaults()
	}
	var (
		out     = fs.String("out", "", "file to write to; empty prints to stdout")
		engine  = fs.String("engine", "", "implementation name for the header; defaults to the report's adapter")
		release = fs.String("version", "", "implementation version for the header; defaults to the report's")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The report is the natural thing to type first and the flags after it, and
	// Go's flag package stops at the first argument that is not a flag. Rather
	// than tell people they typed it wrong, parse what is left after each one.
	var paths []string
	for rest := fs.Args(); len(rest) > 0; rest = fs.Args() {
		paths = append(paths, rest[0])
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
	}
	if len(paths) > 1 {
		return errors.New("statement takes at most one report file: one statement describes one implementation")
	}

	cat, err := iso.Load()
	if err != nil {
		return err
	}

	st := impdef.Statement{Engine: *engine, Version: *release}
	res := &impdef.Result{
		DefinedTotal:   len(cat.ImplementationDefined),
		DependentTotal: len(cat.ImplementationDependent),
	}
	if len(paths) == 1 {
		rep, err := readReport(paths[0])
		if err != nil {
			return err
		}
		if rep.Implementation != nil {
			res = rep.Implementation
		}
		st.Source = paths[0]
		st.Observed = rep.Run.Started.UTC().Format("2006-01-02 15:04:05 UTC")
		if st.Engine == "" {
			st.Engine = rep.Engine.Adapter
		}
		if st.Version == "" {
			st.Version = rep.Engine.Version
		}
		if warn := unobserved(rep); warn != "" {
			fmt.Fprintln(os.Stderr, warn)
		}
	}

	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		w = f
	}
	if err := impdef.WriteStatement(w, st, items(cat), res); err != nil {
		return err
	}
	if *out != "" {
		fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
	}
	return nil
}

// items flattens the two vendored lists into the form the template wants.
func items(cat *iso.Catalog) []impdef.Item {
	out := make([]impdef.Item, 0, len(cat.ImplementationDefined)+len(cat.ImplementationDependent))
	for _, b := range cat.ImplementationDefined {
		out = append(out, impdef.Item{Code: b.Code, Description: b.Description, Kind: impdef.Defined})
	}
	for _, b := range cat.ImplementationDependent {
		out = append(out, impdef.Item{Code: b.Code, Description: b.Description, Kind: impdef.Dependent})
	}
	return out
}

// unobserved says on stderr, not in the document, how much of the template the
// vendor still has to fill in. It goes to stderr because a number about the
// harness has no business inside a statement about the implementation.
func unobserved(rep *runner.Report) string {
	r := rep.Implementation
	if r.Len() == 0 {
		return "note: this report observed nothing; every row of the statement is an em dash for you to answer"
	}
	return fmt.Sprintf("note: %d of the %d implementation-defined items carry an observed answer; the rest are yours to write",
		r.Observed(impdef.Defined), r.DefinedTotal)
}
