//go:build unix

package main

import (
	"os/signal"
	"syscall"
)

// ignoreBrokenPipe makes a write to a closed stdout reader fail with EPIPE,
// which path-agent reports as exit 1, instead of killing the process with
// SIGPIPE.
func ignoreBrokenPipe() {
	signal.Ignore(syscall.SIGPIPE)
}
