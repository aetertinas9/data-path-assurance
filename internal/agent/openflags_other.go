//go:build !(darwin || linux)

package agent

// openFileFlags is empty where the collector's non-blocking guards are not
// available; the stat before every open remains the primary guard.
const openFileFlags = 0

// openDirFlags is empty where O_DIRECTORY is not available.
const openDirFlags = 0
