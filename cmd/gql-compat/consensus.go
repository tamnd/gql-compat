package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tamnd/gql-compat/consensus"
	"github.com/tamnd/gql-compat/report"
	"github.com/tamnd/gql-compat/runner"
)

// cmdConsensus compares several engines' reports and prints the cases they all
// failed.
//
// The command deliberately produces no score and no exit status of its own. It
// exits zero with a queue of forty cases and zero with an empty one, because
// the queue is work for a person and not a verdict on anything, and a nonzero
// exit would put it in a CI gate where somebody would start trying to make the
// number go down.
func cmdConsensus(args []string) error {
	fs := flag.NewFlagSet("consensus", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: gql-compat consensus [flags] report.json report.json [report.json...]

Reads two or more report JSON files and lists, per case, which engines passed
and which failed. Cases every judging engine failed become a corpus review
queue: they are candidates for a case that was written wrong, not findings
about the engines, and nothing here enters any pass rate.

Two engines agreeing is a coin flip. Three unrelated engines agreeing is worth
reading the case. The output says which of those it is.

`)
		fs.PrintDefaults()
	}
	var (
		dispositions = fs.String("dispositions", "", "YAML file of decisions already made about queued cases")
		out          = fs.String("out", "", "directory to write consensus.json and consensus.md into; empty prints to stdout")
		format       = fs.String("format", "text", "stdout format: text, markdown, json")
		template     = fs.Bool("template", false, "print a dispositions skeleton for the undisposed cases and exit")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	paths := fs.Args()
	if len(paths) < 2 {
		return errors.New("consensus needs at least two report files; one engine failing a case is what its own report already says")
	}

	reports := make([]*runner.Report, 0, len(paths))
	for _, p := range paths {
		rep, err := readReport(p)
		if err != nil {
			return err
		}
		reports = append(reports, rep)
	}

	res, err := consensus.Compare(reports, paths)
	if err != nil {
		return err
	}
	if *dispositions != "" {
		ds, err := consensus.LoadDispositions(*dispositions)
		if err != nil {
			return err
		}
		res.Dispositions = ds
		// A decision about a case the queue no longer holds is not an error,
		// because the usual cause is the queue getting shorter, which is the
		// outcome everybody wants. It is worth saying out loud so the file
		// gets tidied.
		if stale := res.Stale(); len(stale) > 0 {
			fmt.Fprintf(os.Stderr, "note: %d disposition(s) in %s name cases no longer in the queue\n",
				len(stale), *dispositions)
		}
	}

	if *template {
		fmt.Print(res.Template())
		return nil
	}

	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(*out, "consensus.json"), func(f *os.File) error {
			return consensus.WriteJSON(f, res)
		}); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(*out, "consensus.md"), func(f *os.File) error {
			return consensus.WriteMarkdown(f, res)
		}); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s and %s\n",
			filepath.Join(*out, "consensus.json"), filepath.Join(*out, "consensus.md"))
	}

	switch *format {
	case "text":
		return consensus.WriteText(os.Stdout, res)
	case "markdown", "md":
		return consensus.WriteMarkdown(os.Stdout, res)
	case "json":
		return consensus.WriteJSON(os.Stdout, res)
	default:
		return fmt.Errorf("unknown -format %q: text, markdown, or json", *format)
	}
}

// readReport reads one report, naming the file in any error, because a
// comparison of five reports that fails with "unexpected end of JSON input"
// tells nobody which one.
func readReport(path string) (*runner.Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	rep, err := report.ReadJSON(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if rep.Engine.Adapter == "" {
		return nil, fmt.Errorf("%s: no engine recorded; this is not a gql-compat report", path)
	}
	return rep, nil
}

func writeFile(path string, write func(*os.File) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
