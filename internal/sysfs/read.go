package sysfs

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Bounds of a single read.
const (
	MaxAttrBytes   = 4096
	MaxConfigBytes = 4096
	maxLinkTarget  = 4096
	maxDirNames    = 16384
)

// Status is the outcome of reading one file.
type Status int

const (
	OK Status = iota
	Missing
	Permission
	Unreadable
	Malformed
	NoData
)

func (s Status) String() string {
	switch s {
	case OK:
		return "ok"
	case Missing:
		return "missing"
	case Permission:
		return "permission"
	case Malformed:
		return "malformed"
	case NoData:
		return "nodata"
	default:
		return "unreadable"
	}
}

func statusOf(err error) Status {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Missing
	case errors.Is(err, fs.ErrPermission):
		return Permission
	default:
		return Unreadable
	}
}

// readFile reads at most limit+1 bytes of the regular file name inside root,
// following symlinks only within root. Anything but a regular file is never
// opened, so a FIFO or device cannot block the read.
func readFile(root *os.Root, name string, limit int) ([]byte, Status) {
	info, err := root.Stat(name)
	if err != nil {
		return nil, statusOf(err)
	}
	if !info.Mode().IsRegular() {
		return nil, Unreadable
	}
	f, err := root.OpenFile(name, os.O_RDONLY|openFileFlags, 0)
	if err != nil {
		return nil, statusOf(err)
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, Unreadable
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		// sysfs reports EINVAL for some attributes of a device that is down
		// (net speed); that is "no data", not a broken file.
		if errors.Is(err, os.ErrInvalid) || strings.Contains(err.Error(), "invalid argument") {
			return nil, NoData
		}
		return nil, statusOf(err)
	}
	return data, OK
}

// readAttr reads one attribute: a regular file of at most MaxAttrBytes with
// surrounding blanks trimmed. An empty result is NoData.
func readAttr(root *os.Root, name string) (string, Status) {
	data, status := readFile(root, name, MaxAttrBytes)
	if status != OK {
		return "", status
	}
	if len(data) > MaxAttrBytes || bytes.IndexByte(data, 0) >= 0 {
		return "", Malformed
	}
	s := strings.Trim(string(data), " \t\r\n")
	if s == "" {
		return "", NoData
	}
	return s, OK
}

// listDir returns the sorted names in directory name inside root.
func listDir(root *os.Root, name string) ([]string, bool) {
	info, err := root.Stat(name)
	if err != nil || !info.IsDir() {
		return nil, false
	}
	dir, err := root.OpenFile(name, os.O_RDONLY|openDirFlags, 0)
	if err != nil {
		return nil, false
	}
	defer func() { _ = dir.Close() }()
	var names []string
	for len(names) <= maxDirNames {
		batch, err := dir.Readdirnames(256)
		names = append(names, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false
		}
	}
	if len(names) > maxDirNames {
		names = names[:maxDirNames]
	}
	return names, true
}

// readLinkBase returns the base name of the symlink target of name, after
// checking that the link stays inside root lexically. It never follows the
// link.
func readLinkBase(root *os.Root, name string) (string, bool) {
	info, err := root.Lstat(name)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return "", false
	}
	target, err := root.Readlink(name)
	if err != nil || target == "" || len(target) > maxLinkTarget || strings.HasPrefix(target, "/") {
		return "", false
	}
	rel := path.Clean(path.Dir(name) + "/" + target)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return path.Base(rel), true
}

var (
	bdfPattern        = regexp.MustCompile(`^[0-9a-f]{4,8}:[0-9a-f]{2}:[01][0-9a-f]\.[0-7]$`)
	hostBridgePattern = regexp.MustCompile(`^pci[0-9a-f]{4,8}:[0-9a-f]{2}$`)
	namePattern       = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// IsBDF reports whether s is a canonical lowercase PCI address.
func IsBDF(s string) bool { return bdfPattern.MatchString(s) }

func parseHex(s string) (uint32, bool) {
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	if s == "" || len(s) > 8 {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	return uint32(v), err == nil
}

func parseInt(s string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v, err == nil
}
