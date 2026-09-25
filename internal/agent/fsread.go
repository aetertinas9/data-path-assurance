package agent

import (
	"errors"
	"io"
	"io/fs"
	"os"
)

// readStatus is the outcome of reading one file or attribute (GFO-045).
type readStatus int

const (
	readOK readStatus = iota
	readMissing
	readPermission
	readUnreadable
	readMalformed
	readNoData
)

// attrCode returns the diagnostic code of a failed or empty attribute read.
func (s readStatus) attrCode() string {
	switch s {
	case readMissing:
		return codeAttrMissing
	case readPermission:
		return codeAttrPermission
	case readMalformed:
		return codeAttrMalformed
	case readNoData:
		return codeAttrNoData
	default:
		return codeAttrUnreadable
	}
}

// statusOf classifies a filesystem error: absence, permission (EACCES or
// EPERM), or anything else, which includes a root boundary violation.
func statusOf(err error) readStatus {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return readMissing
	case errors.Is(err, fs.ErrPermission):
		return readPermission
	default:
		return readUnreadable
	}
}

// readRegularFile reads at most limit+1 bytes of the regular file name inside
// root, following symlinks only within root. Anything but a regular file is
// unreadable and is never opened, so a FIFO or socket cannot block the read; a
// longer result tells the caller the file exceeds limit.
func readRegularFile(root *os.Root, name string, limit int) ([]byte, readStatus) {
	info, err := root.Stat(name)
	if err != nil {
		return nil, statusOf(err)
	}
	if !info.Mode().IsRegular() {
		return nil, readUnreadable
	}
	f, err := root.OpenFile(name, os.O_RDONLY|openFileFlags, 0)
	if err != nil {
		return nil, statusOf(err)
	}
	data, status := readOpenedRegular(f, limit)
	// The descriptor was opened read-only, so a close failure loses nothing.
	_ = f.Close()
	return data, status
}

// readOpenedRegular rechecks that f is regular, closing the window between the
// stat and the open, and reads at most limit+1 bytes from it.
func readOpenedRegular(f *os.File, limit int) ([]byte, readStatus) {
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, readUnreadable
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, statusOf(err)
	}
	return data, readOK
}

// listDirNames returns the names in the directory name inside root. It stops
// reading once more than limit names have been seen, returning limit+1 names so
// the caller can report the bound. ok is false when the path is not a readable
// directory inside root.
func listDirNames(root *os.Root, name string, limit int) (names []string, ok bool) {
	info, err := root.Stat(name)
	if err != nil || !info.IsDir() {
		return nil, false
	}
	dir, err := root.OpenFile(name, os.O_RDONLY|openDirFlags, 0)
	if err != nil {
		return nil, false
	}
	// A read-only directory handle loses nothing on a failed close.
	defer func() { _ = dir.Close() }()
	for len(names) <= limit {
		batch, err := dir.Readdirnames(256)
		names = append(names, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false
		}
	}
	if len(names) > limit {
		names = names[:limit+1]
	}
	return names, true
}
