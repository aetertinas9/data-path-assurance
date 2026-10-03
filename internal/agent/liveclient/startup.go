package liveclient

import (
	"errors"
	"io"
	"net"
	"os"
	"strings"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/transport/mtls"
)

// maxBootIDFileBytes bounds how much of the boot ID file is read; a boot ID is
// 36 bytes. procfs reports a size of 0, so the bound is on the bytes read.
const maxBootIDFileBytes = 4096

// controllerHost returns the host of a validated --controller value.
func controllerHost(controller string) string {
	host, _, err := net.SplitHostPort(controller)
	if err != nil {
		return controller
	}
	return host
}

// tlsFiles returns the TLS files in the form of package mtls.
func (o Options) tlsFiles() mtls.Files {
	return mtls.Files{CAFile: o.TLS.CAFile, CertFile: o.TLS.CertFile, KeyFile: o.TLS.KeyFile}
}

// validateStartup is the I/O validation of Run (GLI-112): the TLS files, the
// identity of the client certificate, the sysfs root and the boot ID file. A
// failure is an error that matches ErrLiveConfig with a fixed phrase. Nothing
// here opens a file that is not a regular file or a directory, so a FIFO cannot
// block the start.
func validateStartup(o Options) error {
	cfg, err := mtls.ClientConfig(o.tlsFiles(), controllerHost(o.Controller))
	if err != nil {
		return &configError{tlsPhrase(err)}
	}
	id, err := mtls.ParseIdentity(cfg.Certificates[0].Leaf)
	if err != nil || id.Role != mtls.RoleNode || id.ClusterID != o.ClusterID || id.Subject != o.NodeUID {
		return &configError{"the client certificate identity does not match the cluster and node options"}
	}
	root, err := openSysfsRoot(o.SysfsRoot)
	if err != nil {
		return &configError{"the sysfs root cannot be opened as a directory"}
	}
	// The root is only read, so a close failure loses nothing.
	_ = root.Close()
	if _, err := readBootID(o.BootIDFile); err != nil {
		return &configError{"the boot ID file cannot be read or holds no valid boot ID"}
	}
	return nil
}

// tlsPhrase maps a failure of mtls.ClientConfig to a fixed phrase.
func tlsPhrase(err error) string {
	switch {
	case errors.Is(err, mtls.ErrFile):
		return "a TLS file cannot be read"
	case errors.Is(err, mtls.ErrNoCA):
		return "the CA file holds no usable certificate"
	case errors.Is(err, mtls.ErrKeyPair):
		return "the client certificate and key cannot be loaded as a pair"
	default:
		return "the TLS configuration cannot be built"
	}
}

// openSysfsRoot opens dir as the sysfs observation root. dir is stat'ed first,
// following symlinks, and anything but a directory is refused without being
// opened, so a FIFO or device cannot block the open. It is called once per
// cycle, so a bind mount that replaced the directory is seen (GLI-081).
func openSysfsRoot(dir string) (*os.Root, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("liveclient: the sysfs root is not a directory")
	}
	return os.OpenRoot(dir)
}

// errBootID is the fixed failure of readBootID.
var errBootID = errors.New("liveclient: the boot ID file is unusable")

// readBootID reads the boot ID file (GLI-084): a regular file whose content,
// trimmed of leading and trailing ASCII blanks and line ends, is 1 to 128 bytes
// of identifier ASCII. A path that is not a regular file (a FIFO, a device, a
// directory) is refused without being opened. The error is fixed and never
// carries the path.
func readBootID(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errBootID
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errBootID
	}
	// The file was opened read-only, so a close failure loses nothing.
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxBootIDFileBytes+1))
	if err != nil || len(data) > maxBootIDFileBytes {
		return "", errBootID
	}
	id := strings.Trim(string(data), " \t\r\n")
	if !liveingest.ValidBootID(id) {
		return "", errBootID
	}
	return id, nil
}
