package report

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/impdef"
	"github.com/tamnd/gql-compat/metrics"
	"github.com/tamnd/gql-compat/runner"
)

// WriteMarkdown renders the report for a human.
//
// The order is deliberate. What was measured and on what comes first, because
// a score without a version and a machine is not a claim about anything. Then
// the scoreboard, then what the engine could not be asked and why, then the
// failures in full, and only then the metric tables — which are long, and
// which nobody reads before knowing whether the run passed.
func WriteMarkdown(w io.Writer, rep *runner.Report) error {
	b := bufio.NewWriter(w)
	defer func() { _ = b.Flush() }()

	p := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	nl := func() { fmt.Fprintln(b) }

	p("# GQL conformance report: %s\n", rep.Engine.Adapter)
	nl()
	p("%s\n", headline(rep))
	nl()

	p("## What was measured\n")
	nl()
	p("| | |\n|---|---|\n")
	p("| Engine | `%s` |\n", rep.Engine.Adapter)
	p("| Version | %s |\n", md(rep.Engine.Version))
	p("| Mode | %s |\n", rep.Run.Mode)
	p("| Cases | %d |\n", rep.Totals.Cases)
	p("| Repetitions per case | %d (after %d warmup%s) |\n",
		rep.Run.Repeats, rep.Run.Warmups, plural(rep.Run.Warmups))
	p("| Statement timeout | %s |\n", metrics.Format(rep.Run.Timeout))
	p("| Sampler interval | %s |\n", metrics.Format(rep.Run.SampleInterval))
	if rep.Run.Selector != "" {
		p("| Selector | `%s` |\n", rep.Run.Selector)
	}
	p("| Started | %s |\n", rep.Run.Started.UTC().Format("2006-01-02 15:04:05 UTC"))
	p("| Wall time | %s |\n", metrics.Format(rep.Run.Wall))
	p("| Standard | ISO/IEC 39075:2024, artifacts from %s |\n", rep.Run.ISOSource)
	nl()

	p("### Host\n")
	nl()
	h := rep.Host
	p("| | |\n|---|---|\n")
	p("| Platform | %s (%s/%s) |\n", md(fallback(h.Platform, "unknown")), h.OS, h.Arch)
	if h.Kernel != "" {
		p("| Kernel | %s |\n", md(h.Kernel))
	}
	if h.CPUModel != "" {
		p("| CPU | %s |\n", md(h.CPUModel))
	}
	p("| Cores | %d physical, %d logical, GOMAXPROCS %d |\n", h.CPUCores, h.CPULogical, h.GOMAXPROCS)
	if h.MemoryTotal > 0 {
		p("| Memory | %s |\n", metrics.FormatBytes(h.MemoryTotal))
	}
	p("| Go | %s |\n", h.GoVersion)
	if h.Containerised {
		p("| Virtualised | yes — absolute latencies on a shared runner are not comparable across machines |\n")
	}
	nl()

	writeCapabilities(b, rep)
	writeChallenge(b, rep)
	writeScoreboard(b, rep)
	writeCoverage(b, rep)
	writeSkips(b, rep)
	writeFailures(b, rep)
	writeLatency(b, rep)
	writePlans(b, rep)
	writeResources(b, rep)
	writeLoads(b, rep)
	writeExploration(b, rep)
	writeImplementation(b, rep)
	writeMethodology(b, rep)
	return b.Flush()
}

func headline(rep *runner.Report) string {
	t := rep.Totals
	judged := t.Pass + t.Fail
	var parts []string
	// A challenging run has to say so in its first sentence. Its cases were
	// chosen for being ones the engine said it could not take, so the pass rate
	// below is a rate over the wrong denominator by design, and a reader who
	// meets it without the warning will read it as a conformance score.
	if rep.Run.Challenge {
		parts = append(parts, "**this run challenged the engine's declaration** and is not a conformance score")
	}
	if judged > 0 {
		parts = append(parts, fmt.Sprintf("**%d of %d judged cases passed** (%.1f%%)",
			t.Pass, judged, 100*float64(t.Pass)/float64(judged)))
	} else {
		parts = append(parts, "**no case produced a verdict**")
	}
	if t.Skip > 0 {
		parts = append(parts, fmt.Sprintf("%d were skipped because the engine declared it could not hold the fixture or accept the case's shape", t.Skip))
	}
	if t.Error > 0 {
		// Not "the harness failed": the commonest error by far is a setup
		// statement the engine refused, which is the engine's answer and not a
		// malfunction of this tool. Naming a culprit the summary cannot know
		// sends a reader to debug the wrong program; the failure list below
		// gives the reason for each one.
		parts = append(parts, fmt.Sprintf("%d never reached a verdict — the case, its setup, or the session did not complete", t.Error))
	}
	if t.WeakEvidence > 0 {
		parts = append(parts, fmt.Sprintf("%d passes rest on error-text matching rather than a GQLSTATUS and are the weakest evidence here", t.WeakEvidence))
	}
	return strings.Join(parts, "; ") + "."
}

func writeCapabilities(b io.Writer, rep *runner.Report) {
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	p("## Declared capabilities\n\n")
	p("These are the engine's own statements about what it can hold and do, made before any case ran. Every skip below traces back to one of them.\n\n")
	p("| Capability | Supported |\n|---|:-:|\n")
	caps := rep.Engine.Capabilities
	for _, c := range fixture.AllCapabilities {
		p("| %s | %s |\n", c, tick(caps.Data[c]))
	}
	p("| GQLSTATUS reporting | %s |\n", tick(caps.GQLStatus))
	p("| Named parameters | %s |\n", tick(caps.Parameters))
	p("| Explicit transactions | %s |\n", tick(caps.Transactions))
	p("| Multiple statements | %s |\n", tick(caps.MultipleStatements))
	p("| Resettable in place | %s |\n", tick(caps.Isolated))
	p("\n")
	if len(caps.Notes) > 0 {
		p("Adapter notes:\n\n")
		for _, n := range caps.Notes {
			p("- %s\n", n)
		}
		p("\n")
	}
}

// writeChallenge reports what became of the cases the declaration would have
// skipped, and is silent on a run that did not challenge it.
//
// The table reads in the engine's favour by default. A claim of absence whose
// cases failed is a claim the run confirmed, and most of them are: the whole
// point of a declaration is to save the run from measuring the same absence
// through fifty cases that needed it in passing. What the section exists for
// is the row where every case passed, which is the one thing an engine that
// genuinely lacks the capability cannot produce.
func writeChallenge(b io.Writer, rep *runner.Report) {
	if !rep.Run.Challenge {
		return
	}
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	p("## The declaration under challenge\n\n")
	p("This run ignored the table above and put the excluded cases to the engine anyway, so its failures are expected and its totals are not a conformance score. A claim is contradicted when every case it excluded passed, which is the one outcome an engine that lacks the thing cannot produce.\n\n")
	if len(rep.Declarations) == 0 {
		p("No case was excluded by the declaration, so there was nothing to challenge.\n\n")
		return
	}
	p("| Claimed absent | Excluded by | Cases | Pass | Fail | Error | Verdict |\n")
	p("|---|---|--:|--:|--:|--:|---|\n")
	var wrong, unrefuted []runner.DeclarationCheck
	for _, d := range rep.Declarations {
		v := "claim stands"
		switch {
		case d.Contradicted:
			v = "**contradicted**"
			wrong = append(wrong, d)
		case d.Unrefuted():
			v = "not refuted"
			unrefuted = append(unrefuted, d)
		}
		p("| `%s` | %s | %d | %d | %d | %d | %s |\n",
			md(d.Claim), d.Reason, d.Cases, d.Pass, d.Fail, d.Error, v)
	}
	p("\n")
	for _, d := range wrong {
		p("The engine declares `%s` absent, and all %d case%s it excluded passed%s. Either the declaration is out of date or those cases are reaching a verdict without the thing they claim to need, and both are worth a look before the next run believes the declaration again.\n\n",
			md(d.Claim), d.Cases, plural(d.Cases), passingIDs(d))
	}
	for _, d := range unrefuted {
		p("The engine declares `%s` absent, and of the %d case%s it excluded, %d passed and none failed; the other %d never reached a verdict. That is not a contradiction, because an error is the harness failing to get an answer rather than the engine answering. It is still the shape a capability an engine quietly has makes when a second claim excludes the same cases, so it is worth reading the errors before believing this one.\n\n",
			md(d.Claim), d.Cases, plural(d.Cases), d.Pass, d.Cases-d.Pass)
	}
}

func writeScoreboard(b io.Writer, rep *runner.Report) {
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	p("## Scoreboard\n\n")
	p("Pass rate is passes over cases that produced a verdict. Skips and harness errors are excluded from the denominator and shown beside it, so a rate cannot be improved by refusing more work.\n\n")
	p("| Kind | Cases | Pass | Fail | Skip | Error | Pass rate |\n|---|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range kindRows(rep) {
		p("| %s | %d | %d | %d | %d | %d | %s |\n",
			r.Kind, r.Cases, r.Pass, r.Fail, r.Skip, r.Error, rate(r.KindTotals))
	}
	t := rep.Totals
	p("| **total** | **%d** | **%d** | **%d** | **%d** | **%d** | **%s** |\n",
		t.Cases, t.Pass, t.Fail, t.Skip, t.Error,
		rate(runner.KindTotals{Pass: t.Pass, Fail: t.Fail}))
	// The generated row sits under the total line and not above it, because it
	// is not part of the total and the layout should say so before the caption
	// does. Its rate column is a dash on purpose: a rate is a conformance
	// claim, and a statement that cites no clause cannot support one.
	if x := rep.Exploration; x != nil && x.Totals.Cases > 0 {
		g := x.Totals
		p("| _generated_ | _%d_ | _%d_ | _%d_ | _%d_ | _%d_ | _—_ |\n",
			g.Cases, g.Pass, g.Fail, g.Skip, g.Error)
	}
	p("\n")
	if x := rep.Exploration; x != nil && x.Totals.Cases > 0 {
		p("The generated row is below the total because it is not in it. Those statements came from a walk of the published grammar, they cite no clause, and nothing in that row is a conformance result. See the section on statements the grammar admits.\n\n")
	}
	if rep.Run.Mode == runner.ModeCompat {
		p("> This is a **compatibility** run: the statements executed were the engine's own documented spellings, not standard GQL. Nothing in this table is a conformance result.\n\n")
	}
}

func writeCoverage(b io.Writer, rep *runner.Report) {
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	cov := rep.Coverage
	p("## Coverage of the standard\n\n")
	p("Denominators come from ISO's own published surface, not from this corpus: %d optional features, %d GQLSTATUS codes, %d grammar productions, and %d clauses that specify behaviour. A corpus that tests twelve features reads as twelve of %d, which is the honest way to say it.\n\n",
		cov.FeaturesTotal, cov.ConditionsTotal, cov.ProductionsTotal, cov.SubclausesTotal, cov.FeaturesTotal)

	p("### Optional feature families\n\n")
	p("| Family | ISO features | Tested here | Supported | No portable case |\n|---|---:|---:|---:|---:|\n")
	var tested, supported, total, unwritable int
	for _, f := range cov.Families {
		total += f.Total
		tested += f.Tested
		supported += f.Supported
		unwritable += f.Unwritable
		if f.Tested == 0 && f.Unwritable == 0 {
			continue
		}
		p("| %s | %d | %d | %d | %d |\n", f.Family, f.Total, f.Tested, f.Supported, f.Unwritable)
	}
	p("| **all families** | **%d** | **%d** | **%d** | **%d** |\n\n", total, tested, supported, unwritable)
	if tested < total {
		p("%d of the %d optional features this standard defines are not exercised by the corpus at all. They are neither supported nor unsupported here; they are untested, and the report says so rather than defaulting them either way.\n\n",
			total-tested, total)
	}
	if len(cov.Unwritable) > 0 {
		groups := groupUnwritable(cov.Unwritable)
		p("%d of them will stay that way, in %d way%s:\n\n", len(cov.Unwritable),
			len(groups), plural(len(groups)))
		for _, g := range groups {
			p("No case for %s, because %s:\n\n", codeList(g.Features()), g.Reason.Because())
			for _, u := range g.Entries {
				p("- %s, which reaches the grammar at `<%s>`. %s\n", u.Feature, u.Production, u.Note)
			}
			p("\n")
		}
	}

	writeStatusTable(b, "### Optional features tested", "Feature", cov.Features)
	p("### Mandatory behaviour\n\n")
	p("ISO gives mandatory features no code, so a claim about one can only cite the subclause that specifies it. This corpus cites %d of the %d clauses that specify behaviour.\n\n",
		len(cov.Subclauses), cov.SubclausesTotal)
	writeStatusTable(b, "", "Subclause", cov.Subclauses)
	writeStatusTable(b, "### GQLSTATUS conditions tested", "Code", cov.Conditions)
}

func writeStatusTable(b io.Writer, heading, label string, items map[string]runner.Status) {
	if len(items) == 0 {
		return
	}
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sortKeys(keys)
	if heading != "" {
		p("%s\n\n", heading)
	}
	p("| %s | Description | Cases | Pass | Fail | Skip | Verdict |\n|---|---|---:|---:|---:|---:|---|\n", label)
	for _, k := range keys {
		s := items[k]
		p("| `%s` | %s | %d | %d | %d | %d | %s |\n",
			k, md(s.Description), s.Cases, s.Pass, s.Fail, s.Skip, verdict(s))
	}
	p("\n")
}

func verdict(s runner.Status) string {
	switch {
	case s.Supported():
		return "supported"
	case s.Pass+s.Fail == 0:
		return "untested — every case was skipped"
	case s.Fail > 0:
		return "**not supported**"
	default:
		return "partly tested"
	}
}

func writeSkips(b io.Writer, rep *runner.Report) {
	groups := skipsByReason(rep)
	if len(groups) == 0 {
		return
	}
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	p("## What the engine was not asked\n\n")
	p("A skip is a measurement. It says the engine declared, in advance, that it could not represent the data a case needs or accept the shape the case has. None of these are failures and none of them are passes.\n\n")
	// The HTML report puts the case ids behind a disclosure triangle. Markdown
	// has no such thing, so they go in the table: a count on its own tells a
	// reader that something was declined without telling them what, and the
	// ids are the only way to check a skip was legitimate.
	p("| Reason | Cases | Which |\n|---|---:|---|\n")
	for _, g := range groups {
		p("| %s | %d | %s |\n", g.Reason, len(g.IDs), idList(g.IDs))
	}
	p("\n")

	byCap := map[fixture.Capability]int{}
	for _, c := range rep.Cases {
		if c.Outcome != runner.Skip {
			continue
		}
		for _, m := range c.Missing {
			byCap[m]++
		}
	}
	if len(byCap) > 0 {
		p("Missing capabilities, by how many cases each one cost:\n\n")
		p("| Capability | Cases blocked |\n|---|---:|\n")
		for _, c := range fixture.AllCapabilities {
			if n := byCap[c]; n > 0 {
				p("| %s | %d |\n", c, n)
			}
		}
		p("\n")
	}
}

func writeFailures(b io.Writer, rep *runner.Report) {
	fs := failures(rep)
	if len(fs) == 0 {
		return
	}
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	p("## Failures and errors\n\n")
	for i := range fs {
		c := &fs[i]
		p("### `%s` — %s\n\n", c.ID, c.Name)
		p("%s. %s.\n\n", strings.ToUpper(string(c.Outcome[:1]))+string(c.Outcome[1:]), md(strings.TrimSuffix(c.Reason, ".")))
		if claims := claimList(c); claims != "" {
			p("Claims: %s.\n\n", claims)
		}
		p("```gql\n%s\n```\n\n", strings.TrimSpace(c.Statement))
		if c.WantStatus != "" || c.GotStatus != "" {
			p("- Expected GQLSTATUS `%s`, engine reported `%s`\n",
				fallback(c.WantStatus, "any failure"), fallback(c.GotStatus, "none"))
		}
		if c.Message != "" {
			p("- Engine said: %s\n", md(oneLine(c.Message)))
		}
		if d := c.Diff; d != nil {
			// A difference in the shape of the table locates itself at row -1,
			// because there is no row to point at. Printing the coordinates
			// unconditionally turned "the row count differs" into "row -1,
			// column 0", which sends a reader looking for a row that does not
			// exist. The Diff renders itself correctly either way.
			if d.Row >= 0 {
				p("- Row %d, column %d: expected `%s`, got `%s`\n", d.Row, d.Col, md(d.Want), md(d.Got))
			} else {
				p("- %s: expected `%s`, got `%s`\n", md(d.Reason), md(d.Want), md(d.Got))
			}
		}
		p("\n")
	}
}

// idList renders case ids for a table cell. Past a couple of dozen the list
// stops being something a person reads and starts being something that pushes
// the rest of the table off the screen; the count beside it is exact either
// way, and the JSON and CSV always carry every id.
// passingIDs names the cases that contradicted a claim, saying so when the
// report is carrying only the first few of them.
func passingIDs(d runner.DeclarationCheck) string {
	switch {
	case len(d.Passing) == 0:
		return ""
	case len(d.Passing) < d.Pass:
		return ", among them " + idList(d.Passing)
	default:
		return ": " + idList(d.Passing)
	}
}

func idList(ids []string) string {
	const shown = 12
	parts := make([]string, 0, shown+1)
	for i, id := range ids {
		if i == shown {
			parts = append(parts, fmt.Sprintf("and %d more", len(ids)-shown))
			break
		}
		parts = append(parts, "`"+id+"`")
	}
	return strings.Join(parts, ", ")
}

func claimList(c *runner.CaseResult) string {
	var parts []string
	if len(c.Subclauses) > 0 {
		parts = append(parts, "subclause "+strings.Join(c.Subclauses, ", "))
	}
	if len(c.Features) > 0 {
		parts = append(parts, "feature "+strings.Join(c.Features, ", "))
	}
	if len(c.Conditions) > 0 {
		parts = append(parts, "condition "+strings.Join(c.Conditions, ", "))
	}
	if len(c.Productions) > 0 {
		parts = append(parts, "production <"+strings.Join(c.Productions, ">, <")+">")
	}
	return strings.Join(parts, "; ")
}

func writeLatency(b io.Writer, rep *runner.Report) {
	ran := judged(rep)
	if len(ran) == 0 {
		return
	}
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	p("## Latency, per case\n\n")
	p("A read case ran %d time%s after %d warmup%s. Percentiles are nearest-rank over that many samples and are not interpolated: with this few samples an interpolated p99 would be an invention.\n\n",
		rep.Run.Repeats, plural(rep.Run.Repeats), rep.Run.Warmups, plural(rep.Run.Warmups))
	p("%s\n\n", roundTripSentence(rep, backtickCode))
	p("**How** is the treatment that produced the samples. `series` is that default. `restored` is a statement that changes the graph, measured with the fixture rebuilt before each execution so that every sample is its first application; the rebuilds are outside the samples, and the process and storage figures for such a case describe its last execution. `cold-once` is a statement that changes the graph and could not be repeated that way, which is one sample and no distribution.\n\n")
	p("| Case | how | n | min | p50 | p90 | p99 | max | mean | stddev | MAD | rows | q/s | rows/s |\n")
	p("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for i := range ran {
		c := ran[i]
		s := c.Stats
		p("| `%s` | %s | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			c.ID, timingCell(c), s.Count,
			metrics.Format(s.Min), metrics.Format(s.P50), metrics.Format(s.P90),
			metrics.Format(s.P99), metrics.Format(s.Max), metrics.Format(s.Mean),
			metrics.Format(s.StdDev), metrics.Format(s.MAD),
			num(s.MeanRows), num(s.QueriesPerSec), num(s.RowsPerSec))
	}
	p("\n")
	if near := nearTheFloor(rep, ran); len(near) > 0 {
		p("Within twice the floor, so what separates these from each other is mostly the round trip: %s.\n\n", idList(near))
	}
	timingNotes(b, ran)
}

// FloorMultiple is how many times the round trip a case's p50 has to clear
// before the report stops warning that the two are close. Two is the point at
// which at least half of what was measured is the engine, which is the weakest
// claim worth making.
const FloorMultiple = 2

// roundTripSentence states the floor, or says there is none.
//
// The floor is not a detail of method. It is the largest single thing a reader
// can get wrong from this table: two engines reached over two different
// transports have two different numbers under every row, and a case at the
// floor is a measurement of the pipe. The sentence is printed above the table
// rather than in the methodology section for that reason.
// It takes a code formatter because both renderers print it and they mark up a
// statement differently. The alternative, writing the sentence twice, is how
// two renderers come to describe the same measurement in two ways.
func roundTripSentence(rep *runner.Report, code func(string) string) string {
	rt := rep.Engine.RoundTrip
	if !rt.OK {
		reason := rt.Note
		if reason == "" {
			reason = "the run did not measure one"
		}
		return fmt.Sprintf("There is no floor under this table: %s, the cheapest statement the harness knows how to ask, was not measured against this engine (%s). Some part of every figure below is the harness reaching the engine and this run cannot say how much.",
			code(runner.FloorStatement), oneLine(reason))
	}
	return fmt.Sprintf("%s — one row of one constant, so a parse, a plan, an execution and a round trip and nothing else — takes %s at p50 and %s at p99 against this engine, over %d samples. That is the floor under every row below: it is the harness reaching the engine, it is paid by every case, and it is not the same for an engine embedded behind a pipe as for one across a socket. Two engines' latencies are worth comparing to the extent that both stand well clear of their own floors.",
		code(runner.FloorStatement), metrics.Format(rt.Stats.P50), metrics.Format(rt.Stats.P99), rt.Stats.Count)
}

// backtickCode marks up a statement for Markdown, and plainCode leaves it
// alone for a renderer that will escape the result and wrap it itself.
func backtickCode(s string) string { return "`" + md(s) + "`" }
func plainCode(s string) string    { return s }

// nearTheFloor lists the cases whose p50 is within FloorMultiple of the round
// trip, which is the set a reader must not draw a comparison from.
func nearTheFloor(rep *runner.Report, ran []*runner.CaseResult) []string {
	rt := rep.Engine.RoundTrip
	if !rt.OK || rt.Stats.P50 <= 0 {
		return nil
	}
	var ids []string
	for _, c := range ran {
		if c.Stats.Count > 0 && c.Stats.P50 <= FloorMultiple*rt.Stats.P50 {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// writePlans prints what the engine said about how it ran each case, folded
// away so the section costs a reader nothing until a latency sends them looking.
//
// It sits under the latency table because that is the only reason to read it.
// A plan is not evidence of conformance and is never scored: it is the answer
// to "why was that one slow", and until now that answer required reproducing
// the run by hand against a graph the harness had already deleted.
//
// Nothing here is comparable across engines. Two engines' plans are two
// vocabularies for two execution models, and a reader who lines them up side by
// side is comparing the words. The heading says so once rather than the section
// implying otherwise by its shape.
func writePlans(b io.Writer, rep *runner.Report) {
	var have []*runner.CaseResult
	for _, c := range judged(rep) {
		if c.Plan != "" {
			have = append(have, c)
		}
	}
	if len(have) == 0 {
		return
	}
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	p("## Query plans\n\n")
	p("What the engine says it did, in the engine's own words, for the %d case%s that could be asked without running the statement again. It is recorded and not scored: plans are not comparable between engines, and this section exists so that a surprising row in the table above has something attached to it.\n\n",
		len(have), plural(len(have)))
	p("A plan is taken once, after the measured repetitions and outside the sampler, so it costs no latency figure anything. For a case that changes the graph that means the plan describes the graph as the statement left it.\n\n")
	for _, c := range have {
		p("<details>\n<summary><code>%s</code> — %s</summary>\n\n", c.ID, md(c.Name))
		p("```\n%s\n```\n\n", strings.TrimRight(c.Plan, "\n"))
		p("</details>\n\n")
	}
}

// timingCell names the treatment. A reader comparing two rows is entitled to
// know that one of them is a single cold sample before drawing anything from
// the comparison.
func timingCell(c *runner.CaseResult) string {
	if c.Timing == "" {
		return string(runner.TimingSeries)
	}
	return string(c.Timing)
}

// timingNotes is the arithmetic behind every treatment the runner had to choose
// rather than apply, one line per case, printed under the table it explains.
func timingNotes(b io.Writer, cases []*runner.CaseResult) {
	var lines []string
	for _, c := range cases {
		if n := c.TimingNote; n != "" {
			lines = append(lines, fmt.Sprintf("- `%s`: %s\n", c.ID, n))
		}
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprint(b, "Where a case did not get the default treatment, this is why:\n\n")
	for _, l := range lines {
		fmt.Fprint(b, l)
	}
	fmt.Fprint(b, "\n")
}

func writeResources(b io.Writer, rep *runner.Report) {
	ran := judged(rep)
	if len(ran) == 0 {
		return
	}
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	p("## Process and storage, per case\n\n")
	p("A dash means the measurement was not available on this platform for this process, which is different from zero. I/O counters and page faults, in particular, are unreadable for another user's process on most systems, and a server engine's data directory is not on this machine at all.\n\n")
	p("| Case | CPU user | CPU sys | util | RSS peak | RSS end | VMS peak | threads | read | write | minor flt | major flt | vol cs | invol cs | disk after | disk Δ | alloc Δ | files | reads |\n")
	p("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for i := range ran {
		c := ran[i]
		pr, d := c.Process, c.Disk
		p("| `%s` | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %d |\n",
			c.ID,
			dashDur(pr.CPUOK, pr.CPUUser), dashDur(pr.CPUOK, pr.CPUSys),
			dashPct(c),
			dashBytes(pr.MemoryOK, pr.RSSPeak), dashBytes(pr.MemoryOK, pr.RSSEnd),
			dashBytes(pr.MemoryOK, pr.VMSPeak), dashInt(pr.MemoryOK, int64(pr.NumThread)),
			dashBytes(pr.IOOK, pr.ReadBytes), dashBytes(pr.IOOK, pr.WriteBytes),
			dashInt(pr.FaultsOK, pr.MinorFaults), dashInt(pr.FaultsOK, pr.MajorFaults),
			dashInt(pr.CtxOK, pr.VoluntaryCS), dashInt(pr.CtxOK, pr.InvoluntaryCS),
			dashBytes(d.OK, d.BytesAfter), dashSigned(d.OK, d.Growth()),
			dashSigned(d.OK, d.AllocGrowth()), dashInt(d.OK, int64(d.Files)),
			pr.Samples)
	}
	p("\n")
}

func writeLoads(b io.Writer, rep *runner.Report) {
	var loads []runner.CaseResult
	for _, c := range rep.Cases {
		if c.Load != nil {
			loads = append(loads, c)
		}
	}
	if len(loads) == 0 {
		return
	}
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	p("## Ingest\n\n")
	p("One row per fixture load. Cases that reused a graph another case had already loaded contribute nothing here, which is why these times must not be summed into a per-case cost.\n\n")
	p("**Wall** is everything the harness waited for; **engine** is the part of it the engine itself spent, where the adapter can separate the two, and is what the rates are computed against. The gap between them is this harness's cost of getting the fixture in — a staging file, an encoded batch, a process start — and belongs to the route rather than to the store.\n\n")
	p("%s\n\n", floorSentence(rep))
	if s := schemaSentence(loadsOf(loads)); s != "" {
		p("%s\n\n", s)
	}
	p("| Fixture | Triggered by | Nodes | Edges | Wall | Engine | nodes/s | edges/s | Apparent Δ | Allocated Δ | × floor | graph | bits/edge | bytes/node | RSS peak | CPU |\n")
	p("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for i := range loads {
		c := loads[i]
		l := c.Load
		cpu := "—"
		if l.Process.CPUOK {
			cpu = metrics.Format(l.Process.CPUUser + l.Process.CPUSys)
		}
		engine := "—"
		if l.EngineWall > 0 {
			engine = metrics.Format(l.EngineWall)
		}
		p("| %s | `%s` | %d | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			c.Fixture, c.ID, l.Nodes, l.Edges, metrics.Format(l.Wall), engine,
			num(l.NodesPerSec), num(l.EdgesPerSec),
			dashSigned(l.Disk.OK, l.Disk.Growth()), dashSigned(l.Disk.OK, l.Disk.AllocGrowth()),
			floorCell(l), dashBytes(l.SchemaBytes > 0, l.GraphBytes),
			dashFloat(l.DensityOK, l.BitsPerEdge), dashFloat(l.DensityOK, l.BytesPerNode),
			dashBytes(l.Process.MemoryOK, l.Process.RSSPeak), cpu)
	}
	p("\n")
	if notes := densityNotes(loadsOf(loads)); len(notes) > 0 {
		p("Where the density columns hold a dash, the run declined to divide:\n\n")
		for _, n := range notes {
			p("- %s\n", n)
		}
		p("\n")
	}
}

// floorSentence explains the empty store the density columns are checked
// against, and says so even when there is no empty store to report. A reader
// who cannot see the check has to know to make it, and the whole point of the
// check is that nobody did.
func floorSentence(rep *runner.Report) string {
	es := rep.Engine.EmptyStore
	if !es.OK {
		reason := es.Note
		if reason == "" {
			reason = "the run did not measure one"
		}
		return fmt.Sprintf("The bits/edge and bytes/node columns are withheld throughout, because they are the whole store divided by the graph and this run does not know how much of the store is the engine's own preallocation: %s.", reason)
	}
	return fmt.Sprintf("The × floor column is the loaded store over this engine's empty one, which weighs %s across %d files with no graph in it. Below %g× the store is mostly that floor, dividing it by the fixture measures the preallocation, and bits/edge and bytes/node are withheld rather than printed. A load that clears the floor is checked once more, per element, because an engine whose empty store is measured before it has written anything understates its own fixed cost: a graph whose single node or single edge appears to weigh more than the whole empty store is measuring allocation, and its density is withheld too. That second check catches the shares that are absurd rather than the ones that are merely inflated, so a small fixture that clears both is still worth reading beside a large one rather than on its own.",
		metrics.FormatBytes(es.Bytes), es.Files, metrics.DensityFloor)
}

// schemaSentence describes the exact route, for an engine that can say which
// part of its store is the graph. It replaces nothing in floorSentence above:
// both are printed, because the two tests are applied per load and a run can
// have one engine reporting a schema size for some loads and not others.
//
// The sentence is worth the space. A reader looking at bits/edge for a fixture
// of six nodes would be right to distrust it, and the reason to trust this one
// is not visible in the number: it is that the engine's own fixed cost was
// subtracted rather than assumed to be small.
func schemaSentence(loads []*metrics.Load) string {
	var with *metrics.Load
	for _, l := range loads {
		if l.SchemaBytes > 0 {
			with = l
			break
		}
	}
	if with == nil {
		return ""
	}
	s := fmt.Sprintf("This engine reports how much of its store is fixed by the shape of the database rather than by the graph, so for those loads the **graph** column is the store with that part taken off, and it is that column and not the whole store that bits/edge and bytes/node divide. The × floor column is the store over the fixed part, which for the first such load was %s of a %s store.",
		metrics.FormatBytes(with.SchemaBytes), metrics.FormatBytes(with.Disk.BytesAfter))
	if with.AllocUnit > 0 {
		s += fmt.Sprintf(" A store that grows in units of %s still rounds the last one up, so a density is withheld until the graph fills at least %g of them and the rounding is under a tenth of the figure.",
			metrics.FormatBytes(with.AllocUnit), metrics.DensityFloor)
	}
	return s
}

func floorCell(l *metrics.Load) string {
	if l.FloorRatio <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f×", l.FloorRatio)
}

// loadsOf pulls the loads out of the cases that triggered them.
func loadsOf(cases []runner.CaseResult) []*metrics.Load {
	out := make([]*metrics.Load, 0, len(cases))
	for i := range cases {
		if cases[i].Load != nil {
			out = append(out, cases[i].Load)
		}
	}
	return out
}

// densityNotes collects the distinct reasons density was withheld, in the order
// they first appear. Nine loads of the same engine usually share one reason,
// and printing it nine times would bury it.
func densityNotes(loads []*metrics.Load) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range loads {
		if l.DensityOK || l.DensityNote == "" || seen[l.DensityNote] {
			continue
		}
		seen[l.DensityNote] = true
		out = append(out, l.DensityNote)
	}
	return out
}

// ExplorationHeading is the section a grammar walk gets. It is a description of
// what the statements are and not of what they proved, because they proved
// nothing: no statement in this section cites a clause.
const ExplorationHeading = "Statements the grammar admits"

// writeExploration prints the walk of the published BNF.
//
// It comes after every table that counts something and before the
// implementation-defined section, which is the right place for it: a reader who
// has got this far has the engine's score and knows that nothing from here on
// is part of it. Every sentence in the section is written to keep it that way.
func writeExploration(b io.Writer, rep *runner.Report) {
	x := rep.Exploration
	if x == nil || x.Totals.Cases == 0 {
		return
	}
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	p("## %s\n\n", ExplorationHeading)
	p("These statements were written by a walk of ISO's published BNF, not by a person. They cite no clause and carry no expectation, so nothing here is a conformance result and nothing here is in the scoreboard. What the section is for is the opposite direction: the corpus is hand written, 814 productions cannot be covered by hand, and a walk reaches constructs nobody would think to write.\n\n")
	p("| | |\n|---|---|\n")
	p("| Seed | `%d` |\n", x.Seed)
	p("| Start production | `<%s>` |\n", x.Start)
	p("| Statements walked | %d, of which %d were different |\n", x.Walked, x.Distinct)
	p("| Productions reachable | %d of %d, with %d replaced by a token the harness supplies |\n",
		x.Coverage.Reachable, x.Coverage.Total, x.Coverage.Cut)
	if n := len(x.Coverage.Unwritable); n > 0 {
		p("| Reachable but unwritable | %d, because every path through them ends in a production ISO defines in prose |\n", n)
	}
	if x.Known > 0 {
		p("| Already reviewed | %d |\n", x.Known)
	}
	p("| Leads | %d |\n", len(x.Leads))
	p("\n")
	p("The same seed and the same grammar give the same statements in the same order on every machine, so a lead below can be reproduced exactly.\n\n")

	if len(x.Leads) == 0 {
		p("The engine accepted, or refused on grounds other than syntax, every statement the walk put to it. That is not a claim that its parser matches the standard: %d statements is a sample of a language, the walk stops at tokens this harness chooses rather than at characters, and the grammar admits far more than any walk of this size reaches.\n\n", x.Distinct)
		return
	}

	p("### Leads\n\n")
	p("Each of these is a statement the published grammar admits and the engine rejected with GQLSTATUS %s, invalid syntax. A lead is the beginning of work and not the end of it: the walk knows only that the statement is well formed, the harness supplies its own tokens for the productions ISO writes in prose, and §24.5.3 lets an implementation document a restriction. What makes a lead worth a person's time is the reduced form, which is the smallest statement the engine still called a syntax error.\n\n", runner.StatusSyntaxError)
	for i := range x.Leads {
		l := x.Leads[i]
		p("#### `%s`\n\n", l.ID)
		p("```gql\n%s\n```\n\n", l.Reduced)
		if l.Reduced != l.Statement {
			p("Reduced from %d candidate%s put to the engine. The statement the walk originally wrote was:\n\n", l.Tried, plural(l.Tried))
			p("```gql\n%s\n```\n\n", l.Statement)
		}
		p("| | |\n|---|---|\n")
		p("| GQLSTATUS | `%s` |\n", l.GQLStatus)
		if l.Message != "" {
			p("| Engine's words | %s |\n", md(oneLine(l.Message)))
		}
		p("| Fingerprint | `%s` |\n", l.Fingerprint)
		p("\n")
		if len(l.Path) > 0 {
			p("Productions on the way down, outermost first:\n\n")
			p("`<%s>`\n\n", strings.Join(l.Path, ">` → `<"))
		}
	}
	p("To settle a lead, add its fingerprint to the promotion list with either the id of the hand-written case it became or a note saying why it is not a defect. The walk is seeded and would report it again on every run otherwise.\n\n")
}

// writeImplementation prints what the run observed of the behaviour ISO
// delegates.
//
// It sits after the ingest tables and before the methodology because it is not
// a result: nothing above it counted any of this, and a reader who has got this
// far already knows what the engine scored. The rendering is the impdef
// package's own, unchanged, so that the section a vendor pastes into a 24.5.2
// statement and the section printed here cannot differ.
func writeImplementation(b io.Writer, rep *runner.Report) {
	if rep.Implementation.Len() == 0 {
		return
	}
	// WriteSection's only error is the writer's, and every other section here
	// ignores that the same way: the bufio flush at the end of WriteMarkdown
	// reports it.
	_ = impdef.WriteSection(b, rep.Implementation)
}

func writeMethodology(b io.Writer, rep *runner.Report) {
	p := func(f string, a ...any) { fmt.Fprintf(b, f, a...) }
	p("## How to read this\n\n")
	p("- **Conformance is not a percentage.** ISO/IEC 39075 asks for a claim, not a score: §24.2 fixes what minimum conformance is, §24.3 makes each optional feature its own claim by code, and §24.5.2 says what an implementation must state to claim conformance at all. The scoreboard above is evidence for such a statement, not the statement itself.\n")
	p("- **Skips are the engine's own declaration.** They come from the capability table, which the adapter fills in before the run. An engine cannot be made to look better by skipping more, because skips are never in the pass-rate denominator.\n")
	p("- **A pass on error text is weaker than a pass on a code.** Where an engine reports no GQLSTATUS, a condition case can only confirm that something was refused. Those passes are counted separately in the headline.\n")
	p("- **Apparent and allocated disk sizes both appear.** A sparse or compressed file makes them differ, and quoting only one of them flatters by accident.\n")
	if rep.Exploration != nil && rep.Exploration.Totals.Cases > 0 {
		p("- **Generated statements are leads, not results.** They cite no clause, so they are in no total and in no pass rate. A lead becomes a result only when a person writes a case for it that cites one.\n")
	}
	p("- **Absolute latencies are about this machine.** %s\n", hostCaveat(rep))
	p("\n")
	p("Generated by gql-compat, report schema %d, %s.\n",
		rep.Schema, rep.Generated.UTC().Format("2006-01-02 15:04:05 UTC"))
}

func hostCaveat(rep *runner.Report) string {
	if rep.Host.Containerised {
		return "This run was on a virtualised or shared host, where a p99 says as much about the neighbours as about the engine. Compare engines within a run, not numbers across runs."
	}
	return "Compare engines within one run; comparing a latency here against one from another machine compares the machines."
}

func judged(rep *runner.Report) []*runner.CaseResult {
	var out []*runner.CaseResult
	for i := range rep.Cases {
		if rep.Cases[i].Stats.Count > 0 {
			out = append(out, &rep.Cases[i])
		}
	}
	return out
}

func tick(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func rate(k runner.KindTotals) string {
	if k.Pass+k.Fail == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", 100*k.Rate())
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func fallback(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// md escapes the characters that would otherwise break a table cell.
func md(s string) string {
	s = oneLine(s)
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}

func num(f float64) string {
	switch {
	case f == 0:
		return "0"
	case f >= 1000:
		return fmt.Sprintf("%.0f", f)
	case f >= 10:
		return fmt.Sprintf("%.1f", f)
	default:
		return fmt.Sprintf("%.2f", f)
	}
}

func dashDur[T ~int64](ok bool, d T) string {
	if !ok {
		return "—"
	}
	return metrics.Format(durationOf(d))
}

func dashBytes(ok bool, n int64) string {
	if !ok {
		return "—"
	}
	return metrics.FormatBytes(n)
}

func dashSigned(ok bool, n int64) string {
	if !ok {
		return "—"
	}
	if n < 0 {
		return "-" + metrics.FormatBytes(-n)
	}
	return metrics.FormatBytes(n)
}

func dashInt(ok bool, n int64) string {
	if !ok {
		return "—"
	}
	return strconv.FormatInt(n, 10)
}

func dashFloat(ok bool, f float64) string {
	if !ok {
		return "—"
	}
	return num(f)
}

func dashPct(c *runner.CaseResult) string {
	s := utilization(c)
	if s == "" {
		return "—"
	}
	series := metrics.Series{Process: c.Process, Samples: []metrics.Sample{{Wall: c.Stats.Total}}}
	u, _ := series.CPUUtilization()
	return fmt.Sprintf("%.2f×", u)
}

// sortKeys orders the keys of a coverage table.
//
// Plain string order would put subclause 14.10 before 14.4, which reads as a
// mistake in a document whose whole point is to be checked against another
// document. Dotted numbers are compared segment by segment, numerically where
// both segments are numbers; everything else falls back to string order, which
// is what feature codes and GQLSTATUS codes want anyway.
func sortKeys(keys []string) {
	sort.Slice(keys, func(i, j int) bool { return lessKey(keys[i], keys[j]) })
}

func lessKey(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		if aerr == nil && berr == nil {
			return an < bn
		}
		return as[i] < bs[i]
	}
	return len(as) < len(bs)
}

// durationOf converts a duration-like value back to a time.Duration for the
// formatter, which is what lets the dash helpers take either.
func durationOf[T ~int64](d T) time.Duration { return time.Duration(d) }
