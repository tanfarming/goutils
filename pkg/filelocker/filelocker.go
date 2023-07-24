package filelocker

import "os"

func Lock(f *os.File) error {
	return lock(f, writeLock)
}

func RLock(f *os.File) error {
	return lock(f, readLock)
}

func Unlock(f *os.File) error {
	return unlock(f)
}
