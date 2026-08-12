//go:build !darwin

package metrics

// ioOpsReported is true everywhere else. Linux gives syscr and syscw in
// /proc/<pid>/io alongside the byte counts, and Windows reports both through
// IO_COUNTERS, so where the byte figures are available the operation figures
// are too. A platform that turns out not to follow that gets its own file
// rather than a runtime guess.
const ioOpsReported = true
