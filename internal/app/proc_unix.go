//go:build unix

package app

import (
	"os"
	"os/exec"
	"syscall"
)

func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

func exitCode(e *exec.ExitError) int {
	if ws, ok := e.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return e.ExitCode()
}
