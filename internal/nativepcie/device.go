package nativepcie

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FieldStatus classifies the outcome of observing one device attribute.
type FieldStatus string

// The complete set of FieldStatus values.
const (
	// FieldOK: the attribute was read and parsed.
	FieldOK FieldStatus = "ok"
	// FieldMissing: the attribute file does not exist.
	FieldMissing FieldStatus = "missing"
	// FieldUnsupported: the attribute exists but carries no data.
	FieldUnsupported FieldStatus = "unsupported"
	// FieldInvalid: the attribute content is malformed, out of range or too large.
	FieldInvalid FieldStatus = "invalid"
	// FieldIO: the attribute could not be opened or read (permission, root
	// boundary, nonregular object, or descriptor failure).
	FieldIO FieldStatus = "io"
)

// FieldObservation is one raw parsed attribute of a device. Only the value
// member that applies to the field kind is populated: Value for link width
// (lanes) and link speed (milli-GT/s), NUMA for numa_node, Counters for AER
// files. Err is nil only when Status is FieldOK.
type FieldObservation struct {
	Name     string
	Status   FieldStatus
	Value    uint32
	NUMA     int32
	Counters []AERCounter
	Err      error
}

// DeviceObservation holds the raw parsed attributes of one PCI device. It
// carries facts only: no discovery, identity resolution, topology inference,
// readiness or health evaluation happens here.
type DeviceObservation struct {
	BDF    string
	Fields []FieldObservation
}

type fieldKind uint8

const (
	kindWidth fieldKind = iota
	kindSpeed
	kindNUMA
	kindAER
)

// deviceFields lists the fixed attributes in observation order.
var deviceFields = [...]struct {
	name string
	kind fieldKind
}{
	{"current_link_width", kindWidth},
	{"max_link_width", kindWidth},
	{"current_link_speed", kindSpeed},
	{"max_link_speed", kindSpeed},
	{"numa_node", kindNUMA},
	{"aer_dev_correctable", kindAER},
	{"aer_dev_nonfatal", kindAER},
	{"aer_dev_fatal", kindAER},
}

// ReadDevice observes the fixed attribute set of the device at
// bus/pci/devices/<canonical BDF> under root. Every path is resolved through
// root; there is no /sys default. A missing device directory returns an error
// matching fs.ErrNotExist; a directory that is inaccessible, escapes root, or
// is not a directory returns the preserved OS error. Once the directory is
// open, every field is attempted and the observation is returned with a nil
// error even when individual fields fail; each field carries its own status
// and error.
func ReadDevice(root *os.Root, bdf string) (DeviceObservation, error) {
	if !Available() {
		return DeviceObservation{}, unavailable("read device")
	}
	if root == nil {
		return DeviceObservation{}, fmt.Errorf("nativepcie: read device: nil root: %w", ErrInvalid)
	}
	parsed, err := ParseBDF(bdf)
	if err != nil {
		return DeviceObservation{}, err
	}
	canonical := parsed.String()
	base := filepath.Join("bus", "pci", "devices", canonical)

	// openDirFlags carries O_DIRECTORY so the OS reports ENOTDIR for a
	// nondirectory; root reports escapes, missing paths and permission
	// failures itself.
	dir, err := root.OpenFile(base, os.O_RDONLY|openDirFlags, 0)
	if err != nil {
		return DeviceObservation{}, fmt.Errorf("nativepcie: read device %s: %w", canonical, err)
	}
	// The directory handle only validates the base; fields are resolved
	// through root so symlinks inside root remain acceptable.
	if err := dir.Close(); err != nil {
		return DeviceObservation{}, fmt.Errorf("nativepcie: read device %s: %w: %w", canonical, ErrIO, err)
	}

	obs := DeviceObservation{BDF: canonical, Fields: make([]FieldObservation, 0, len(deviceFields))}
	for _, field := range deviceFields {
		obs.Fields = append(obs.Fields, readField(root, filepath.Join(base, field.name), field.name, field.kind))
	}
	return obs, nil
}

// readField attempts one attribute and never returns an error to the caller;
// the outcome is encoded in the FieldObservation.
func readField(root *os.Root, path, name string, kind fieldKind) FieldObservation {
	obs := FieldObservation{Name: name}

	// Stat before Open: a FIFO, socket or device node is classified as io
	// here without ever opening (and thus blocking on) it.
	info, err := root.Stat(path)
	if err != nil {
		return failField(obs, err)
	}
	if !info.Mode().IsRegular() {
		obs.Status = FieldIO
		obs.Err = fmt.Errorf("nativepcie: field %s: %w: not a regular file (%s)", name, ErrIO, info.Mode().Type())
		return obs
	}
	// openFieldFlags carries O_NONBLOCK as a second guard against blocking.
	f, err := root.OpenFile(path, os.O_RDONLY|openFieldFlags, 0)
	if err != nil {
		return failField(obs, err)
	}
	data, err := ReadFile(f)
	// Go owns the descriptor; it is closed as soon as the native call returns.
	if cerr := f.Close(); cerr != nil && err == nil {
		err = fmt.Errorf("nativepcie: field %s: %w: %w", name, ErrIO, cerr)
	}
	if err != nil {
		return failField(obs, err)
	}

	switch kind {
	case kindWidth:
		obs.Value, err = ParseWidth(data)
	case kindSpeed:
		obs.Value, err = ParseSpeed(data)
	case kindNUMA:
		obs.NUMA, err = ParseNUMA(data)
	case kindAER:
		obs.Counters, err = ParseAER(data)
	}
	if err != nil {
		obs.Value, obs.NUMA, obs.Counters = 0, 0, nil
		return failField(obs, err)
	}
	obs.Status = FieldOK
	return obs
}

// failField classifies err into a field status, preserving the cause.
func failField(obs FieldObservation, err error) FieldObservation {
	obs.Err = err
	switch {
	case errors.Is(err, fs.ErrNotExist):
		obs.Status = FieldMissing
	case errors.Is(err, ErrNoData):
		obs.Status = FieldUnsupported
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrRange), errors.Is(err, ErrTooLarge):
		obs.Status = FieldInvalid
	case errors.Is(err, ErrIO):
		obs.Status = FieldIO
	default:
		// Boundary, permission or type errors from root, and any other
		// failure, are I/O outcomes; wrap ErrIO so callers can classify.
		obs.Status = FieldIO
		obs.Err = fmt.Errorf("nativepcie: field %s: %w: %w", obs.Name, ErrIO, err)
	}
	return obs
}
