//go:build !unix

package explain

// ignoreBrokenPipe does nothing where SIGPIPE does not exist; a failed stdout
// write is still reported as exit 1.
func ignoreBrokenPipe() {}
