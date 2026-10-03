package liveclient

import (
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/transport/mtls"
)

// The binary constants of the stream client (GLI-082, GLI-083). Options can
// shorten the time seams, the binary never exposes them as flags.
const (
	defaultInterval         = 15 * time.Second
	defaultEvidenceTTL      = 300 * time.Second
	minEvidenceTTL          = time.Second
	maxEvidenceTTL          = 86400 * time.Second
	defaultHelloTimeout     = 30 * time.Second
	defaultAckTimeout       = 30 * time.Second
	defaultReconnectInitial = time.Second
	defaultReconnectMax     = 60 * time.Second
	defaultExhaustedMin     = 10 * time.Second

	// DefaultBootIDFile is where the kernel keeps the boot ID of a Linux host
	// (GLI-084). It is the default of Options.BootIDFile and not a sysfs path.
	DefaultBootIDFile = "/proc/sys/kernel/random/boot_id"
)

// ErrInvalidOptions is matched by the error of Options.ValidateFlags and of Run
// when an option value breaks the rules of GLI-080. The error names the option
// and never quotes its value.
var ErrInvalidOptions = errors.New("liveclient: invalid options")

// ErrLiveConfig is matched by the error of Run when the start-up validation
// fails: the CA, certificate or key files cannot be read or parsed or do not
// belong together, the certificate identity does not match the cluster and node
// options, the sysfs root cannot be opened as a directory, or the boot ID file
// cannot be read or holds no valid boot ID. The error is a fixed phrase and
// never carries a path or a file content.
var ErrLiveConfig = errors.New("liveclient: invalid live configuration")

// optionError is the error of one invalid option.
type optionError struct{ name string }

func (e *optionError) Error() string { return "invalid value for " + e.name }

func (e *optionError) Is(target error) bool { return target == ErrInvalidOptions }

// configError is a start-up validation failure with a fixed phrase.
type configError struct{ phrase string }

func (e *configError) Error() string { return e.phrase }

func (e *configError) Is(target error) bool { return target == ErrLiveConfig }

// TLSFiles names the PEM files of the client TLS setup: the CA certificates
// that sign the controller's certificate, and this client's certificate chain
// and private key.
type TLSFiles struct{ CAFile, CertFile, KeyFile string }

// Clock supplies the current time. It gives the observed_at of a frame; every
// wait is a real timer.
type Clock interface{ Now() time.Time }

// Options configures Run.
type Options struct {
	Controller, ClusterID, NodeName, NodeUID string
	// SysfsRoot, NVIDIASMI and BootIDFile: NVIDIASMI "" means no NVIDIA
	// inventory, BootIDFile "" means DefaultBootIDFile.
	SysfsRoot, NVIDIASMI, BootIDFile string
	TLS                              TLSFiles
	// Interval and EvidenceTTL are 15s and 300s when 0.
	Interval, EvidenceTTL time.Duration
	// HelloTimeout and AckTimeout are 30s when 0.
	HelloTimeout, AckTimeout time.Duration
	// ReconnectInitial and ReconnectMax are 1s and 60s when 0.
	ReconnectInitial, ReconnectMax time.Duration
	// ExhaustedMin is 10s when 0.
	ExhaustedMin time.Duration
	// Clock is the system clock when nil.
	Clock Clock
	// Logger is slog.Default() when nil.
	Logger *slog.Logger
}

// ValidateFlags checks the value rules of GLI-080 without touching a file, the
// network or a process. An error matches ErrInvalidOptions and names the
// option, never its value.
func (o Options) ValidateFlags() error {
	switch {
	case !validController(o.Controller):
		return &optionError{"--controller"}
	case !liveingest.ValidClusterID(o.ClusterID):
		return &optionError{"--cluster-id"}
	case !liveingest.ValidNodeName(o.NodeName):
		return &optionError{"--node-name"}
	case !liveingest.ValidNodeUID(o.NodeUID):
		return &optionError{"--node-uid"}
	case o.SysfsRoot == "":
		return &optionError{"--sysfs-root"}
	case o.TLS.CAFile == "":
		return &optionError{"--ca-file"}
	case o.TLS.CertFile == "":
		return &optionError{"--cert-file"}
	case o.TLS.KeyFile == "":
		return &optionError{"--key-file"}
	case o.NVIDIASMI != "" && (!filepath.IsAbs(o.NVIDIASMI) || filepath.Clean(o.NVIDIASMI) != o.NVIDIASMI):
		return &optionError{"--nvidia-smi"}
	case o.Interval < 0:
		return &optionError{"Interval"}
	case o.EvidenceTTL < 0 || (o.EvidenceTTL != 0 && (o.EvidenceTTL < minEvidenceTTL || o.EvidenceTTL > maxEvidenceTTL)):
		return &optionError{"EvidenceTTL"}
	case o.HelloTimeout < 0:
		return &optionError{"HelloTimeout"}
	case o.AckTimeout < 0:
		return &optionError{"AckTimeout"}
	case o.ReconnectInitial < 0:
		return &optionError{"ReconnectInitial"}
	case o.ReconnectMax < 0:
		return &optionError{"ReconnectMax"}
	case o.ExhaustedMin < 0:
		return &optionError{"ExhaustedMin"}
	}
	return nil
}

// validController reports whether s is host:port with a DNS host (never an IP
// address literal, GLI-022) and a port from 1 to 65535.
func validController(s string) bool {
	if strings.ContainsAny(s, "[]") {
		return false
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil || mtls.ValidateServerName(host) != nil {
		return false
	}
	if port == "" || len(port) > 5 {
		return false
	}
	n := 0
	for i := 0; i < len(port); i++ {
		if port[i] < '0' || port[i] > '9' {
			return false
		}
		n = n*10 + int(port[i]-'0')
	}
	return n >= 1 && n <= 65535
}

// withDefaults returns o with every zero seam replaced by its constant.
func (o Options) withDefaults() Options {
	o.Interval = orDefault(o.Interval, defaultInterval)
	o.EvidenceTTL = orDefault(o.EvidenceTTL, defaultEvidenceTTL)
	o.HelloTimeout = orDefault(o.HelloTimeout, defaultHelloTimeout)
	o.AckTimeout = orDefault(o.AckTimeout, defaultAckTimeout)
	o.ReconnectInitial = orDefault(o.ReconnectInitial, defaultReconnectInitial)
	o.ReconnectMax = orDefault(o.ReconnectMax, defaultReconnectMax)
	o.ExhaustedMin = orDefault(o.ExhaustedMin, defaultExhaustedMin)
	if o.BootIDFile == "" {
		o.BootIDFile = DefaultBootIDFile
	}
	if o.Clock == nil {
		o.Clock = systemClock{}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

func orDefault(d, def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return d
}

// systemClock is the wall clock; it is the only place that reads it.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
