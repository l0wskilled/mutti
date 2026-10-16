// Package tui is mutti's terminal UI: every instance at a glance, its services, what is wrong
// with them, and every mutti command a key or a short search away.
package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"github.com/l0wskilled/mutti/internal/app"
)

// Command is a mutti command the palette offers.
type Command struct {
	Path  []string // the words that call it, e.g. ["router", "status"]
	Use   string   // the path with its arguments, e.g. "exec <service> -- <command...>"
	Short string
}

// takesArgs reports whether the command wants arguments or has flags.
func (c Command) takesArgs() bool {
	for _, r := range c.Use {
		if r == '<' || r == '[' {
			return true
		}
	}
	return false
}

// Options configure the UI.
type Options struct {
	App      *app.App
	Dir      string // where mutti tui runs; its instance is selected first
	Self     string // the mutti binary, which runs the commands that take over the terminal
	Version  string
	Commands []Command
}

// Run shows the UI until the user quits or ctx ends.
func Run(ctx context.Context, opts Options, in io.Reader, out io.Writer) error {
	_, err := tea.NewProgram(newModel(ctx, opts), tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)).Run()
	if errors.Is(err, context.Canceled) {
		return nil // mutti was asked to stop: the UI ends with it
	}
	return err
}

// pausedCommand runs a command in the terminal and waits for enter afterwards, so its output
// can be read before the UI takes the screen back.
type pausedCommand struct {
	cmd *exec.Cmd
	in  io.Reader
	out io.Writer
}

func (p *pausedCommand) SetStdin(r io.Reader)  { p.in, p.cmd.Stdin = r, r }
func (p *pausedCommand) SetStdout(w io.Writer) { p.out, p.cmd.Stdout = w, w }
func (p *pausedCommand) SetStderr(w io.Writer) { p.cmd.Stderr = w }

func (p *pausedCommand) Run() error {
	err := p.cmd.Run()
	state := "done"
	if err != nil {
		state = err.Error()
	}
	fmt.Fprintf(p.out, "\n\x1b[2m── %s · press enter to return to mutti\x1b[0m", state)
	_, _ = bufio.NewReader(p.in).ReadString('\n')
	return err
}

// execDoneMsg reports that a command run in the terminal ended.
type execDoneMsg struct {
	what string
	err  error
}

// runInTerminal hands the terminal to `mutti -C dir args...` until it ends.
func (m *model) runInTerminal(dir string, args ...string) tea.Cmd {
	what := "mutti " + joinArgs(args)
	c := exec.CommandContext(m.ctx, m.opts.Self, append([]string{"-C", dir}, args...)...) //nolint:gosec // mutti runs itself
	return tea.Exec(&pausedCommand{cmd: c}, execDone(what))
}

func execDone(what string) tea.ExecCallback {
	return func(err error) tea.Msg { return execDoneMsg{what: what, err: err} }
}
