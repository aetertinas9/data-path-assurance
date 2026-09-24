package tests_test

// Shared helpers and compile-time surface checks for the native-pcie-observer
// acceptance tests (spec v1.0). Built with and without cgo. All helpers carry
// the npo prefix to avoid colliding with other tests_test helpers.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/nativepcie"
)

// NPO-040 / NPO-050: the public surface must exist with exactly these
// signatures and field types. Typed constants make a field with a different
// type a compile error.
var (
	_ func() bool                                                  = nativepcie.Available
	_ func() uint32                                                = nativepcie.ABIVersion
	_ func(string) (nativepcie.BDF, error)                         = nativepcie.ParseBDF
	_ func([]byte) (uint32, error)                                 = nativepcie.ParseWidth
	_ func([]byte) (uint32, error)                                 = nativepcie.ParseSpeed
	_ func([]byte) (int32, error)                                  = nativepcie.ParseNUMA
	_ func([]byte) ([]nativepcie.AERCounter, error)                = nativepcie.ParseAER
	_ func(*os.File) ([]byte, error)                               = nativepcie.ReadFile
	_ func(*os.Root, string) (nativepcie.DeviceObservation, error) = nativepcie.ReadDevice

	_ = nativepcie.BDF{Domain: uint16(0), Bus: uint8(0), Device: uint8(0), Function: uint8(0)}
	_ = nativepcie.AERCounter{Name: string(""), Count: uint64(0)}
	_ = nativepcie.DeviceObservation{BDF: string(""), Fields: []nativepcie.FieldObservation(nil)}
	_ = nativepcie.FieldObservation{
		Name:     string(""),
		Status:   nativepcie.FieldStatus(nativepcie.FieldObservation{}.Status),
		Value:    uint32(0),
		NUMA:     int32(0),
		Counters: []nativepcie.AERCounter(nil),
		Err:      error(nil),
	}
	_ fmt.Stringer = &nativepcie.BDF{}

	npoSentinels = []error{
		nativepcie.ErrInvalid,
		nativepcie.ErrRange,
		nativepcie.ErrNoData,
		nativepcie.ErrTooLarge,
		nativepcie.ErrIO,
		nativepcie.ErrUnavailable,
	}
)

func npoOpenRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("fixture: OpenRoot(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// npoRepoRoot returns the absolute repository root (tests run inside tests/).
func npoRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("cannot resolve repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	return root
}

// npoNoPanic runs fn and reports a failure when it panics (NPO-042).
func npoNoPanic(t *testing.T, clause string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: call panicked: %v", clause, r)
		}
	}()
	fn()
}

// npoEmptyObservation reports whether obs is the empty observation.
func npoEmptyObservation(obs nativepcie.DeviceObservation) bool {
	return obs.BDF == "" && len(obs.Fields) == 0
}
