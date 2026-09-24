//go:build cgo

package tests_test

// Helpers used only by the cgo (real backend) tests: fixture layout, field
// order, timing guard and observation accessors (NPO-052..NPO-054).

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/nativepcie"
)

// npoFieldOrder is the NPO-052 fixed field list, in the mandated order.
var npoFieldOrder = []string{
	"current_link_width",
	"max_link_width",
	"current_link_speed",
	"max_link_speed",
	"numa_node",
	"aer_dev_correctable",
	"aer_dev_nonfatal",
	"aer_dev_fatal",
}

// npoGoodDeviceFiles returns a complete, valid sysfs-like device directory.
func npoGoodDeviceFiles() map[string]string {
	return map[string]string{
		"current_link_width":  "16\n",
		"max_link_width":      "16\n",
		"current_link_speed":  "16.0 GT/s PCIe\n",
		"max_link_speed":      "32.0 GT/s PCIe\n",
		"numa_node":           "0\n",
		"aer_dev_correctable": "RxErr 0\nBadTLP 3\nTOTAL_ERR_COR 3\n",
		"aer_dev_nonfatal":    "Undefined 0\nTOTAL_ERR_NONFATAL 0\n",
		"aer_dev_fatal":       "DLP 1\n",
	}
}

// npoDeviceDir returns <rootDir>/bus/pci/devices/<bdf>.
func npoDeviceDir(rootDir, bdf string) string {
	return filepath.Join(rootDir, "bus", "pci", "devices", bdf)
}

// npoFixture creates a fixture root under t.TempDir() containing
// bus/pci/devices/<bdf>/ with the given files, and returns the root directory
// and the device directory.
func npoFixture(t *testing.T, bdf string, files map[string]string) (rootDir, deviceDir string) {
	t.Helper()
	rootDir = t.TempDir()
	deviceDir = npoDeviceDir(rootDir, bdf)
	if err := os.MkdirAll(deviceDir, 0o755); err != nil {
		t.Fatalf("fixture: mkdir %s: %v", deviceDir, err)
	}
	for name, content := range files {
		npoWriteFile(t, filepath.Join(deviceDir, name), content)
	}
	return rootDir, deviceDir
}

func npoWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("fixture: write %s: %v", path, err)
	}
}

// npoWithin runs fn and reports a failure tagged with clause when it does not
// return within limit. A hung call is left running; the test continues.
func npoWithin(t *testing.T, clause string, limit time.Duration, fn func()) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return true
	case <-time.After(limit):
		t.Errorf("%s: call did not return within %v (blocked waiting on a nonregular file?)", clause, limit)
		return false
	}
}

// npoStatus renders a FieldStatus the way it serializes as a string.
func npoStatus(s nativepcie.FieldStatus) string {
	return fmt.Sprint(s)
}

// npoFieldByName returns the field with the given name, or nil.
func npoFieldByName(obs nativepcie.DeviceObservation, name string) *nativepcie.FieldObservation {
	for i := range obs.Fields {
		if obs.Fields[i].Name == name {
			return &obs.Fields[i]
		}
	}
	return nil
}
