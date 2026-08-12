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
	// pid is asked again on every tick rather than resolved once, so that a
	// session which restarts its engine mid-run is followed rather than lost.
	pid      func() int
	proc     *process.Process
	interval time.Duration

	mu    sync.Mutex
	first snapshot
	last  snapshot
	peak  snapshot
	reads int
	have  bool
	// bank holds the counters of every process the session has already
	// finished with. A window that spans a restart is the sum of what each
	// process in it spent, not the difference between the first and the last.
	bank banked
	// memBase and memLast are the first and most recent readable memory
	// figures, which unlike the counters are not banked: memory is a level
	// rather than a total, so it is read from whichever process was there.
	memBase, memLast snapshot

	cancel context.CancelFunc
	done   chan struct{}
}

// banked is the running total of the cumulative counters of processes that
// have come and gone inside the window.
type banked struct {
	cpuUser, cpuSys time.Duration
	cpuOK           bool

	readBytes, writeBytes int64
	readOps, writeOps     int64
	ioOK                  bool

	minorFaults, majorFaults int64
	faultsOK                 bool

	volCS, involCS int64
	ctxOK          bool
}

// add folds one process's contribution in: what it had read last, less the
// baseline it was measured from. Each group is banked only when both ends of
// it were readable, so a counter the platform does not expose stays absent
// rather than becoming a zero contribution.
func (b *banked) add(from, to snapshot) {
	if from.cpuOK && to.cpuOK {
		b.cpuUser += max(to.cpuUser-from.cpuUser, 0)
		b.cpuSys += max(to.cpuSys-from.cpuSys, 0)
		b.cpuOK = true
	}
	if from.ioOK && to.ioOK {
		b.readBytes += to.readBytes - from.readBytes
		b.writeBytes += to.writeBytes - from.writeBytes
		b.readOps += to.readOps - from.readOps
		b.writeOps += to.writeOps - from.writeOps
		b.ioOK = true
	}
	if from.faultsOK && to.faultsOK {
		b.minorFaults += to.minorFaults - from.minorFaults
		b.majorFaults += to.majorFaults - from.majorFaults
		b.faultsOK = true
	}
	if from.ctxOK && to.ctxOK {
		b.volCS += to.volCS - from.volCS
		b.involCS += to.involCS - from.involCS
		b.ctxOK = true
	}
}

type snapshot struct {
	// pid is which process the reading came from, 0 for a tick that found
	// none. Every cumulative counter below is meaningless across a change of
	// it, which is what makes it worth recording.
	pid int32

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
	return NewSamplerFunc(func() int { return pid }, interval)
}

// NewSamplerFunc attaches to whatever process pid returns, asking again on
// every tick.
//
// A session that restarts its engine mid-measurement is the reason this
// exists. An adapter that reloads by stopping its process, running a loader,
// and starting a new one would otherwise be measured through a pid resolved
// before any of that happened: a process that is about to die, or — when the
// session has not started one yet and pid is 0 — the kernel, which reports
// plausible-looking numbers belonging to nothing the run is about. Re-reading
// the pid means the sampler follows the session, and the intervals when the
// session had no process at all contribute no reads rather than zeroes.
func NewSamplerFunc(pid func() int, interval time.Duration) *Sampler {
	if interval <= 0 {
		interval = 5 * time.Millisecond
	}
	if pid == nil {
		pid = func() int { return 0 }
	}
	return &Sampler{pid: pid, interval: interval}
}

// attach resolves the current pid. A nonpositive pid is nothing to watch: pid
// 0 is the kernel on the platforms this runs on, and sampling it would put a
// number in the report that no engine produced.
func (s *Sampler) attach() *process.Process {
	id := s.pid()
	if id <= 0 {
		return nil
	}
	if s.proc != nil && s.proc.Pid == int32(id) {
		return s.proc
	}
	p, err := process.NewProcess(int32(id))
	if err != nil {
		return nil
	}
	s.proc = p
	return p
}

// Self attaches to the harness's own process, which is what an in-process
// adapter is measured through.
func Self(interval time.Duration) *Sampler { return NewSampler(os.Getpid(), interval) }

// Start begins polling. Every Start must be matched by a Stop.
//
// Polling starts even when there is no process to watch yet: an adapter whose
// Load starts the engine has none at this point, and refusing to poll would
// lose the whole load. A tick that finds nothing records nothing.
func (s *Sampler) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})

	s.mu.Lock()
	// The process alive at this instant, if there is one, is measured from
	// what it has already spent: it was not born here, and the window is not
	// entitled to its earlier life. A process that appears later is banked
	// from zero instead, which is what rebase does.
	s.first = s.read()
	s.last, s.peak, s.have = s.first, s.first, s.first.cpuOK || s.first.memOK
	s.reads = 0
	if s.first.pid != 0 {
		s.reads = 1
	}
	s.bank = banked{}
	s.memBase, s.memLast = snapshot{}, snapshot{}
	if s.first.memOK {
		s.memBase, s.memLast = s.first, s.first
	}
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
				s.observe(snap)
				s.mu.Unlock()
			}
		}
	}()
}

// observe folds one reading in. The caller holds s.mu.
func (s *Sampler) observe(snap snapshot) {
	if snap.pid == 0 {
		// The session owns no process at this instant: it is between a loader
		// and the shell that replaces it, or it has shut down for good. The
		// reading is dropped rather than recorded, because letting an empty
		// snapshot become s.last would hand the outgoing process's account a
		// closing balance of nothing and bank its whole contribution as zero.
		return
	}
	if snap.pid != s.first.pid {
		// Either the session replaced its process or it started one after the
		// window opened. Both mean the process now being watched was born
		// inside the window, so s.first describes something that is not it.
		s.rebase(snap)
	}
	snap = carry(s.last, snap)
	s.last = snap
	s.peak = maxOf(s.peak, snap)
	s.reads++
	s.have = s.have || snap.cpuOK || snap.memOK
	if snap.memOK {
		if !s.memBase.memOK {
			s.memBase = snap
		}
		s.memLast = snap
	}
}

// carry replaces what a reading failed to get with what the previous reading
// of the same process did get.
//
// Two failures need it, and only one of them announces itself.
//
// The first is the tick that catches a process on its way out. It still finds
// the pid — the entry is there until it is reaped — but the counters behind it
// have gone, so the reading comes back with a pid and an error for everything
// else.
//
// The second is a reading that fails and says it succeeded. gopsutil's Darwin
// path ignores the return code of proc_pidinfo and reports whatever is in the
// struct, so a call that did not fill it in yields a confident zero. That is
// indistinguishable from an idle process in a single reading, but not across
// two: every counter here is cumulative over a process's life and cannot
// decrease while it is alive, so a value below the last one is a failed read
// however cheerfully it was reported.
//
// Either one, left alone, becomes the process's closing balance, and since a
// group is banked only when both of its ends were readable — and a fall to
// zero clamps the difference to nothing — the whole of its work is dropped.
// That is what made the same 100k-node load report 470 ms of CPU three times
// and 0.5 ms the fourth, depending on where the bad tick fell.
//
// Memory is exempt from the monotonic rule and only fills in on an outright
// error: an RSS that falls is a process that freed something, which is a
// measurement rather than a fault.
func carry(prev, snap snapshot) snapshot {
	if prev.pid != snap.pid {
		return snap
	}
	if prev.cpuOK && (!snap.cpuOK || snap.cpuUser < prev.cpuUser || snap.cpuSys < prev.cpuSys) {
		snap.cpuUser, snap.cpuSys, snap.cpuOK = prev.cpuUser, prev.cpuSys, true
	}
	if !snap.memOK && prev.memOK {
		snap.rss, snap.vms, snap.swap = prev.rss, prev.vms, prev.swap
		snap.threads, snap.memOK = prev.threads, true
	}
	if prev.ioOK && (!snap.ioOK || snap.readBytes < prev.readBytes || snap.writeBytes < prev.writeBytes ||
		snap.readOps < prev.readOps || snap.writeOps < prev.writeOps) {
		snap.readBytes, snap.writeBytes = prev.readBytes, prev.writeBytes
		snap.readOps, snap.writeOps, snap.ioOK = prev.readOps, prev.writeOps, true
	}
	if prev.faultsOK && (!snap.faultsOK || snap.minorFaults < prev.minorFaults || snap.majorFaults < prev.majorFaults) {
		snap.minorFaults, snap.majorFaults, snap.faultsOK = prev.minorFaults, prev.majorFaults, true
	}
	if prev.ctxOK && (!snap.ctxOK || snap.volCS < prev.volCS || snap.involCS < prev.involCS) {
		snap.volCS, snap.involCS, snap.ctxOK = prev.volCS, prev.involCS, true
	}
	return snap
}

// rebase closes the outgoing process's account and opens one for snap's.
//
// Banking rather than switching baselines is what makes a window that spans a
// restart add up. The work is spread over however many processes the session
// owned, and the interesting one is often neither the first nor the last: a
// bulk load stops the shell, converts in a child, and starts a fresh shell, so
// a delta taken between the endpoints would report the new shell's first few
// milliseconds and lose the load entirely.
//
// The two kinds of counter part company here. CPU, I/O, faults and context
// switches are cumulative over a process's life, so a process seen for the
// first time inside the window is measured from zero — subtracting the dead
// process's totals would produce a negative. Memory is not cumulative: it is
// tracked separately by observe, and the peak carries across untouched,
// because the peak belongs to the interval rather than to any one process
// in it.
func (s *Sampler) rebase(snap snapshot) {
	s.bank.add(s.first, s.last)
	s.first = snapshot{
		pid:      snap.pid,
		cpuOK:    true,
		ioOK:     true,
		faultsOK: true,
		ctxOK:    true,
	}
}

// Stop ends polling, takes a final reading, and returns the delta over the
// whole watched interval.
func (s *Sampler) Stop() ProcessDelta {
	if s.cancel != nil {
		s.cancel()
		<-s.done
		s.cancel = nil
	}
	final := s.read()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.observe(final)
	if !s.have {
		// Nothing was ever readable: no process, or none this user may
		// inspect. An empty delta reports every group as unavailable, which is
		// the difference between "we did not measure" and "we measured zero".
		return ProcessDelta{}
	}

	// The process still alive at the end has not been banked yet. Adding it
	// makes total the sum over every process the window saw, which for the
	// common case of one process that outlives the window is just its delta.
	total := s.bank
	total.add(s.first, s.last)

	d := ProcessDelta{Samples: s.reads}
	if total.cpuOK {
		d.CPUUser, d.CPUSys = total.cpuUser, total.cpuSys
		d.CPUOK = true
	}
	if s.memBase.memOK {
		d.RSSStart, d.RSSEnd, d.RSSPeak = s.memBase.rss, s.memLast.rss, s.peak.rss
		d.VMSPeak, d.SwapPeak, d.NumThread = s.peak.vms, s.peak.swap, s.peak.threads
		d.MemoryOK = true
	}
	if total.ioOK {
		d.ReadBytes, d.WriteBytes = total.readBytes, total.writeBytes
		d.IOOK = true
		if ioOpsReported {
			d.ReadOps, d.WriteOps = total.readOps, total.writeOps
			d.IOOpsOK = true
		}
	}
	if total.faultsOK {
		d.MinorFaults, d.MajorFaults = total.minorFaults, total.majorFaults
		d.FaultsOK = true
	}
	if total.ctxOK {
		d.VoluntaryCS, d.InvoluntaryCS = total.volCS, total.involCS
		d.CtxOK = true
	}
	return d
}

func (s *Sampler) read() snapshot {
	var snap snapshot
	p := s.attach()
	if p == nil {
		return snap
	}
	snap.pid = p.Pid
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
