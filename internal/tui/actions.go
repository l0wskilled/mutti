package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/l0wskilled/mutti/internal/app"
)

// actionKey runs the action of a key on the selected instance or service.
func (m *model) actionKey(msg tea.KeyPressMsg) tea.Cmd {
	for _, a := range m.actions() {
		if key.Matches(msg, a.key) {
			return a.run()
		}
	}
	return nil
}

// action is something the UI does, with a key or from the palette.
type action struct {
	key   key.Binding
	title string
	desc  string
	run   func() tea.Cmd
}

// actions are what mutti does to the selected instance or service.
func (m *model) actions() []action {
	k := m.keys
	what, services := m.target()
	path := m.selected
	needInstance := func(run func() tea.Cmd) func() tea.Cmd {
		return func() tea.Cmd {
			if path == "" {
				return m.setToast("no instance yet: start one with mutti up in a project", false)
			}
			return run()
		}
	}
	// Most actions need the worktree; a gone one leaves only removing what is left.
	needWorktree := func(run func() tea.Cmd) func() tea.Cmd {
		return needInstance(func() tea.Cmd {
			if c := m.current(); c != nil && c.Orphaned {
				return m.setToast("the worktree of "+c.Name+" is gone: X removes what is left", false)
			}
			return run()
		})
	}
	return []action{
		{k.Start, "Start", "start the instance, or the selected service", needWorktree(func() tea.Cmd {
			return m.act("starting "+what, func(a *app.App) (string, error) {
				res, err := a.Up(m.ctx, path, app.UpOptions{Services: services})
				if err != nil {
					return "", err
				}
				return what + " is up " + res.URL, nil
			})
		})},
		{k.Stop, "Stop", "stop the instance, or the selected service; data stays", needWorktree(func() tea.Cmd {
			return m.act("stopping "+what, func(a *app.App) (string, error) {
				_, err := a.Stop(m.ctx, path, app.Selection{}, services...)
				return what + " stopped", err
			})
		})},
		{k.Restart, "Restart", "restart the instance, or the selected service", needWorktree(func() tea.Cmd {
			return m.act("restarting "+what, func(a *app.App) (string, error) {
				_, err := a.Restart(m.ctx, path, app.Selection{}, services...)
				return what + " restarted", err
			})
		})},
		{k.Down2, "Down", "remove the containers and networks; data stays", needWorktree(func() tea.Cmd {
			return m.confirmThen("Remove the containers of "+m.instanceName()+"? Its data stays.", func() tea.Cmd {
				return m.act("removing "+m.instanceName(), func(a *app.App) (string, error) {
					_, err := a.Down(m.ctx, path, app.Selection{})
					return m.instanceName() + " is down", err
				})
			})
		})},
		{k.Reset, "Reset", "delete the data and start fresh: the bootstrap runs again", needWorktree(func() tea.Cmd {
			return m.confirmThen("Delete all data of "+m.instanceName()+" and start it fresh?", func() tea.Cmd { return m.runInTerminal(path, "reset", "--yes") })
		})},
		{k.Destroy, "Destroy", "remove everything of the instance: containers, data, its name", needInstance(func() tea.Cmd {
			name, orphaned := m.instanceName(), m.current().Orphaned
			return m.confirmThen("Destroy "+name+" with all its data?", func() tea.Cmd {
				return m.act("destroying "+name, func(a *app.App) (string, error) {
					// Through Compose and the pre-down hooks where the project still works; by
					// labels where it does not, so the instance goes away whatever state it is in.
					if !orphaned {
						if _, err := a.Destroy(m.ctx, path); err == nil {
							return name + " is gone", nil
						}
					}
					return name + " is gone", a.Remove(m.ctx, name)
				})
			})
		})},
		{k.Tidy, "Tidy", "remove what deleted worktrees left behind", func() tea.Cmd {
			var gone []string
			for _, i := range m.instances {
				if i.Orphaned {
					gone = append(gone, i.Name)
				}
			}
			if len(gone) == 0 {
				return m.setToast("nothing to tidy: every instance has its worktree", true)
			}
			return m.confirmThen(fmt.Sprintf("Remove what %d deleted worktree(s) left behind?\n%s", len(gone), strings.Join(gone, ", ")), func() tea.Cmd {
				return m.act("tidying", func(a *app.App) (string, error) {
					res, err := a.Tidy(m.ctx, app.TidyOptions{Apply: true})
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("removed %d instance(s)", len(res.Removed)), nil
				})
			})
		}},
		{k.Open, "Open", "open the instance or the selected service in the browser", needWorktree(func() tea.Cmd {
			svc := m.service()
			return m.act("opening", func(a *app.App) (string, error) {
				url, err := a.Open(m.ctx, path, svc)
				return "opened " + url, err
			})
		})},
		{k.Copy, "Copy URL", "copy the URL of the instance or the selected service", needWorktree(func() tea.Cmd {
			svc := m.service()
			a, _ := m.app()
			return func() tea.Msg {
				url, err := a.URL(m.ctx, path, svc)
				return copyMsg{url: url, err: err}
			}
		})},
		{k.Logs, "Logs", "follow the logs of the instance or the selected service", needWorktree(func() tea.Cmd {
			m.logsOf = m.service()
			m.logsGen++
			m.openViewer(modeLogs, "Logs of "+what, "loading…")
			return tea.Batch(m.loadLogs(), m.logsTick())
		})},
		{k.Diagnose, "Diagnose", "explain what is wrong: health checks, exit codes, last logs", needWorktree(func() tea.Cmd {
			a, _ := m.app()
			return func() tea.Msg {
				diags, err := a.Diagnose(m.ctx, path, services...)
				return diagnosisMsg{title: "Diagnosis of " + what, diags: diags, err: err}
			}
		})},
		{k.Exec, "Exec", "run a command in the selected service", needWorktree(func() tea.Cmd {
			svc := m.service()
			if svc == "" {
				return m.setToast("select a service first: tab, then ↑/↓", false)
			}
			return m.askFor("Command to run in "+svc, "", func(cmd string) tea.Cmd {
				return m.runInTerminal(path, "exec", svc, "--", "sh", "-c", cmd)
			})
		})},
		{k.Worktree, "New worktree", "create a git worktree with its own environment, and start it", needWorktree(func() tea.Cmd {
			return m.askFor("Name of the new worktree", "", func(name string) tea.Cmd {
				return m.runInTerminal(path, "worktree", "add", name)
			})
		})},
	}
}

func (m *model) instanceName() string {
	if c := m.current(); c != nil {
		return c.Name
	}
	return "the instance"
}

// confirmThen asks a yes/no question; yes runs run.
func (m *model) confirmThen(question string, run func() tea.Cmd) tea.Cmd {
	m.mode, m.confirm = modeConfirm, confirmation{question: question, run: run}
	return nil
}

// askFor asks for a line of input, then runs with it.
func (m *model) askFor(title, value string, run func(string) tea.Cmd) tea.Cmd {
	m.mode, m.prompt = modePrompt, prompt{title: title, run: run}
	m.input.SetValue(value)
	m.input.CursorEnd()
	return m.input.Focus()
}

func (m *model) promptKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.mode = modeMain
		return nil
	case "enter":
		m.mode = modeMain
		value := strings.TrimSpace(m.input.Value())
		if value == "" {
			return m.setToast("nothing entered, nothing done", false)
		}
		return m.prompt.run(value)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

// paletteItem is a line of the palette.
type paletteItem struct {
	name, desc string
	run        func(m *model) tea.Cmd
}

func (i paletteItem) Title() string       { return i.name }
func (i paletteItem) Description() string { return i.desc }
func (i paletteItem) FilterValue() string { return i.name + "\x00" + i.desc }

// paletteItems are the actions of the keys first, then every mutti command.
func (m *model) paletteItems() []list.Item {
	var items []list.Item
	for i, a := range m.actions() {
		items = append(items, paletteItem{
			name: fmt.Sprintf("%-14s %s", a.title, a.key.Help().Key),
			desc: a.desc,
			run:  func(m *model) tea.Cmd { return m.actions()[i].run() },
		})
	}
	for _, c := range m.opts.Commands {
		items = append(items, paletteItem{name: "mutti " + c.Use, desc: c.Short, run: func(m *model) tea.Cmd { return m.command(c) }})
	}
	return items
}

// filterPalette keeps the entries that have every word of term, those with the words in their
// name first. Fuzzy matching finds too much in the long descriptions.
func filterPalette(term string, targets []string) []list.Rank {
	words := strings.Fields(strings.ToLower(term))
	var inName, inDesc []list.Rank
	for i, t := range targets {
		t = strings.ToLower(t)
		name, _, _ := strings.Cut(t, "\x00")
		all, named := true, true
		for _, w := range words {
			all = all && strings.Contains(t, w)
			named = named && strings.Contains(name, w)
		}
		switch {
		case named:
			inName = append(inName, list.Rank{Index: i})
		case all:
			inDesc = append(inDesc, list.Rank{Index: i})
		}
	}
	return append(inName, inDesc...)
}

// command runs a mutti command in the terminal, asking for its arguments first.
func (m *model) command(c Command) tea.Cmd {
	dir := m.selected
	if dir == "" {
		dir = m.opts.Dir
	}
	if !c.takesArgs() {
		return m.runInTerminal(dir, c.Name)
	}
	value := ""
	if strings.Contains(c.Use, "service") {
		value = m.service()
	}
	return m.askFor("mutti "+c.Use, value, func(args string) tea.Cmd {
		return m.runInTerminal(dir, append([]string{c.Name}, strings.Fields(args)...)...)
	})
}

// paletteKey: typing filters, enter runs the highlighted entry, esc closes.
func (m *model) paletteKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.mode = modeMain
		return nil
	case "enter":
		m.mode = modeMain
		if len(m.palette.VisibleItems()) == 0 {
			return m.setToast("nothing matches "+m.palette.FilterValue(), false)
		}
		m.palette, _ = m.palette.Update(msg) // accepts the filter
		return m.palette.SelectedItem().(paletteItem).run(m)
	}
	var cmd tea.Cmd
	m.palette, cmd = m.palette.Update(msg)
	if strings.EqualFold(m.palette.FilterValue(), "uwu") {
		m.mode = modeUwu
	}
	return cmd
}

// openViewer shows text in the logs and diagnosis view.
func (m *model) openViewer(md mode, title, text string) {
	m.mode, m.viewTitle = md, title
	m.viewer.SetContent(text)
	m.viewer.GotoTop()
}

func (m *model) viewerKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		m.mode = modeMain
		m.logsGen++
		return nil
	case "f":
		if m.mode == modeLogs {
			m.follow = !m.follow
			if m.follow {
				m.viewer.GotoBottom()
				return m.logsTick()
			}
		}
		return nil
	}
	var cmd tea.Cmd
	m.viewer, cmd = m.viewer.Update(msg)
	return cmd
}

func (m *model) loadLogs() tea.Cmd {
	path, svc := m.selected, m.logsOf
	a, out := m.app()
	return func() tea.Msg {
		args := []string{"logs", "--no-color", "--timestamps", "--tail", fmt.Sprint(logLines)}
		if svc != "" {
			args = append(args, svc)
		}
		_, _ = a.ComposePassthrough(m.ctx, path, app.Selection{}, args)
		return logsMsg{path: path, service: svc, text: out.String()}
	}
}

func (m *model) logsTick() tea.Cmd {
	gen := m.logsGen
	return tea.Tick(followLogs, func(time.Time) tea.Msg { return logsTickMsg{gen: gen} })
}

// logLine splits "web-1  | 2026-...Z message" into its parts.
var logLine = regexp.MustCompile(`^(\S+\s*\|\s)(\S+Z\s)?(.*)$`)

// colorLogs sets the service and the time apart, and shows errors and warnings in color.
func (m *model) colorLogs(text string) string {
	s := m.styles
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		msg := s.title.Faint(false).Bold(false)
		lower := strings.ToLower(l)
		switch {
		case strings.Contains(lower, "error"), strings.Contains(lower, "fatal"), strings.Contains(lower, "panic"), strings.Contains(lower, "exception"):
			msg = s.errText
		case strings.Contains(lower, "warn"):
			msg = msg.Foreground(s.amber)
		}
		if p := logLine.FindStringSubmatch(l); p != nil {
			lines[i] = s.accentText.Render(p[1]) + s.faintText.Render(p[2]) + msg.Render(p[3])
		} else {
			lines[i] = msg.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// renderDiagnoses is the text of the diagnosis view.
func (m *model) renderDiagnoses(diags []app.Diagnosis, err error) string {
	s := m.styles
	if err != nil {
		return s.errText.Render(err.Error())
	}
	if len(diags) == 0 {
		return s.okText.Render("All services are well.") + s.muted.Render("  (✿◠‿◠)")
	}
	var b strings.Builder
	for i, d := range diags {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(s.errText.Bold(true).Render("! "+d.Service) + "  " + s.title.Render(d.Problem) + "\n")
		if d.Hint != "" {
			b.WriteString(s.accentText.Render("  → "+d.Hint) + "\n")
		}
		if n := len(d.Health); n > 0 {
			last := d.Health[n-1]
			b.WriteString(s.muted.Render(fmt.Sprintf("\n  last health check · exit %d · %s", last.ExitCode, last.Time.Local().Format(time.TimeOnly))) + "\n")
			b.WriteString(indent(s.code.Render(last.Output), "    ") + "\n")
		}
		if logs := strings.TrimSpace(d.Logs); logs != "" {
			b.WriteString(s.muted.Render("\n  last logs") + "\n")
			b.WriteString(indent(m.colorLogs(logs), "    ") + "\n")
		}
	}
	return b.String()
}

func indent(text, prefix string) string {
	return prefix + strings.ReplaceAll(text, "\n", "\n"+prefix)
}

func lastLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	return strings.Join(lines[max(len(lines)-n, 0):], "\n")
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func joinArgs(args []string) string { return strings.Join(args, " ") }
