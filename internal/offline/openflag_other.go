//go:build !unix

package offline

// nonBlockingOpenFlag is empty where no non-blocking open exists; the
// regular-file check before the open still keeps non-regular files unopened.
const nonBlockingOpenFlag = 0
