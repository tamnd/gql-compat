package runner

import (
	"runtime"
	"sort"
	"time"

	"github.com/tamnd/gql-compat/adapter"
	"github.com/tamnd/gql-compat/corpus"
	"github.com/tamnd/gql-compat/fixture"
	"github.com/tamnd/gql-compat/impdef"
	"github.com/tamnd/gql-compat/iso"
	"github.com/tamnd/gql-compat/metrics"
	"github.com/tamnd/gql-compat/rows"
)

// Mode is which text a run puts to the engine.
type Mode string

const (
	// ModeConformance runs the standard's own spelling, unaltered, on every
	// engine. It is the only mode whose results may be called conformance.
	ModeConformance Mode = "conformance"
	// ModeCompat runs the engine's documented spelling of the same meaning
	// from the case's Dialects map. It answers a different question — can
	// this engine express this at all — and is scored separately so the two
	// answers can never be added together.
	ModeCompat Mode = "compat"
)

// Outcome is a case's verdict.
type Outcome string

const (
	// Pass means the engine did what the standard says.
	Pass Outcome = "pass"
	// Fail means it did something else. A wrong answer and a rejection of
	// valid syntax are both failures, distinguished by Evidence and Reason.
	Fail Outcome = "fail"
	// Skip means the case was never put to the engine, because the engine
	// declared it could not hold the fixture or could not accept the case's
	// shape. A skip is a measurement of the engine, not a gap in the run, and
	// the report counts skips separately rather than dropping them.
	Skip Outcome = "skip"
	// Error means the harness could not obtain a verdict: a dead session, a
	// load that failed, a timeout. It is never counted as a pass or a fail.
	Error Outcome = "error"
)

// Evidence records how strong a verdict is.
//
// An engine that rejects bad syntax with the GQLSTATUS the standard names has
// demonstrated more than one that merely rejects it, and a report that scored
// them identically would be flattering the second. Every passing condition
// case carries the evidence that produced it.
type Evidence string

const (
	// EvidenceNone is a verdict that needed no evidence beyond completion.
	EvidenceNone Evidence = ""
	// EvidenceRows is a full comparison of the returned table.
	EvidenceRows Evidence = "rows"
	// EvidenceStatus is a match on the five-character GQLSTATUS code.
	EvidenceStatus Evidence = "gqlstatus"
	// EvidenceMessage is a match on the error text only, which is what an
	// engine reporting no GQLSTATUS can offer. It is weaker and is labelled.
	EvidenceMessage Evidence = "message"
	// EvidenceAccepted is a statement the engine took, for a grammar case
	// that asks nothing more.
	EvidenceAccepted Evidence = "accepted"
	// EvidenceRejected is a statement the engine refused, for a grammar case
	// that requires refusal.
	EvidenceRejected Evidence = "rejected"
)

// SkipReason is why a case never ran, in a form a report can aggregate.
type SkipReason string

const (
	// SkipCapability is a fixture the engine's data model cannot hold.
	SkipCapability SkipReason = "fixture-capability"
	// SkipParameters is a case with parameters an engine cannot bind.
	SkipParameters SkipReason = "parameters"
	// SkipSetup is a case whose setup statements need multi-statement support.
	SkipSetup SkipReason = "setup-statements"
	// SkipNoDialect is a compatibility-mode case for which this engine has no
	// documented spelling. It is not a failure: nobody claimed one exists.
	SkipNoDialect SkipReason = "no-dialect"
	// SkipTransactions is a case tagged `transaction` run against an engine
	// whose adapter declares no explicit transaction control. Feature GT01 is
	// optional, so not having it is a measurement and not a fault.
	SkipTransactions SkipReason = "transactions"
	// SkipRequires is a case whose `requires` names an optional feature the
	// adapter declares unsupported. The case needed that syntax to reach
	// something else, so running it would measure the missing feature again
	// under another case's name.
	SkipRequires SkipReason = "required-feature"
	// SkipNoGQLStatus is a condition case put to an engine that reports no
	// GQLSTATUS. Feature GB01 is optional, so not reporting codes is lawful;
	// what is not lawful is calling the resulting refusal evidence. Any engine
	// can decline a statement, and an engine that never implemented the
	// function under test declines it for a reason the case is not about.
	SkipNoGQLStatus SkipReason = "no-gqlstatus"
	// SkipSelected is a case excluded by the run's selector, recorded only
	// when the caller asked for the full list.
	SkipSelected SkipReason = "not-selected"
)

// CaseResult is everything one case produced.
type CaseResult struct {
	ID   string      `json:"id"`
	Name string      `json:"name"`
	Kind corpus.Kind `json:"kind"`

	Features    []string `json:"features,omitempty"`
	Subclauses  []string `json:"subclauses,omitempty"`
	Productions []string `json:"productions,omitempty"`
	Conditions  []string `json:"conditions,omitempty"`
	Tags        []string `json:"tags,omitempty"`

	Fixture string `json:"fixture,omitempty"`
	Mode    Mode   `json:"mode"`
	// Statement is the text that actually ran, which in compatibility mode is
	// not the text the case was written with. Recording it is what lets a
	// reader check that no rewriting happened.
	Statement string `json:"statement"`

	Outcome  Outcome  `json:"outcome"`
	Evidence Evidence `json:"evidence,omitempty"`
	// Reason is prose for a human: why this failed, or why it was skipped.
	Reason string `json:"reason,omitempty"`
	// Skip classifies a skip so the report can total them by cause.
	Skip SkipReason `json:"skip_reason,omitempty"`
	// Missing names the capabilities a skipped fixture needed.
	Missing []fixture.Capability `json:"missing_capabilities,omitempty"`

	// Diff is the first difference between expected and actual rows.
	Diff *rows.Diff `json:"diff,omitempty"`
	// WantStatus and GotStatus are the GQLSTATUS codes at issue.
	WantStatus string `json:"want_gqlstatus,omitempty"`
	GotStatus  string `json:"got_gqlstatus,omitempty"`
	// Message is the engine's own error text, verbatim.
	Message string `json:"message,omitempty"`

	// Stats is the latency distribution over the measured repetitions.
	Stats metrics.Stats `json:"stats"`
	// Process is what the engine's process did while they ran.
	Process metrics.ProcessDelta `json:"process"`
	// Disk is how the engine's storage changed across the case.
	Disk metrics.DiskDelta `json:"disk"`
	// Load, when this case caused a fixture load, is what the ingest cost.
	// Cases that reused an already-loaded fixture leave it nil, which is why
	// a report must not sum load times across cases.
	Load *metrics.Load `json:"load,omitempty"`

	// Repeats and Warmups say how the distribution was obtained, because a
	// p99 over three samples is not a p99.
	Repeats int `json:"repeats"`
	Warmups int `json:"warmups"`

	Started time.Time     `json:"started"`
	Wall    time.Duration `json:"wall_ns"`
}

// Passed reports whether the case is a verdict in the engine's favour.
func (r *CaseResult) Passed() bool { return r.Outcome == Pass }

// Totals is the scoreboard.
type Totals struct {
	Cases int `json:"cases"`
	Pass  int `json:"pass"`
	Fail  int `json:"fail"`
	Skip  int `json:"skip"`
	Error int `json:"error"`

	// ByKind splits the same counts by what part of the standard was tested,
	// because a mandatory failure and an optional failure mean different
	// things and averaging them means nothing.
	ByKind map[corpus.Kind]KindTotals `json:"by_kind"`
	// BySkip counts skips by cause.
	BySkip map[SkipReason]int `json:"by_skip_reason,omitempty"`
	// WeakEvidence counts passes that rest on message matching rather than a
	// GQLSTATUS. It is printed beside the score, never folded into it.
	WeakEvidence int `json:"weak_evidence_passes"`
}

// KindTotals is one kind's row of the scoreboard.
type KindTotals struct {
	Cases int `json:"cases"`
	Pass  int `json:"pass"`
	Fail  int `json:"fail"`
	Skip  int `json:"skip"`
	Error int `json:"error"`
}

// Rate is passes over cases that produced a verdict. Skips and harness errors
// are excluded from the denominator and reported alongside, so the number
// cannot be improved by skipping more.
func (k KindTotals) Rate() float64 {
	judged := k.Pass + k.Fail
	if judged == 0 {
		return 0
	}
	return float64(k.Pass) / float64(judged)
}

// Coverage is what the run touched of the standard, and how much of that the
// engine got right.
//
// The denominators come from ISO's own artifacts, never from the corpus: a
// suite that tests twelve features out of 228 should read as twelve out of
// 228, not as twelve out of twelve.
type Coverage struct {
	// Features is per-optional-feature, keyed by ISO code.
	Features map[string]Status `json:"features"`
	// Families aggregates features by their code family.
	Families []FamilyCoverage `json:"families"`
	// Subclauses is per-mandatory-subclause, keyed by clause number.
	Subclauses map[string]Status `json:"subclauses"`
	// Conditions is per-GQLSTATUS code.
	Conditions map[string]Status `json:"conditions"`
	// Productions is per-BNF production name.
	Productions map[string]Status `json:"productions"`

	// FeaturesTotal, ConditionsTotal, ProductionsTotal, and SubclausesTotal
	// are the ISO denominators: 228 optional features, the codes
	// conditions.xml defines, the productions in the grammar artifact, and the
	// clauses of the standard that specify behaviour to conform to.
	FeaturesTotal    int `json:"features_total"`
	ConditionsTotal  int `json:"conditions_total"`
	ProductionsTotal int `json:"productions_total"`
	SubclausesTotal  int `json:"subclauses_total"`
}

// Status is one standard item's result across every case claiming it.
type Status struct {
	// Cases is how many cases claim this item.
	Cases int `json:"cases"`
	Pass  int `json:"pass"`
	Fail  int `json:"fail"`
	Skip  int `json:"skip"`
	Error int `json:"error"`
	// Description is the standard's own words for the item, where the
	// catalogue has them.
	Description string `json:"description,omitempty"`
}

// Supported means every case claiming the item passed and at least one ran.
// Anything less is not support, including a single skip: an item nobody could
// test is an item nobody may claim.
func (s Status) Supported() bool { return s.Cases > 0 && s.Pass == s.Cases }

// FamilyCoverage aggregates one optional-feature family.
type FamilyCoverage struct {
	// Family is the letter prefix: G, GA, GV, and so on.
	Family string `json:"family"`
	// Total is how many features ISO defines in the family.
	Total int `json:"total"`
	// Tested is how many the corpus claims.
	Tested int `json:"tested"`
	// Supported is how many of the tested ones passed everywhere.
	Supported int `json:"supported"`
}

// EngineInfo pins the results to something.
type EngineInfo struct {
	Adapter      string               `json:"adapter"`
	Version      string               `json:"version"`
	Capabilities adapter.Capabilities `json:"capabilities"`
	// DataCapabilities is the supported set in report-column order, so a
	// consumer does not have to know AllCapabilities to render it.
	DataCapabilities []fixture.Capability `json:"data_capabilities"`
}

// HostInfo is the machine, because a latency table without one is a rumour.
type HostInfo struct {
	OS            string  `json:"os"`
	Arch          string  `json:"arch"`
	Kernel        string  `json:"kernel,omitempty"`
	Platform      string  `json:"platform,omitempty"`
	CPUModel      string  `json:"cpu_model,omitempty"`
	CPUCores      int     `json:"cpu_cores"`
	CPULogical    int     `json:"cpu_logical"`
	CPUMHz        float64 `json:"cpu_mhz,omitempty"`
	MemoryTotal   int64   `json:"memory_total_bytes,omitempty"`
	GoVersion     string  `json:"go_version"`
	GOMAXPROCS    int     `json:"gomaxprocs"`
	Hostname      string  `json:"hostname,omitempty"`
	Containerised bool    `json:"containerised,omitempty"`
}

// RunInfo is how the run was configured, which is half of what makes a number
// reproducible.
type RunInfo struct {
	Mode           Mode          `json:"mode"`
	Repeats        int           `json:"repeats"`
	Warmups        int           `json:"warmups"`
	Timeout        time.Duration `json:"timeout_ns"`
	LoadTimeout    time.Duration `json:"load_timeout_ns"`
	SampleInterval time.Duration `json:"sample_interval_ns"`
	Selector       string        `json:"selector,omitempty"`
	WorkDir        string        `json:"workdir,omitempty"`
	Started        time.Time     `json:"started"`
	Finished       time.Time     `json:"finished"`
	Wall           time.Duration `json:"wall_ns"`
	// ISOSource is where the conformance vocabulary came from, so a reader can
	// check the claims against the same artifacts.
	ISOSource string `json:"iso_source"`
}

// Report is one engine's whole run.
type Report struct {
	Tool      string     `json:"tool"`
	Schema    int        `json:"schema"`
	Generated time.Time  `json:"generated"`
	Engine    EngineInfo `json:"engine"`
	Host      HostInfo   `json:"host"`
	Run       RunInfo    `json:"run"`

	Cases    []CaseResult `json:"cases"`
	Totals   Totals       `json:"totals"`
	Coverage Coverage     `json:"coverage"`

	// Implementation is what the run observed of the choices ISO leaves open.
	// It is deliberately outside Totals and Coverage: an implementation-defined
	// choice has no right answer, so counting it would put a number on
	// something the standard declined to decide. Nil when no probe ran.
	Implementation *impdef.Result `json:"implementation,omitempty"`
}

// ReportSchema is the version of the JSON shape above. It changes whenever a
// consumer would need to change with it.
const ReportSchema = 1

// summarize computes totals and coverage from the case results.
func summarize(cat *iso.Catalog, results []CaseResult) (Totals, Coverage) {
	t := Totals{
		Cases:  len(results),
		ByKind: map[corpus.Kind]KindTotals{},
		BySkip: map[SkipReason]int{},
	}
	cov := Coverage{
		Features:         map[string]Status{},
		Subclauses:       map[string]Status{},
		Conditions:       map[string]Status{},
		Productions:      map[string]Status{},
		FeaturesTotal:    len(cat.Features),
		ProductionsTotal: len(cat.Productions),
		SubclausesTotal:  len(cat.NormativeSubclauses()),
	}
	for _, c := range cat.Classes {
		cov.ConditionsTotal += len(c.Subclasses)
	}

	for i := range results {
		r := &results[i]
		k := t.ByKind[r.Kind]
		k.Cases++
		switch r.Outcome {
		case Pass:
			t.Pass++
			k.Pass++
			if r.Evidence == EvidenceMessage {
				t.WeakEvidence++
			}
		case Fail:
			t.Fail++
			k.Fail++
		case Skip:
			t.Skip++
			k.Skip++
			t.BySkip[r.Skip]++
		case Error:
			t.Error++
			k.Error++
		}
		t.ByKind[r.Kind] = k

		record(cov.Features, r.Features, r.Outcome)
		record(cov.Subclauses, r.Subclauses, r.Outcome)
		record(cov.Conditions, r.Conditions, r.Outcome)
		record(cov.Productions, r.Productions, r.Outcome)
	}

	for code, st := range cov.Features {
		if f, ok := cat.Feature(code); ok {
			st.Description = f.Description
			cov.Features[code] = st
		}
	}
	for code, st := range cov.Conditions {
		if name, ok := cat.Status(code); ok {
			st.Description = name
			cov.Conditions[code] = st
		}
	}
	for number, st := range cov.Subclauses {
		if s, ok := cat.Subclause(number); ok {
			st.Description = s.Title
			cov.Subclauses[number] = st
		}
	}
	cov.Families = families(cat, cov.Features)
	return t, cov
}

func record(into map[string]Status, keys []string, outcome Outcome) {
	for _, key := range keys {
		st := into[key]
		st.Cases++
		switch outcome {
		case Pass:
			st.Pass++
		case Fail:
			st.Fail++
		case Skip:
			st.Skip++
		case Error:
			st.Error++
		}
		into[key] = st
	}
}

func families(cat *iso.Catalog, tested map[string]Status) []FamilyCoverage {
	totals := map[string]int{}
	for _, f := range cat.Features {
		totals[f.Family]++
	}
	agg := map[string]*FamilyCoverage{}
	for fam, n := range totals {
		agg[fam] = &FamilyCoverage{Family: fam, Total: n}
	}
	for code, st := range tested {
		f, ok := cat.Feature(code)
		if !ok {
			continue
		}
		fc := agg[f.Family]
		if fc == nil {
			fc = &FamilyCoverage{Family: f.Family}
			agg[f.Family] = fc
		}
		fc.Tested++
		if st.Supported() {
			fc.Supported++
		}
	}
	out := make([]FamilyCoverage, 0, len(agg))
	for _, fc := range agg {
		out = append(out, *fc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Family < out[j].Family })
	return out
}

func goHost() HostInfo {
	return HostInfo{
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		GoVersion:  runtime.Version(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		CPULogical: runtime.NumCPU(),
	}
}
