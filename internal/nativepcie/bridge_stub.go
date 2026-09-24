//go:build !cgo || !((darwin && arm64) || (linux && (amd64 || arm64)))

package nativepcie

import "os"

// This file is the unavailable backend: it is selected when cgo is disabled
// or the target platform has no native bridge. Every operation reports
// ErrUnavailable through the public wrappers; nothing is fabricated.

// ReadDevice returns ErrUnavailable before opening anything on this backend,
// so no platform open flags are needed; zero keeps every target buildable.
const (
	openDirFlags   = 0
	openFieldFlags = 0
)

func backendAvailable() bool { return false }

func backendParseBDF(string) (BDF, error) { return BDF{}, unavailable("parse bdf") }

func backendParseWidth([]byte) (uint32, error) { return 0, unavailable("parse width") }

func backendParseSpeed([]byte) (uint32, error) { return 0, unavailable("parse speed") }

func backendParseNUMA([]byte) (int32, error) { return 0, unavailable("parse numa") }

func backendParseAER([]byte) ([]AERCounter, error) { return nil, unavailable("parse aer") }

func backendReadFile(*os.File) ([]byte, error) { return nil, unavailable("read file") }
