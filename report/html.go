package report

import (
	"bufio"
	"fmt"
	"html"
	"io"
	"strconv"
	"strings"

	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/impdef"
	"github.com/tamnd/gql-compat/metrics"
	"github.com/tamnd/gql-compat/runner"
)

// WriteHTML renders the report as one self-contained page.
//
// Self-contained is the point: no stylesheet, no font, no script fetched from
// anywhere. A conformance report gets attached to a CI run, mailed around, and
// opened years later from a directory with no network, and it has to look the
// same then as now.
//
// It carries the same sections as the Markdown report and adds only what a
// browser can do that a text file cannot: a table of contents that follows the
// page, and a filter box over the long per-case tables.
func WriteHTML(w io.Writer, rep *runner.Report) error {
	b := bufio.NewWriter(w)
	defer func() { _ = b.Flush() }()

	h := &htmlWriter{w: b}
	h.head(rep)
	h.nav(rep)

	h.p(`<main>`)
	h.title(rep)
	h.overview(rep)
	h.capabilities(rep)
	h.challenge(rep)
	h.scoreboard(rep)
	h.coverage(rep)
	h.skips(rep)
	h.failures(rep)
	h.latency(rep)
	h.resources(rep)
	h.loads(rep)
	h.exploration(rep)
	h.implementation(rep)
	h.methodology(rep)
	h.p(`</main>`)

	h.p(`<script>%s</script>`, filterScript)
	h.p(`</body></html>`)
	return b.Flush()
}

type htmlWriter struct{ w io.Writer }

func (h *htmlWriter) p(format string, args ...any) {
	fmt.Fprintf(h.w, format, args...)
	fmt.Fprintln(h.w)
}

// e escapes a value for element text or an attribute.
func e(s string) string { return html.EscapeString(s) }

func (h *htmlWriter) head(rep *runner.Report) {
	title := fmt.Sprintf("GQL conformance report — %s %s", rep.Engine.Adapter, rep.Engine.Version)
	h.p(`<!doctype html>`)
	h.p(`<html lang="en"><head>`)
	h.p(`<meta charset="utf-8">`)
	h.p(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	h.p(`<meta name="generator" content="gql-compat">`)
	h.p(`<title>%s</title>`, e(title))
	h.p(`<style>%s</style>`, stylesheet)
	h.p(`</head><body>`)
}

// section is one entry in the table of contents. The list is fixed rather than
// derived, so two reports have the same navigation even when one of them has
// no failures to show.
type section struct {
	ID, Label string
	Present   bool
}

func sections(rep *runner.Report) []section {
	hasLoads := false
	for i := range rep.Cases {
		if rep.Cases[i].Load != nil {
			hasLoads = true
			break
		}
	}
	ran := len(judged(rep)) > 0
	return []section{
		{"overview", "What was measured", true},
		{"capabilities", "Declared capabilities", true},
		{"challenge", "The declaration under challenge", rep.Run.Challenge},
		{"scoreboard", "Scoreboard", true},
		{"coverage", "Coverage of the standard", true},
		{"skips", "What the engine was not asked", rep.Totals.Skip > 0},
		{"failures", "Failures and errors", len(failures(rep)) > 0},
		{"latency", "Latency", ran},
		{"resources", "Process and storage", ran},
		{"ingest", "Ingest", hasLoads},
		{"exploration", ExplorationHeading, rep.Exploration != nil && rep.Exploration.Totals.Cases > 0},
		{"implementation", impdef.Heading, rep.Implementation.Len() > 0},
		{"methodology", "How to read this", true},
	}
}

func (h *htmlWriter) nav(rep *runner.Report) {
	h.p(`<nav aria-label="Contents"><h2>Contents</h2><ol>`)
	for _, s := range sections(rep) {
		if !s.Present {
			continue
		}
		h.p(`<li><a href="#%s">%s</a></li>`, s.ID, e(s.Label))
	}
	h.p(`</ol></nav>`)
}

func (h *htmlWriter) title(rep *runner.Report) {
	t := rep.Totals
	h.p(`<header>`)
	h.p(`<h1>GQL conformance report: <code>%s</code></h1>`, e(rep.Engine.Adapter))
	h.p(`<p class="lede">%s</p>`, inlineMarkdown(headline(rep)))
	if rep.Run.Mode == runner.ModeCompat {
		h.p(`<p class="warn"><strong>Compatibility run.</strong> The statements executed were this engine's own documented spellings, not standard GQL. Nothing on this page is a conformance result.</p>`)
	}
	h.p(`<div class="cards">`)
	judgedN := t.Pass + t.Fail
	h.card("Pass rate", rate(runner.KindTotals{Pass: t.Pass, Fail: t.Fail}),
		fmt.Sprintf("%d of %d judged", t.Pass, judgedN), classFor(t))
	h.card("Failed", strconv.Itoa(t.Fail), "verdict against the engine", failClass(t.Fail))
	h.card("Skipped", strconv.Itoa(t.Skip), "never asked — see below", "neutral")
	h.card("Errors", strconv.Itoa(t.Error), "harness or session", failClass(t.Error))
	h.card("Weak passes", strconv.Itoa(t.WeakEvidence), "error text, not GQLSTATUS", weakClass(t.WeakEvidence))
	h.p(`</div>`)
	h.p(`</header>`)
}

func (h *htmlWriter) card(label, value, note, class string) {
	h.p(`<div class="card %s"><div class="v">%s</div><div class="k">%s</div><div class="n">%s</div></div>`,
		class, e(value), e(label), e(note))
}

func classFor(t runner.Totals) string {
	switch {
	case t.Pass+t.Fail == 0:
		return "neutral"
	case t.Fail == 0:
		return "good"
	default:
		return "bad"
	}
}

func failClass(n int) string {
	if n > 0 {
		return "bad"
	}
	return "good"
}

func weakClass(n int) string {
	if n > 0 {
		return "warnish"
	}
	return "neutral"
}

func (h *htmlWriter) overview(rep *runner.Report) {
	h.p(`<section id="overview"><h2>What was measured</h2>`)
	h.p(`<table class="kv">`)
	h.kv("Engine", "<code>"+e(rep.Engine.Adapter)+"</code>")
	h.kv("Version", e(fallback(rep.Engine.Version, "unknown")))
	h.kv("Mode", e(string(rep.Run.Mode)))
	h.kv("Cases", strconv.Itoa(rep.Totals.Cases))
	h.kv("Repetitions per case", fmt.Sprintf("%d (after %d warmup%s)",
		rep.Run.Repeats, rep.Run.Warmups, plural(rep.Run.Warmups)))
	h.kv("Statement timeout", e(metrics.Format(rep.Run.Timeout)))
	h.kv("Sampler interval", e(metrics.Format(rep.Run.SampleInterval)))
	if rep.Run.Selector != "" {
		h.kv("Selector", "<code>"+e(rep.Run.Selector)+"</code>")
	}
	h.kv("Started", rep.Run.Started.UTC().Format("2006-01-02 15:04:05 UTC"))
	h.kv("Wall time", e(metrics.Format(rep.Run.Wall)))
	h.kv("Standard", "ISO/IEC 39075:2024, artifacts from "+link(rep.Run.ISOSource))
	h.p(`</table>`)

	hi := rep.Host
	h.p(`<h3>Host</h3><table class="kv">`)
	h.kv("Platform", e(fmt.Sprintf("%s (%s/%s)", fallback(hi.Platform, "unknown"), hi.OS, hi.Arch)))
	if hi.Kernel != "" {
		h.kv("Kernel", e(hi.Kernel))
	}
	if hi.CPUModel != "" {
		h.kv("CPU", e(hi.CPUModel))
	}
	h.kv("Cores", fmt.Sprintf("%d physical, %d logical, GOMAXPROCS %d",
		hi.CPUCores, hi.CPULogical, hi.GOMAXPROCS))
	if hi.MemoryTotal > 0 {
		h.kv("Memory", e(metrics.FormatBytes(hi.MemoryTotal)))
	}
	h.kv("Go", e(hi.GoVersion))
	if hi.Containerised {
		h.kv("Virtualised", `<span class="warnish">yes — absolute latencies on a shared runner are not comparable across machines</span>`)
	}
	h.p(`</table></section>`)
}

func (h *htmlWriter) kv(k, vHTML string) {
	h.p(`<tr><th>%s</th><td>%s</td></tr>`, e(k), vHTML)
}

func link(u string) string {
	if !strings.HasPrefix(u, "http") {
		return e(u)
	}
	return `<a href="` + e(u) + `">` + e(u) + `</a>`
}

func (h *htmlWriter) capabilities(rep *runner.Report) {
	caps := rep.Engine.Capabilities
	h.p(`<section id="capabilities"><h2>Declared capabilities</h2>`)
	h.p(`<p>These are the engine's own statements about what it can hold and do, made before any case ran. Every skip below traces back to one of them.</p>`)
	h.p(`<table class="grid caps"><thead><tr><th>Capability</th><th>Supported</th></tr></thead><tbody>`)
	for _, c := range fixture.AllCapabilities {
		h.capRow(string(c), caps.Data[c])
	}
	h.capRow("GQLSTATUS reporting", caps.GQLStatus)
	h.capRow("Named parameters", caps.Parameters)
	h.capRow("Explicit transactions", caps.Transactions)
	h.capRow("Multiple statements", caps.MultipleStatements)
	h.capRow("Resettable in place", caps.Isolated)
	h.p(`</tbody></table>`)
	if len(caps.Notes) > 0 {
		h.p(`<h3>Adapter notes</h3><ul>`)
		for _, n := range caps.Notes {
			h.p(`<li>%s</li>`, e(n))
		}
		h.p(`</ul>`)
	}
	h.p(`</section>`)
}

// challenge is the HTML of what became of the cases the declaration would have
// excluded. See writeChallenge for why the interesting row is the one where
// nothing went wrong.
func (h *htmlWriter) challenge(rep *runner.Report) {
	if !rep.Run.Challenge {
		return
	}
	h.p(`<section id="challenge"><h2>The declaration under challenge</h2>`)
	h.p(`<p class="warn"><strong>Not a conformance run.</strong> This run ignored the table above and put the excluded cases to the engine anyway, so its failures are expected and its totals are not a score. A claim is contradicted when every case it excluded passed, which is the one outcome an engine that lacks the thing cannot produce.</p>`)
	if len(rep.Declarations) == 0 {
		h.p(`<p>No case was excluded by the declaration, so there was nothing to challenge.</p></section>`)
		return
	}
	h.p(`<table class="grid"><thead><tr><th>Claimed absent</th><th>Excluded by</th><th class="n">Cases</th><th class="n">Pass</th><th class="n">Fail</th><th class="n">Error</th><th>Verdict</th></tr></thead><tbody>`)
	var wrong []runner.DeclarationCheck
	for _, d := range rep.Declarations {
		cls, v := "yes", "claim stands"
		switch {
		case d.Contradicted:
			cls, v = "no", "contradicted"
			wrong = append(wrong, d)
		case d.Unrefuted():
			cls, v = "warnish", "not refuted"
		}
		h.p(`<tr><td><code>%s</code></td><td>%s</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="c %s">%s</td></tr>`,
			e(d.Claim), e(string(d.Reason)), d.Cases, d.Pass, d.Fail, d.Error, cls, e(v))
	}
	h.p(`</tbody></table>`)
	for _, d := range wrong {
		h.p(`<p>The engine declares <code>%s</code> absent, and all %d case%s it excluded passed. Either the declaration is out of date or those cases are reaching a verdict without the thing they claim to need, and both are worth a look before the next run believes the declaration again.</p>`,
			e(d.Claim), d.Cases, plural(d.Cases))
		h.p(`<ul class="ids">`)
		for _, id := range d.Passing {
			h.p(`<li><code>%s</code></li>`, e(id))
		}
		h.p(`</ul>`)
	}
	h.p(`</section>`)
}

func (h *htmlWriter) capRow(label string, ok bool) {
	cls, text := "no", "no"
	if ok {
		cls, text = "yes", "yes"
	}
	h.p(`<tr><td>%s</td><td class="c %s">%s</td></tr>`, e(label), cls, text)
}

func (h *htmlWriter) scoreboard(rep *runner.Report) {
	h.p(`<section id="scoreboard"><h2>Scoreboard</h2>`)
	h.p(`<p>Pass rate is passes over cases that produced a verdict. Skips and harness errors are excluded from the denominator and shown beside it, so a rate cannot be improved by refusing more work.</p>`)
	h.p(`<table class="grid"><thead><tr><th>Kind</th><th class="n">Cases</th><th class="n">Pass</th><th class="n">Fail</th><th class="n">Skip</th><th class="n">Error</th><th class="n">Pass rate</th><th>&nbsp;</th></tr></thead><tbody>`)
	for _, r := range kindRows(rep) {
		h.p(`<tr><td>%s</td><td class="n">%d</td><td class="n">%d</td><td class="n %s">%d</td><td class="n">%d</td><td class="n %s">%d</td><td class="n">%s</td><td class="barcell">%s</td></tr>`,
			e(string(r.Kind)), r.Cases, r.Pass, failClass(r.Fail), r.Fail, r.Skip,
			failClass(r.Error), r.Error, rate(r.KindTotals), bar(r.KindTotals))
	}
	t := rep.Totals
	total := runner.KindTotals{Cases: t.Cases, Pass: t.Pass, Fail: t.Fail, Skip: t.Skip, Error: t.Error}
	h.p(`<tr class="total"><td>total</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="n">%s</td><td class="barcell">%s</td></tr>`,
		t.Cases, t.Pass, t.Fail, t.Skip, t.Error, rate(total), bar(total))
	// Below the total line, and marked as not being part of it. The rate cell
	// is the report's dash for a value that does not exist, which is what a
	// pass rate over statements that cite no clause is.
	if x := rep.Exploration; x != nil && x.Totals.Cases > 0 {
		g := x.Totals
		h.p(`<tr class="aside"><td><em>generated</em></td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="n na">&mdash;</td><td class="barcell">%s</td></tr>`,
			g.Cases, g.Pass, g.Fail, g.Skip, g.Error, bar(g))
	}
	h.p(`</tbody></table>`)
	if x := rep.Exploration; x != nil && x.Totals.Cases > 0 {
		h.p(`<p>The generated row is below the total because it is not in it. Those statements came from a walk of the published grammar, they cite no clause, and nothing in that row is a conformance result. See <a href="#exploration">%s</a>.</p>`, e(ExplorationHeading))
	}
	h.p(`</section>`)
}

// bar draws the pass/fail split with the skips alongside, in the same widths
// as the counts. It uses no colour alone to carry meaning: the numbers are in
// the row and the segments are titled.
func bar(k runner.KindTotals) string {
	total := k.Pass + k.Fail + k.Skip + k.Error
	if total == 0 {
		return ""
	}
	seg := func(class string, n int, label string) string {
		if n == 0 {
			return ""
		}
		return fmt.Sprintf(`<span class="seg %s" style="width:%.4f%%" title="%d %s"></span>`,
			class, 100*float64(n)/float64(total), n, label)
	}
	return `<span class="bar">` +
		seg("s-pass", k.Pass, "pass") +
		seg("s-fail", k.Fail, "fail") +
		seg("s-skip", k.Skip, "skipped") +
		seg("s-err", k.Error, "error") +
		`</span>`
}

func (h *htmlWriter) coverage(rep *runner.Report) {
	cov := rep.Coverage
	h.p(`<section id="coverage"><h2>Coverage of the standard</h2>`)
	h.p(`<p>Denominators come from ISO's own published surface, not from this corpus: %d optional features, %d GQLSTATUS codes, %d grammar productions, and %d clauses that specify behaviour. A corpus that tests twelve features reads as twelve of %d, which is the honest way to say it.</p>`,
		cov.FeaturesTotal, cov.ConditionsTotal, cov.ProductionsTotal, cov.SubclausesTotal, cov.FeaturesTotal)

	h.p(`<h3>Optional feature families</h3>`)
	h.p(`<table class="grid"><thead><tr><th>Family</th><th class="n">ISO features</th><th class="n">Tested here</th><th class="n">Supported</th><th class="n">No portable case</th><th>&nbsp;</th></tr></thead><tbody>`)
	var tested, supported, total, unwritable int
	for _, f := range cov.Families {
		total += f.Total
		tested += f.Tested
		supported += f.Supported
		unwritable += f.Unwritable
		if f.Tested == 0 && f.Unwritable == 0 {
			continue
		}
		h.p(`<tr><td><code>%s</code></td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="barcell">%s</td></tr>`,
			e(f.Family), f.Total, f.Tested, f.Supported, f.Unwritable, familyBar(f))
	}
	h.p(`<tr class="total"><td>all families</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td></td></tr>`,
		total, tested, supported, unwritable)
	h.p(`</tbody></table>`)
	if tested < total {
		h.p(`<p class="note">%d of the %d optional features this standard defines are not exercised by the corpus at all. They are neither supported nor unsupported here; they are untested, and this report says so rather than defaulting them either way.</p>`,
			total-tested, total)
	}
	if len(cov.Unwritable) > 0 {
		groups := groupUnwritable(cov.Unwritable)
		h.p(`<p class="note">%d of them will stay that way, in %d way%s.</p>`,
			len(cov.Unwritable), len(groups), plural(len(groups)))
		for _, g := range groups {
			h.p(`<p class="note">No case for %s, because %s.</p>`, e(codeList(g.Features())), e(g.Reason.Because()))
			h.p(`<ul class="note">`)
			for _, u := range g.Entries {
				h.p(`<li><code>%s</code>, which reaches the grammar at <code>&lt;%s&gt;</code>. %s</li>`,
					e(u.Feature), e(u.Production), e(u.Note))
			}
			h.p(`</ul>`)
		}
	}

	h.statusTable("Optional features tested", "Feature", cov.Features)
	h.p(`<h3>Mandatory behaviour</h3>`)
	h.p(`<p>ISO gives mandatory features no code, so a claim about one can only cite the subclause that specifies it. This corpus cites %d of the %d clauses that specify behaviour.</p>`,
		len(cov.Subclauses), cov.SubclausesTotal)
	h.statusTable("", "Subclause", cov.Subclauses)
	h.statusTable("GQLSTATUS conditions tested", "Code", cov.Conditions)
	h.p(`</section>`)
}

func familyBar(f runner.FamilyCoverage) string {
	if f.Total == 0 {
		return ""
	}
	pct := func(n int) float64 { return 100 * float64(n) / float64(f.Total) }
	return fmt.Sprintf(`<span class="bar"><span class="seg s-pass" style="width:%.4f%%" title="%d supported"></span><span class="seg s-part" style="width:%.4f%%" title="%d tested, not supported"></span></span>`,
		pct(f.Supported), f.Supported, pct(f.Tested-f.Supported), f.Tested-f.Supported)
}

func (h *htmlWriter) statusTable(heading, label string, items map[string]runner.Status) {
	if len(items) == 0 {
		return
	}
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sortKeys(keys)
	id := slug(fallback(heading, label))
	if heading != "" {
		h.p(`<h3 id="%s">%s</h3>`, id, e(heading))
	}
	h.filter(id)
	h.p(`<table class="grid filterable" data-filter="%s"><thead><tr><th>%s</th><th>Description</th><th class="n">Cases</th><th class="n">Pass</th><th class="n">Fail</th><th class="n">Skip</th><th>Verdict</th></tr></thead><tbody>`,
		id, e(label))
	for _, k := range keys {
		s := items[k]
		h.p(`<tr><td><code>%s</code></td><td>%s</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="n">%d</td><td class="%s">%s</td></tr>`,
			e(k), e(s.Description), s.Cases, s.Pass, s.Fail, s.Skip,
			verdictClass(s), e(plainVerdict(s)))
	}
	h.p(`</tbody></table>`)
}

func plainVerdict(s runner.Status) string {
	return strings.NewReplacer("**", "", "—", "—").Replace(verdict(s))
}

func verdictClass(s runner.Status) string {
	switch {
	case s.Supported():
		return "good"
	case s.Pass+s.Fail == 0:
		return "neutral"
	case s.Fail > 0:
		return "bad"
	default:
		return "warnish"
	}
}

func (h *htmlWriter) skips(rep *runner.Report) {
	groups := skipsByReason(rep)
	if len(groups) == 0 {
		return
	}
	h.p(`<section id="skips"><h2>What the engine was not asked</h2>`)
	h.p(`<p>A skip is a measurement. It says the engine declared, in advance, that it could not represent the data a case needs or accept the shape the case has. None of these are failures and none of them are passes.</p>`)
	h.p(`<table class="grid"><thead><tr><th>Reason</th><th class="n">Cases</th><th>Which</th></tr></thead><tbody>`)
	for _, g := range groups {
		h.p(`<tr><td>%s</td><td class="n">%d</td><td><details><summary>show</summary><ul class="ids">`,
			e(string(g.Reason)), len(g.IDs))
		for _, id := range g.IDs {
			h.p(`<li><code>%s</code></li>`, e(id))
		}
		h.p(`</ul></details></td></tr>`)
	}
	h.p(`</tbody></table>`)

	byCap := map[fixture.Capability]int{}
	for i := range rep.Cases {
		c := &rep.Cases[i]
		if c.Outcome != runner.Skip {
			continue
		}
		for _, m := range c.Missing {
			byCap[m]++
		}
	}
	if len(byCap) > 0 {
		h.p(`<h3>Missing capabilities, by how many cases each one cost</h3>`)
		h.p(`<table class="grid"><thead><tr><th>Capability</th><th class="n">Cases blocked</th></tr></thead><tbody>`)
		for _, c := range fixture.AllCapabilities {
			if n := byCap[c]; n > 0 {
				h.p(`<tr><td>%s</td><td class="n">%d</td></tr>`, e(string(c)), n)
			}
		}
		h.p(`</tbody></table>`)
	}
	h.p(`</section>`)
}

func (h *htmlWriter) failures(rep *runner.Report) {
	fs := failures(rep)
	if len(fs) == 0 {
		return
	}
	h.p(`<section id="failures"><h2>Failures and errors</h2>`)
	for i := range fs {
		c := &fs[i]
		cls := "bad"
		if c.Outcome == runner.Error {
			cls = "warnish"
		}
		h.p(`<article class="failure">`)
		h.p(`<h3><span class="tag %s">%s</span> <code>%s</code> — %s</h3>`,
			cls, e(string(c.Outcome)), e(c.ID), e(c.Name))
		h.p(`<p>%s.</p>`, e(strings.TrimSuffix(c.Reason, ".")))
		if claims := claimList(c); claims != "" {
			h.p(`<p class="note">Claims: %s.</p>`, e(claims))
		}
		h.p(`<pre class="gql">%s</pre>`, e(strings.TrimSpace(c.Statement)))
		h.p(`<ul>`)
		if c.WantStatus != "" || c.GotStatus != "" {
			h.p(`<li>Expected GQLSTATUS <code>%s</code>, engine reported <code>%s</code></li>`,
				e(fallback(c.WantStatus, "any failure")), e(fallback(c.GotStatus, "none")))
		}
		if c.Message != "" {
			h.p(`<li>Engine said: %s</li>`, e(oneLine(c.Message)))
		}
		if d := c.Diff; d != nil {
			h.p(`<li>Row %d, column %d: expected <code>%v</code>, got <code>%v</code></li>`,
				d.Row, d.Col, e(d.Want), e(d.Got))
		}
		h.p(`<li>Fixture <code>%s</code>, mode %s, evidence %s</li>`,
			e(fallback(c.Fixture, "none")), e(string(c.Mode)), e(fallback(string(c.Evidence), "none")))
		h.p(`</ul></article>`)
	}
	h.p(`</section>`)
}

func (h *htmlWriter) latency(rep *runner.Report) {
	ran := judged(rep)
	if len(ran) == 0 {
		return
	}
	h.p(`<section id="latency"><h2>Latency, per case</h2>`)
	h.p(`<p>Every case ran %d times after %d warmup%s. Percentiles are nearest-rank over that many samples and are not interpolated: with this few samples an interpolated p99 would be an invention.</p>`,
		rep.Run.Repeats, rep.Run.Warmups, plural(rep.Run.Warmups))
	h.p(`<p>%s</p>`, e(roundTripSentence(rep, plainCode)))
	if near := nearTheFloor(rep, ran); len(near) > 0 {
		h.p(`<p>Within twice the floor, so what separates these from each other is mostly the round trip:</p><ul class="ids">`)
		for _, id := range near {
			h.p(`<li><code>%s</code></li>`, e(id))
		}
		h.p(`</ul>`)
	}
	h.filter("latency")
	h.p(`<table class="grid filterable wide" data-filter="latency"><thead><tr>`)
	for _, col := range []string{"Case", "n", "min", "p50", "p90", "p95", "p99", "max", "mean", "stddev", "MAD", "rows", "q/s", "rows/s", "cells/s"} {
		h.p(`<th class="n">%s</th>`, e(col))
	}
	h.p(`</tr></thead><tbody>`)
	for _, c := range ran {
		s := c.Stats
		h.p(`<tr><td class="case">%s <code>%s</code></td><td class="n">%d</td>`+
			`<td class="n">%s</td><td class="n">%s</td><td class="n">%s</td><td class="n">%s</td><td class="n">%s</td><td class="n">%s</td>`+
			`<td class="n">%s</td><td class="n">%s</td><td class="n">%s</td>`+
			`<td class="n">%s</td><td class="n">%s</td><td class="n">%s</td><td class="n">%s</td></tr>`,
			dot(c.Outcome), e(c.ID), s.Count,
			e(metrics.Format(s.Min)), e(metrics.Format(s.P50)), e(metrics.Format(s.P90)),
			e(metrics.Format(s.P95)), e(metrics.Format(s.P99)), e(metrics.Format(s.Max)),
			e(metrics.Format(s.Mean)), e(metrics.Format(s.StdDev)), e(metrics.Format(s.MAD)),
			e(num(s.MeanRows)), e(num(s.QueriesPerSec)), e(num(s.RowsPerSec)), e(num(s.CellsPerSec)))
	}
	h.p(`</tbody></table></section>`)
}

func (h *htmlWriter) resources(rep *runner.Report) {
	ran := judged(rep)
	if len(ran) == 0 {
		return
	}
	h.p(`<section id="resources"><h2>Process and storage, per case</h2>`)
	h.p(`<p>A dash means the measurement was not available on this platform for this process, which is different from zero. I/O counters and page faults, in particular, are unreadable for another user's process on most systems, and a server engine's data directory is not on this machine at all.</p>`)
	h.filter("resources")
	h.p(`<table class="grid filterable wide" data-filter="resources"><thead><tr>`)
	for _, col := range []string{"Case", "CPU user", "CPU sys", "util", "RSS peak", "RSS end", "VMS peak", "threads",
		"read", "write", "minor flt", "major flt", "vol cs", "invol cs", "disk after", "disk Δ", "alloc Δ", "files", "reads"} {
		h.p(`<th class="n">%s</th>`, e(col))
	}
	h.p(`</tr></thead><tbody>`)
	for _, c := range ran {
		pr, d := c.Process, c.Disk
		cells := []string{
			dashDur(pr.CPUOK, pr.CPUUser), dashDur(pr.CPUOK, pr.CPUSys), dashPct(c),
			dashBytes(pr.MemoryOK, pr.RSSPeak), dashBytes(pr.MemoryOK, pr.RSSEnd),
			dashBytes(pr.MemoryOK, pr.VMSPeak), dashInt(pr.MemoryOK, int64(pr.NumThread)),
			dashBytes(pr.IOOK, pr.ReadBytes), dashBytes(pr.IOOK, pr.WriteBytes),
			dashInt(pr.FaultsOK, pr.MinorFaults), dashInt(pr.FaultsOK, pr.MajorFaults),
			dashInt(pr.CtxOK, pr.VoluntaryCS), dashInt(pr.CtxOK, pr.InvoluntaryCS),
			dashBytes(d.OK, d.BytesAfter), dashSigned(d.OK, d.Growth()),
			dashSigned(d.OK, d.AllocGrowth()), dashInt(d.OK, int64(d.Files)),
			strconv.Itoa(pr.Samples),
		}
		h.p(`<tr><td class="case">%s <code>%s</code></td>%s</tr>`, dot(c.Outcome), e(c.ID), numCells(cells))
	}
	h.p(`</tbody></table></section>`)
}

func (h *htmlWriter) loads(rep *runner.Report) {
	var loads []*runner.CaseResult
	for i := range rep.Cases {
		if rep.Cases[i].Load != nil {
			loads = append(loads, &rep.Cases[i])
		}
	}
	if len(loads) == 0 {
		return
	}
	h.p(`<section id="ingest"><h2>Ingest</h2>`)
	h.p(`<p>One row per fixture load. Cases that reused a graph another case had already loaded contribute nothing here, which is why these times must not be summed into a per-case cost.</p>`)
	h.p(`<p><b>Wall</b> is everything the harness waited for; <b>engine</b> is the part of it the engine itself spent, where the adapter can separate the two, and is what the rates are computed against. The gap between them is this harness's cost of getting the fixture in &mdash; a staging file, an encoded batch, a process start &mdash; and belongs to the route rather than to the store.</p>`)
	h.p(`<p>%s</p>`, e(floorSentence(rep)))
	if s := schemaSentence(loadsOf(rep.Cases)); s != "" {
		h.p(`<p>%s</p>`, e(s))
	}
	h.p(`<table class="grid wide"><thead><tr>`)
	for _, col := range []string{"Fixture", "Triggered by", "Nodes", "Edges", "Wall", "Engine", "nodes/s", "edges/s",
		"Apparent Δ", "Allocated Δ", "× floor", "graph", "bits/edge", "bytes/node", "RSS peak", "CPU"} {
		h.p(`<th class="n">%s</th>`, e(col))
	}
	h.p(`</tr></thead><tbody>`)
	for _, c := range loads {
		l := c.Load
		cpu := "—"
		if l.Process.CPUOK {
			cpu = metrics.Format(l.Process.CPUUser + l.Process.CPUSys)
		}
		engine := "\u2014"
		if l.EngineWall > 0 {
			engine = metrics.Format(l.EngineWall)
		}
		cells := []string{
			strconv.Itoa(l.Nodes), strconv.Itoa(l.Edges), metrics.Format(l.Wall), engine,
			num(l.NodesPerSec), num(l.EdgesPerSec),
			dashSigned(l.Disk.OK, l.Disk.Growth()), dashSigned(l.Disk.OK, l.Disk.AllocGrowth()),
			floorCell(l), dashBytes(l.SchemaBytes > 0, l.GraphBytes),
			dashFloat(l.DensityOK, l.BitsPerEdge), dashFloat(l.DensityOK, l.BytesPerNode),
			dashBytes(l.Process.MemoryOK, l.Process.RSSPeak), cpu,
		}
		h.p(`<tr><td>%s</td><td><code>%s</code></td>%s</tr>`,
			e(c.Fixture), e(c.ID), numCells(cells))
	}
	h.p(`</tbody></table>`)
	if notes := densityNotes(loadsOf(rep.Cases)); len(notes) > 0 {
		h.p(`<p>Where the density columns hold a dash, the run declined to divide:</p><ul>`)
		for _, n := range notes {
			h.p(`<li>%s</li>`, e(n))
		}
		h.p(`</ul>`)
	}
	h.p(`</section>`)
}

// exploration renders the walk of the published grammar.
//
// The one thing this section must not look like is the failures section. Both
// are lists of statements an engine refused, and only one of them is a finding.
// So the leads carry no status class, no colour, and no verdict word: they are
// rendered as what they are, a question with the smallest statement that raises
// it and the productions it came from.
func (h *htmlWriter) exploration(rep *runner.Report) {
	x := rep.Exploration
	if x == nil || x.Totals.Cases == 0 {
		return
	}
	h.p(`<section id="exploration"><h2>%s</h2>`, e(ExplorationHeading))
	h.p(`<p>These statements were written by a walk of ISO's published BNF, not by a person. They cite no clause and carry no expectation, so nothing here is a conformance result and nothing here is in the scoreboard. What the section is for is the opposite direction: the corpus is hand written, 814 productions cannot be covered by hand, and a walk reaches constructs nobody would think to write.</p>`)

	h.p(`<table class="kv">`)
	h.kv("Seed", fmt.Sprintf(`<code>%d</code>`, x.Seed))
	h.kv("Start production", fmt.Sprintf(`<code>&lt;%s&gt;</code>`, e(x.Start)))
	h.kv("Statements walked", fmt.Sprintf("%d, of which %d were different", x.Walked, x.Distinct))
	h.kv("Productions reachable", fmt.Sprintf("%d of %d, with %d replaced by a token the harness supplies",
		x.Coverage.Reachable, x.Coverage.Total, x.Coverage.Cut))
	if n := len(x.Coverage.Unwritable); n > 0 {
		h.kv("Reachable but unwritable", fmt.Sprintf("%d, because every path through them ends in a production ISO defines in prose", n))
	}
	if x.Known > 0 {
		h.kv("Already reviewed", strconv.Itoa(x.Known))
	}
	h.kv("Leads", strconv.Itoa(len(x.Leads)))
	h.p(`</table>`)
	h.p(`<p>The same seed and the same grammar give the same statements in the same order on every machine, so a lead below can be reproduced exactly.</p>`)

	if len(x.Leads) == 0 {
		h.p(`<p>The engine accepted, or refused on grounds other than syntax, every statement the walk put to it. That is not a claim that its parser matches the standard: %d statements is a sample of a language, the walk stops at tokens this harness chooses rather than at characters, and the grammar admits far more than any walk of this size reaches.</p>`, x.Distinct)
		h.p(`</section>`)
		return
	}

	h.p(`<h3>Leads</h3>`)
	h.p(`<p>Each of these is a statement the published grammar admits and the engine rejected with GQLSTATUS %s, invalid syntax. A lead is the beginning of work and not the end of it: the walk knows only that the statement is well formed, the harness supplies its own tokens for the productions ISO writes in prose, and &sect;24.5.3 lets an implementation document a restriction. What makes a lead worth a person's time is the reduced form, which is the smallest statement the engine still called a syntax error.</p>`,
		e(runner.StatusSyntaxError))
	for i := range x.Leads {
		l := x.Leads[i]
		h.p(`<article class="lead"><h3><code>%s</code></h3>`, e(l.ID))
		h.p(`<pre class="gql">%s</pre>`, e(l.Reduced))
		if l.Reduced != l.Statement {
			h.p(`<p>Reduced from %d candidate%s put to the engine. The statement the walk originally wrote was:</p>`, l.Tried, plural(l.Tried))
			h.p(`<pre class="gql orig">%s</pre>`, e(l.Statement))
		}
		h.p(`<table class="kv">`)
		h.kv("GQLSTATUS", fmt.Sprintf(`<code>%s</code>`, e(l.GQLStatus)))
		if l.Message != "" {
			h.kv("Engine's words", e(oneLine(l.Message)))
		}
		h.kv("Fingerprint", fmt.Sprintf(`<code>%s</code>`, e(l.Fingerprint)))
		h.p(`</table>`)
		if len(l.Path) > 0 {
			parts := make([]string, len(l.Path))
			for j, name := range l.Path {
				parts[j] = fmt.Sprintf(`<code>&lt;%s&gt;</code>`, e(name))
			}
			h.p(`<p class="path">Productions on the way down, outermost first: %s</p>`,
				strings.Join(parts, " &rarr; "))
		}
		h.p(`</article>`)
	}
	h.p(`<p>To settle a lead, add its fingerprint to the promotion list with either the id of the hand-written case it became or a note saying why it is not a defect. The walk is seeded and would report it again on every run otherwise.</p>`)
	h.p(`</section>`)
}

// implementation renders what the run observed of the behaviour ISO delegates.
//
// The words are the impdef package's, not this file's, so the HTML and the
// Markdown say the same thing about the same run. What the browser adds is only
// what a browser can: the section is marked up as its own tables rather than as
// a block of pre-rendered markdown, so the filter box and the stylesheet reach
// it. Nothing here has a status class, because nothing here has a status.
func (h *htmlWriter) implementation(rep *runner.Report) {
	r := rep.Implementation
	if r.Len() == 0 {
		return
	}
	h.p(`<section id="implementation"><h2>%s</h2>`, e(impdef.Heading))
	h.p(`<p>%s</p>`, e(impdef.Preamble))
	for _, k := range impdef.AllKinds {
		obs := r.Of(k)
		if len(obs) == 0 {
			continue
		}
		h.p(`<h3>%s</h3>`, e(impdef.KindHeading(k)))
		h.p(`<p>%s</p>`, e(impdef.KindPreamble(k, r)))
		// The padding probe asks about two consecutive spaces, which ordinary
		// HTML whitespace handling would collapse into one, printing a
		// different question from the one that was asked.
		h.p(`<table class="grid wide impdef"><thead><tr>`)
		cols := []string{"Item", "What ISO leaves open", "What was asked", "Observed", "The statement"}
		if k == impdef.Extension {
			cols = []string{"Extension", "Observed", "The statement"}
		}
		for _, col := range cols {
			h.p(`<th>%s</th>`, e(col))
		}
		h.p(`</tr></thead><tbody>`)
		for _, o := range obs {
			stmt := fmt.Sprintf(`<td><code>%s</code></td>`, e(impdef.Escape(o.Statement)))
			if k == impdef.Extension {
				h.p(`<tr><td>%s</td>%s%s</tr>`, e(o.Question), h.observed(o), stmt)
				continue
			}
			h.p(`<tr><td><code>%s</code></td><td>%s</td><td>%s</td>%s%s</tr>`,
				e(o.Item), e(o.Description), e(o.Question), h.observed(o), stmt)
		}
		h.p(`</tbody></table>`)
		var notes []string
		for _, o := range obs {
			if o.Note != "" {
				notes = append(notes, fmt.Sprintf(`<li><code>%s</code> %s</li>`, e(o.Item), e(o.Note)))
			}
		}
		if len(notes) > 0 {
			h.p(`<ul class="notes">%s</ul>`, strings.Join(notes, ""))
		}
	}
	h.p(`</section>`)
}

// observed is the answer cell. An unobserved one gets the na class, which is
// the same grey the report gives an unavailable measurement, and for the same
// reason: it is the absence of a number, not a bad number.
func (h *htmlWriter) observed(o impdef.Observation) string {
	if !o.Observed() {
		return fmt.Sprintf(`<td class="na">%s</td>`, e(o.Cell()))
	}
	return fmt.Sprintf(`<td><code>%s</code></td>`, e(o.Cell()))
}

func numCells(cells []string) string {
	var b strings.Builder
	for _, c := range cells {
		cls := "n"
		if c == "—" {
			cls = "n na"
		}
		fmt.Fprintf(&b, `<td class="%s">%s</td>`, cls, e(c))
	}
	return b.String()
}

// dot is the small coloured marker beside a case id, so a metric table still
// says which rows failed without repeating the whole scoreboard.
func dot(o runner.Outcome) string {
	cls := map[runner.Outcome]string{
		runner.Pass:  "s-pass",
		runner.Fail:  "s-fail",
		runner.Skip:  "s-skip",
		runner.Error: "s-err",
	}[o]
	return `<span class="dot ` + cls + `" title="` + e(string(o)) + `"></span>`
}

func (h *htmlWriter) methodology(rep *runner.Report) {
	h.p(`<section id="methodology"><h2>How to read this</h2><ul class="prose">`)
	h.p(`<li><strong>Conformance is not a percentage.</strong> ISO/IEC 39075 asks for a claim, not a score: §24.2 fixes what minimum conformance is, §24.3 makes each optional feature its own claim by code, and §24.5.2 says what an implementation must state to claim conformance at all. The scoreboard above is evidence for such a statement, not the statement itself.</li>`)
	h.p(`<li><strong>Skips are the engine's own declaration.</strong> They come from the capability table, which the adapter fills in before the run. An engine cannot be made to look better by skipping more, because skips are never in the pass-rate denominator.</li>`)
	h.p(`<li><strong>A pass on error text is weaker than a pass on a code.</strong> Where an engine reports no GQLSTATUS, a condition case can only confirm that something was refused. Those passes are counted separately in the headline.</li>`)
	h.p(`<li><strong>Apparent and allocated disk sizes both appear.</strong> A sparse or compressed file makes them differ, and quoting only one of them flatters by accident.</li>`)
	h.p(`<li><strong>Absolute latencies are about this machine.</strong> %s</li>`, e(hostCaveat(rep)))
	h.p(`</ul>`)
	h.p(`<p class="footer">Generated by gql-compat, report schema %d, %s. The JSON alongside this page is the complete record; everything here is a view of it.</p>`,
		rep.Schema, rep.Generated.UTC().Format("2006-01-02 15:04:05 UTC"))
	h.p(`</section>`)
}

func (h *htmlWriter) filter(id string) {
	h.p(`<p class="filterbox"><label>Filter <input type="search" data-filters="%s" placeholder="substring, e.g. GV01 or match"></label> <span class="count" data-count="%s"></span></p>`, id, id)
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// inlineMarkdown renders the small amount of markup the shared headline text
// carries. It escapes first, so the only tags that reach the page are the ones
// this function adds.
func inlineMarkdown(s string) string {
	out := e(s)
	for strings.Count(out, "**") >= 2 {
		out = strings.Replace(out, "**", "<strong>", 1)
		out = strings.Replace(out, "**", "</strong>", 1)
	}
	return out
}

const stylesheet = `
:root {
  --fg: #16181d; --dim: #5c6370; --line: #dfe3e8; --bg: #fff; --panel: #f7f8fa;
  --good: #1a7f37; --bad: #b42318; --warn: #b54708; --skip: #6b7280;
}
@media (prefers-color-scheme: dark) {
  :root { --fg: #e6e8eb; --dim: #9aa2ad; --line: #2b2f36; --bg: #0f1115; --panel: #161920;
          --good: #3fb950; --bad: #f85149; --warn: #d29922; --skip: #8b949e; }
}
* { box-sizing: border-box }
body { margin: 0; background: var(--bg); color: var(--fg);
  font: 15px/1.55 ui-sans-serif, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  display: grid; grid-template-columns: 17rem 1fr; align-items: start }
code, pre, .n, td.n { font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace }
nav { position: sticky; top: 0; max-height: 100vh; overflow: auto; padding: 1.5rem 1rem;
  border-right: 1px solid var(--line); background: var(--panel) }
nav h2 { font-size: .75rem; text-transform: uppercase; letter-spacing: .08em; color: var(--dim); margin: 0 0 .5rem }
nav ol { margin: 0; padding-left: 1.2rem; font-size: .9rem }
nav li { margin: .3rem 0 }
nav a { color: inherit; text-decoration: none }
nav a:hover { text-decoration: underline }
main { padding: 2rem 2.5rem 6rem; max-width: 100%; overflow-x: hidden; min-width: 0 }
h1 { font-size: 1.7rem; margin: 0 0 .4rem; font-weight: 650 }
h2 { font-size: 1.25rem; margin: 2.5rem 0 .6rem; padding-bottom: .3rem; border-bottom: 1px solid var(--line) }
h3 { font-size: 1rem; margin: 1.6rem 0 .5rem }
p { margin: .5rem 0 1rem; max-width: 62rem }
.lede { font-size: 1.02rem }
.note, .footer { color: var(--dim); font-size: .9rem }
.warn { border-left: 3px solid var(--warn); background: var(--panel); padding: .7rem .9rem; margin: 1rem 0 }
.cards { display: flex; flex-wrap: wrap; gap: .75rem; margin: 1.2rem 0 0 }
.card { border: 1px solid var(--line); border-radius: .5rem; padding: .7rem .9rem; min-width: 8.5rem; background: var(--panel) }
.card .v { font-size: 1.5rem; font-weight: 650; line-height: 1.1 }
.card .k { font-size: .8rem; text-transform: uppercase; letter-spacing: .05em; color: var(--dim); margin-top: .2rem }
.card .n { font-size: .78rem; color: var(--dim) }
.card.good .v { color: var(--good) } .card.bad .v { color: var(--bad) }
.card.warnish .v { color: var(--warn) } .card.neutral .v { color: var(--fg) }
table { border-collapse: collapse; margin: .4rem 0 1.2rem; font-size: .88rem }
table.kv { min-width: 32rem }
table.kv th { text-align: left; font-weight: 500; color: var(--dim); width: 14rem; vertical-align: top }
table.kv th, table.kv td { padding: .28rem .6rem .28rem 0; border-bottom: 1px solid var(--line) }
table.grid { width: 100%; }
table.grid.wide { display: block; overflow-x: auto; white-space: nowrap }
table.grid th, table.grid td { padding: .3rem .55rem; border-bottom: 1px solid var(--line); text-align: left }
table.grid thead th { font-size: .74rem; text-transform: uppercase; letter-spacing: .04em;
  color: var(--dim); font-weight: 600; position: sticky; top: 0; background: var(--bg) }
table.grid th.n, table.grid td.n { text-align: right }
table.grid tbody tr:hover { background: var(--panel) }
tr.total td { font-weight: 650; border-top: 2px solid var(--line) }
td.na { color: var(--dim) }
td.c { text-align: center; width: 6rem }
td.c.yes { color: var(--good) } td.c.no { color: var(--dim) }
.good { color: var(--good) } .bad { color: var(--bad) } .warnish { color: var(--warn) } .neutral { color: var(--dim) }
.barcell { width: 12rem }
.bar { display: flex; height: .6rem; width: 11rem; border-radius: .3rem; overflow: hidden; background: var(--line) }
.seg { display: block; height: 100% }
.s-pass { background: var(--good) } .s-fail { background: var(--bad) }
.s-skip { background: var(--skip) } .s-err { background: var(--warn) } .s-part { background: var(--warn) }
.dot { display: inline-block; width: .55rem; height: .55rem; border-radius: 50%; vertical-align: middle; margin-right: .35rem }
td.case { white-space: nowrap }
.tag { display: inline-block; padding: .05rem .45rem; border: 1px solid currentColor; border-radius: .3rem;
  font-size: .72rem; text-transform: uppercase; letter-spacing: .05em; vertical-align: middle }
.failure { border: 1px solid var(--line); border-radius: .5rem; padding: .2rem 1rem 1rem; margin: 1rem 0; background: var(--panel) }
.failure h3 { margin-top: 1rem }
/* A lead is not a failure and must not be dressed as one: no panel fill, no
   status colour, a dashed rule instead of a border. */
.lead { border-left: 2px dashed var(--line); padding: .1rem 0 .1rem 1rem; margin: 1.4rem 0 }
.lead h3 { margin-top: .6rem }
pre.gql.orig { color: var(--dim) }
tr.aside td { color: var(--dim); border-top: 1px dashed var(--line) }
p.path { font-size: .85rem; color: var(--dim); line-height: 1.9 }
pre.gql { background: var(--bg); border: 1px solid var(--line); border-radius: .4rem;
  padding: .7rem .9rem; overflow-x: auto; font-size: .85rem; white-space: pre-wrap }
ul.ids { margin: .4rem 0; padding-left: 1.1rem; columns: 3; font-size: .82rem }
ul.prose li { margin: .4rem 0; max-width: 62rem }
table.impdef code { white-space: pre-wrap }
ul.notes { margin: .6rem 0 1.4rem; padding-left: 1.1rem; font-size: .86rem; color: var(--dim) }
ul.notes li { margin: .3rem 0; max-width: 62rem }
details summary { cursor: pointer; color: var(--dim) }
.filterbox { margin: .3rem 0 .2rem; font-size: .85rem; color: var(--dim) }
.filterbox input { font: inherit; padding: .25rem .5rem; border: 1px solid var(--line);
  border-radius: .3rem; background: var(--bg); color: var(--fg); min-width: 18rem }
@media (max-width: 900px) {
  body { grid-template-columns: 1fr }
  nav { position: static; max-height: none; border-right: 0; border-bottom: 1px solid var(--line) }
  main { padding: 1.2rem }
}
@media print {
  body { grid-template-columns: 1fr } nav, .filterbox { display: none }
  table.grid.wide { display: table; white-space: normal } .failure { break-inside: avoid }
}
`

// filterScript is the only script on the page: it hides rows that do not match
// a substring, and says how many it hid. Nothing else on the page depends on
// it running.
const filterScript = `
document.querySelectorAll('input[data-filters]').forEach(function (input) {
  var id = input.getAttribute('data-filters');
  var table = document.querySelector('table[data-filter="' + id + '"]');
  var count = document.querySelector('.count[data-count="' + id + '"]');
  if (!table) return;
  var rows = Array.prototype.slice.call(table.tBodies[0].rows);
  function apply() {
    var q = input.value.trim().toLowerCase();
    var shown = 0;
    rows.forEach(function (row) {
      var hit = !q || row.textContent.toLowerCase().indexOf(q) !== -1;
      row.style.display = hit ? '' : 'none';
      if (hit) shown++;
    });
    if (count) count.textContent = q ? shown + ' of ' + rows.length + ' rows' : rows.length + ' rows';
  }
  input.addEventListener('input', apply);
  apply();
});
`
