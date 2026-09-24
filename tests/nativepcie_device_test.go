//go:build cgo

package tests_test

// ReadDevice with injected os.Root fixtures (NPO-003, NPO-050..NPO-054).

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/nativepcie"
)

const npoBDF = "0000:01:00.0"

// npoRootCause opens rel through root the way ReadDevice must and returns the
// underlying cause (*fs.PathError.Err) the boundary produces, so tests can
// assert the same cause is preserved without comparing strings.
func npoRootCause(t *testing.T, root *os.Root, rel string) error {
	t.Helper()
	f, err := root.Open(rel)
	if err == nil {
		_ = f.Close()
		t.Fatalf("fixture: %s opened through the root; expected a boundary error", rel)
	}
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Err == nil {
		t.Fatalf("fixture: root.Open(%s) error %v is not a *fs.PathError with a cause", rel, err)
	}
	return pe.Err
}

func npoAssertOrder(t *testing.T, clause string, obs nativepcie.DeviceObservation) {
	t.Helper()
	if len(obs.Fields) != len(npoFieldOrder) {
		t.Errorf("%s: %d fields, want %d", clause, len(obs.Fields), len(npoFieldOrder))
		return
	}
	for i, name := range npoFieldOrder {
		if obs.Fields[i].Name != name {
			t.Errorf("%s: field %d = %q, want %q", clause, i, obs.Fields[i].Name, name)
		}
	}
}

// npoAssertFailedField checks the status/error contract of a non-ok field and
// that every typed result member is empty.
func npoAssertFailedField(t *testing.T, clause string, f *nativepcie.FieldObservation, wantStatus string, wantErr error) {
	t.Helper()
	if f == nil {
		t.Errorf("%s: field missing from observation", clause)
		return
	}
	if got := npoStatus(f.Status); got != wantStatus {
		t.Errorf("%s: %s status %q, want %q (err=%v)", clause, f.Name, got, wantStatus, f.Err)
	}
	if f.Err == nil {
		t.Errorf("%s: %s has nil Err with status %q", clause, f.Name, wantStatus)
	} else if wantErr != nil && !errors.Is(f.Err, wantErr) {
		t.Errorf("%s: %s Err %v does not match %v", clause, f.Name, f.Err, wantErr)
	}
	if f.Value != 0 || f.NUMA != 0 || len(f.Counters) != 0 {
		t.Errorf("%s: %s typed result not empty on %q: Value=%d NUMA=%d Counters=%d", clause, f.Name, wantStatus, f.Value, f.NUMA, len(f.Counters))
	}
}

func TestNPO052_AllFieldsOkInOrder(t *testing.T) {
	npoRequireBackend(t)
	rootDir, _ := npoFixture(t, npoBDF, npoGoodDeviceFiles())
	obs, err := nativepcie.ReadDevice(npoOpenRoot(t, rootDir), npoBDF)
	if err != nil {
		t.Fatalf("NPO-054: ReadDevice error %v, want nil", err)
	}
	if obs.BDF != npoBDF {
		t.Errorf("NPO-052: BDF %q, want %q", obs.BDF, npoBDF)
	}
	npoAssertOrder(t, "NPO-052", obs)
	want := map[string]struct {
		value    uint32
		numa     int32
		counters []nativepcie.AERCounter
	}{
		"current_link_width":  {value: 16},
		"max_link_width":      {value: 16},
		"current_link_speed":  {value: 16000},
		"max_link_speed":      {value: 32000},
		"numa_node":           {numa: 0},
		"aer_dev_correctable": {counters: []nativepcie.AERCounter{{Name: "RxErr"}, {Name: "BadTLP", Count: 3}, {Name: "TOTAL_ERR_COR", Count: 3}}},
		"aer_dev_nonfatal":    {counters: []nativepcie.AERCounter{{Name: "Undefined"}, {Name: "TOTAL_ERR_NONFATAL"}}},
		"aer_dev_fatal":       {counters: []nativepcie.AERCounter{{Name: "DLP", Count: 1}}},
	}
	for _, f := range obs.Fields {
		w, ok := want[f.Name]
		if !ok {
			continue
		}
		if s := npoStatus(f.Status); s != "ok" {
			t.Errorf("NPO-053: %s status %q, want ok (err=%v)", f.Name, s, f.Err)
		}
		if f.Err != nil {
			t.Errorf("NPO-053: %s Err %v, want nil", f.Name, f.Err)
		}
		if f.Value != w.value || f.NUMA != w.numa {
			t.Errorf("NPO-052: %s Value=%d NUMA=%d, want Value=%d NUMA=%d (non-applicable members must stay zero)", f.Name, f.Value, f.NUMA, w.value, w.numa)
		}
		if len(f.Counters) != len(w.counters) {
			t.Errorf("NPO-052: %s %d counters, want %d", f.Name, len(f.Counters), len(w.counters))
			continue
		}
		for i := range w.counters {
			if f.Counters[i] != w.counters[i] {
				t.Errorf("NPO-052: %s counter %d = %+v, want %+v", f.Name, i, f.Counters[i], w.counters[i])
			}
		}
	}
}

func TestNPO050_FieldStatusStrings(t *testing.T) {
	npoRequireBackend(t)
	files := npoGoodDeviceFiles()
	delete(files, "max_link_width")        // missing
	files["numa_node"] = "-1\n"            // unsupported
	files["current_link_speed"] = "fast\n" // invalid
	files["aer_dev_fatal"] = ""            // unsupported (no data)
	rootDir, deviceDir := npoFixture(t, npoBDF, files)
	if err := os.Mkdir(filepath.Join(deviceDir, "aer_dev_nonfatal.dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// aer_dev_nonfatal becomes a directory: io.
	if err := os.Remove(filepath.Join(deviceDir, "aer_dev_nonfatal")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(deviceDir, "aer_dev_nonfatal.dir"), filepath.Join(deviceDir, "aer_dev_nonfatal")); err != nil {
		t.Fatal(err)
	}
	obs, err := nativepcie.ReadDevice(npoOpenRoot(t, rootDir), npoBDF)
	if err != nil {
		t.Fatalf("NPO-054: ReadDevice error %v", err)
	}
	seen := map[string]bool{}
	for _, f := range obs.Fields {
		seen[npoStatus(f.Status)] = true
	}
	for _, s := range []string{"ok", "missing", "unsupported", "invalid", "io"} {
		if !seen[s] {
			t.Errorf("NPO-050: status %q never produced (seen %v)", s, seen)
		}
	}
	for s := range seen {
		switch s {
		case "ok", "missing", "unsupported", "invalid", "io":
		default:
			t.Errorf("NPO-050: status %q outside the allowed set", s)
		}
	}
}

func TestNPO051_NilRootPrecedesBDFValidation(t *testing.T) {
	npoRequireBackend(t)
	for _, bdf := range []string{"", "garbage", "0000:00:20.0", npoBDF} {
		var obs nativepcie.DeviceObservation
		var err error
		npoNoPanic(t, "NPO-051", func() { obs, err = nativepcie.ReadDevice(nil, bdf) })
		if !errors.Is(err, nativepcie.ErrInvalid) {
			t.Errorf("NPO-051: ReadDevice(nil, %q) error %v, want ErrInvalid", bdf, err)
		}
		if errors.Is(err, nativepcie.ErrRange) {
			t.Errorf("NPO-051: ReadDevice(nil, %q) matched ErrRange; nil root must take precedence", bdf)
		}
		if !npoEmptyObservation(obs) {
			t.Errorf("NPO-051: ReadDevice(nil, %q) returned non-empty observation %+v", bdf, obs)
		}
	}
}

func TestNPO051_InvalidBDFMatchesParseBDFSentinel(t *testing.T) {
	npoRequireBackend(t)
	rootDir, _ := npoFixture(t, npoBDF, npoGoodDeviceFiles())
	root := npoOpenRoot(t, rootDir)
	cases := []struct {
		bdf  string
		want error
	}{
		{"", nativepcie.ErrInvalid},
		{"garbage", nativepcie.ErrInvalid},
		{"0000:01:00.0\n", nativepcie.ErrInvalid},
		{"0000:01:00.\x00", nativepcie.ErrInvalid},
		{"0000-01:00.0", nativepcie.ErrInvalid},
		{"0000:00:20.0", nativepcie.ErrRange},
		{"0000:00:00.8", nativepcie.ErrRange},
		{"../0000:01:00.0", nativepcie.ErrInvalid},
	}
	for _, c := range cases {
		_, perr := nativepcie.ParseBDF(c.bdf)
		if !errors.Is(perr, c.want) {
			t.Errorf("NPO-020: ParseBDF(%q) = %v, want %v", c.bdf, perr, c.want)
		}
		obs, err := nativepcie.ReadDevice(root, c.bdf)
		if !errors.Is(err, c.want) {
			t.Errorf("NPO-051: ReadDevice(root, %q) error %v, want %v (same as ParseBDF)", c.bdf, err, c.want)
		}
		if errors.Is(err, fs.ErrNotExist) {
			t.Errorf("NPO-051: ReadDevice(root, %q) matched fs.ErrNotExist; a device path must not be opened for an invalid BDF", c.bdf)
		}
		if !npoEmptyObservation(obs) {
			t.Errorf("NPO-051: ReadDevice(root, %q) returned non-empty observation", c.bdf)
		}
	}
}

func TestNPO051_CanonicalizesBDFBeforeOpening(t *testing.T) {
	npoRequireBackend(t)
	const lower = "0000:0a:00.0"
	rootDir, _ := npoFixture(t, lower, npoGoodDeviceFiles())
	obs, err := nativepcie.ReadDevice(npoOpenRoot(t, rootDir), "0000:0A:00.0")
	if err != nil {
		t.Fatalf("NPO-051: ReadDevice(uppercase BDF) error %v; device dir is %q", err, lower)
	}
	if obs.BDF != lower {
		t.Errorf("NPO-041/NPO-052: observation BDF %q, want canonical %q", obs.BDF, lower)
	}
	npoAssertOrder(t, "NPO-052", obs)
	if f := npoFieldByName(obs, "current_link_width"); f == nil || npoStatus(f.Status) != "ok" || f.Value != 16 {
		t.Errorf("NPO-053: current_link_width not ok after canonicalization: %+v", f)
	}
}

func TestNPO051_MissingDeviceDirectory(t *testing.T) {
	npoRequireBackend(t)
	rootDir, _ := npoFixture(t, npoBDF, npoGoodDeviceFiles())
	obs, err := nativepcie.ReadDevice(npoOpenRoot(t, rootDir), "0000:ff:1f.7")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("NPO-051: ReadDevice(missing device) error %v, want fs.ErrNotExist", err)
	}
	if !npoEmptyObservation(obs) {
		t.Errorf("NPO-051: ReadDevice(missing device) returned non-empty observation %+v", obs)
	}
	// Empty root: bus/pci/devices itself is missing.
	obs, err = nativepcie.ReadDevice(npoOpenRoot(t, t.TempDir()), npoBDF)
	if !errors.Is(err, fs.ErrNotExist) || !npoEmptyObservation(obs) {
		t.Errorf("NPO-051: ReadDevice(empty root) = %+v, %v; want empty, fs.ErrNotExist", obs, err)
	}
}

func TestNPO051_BaseNotADirectory(t *testing.T) {
	npoRequireBackend(t)
	rootDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootDir, "bus", "pci", "devices"), 0o755); err != nil {
		t.Fatal(err)
	}
	npoWriteFile(t, npoDeviceDir(rootDir, npoBDF), "16\n")
	obs, err := nativepcie.ReadDevice(npoOpenRoot(t, rootDir), npoBDF)
	if err == nil {
		t.Fatalf("NPO-051: ReadDevice(base is a regular file) returned nil error with %+v", obs)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("NPO-051: ReadDevice(base is a regular file) reported fs.ErrNotExist; want the type error preserved (%v)", err)
	}
	// The OS type error for "opened as a directory but is a file" is ENOTDIR;
	// it must survive in the returned error.
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("NPO-051: ReadDevice(base is a regular file) error %v does not preserve syscall.ENOTDIR", err)
	}
	if !npoEmptyObservation(obs) {
		t.Errorf("NPO-051: ReadDevice(base is a regular file) read fields: %+v", obs)
	}
}

func TestNPO051_BaseSymlinkEscapesRoot(t *testing.T) {
	npoRequireBackend(t)
	outside := t.TempDir()
	for name, content := range npoGoodDeviceFiles() {
		npoWriteFile(t, filepath.Join(outside, name), content)
	}
	rootDir := t.TempDir()
	devices := filepath.Join(rootDir, "bus", "pci", "devices")
	if err := os.MkdirAll(devices, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(devices, npoBDF)); err != nil {
		t.Fatal(err)
	}
	root := npoOpenRoot(t, rootDir)
	baseRel := filepath.Join("bus", "pci", "devices", npoBDF)
	cause := npoRootCause(t, root, baseRel)
	obs, err := nativepcie.ReadDevice(root, npoBDF)
	if err == nil {
		t.Fatalf("NPO-003/NPO-051: ReadDevice followed a symlink escaping the root and returned %+v", obs)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("NPO-051: absolute symlink escape reported fs.ErrNotExist instead of the boundary error (%v)", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("NPO-051: absolute symlink escape error %v does not preserve the root boundary cause %v", err, cause)
	}
	if !npoEmptyObservation(obs) {
		t.Errorf("NPO-051: escaped base still produced fields: %+v", obs)
	}

	// Relative "../" symlink that also resolves outside the root. The link
	// sits at <root>/bus/pci/devices/<bdf>, so four levels up is the parent
	// of the root; the outside directory is its sibling.
	link := filepath.Join(devices, npoBDF)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "..", "..", filepath.Base(outside)), link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("fixture: relative escape link does not resolve: %v", err)
	}
	realRoot, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(resolved, realRoot+string(filepath.Separator)) {
		t.Fatalf("fixture: relative link resolves inside the root (%s)", resolved)
	}
	obs, err = nativepcie.ReadDevice(npoOpenRoot(t, rootDir), npoBDF)
	if err == nil {
		t.Fatalf("NPO-003/NPO-051: ReadDevice followed a relative symlink escaping the root and returned %+v", obs)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("NPO-051: relative symlink escape reported fs.ErrNotExist instead of the boundary error (%v)", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("NPO-051: relative symlink escape error %v does not preserve the root boundary cause %v", err, cause)
	}
	if !npoEmptyObservation(obs) {
		t.Errorf("NPO-051: relative symlink escape still produced fields: %+v", obs)
	}
}

func TestNPO051_BaseInaccessible(t *testing.T) {
	npoRequireBackend(t)
	if os.Geteuid() == 0 {
		t.Skip("NPO-051: permission checks are not observable as root")
	}
	rootDir, deviceDir := npoFixture(t, npoBDF, npoGoodDeviceFiles())
	if err := os.Chmod(deviceDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(deviceDir, 0o755) })
	obs, err := nativepcie.ReadDevice(npoOpenRoot(t, rootDir), npoBDF)
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("NPO-051: ReadDevice(inaccessible base) error %v, want fs.ErrPermission preserved", err)
	}
	if !npoEmptyObservation(obs) {
		t.Errorf("NPO-051: ReadDevice(inaccessible base) read fields: %+v", obs)
	}
}

func TestNPO053_PerFieldOutcomes(t *testing.T) {
	npoRequireBackend(t)
	files := npoGoodDeviceFiles()
	delete(files, "max_link_width")                                                           // missing
	files["current_link_width"] = "Unknown\n"                                                 // unsupported
	files["current_link_speed"] = "fast\n"                                                    // invalid (ErrInvalid)
	files["max_link_speed"] = "4294967.296 GT/s\n"                                            // invalid (ErrRange)
	files["numa_node"] = "-1\n"                                                               // unsupported
	files["aer_dev_correctable"] = ""                                                         // unsupported (NODATA)
	files["aer_dev_nonfatal"] = string(make([]byte, 0))                                       // replaced below with 65 entries
	files["aer_dev_fatal"] = string(append([]byte("DLP 1\n"), make([]byte, npoTextLimit)...)) // > 16,384 bytes: read too-large
	var big []byte
	for i := 0; i < 65; i++ {
		big = append(big, []byte("E"+string(rune('a'+i%26))+string(rune('a'+i/26))+" 1\n")...)
	}
	files["aer_dev_nonfatal"] = string(big) // invalid (ErrTooLarge from parser)
	rootDir, _ := npoFixture(t, npoBDF, files)

	obs, err := nativepcie.ReadDevice(npoOpenRoot(t, rootDir), npoBDF)
	if err != nil {
		t.Fatalf("NPO-054: ReadDevice error %v, want nil with per-field failures", err)
	}
	npoAssertOrder(t, "NPO-052", obs)
	npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "max_link_width"), "missing", fs.ErrNotExist)
	npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "current_link_width"), "unsupported", nativepcie.ErrNoData)
	npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "current_link_speed"), "invalid", nativepcie.ErrInvalid)
	npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "max_link_speed"), "invalid", nativepcie.ErrRange)
	npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "numa_node"), "unsupported", nativepcie.ErrNoData)
	npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "aer_dev_correctable"), "unsupported", nativepcie.ErrNoData)
	npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "aer_dev_nonfatal"), "invalid", nativepcie.ErrTooLarge)
	npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "aer_dev_fatal"), "invalid", nativepcie.ErrTooLarge)

	// A missing field must not be reported as unsupported/io and vice versa.
	if f := npoFieldByName(obs, "max_link_width"); f != nil && (errors.Is(f.Err, nativepcie.ErrNoData) || errors.Is(f.Err, nativepcie.ErrIO)) {
		t.Errorf("NPO-053: missing field error %v matches a parser/io sentinel", f.Err)
	}
}

func TestNPO053_FieldIOOutcomes(t *testing.T) {
	npoRequireBackend(t)
	files := npoGoodDeviceFiles()
	delete(files, "current_link_width") // becomes a directory
	delete(files, "max_link_width")     // becomes a symlink escaping the root
	delete(files, "current_link_speed") // becomes permission-denied (unless root)
	rootDir, deviceDir := npoFixture(t, npoBDF, files)
	if err := os.Mkdir(filepath.Join(deviceDir, "current_link_width"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "max_link_width")
	npoWriteFile(t, outside, "16\n")
	if err := os.Symlink(outside, filepath.Join(deviceDir, "max_link_width")); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(deviceDir, "current_link_speed")
	npoWriteFile(t, denied, "16.0 GT/s PCIe\n")
	if err := os.Chmod(denied, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(denied, 0o644) })

	root := npoOpenRoot(t, rootDir)
	escapeCause := npoRootCause(t, root, filepath.Join("bus", "pci", "devices", npoBDF, "max_link_width"))
	obs, err := nativepcie.ReadDevice(root, npoBDF)
	if err != nil {
		t.Fatalf("NPO-054: ReadDevice error %v, want nil", err)
	}
	npoAssertOrder(t, "NPO-052", obs)
	npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "current_link_width"), "io", nil)
	escaped := npoFieldByName(obs, "max_link_width")
	npoAssertFailedField(t, "NPO-003/NPO-053", escaped, "io", escapeCause)
	if escaped != nil && escaped.Value == 16 {
		t.Errorf("NPO-003: value read through a symlink escaping the root")
	}
	if os.Geteuid() != 0 {
		npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "current_link_speed"), "io", fs.ErrPermission)
	}
	// Siblings after the failures are unaffected.
	for _, name := range []string{"max_link_speed", "numa_node", "aer_dev_correctable", "aer_dev_nonfatal", "aer_dev_fatal"} {
		f := npoFieldByName(obs, name)
		if f == nil || npoStatus(f.Status) != "ok" || f.Err != nil {
			t.Errorf("NPO-054: sibling %s suppressed: %+v", name, f)
		}
	}
}

func TestNPO053_FIFOFieldIsIOWithoutWaiting(t *testing.T) {
	npoRequireBackend(t)
	const limit = 3 * time.Second

	// (a) FIFO with no writer at all: a blocking open would hang.
	files := npoGoodDeviceFiles()
	delete(files, "current_link_speed")
	rootDir, deviceDir := npoFixture(t, npoBDF, files)
	fifo := filepath.Join(deviceDir, "current_link_speed")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	var obs nativepcie.DeviceObservation
	var err error
	if npoWithin(t, "NPO-053", limit, func() { obs, err = nativepcie.ReadDevice(npoOpenRoot(t, rootDir), npoBDF) }) {
		if err != nil {
			t.Fatalf("NPO-054: ReadDevice error %v, want nil", err)
		}
		npoAssertOrder(t, "NPO-052", obs)
		npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "current_link_speed"), "io", nil)
		for _, name := range []string{"max_link_speed", "numa_node", "aer_dev_correctable", "aer_dev_nonfatal", "aer_dev_fatal"} {
			if f := npoFieldByName(obs, name); f == nil || npoStatus(f.Status) != "ok" {
				t.Errorf("NPO-053: sibling %s after the FIFO not processed: %+v", name, f)
			}
		}
	}

	// (b) FIFO with a writer attached and no data: a blocking read would hang.
	files = npoGoodDeviceFiles()
	delete(files, "numa_node")
	rootDir, deviceDir = npoFixture(t, npoBDF, files)
	fifo = filepath.Join(deviceDir, "numa_node")
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
	if npoWithin(t, "NPO-053", limit, func() { obs, err = nativepcie.ReadDevice(npoOpenRoot(t, rootDir), npoBDF) }) {
		if err != nil {
			t.Fatalf("NPO-054: ReadDevice error %v, want nil", err)
		}
		npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "numa_node"), "io", nil)
		for _, name := range []string{"aer_dev_correctable", "aer_dev_nonfatal", "aer_dev_fatal"} {
			if f := npoFieldByName(obs, name); f == nil || npoStatus(f.Status) != "ok" {
				t.Errorf("NPO-053: sibling %s after the FIFO not processed: %+v", name, f)
			}
		}
	}
}

func TestNPO054_PartialObservationKeepsSiblings(t *testing.T) {
	npoRequireBackend(t)
	files := npoGoodDeviceFiles()
	delete(files, "current_link_width")
	files["max_link_speed"] = "\n"
	files["aer_dev_nonfatal"] = "dup 1\ndup 2\n"
	rootDir, _ := npoFixture(t, npoBDF, files)
	obs, err := nativepcie.ReadDevice(npoOpenRoot(t, rootDir), npoBDF)
	if err != nil {
		t.Fatalf("NPO-054: top-level error %v with partial failures, want nil", err)
	}
	npoAssertOrder(t, "NPO-052", obs)
	npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "current_link_width"), "missing", fs.ErrNotExist)
	npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "max_link_speed"), "unsupported", nativepcie.ErrNoData)
	npoAssertFailedField(t, "NPO-053", npoFieldByName(obs, "aer_dev_nonfatal"), "invalid", nativepcie.ErrInvalid)
	okFields := map[string]bool{"max_link_width": true, "current_link_speed": true, "numa_node": true, "aer_dev_correctable": true, "aer_dev_fatal": true}
	for name := range okFields {
		f := npoFieldByName(obs, name)
		if f == nil || npoStatus(f.Status) != "ok" || f.Err != nil {
			t.Errorf("NPO-054: successful sibling %s suppressed: %+v", name, f)
		}
	}
	if f := npoFieldByName(obs, "aer_dev_correctable"); f != nil && len(f.Counters) != 3 {
		t.Errorf("NPO-054: aer_dev_correctable counters %d, want 3", len(f.Counters))
	}
}

func TestNPO052_DeterministicOrderAcrossRuns(t *testing.T) {
	npoRequireBackend(t)
	// Create the files in reverse order so directory order differs from the
	// mandated order on filesystems that preserve creation order.
	rootDir := t.TempDir()
	deviceDir := npoDeviceDir(rootDir, npoBDF)
	if err := os.MkdirAll(deviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := npoGoodDeviceFiles()
	delete(files, "numa_node") // one missing field in the middle
	for i := len(npoFieldOrder) - 1; i >= 0; i-- {
		name := npoFieldOrder[i]
		if content, ok := files[name]; ok {
			npoWriteFile(t, filepath.Join(deviceDir, name), content)
		}
	}
	root := npoOpenRoot(t, rootDir)
	var first nativepcie.DeviceObservation
	for run := 0; run < 5; run++ {
		obs, err := nativepcie.ReadDevice(root, npoBDF)
		if err != nil {
			t.Fatalf("NPO-054: run %d error %v", run, err)
		}
		npoAssertOrder(t, "NPO-052", obs)
		if run == 0 {
			first = obs
			continue
		}
		for i := range obs.Fields {
			a, b := first.Fields[i], obs.Fields[i]
			if a.Name != b.Name || npoStatus(a.Status) != npoStatus(b.Status) || a.Value != b.Value || a.NUMA != b.NUMA || len(a.Counters) != len(b.Counters) {
				t.Errorf("NPO-052: run %d field %d differs: %+v vs %+v", run, i, a, b)
			}
		}
	}
}

func TestNPO054_NoDescriptorLeak(t *testing.T) {
	npoRequireBackend(t)
	// Probe descriptors directly with fstat: /dev/fd enumeration is not
	// reliable on darwin, and POSIX allocates the lowest free number, so a
	// leak both raises the open count and moves the next free descriptor.
	countFDs := func() int {
		n := 0
		var st syscall.Stat_t
		for fd := 0; fd < 4096; fd++ {
			if syscall.Fstat(fd, &st) == nil {
				n++
			}
		}
		return n
	}
	lowestFree := func() int {
		fd, err := syscall.Open("/dev/null", syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatalf("NPO-054: cannot probe descriptors: %v", err)
		}
		_ = syscall.Close(fd)
		return fd
	}
	files := npoGoodDeviceFiles()
	delete(files, "max_link_width")
	files["numa_node"] = "-1\n"
	files["aer_dev_fatal"] = "x 1\nx 2\n"
	rootDir, _ := npoFixture(t, npoBDF, files)
	root := npoOpenRoot(t, rootDir)

	// Disable GC so finalizers cannot hide a leaked *os.File.
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)
	if _, err := nativepcie.ReadDevice(root, npoBDF); err != nil {
		t.Fatal(err)
	}
	before := countFDs()
	beforeFree := lowestFree()
	for i := 0; i < 32; i++ {
		if _, err := nativepcie.ReadDevice(root, npoBDF); err != nil {
			t.Fatal(err)
		}
	}
	after := countFDs()
	afterFree := lowestFree()
	if after != before {
		t.Errorf("NPO-054: open descriptors grew from %d to %d over 32 ReadDevice calls (descriptors not closed)", before, after)
	}
	if afterFree != beforeFree {
		t.Errorf("NPO-054: lowest free descriptor moved from %d to %d over 32 ReadDevice calls (descriptors not closed)", beforeFree, afterFree)
	}
}
