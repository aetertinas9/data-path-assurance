//go:build !cgo

package tests_test

// Unavailable-backend contract (NPO-043, NPO-060): run with
// CGO_ENABLED=0 go test ./tests/... — no native artifact may be required.

import (
	"errors"
	"os"
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/nativepcie"
)

func TestNPO043_UnavailableBuildReportsNoBackend(t *testing.T) {
	if nativepcie.Available() {
		t.Fatalf("NPO-043/NPO-060: Available() must be false when cgo is disabled")
	}
	if v := nativepcie.ABIVersion(); v != 0 {
		t.Fatalf("NPO-043: ABIVersion() = %d, want 0 when unavailable", v)
	}
}

func TestNPO043_UnavailableOperationsReturnErrUnavailableBeforeValidation(t *testing.T) {
	root := npoOpenRoot(t, t.TempDir())
	big := make([]byte, 16385)
	for i := range big {
		big[i] = '1'
	}
	tmp, err := os.CreateTemp(t.TempDir(), "npo")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tmp.Close() }()

	type call struct {
		name string
		run  func() (any, error)
		zero any
	}
	calls := []call{
		{"ParseBDF(valid)", func() (any, error) { return nativepcie.ParseBDF("0000:00:00.0") }, nativepcie.BDF{}},
		{"ParseBDF(invalid)", func() (any, error) { return nativepcie.ParseBDF("garbage") }, nativepcie.BDF{}},
		{"ParseBDF(range)", func() (any, error) { return nativepcie.ParseBDF("0000:00:20.0") }, nativepcie.BDF{}},
		{"ParseWidth(valid)", func() (any, error) { return nativepcie.ParseWidth([]byte("16")) }, uint32(0)},
		{"ParseWidth(nil)", func() (any, error) { return nativepcie.ParseWidth(nil) }, uint32(0)},
		{"ParseWidth(too large)", func() (any, error) { return nativepcie.ParseWidth(big) }, uint32(0)},
		{"ParseSpeed(valid)", func() (any, error) { return nativepcie.ParseSpeed([]byte("2.5 GT/s PCIe")) }, uint32(0)},
		{"ParseSpeed(invalid)", func() (any, error) { return nativepcie.ParseSpeed([]byte("x")) }, uint32(0)},
		{"ParseNUMA(valid)", func() (any, error) { return nativepcie.ParseNUMA([]byte("0")) }, int32(0)},
		{"ParseNUMA(nodata)", func() (any, error) { return nativepcie.ParseNUMA([]byte("-1")) }, int32(0)},
		{"ParseAER(valid)", func() (any, error) {
			v, err := nativepcie.ParseAER([]byte("RxErr 1\n"))
			return len(v), err
		}, 0},
		{"ParseAER(empty)", func() (any, error) {
			v, err := nativepcie.ParseAER(nil)
			return len(v), err
		}, 0},
		{"ReadFile(nil)", func() (any, error) {
			v, err := nativepcie.ReadFile(nil)
			return len(v), err
		}, 0},
		{"ReadFile(open file)", func() (any, error) {
			v, err := nativepcie.ReadFile(tmp)
			return len(v), err
		}, 0},
		{"ReadDevice(nil root, empty)", func() (any, error) {
			obs, err := nativepcie.ReadDevice(nil, "")
			return npoEmptyObservation(obs), err
		}, true},
		{"ReadDevice(nil root, valid)", func() (any, error) {
			obs, err := nativepcie.ReadDevice(nil, "0000:00:00.0")
			return npoEmptyObservation(obs), err
		}, true},
		{"ReadDevice(root, invalid)", func() (any, error) {
			obs, err := nativepcie.ReadDevice(root, "nope")
			return npoEmptyObservation(obs), err
		}, true},
		{"ReadDevice(root, valid)", func() (any, error) {
			obs, err := nativepcie.ReadDevice(root, "0000:00:00.0")
			return npoEmptyObservation(obs), err
		}, true},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			var got any
			var err error
			npoNoPanic(t, "NPO-043", func() { got, err = c.run() })
			if err == nil {
				t.Fatalf("NPO-043: %s returned nil error while unavailable (fabricated observation)", c.name)
			}
			if !errors.Is(err, nativepcie.ErrUnavailable) {
				t.Errorf("NPO-043: %s error %v does not match ErrUnavailable", c.name, err)
			}
			for _, s := range npoSentinels[:5] {
				if errors.Is(err, s) {
					t.Errorf("NPO-043: %s error %v matches validation sentinel %v; unavailable must precede input validation", c.name, err, s)
				}
			}
			if got != c.zero {
				t.Errorf("NPO-043: %s returned %v, want zero/empty %v", c.name, got, c.zero)
			}
		})
	}
}

func TestNPO060_UnavailableStubKeepsSentinelsDistinct(t *testing.T) {
	for i, a := range npoSentinels {
		if a == nil {
			t.Fatalf("NPO-042: sentinel %d is nil", i)
		}
		for j, b := range npoSentinels {
			if i != j && errors.Is(a, b) {
				t.Errorf("NPO-042: sentinel %v matches distinct sentinel %v", a, b)
			}
		}
	}
}
