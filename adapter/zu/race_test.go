//go:build race

package zu

// raceDetector says the binary was built with -race, which multiplies the
// constant factor of every memory access without changing the complexity of
// anything. The staging budget is a complexity check, so it widens here rather
// than the test being skipped: a quadratic planner is still quadratic under the
// detector and would still blow a budget ten times the size.
const raceDetector = true
