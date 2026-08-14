# gql-compat

Measures how closely a graph database implements **ISO/IEC 39075:2024 (GQL)**,
and what it costs the engine to do so.

```
$ gql-compat run -adapter zu -binary ./zu -out ./reports
```

263 cases citing ISO's own artifacts, run against three engines, scored by kind
and reported five ways with a full cost measurement attached to every case.

Both a **command-line tool** and a **Go library**. There is no `internal/`
directory: the CLI is written entirely against the exported API, so anything
it does, an importing program can do.

---

## There is no official GQL conformance test

This is the first thing to be clear about, because a tool in this space that is
vague about it is selling something.

ISO/IEC 39075:2024 defines conformance in Clause 6 and publishes a set of
machine-readable artifacts alongside the text — the BNF grammar, the optional
feature list, the GQLSTATUS condition codes, the subclause structure, the
implementation-defined and implementation-dependent element lists. It does
**not** publish an executable test suite, and no other body publishes one that
ISO recognises. There is no certification, and nobody can hand you a pass.

What ISO does give you is a vocabulary precise enough to be checked against:

| Artifact | Count |
|---|---:|
| Optional features, in 15 families | 228 |
| GQLSTATUS codes, in 12 classes | 68 |
| Grammar productions | 814 |
| Subclauses (317 normative) | 360 |
| Reserved and non-reserved keywords | 310 |
| Implementation-defined elements | 117 |
| Implementation-dependent elements | 20 |

All of it is vendored, unmodified, under [`iso/artifacts`](iso/artifacts), and
exposed through the `iso` package and the `gql-compat iso` subcommand.

**The placement rule.** If `features.xml` gives a construct a feature code, the
construct is optional. Everything else the standard specifies is mandatory.
Mandatory features carry no code at all — only a subclause number — which is
why a mandatory case here cites `§14.9` and an optional one cites `GQ13`. A
suite that reported one number for both would be averaging two different
questions.

Every case in the corpus cites the standard by one of those four vocabularies,
and the citations are verified at load time. A case naming a production the
grammar does not define, or a GQLSTATUS the standard does not, will not load.

```
$ gql-compat validate
263 cases loaded; every ISO reference in them resolves.

COVERAGE              CLAIMED  ISO TOTAL
optional features     117      228
GQLSTATUS codes       15       68
grammar productions   300      814
normative subclauses  94       317
```

The denominators are ISO's, never the corpus's. 117 of 228 reads as 117 of 228.
A tool that divided by its own corpus size would report full coverage for
testing twelve things.

---

## What it measures

### Conformance

Five kinds of case, scored separately and never summed:

| Kind | What it asks | Cites |
|---|---|---|
| `mandatory` | Behaviour every conforming implementation must have | subclause |
| `optional` | One of the 228 coded features | feature code |
| `condition` | The right GQLSTATUS for the wrong input | GQLSTATUS |
| `grammar` | Whether a production is accepted or refused | production |
| `performance` | Cost at scale, on generated graphs | — |

There is a sixth kind, `generated`, and it is not in that table because it is
not scored. It is what a walk of the published BNF writes, it cites no clause,
and it is in no total, no pass rate and no exit status. See
[statements the grammar admits](#statements-the-grammar-admits).

**A skip is a measurement, not a gap.** An engine that declares it cannot hold
temporal values skips the temporal cases, and the report says so by name. Skips
are excluded from the denominator, so a pass rate can never be improved by
skipping more.

**A declaration can be put to the test.** `-challenge` ignores the engine's own
declaration and runs the cases it would have excluded. Most of them fail, which
is the point of believing the declaration by default; what the run is looking
for is the claim of absence whose cases all passed, which is the one outcome an
engine that lacks the thing cannot produce. Declaring a capability an engine in
fact has costs nothing at the time, turns real passes into skips, and reads in
the report exactly like a limitation. A challenging run exits nonzero on a
contradicted claim and its totals are not a conformance score.

**Evidence is graded.** Rejecting `1/0` with GQLSTATUS `22012` is stronger than
rejecting it with prose, which is stronger than merely rejecting it. The three
are recorded as `gqlstatus`, `message`, and `rejected`, and message-only passes
are counted beside the score rather than folded into it.

**Two modes, never mixed.** `-mode conformance` runs the standard's own
spelling on every engine and is the only mode whose results may be called
conformance. `-mode compat` runs the engine's documented spelling of the same
meaning, taken from the case's `dialects` map, and answers a different question:
can this engine express the thing at all.

### Cost

Every case, not just the performance ones, carries a full measurement:

- **Latency** — min, p50, p90, p95, p99, max, mean, stddev, and MAD over N
  repetitions after M discarded warmups. MAD is there because a standard
  deviation over seven samples is destroyed by the one the scheduler ruined.
- **Throughput** — queries, rows, and cells per second, computed over observed
  serial time. Nothing is multiplied by a core count.
- **CPU** — user and system time, and the utilisation ratio against wall time,
  which is what separates *fast* from *spent eight cores being fast*.
- **Memory** — RSS start, end, and sampled peak; VMS and swap peaks; thread
  count. The peak needs polling, because an engine that allocates a gigabyte
  mid-query and frees it leaves no trace in a before-and-after reading.
- **Disk** — apparent *and* allocated bytes before and after, plus file count.
  The two differ for sparse and compressed files, and quoting only one of them
  is quoting the flattering one by accident.
- **Kernel** — bytes and ops read and written, minor and major page faults,
  voluntary and involuntary context switches. A major fault during a query the
  engine called warm is the single most useful signal that a cache is not doing
  what its documentation claims.
- **Ingest** — wall time, nodes/sec, edges/sec, and the density figures
  **bits per edge** and **bytes per node**. Ingest is measured separately from
  query because the two are optimised against each other: an engine can buy a
  fast scan with a slow, wide write, and a report that timed only queries would
  call that free.

> **Unavailable is never zero.** Page-fault counters are Linux-only, a server
> engine's data directory is not on this machine, and a sampler that got no
> reading measured nothing. Each of those renders as `—` in Markdown and HTML
> and as an empty field in CSV. Writing a `0` would put a number into a
> comparison that no measurement produced. The JSON keeps an explicit
> availability flag beside every group so a consumer cannot lose the
> distinction.

---

## Engines

| Adapter | Engine | How |
|---|---|---|
| `zu` | [tamnd/zu](https://github.com/tamnd/zu) | subprocess, embedded store |
| `neo4j` | Neo4j, a release that speaks Cypher 25 | Bolt, `neo4j-go-driver/v6` |
| `ladybug` | Ladybug / Kùzu-lineage | subprocess |
| `fake` | scriptable in-process engine | for testing the harness itself |

Adapters are registered by name and selected by string, so linking an engine's
client library is a decision made in exactly one file. A third-party adapter
lives outside this module and still works:

```go
adapter.Register("mine", func(o adapter.Options) (adapter.Driver, error) { ... })
```

An adapter declares what its data model can hold — multi-label nodes, edge
properties, undirected edges, self-loops, parallel edges, temporal values, and
so on. Fixtures **derive** their requirements from their own data rather than
declaring them, so a fixture cannot understate itself and get quietly truncated
into an engine that will then fail cases about the missing half.

---

## Install

```
go install github.com/tamnd/gql-compat/cmd/gql-compat@latest
```

Go 1.26.5 or newer, and CI builds on 1.26.6, which is where three standard library advisories are fixed. The corpus and the ISO artifacts are embedded in the binary, so a
report produced by a vendored copy is comparable to one produced by the CLI.

## Use as a CLI

```sh
# every case against a local zu build
gql-compat run -adapter zu -binary ./target/release/zu -out ./reports

# one family of optional features, 25 repetitions, against Neo4j
GQL_COMPAT_PASSWORD=... gql-compat run -adapter neo4j \
    -uri bolt://localhost:7687 -user neo4j \
    -feature GQ13 -feature GQ14 -repeats 25 -out ./reports

# what the engine can express in its own dialect, rather than in ISO's
gql-compat run -adapter neo4j -mode compat -out ./reports

# the performance corpus only, with the working directory left for inspection
gql-compat run -adapter zu -kind performance -keep-workdir -out ./reports

# the corpus, then 200 statements walked out of the published grammar
gql-compat run -adapter neo4j -uri bolt://localhost:7687 -user neo4j \
    -generate 200 -generate-seed 7 -out ./reports
```

| Subcommand | |
|---|---|
| `run` | run the corpus against an engine and write reports |
| `list` | `cases`, `fixtures`, or `adapters` in this binary |
| `iso` | print the vendored catalogue: `summary`, `features`, `families`, `conditions`, `productions`, `subclauses`, `keywords`, `impdef`, `impdep` |
| `consensus` | compare two or more reports and queue the cases every engine failed |
| `statement` | print the Clause 24.5.2 template a vendor has to fill in, with a run's answers in it |
| `validate` | load a corpus and check every ISO reference in it |
| `version` | |

Exit status: `0` clean, `1` the engine failed cases the run was set to fail on
(`-fail-on mandatory` by default, because declining an optional feature is
lawful), `2` a usage error.

### A Neo4j to measure

Two things about the server matter to the result. It has to parse Cypher 25,
because Cypher 5 predates the GQL alignment and answers a large part of the
corpus with a syntax error, and the adapter aborts rather than record a run
that measures the language version instead of the engine. And it should be a
server you are willing to throw away, because the corpus creates and drops
graphs and writes fixtures into the database it is pointed at.

```sh
# a store that exists only for this run, on a port nothing else is using
export NEO4J_HOME=/tmp/gql-compat-neo4j NEO4J_CONF=$NEO4J_HOME/conf
mkdir -p "$NEO4J_CONF" "$NEO4J_HOME"/{data,logs,run,import,plugins,licenses}
cat > "$NEO4J_CONF/neo4j.conf" <<CONF
server.directories.data=$NEO4J_HOME/data
server.directories.logs=$NEO4J_HOME/logs
server.directories.run=$NEO4J_HOME/run
server.directories.transaction.logs.root=$NEO4J_HOME/data/transactions
server.bolt.listen_address=127.0.0.1:7688
server.http.enabled=false
db.query.default_language=CYPHER_25
CONF
cp "$(dirname "$(readlink -f "$(command -v neo4j)")")"/../conf/*.xml "$NEO4J_CONF/"

neo4j-admin dbms set-initial-password "$PW"   # before the first start, or it is ignored
neo4j console &

GQL_COMPAT_PASSWORD=$PW gql-compat run -adapter neo4j \
    -uri bolt://127.0.0.1:7688 -user neo4j -fail-on none -out ./reports
```

An existing database that already has a default language can be moved with
`ALTER DATABASE neo4j SET DEFAULT LANGUAGE CYPHER 25`, and
`GQL_COMPAT_NEO4J_ANY_LANGUAGE=1` measures an older language on purpose. The
password is read from the environment and there is a `-password` flag only
because some setups have nowhere else to put it.

## Use as a library

```go
std, err := gqlcompat.Load()          // embedded corpus + ISO catalogue
if err != nil {
    return err
}

drv, err := adapter.New("zu", adapter.Options{Binary: "./zu"})
if err != nil {
    return err
}
defer drv.Close()

rep, err := std.Run(ctx, drv, runner.Config{Repeats: 25, Warmups: 3})
if err != nil {
    return err
}

return report.Write(os.Stdout, rep, report.FormatMarkdown)
```

Reach past the facade for anything else: `gqlcompat.LoadFS` for your own cases,
`iso.Load` for the catalogue alone, `metrics` for the sampler, `rows.Compare`
for the comparison semantics, `report.ReadJSON` to diff a run against a
baseline.

| Package | |
|---|---|
| `gqlcompat` | facade: `Load`, `LoadFS`, `Standard.Run` |
| `iso` | the vendored ISO catalogue and its lookups |
| `corpus` | cases, selectors, and reference checking |
| `fixture` | graphs as data, capability derivation, deterministic generators |
| `adapter` | the `Driver`/`Session` contract and the name registry |
| `runner` | execution, judgement, timing |
| `metrics` | latency statistics, process sampler, disk measurement |
| `rows` | result normalisation and comparison |
| `report` | JSON, Markdown, HTML, CSV, JUnit |
| `impdef` | the choices ISO delegates, the probes that observe them, the 24.5.2 template |
| `grammar` | the published BNF as a tree, the seeded walk of it, and the reducer |

## Reports

Five formats from one run. JSON is the archive and the only lossless one; the
other four are views of it.

| Format | For |
|---|---|
| `json` | the record, and the input to a baseline diff |
| `markdown` | reading |
| `html` | reading, with a table of contents and colour |
| `csv` | one row per case, ~70 columns, for a spreadsheet or a plot |
| `junit` | CI. Skips map to `<skipped>`, never to passes |

CSV column order is a contract: new columns go on the end, so a script that
plots column 31 of last month's run finds the same quantity there this month.

## Comparing engines, and the one thing that comparison is for

```
$ gql-compat consensus reports/neo4j/neo4j.json reports/zu/zu.json reports/ladybug/ladybug.json
```

Not a leaderboard. The command reads two or more reports and lists, per case,
which engines passed and which failed, and the only thing it computes is the
set of cases that **every engine judging them failed**. Those go on a corpus
review queue, because a case that three unrelated engines all fail is more
likely to have been written wrong than to have found the same bug three times.

Three rules keep it honest, and all three are tested:

- Nothing it produces enters any pass rate. A queued case is not excused and
  not penalised; it is listed for someone to read.
- A skip is not agreement, and neither is an error. An engine that declined a
  case did not judge it.
- Two engines agreeing is a coin flip, and the output says so in those words
  until there are three.

Decisions live in [`corpus/dispositions.yaml`](corpus/dispositions.yaml), one
per queued case, each with a verdict from a closed set and a written reason. A
verdict of `corpus-bug` means the case gets fixed, and where the loader could
have caught the mistake it becomes a load rule too. The first review, in August
2026, ran 51 cases and found one: `optional/gc04/create-graph` wrote `CREATE
PROPERTY GRAPH` where `PROPERTY` is optional in the production, so an engine
that implements `CREATE GRAPH` was being reported as not implementing the
feature at all.

The method has a limit, and it is printed with every report rather than kept in
the documentation: consensus only detects a shared misreading between engines
that are actually independent. Three engines that all grew out of Cypher will
agree about Cypher.

## Statements the grammar admits

The corpus is hand written and every case in it cites a clause, which is what
makes it reviewable, and that does not change. But 814 productions cannot be
covered by hand. `-generate N` walks the published BNF after the corpus has
run and puts what it wrote to the same session.

```
$ gql-compat run -adapter neo4j -uri bolt://localhost:7687 -user neo4j -generate 200
```

Nothing that comes out of it is a conformance result. A generated statement
cites no clause, so it is in no total, no pass rate, no coverage denominator
and no exit status, and the mechanism for that is structural rather than a
convention: the walk's results live in a different field of the report from the
scored cases, and the scoreboard is summed from the other one.

What the walk produces is a **lead**, and the bar for one is deliberately high.
The grammar describes syntax and nothing else, so a statement it admits can
still be meaningless, and an engine that refuses one of those is right. A
rejection is only a lead when the engine reports GQLSTATUS `42001`, invalid
syntax. Any other code is a semantic refusal and is recorded as a skip. An
engine that reports no GQLSTATUS at all is not judged on this at any point,
because guessing from the wording of an error message is how a harness invents
findings.

A lead is then **reduced**. Every candidate is produced by taking a decision in
the derivation differently rather than by deleting text, so every candidate is
still a statement the published grammar admits, and the smallest one the engine
still calls a syntax error is what gets printed, along with the productions on
the way down to it. A forty token rejection tells a reader nothing; the same
rejection on six tokens names the construct in dispute.

The walk is seeded, so the same seed writes the same statements on every
machine, and a lead nobody records would come back forever. Recording it is
[`grammar/promoted.yaml`](grammar/promoted.yaml): a lead that survives review is
rewritten by hand as a case citing a clause and its fingerprint is noted here,
and a lead review rejects is noted here with the reason. The generator's output
is a lead, and the corpus is the record.

Two limits are printed with the section rather than kept in the documentation.
The walk stops at tokens this harness chooses by hand, because ISO defines 23 of
its productions in prose, so the productions inside a token are never exercised.
And a walk from `<GQL-program>` reaches 699 of the 814 productions, with 14
replaced by a token and 4 reachable but unwritable, which is the ceiling on
anything the phase can say.

## The choices the standard leaves open

ISO/IEC 39075 does not specify everything. It leaves 117 items to the
implementation, which Clause 24.5.2 then obliges to write down what it chose,
20 to the running system, which nobody has to document and no program may rely
on, and it permits extensions under 24.5.3 on the same condition as the first:
say what they are.

No case tests any of that, and none ever will: an engine that pads character
strings for comparison and an engine that does not are both conforming. Every
run instead ends with an observation phase, seventeen probes deep, and prints
what it saw in a section that carries no verdict at all. There is no `pass`,
`fail`, `skip` or `error` anywhere in it, which is asserted by a test rather
than left to care, and nothing in it enters a total, a coverage denominator or
the exit status. A probe that could not be put to the engine prints an em dash
and the reason, because not observed is not the same as none and not the same
as unlimited.

```
$ gql-compat statement reports/zu/zu.json -out STATEMENT.md
note: 7 of the 117 implementation-defined items carry an observed answer; the rest are yours to write
```

That is the point of the exercise. `statement` prints every delegated item in
ISO code order with the standard's own words for each, the answers this run
observed already filled in beside the statement that observed them, and a dash
against everything a vendor still has to answer themselves. It is the only
output of this tool that is meant to be edited.

## Reproducibility

A latency number without its context is a rumour, so every report records the
engine version, the OS, kernel, CPU model, core count, GOMAXPROCS, total
memory, whether it ran containerised, the repetition and warmup counts, the
statement timeout, the sampler interval, the selector, and the ISO artifact
source. Generated fixtures are built from a seeded ChaCha8 stream, so two
machines building the same graph build it edge for edge — otherwise a
cross-machine comparison is comparing graphs, not engines.

Warmups are never applied to a mutating case, because running an `INSERT` eight
times measures something other than the `INSERT`.

## Contributing a case

Cases live in [`corpus/suite`](corpus/suite), grouped by file; the kind comes
from the file the case is in, and the fixtures it may use are declared at the
top of that file or in `00-fixtures.yaml`.

```yaml
- id: mandatory/pattern/node-pattern-unlabelled
  name: A node pattern with only a variable binds every node in the graph
  subclauses: ["16.4"]
  productions: [node pattern, element variable declaration]
  query: |
    MATCH (x)
    RETURN COUNT(*) AS n
  expect:
    kind: rows
    columns: [n]
    rows: [[7]]
```

`gql-compat validate` rejects it if `16.4` is not a normative subclause or
`node pattern` is not a production in the grammar. Order-sensitive comparison
is the default; add `unordered: true` under `expect` for a query with no
`ORDER BY`, since GQL promises nothing about order without one.

## Licence

The Go code is Apache-2.0; see [LICENSE](LICENSE). The files under
[`iso/artifacts`](iso/artifacts) are ISO/IEC's, redistributed unmodified from
the machine-readable set ISO publishes alongside the standard; their checksums
are in `iso/artifacts/SHA256SUMS` and `make verify-artifacts` checks them. This
project is not affiliated with or endorsed by ISO, IEC, or any engine vendor,
and a report it produces is not a certification.
