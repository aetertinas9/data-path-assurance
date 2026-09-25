//go:build unix

package offline

import "syscall"

// nonBlockingOpenFlag makes an open of a FIFO or device return at once instead
// of waiting for a peer; the opened file is then rejected as not regular.
const nonBlockingOpenFlag = syscall.O_NONBLOCK
