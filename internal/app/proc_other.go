//go:build !unix

package app

import (
	"os"
	"os/exec"
)

func terminate(p *os.Process) error { return p.Kill() }

func exitCode(e *exec.ExitError) int { return e.ExitCode() }
