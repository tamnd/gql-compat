package metrics

import (
	"testing"
	"time"
)

// cpuSnap is a reading that found everything it looked for.
func cpuSnap(pid int32, cpu time.Duration) snapshot {
	return snapshot{pid: pid, cpuUser: cpu, cpuOK: true, rss: 1 << 20, memOK: true}
}

func TestAWindowSpanningARestartIsTheSumOfItsProcesses(t *testing.T) {
	// A shell that had already run, a loader that does the work, and a fresh
	// shell that has done nothing: the ingest measurement of every adapter
	// that reloads by replacing its process.
	s := NewSamplerFunc(func() int { return 0 }, time.Hour)
	s.first = cpuSnap(1, 5*time.Second) // the old shell, mid-life
	s.last, s.peak, s.have = s.first, s.first, true
	s.memBase, s.memLast, s.reads = s.first, s.first, 1

	s.observe(cpuSnap(1, 5*time.Second+20*time.Millisecond)) // it ticks over
	s.observe(cpuSnap(2, 100*time.Millisecond))              // the loader appears
	s.observe(cpuSnap(2, 900*time.Millisecond))              // and does the work
	s.observe(snapshot{})                                    // the gap between the two
	s.observe(cpuSnap(3, 2*time.Millisecond))                // the new shell

	total := s.bank
	total.add(s.first, s.last)
	// 20ms from the old shell, 900ms from the loader — which is measured from
	// zero because it was born inside the window — and 2ms from the new one.
	if want := 922 * time.Millisecond; total.cpuUser != want {
		t.Errorf("CPU over the window was %v, want %v", total.cpuUser, want)
	}
}

func TestAProcessCaughtMidExitKeepsTheCountersItLastReported(t *testing.T) {
	// The tick that lands as a process is being reaped finds the pid and
	// nothing behind it. Treating that as the closing balance discards the
	// process's whole contribution, because a group is only banked when both
	// of its ends were readable.
	s := NewSamplerFunc(func() int { return 0 }, time.Hour)
	s.first = snapshot{pid: 7, cpuOK: true}
	s.last, s.have, s.reads = s.first, true, 1

	s.observe(cpuSnap(7, 400*time.Millisecond))
	s.observe(snapshot{pid: 7}) // alive enough to have a pid, not enough to read
	s.observe(cpuSnap(8, 0))    // the replacement

	if got := s.bank.cpuUser; got != 400*time.Millisecond {
		t.Errorf("banked %v for the departed process, want 400ms", got)
	}
}

func TestACounterThatGoesBackwardsIsAFailedReadAndNotAMeasurement(t *testing.T) {
	// gopsutil's Darwin path ignores the return code of proc_pidinfo, so a
	// call that fails comes back as a successful reading of zero. Believing it
	// costs the whole process's account, because the difference from its
	// baseline then clamps to nothing.
	s := NewSamplerFunc(func() int { return 0 }, time.Hour)
	s.first = snapshot{pid: 7, cpuOK: true}
	s.last, s.have, s.reads = s.first, true, 1

	s.observe(cpuSnap(7, 300*time.Millisecond))
	s.observe(snapshot{pid: 7, cpuOK: true}) // the confident zero
	s.observe(cpuSnap(8, 0))

	if got := s.bank.cpuUser; got != 300*time.Millisecond {
		t.Errorf("banked %v, want the 300ms the process had actually spent", got)
	}
}
