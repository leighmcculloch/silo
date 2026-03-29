//go:build linux

package daytona

import "golang.org/x/sys/unix"

func flushTerminalInput(fd uintptr) {
	_ = unix.IoctlSetInt(int(fd), unix.TCFLSH, unix.TCIFLUSH)
}
