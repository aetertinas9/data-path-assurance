//go:build cgo

package tests_test

// ReadFile through the real bridge (NPO-034, NPO-042, NPO-044).

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/nativepcie"
)

func npoTempFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "field")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func npoOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestNPO044_ReadFileRegularContentAndOffset(t *testing.T) {
	npoRequireBackend(t)
	f := npoOpen(t, npoTempFile(t, []byte("hello world")))
	if _, err := f.Seek(6, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := nativepcie.ReadFile(f)
	if err != nil {
		t.Fatalf("NPO-044: ReadFile error %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("NPO-044: ReadFile = %q, want full content from offset zero without NUL", got)
	}
	if off, err := f.Seek(0, io.SeekCurrent); err != nil || off != 6 {
		t.Errorf("NPO-044: file offset after ReadFile = %d (%v), want 6 preserved", off, err)
	}
	// NPO-034: the caller's file is still open and usable afterwards.
	if _, err := f.Stat(); err != nil {
		t.Errorf("NPO-034: file unusable after ReadFile: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Errorf("NPO-034: seek after ReadFile: %v", err)
	}
	rest, err := io.ReadAll(f)
	if err != nil || string(rest) != "hello world" {
		t.Errorf("NPO-034: caller read after ReadFile = %q, %v", rest, err)
	}
}

func TestNPO044_ReadFileEmptyAndBinary(t *testing.T) {
	npoRequireBackend(t)
	got, err := nativepcie.ReadFile(npoOpen(t, npoTempFile(t, nil)))
	if err != nil || len(got) != 0 {
		t.Errorf("NPO-044: ReadFile(empty) = %q, %v; want empty, nil", got, err)
	}
	bin := []byte("a\x00b\x00")
	got, err = nativepcie.ReadFile(npoOpen(t, npoTempFile(t, bin)))
	if err != nil || !bytes.Equal(got, bin) {
		t.Errorf("NPO-044: ReadFile(binary with NULs) = %q, %v; want %q (length, not strlen)", got, err, bin)
	}
	// Result is Go-owned: a second call must not disturb the first result.
	first, _ := nativepcie.ReadFile(npoOpen(t, npoTempFile(t, []byte("first"))))
	_, _ = nativepcie.ReadFile(npoOpen(t, npoTempFile(t, []byte("second"))))
	if string(first) != "first" {
		t.Errorf("NPO-006: earlier ReadFile result changed after a later call: %q", first)
	}
}

func TestNPO044_ReadFileExactLimitAndOversized(t *testing.T) {
	npoRequireBackend(t)
	exact := bytes.Repeat([]byte{'q'}, npoTextLimit)
	exact[0], exact[npoTextLimit-1] = 'a', 'b'
	got, err := nativepcie.ReadFile(npoOpen(t, npoTempFile(t, exact)))
	if err != nil || !bytes.Equal(got, exact) {
		t.Errorf("NPO-044: ReadFile(16384 bytes) len=%d err=%v; want exact content, nil", len(got), err)
	}
	over := bytes.Repeat([]byte{'q'}, npoTextLimit+1)
	got, err = nativepcie.ReadFile(npoOpen(t, npoTempFile(t, over)))
	if !errors.Is(err, nativepcie.ErrTooLarge) || len(got) != 0 {
		t.Errorf("NPO-044: ReadFile(16385 bytes) len=%d err=%v; want empty, ErrTooLarge", len(got), err)
	}
	big := bytes.Repeat([]byte{'q'}, 4*npoTextLimit)
	got, err = nativepcie.ReadFile(npoOpen(t, npoTempFile(t, big)))
	if !errors.Is(err, nativepcie.ErrTooLarge) || len(got) != 0 {
		t.Errorf("NPO-044: ReadFile(65536 bytes) len=%d err=%v; want empty, ErrTooLarge", len(got), err)
	}
}

func TestNPO044_ReadFileNilAndClosed(t *testing.T) {
	npoRequireBackend(t)
	var got []byte
	var err error
	npoNoPanic(t, "NPO-042", func() { got, err = nativepcie.ReadFile(nil) })
	if !errors.Is(err, nativepcie.ErrInvalid) || len(got) != 0 {
		t.Errorf("NPO-044: ReadFile(nil) = %q, %v; want empty, ErrInvalid", got, err)
	}
	if errors.Is(err, nativepcie.ErrIO) {
		t.Errorf("NPO-044: ReadFile(nil) must not match ErrIO")
	}

	f, err := os.Open(npoTempFile(t, []byte("data")))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	npoNoPanic(t, "NPO-042", func() { got, err = nativepcie.ReadFile(f) })
	if !errors.Is(err, nativepcie.ErrIO) {
		t.Errorf("NPO-044: ReadFile(closed) error %v does not match ErrIO", err)
	}
	if !errors.Is(err, fs.ErrClosed) {
		t.Errorf("NPO-044: ReadFile(closed) error %v does not match fs.ErrClosed", err)
	}
	if len(got) != 0 {
		t.Errorf("NPO-044: ReadFile(closed) returned %q, want empty", got)
	}
}

func TestNPO042_ReadFilePreservesErrno(t *testing.T) {
	npoRequireBackend(t)
	path := npoTempFile(t, []byte("data"))
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got, err := nativepcie.ReadFile(f)
	if !errors.Is(err, nativepcie.ErrIO) || len(got) != 0 {
		t.Fatalf("NPO-044: ReadFile(write-only) = %q, %v; want empty, ErrIO", got, err)
	}
	if !errors.Is(err, syscall.EBADF) {
		t.Errorf("NPO-042: ReadFile(write-only) error %v does not match syscall.EBADF", err)
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) || errno != syscall.EBADF {
		t.Errorf("NPO-042: ReadFile(write-only) error %v does not carry syscall.Errno EBADF", err)
	}
}

func TestNPO044_ReadFileNonregularIsIO(t *testing.T) {
	npoRequireBackend(t)
	const limit = 3 * time.Second

	// Directory.
	dir := npoOpen(t, t.TempDir())
	var got []byte
	var err error
	if npoWithin(t, "NPO-044", limit, func() { got, err = nativepcie.ReadFile(dir) }) {
		if !errors.Is(err, nativepcie.ErrIO) || len(got) != 0 {
			t.Errorf("NPO-044: ReadFile(directory) = %q, %v; want empty, ErrIO", got, err)
		}
	}

	// FIFO with a writer attached but no data: must not block.
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	reader, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	writer, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	if npoWithin(t, "NPO-044", limit, func() { got, err = nativepcie.ReadFile(reader) }) {
		if !errors.Is(err, nativepcie.ErrIO) || len(got) != 0 {
			t.Errorf("NPO-044: ReadFile(FIFO) = %q, %v; want empty, ErrIO", got, err)
		}
		if errors.Is(err, nativepcie.ErrNoData) || errors.Is(err, nativepcie.ErrTooLarge) {
			t.Errorf("NPO-044: ReadFile(FIFO) mapped to a parser sentinel %v instead of ErrIO", err)
		}
	}

	// Device streams: /dev/zero would be TOO_LARGE and /dev/null would be OK
	// if the file type were ignored; both must be ErrIO.
	for _, dev := range []string{"/dev/zero", "/dev/null"} {
		f, err := os.Open(dev)
		if err != nil {
			t.Logf("NPO-044: %s unavailable (%v); skipping", dev, err)
			continue
		}
		if npoWithin(t, "NPO-044", limit, func() { got, err = nativepcie.ReadFile(f) }) {
			if !errors.Is(err, nativepcie.ErrIO) || len(got) != 0 {
				t.Errorf("NPO-044: ReadFile(%s) = %d bytes, %v; want empty, ErrIO", dev, len(got), err)
			}
		}
		_ = f.Close()
	}
}
