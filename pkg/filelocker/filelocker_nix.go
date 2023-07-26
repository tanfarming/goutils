//go:build darwin || freebsd || linux || penbsd

package filelocker

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

type lockType int16

const (
	readLock  lockType = syscall.LOCK_SH
	writeLock lockType = syscall.LOCK_EX
)

func lock(f *os.File, lt lockType) (err error) {
	for {
		err = syscall.Flock(int(f.Fd()), int(lt))
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		return &fs.PathError{
			Op:   fmt.Sprintf("%v", lt),
			Path: f.Name(),
			Err:  err,
		}
	}
	return nil
}

func unlock(f *os.File) error {
	return lock(f, syscall.LOCK_UN)
}
