package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/l0wskilled/mutti/internal/app"
)

func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "mutti"
	return v
}

func (m *model) render() string {
	if m.width == 0 {
		return "mutti is waking up…"
	}
	header, footer := m.header(), m.footer()
	h := max(m.height-lipgloss.Height(header)-lipgloss.Height(footer), 3)
	var body string
	switch m.mode {
	case modeMain:
		body = m.mainView(h)
	case modeLogs, modeDiagnose:
		body = m.viewerView(h)
	default:
		body = lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, m.overlay())
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *model) header() string {
	s := m.styles
	running, care := 0, 0
	for _, i := range m.instances {
		if i.Status == app.StatusRunning || i.Status == app.StatusPartial {
			running++
		}
		if i.Status == app.StatusPartial || i.Orphaned {
			care++
		}
	}
	summary := fmt.Sprintf("%d instances · %d running", len(m.instances), running)
	if care > 0 {
		summary += s.errText.Render(fmt.Sprintf(" · %d need care", care))
	}
	left := " " + s.brand.Render("◆ mutti") + s.faintText.Render(" "+m.opts.Version) + "   " + s.muted.Render(summary)
	right := ""
	switch {
	case m.busy != "":
		right = s.accentText.Render(m.spinner.View() + " " + m.busy)
	case m.toast != "" && m.toastOK:
		right = s.okText.Render("✓ " + m.toast)
	case m.toast != "":
		right = s.errText.Render("✗ " + m.toast)
	case m.listErr != nil:
		right = s.errText.Render("✗ " + firstLine(m.listErr.Error()))
	}
	right = trunc(right, max(m.width-lipgloss.Width(left)-2, 0)) + " "
	gap := max(m.width-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", gap) + right
}

func (m *model) footer() string {
	return " " + m.help.ShortHelpView(m.keys.ShortHelp())
}

func (m *model) mainView(h int) string {
	if len(m.instances) == 0 {
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, m.welcome())
	}
	lw := min(max(m.width/3, 28), 44)
	rw := m.width - lw
	left, right := m.styles.pane, m.styles.pane
	if m.focus == paneInstances {
		left = m.styles.focusedPane
	} else {
		right = m.styles.focusedPane
	}
	// Width and height count the border.
	return lipgloss.JoinHorizontal(lipgloss.Top,
		left.Width(lw).Height(h).Render(clip(m.withCheatSheet(m.instancesView(lw-4), h-2), h-2)),
		right.Width(rw).Height(h).Render(clip(m.servicesView(rw-4), h-2)),
	)
}

// withCheatSheet puts what mutti can do below the instances, where there is room.
func (m *model) withCheatSheet(instances string, h int) string {
	s := m.styles
	k := func(key, what string) string { return s.key.Render(key) + " " + s.muted.Render(what) }
	sheet := []string{
		s.muted.Render("WHAT YOU CAN DO"),
		s.faintText.Render("instance, or the selected service"),
		"  " + k("u", "start") + "  " + k("s", "stop") + "  " + k("r", "restart"),
		"  " + k("l", "logs") + "  " + k("i", "diagnose") + "  " + k("o", "open"),
		s.faintText.Render("service"),
		"  " + k("enter", "shell") + "  " + k("e", "exec") + "  " + k("y", "copy URL"),
		s.faintText.Render("instance"),
		"  " + k("d", "down") + "  " + k("R", "reset") + "  " + k("X", "destroy"),
		"  " + k("n", "new worktree"),
		s.faintText.Render("everything else"),
		"  " + k(":", "search every mutti command"),
		"  " + k("?", "help"),
	}
	free := h - lipgloss.Height(instances) - len(sheet)
	if free < 1 {
		return instances
	}
	return instances + strings.Repeat("\n", free) + strings.Join(sheet, "\n")
}

func (m *model) welcome() string {
	s := m.styles
	return s.overlay.Render(strings.Join([]string{
		s.renderLogo() + s.title.Render(" has no instances yet"),
		"",
		s.muted.Render("In a project with a Compose file, start one:"),
		s.accentText.Render("  mutti up"),
		"",
		s.muted.Render("Press ") + s.key.Render(":") + s.muted.Render(" to see everything mutti does."),
	}, "\n"))
}

func (m *model) instancesView(w int) string {
	s := m.styles
	lines := []string{s.muted.Render("INSTANCES"), ""}
	for _, i := range m.instances {
		marker, name := "  ", s.title.Render(i.Name)
		if i.Path == m.selected {
			marker = s.cursor.Render("› ")
			name = s.cursor.Render(i.Name)
		}
		count := s.muted.Render(fmt.Sprintf("%d/%d", i.Running, i.Total))
		first := marker + s.dot(i.Status) + " " + name
		lines = append(lines, padBetween(trunc(first, w-lipgloss.Width(count)-1), count, w))
		detail := i.Branch
		if i.Orphaned {
			detail = s.errText.Render("worktree gone · mutti tidy")
		}
		lines = append(lines, "    "+s.faintText.Render(trunc(detail, w-4)))
	}
	return strings.Join(lines, "\n")
}

func (m *model) servicesView(w int) string {
	s := m.styles
	c := m.current()
	if c == nil {
		return s.muted.Render("Select an instance.")
	}
	lines := []string{s.title.Render(c.Name) + "  " + s.muted.Render(statusWord(c.Status))}
	if len(c.URLs) > 0 {
		lines = append(lines, s.accentText.Render(trunc(c.URLs[0], w)))
	}
	lines = append(lines, s.faintText.Render(trunc(c.Path, w)), "")
	switch {
	case m.statusErr != nil:
		return strings.Join(append(lines, s.errText.Render(m.statusErr.Error())), "\n")
	case m.status == nil:
		return strings.Join(append(lines, s.muted.Render(m.spinner.View()+" loading…")), "\n")
	}
	svcs := m.services()
	nameW := 0
	for _, svc := range svcs {
		nameW = max(nameW, len(svc.Service))
	}
	group := ""
	for i, svc := range svcs {
		if p := firstProfile(svc); p != group {
			group = p
			lines = append(lines, "", s.muted.Render("profile "+p+" · optional"))
		}
		kind, word := badge(svc)
		marker, name := "  ", svc.Service
		if m.focus == paneServices && i == m.svcCursor {
			marker, name = s.cursor.Render("› "), s.selectedRow.Render(svc.Service)
		}
		row := fmt.Sprintf("%s%s  %s  %s", marker, name+strings.Repeat(" ", nameW-len(svc.Service)), s.badge(kind).Render(fmt.Sprintf("%-10s", word)), s.faintText.Render(svc.Status))
		if _, sick := m.diags[svc.Service]; sick {
			row += s.errText.Render("  !")
		}
		lines = append(lines, trunc(row, w))
	}
	lines = append(lines, "", m.detailView(w))
	return strings.Join(lines, "\n")
}

// detailView tells about the selected service, or what needs care.
func (m *model) detailView(w int) string {
	s := m.styles
	svc := m.service()
	if svc == "" {
		if n := len(m.diags); n > 0 {
			return s.errText.Render(fmt.Sprintf("%d service(s) need care", n)) + s.muted.Render(" · tab to pick one, i to diagnose")
		}
		return s.muted.Render("tab: pick a service · enter: shell · u/s/r: start, stop, restart")
	}
	if d, ok := m.diags[svc]; ok {
		return s.diag.Width(w).Render(m.renderDiagnosisShort(d, w-2))
	}
	var lines []string
	for _, st := range m.services() {
		if st.Service != svc {
			continue
		}
		for _, u := range st.URLs {
			lines = append(lines, s.muted.Render("url   ")+s.accentText.Render(u))
		}
		for _, p := range st.Ports {
			lines = append(lines, s.muted.Render(fmt.Sprintf("port  %d → ", p.ContainerPort))+p.Address)
		}
	}
	lines = append(lines, s.muted.Render("enter: shell · e: exec · l: logs · i: diagnose"))
	return strings.Join(lines, "\n")
}

// renderDiagnosisShort fits a diagnosis below the services.
func (m *model) renderDiagnosisShort(d app.Diagnosis, w int) string {
	s := m.styles
	// Problem and hint wrap; they are the point. Output lines are cut, they are evidence.
	lines := []string{s.errText.Bold(true).Width(w).Render(d.Problem)}
	if d.Hint != "" {
		lines = append(lines, s.accentText.Width(w).Render("→ "+d.Hint))
	}
	if n := len(d.Health); n > 0 {
		lines = append(lines, s.muted.Render("health check:"))
		for _, l := range strings.Split(lastLines(strings.TrimSpace(d.Health[n-1].Output), 3), "\n") {
			lines = append(lines, s.code.Render(trunc("  "+l, w)))
		}
	}
	if logs := strings.TrimSpace(d.Logs); logs != "" {
		lines = append(lines, s.muted.Render("last logs:"))
		for _, l := range strings.Split(lastLines(logs, 6), "\n") {
			lines = append(lines, s.code.Render(trunc("  "+l, w)))
		}
	}
	return strings.Join(lines, "\n")
}

func (m *model) viewerView(h int) string {
	s := m.styles
	hint := "↑/↓ scroll · esc back"
	if m.mode == modeLogs {
		follow := "off"
		if m.follow {
			follow = "on"
		}
		hint = "↑/↓ scroll · f follow (" + follow + ") · esc back"
	}
	title := " " + s.title.Render(m.viewTitle) + "  " + s.muted.Render(hint)
	m.viewer.SetHeight(max(h-2, 1))
	return lipgloss.JoinVertical(lipgloss.Left, title, s.pane.Width(m.width).Render(m.viewer.View()))
}

func (m *model) overlay() string {
	s := m.styles
	switch m.mode {
	case modePalette:
		return s.overlay.Render(m.palette.View())
	case modePrompt:
		return s.overlay.Render(s.title.Render(m.prompt.title) + "\n\n" + m.input.View() + "\n\n" + s.muted.Render("enter: run · esc: cancel"))
	case modeConfirm:
		return s.overlay.BorderForeground(s.red).Render(s.title.Render(m.confirm.question) + "\n\n" + s.key.Render("y") + s.muted.Render(" yes · any other key: no"))
	case modeHelp:
		return s.overlay.Render(s.renderLogo() + s.title.Render(" keys") + "\n\n" + m.help.FullHelpView(m.keys.FullHelp()) + "\n\n" +
			s.muted.Render("Every mutti command, searchable: ") + s.key.Render(":"))
	}
	return s.overlay.Render(uwu(s))
}

// badge is the kind and word of a service's state.
func badge(svc app.ServiceStatus) (string, string) {
	switch {
	case len(svc.Profiles) > 0 && svc.State != "running":
		return "off", "off"
	case svc.State == "missing":
		return "missing", "not created"
	case strings.Contains(svc.Status, "(unhealthy)"):
		return "unhealthy", "unhealthy"
	case strings.Contains(svc.Status, "(healthy)"):
		return "healthy", "healthy"
	case strings.Contains(svc.Status, "health: starting"):
		return "starting", "starting"
	case svc.Completed:
		return "done", "done"
	case svc.State == "exited":
		if strings.HasPrefix(svc.Status, "Exited (0)") || strings.HasPrefix(svc.Status, "Exited (143)") {
			return "stopped", "stopped"
		}
		return "crashed", "crashed"
	}
	return svc.State, svc.State
}

func firstProfile(svc app.ServiceStatus) string {
	if len(svc.Profiles) == 0 {
		return ""
	}
	return svc.Profiles[0]
}

func statusWord(status string) string {
	return map[string]string{app.StatusRunning: "running", app.StatusPartial: "partly running", app.StatusStopped: "stopped", app.StatusDown: "down"}[status]
}

func trunc(s string, w int) string { return ansi.Truncate(s, max(w, 0), "…") }

// padBetween puts left and right on a line of width w.
func padBetween(left, right string, w int) string {
	return left + strings.Repeat(" ", max(w-lipgloss.Width(left)-lipgloss.Width(right), 1)) + right
}

// clip keeps the first h lines.
func clip(s string, h int) string {
	lines := strings.Split(s, "\n")
	return strings.Join(lines[:min(len(lines), max(h, 0))], "\n")
}
