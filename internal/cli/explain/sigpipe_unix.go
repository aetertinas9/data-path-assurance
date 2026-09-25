//go:build unix

package explain

import (
	"os/signal"
	"syscall"
)

// ignoreBrokenPipe makes a write to a closed stdout reader fail with EPIPE,
// which is reported as exit 1, instead of killing the process with SIGPIPE.
func ignoreBrokenPipe() {
	signal.Ignore(syscall.SIGPIPE)
}
