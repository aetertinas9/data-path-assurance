package offline

import (
	"errors"
	"io"
	"os"
)

// The input size bounds.
const (
	MaxArtifactBytes = 64 << 20 // 67108864
	MaxFleetBytes    = 1 << 20  // 1048576
)

// errNotReadable and errTooLarge classify readBounded failures; callers turn
// them into input errors of their own class.
var (
	errNotReadable = errors.New("not a readable regular file")
	errTooLarge    = errors.New("larger than the bound")
)

// readBounded reads the regular file at path (symlinks followed) and returns
// its content. A path that is absent, unreadable or not a regular file
// (directory, FIFO, socket, device) is errNotReadable; such a file is never
// waited on. At most limit+1 bytes are read; a larger file is errTooLarge.
func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errNotReadable
	}
	f, err := os.OpenFile(path, os.O_RDONLY|nonBlockingOpenFlag, 0)
	if err != nil {
		return nil, errNotReadable
	}
	defer func() { _ = f.Close() }()
	// The path may have been replaced between the stat and the open; judge
	// the opened file itself.
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errNotReadable
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, errNotReadable
	}
	if int64(len(data)) > limit {
		return nil, errTooLarge
	}
	return data, nil
}
