//go:build !unix

package metrics

import "io/fs"

// allocatedBytes falls back to the apparent size on platforms with no block
// count in stat. The report prints allocated and apparent side by side, so a
// reader can see they are equal here and draw the right conclusion about
// what the number means.
func allocatedBytes(info fs.FileInfo) int64 { return info.Size() }
