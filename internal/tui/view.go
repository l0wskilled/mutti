package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/l0wskilled/mutti/internal/app"
)

func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
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
	case modeMain, modeFilter:
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
	if m.narrow() {
		summary = fmt.Sprintf("%d/%d running", running, len(m.instances))
	}
	if care > 0 {
		summary += s.errText.Render(fmt.Sprintf(" · %d need care", care))
	}
	left := " " + s.brand.Render("◆ mutti") + s.faintText.Render(" "+strings.TrimSuffix(m.opts.Version, " (none)")) + "   " + s.muted.Render(summary) + "   " + m.health()
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

// health tells in the header whether Docker answers and the router runs.
func (m *model) health() string {
	s := m.styles
	if dockerDown(m.listErr) {
		return s.errText.Render("● docker unreachable")
	}
	out := s.okText.Render("●") + s.muted.Render(" docker")
	switch {
	case m.router == nil:
	case m.router.Status == app.CheckOK:
		out += "  " + s.okText.Render("●") + s.muted.Render(" router")
	case m.router.Status == app.CheckWarn:
		out += "  " + s.badge("starting").Render("● router "+lastWord(m.router.Detail))
	default:
		out += "  " + s.errText.Render("● router failing")
	}
	return out
}

// lastWord is the state at the end of a check's detail, e.g. "exited" of "... :443 (https), exited".
func lastWord(detail string) string {
	if i := strings.LastIndex(detail, ", "); i >= 0 {
		return detail[i+2:]
	}
	return detail
}

// dockerDown reports whether err says that Docker does not answer.
func dockerDown(err error) bool {
	var e *app.Error
	return errors.As(err, &e) && e.Code == app.CodeDockerUnavailable
}

// listFailed is the main view while the instances cannot be listed.
func (m *model) listFailed() string {
	s := m.styles
	title, hint := "mutti cannot list the instances", "mutti tries again every few seconds."
	if dockerDown(m.listErr) {
		title, hint = "Docker does not answer", "Start Docker; mutti looks again every few seconds."
	}
	return s.overlay.BorderForeground(s.red).Render(strings.Join([]string{
		s.errText.Bold(true).Render(title),
		"",
		s.muted.Width(min(m.width-12, 70)).Render(m.listErr.Error()),
		"",
		s.muted.Render(hint),
	}, "\n"))
}

func (m *model) welcome() string {
	s := m.styles
	return s.overlay.Render(strings.Join([]string{
		s.renderLogo() + s.title.Render(" has no instances yet"),
		"",
		s.accentText.Render("mutti up") + s.muted.Render(" in a project with a Compose file starts one."),
	}, "\n"))
}

// facts are what the selected service runs, since when and what it uses.
func (m *model) facts(svc string) []string {
	s := m.styles
	d, ok := m.details[svc]
	if !ok {
		return nil
	}
	label := func(l string) string { return s.muted.Render(fmt.Sprintf("%-7s", l)) }
	lines := []string{label("image") + d.Image}
	u := m.usage[svc]
	if u != nil && !d.StartedAt.IsZero() {
		up := label("up") + humanDuration(time.Since(d.StartedAt))
		if d.RestartCount > 0 {
			up += s.errText.Render(fmt.Sprintf(" · restarted %d times", d.RestartCount))
		}
		lines = append(lines, up)
	}
	if h := d.Health; h != nil && u != nil { // a stopped one's last check is old news
		state := s.okText.Render("passing")
		if h.ExitCode != 0 {
			state = s.errText.Render(fmt.Sprintf("failing, exit %d", h.ExitCode))
		}
		lines = append(lines, label("health")+state+s.faintText.Render(" · "+humanDuration(time.Since(h.Time))+" ago"))
	}
	if u != nil {
		cpu := "…"
		if len(u.cpu) > 0 {
			cpu = fmt.Sprintf("%-14s", fmt.Sprintf("%.1f%%", last(u.cpu))) + s.accentText.Render(cpuLine(u.cpu, len(u.cpu)))
		}
		mem := fmt.Sprintf("%-14s", humanBytes(last(u.mem))+" of "+humanBytes(float64(u.last.MemoryLimit)))
		lines = append(lines, label("cpu")+cpu, label("memory")+mem+s.accentText.Render(memLine(u.mem, len(u.mem))))
	}
	return lines
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

// badge is the kind and word of a service's state. An optional service that is not running is
// off, unless it crashed.
func badge(svc app.ServiceStatus) (string, string) {
	optional := len(svc.Profiles) > 0
	switch {
	case svc.State == "missing" && optional:
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
	case svc.State == "exited" && !strings.HasPrefix(svc.Status, "Exited (0)") && !strings.HasPrefix(svc.Status, "Exited (143)"):
		return "crashed", "crashed"
	case optional && svc.State != "running":
		return "off", "off"
	case svc.State == "exited":
		return "stopped", "stopped"
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
