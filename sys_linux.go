//go:build linux

package main

import (
	"io/fs"
	"os"
	"syscall"
)

func diskUsage(path string) (total, free, avail uint64, err error) {
	var st syscall.Statfs_t
	if err = syscall.Statfs(path, &st); err != nil {
		return
	}
	bs := uint64(st.Bsize)
	return uint64(st.Blocks) * bs, uint64(st.Bfree) * bs, uint64(st.Bavail) * bs, nil
}

func allocatedBytes(fi fs.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int64(st.Blocks) * 512
	}
	return fi.Size()
}

func fileOwner(fi fs.FileInfo) (uid, gid int, ok bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid), true
	}
	return 0, 0, false
}

// openNoFollow opens a file without following a symlink and without blocking
// on a FIFO. Protects root when reading from the spool.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
