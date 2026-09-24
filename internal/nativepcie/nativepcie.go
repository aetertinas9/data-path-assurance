// Package nativepcie is the Go bridge to libdpa_pcie, a bounded, read-only
// native library that parses PCIe sysfs attribute text and reads bounded
// descriptor content. It is an adapter at the edge of the application: the
// domain core never imports it, and nothing here converts observations into
// readiness, health, finding, or scheduling decisions.
//
// The native backend is linked through cgo only on supported targets and only
// after `make native` has produced the target-specific static archive and the
// generated build stamp. On every other build (any other platform, or
// CGO_ENABLED=0) the package compiles to an unavailable stub: Available
// reports false and every fallible operation returns ErrUnavailable.
//
// Every operation validates or reads caller-supplied data only. C never opens
// a path; ReadDevice resolves every path through the caller's *os.Root.
package nativepcie

import (
	"errors"
	"fmt"
	"os"
)

// MaxTextLen is the largest text input, in bytes, accepted by the parsers and
// the largest content ReadFile returns.
const MaxTextLen = 16384

// Sentinel errors. Errors returned by this package wrap exactly one of them
// (plus, for I/O failures, the OS cause) so callers classify with errors.Is.
var (
	// ErrInvalid reports malformed input or an invalid argument.
	ErrInvalid = errors.New("nativepcie: invalid input")
	// ErrRange reports syntactically valid input whose value is out of range.
	ErrRange = errors.New("nativepcie: value out of range")
	// ErrNoData reports input that carries no observation (empty, Unknown, ...).
	ErrNoData = errors.New("nativepcie: no data")
	// ErrTooLarge reports input or content beyond the bounded limits.
	ErrTooLarge = errors.New("nativepcie: input too large")
	// ErrIO reports a descriptor or file failure; the OS cause is wrapped too.
	ErrIO = errors.New("nativepcie: i/o failure")
	// ErrUnavailable reports that no usable native backend is linked.
	ErrUnavailable = errors.New("nativepcie: native backend unavailable")
)

// BDF is a PCI domain:bus:device.function address.
type BDF struct {
	Domain   uint16
	Bus      uint8
	Device   uint8
	Function uint8
}

// String returns the canonical lowercase full-domain form dddd:bb:dd.f.
func (b BDF) String() string {
	return fmt.Sprintf("%04x:%02x:%02x.%x", b.Domain, b.Bus, b.Device, b.Function)
}

// AERCounter is one AER counter as listed in a sysfs aer_dev_* file.
type AERCounter struct {
	Name  string
	Count uint64
}

// Available reports whether a supported native backend is linked and reports
// ABI version 1.
func Available() bool {
	return backendAvailable()
}

// ABIVersion returns 1 when the native backend is available and 0 otherwise.
func ABIVersion() uint32 {
	if !Available() {
		return 0
	}
	return 1
}

// ParseBDF parses exactly 12 bytes in dddd:bb:dd.f form (either hex case).
func ParseBDF(s string) (BDF, error) {
	if !Available() {
		return BDF{}, unavailable("parse bdf")
	}
	return backendParseBDF(s)
}

// ParseWidth parses a sysfs link-width value and returns 1, 2, 4, 8, 12, 16 or 32.
func ParseWidth(data []byte) (uint32, error) {
	if !Available() {
		return 0, unavailable("parse width")
	}
	return backendParseWidth(data)
}

// ParseSpeed parses a sysfs link-speed value ("16.0 GT/s", "2.5 GT/s PCIe")
// and returns the rate in milli-GT/s without inferring a PCIe generation.
func ParseSpeed(data []byte) (uint32, error) {
	if !Available() {
		return 0, unavailable("parse speed")
	}
	return backendParseSpeed(data)
}

// ParseNUMA parses a sysfs numa_node value; -1 and Unknown report ErrNoData.
func ParseNUMA(data []byte) (int32, error) {
	if !Available() {
		return 0, unavailable("parse numa")
	}
	return backendParseNUMA(data)
}

// ParseAER parses an AER counter file into counters in input order.
func ParseAER(data []byte) ([]AERCounter, error) {
	if !Available() {
		return nil, unavailable("parse aer")
	}
	return backendParseAER(data)
}

// ReadFile reads at most MaxTextLen bytes of a regular file from offset zero
// through the native bounded reader, preserving the file's current offset.
// The caller keeps f open for the whole call; the descriptor is only borrowed
// during the synchronous native call and is never retained by C.
func ReadFile(f *os.File) ([]byte, error) {
	if !Available() {
		return nil, unavailable("read file")
	}
	return backendReadFile(f)
}

func unavailable(op string) error {
	return fmt.Errorf("nativepcie: %s: %w", op, ErrUnavailable)
}
