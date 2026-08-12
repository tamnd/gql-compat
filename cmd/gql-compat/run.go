package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	gqlcompat "github.com/tamnd/gql-compat"
	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/report"
	"github.com/tamnd/gql-compat/runner"
)

// failedRun is a run that produced results, some of which were failures. It is
// separated from an ordinary error so that a caller can tell a database that
// does not conform from a harness that could not measure one.
type failedRun struct {
	mandatory, total int
	policy           string
}

func (f *failedRun) Error() string {
	if f.policy == "mandatory" {
		return fmt.Sprintf("%d mandatory case(s) failed", f.mandatory)
	}
	return fmt.Sprintf("%d case(s) failed", f.total)
}

// stringList collects a flag given more than once, which is how -format and
// -tag take several values without inventing a separator that a tag might
// contain.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// keyValue collects -opt name=value pairs for adapter-specific settings. It is
// untyped on purpose: an adapter outside this module can document its own
// options and be configured through the same flag.
type keyValue map[string]string

func (k keyValue) String() string { return fmt.Sprint(map[string]string(k)) }
func (k keyValue) Set(v string) error {
	name, value, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("expected name=value, got %q", v)
	}
	k[name] = value
	return nil
}

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `usage: gql-compat run -adapter NAME [flags]

Runs the corpus against one engine and writes a report.

Adapters in this binary: %s

`, strings.Join(adapter.Registered(), ", "))
		fs.PrintDefaults()
	}

	var (
		name     = fs.String("adapter", "", "engine to measure (required)")
		binary   = fs.String("binary", "", "path to the engine executable, for adapters that drive one")
		uri      = fs.String("uri", "", "connection URI, for client/server engines")
		user     = fs.String("user", "", "username")
		password = fs.String("password", "", "password; prefer GQL_COMPAT_PASSWORD in CI")
		database = fs.String("database", "", "database or graph name")

		modeName = fs.String("mode", "conformance", "conformance (standard text) or compat (the engine's own spelling)")
		pattern  = fs.String("run", "", "regular expression over case ids")
		corpusIn = fs.String("corpus", "", "directory of case files; empty uses the corpus embedded in this binary")

		repeats  = fs.Int("repeats", 7, "timed executions per case")
		warmups  = fs.Int("warmups", 1, "discarded executions before the timed ones; never applied to a mutating case")
		timeout  = fs.Duration("timeout", 30*time.Second, "limit for one statement")
		interval = fs.Duration("sample-interval", 5*time.Millisecond, "how often the process sampler reads")

		workdir     = fs.String("workdir", "", "where engine state goes; empty uses a temporary directory")
		keepWorkDir = fs.Bool("keep-workdir", false, "leave the working directory behind for inspection")

		out    = fs.String("out", "", "directory to write reports into; empty writes one report to stdout")
		failOn = fs.String("fail-on", "mandatory", "exit nonzero on: mandatory, any, or none")
		quiet  = fs.Bool("quiet", false, "suppress per-case progress")
	)
	var kinds, features, tags, skipTags, formats stringList
	opts := keyValue{}
	fs.Var(&kinds, "kind", "limit to a kind: mandatory, optional, condition, grammar, performance (repeatable)")
	fs.Var(&features, "feature", "limit to cases claiming an ISO feature code (repeatable)")
	fs.Var(&tags, "tag", "limit to cases carrying a tag (repeatable)")
	fs.Var(&skipTags, "skip-tag", "exclude cases carrying a tag (repeatable)")
	fs.Var(&formats, "format", "report format: json, markdown, html, csv, junit (repeatable; default json to stdout, all five to -out)")
	fs.Var(opts, "opt", "adapter-specific setting as name=value (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		fs.Usage()
		return errors.New("no -adapter given")
	}

	mode := runner.Mode(*modeName)
	if mode != runner.ModeConformance && mode != runner.ModeCompat {
		return fmt.Errorf("unknown -mode %q; want conformance or compat", *modeName)
	}
	chosen, err := parseFormats(formats, *out)
	if err != nil {
		return err
	}
	if err := checkFailOn(*failOn); err != nil {
		return err
	}

	sel, err := runner.ParseSelector(*pattern, kinds, features, tags, skipTags)
	if err != nil {
		return err
	}

	std, err := loadStandard(*corpusIn)
	if err != nil {
		return err
	}

	// An empty selection is a mistake worth catching before an engine is
	// started: a report over no cases is a scoreboard of zeroes that looks
	// like a clean run.
	if n := len(std.Suite.Filter(sel)); n == 0 {
		return fmt.Errorf("the selection matches none of the %d cases", std.Suite.Len())
	}

	pw := *password
	if pw == "" {
		pw = os.Getenv("GQL_COMPAT_PASSWORD")
	}
	drv, err := adapter.New(*name, adapter.Options{
		Binary:   *binary,
		URI:      *uri,
		Username: *user,
		Password: pw,
		Database: *database,
		Extra:    opts,
	})
	if err != nil {
		return err
	}

	cfg := runner.Config{
		Mode:           mode,
		Select:         sel,
		SelectorText:   describeSelector(*pattern, kinds, features, tags, skipTags),
		Repeats:        *repeats,
		Warmups:        *warmups,
		Timeout:        *timeout,
		SampleInterval: *interval,
		WorkDir:        *workdir,
		KeepWorkDir:    *keepWorkDir,
	}
	if !*quiet {
		cfg.Progress = progress(os.Stderr)
	}

	rep, err := std.Run(ctx, drv, cfg)
	if err != nil {
		return err
	}
	rep.Tool = "gql-compat " + version

	if err := emit(rep, chosen, *out, drv.Name()); err != nil {
		return err
	}
	return verdict(rep, *failOn)
}

func loadStandard(dir string) (*gqlcompat.Standard, error) {
	if dir == "" {
		return gqlcompat.Load()
	}
	return gqlcompat.LoadFS(os.DirFS(dir))
}

// parseFormats decides what to render. Writing to a directory defaults to all
// five, because the cost of the extra four is milliseconds and the cost of
// discovering afterwards that the HTML was not written is a whole rerun.
// Writing to stdout defaults to JSON, the only complete one.
func parseFormats(given stringList, out string) ([]report.Format, error) {
	if len(given) == 0 {
		if out == "" {
			return []report.Format{report.FormatJSON}, nil
		}
		return report.AllFormats, nil
	}
	var chosen []report.Format
	for _, g := range given {
		f, err := report.ParseFormat(g)
		if err != nil {
			return nil, err
		}
		chosen = append(chosen, f)
	}
	if out == "" && len(chosen) > 1 {
		return nil, errors.New("more than one -format needs -out; two reports cannot share stdout")
	}
	return chosen, nil
}

func checkFailOn(policy string) error {
	switch policy {
	case "mandatory", "any", "none":
		return nil
	}
	return fmt.Errorf("unknown -fail-on %q; want mandatory, any, or none", policy)
}

func emit(rep *runner.Report, formats []report.Format, out, engine string) error {
	if out == "" {
		return report.Write(os.Stdout, rep, formats[0])
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for _, f := range formats {
		// The engine name is in the filename because comparing engines means
		// putting their reports in one directory, and a fixed name would have
		// the second overwrite the first.
		path := filepath.Join(out, engine+f.Extension())
		file, err := os.Create(path)
		if err != nil {
			return err
		}
		err = report.Write(file, rep, f)
		if cerr := file.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		fmt.Fprintln(os.Stderr, "wrote "+path)
	}
	return nil
}

// verdict turns the scoreboard into an exit status.
//
// The default policy is mandatory-only. An optional feature an engine does not
// implement is a lawful choice under the standard, and a CI job that failed on
// one would be enforcing a rule ISO does not have. Harness errors always fail:
// they mean the run did not measure what it claims to have measured.
func verdict(rep *runner.Report, policy string) error {
	if rep.Totals.Error > 0 {
		return fmt.Errorf("%d case(s) could not be measured; the report says why", rep.Totals.Error)
	}
	mandatory := rep.Totals.ByKind[corpus.KindMandatory].Fail
	switch policy {
	case "mandatory":
		if mandatory > 0 {
			return &failedRun{mandatory: mandatory, total: rep.Totals.Fail, policy: policy}
		}
	case "any":
		if rep.Totals.Fail > 0 {
			return &failedRun{mandatory: mandatory, total: rep.Totals.Fail, policy: policy}
		}
	}
	return nil
}

// progress prints one line per case. It goes to stderr so that a report on
// stdout can still be piped.
func progress(w *os.File) func(done, total int, r *runner.CaseResult) {
	return func(done, total int, r *runner.CaseResult) {
		mark := map[runner.Outcome]string{
			runner.Pass:  "ok  ",
			runner.Fail:  "FAIL",
			runner.Skip:  "skip",
			runner.Error: "ERR ",
		}[r.Outcome]
		fmt.Fprintf(w, "[%*d/%d] %s %-56s %8s", len(strconv.Itoa(total)), done, total,
			mark, r.ID, r.Stats.P50.Round(time.Microsecond))
		if r.Reason != "" && r.Outcome != runner.Pass {
			fmt.Fprintf(w, "  %s", r.Reason)
		}
		fmt.Fprintln(w)
	}
}

// describeSelector reproduces the selection in the report, because a score
// over a subset of the corpus that does not say which subset is not a score.
func describeSelector(pattern string, kinds, features, tags, skipTags stringList) string {
	var parts []string
	add := func(name string, vs []string) {
		if len(vs) > 0 {
			parts = append(parts, name+"="+strings.Join(vs, ","))
		}
	}
	if pattern != "" {
		parts = append(parts, "run="+pattern)
	}
	add("kind", kinds)
	add("feature", features)
	add("tag", tags)
	add("skip-tag", skipTags)
	if len(parts) == 0 {
		return "the whole corpus"
	}
	return strings.Join(parts, " ")
}
