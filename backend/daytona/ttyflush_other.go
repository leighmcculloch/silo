//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || aix || solaris)

package daytona

func flushTerminalInput(fd uintptr) {}
