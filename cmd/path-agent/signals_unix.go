//go:build unix

package main

import (
	"os"
	"syscall"
)

// stopSignals are the signals that stop the live mode: SIGINT and SIGTERM.
func stopSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
