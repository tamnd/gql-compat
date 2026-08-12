// Package consensus compares several engines' reports of the same corpus and
// finds the cases they all fail.
//
// It exists because of a hole nothing else in this project closes. A case that
// misreads the standard produces a confidently wrong verdict, and the report
// describes it as engines failing rather than as a case being wrong. Review is
// the only remedy and review does not scale.
//
// The signal is cheap and it is not proof: engines that share no lineage, no
// parser and no execution model agreeing on a failure is better explained by
// the case than by the engines. So a case every engine failed goes on a
// review queue, which is a queue of work for whoever maintains the corpus and
// is never a finding about a database.
//
// Three rules hold this package honest, and they are the reason it computes so
// little:
//
// It changes no score. Nothing here is added to, subtracted from, or weighted
// into any pass rate. A case under review is neither excused nor penalised; it
// is listed.
//
// It needs three engines. Two engines agreeing is a coin flip, and this
// package says so in its output rather than leaving the reader to know it.
//
// It only counts engines that judged the case. An engine that skipped a case
// declared it could not run it, which is not agreement about the answer, and
// an engine that errored never got one.
package consensus

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/runner"
)

// Engine is one report's provenance, enough for a reader to tell whether two
// reports are about comparable things.
type Engine struct {
	Adapter string      `json:"adapter"`
	Version string      `json:"version"`
	Mode    runner.Mode `json:"mode"`
	// Source is the file the report was read from.
	Source string `json:"source,omitempty"`
	// Cases is how many cases this report holds, which is how a reader spots
	// a report produced by a selector rather than by a whole run.
	Cases int `json:"cases"`
}

// Verdict is one engine's outcome on one case.
type Verdict struct {
	Engine  string         `json:"engine"`
	Outcome runner.Outcome `json:"outcome"`
	// Reason is the engine's own words, kept because a review queue whose
	// entries do not say how each engine failed is a list of homework.
	Reason string `json:"reason,omitempty"`
}

// Case is one case as several engines saw it.
type Case struct {
	ID   string      `json:"id"`
	Name string      `json:"name,omitempty"`
	Kind corpus.Kind `json:"kind,omitempty"`

	Verdicts []Verdict `json:"verdicts"`

	// Passed, Failed, Skipped and Errored are engine names, sorted. They are
	// separate rather than derived at render time so the JSON is usable
	// without reimplementing the classification.
	Passed  []string `json:"passed,omitempty"`
	Failed  []string `json:"failed,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
	Errored []string `json:"errored,omitempty"`
}

// Judged is how many engines returned a verdict on the answer, as opposed to
// skipping the case or failing to reach it.
func (c *Case) Judged() int { return len(c.Passed) + len(c.Failed) }

// Unanimous reports whether every engine that judged the case failed it, over
// at least min engines. It is the whole signal this package produces.
func (c *Case) Unanimous(min int) bool {
	return len(c.Passed) == 0 && len(c.Failed) >= min
}

// Divided reports whether the engines disagreed, which is the ordinary and
// uninteresting case: a case some engines pass and others fail is a case doing
// its job.
func (c *Case) Divided() bool { return len(c.Passed) > 0 && len(c.Failed) > 0 }

// Result is the whole comparison.
type Result struct {
	Engines []Engine `json:"engines"`
	Cases   []*Case  `json:"cases"`
	// Review is the queue: cases every judging engine failed, in corpus
	// order. These are candidates for a corpus bug and nothing else; see
	// Result.Disclaimer.
	Review []*Case `json:"review"`
	// Dispositions carries what a human decided about each queued case, keyed
	// by case id. A queued case with no disposition is the point of the queue.
	Dispositions map[string]Disposition `json:"dispositions,omitempty"`
	// MinEngines is how many engines had to fail a case for it to be queued.
	MinEngines int `json:"min_engines"`
	// Thin records that the comparison ran with fewer than three engines, so
	// every renderer can say so rather than each one remembering to.
	Thin bool `json:"thin"`
}

// Disclaimer is the sentence that must accompany the queue wherever it is
// rendered. It is a constant rather than a docs paragraph because the wording
// is the safeguard: a queue presented as findings about engines would be the
// exact error this package exists to prevent.
const Disclaimer = "This is a corpus review queue and not an engine finding. " +
	"A case that every engine fails is more likely to have been written wrong than to have found the same bug in unrelated engines, " +
	"and until someone reads it neither reading is established. Nothing here changes any pass rate."

// Limit is the honest bound on the signal, printed with it.
const Limit = "Consensus only detects a shared misreading where the engines are independent. " +
	"Three engines that all grew out of Cypher will agree about Cypher, and a case that misreads GQL in a Cypher-flavoured direction will be confirmed by all three. " +
	"It is a smoke detector, not a proof."

// Compare builds the comparison from two or more reports.
//
// It refuses one report, because a single engine failing a case is what the
// ordinary report already says, and refuses reports from the same adapter,
// because an engine agreeing with itself is not evidence of anything.
func Compare(reports []*runner.Report, sources []string) (*Result, error) {
	if len(reports) < 2 {
		return nil, errors.New("consensus: needs at least two reports")
	}
	res := &Result{
		Dispositions: map[string]Disposition{},
		// Every engine that judged the case must have failed it, and there
		// must be at least two of them for the word consensus to mean
		// anything. Three is what makes it worth acting on, which is what
		// Thin is for.
		MinEngines: 2,
		Thin:       len(reports) < 3,
	}
	seen := map[string]string{}
	byID := map[string]*Case{}
	var order []string

	for i, rep := range reports {
		if rep == nil {
			return nil, fmt.Errorf("consensus: report %d is empty", i+1)
		}
		name := rep.Engine.Adapter
		src := ""
		if i < len(sources) {
			src = sources[i]
		}
		if prev, ok := seen[name]; ok {
			return nil, fmt.Errorf("consensus: two reports from adapter %q (%s and %s); an engine agreeing with itself is not evidence",
				name, prev, src)
		}
		seen[name] = src
		res.Engines = append(res.Engines, Engine{
			Adapter: name,
			Version: rep.Engine.Version,
			Mode:    rep.Run.Mode,
			Source:  src,
			Cases:   len(rep.Cases),
		})

		for j := range rep.Cases {
			cr := &rep.Cases[j]
			c, ok := byID[cr.ID]
			if !ok {
				c = &Case{ID: cr.ID, Name: cr.Name, Kind: cr.Kind}
				byID[cr.ID] = c
				order = append(order, cr.ID)
			}
			c.Verdicts = append(c.Verdicts, Verdict{
				Engine: name, Outcome: cr.Outcome, Reason: reasonOf(cr)})
			switch cr.Outcome {
			case runner.Pass:
				c.Passed = append(c.Passed, name)
			case runner.Fail:
				c.Failed = append(c.Failed, name)
			case runner.Skip:
				c.Skipped = append(c.Skipped, name)
			case runner.Error:
				c.Errored = append(c.Errored, name)
			}
		}
	}

	sort.Strings(order)
	for _, id := range order {
		c := byID[id]
		for _, s := range [][]string{c.Passed, c.Failed, c.Skipped, c.Errored} {
			sort.Strings(s)
		}
		res.Cases = append(res.Cases, c)
		if c.Unanimous(res.MinEngines) {
			res.Review = append(res.Review, c)
		}
	}
	return res, nil
}

// reasonOf is the shortest true description of what one engine did, which is
// its own reason when it gave one and its diff when it did not.
func reasonOf(cr *runner.CaseResult) string {
	if cr.Reason != "" {
		return cr.Reason
	}
	if cr.Diff != nil {
		return cr.Diff.Error()
	}
	return cr.Message
}

// Undisposed are the queued cases nobody has written a decision about yet. It
// is the working list, and an empty one is the only state in which the queue
// has been dealt with.
func (r *Result) Undisposed() []*Case {
	var out []*Case
	for _, c := range r.Review {
		if _, ok := r.Dispositions[c.ID]; !ok {
			out = append(out, c)
		}
	}
	return out
}

// Stale are dispositions for cases the queue no longer holds. A case that was
// fixed, or that one engine now passes, leaves its disposition behind, and a
// file full of decisions about cases that are no longer queued is a file
// nobody trusts.
func (r *Result) Stale() []string {
	queued := make(map[string]bool, len(r.Review))
	for _, c := range r.Review {
		queued[c.ID] = true
	}
	var out []string
	for id := range r.Dispositions {
		if !queued[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Agreement counts how the cases divided, for the one table this produces.
type Agreement struct {
	// AllPassed, AllFailed and Divided partition the cases at least two
	// engines judged.
	AllPassed, AllFailed, Divided int
	// Unjudged is every other case: nobody judged it, or only one engine did,
	// which says nothing about agreement in either direction.
	Unjudged int
	// Total is every case any report held.
	Total int
}

// Summarize counts the four groups.
func (r *Result) Summarize() Agreement {
	var a Agreement
	a.Total = len(r.Cases)
	for _, c := range r.Cases {
		switch {
		case c.Judged() < 2:
			a.Unjudged++
		case c.Divided():
			a.Divided++
		case len(c.Failed) == 0:
			a.AllPassed++
		default:
			a.AllFailed++
		}
	}
	return a
}

// EngineNames are the adapters compared, in the order given.
func (r *Result) EngineNames() []string {
	out := make([]string, 0, len(r.Engines))
	for _, e := range r.Engines {
		out = append(out, e.Adapter)
	}
	return out
}

// Modes reports the distinct run modes across the reports. Comparing a
// conformance run with a compatibility run is comparing two questions, and the
// renderers warn about it rather than refusing, because there are legitimate
// reasons to look.
func (r *Result) Modes() []string {
	var out []string
	for _, e := range r.Engines {
		if m := string(e.Mode); m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// MixedModes reports whether the reports answer different questions.
func (r *Result) MixedModes() bool { return len(r.Modes()) > 1 }

// String is a one-line summary, for a log.
func (r *Result) String() string {
	a := r.Summarize()
	return fmt.Sprintf("%s: %d cases, %d agreed pass, %d agreed fail, %d divided",
		strings.Join(r.EngineNames(), " vs "), a.Total, a.AllPassed, a.AllFailed, a.Divided)
}
