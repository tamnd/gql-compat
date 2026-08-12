package metrics

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// Sampler watches one operating-system process while a series runs and
// reports what changed.
//
// The peak figures need polling: an engine that allocates a gigabyte in the
// middle of a query and frees it before returning leaves no trace in a
// before-and-after reading. The counters — CPU, faults, I/O — are monotonic
// and only need the two endpoints, but they are read on every poll anyway so
// that a series interrupted by a crash still has most of its numbers.
//
// Polling is not free. At the default 5 ms interval a sampler costs well
// under a percent of one core on both platforms this runs on, which is below
// the run-to-run noise on the latencies it sits beside. Sub-millisecond
// queries are measured in batches for exactly this reason: the sampler
// describes the batch, and the latency distribution describes the query.
type Sampler struct {
	proc     *process.Process
	interval time.Duration

	mu    sync.Mutex
	first snapshot
	last  snapshot
	peak  snapshot
	reads int
	have  bool

	cancel context.CancelFunc
	done   chan struct{}
}

type snapshot struct {
	cpuUser, cpuSys time.Duration
	cpuOK           bool

	rss, vms, swap int64
	threads        int32
	memOK          bool

	readBytes, writeBytes int64
	readOps, writeOps     int64
	ioOK                  bool

	minorFaults, majorFaults int64
	faultsOK                 bool

	volCS, involCS int64
	ctxOK          bool
}

// NewSampler attaches to a pid. A pid that has already exited, or that this
// process may not inspect, yields a sampler that reports nothing available
// rather than an error: a missing metric should degrade the report, not fail
// the conformance run that produced it.
func NewSampler(pid int, interval time.Duration) *Sampler {
	if interval <= 0 {
		interval = 5 * time.Millisecond
	}
	s := &Sampler{interval: interval}
	if p, err := process.NewProcess(int32(pid)); err == nil {
		s.proc = p
	}
	return s
}

// Self attaches to the harness's own process, which is what an in-process
// adapter is measured through.
func Self(interval time.Duration) *Sampler { return NewSampler(os.Getpid(), interval) }

// Start begins polling. Every Start must be matched by a Stop.
func (s *Sampler) Start() {
	if s.proc == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})

	s.mu.Lock()
	s.first = s.read()
	s.last, s.peak, s.have = s.first, s.first, s.first.cpuOK || s.first.memOK
	s.reads = 1
	s.mu.Unlock()

	go func() {
		defer close(s.done)
		t := time.NewTicker(s.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				snap := s.read()
				s.mu.Lock()
				s.last = snap
				s.peak = maxOf(s.peak, snap)
				s.reads++
				s.have = s.have || snap.cpuOK || snap.memOK
				s.mu.Unlock()
			}
		}
	}()
}

// Stop ends polling, takes a final reading, and returns the delta over the
// whole watched interval.
func (s *Sampler) Stop() ProcessDelta {
	if s.proc == nil {
		return ProcessDelta{}
	}
	if s.cancel != nil {
		s.cancel()
		<-s.done
		s.cancel = nil
	}
	final := s.read()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = final
	s.peak = maxOf(s.peak, final)
	s.reads++

	d := ProcessDelta{Samples: s.reads}
	if s.first.cpuOK && final.cpuOK {
		d.CPUUser = final.cpuUser - s.first.cpuUser
		d.CPUSys = final.cpuSys - s.first.cpuSys
		// gopsutil reports CPU time as a float of seconds; across a very
		// short series the difference can round negative. Clamp rather than
		// report a negative duration nobody can interpret.
		d.CPUUser = max(d.CPUUser, 0)
		d.CPUSys = max(d.CPUSys, 0)
		d.CPUOK = true
	}
	if s.first.memOK {
		d.RSSStart, d.RSSEnd, d.RSSPeak = s.first.rss, final.rss, s.peak.rss
		d.VMSPeak, d.SwapPeak, d.NumThread = s.peak.vms, s.peak.swap, s.peak.threads
		d.MemoryOK = true
	}
	if s.first.ioOK && final.ioOK {
		d.ReadBytes = final.readBytes - s.first.readBytes
		d.WriteBytes = final.writeBytes - s.first.writeBytes
		d.ReadOps = final.readOps - s.first.readOps
		d.WriteOps = final.writeOps - s.first.writeOps
		d.IOOK = true
	}
	if s.first.faultsOK && final.faultsOK {
		d.MinorFaults = final.minorFaults - s.first.minorFaults
		d.MajorFaults = final.majorFaults - s.first.majorFaults
		d.FaultsOK = true
	}
	if s.first.ctxOK && final.ctxOK {
		d.VoluntaryCS = final.volCS - s.first.volCS
		d.InvoluntaryCS = final.involCS - s.first.involCS
		d.CtxOK = true
	}
	return d
}

func (s *Sampler) read() snapshot {
	var snap snapshot
	p := s.proc
	if p == nil {
		return snap
	}
	if t, err := p.Times(); err == nil && t != nil {
		snap.cpuUser = time.Duration(t.User * float64(time.Second))
		snap.cpuSys = time.Duration(t.System * float64(time.Second))
		snap.cpuOK = true
	}
	if m, err := p.MemoryInfo(); err == nil && m != nil {
		snap.rss = int64(m.RSS)
		snap.vms = int64(m.VMS)
		snap.swap = int64(m.Swap)
		snap.memOK = true
	}
	if n, err := p.NumThreads(); err == nil {
		snap.threads = n
	}
	// IOCounters is Linux-only for a non-root reader on most systems; on
	// macOS it returns an error and the report says so.
	if io, err := p.IOCounters(); err == nil && io != nil {
		snap.readBytes = int64(io.ReadBytes)
		snap.writeBytes = int64(io.WriteBytes)
		snap.readOps = int64(io.ReadCount)
		snap.writeOps = int64(io.WriteCount)
		snap.ioOK = true
	}
	if f, err := p.PageFaults(); err == nil && f != nil {
		snap.minorFaults = int64(f.MinorFaults)
		snap.majorFaults = int64(f.MajorFaults)
		snap.faultsOK = true
	}
	if c, err := p.NumCtxSwitches(); err == nil && c != nil {
		snap.volCS = c.Voluntary
		snap.involCS = c.Involuntary
		snap.ctxOK = true
	}
	return snap
}

func maxOf(a, b snapshot) snapshot {
	out := a
	if b.memOK {
		out.memOK = true
		out.rss = max(a.rss, b.rss)
		out.vms = max(a.vms, b.vms)
		out.swap = max(a.swap, b.swap)
	}
	if b.threads > out.threads {
		out.threads = b.threads
	}
	return out
}

// MeasureDisk walks a directory and reports both what its contents claim to
// weigh and what they are actually charged.
//
// The two differ, and the difference is the point. A columnar engine that
// writes a sparse file, or a filesystem that compresses transparently, has an
// apparent size that overstates the bill; a small file on a large block size
// has an allocated size that understates the density of the format. A
// storage comparison that quotes only one of them is quoting the flattering
// one by accident.
func MeasureDisk(dir string) DiskDelta {
	d := DiskDelta{}
	// An engine that keeps nothing on this machine — a server on the far end
	// of a socket — has no directory to walk, and a directory that is not
	// there yet has no size. Both are unavailable, not empty. The walk below
	// tolerates a file vanishing under it, which would otherwise turn a
	// missing root into a measurement of zero bytes.
	if dir == "" {
		return d
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return d
	}
	apparent, allocated, files, err := walkSize(dir)
	if err != nil {
		return d
	}
	d.BytesAfter, d.AllocAfter, d.Files, d.OK = apparent, allocated, files, true
	return d
}

// Before records the starting size of a directory into a delta the caller
// finishes with After.
func Before(dir string) DiskDelta {
	d := MeasureDisk(dir)
	d.BytesBefore, d.AllocBefore = d.BytesAfter, d.AllocAfter
	d.BytesAfter, d.AllocAfter = 0, 0
	return d
}

// After completes a delta started by Before.
func After(dir string, d DiskDelta) DiskDelta {
	end := MeasureDisk(dir)
	d.BytesAfter, d.AllocAfter, d.Files = end.BytesAfter, end.AllocAfter, end.Files
	d.OK = d.OK && end.OK
	return d
}

func walkSize(dir string) (apparent, allocated int64, files int, err error) {
	err = filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			// A file the engine deleted between the walk and the stat is not
			// an error worth failing a measurement over.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if e.IsDir() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		apparent += info.Size()
		allocated += allocatedBytes(info)
		files++
		return nil
	})
	return apparent, allocated, files, err
}
