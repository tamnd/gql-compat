package runner

import (
	"context"

	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/grammar"
)

// StatusSyntaxError is GQLSTATUS 42001, invalid syntax. It is the only answer
// to a generated statement that means anything, which is why it is a named
// constant and not a string in a comparison.
const StatusSyntaxError = "42001"

// Explore configures the grammar walk. A nil Explore skips the phase.
//
// The walk is not part of the suite and its results are not part of the
// scoreboard. It runs after the cases and before the observations, on the same
// session, and everything it produces lands in Report.Exploration.
type Explore struct {
	// Grammar is the parsed BNF artifact.
	Grammar *grammar.Grammar
	// Seed fixes the walk. The same seed and the same grammar give the same
	// statements in the same order on every platform.
	Seed uint64
	// Count is how many statements to walk.
	Count int
	// Start is the production to walk from, empty for <GQL-program>.
	Start string
	// MaxDepth bounds the descent, zero for the default.
	MaxDepth int
	// Promoted is the list of leads already dealt with. A statement on it is
	// not put to the engine again, and a lead that reduces onto it is not
	// reported again.
	Promoted *grammar.Promoted
}

// Exploration is what the walk found. It is a separate field of the report from
// Cases, and that is the enforcement rather than a convention: summarize is
// given Report.Cases and has no way to reach this, so nothing generated can
// enter a total, a pass rate, or a coverage table however the report code
// changes later.
type Exploration struct {
	Seed     uint64 `json:"seed"`
	Start    string `json:"start"`
	MaxDepth int    `json:"max_depth"`
	// Walked is how many statements the walk wrote, and Distinct how many of
	// them were different. The gap is the walk repeating itself, which is worth
	// printing because it says how much a larger Count would buy.
	Walked   int `json:"walked"`
	Distinct int `json:"distinct"`
	// Known is how many statements were skipped because the promotion list
	// already has them.
	Known int `json:"known"`
	// Coverage is how much of the 814 productions a walk from Start can reach
	// at all. It bounds what the whole phase could ever say.
	Coverage grammar.Coverage `json:"coverage"`

	// Totals is the phase's own scoreboard row. It is a KindTotals like the
	// five, and it is deliberately not in Totals.ByKind.
	Totals KindTotals `json:"totals"`
	// Cases is every statement's result.
	Cases []CaseResult `json:"cases"`
	// Leads are the rejections worth a person's time, reduced.
	Leads []Lead `json:"leads"`
}

// Lead is a statement the published grammar admits and the engine called a
// syntax error.
//
// It is not a finding and the report never calls it one. ISO's grammar is
// syntax only, this harness's walk of it stops at tokens chosen by hand, and an
// engine is entitled to a documented extension or restriction under 24.5.3. A
// lead is the beginning of the work, and Reduced and Path are what make that
// work short.
type Lead struct {
	ID string `json:"id"`
	// Statement is what the walk wrote.
	Statement string `json:"statement"`
	// Reduced is the smallest statement the engine still called a syntax
	// error, and is the one to read.
	Reduced string `json:"reduced"`
	// Path is the productions the reduced statement went through, outermost
	// first. It is the answer to which of the 814 is in dispute.
	Path []string `json:"path"`
	// GQLStatus and Message are the engine's own words about the reduced
	// statement.
	GQLStatus string `json:"gqlstatus"`
	Message   string `json:"message,omitempty"`
	// Tried is how many candidates the reducer put to the engine.
	Tried int `json:"tried"`
	// Fingerprint keys the promotion list.
	Fingerprint string `json:"fingerprint"`
}

// explore walks the grammar, runs what it wrote, and reduces the rejections.
func (e *executor) explore(ctx context.Context, cfg *Explore) *Exploration {
	gen, err := grammar.NewGenerator(cfg.Grammar, cfg.Seed, grammar.Options{
		Start:    cfg.Start,
		MaxDepth: cfg.MaxDepth,
	})
	if err != nil {
		// A grammar the walk cannot start from is a fact about the artifact and
		// the cut in leaves.go, not about the engine. It is reported as an
		// exploration that found nothing and said why, and the run continues.
		return &Exploration{Seed: cfg.Seed, Start: cfg.Start, Leads: nil,
			Coverage: grammar.Coverage{Start: cfg.Start}}
	}

	statements, err := gen.GenerateN(cfg.Count)
	if err != nil {
		return &Exploration{Seed: gen.Seed(), Start: gen.Start(), Walked: len(statements),
			Coverage: gen.Coverage()}
	}

	out := &Exploration{
		Seed:     gen.Seed(),
		Start:    gen.Start(),
		MaxDepth: cfg.MaxDepth,
		Walked:   len(statements),
		Coverage: gen.Coverage(),
	}

	// Dedup first, so a statement the walk wrote twice is one case and not two,
	// and keep the statement beside its case so a rejection can be reduced.
	byID := map[string]grammar.Statement{}
	cases := grammar.Cases(statements)
	for _, s := range statements {
		if _, dup := byID[s.ID()]; !dup {
			byID[s.ID()] = s
		}
	}
	out.Distinct = len(cases)

	for _, c := range cases {
		if ctx.Err() != nil {
			break
		}
		if cfg.Promoted.Has(c.Query) {
			out.Known++
			continue
		}
		r := e.run(ctx, c)
		if r.Outcome == Fail && r.GotStatus == StatusSyntaxError {
			if lead, ok := e.lead(ctx, byID[c.ID], cfg.Promoted); ok {
				out.Leads = append(out.Leads, lead)
			} else {
				// The lead reduced onto a statement review has already dealt
				// with. It is not news and it is not silence either: the case
				// result stays, marked as a rejection already accounted for.
				out.Known++
				r.Outcome, r.Skip = Skip, SkipPromoted
				r.Reason = "the reduced statement is on the promotion list"
			}
		}
		out.Cases = append(out.Cases, r)
	}

	// The walk writes whatever the grammar admits, which includes DELETE and
	// DROP GRAPH. Whatever the session held is not what it holds now, and the
	// observation phase that runs next must reload rather than trust it.
	e.dirty = true

	for i := range out.Cases {
		out.Totals.Cases++
		switch out.Cases[i].Outcome {
		case Pass:
			out.Totals.Pass++
		case Fail:
			out.Totals.Fail++
		case Skip:
			out.Totals.Skip++
		case Error:
			out.Totals.Error++
		}
	}
	return out
}

// lead reduces a rejected statement and returns what to print, or false when
// the reduced statement is one review has already seen.
func (e *executor) lead(ctx context.Context, s grammar.Statement, promoted *grammar.Promoted) (Lead, bool) {
	tried := 0
	var status, message string

	reduced := grammar.Reduce(s, func(candidate string) bool {
		if ctx.Err() != nil {
			return false
		}
		tried++
		gotStatus, gotMessage, ok := e.syntaxError(ctx, candidate)
		if !ok {
			return false
		}
		status, message = gotStatus, gotMessage
		return true
	})

	if promoted.Has(reduced.Text) {
		return Lead{}, false
	}
	if status == "" {
		// The reducer found nothing smaller, so the original is the lead and
		// the engine's words about it are the ones already recorded.
		status = StatusSyntaxError
	}
	return Lead{
		ID:          s.ID(),
		Statement:   s.Text,
		Reduced:     reduced.Text,
		Path:        reduced.Path,
		GQLStatus:   status,
		Message:     message,
		Tried:       tried,
		Fingerprint: grammar.Fingerprint(reduced.Text),
	}, true
}

// syntaxError puts a candidate to the engine and reports whether the engine
// called it invalid syntax.
//
// It is one execution, untimed and unsampled. A reducer's candidates are not
// measurements: they exist to make one statement smaller, and putting a hundred
// of them in the latency tables would drown the cases that were chosen.
func (e *executor) syntaxError(ctx context.Context, statement string) (status, message string, ok bool) {
	sess, err := e.session(ctx)
	if err != nil {
		return "", "", false
	}
	qctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	_, err = sess.Exec(qctx, statement, nil)
	cancel()
	if err == nil {
		return "", "", false
	}
	f := adapter.AsFailure(err)
	if f == nil {
		return "", "", false
	}
	if f.Fatal {
		e.discard()
		return "", "", false
	}
	if f.Timeout || f.Transport {
		return "", "", false
	}
	if f.GQLStatus != StatusSyntaxError {
		return "", "", false
	}
	return f.GQLStatus, f.Message, true
}

// judgeGenerated scores a statement the published grammar admits.
//
// The rule is narrow on purpose, and narrower than the ordinary accept
// judgement, because the walk knows only that the statement is well formed.
// The grammar says nothing about meaning, so a statement it admits can
// reference a variable nothing bound or compare a duration to a string, and an
// engine that refuses those is right. Only a syntax error contradicts the
// grammar, and only an engine that reports GQLSTATUS can say it was one.
func judgeGenerated(err error, r *CaseResult) {
	if err == nil {
		r.Outcome, r.Evidence = Pass, EvidenceAccepted
		return
	}
	switch r.GotStatus {
	case StatusSyntaxError:
		r.Outcome, r.Evidence = Fail, EvidenceStatus
		r.Reason = "the engine reports invalid syntax for a statement the published grammar admits"
	case "":
		// The adapter declares GQLSTATUS or this case would have been skipped
		// before it ran, and then reported none. Without a code there is no way
		// to tell a syntax error from a semantic one, and guessing from the
		// message is how a harness invents findings.
		r.Outcome, r.Skip = Skip, SkipNoGQLStatus
		r.Reason = "the engine refused the statement without a GQLSTATUS, so it cannot be told whether the refusal was about syntax"
	default:
		r.Outcome, r.Skip = Skip, SkipSemantic
		r.Reason = "the engine refused with GQLSTATUS " + r.GotStatus +
			", which is not a syntax error; the grammar admits the statement and says nothing about what it means"
	}
}

// generatedSkip is the part of the skip ladder that belongs to the walk. It
// runs before the statement is executed and answers the two questions that make
// running it pointless.
func (e *executor) generatedSkip(c *corpus.Case, stmt string, r *CaseResult) bool {
	if c.Kind != corpus.KindGenerated {
		return false
	}
	if !e.caps.GQLStatus {
		r.Outcome, r.Skip = Skip, SkipNoGQLStatus
		r.Reason = "the engine reports no GQLSTATUS, so a refusal cannot be told from a syntax error, and the grammar walk has nothing else to go on"
		return true
	}
	if e.cfg.Explore != nil && e.cfg.Explore.Promoted.Has(stmt) {
		r.Outcome, r.Skip = Skip, SkipPromoted
		r.Reason = "review has already dealt with this statement"
		return true
	}
	return false
}
