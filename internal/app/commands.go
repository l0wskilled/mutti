package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/l0wskilled/mutti/internal/extend"
)

// RunCommand runs a custom command: a host command as a process in the worktree root, a
// service command's script with sh (or bash) inside the service, as the configured user. Both
// get the MUTTI_* variables; the exit code is the command's.
func (a *App) RunCommand(ctx context.Context, dir string, c extend.Command, args []string) (int, error) {
	if c.Service != "" {
		script, err := os.ReadFile(c.Path)
		if err != nil {
			return 0, err
		}
		cmd := append([]string{extend.Interpreter(script), "-c", string(script), c.Name}, args...)
		return a.Exec(ctx, dir, c.Service, cmd, ExecOptions{})
	}

	inst, err := a.Resolve(ctx, dir)
	if err != nil {
		return 0, err
	}
	a.touch(inst.Name)
	argv := append([]string{c.Path}, args...)
	if fi, err := os.Stat(c.Path); err != nil {
		return 0, err
	} else if fi.Mode().Perm()&0o111 == 0 {
		argv = append([]string{"sh"}, argv...) // not executable: still runs as a shell script
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = inst.Root
	cmd.Env = append(os.Environ(), a.Env(inst)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.IO.In, a.IO.Out, a.IO.Err
	code, err := runCommand(cmd)
	if err != nil {
		return 0, fmt.Errorf("run %s: %w", c.Name, err)
	}
	return code, nil
}
