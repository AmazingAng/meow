package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReadOptional reads a file; a missing file yields nil and exists=false.
func ReadOptional(path string) (data []byte, exists bool, err error) {
	data, err = os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

// WriteFile replaces path with data, first saving the previous content as
// path.bak.<timestamp>. Symlinks are followed, so a settings file linked
// from a dotfiles repo stays a link. It returns the backup path, if any.
func WriteFile(path string, data []byte, now time.Time) (backup string, err error) {
	target, mode := path, os.FileMode(0o644)
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, err = filepath.EvalSymlinks(path); err != nil {
				return "", err
			}
			if fi, err = os.Stat(target); err != nil {
				return "", err
			}
		}
		mode = fi.Mode().Perm()
		old, err := os.ReadFile(target)
		if err != nil {
			return "", err
		}
		// O_EXCL: never overwrite an earlier backup, even within the same second.
		base := target + ".bak." + now.Format("20060102-150405")
		for i := 0; ; i++ {
			backup = base
			if i > 0 {
				backup = fmt.Sprintf("%s-%d", base, i)
			}
			f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if errors.Is(err, os.ErrExist) && i < 100 {
				continue
			}
			if err != nil {
				return "", err
			}
			_, err = f.Write(old)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return "", err
			}
			break
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	tmp := target + ".meow-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return backup, nil
}

// RemoveFile deletes path and then its directory if that is now empty.
func RemoveFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.Remove(filepath.Dir(path)) // fails, harmlessly, if not empty
	return nil
}

// ShellQuote quotes s as one word for a POSIX shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
