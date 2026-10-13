package app

import (
	"errors"
	"os/exec"
	"time"
)

// stopGrace is how long a command the user started gets to finish after mutti asked it to stop.
var stopGrace = 10 * time.Second

// runCommand runs a command the user asked for, made with exec.CommandContext. When the context
// ends (mutti got a signal), the command is asked to stop and killed only after stopGrace. The
// exit code follows the shell's convention: 128+n for a command ended by signal n.
func runCommand(cmd *exec.Cmd) (int, error) {
	cmd.Cancel = func() error { return terminate(cmd.Process) }
	cmd.WaitDelay = stopGrace
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitCode(exitErr), nil
	}
	return 0, err
}
