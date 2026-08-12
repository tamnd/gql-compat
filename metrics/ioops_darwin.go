package metrics

// ioOpsReported is false here because macOS does not report them. The bytes
// come from proc_pid_rusage, whose rusage_info structure carries
// ri_diskio_bytesread and ri_diskio_byteswritten and no count of the calls
// that moved them; gopsutil fills the byte fields and leaves the operation
// fields at their zero value. Publishing that zero would be a measurement of
// nothing, so the report says the figure was unavailable instead.
const ioOpsReported = false
