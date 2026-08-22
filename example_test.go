package gqlcompat_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"time"

	gqlcompat "github.com/tamnd/gql-compat"
	"github.com/tamnd/gql-compat/adapter"
	_ "github.com/tamnd/gql-compat/adapter/fake"
	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/report"
	"github.com/tamnd/gql-compat/runner"
)

// Example is the README's library snippet, kept here so it cannot drift out of
// date without the build noticing. It runs against the scripted `fake` engine
// rather than a real one, since a doc example must not need a database.
func Example() {
	std, err := gqlcompat.Load()
	if err != nil {
		log.Fatal(err)
	}

	drv, err := adapter.New("fake", adapter.Options{})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = drv.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	rep, err := std.Run(ctx, drv, runner.Config{
		Repeats: 1,
		Warmups: 0,
		// The whole corpus against a scripted engine would prove nothing; one
		// case is enough to show the shape.
		Select: corpus.Selector{IDPattern: regexp.MustCompile(`^mandatory/pattern/node-pattern-unlabelled$`)},
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(rep.Totals.Cases, "case measured")
	if err := report.Write(os.Stdout, rep, report.FormatJUnit); err != nil {
		log.Fatal(err)
	}
	// Output is not asserted: latencies differ every run, which is the point
	// of measuring them.
}

// ExampleStandard shows the denominators a report is scored against. They come
// from ISO's own artifacts, never from the corpus, so a suite that tests a
// hundred features reads as a hundred of 228.
func ExampleStandard() {
	std, err := gqlcompat.Load()
	if err != nil {
		log.Fatal(err)
	}
	conditions := 0
	for _, c := range std.Catalog.Classes {
		conditions += len(c.Subclasses)
	}
	// The corpus's own size is deliberately not printed. It goes up whenever
	// somebody writes a case, and an example that had to be edited for that
	// would be teaching the wrong lesson: the numerator is this project's and
	// the denominators are ISO's, and only one of the two is worth asserting.
	if std.Suite.Len() == 0 {
		log.Fatal("the embedded corpus is empty")
	}
	fmt.Printf("scored against ISO: %d optional features, %d GQLSTATUS codes, %d grammar productions, %d normative subclauses\n",
		len(std.Catalog.Features), conditions,
		len(std.Catalog.Productions), len(std.Catalog.NormativeSubclauses()))
	// Output:
	// scored against ISO: 228 optional features, 68 GQLSTATUS codes, 814 grammar productions, 317 normative subclauses
}
