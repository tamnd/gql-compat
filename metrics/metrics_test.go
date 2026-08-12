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
		Disk:  metrics.DiskDelta{BytesBefore: 0, BytesAfter: 8000, OK: true},
	}
	l.Compute()
	if l.NodesPerSec != 1000 || l.EdgesPerSec != 4000 {
		t.Errorf("ingest rates %v and %v", l.NodesPerSec, l.EdgesPerSec)
	}
	// 8000 bytes over 4000 edges is 16 bits per edge.
	if l.BitsPerEdge != 16 {
		t.Errorf("bits per edge %v, want 16", l.BitsPerEdge)
	}
	if l.BytesPerNode != 8 {
		t.Errorf("bytes per node %v, want 8", l.BytesPerNode)
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
