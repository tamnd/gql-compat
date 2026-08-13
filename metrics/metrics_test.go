package metrics_test

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/gql-compat/metrics"
)

func series(warmups int, ds ...time.Duration) *metrics.Series {
	s := &metrics.Series{Warmups: warmups}
	for _, d := range ds {
		s.Samples = append(s.Samples, metrics.Sample{Wall: d, Rows: 2, Cells: 4, Bytes: 100})
	}
	return s
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestPercentilesComeFromTheSortedSamples(t *testing.T) {
	// Deliberately out of order: a summary that assumed arrival order would
	// get the min and the max right and everything between them wrong.
	st := series(0, ms(50), ms(10), ms(30), ms(20), ms(40)).Summarize()

	if st.Count != 5 {
		t.Fatalf("count %d, want 5", st.Count)
	}
	if st.Min != ms(10) || st.Max != ms(50) {
		t.Errorf("min %v max %v, want 10ms and 50ms", st.Min, st.Max)
	}
	if st.P50 != ms(30) {
		t.Errorf("p50 %v, want 30ms", st.P50)
	}
	if st.Mean != ms(30) {
		t.Errorf("mean %v, want 30ms", st.Mean)
	}
	if st.Total != ms(150) {
		t.Errorf("total %v, want 150ms", st.Total)
	}
	// The percentiles must be monotonic. A p90 below a p50 is arithmetically
	// impossible and would mean the index calculation is wrong.
	if st.P50 > st.P90 || st.P90 > st.P95 || st.P95 > st.P99 || st.P99 > st.Max {
		t.Errorf("percentiles are not monotonic: %v %v %v %v %v",
			st.P50, st.P90, st.P95, st.P99, st.Max)
	}
}

func TestMedianAbsoluteDeviationSurvivesOneRuinedSample(t *testing.T) {
	// This is why MAD is reported beside the standard deviation. Nine samples
	// agree and one was interrupted by the scheduler; a reader needs a spread
	// figure that says the engine was steady, and stddev does not say it.
	st := series(0, ms(10), ms(10), ms(10), ms(10), ms(10), ms(10), ms(10), ms(10), ms(10), ms(1000)).Summarize()
	if st.MAD > ms(1) {
		t.Errorf("MAD %v; one outlier in ten should barely move it", st.MAD)
	}
	if st.StdDev < ms(100) {
		t.Errorf("stddev %v; the outlier should dominate it, or this test is measuring nothing", st.StdDev)
	}
}

func TestThroughputIsSerialAndDerivedFromTotal(t *testing.T) {
	// Four samples of 250ms each: one second of work, four queries, eight rows.
	st := series(0, ms(250), ms(250), ms(250), ms(250)).Summarize()
	if math.Abs(st.QueriesPerSec-4) > 0.001 {
		t.Errorf("queries/sec %v, want 4", st.QueriesPerSec)
	}
	if math.Abs(st.RowsPerSec-8) > 0.001 {
		t.Errorf("rows/sec %v, want 8", st.RowsPerSec)
	}
	if math.Abs(st.CellsPerSec-16) > 0.001 {
		t.Errorf("cells/sec %v, want 16", st.CellsPerSec)
	}
}

func TestFailedSamplesAreCountedNotDropped(t *testing.T) {
	// How fast an engine rejects bad input is a real measurement. Dropping
	// failed samples would leave every condition case with an empty latency
	// table and a count of zero.
	s := series(0, ms(5), ms(7))
	s.Samples[1].Err = errors.New("refused")
	st := s.Summarize()
	if st.Count != 2 {
		t.Errorf("count %d, want both samples", st.Count)
	}
	if st.Failures != 1 {
		t.Errorf("failures %d, want 1", st.Failures)
	}
}

func TestEmptySeriesIsZeroAndNotADivisionByZero(t *testing.T) {
	st := (&metrics.Series{}).Summarize()
	if st.Count != 0 || st.QueriesPerSec != 0 {
		t.Errorf("an empty series summarised to %+v", st)
	}
}

func TestLoadDensityFiguresUseTheGraphThatWasLoaded(t *testing.T) {
	l := metrics.Load{
		Wall:  time.Second,
		Nodes: 1000,
		Edges: 4000,
		// An empty store of 400 bytes puts the loaded one 20× above the floor,
		// which is what a density figure needs before it means anything.
		EmptyBytes: 400,
		Disk:       metrics.DiskDelta{BytesBefore: 0, BytesAfter: 8000, OK: true},
	}
	l.Compute()
	if l.NodesPerSec != 1000 || l.EdgesPerSec != 4000 {
		t.Errorf("ingest rates %v and %v", l.NodesPerSec, l.EdgesPerSec)
	}
	if !l.DensityOK {
		t.Fatalf("density withheld at %.1f× the floor: %s", l.FloorRatio, l.DensityNote)
	}
	// 8000 bytes over 4000 edges is 16 bits per edge.
	if l.BitsPerEdge != 16 {
		t.Errorf("bits per edge %v, want 16", l.BitsPerEdge)
	}
	if l.BytesPerNode != 8 {
		t.Errorf("bytes per node %v, want 8", l.BytesPerNode)
	}
}

// A store that is mostly the engine's own preallocation divides the floor by
// the fixture. Nine fixtures spanning six nodes to 261 632 edges did exactly
// that on 2026-08-12 and produced nine densities from one store size.
func TestDensityIsWithheldWhenTheStoreIsMostlyFloor(t *testing.T) {
	l := metrics.Load{
		Wall:       time.Second,
		Nodes:      6,
		Edges:      5,
		EmptyBytes: 3_670_016,
		Disk:       metrics.DiskDelta{BytesAfter: 3_932_160, OK: true},
	}
	l.Compute()
	if l.DensityOK {
		t.Fatalf("density reported at %.1f× the floor", l.FloorRatio)
	}
	if l.BitsPerEdge != 0 || l.BytesPerNode != 0 {
		t.Errorf("figures published anyway: %v bits/edge, %v bytes/node", l.BitsPerEdge, l.BytesPerNode)
	}
	if l.FloorRatio < 1 {
		t.Errorf("floor ratio %v, want the measured multiple", l.FloorRatio)
	}
	if l.DensityNote == "" {
		t.Error("no reason given for the missing density")
	}
}

// Clearing the floor is not enough on its own. The numbers below are zu's from
// 2026-08-12: an empty store that measured 256 KiB, a self-loop fixture of one
// node and one edge whose store weighed 3.5 MiB, and a ratio of 14× that walked
// straight past the floor test and published 29 360 128 bits for one edge. An
// engine whose empty store is measured before it has written a single table
// understates its own fixed cost, and the floor ratio cannot see that. The
// share can: one edge does not cost fourteen empty databases.
func TestDensityIsWithheldWhenOneElementOutweighsAnEmptyStore(t *testing.T) {
	l := metrics.Load{
		Wall:       time.Second,
		Nodes:      1,
		Edges:      1,
		EmptyBytes: 262_144,
		Disk:       metrics.DiskDelta{BytesAfter: 3_670_016, OK: true},
	}
	l.Compute()
	if l.FloorRatio < metrics.DensityFloor {
		t.Fatalf("floor ratio %.1f×, want a case that passes the floor test", l.FloorRatio)
	}
	if l.DensityOK {
		t.Fatalf("density reported at %v bits/edge for one edge in a %d byte store", l.BitsPerEdge, l.Disk.BytesAfter)
	}
	if l.BitsPerEdge != 0 || l.BytesPerNode != 0 {
		t.Errorf("figures published anyway: %v bits/edge, %v bytes/node", l.BitsPerEdge, l.BytesPerNode)
	}
	if l.DensityNote == "" {
		t.Error("no reason given for the missing density")
	}
}

// The share test has to leave alone the graphs it was not written for. A
// million edges in a store of a few hundred megabytes is the case the whole
// density figure exists to serve, and its per element share is a handful of
// bytes.
func TestALargeGraphKeepsItsDensity(t *testing.T) {
	l := metrics.Load{
		Wall:       time.Second,
		Nodes:      1_000_000,
		Edges:      999_999,
		EmptyBytes: 262_144,
		Disk:       metrics.DiskDelta{BytesAfter: 12_000_000, OK: true},
	}
	l.Compute()
	if !l.DensityOK {
		t.Fatalf("density withheld from a million node graph: %s", l.DensityNote)
	}
	if l.BitsPerEdge <= 0 || l.BytesPerNode <= 0 {
		t.Errorf("density said to be available but the figures are %v and %v", l.BitsPerEdge, l.BytesPerNode)
	}
}

// An engine that can say which part of its store is the graph gets a density
// for a fixture the floor ratio would have refused. The numbers are zu's: a
// four-block schema of 1 MiB, 256 KiB blocks, and a graph occupying 16 blocks.
// The whole store is 5× its fixed part, well under the floor, and every figure
// the ratio test would have withheld here is now exactly computable.
func TestAKnownSchemaSizeIsSubtractedRatherThanGuessedAt(t *testing.T) {
	l := metrics.Load{
		Wall:  time.Second,
		Nodes: 60_000,
		Edges: 400_000,
		// Deliberately present and deliberately wrong for this store: the empty
		// load is a proxy and the engine's own answer supersedes it.
		EmptyBytes:  262_144,
		SchemaBytes: 1_048_576,
		AllocUnit:   262_144,
		Disk:        metrics.DiskDelta{BytesAfter: 5_242_880, OK: true},
	}
	l.Compute()
	if !l.DensityOK {
		t.Fatalf("density withheld from a graph whose own bytes are known: %s", l.DensityNote)
	}
	if l.GraphBytes != 4_194_304 {
		t.Fatalf("graph bytes %d, want the store less its schema", l.GraphBytes)
	}
	// The graph's bytes and not the store's: 4 MiB over 400 000 edges is
	// 83.9 bits, where dividing the whole store would have said 104.9.
	if want := float64(4_194_304*8) / 400_000; l.BitsPerEdge != want {
		t.Errorf("bits per edge %v, want %v: the whole store was divided rather than the graph", l.BitsPerEdge, want)
	}
	if want := float64(4_194_304) / 60_000; l.BytesPerNode != want {
		t.Errorf("bytes per node %v, want %v", l.BytesPerNode, want)
	}
}

// Subtracting the fixed part exactly leaves one way to be wrong: the graph's
// last allocation unit is charged whole. A graph of one edge in a store that
// grows 256 KiB at a time reports a quarter of a megabyte per edge, which is
// the granularity and not the encoding.
func TestAGraphSmallerThanTenAllocationUnitsGetsNoDensity(t *testing.T) {
	l := metrics.Load{
		Wall:        time.Second,
		Nodes:       2,
		Edges:       1,
		SchemaBytes: 1_048_576,
		AllocUnit:   262_144,
		Disk:        metrics.DiskDelta{BytesAfter: 1_310_720, OK: true},
	}
	l.Compute()
	if l.DensityOK {
		t.Fatalf("density reported at %v bits/edge for one edge in one block", l.BitsPerEdge)
	}
	if l.GraphBytes != 262_144 {
		t.Errorf("graph bytes %d, want the one block the graph occupies", l.GraphBytes)
	}
	if l.DensityNote == "" {
		t.Error("no reason given for the missing density")
	}
}

// A store whose fixed part is the whole of it holds a graph that cost nothing
// measurable. Dividing zero by the graph would publish an encoding of 0 bits
// per edge, which is the most flattering wrong number the report could print.
func TestAGraphThatFitsInsideTheFixedPartGetsNoDensity(t *testing.T) {
	l := metrics.Load{
		Wall:        time.Second,
		Nodes:       2,
		Edges:       1,
		SchemaBytes: 1_048_576,
		Disk:        metrics.DiskDelta{BytesAfter: 1_048_576, OK: true},
	}
	l.Compute()
	if l.DensityOK || l.BitsPerEdge != 0 {
		t.Fatalf("a graph that added no bytes reported %v bits per edge", l.BitsPerEdge)
	}
	if l.GraphBytes != 0 {
		t.Errorf("graph bytes %d, want 0", l.GraphBytes)
	}
	if l.DensityNote == "" {
		t.Error("no reason given for the missing density")
	}
}

// An engine that reports a schema size but no allocation unit has nothing left
// to be suspected of, and the figures stand.
func TestAKnownSchemaWithNoAllocationUnitStillDivides(t *testing.T) {
	l := metrics.Load{
		Wall:        time.Second,
		Nodes:       100,
		Edges:       400,
		SchemaBytes: 4096,
		Disk:        metrics.DiskDelta{BytesAfter: 8192, OK: true},
	}
	l.Compute()
	if !l.DensityOK {
		t.Fatalf("density withheld: %s", l.DensityNote)
	}
	if l.BitsPerEdge != float64(4096*8)/400 {
		t.Errorf("bits per edge %v", l.BitsPerEdge)
	}
}

// The rates are the engine's and do not depend on the floor. An unknown empty
// store costs the density figures and nothing else.
func TestUnknownEmptyStoreCostsOnlyTheDensity(t *testing.T) {
	l := metrics.Load{
		Wall:  time.Second,
		Nodes: 1000,
		Edges: 4000,
		Disk:  metrics.DiskDelta{BytesAfter: 8000, OK: true},
	}
	l.Compute()
	if l.NodesPerSec != 1000 {
		t.Errorf("nodes per second %v", l.NodesPerSec)
	}
	if l.DensityOK || l.BitsPerEdge != 0 {
		t.Errorf("density computed against an unknown floor: %v", l.BitsPerEdge)
	}
	if l.DensityNote == "" {
		t.Error("no reason given for the missing density")
	}
}

func TestDiskMeasuresTheDirectoryItIsGiven(t *testing.T) {
	dir := t.TempDir()
	before := metrics.Before(dir)
	if !before.OK {
		t.Skip("the disk reader is unavailable on this platform")
	}
	if err := os.WriteFile(filepath.Join(dir, "graph.dat"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	after := metrics.After(dir, before)
	if after.Growth() < 4096 {
		t.Errorf("growth %d bytes after writing 4096", after.Growth())
	}
	if after.Files != 1 {
		t.Errorf("file count %d, want 1", after.Files)
	}
}

func TestAnUnreadableDirectoryIsUnavailableNotZero(t *testing.T) {
	// A server engine's data directory is not on this machine. Reporting zero
	// bytes for it would put a number into a comparison that no measurement
	// produced; reporting unavailable is the whole rule of this package.
	d := metrics.Before(filepath.Join(t.TempDir(), "does-not-exist"))
	if d.OK {
		t.Errorf("a missing directory reported as measured: %+v", d)
	}
}

// TestSamplerCreditsWorkDoneByAProcessThatDidNotSurviveTheWindow is the
// bulk-load case in miniature. An adapter that reloads by stopping its shell,
// running a converter, and starting a fresh shell spends nearly all of the
// window's CPU in a process that is gone by the time Stop takes its final
// reading. A sampler that subtracts endpoints reports the new shell's first
// few milliseconds; one that banks each process reports the converter.
func TestSamplerCreditsWorkDoneByAProcessThatDidNotSurviveTheWindow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the busy loop below is a POSIX shell")
	}
	var mu sync.Mutex
	var current *exec.Cmd
	pid := func() int {
		mu.Lock()
		defer mu.Unlock()
		if current == nil || current.Process == nil {
			return 0
		}
		return current.Process.Pid
	}
	start := func(script string) *exec.Cmd {
		cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", script)
		if err := cmd.Start(); err != nil {
			t.Fatalf("starting %q: %v", script, err)
		}
		mu.Lock()
		current = cmd
		mu.Unlock()
		return cmd
	}
	stop := func(cmd *exec.Cmd) {
		mu.Lock()
		current = nil
		mu.Unlock()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	s := metrics.NewSamplerFunc(pid, time.Millisecond)
	s.Start()
	// The work: a process that burns a core and then dies, exactly as a
	// converter does.
	busy := start("while :; do :; done")
	time.Sleep(300 * time.Millisecond)
	stop(busy)
	// The gap, and then the replacement, which does nothing at all.
	time.Sleep(20 * time.Millisecond)
	idle := start("sleep 30")
	time.Sleep(20 * time.Millisecond)
	d := s.Stop()
	stop(idle)

	if !d.CPUOK {
		t.Skip("this platform does not expose per-process CPU time")
	}
	// 300 ms of spinning on a loaded CI box still leaves well over 100 ms of
	// CPU. The threshold is loose on purpose: the point is that the figure is
	// the converter's, not that it is exact.
	if got := d.CPUUser + d.CPUSys; got < 100*time.Millisecond {
		t.Errorf("CPU over the window was %v, want the busy process's share of 300ms", got)
	}
}

func TestSamplerReportsUnavailableForANonexistentProcess(t *testing.T) {
	s := metrics.NewSampler(0, time.Millisecond)
	s.Start()
	time.Sleep(5 * time.Millisecond)
	d := s.Stop()
	// PID 0 means an engine with no process of its own here. Whatever the
	// platform can say about it, it must not claim CPU time it never read.
	if d.CPUOK && d.CPUUser < 0 {
		t.Errorf("negative CPU time reported: %+v", d)
	}
}
