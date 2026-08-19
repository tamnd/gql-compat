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
	fmt.Printf("%d cases; ISO defines %d optional features and %d GQLSTATUS codes\n",
		std.Suite.Len(), len(std.Catalog.Features), conditions)
	// Output:
	// 410 cases; ISO defines 228 optional features and 68 GQLSTATUS codes
}
