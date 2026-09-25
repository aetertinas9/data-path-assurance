//go:build darwin || linux

package agent

import "syscall"

// openFileFlags keeps an open from waiting on a FIFO or device that replaced a
// regular file between the stat and the open.
const openFileFlags = syscall.O_NONBLOCK

// openDirFlags makes a directory open fail on anything but a directory and
// never wait on a FIFO.
const openDirFlags = syscall.O_DIRECTORY | syscall.O_NONBLOCK
