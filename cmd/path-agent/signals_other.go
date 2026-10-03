//go:build !unix

package main

import "os"

// stopSignals are the signals that stop the live mode where SIGTERM does not
// exist: the interrupt.
func stopSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
