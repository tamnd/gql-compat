//go:build unix

package metrics

import (
	"io/fs"
	"syscall"
)

// allocatedBytes returns the space the filesystem actually charges a file,
// which is the 512-byte block count stat reports rather than the length the
// file claims. A hole in a sparse segment file costs nothing and should not
// appear in a storage comparison as though it did.
func allocatedBytes(info fs.FileInfo) int64 {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.Size()
	}
	return st.Blocks * 512
}
