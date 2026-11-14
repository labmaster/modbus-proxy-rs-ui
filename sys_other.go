//go:build !linux

package main

// Stubs so the program also builds on Windows/macOS for testing.
// In production only the Linux variant (sys_linux.go) is used.

import (
	"errors"
	"io/fs"
	"os"
)

func diskUsage(path string) (total, free, avail uint64, err error) {
	return 0, 0, 0, errors.New("only available on Linux")
}

func allocatedBytes(fi fs.FileInfo) int64 { return fi.Size() }

func fileOwner(fi fs.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }

func openNoFollow(path string) (*os.File, error) { return os.Open(path) }
