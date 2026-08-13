package report

import (
	"encoding/csv"
	"io"
	"strconv"
	"strings"

	"github.com/tamnd/gql-compat/metrics"
	"github.com/tamnd/gql-compat/runner"
)

// csvColumns is every per-case field, in a fixed order.
//
// The order is part of the contract: a script that plots column 31 of last
// month's run must find the same quantity there this month. New columns go on
// the end.
var csvColumns = []string{
	"id", "name", "kind", "mode", "outcome", "evidence", "reason",
	"skip_reason", "missing_capabilities", "fixture",
	"features", "subclauses", "productions", "conditions", "tags",

	"repeats", "warmups", "samples", "failures",
	"min_ns", "p50_ns", "p90_ns", "p95_ns", "p99_ns", "max_ns",
	"mean_ns", "stddev_ns", "mad_ns", "total_ns",
	"queries_per_sec", "rows_per_sec", "cells_per_sec",
	"mean_rows", "mean_bytes",

	"cpu_user_ns", "cpu_sys_ns", "cpu_utilization",
	"rss_start_bytes", "rss_end_bytes", "rss_peak_bytes",
	"vms_peak_bytes", "swap_peak_bytes", "threads_peak",
	"read_bytes", "write_bytes", "read_ops", "write_ops",
	"minor_faults", "major_faults",
	"voluntary_ctx_switches", "involuntary_ctx_switches",
	"sampler_reads",

	"disk_before_bytes", "disk_after_bytes", "disk_growth_bytes",
	"disk_alloc_before_bytes", "disk_alloc_after_bytes", "disk_alloc_growth_bytes",
	"disk_files",

	"load_wall_ns", "load_engine_wall_ns", "load_nodes", "load_edges",
	"load_nodes_per_sec", "load_edges_per_sec",
	"load_disk_growth_bytes", "load_bits_per_edge", "load_bytes_per_node",
	"load_rss_peak_bytes", "load_cpu_user_ns", "load_cpu_sys_ns",

	"want_gqlstatus", "got_gqlstatus", "message",
	"wall_ns", "started", "statement",
}

// WriteCSV writes one row per case with every metric the runner captured.
//
// An unavailable metric is written as an empty field, not a zero. gopsutil
// cannot read I/O counters or page faults for another user's process on most
// platforms, a server engine's storage is not on this machine, and a sampler
// that got no reading measured nothing. A spreadsheet that averaged zeros
// over those would produce a number nobody measured.
func WriteCSV(w io.Writer, rep *runner.Report) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvColumns); err != nil {
		return err
	}
	for i := range rep.Cases {
		if err := cw.Write(csvRow(&rep.Cases[i])); err != nil {
			return err
		}
	}
	// The grammar walk's statements are here too, and the `kind` column says
	// `generated` on every one of them. That column is the filter: a script
	// computing a conformance rate must exclude them, the same way the
	// scoreboard does. Leaving them out entirely was the alternative and it is
	// worse, because this file is the metric archive and a statement that ran
	// on the engine belongs in it.
	if x := rep.Exploration; x != nil {
		for i := range x.Cases {
			if err := cw.Write(csvRow(&x.Cases[i])); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

func csvRow(c *runner.CaseResult) []string {
	s := c.Stats
	p := c.Process
	d := c.Disk

	row := []string{
		c.ID, c.Name, string(c.Kind), string(c.Mode), string(c.Outcome),
		string(c.Evidence), c.Reason, string(c.Skip), capsCSV(c.Missing), c.Fixture,
		strings.Join(c.Features, " "), strings.Join(c.Subclauses, " "),
		strings.Join(c.Productions, " "), strings.Join(c.Conditions, " "),
		strings.Join(c.Tags, " "),

		itoa(c.Repeats), itoa(c.Warmups), itoa(s.Count), itoa(s.Failures),
		dur(s.Min), dur(s.P50), dur(s.P90), dur(s.P95), dur(s.P99), dur(s.Max),
		dur(s.Mean), dur(s.StdDev), dur(s.MAD), dur(s.Total),
		ftoa(s.QueriesPerSec), ftoa(s.RowsPerSec), ftoa(s.CellsPerSec),
		ftoa(s.MeanRows), ftoa(s.MeanBytes),

		when(p.CPUOK, dur(p.CPUUser)), when(p.CPUOK, dur(p.CPUSys)),
		utilization(c),
		when(p.MemoryOK, i64(p.RSSStart)), when(p.MemoryOK, i64(p.RSSEnd)),
		when(p.MemoryOK, i64(p.RSSPeak)),
		when(p.MemoryOK, i64(p.VMSPeak)), when(p.MemoryOK, i64(p.SwapPeak)),
		when(p.MemoryOK, itoa(int(p.NumThread))),
		when(p.IOOK, i64(p.ReadBytes)), when(p.IOOK, i64(p.WriteBytes)),
		when(p.IOOpsOK, i64(p.ReadOps)), when(p.IOOpsOK, i64(p.WriteOps)),
		when(p.FaultsOK, i64(p.MinorFaults)), when(p.FaultsOK, i64(p.MajorFaults)),
		when(p.CtxOK, i64(p.VoluntaryCS)), when(p.CtxOK, i64(p.InvoluntaryCS)),
		itoa(p.Samples),

		when(d.OK, i64(d.BytesBefore)), when(d.OK, i64(d.BytesAfter)),
		when(d.OK, i64(d.Growth())),
		when(d.OK, i64(d.AllocBefore)), when(d.OK, i64(d.AllocAfter)),
		when(d.OK, i64(d.AllocGrowth())),
		when(d.OK, itoa(d.Files)),
	}

	if l := c.Load; l != nil {
		row = append(row,
			dur(l.Wall), when(l.EngineWall > 0, dur(l.EngineWall)), itoa(l.Nodes), itoa(l.Edges),
			ftoa(l.NodesPerSec), ftoa(l.EdgesPerSec),
			when(l.Disk.OK, i64(l.Disk.Growth())),
			when(l.Disk.OK, ftoa(l.BitsPerEdge)), when(l.Disk.OK, ftoa(l.BytesPerNode)),
			when(l.Process.MemoryOK, i64(l.Process.RSSPeak)),
			when(l.Process.CPUOK, dur(l.Process.CPUUser)),
			when(l.Process.CPUOK, dur(l.Process.CPUSys)),
		)
	} else {
		// This case reused a fixture another case loaded. Blank is right:
		// zero would say the load was free.
		row = append(row, "", "", "", "", "", "", "", "", "", "", "", "")
	}

	row = append(row,
		c.WantStatus, c.GotStatus, c.Message,
		dur(c.Wall), c.Started.UTC().Format("2006-01-02T15:04:05.000Z"),
		strings.Join(strings.Fields(c.Statement), " "),
	)
	return row
}

// when renders a value only if the measurement behind it was available.
func when(ok bool, v string) string {
	if !ok {
		return ""
	}
	return v
}

func utilization(c *runner.CaseResult) string {
	series := metrics.Series{Process: c.Process, Samples: []metrics.Sample{{Wall: c.Stats.Total}}}
	u, ok := series.CPUUtilization()
	if !ok {
		return ""
	}
	return ftoa(u)
}

func itoa(n int) string  { return strconv.Itoa(n) }
func i64(n int64) string { return strconv.FormatInt(n, 10) }
func ftoa(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func dur[T ~int64](d T) string { return strconv.FormatInt(int64(d), 10) }

func capsCSV[T ~string](caps []T) string {
	parts := make([]string, len(caps))
	for i, c := range caps {
		parts[i] = string(c)
	}
	return strings.Join(parts, " ")
}
