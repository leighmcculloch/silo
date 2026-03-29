//go:build darwin || freebsd || netbsd || openbsd || dragonfly || aix || solaris

package daytona

import "golang.org/x/sys/unix"

func flushTerminalInput(fd uintptr) {
	_ = unix.IoctlSetPointerInt(int(fd), unix.TIOCFLUSH, unix.TCIFLUSH)
}
