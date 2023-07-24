//go:build windows

package filelocker

import (
	"io/fs"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

type lockType uint32

const (
	readLock  lockType = 0
	writeLock lockType = windows.LOCKFILE_EXCLUSIVE_LOCK
)

const (
	reserved = 0
	allBytes = ^uint32(0)
)

func lock(f *os.File, lt lockType) error {

	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 0, 0, nil)
	if err != nil {
		return &fs.PathError{
			Op:   strconv.FormatUint(uint64(lt), 10),
			Path: f.Name(),
			Err:  err,
		}
	}
	return nil
}

func unlock(f *os.File) error {
	err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 0, 0, nil)
	if err != nil {
		return &fs.PathError{
			Op:   "Unlock",
			Path: f.Name(),
			Err:  err,
		}
	}
	return nil
}
