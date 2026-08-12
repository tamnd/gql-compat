// Package metrics captures what a run cost, not merely whether it passed.
//
// A conformance verdict is one bit. The interesting question about two
// engines that both answer a query correctly is what each spent doing it, and
// spending is several things at once: time on the clock, time on a CPU, bytes
// resident, bytes on disk, bytes moved through the kernel. This package
// measures each of them separately, records the distribution rather than a
// mean, and is explicit about which numbers it could not get on the current
// platform instead of reporting a zero that reads like a measurement.
package metrics

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Sample is one execution of one statement.
type Sample struct {
	// Wall is elapsed time from just before the statement was handed to the
	// engine to just after its last row was read.
	Wall time.Duration `json:"wall_ns"`
	// Rows and Cells are what came back, so throughput can be expressed per
	// row and per value rather than only per query.
	Rows  int `json:"rows"`
	Cells int `json:"cells"`
	// Bytes is the size of the serialised result, which for a subprocess
	// adapter is exactly what crossed the pipe and for an in-process one is
	// an estimate of what was materialised.
	Bytes int64 `json:"bytes"`
	// Err is non-nil when the statement failed. Failed samples are kept: how
	// fast an engine rejects bad input is a real number, and dropping them
	// would make an error case's latency table empty.
	Err error `json:"-"`
}

// Series is every sample for one statement, plus the process-level deltas
// measured across the whole series.
type Series struct {
	Samples []Sample `json:"-"`

	// Warmups is how many executions ran before the recorded ones, and are
	// excluded from every statistic below.
	Warmups int `json:"warmups"`

	// Process holds the resource deltas attributable to the series. They are
	// measured once around the whole series rather than per sample, because
	// the cost of reading them is comparable to the cost of a fast query.
	Process ProcessDelta `json:"process"`

	// Disk is what the engine's storage did across the series.
	Disk DiskDelta `json:"disk"`
}

// Stats is the distribution of a Series' wall times together with the derived
// rates. Every field is computed from the samples; nothing is carried over
// from a previous run.
type Stats struct {
	Count    int `json:"count"`
	Failures int `json:"failures"`

	Min    time.Duration `json:"min_ns"`
	P50    time.Duration `json:"p50_ns"`
	P90    time.Duration `json:"p90_ns"`
	P95    time.Duration `json:"p95_ns"`
	P99    time.Duration `json:"p99_ns"`
	Max    time.Duration `json:"max_ns"`
	Mean   time.Duration `json:"mean_ns"`
	StdDev time.Duration `json:"stddev_ns"`
	// MAD is the median absolute deviation. It survives the one sample in
	// fifty that a page fault or a scheduler decision ruined, which a
	// standard deviation over a handful of samples does not.
	MAD time.Duration `json:"mad_ns"`
	// Total is the summed wall time, which is what a throughput over the
	// whole series divides by.
	Total time.Duration `json:"total_ns"`

	// QueriesPerSec, RowsPerSec, and CellsPerSec are computed over Total, so
	// they describe the observed serial rate and not a projected parallel
	// one. A harness that multiplies by a core count is guessing.
	QueriesPerSec float64 `json:"queries_per_sec"`
	RowsPerSec    float64 `json:"rows_per_sec"`
	CellsPerSec   float64 `json:"cells_per_sec"`

	// MeanRows is the mean row count, which makes a suspiciously fast query
	// that returned nothing visible next to its latency.
	MeanRows  float64 `json:"mean_rows"`
	MeanBytes float64 `json:"mean_bytes"`
}

// ProcessDelta is the resource cost of a series as the operating system
// accounts for it. Fields whose Available bool is false were not obtainable
// on this platform and must be rendered as unknown, never as zero.
type ProcessDelta struct {
	// CPUUser and CPUSys are the process's own CPU time. Their sum against
	// Wall is the parallelism the engine actually achieved, which is the one
	// number that separates "fast" from "spent eight cores to be fast".
	CPUUser time.Duration `json:"cpu_user_ns"`
	CPUSys  time.Duration `json:"cpu_sys_ns"`
	CPUOK   bool          `json:"cpu_available"`

	// RSSPeak is the high-water mark of resident memory over the series, and
	// RSSEnd where it settled. A peak far above the end is a query that
	// materialised something it then dropped.
	RSSPeak   int64 `json:"rss_peak_bytes"`
	RSSEnd    int64 `json:"rss_end_bytes"`
	RSSStart  int64 `json:"rss_start_bytes"`
	MemoryOK  bool  `json:"memory_available"`
	VMSPeak   int64 `json:"vms_peak_bytes"`
	SwapPeak  int64 `json:"swap_peak_bytes"`
	NumThread int32 `json:"threads_peak"`

	// ReadBytes and WriteBytes are what the process asked the kernel to move.
	// On Linux these come from /proc/<pid>/io and count page-cache traffic
	// too, so a warm read shows bytes even though no disk turned.
	ReadBytes  int64 `json:"read_bytes"`
	WriteBytes int64 `json:"write_bytes"`
	ReadOps    int64 `json:"read_ops"`
	WriteOps   int64 `json:"write_ops"`
	IOOK       bool  `json:"io_available"`

	// MinorFaults and MajorFaults separate a page that was already in memory
	// from one that had to come off a device. A major fault during a query
	// the engine claimed was warm is the most useful single signal that a
	// cache is not doing what its documentation says.
	MinorFaults int64 `json:"minor_faults"`
	MajorFaults int64 `json:"major_faults"`
	FaultsOK    bool  `json:"faults_available"`

	// VoluntaryCS and InvoluntaryCS are context switches. A high involuntary
	// count under a light load means the measurement was contended and the
	// latencies below it should be read with suspicion.
	VoluntaryCS   int64 `json:"voluntary_ctx_switches"`
	InvoluntaryCS int64 `json:"involuntary_ctx_switches"`
	CtxOK         bool  `json:"ctx_available"`

	// Samples is how many times the sampler read the process while the series
	// ran. A peak from two samples is not a peak.
	Samples int `json:"sampler_reads"`
}

// DiskDelta is what the engine's storage weighed before and after.
type DiskDelta struct {
	// BytesBefore and BytesAfter are the apparent size of everything under
	// the engine's working directory.
	BytesBefore int64 `json:"bytes_before"`
	BytesAfter  int64 `json:"bytes_after"`
	// AllocBefore and AllocAfter are the allocated size: blocks actually
	// charged, which differs from apparent size for a sparse or a compressed
	// file and is the number a storage bill is computed from.
	AllocBefore int64 `json:"alloc_before"`
	AllocAfter  int64 `json:"alloc_after"`
	// Files is the file count after, because an engine that spreads a graph
	// over ten thousand segments has a cost that bytes alone do not show.
	Files int  `json:"files_after"`
	OK    bool `json:"available"`
}

// Growth is how many bytes the series added to disk.
func (d DiskDelta) Growth() int64 { return d.BytesAfter - d.BytesBefore }

// AllocGrowth is how many allocated bytes the series added.
func (d DiskDelta) AllocGrowth() int64 { return d.AllocAfter - d.AllocBefore }

// Load describes what it cost to get a fixture into an engine. Ingest is a
// separate measurement from query because the two are optimised against each
// other: an engine can buy a fast scan with a slow, wide write, and a report
// that only timed queries would call that free.
type Load struct {
	Wall  time.Duration `json:"wall_ns"`
	Nodes int           `json:"nodes"`
	Edges int           `json:"edges"`

	NodesPerSec float64 `json:"nodes_per_sec"`
	EdgesPerSec float64 `json:"edges_per_sec"`

	Process ProcessDelta `json:"process"`
	Disk    DiskDelta    `json:"disk"`

	// BitsPerEdge is the whole on-disk size divided by the edge count. It is
	// the density figure graph storage papers quote and the one number that
	// compares two engines' adjacency encodings directly.
	BitsPerEdge float64 `json:"bits_per_edge"`
	// BytesPerNode is the same figure per vertex, which is the one that moves
	// when properties, not topology, dominate.
	BytesPerNode float64 `json:"bytes_per_node"`
}

// Compute fills the derived rates on a Load.
func (l *Load) Compute() {
	secs := l.Wall.Seconds()
	if secs > 0 {
		l.NodesPerSec = float64(l.Nodes) / secs
		l.EdgesPerSec = float64(l.Edges) / secs
	}
	size := l.Disk.Growth()
	if size <= 0 {
		size = l.Disk.BytesAfter
	}
	if l.Edges > 0 && size > 0 {
		l.BitsPerEdge = float64(size*8) / float64(l.Edges)
	}
	if l.Nodes > 0 && size > 0 {
		l.BytesPerNode = float64(size) / float64(l.Nodes)
	}
}

// Summarize reduces a series to its distribution. It sorts a copy, so the
// caller's samples keep the order they were taken in and a later pass can
// still look for drift across the series.
func (s *Series) Summarize() Stats {
	st := Stats{Count: len(s.Samples)}
	if st.Count == 0 {
		return st
	}
	durs := make([]time.Duration, 0, len(s.Samples))
	var totalRows, totalCells int
	var totalBytes int64
	for _, x := range s.Samples {
		if x.Err != nil {
			st.Failures++
		}
		durs = append(durs, x.Wall)
		st.Total += x.Wall
		totalRows += x.Rows
		totalCells += x.Cells
		totalBytes += x.Bytes
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })

	st.Min, st.Max = durs[0], durs[len(durs)-1]
	st.P50 = percentile(durs, 0.50)
	st.P90 = percentile(durs, 0.90)
	st.P95 = percentile(durs, 0.95)
	st.P99 = percentile(durs, 0.99)
	st.Mean = time.Duration(int64(st.Total) / int64(st.Count))

	var variance float64
	mean := float64(st.Mean)
	for _, d := range durs {
		diff := float64(d) - mean
		variance += diff * diff
	}
	st.StdDev = time.Duration(math.Sqrt(variance / float64(st.Count)))

	med := float64(st.P50)
	devs := make([]time.Duration, len(durs))
	for i, d := range durs {
		devs[i] = time.Duration(math.Abs(float64(d) - med))
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i] < devs[j] })
	st.MAD = percentile(devs, 0.50)

	if secs := st.Total.Seconds(); secs > 0 {
		st.QueriesPerSec = float64(st.Count) / secs
		st.RowsPerSec = float64(totalRows) / secs
		st.CellsPerSec = float64(totalCells) / secs
	}
	st.MeanRows = float64(totalRows) / float64(st.Count)
	st.MeanBytes = float64(totalBytes) / float64(st.Count)
	return st
}

// percentile picks the nearest-rank element of a sorted slice. With the
// sample counts a conformance run uses — tens, not thousands — interpolating
// between two neighbours invents precision the data does not have.
func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(q*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// CPUUtilization is the fraction of a core the series kept busy, or more than
// one if the engine ran parallel. It is unknown when CPU accounting was not
// available, which the bool reports.
func (s *Series) CPUUtilization() (float64, bool) {
	if !s.Process.CPUOK {
		return 0, false
	}
	wall := time.Duration(0)
	for _, x := range s.Samples {
		wall += x.Wall
	}
	if wall == 0 {
		return 0, false
	}
	return float64(s.Process.CPUUser+s.Process.CPUSys) / float64(wall), true
}

// Format renders a duration at a precision that suits its magnitude, so a
// table of query latencies does not print "1.2340000000ms" beside "3ns".
func Format(d time.Duration) string {
	switch {
	case d == 0:
		return "0"
	case d < time.Microsecond:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%.2fµs", float64(d.Nanoseconds())/1e3)
	case d < time.Second:
		return fmt.Sprintf("%.3fms", float64(d.Nanoseconds())/1e6)
	default:
		return fmt.Sprintf("%.3fs", d.Seconds())
	}
}

// FormatBytes renders a byte count in binary units.
func FormatBytes(n int64) string {
	neg := ""
	if n < 0 {
		neg, n = "-", -n
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%s%dB", neg, n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit && exp < 4; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%s%.2f%ciB", neg, float64(n)/float64(div), "KMGTP"[exp])
}
