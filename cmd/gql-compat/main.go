// Command gql-compat measures a graph database against ISO/IEC 39075:2024.
//
// It is a thin shell over the library. Every subcommand below is a few dozen
// lines of flag parsing around an exported call, which is deliberate: there is
// no internal/ package here, so anything this command can do is available to a
// program that imports github.com/tamnd/gql-compat. The CLI exists because
// running a suite from a terminal and from CI should not require writing Go,
// not because it holds logic of its own.
//
// Usage:
//
//	gql-compat run       -adapter zu -binary ./zu -out ./out
//	gql-compat list      -kind condition
//	gql-compat iso       features
//	gql-compat validate  -corpus ./corpus/suite
//	gql-compat version
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	// Adapters register themselves. Importing them here, and only here, is what
	// keeps the library free of every engine's client library: a program that
	// imports gql-compat for the corpus does not link the Neo4j driver.
	_ "github.com/tamnd/gql-compat/adapter/fake"
	_ "github.com/tamnd/gql-compat/adapter/ladybug"
	_ "github.com/tamnd/gql-compat/adapter/neo4j"
	_ "github.com/tamnd/gql-compat/adapter/zu"
)

// version is stamped by the release build. A report whose tool version says
// "devel" was produced by a working copy and should not be published as a
// comparison.
var (
	version = "devel"
	commit  = ""
	date    = ""
)

// main does nothing but turn run's exit code into a process status. The work
// is one function down so that every deferred cleanup — releasing the signal
// handler, and whatever a subcommand deferred below it — actually runs; an
// os.Exit inside a function holding defers skips all of them.
func main() { os.Exit(run()) }

func run() int {
	// A run is interruptible because a performance suite over a hundred
	// thousand nodes takes minutes, and the half of it that finished is worth
	// keeping. The runner stops between cases and reports what it has.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := dispatch(ctx, os.Args[1:])
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		usage(os.Stderr)
		return 2
	}
	// A run that completed and found failures is not a malfunction of this
	// program, so it prints no error prefix; it exits nonzero so CI notices,
	// and the report says what failed.
	if fail, ok := errors.AsType[*failedRun](err); ok {
		fmt.Fprintln(os.Stderr, fail.Error())
		return 1
	}
	fmt.Fprintln(os.Stderr, "gql-compat: "+err.Error())
	return 1
}

var errUsage = errors.New("usage")

func dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "run":
		return cmdRun(ctx, args[1:])
	case "list":
		return cmdList(args[1:])
	case "iso":
		return cmdISO(args[1:])
	case "validate":
		return cmdValidate(args[1:])
	case "version", "-version", "--version":
		printVersion(os.Stdout)
		return nil
	case "help", "-h", "-help", "--help":
		usage(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func printVersion(w *os.File) {
	fmt.Fprintf(w, "gql-compat %s\n", version)
	if commit != "" {
		fmt.Fprintf(w, "commit  %s\n", commit)
	}
	if date != "" {
		fmt.Fprintf(w, "built   %s\n", date)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `gql-compat measures a graph database against ISO/IEC 39075:2024 (GQL).

  run        run the corpus against an engine and write reports
  list       list the cases, fixtures, or adapters this binary has
  iso        print the vendored ISO catalogue: features, conditions,
             productions, subclauses, implementation-defined items
  validate   load a corpus and check every ISO reference in it
  version    print the version

Run "gql-compat <subcommand> -h" for that subcommand's flags.

A conformance score is only meaningful with the run that produced it, so
every report records the engine version, the host, the repetition counts,
and the selector. Reports written by different versions of this tool are
comparable only when their schema numbers match.
`)
}
