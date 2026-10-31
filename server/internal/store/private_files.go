package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Prepare before SQLite opens the file, so WAL/SHM inherit a private mode.
// Do not follow the final component or chmod an unrelated existing directory.
func privateDatabaseFile(path string, create bool) error {
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		if !create && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("database target must be a regular file")
	}
	if err == nil {
		err = f.Chmod(0600)
	}
	return errors.Join(err, f.Close())
}

func privateSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := privateDatabaseFile(path+suffix, false); err != nil {
			return err
		}
	}
	return nil
}
