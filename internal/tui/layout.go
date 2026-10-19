package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"

	"github.com/l0wskilled/mutti/internal/app"
)

func (m *model) footer() string {
	hint := func(keys, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(keys), key.WithHelp(keys, desc))
	}
	var keys []key.Binding
	switch m.mode {
	case modeLogs:
		follow := "off"
		if m.follow {
			follow = "on"
		}
		keys = []key.Binding{hint("↑/↓", "scroll"), hint("/", "filter"), hint("f", "follow ("+follow+")"), hint("esc", "back")}
		if m.logFiltering {
			keys = []key.Binding{hint("enter", "keep"), hint("esc", "clear")}
		}
	case modeDiagnose:
		keys = []key.Binding{hint("↑/↓", "scroll"), hint("esc", "back")}
	case modeFilter:
		keys = []key.Binding{hint("↑/↓", "move"), hint("enter", "keep"), hint("esc", "clear")}
	case modeMain:
		keys = m.keys.footer(m.focus, len(m.marks()) > 0)
	default:
		keys = []key.Binding{hint("esc", "close")}
	}
	return m.wrapKeys(keys)
}

// wrapKeys lays the keys out like the short help, on as many lines as they need.
func (m *model) wrapKeys(keys []key.Binding) string {
	st := m.help.Styles
	sep := st.ShortSeparator.Inline(true).Render(m.help.ShortSeparator)
	var lines []string
	line := ""
	for _, k := range keys {
		item := st.ShortKey.Inline(true).Render(k.Help().Key) + " " + st.ShortDesc.Inline(true).Render(k.Help().Desc)
		switch {
		case line == "":
			line = " " + item
		case lipgloss.Width(line+sep+item) < m.width:
			line += sep + item
		default:
			lines, line = append(lines, line), " "+item
		}
	}
	return strings.Join(append(lines, line), "\n")
}

// box is a pane: a rounded border with the title in its top edge, in the accent color while
// the pane has the keys. Width and height count the border.
func (m *model) box(title string, w, h int, body string, focused bool) string {
	s := m.styles
	color, titled := s.border, s.muted
	if focused {
		color, titled = s.accent, s.key
	}
	edge := lipgloss.NewStyle().Foreground(color)
	label := ""
	if title != "" {
		label = " " + titled.Render(trunc(title, w-6)) + " "
	}
	top := edge.Render("╭─") + label + edge.Render(strings.Repeat("─", max(w-3-lipgloss.Width(label), 0))+"╮")
	lines := strings.Split(clip(body, h-2), "\n")
	for i, l := range lines {
		lines[i] = trunc(l, w-4) // cut, not wrapped: a wrapped line would push the rest out
	}
	rest := lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), false, true, true, true).BorderForeground(color).Padding(0, 1).
		Width(w).Height(max(h-1, 1)).Render(strings.Join(lines, "\n"))
	return top + "\n" + rest
}

func (m *model) mainView(h int) string {
	switch {
	case len(m.instances) == 0 && m.listErr != nil:
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, m.listFailed())
	case len(m.instances) == 0:
		return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, m.welcome())
	}
	// A narrow terminal shows one pane at a time, the one with the keys.
	if m.narrow() {
		if m.focus == paneInstances {
			m.split = m.width
			return m.box("INSTANCES", m.width, h, m.instancesView(m.width-4), true)
		}
		m.split = 0
		return m.rightView(m.width, h)
	}
	lw := min(max(m.width/5, 26), 36)
	m.split = lw
	left := m.box("INSTANCES", lw, h, m.instancesView(lw-4), m.focus == paneInstances)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, m.rightView(m.width-lw, h))
}

// narrow reports whether the terminal is too narrow for the panes side by side.
func (m *model) narrow() bool { return m.width < 90 }

func (m *model) instancesView(w int) string {
	s := m.styles
	var lines []string
	shown := m.shown()
	m.instanceAt = map[int]string{}
	if m.mode == modeFilter || m.filter.Value() != "" {
		count := s.faintText.Render(fmt.Sprintf("%d of %d", len(shown), len(m.instances)))
		lines = append(lines, padBetween(trunc(m.filter.View(), w-lipgloss.Width(count)-1), count, w), "")
		if len(shown) == 0 {
			lines = append(lines, s.muted.Render("nothing matches"))
		}
	}
	for _, i := range shown {
		selected := i.Path == m.selected
		bar := selected && m.focus == paneInstances
		paint := func(st lipgloss.Style, text string) string {
			if bar {
				st = st.Background(s.sel)
			}
			return st.Render(text)
		}
		plain := lipgloss.NewStyle()
		cursor, name := " ", s.title.Bold(false)
		if selected {
			cursor, name = "›", s.cursor
			if !bar { // the services have the keys
				name = s.selectedRow
			}
		}
		count := fmt.Sprintf("%d/%d", i.Running, i.Total)
		first := paint(s.cursor, cursor+" ") + paint(lipgloss.NewStyle().Foreground(s.dotColor(i.Status)), "●") + paint(plain, " ") +
			paint(name, trunc(i.Name, w-5-len(count)))
		first += paint(plain, strings.Repeat(" ", max(w-lipgloss.Width(first)-len(count), 1))) + paint(s.muted, count)
		// The branch where it says more than the name.
		second, secondStyle := statusWord(i.Status), s.faintText
		if i.Branch != "" && !strings.HasSuffix(i.Name, i.Branch) {
			second += " · " + i.Branch
		}
		if i.Orphaned {
			second, secondStyle = "worktree gone", s.errText
		}
		second = paint(plain, "    ") + paint(secondStyle, trunc(second, w-4))
		second += paint(plain, strings.Repeat(" ", max(w-lipgloss.Width(second), 0)))
		m.instanceAt[len(lines)], m.instanceAt[len(lines)+1] = i.Path, i.Path
		lines = append(lines, first, second)
	}
	return strings.Join(lines, "\n")
}

// rightView is the services of the selected instance and, below them, the selected service.
func (m *model) rightView(w, h int) string {
	c := m.current()
	m.serviceAt = map[int]int{}
	if c == nil {
		return m.box("", w, h, m.styles.muted.Render("No instance selected."), false)
	}
	title := c.Name + " · " + statusWord(c.Status)
	lines, ok := m.servicesHead(c, w-4)
	if !ok {
		return m.box(title, w, h, strings.Join(lines, "\n"), m.focus == paneServices)
	}
	rows, cursorLine := m.serviceRows(w - 4)
	detailTitle, detail := m.detailView(w - 4)
	// The services get the room they need, the selected service the rest, at least a third.
	topH := min(len(lines)+len(rows)+2, h-max(h/3, 6))
	if detail == "" || h < 16 { // too low for both: the services win
		topH = h
	}
	// Scrolled so the cursor stays in view.
	room := max(topH-2-len(lines), 1)
	offset := max(cursorLine-room+1, 0)
	for _, r := range rows[offset:min(offset+room, len(rows))] {
		if r.service >= 0 {
			m.serviceAt[len(lines)] = r.service
		}
		lines = append(lines, r.text)
	}
	top := m.box(title, w, topH, strings.Join(lines, "\n"), m.focus == paneServices)
	if topH == h {
		return top
	}
	return lipgloss.JoinVertical(lipgloss.Left, top, m.box(detailTitle, w, h-topH, detail, false))
}

// servicesHead is the top of the services pane: where the instance lives, then the column
// heads. ok is false when there are no services to show, and the lines say why.
func (m *model) servicesHead(c *app.Summary, w int) ([]string, bool) {
	s := m.styles
	where := s.faintText.Render(c.Path)
	if len(c.URLs) > 0 {
		where = s.accentText.Render(c.URLs[0]) + "  " + where
	}
	lines := []string{trunc(where, w), ""}
	switch {
	case c.Orphaned:
		return append(lines,
			s.errText.Render("The worktree is gone."),
			s.muted.Width(w).Render("Its containers, networks and data are left over."),
			"",
			s.key.Render("X")+s.muted.Render(" removes them · ")+s.key.Render("T")+s.muted.Render(" tidies every deleted worktree")), false
	case m.statusErr != nil:
		return append(lines, s.errText.Width(w).Render(m.statusErr.Error())), false
	case m.status == nil:
		return append(lines, s.muted.Render(m.spinner.View()+" loading…")), false
	}
	head := fmt.Sprintf("   %-*s  %-12s  ", m.nameWidth(), "SERVICE", "STATE")
	if m.showUsage(w) {
		head += fmt.Sprintf("%-15s  %5s  ", "CPU", "MEM")
	}
	return append(lines, s.faintText.Render(trunc(head+"UP", w))), true
}

func (m *model) nameWidth() int {
	n := len("SERVICE")
	for _, svc := range m.services() {
		n = max(n, len(svc.Service))
	}
	return n
}

// showUsage reports whether the rows have room for CPU and memory, and there is some to show.
func (m *model) showUsage(w int) bool { return w >= 70 && len(m.usage) > 0 }

// serviceRow is a line of the services list; service is -1 on a group's divider.
type serviceRow struct {
	text    string
	service int
}

// serviceRows are the services, the optional ones by profile below a divider, and the line
// the cursor is on.
func (m *model) serviceRows(w int) ([]serviceRow, int) {
	s := m.styles
	var rows []serviceRow
	cursorLine, group := 0, ""
	for i, svc := range m.services() {
		if p := firstProfile(svc); p != group {
			group = p
			label := "── " + p + " · optional "
			rows = append(rows, serviceRow{"", -1}, serviceRow{s.faintText.Render(label + strings.Repeat("─", max(w-lipgloss.Width(label), 0))), -1})
		}
		selected := m.focus == paneServices && i == m.svcCursor
		if selected {
			cursorLine = len(rows)
		}
		rows = append(rows, serviceRow{m.renderService(svc, selected, w), i})
	}
	return rows, cursorLine
}

// renderService is the line of one service: the selected one on a bar, resting ones dim.
func (m *model) renderService(svc app.ServiceStatus, selected bool, w int) string {
	s := m.styles
	kind, word := badge(svc)
	resting := kind == "done" || kind == "off" || kind == "stopped"
	paint := func(st lipgloss.Style, text string) string {
		if resting {
			st = st.Foreground(s.faint)
		}
		if selected {
			st = st.Background(s.sel)
		}
		return st.Render(text)
	}
	plain := lipgloss.NewStyle()
	cursor, mark := " ", " "
	if selected {
		cursor = "›"
	}
	if m.marked[svc.Service] {
		mark = "✓"
	}
	b := paint(s.cursor, cursor+mark) + paint(plain, " ") +
		paint(s.title.Bold(selected), fmt.Sprintf("%-*s", m.nameWidth(), svc.Service)) + paint(plain, "  ") +
		paint(m.badgeStyle(kind), fmt.Sprintf("%-12s", m.badgeWord(kind, word))) + paint(plain, "  ")
	if m.showUsage(w) {
		if u := m.usage[svc.Service]; u != nil {
			cpu, spark := strings.Repeat(" ", 6), strings.Repeat(" ", 8)
			if len(u.cpu) > 0 {
				cpu, spark = fmt.Sprintf("%5.1f%%", last(u.cpu)), cpuLine(u.cpu, 8)
			}
			b += paint(plain, cpu+" ") + paint(s.accentText, spark) + paint(plain, "  ") +
				paint(s.muted, fmt.Sprintf("%5s", humanBytes(last(u.mem)))) + paint(plain, "  ")
		} else {
			b += paint(plain, strings.Repeat(" ", 15+2+5+2))
		}
	}
	b = trunc(b+paint(s.faintText, m.since(svc)), w)
	return b + paint(plain, strings.Repeat(" ", max(w-lipgloss.Width(b), 0)))
}

// since is how long a service runs, or how it ended and when.
func (m *model) since(svc app.ServiceStatus) string {
	if d, ok := m.details[svc.Service]; ok && svc.State == "running" && !d.StartedAt.IsZero() {
		return humanDuration(time.Since(d.StartedAt))
	}
	status, _, _ := strings.Cut(svc.Status, " (health")
	status = strings.TrimSuffix(strings.TrimSuffix(status, " (healthy)"), " (unhealthy)")
	status = strings.TrimPrefix(status, "Up ")
	if rest, ok := strings.CutPrefix(status, "Exited ("); ok {
		if code, ago, ok := strings.Cut(rest, ") "); ok {
			if code == "0" || code == "143" { // ended as asked
				return ago
			}
			return "exit " + code + ", " + ago
		}
	}
	return status
}

// detailView is the title and the text of the box below the services: the service under the
// cursor, the marked ones, or what needs care.
func (m *model) detailView(w int) (string, string) {
	s := m.styles
	if marks := m.marks(); len(marks) > 0 && m.focus == paneServices {
		return fmt.Sprintf("%d marked", len(marks)), s.muted.Render(strings.Join(marks, ", "))
	}
	svcs := m.services()
	if len(svcs) == 0 {
		return "", ""
	}
	if m.focus != paneServices && len(m.diags) > 0 {
		var lines []string
		for _, svc := range svcs {
			if d, ok := m.diags[svc.Service]; ok {
				lines = append(lines, s.errText.Bold(true).Render(svc.Service)+"  "+s.muted.Render(trunc(d.Problem, w-len(svc.Service)-2)))
			}
		}
		return "needs care", strings.Join(lines, "\n")
	}
	svc := svcs[min(m.svcCursor, len(svcs)-1)]
	lines := m.facts(svc.Service)
	if d, ok := m.diags[svc.Service]; ok {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		return svc.Service, strings.Join(append(lines, s.diag.Width(w).Render(m.renderDiagnosisShort(d, w-2))), "\n")
	}
	for _, u := range svc.URLs {
		lines = append(lines, s.muted.Render(fmt.Sprintf("%-7s", "url"))+s.accentText.Render(u))
	}
	for _, p := range svc.Ports {
		lines = append(lines, s.muted.Render(fmt.Sprintf("%-7s%d → ", "port", p.ContainerPort))+p.Address)
	}
	if len(lines) == 0 {
		_, word := badge(svc)
		lines = append(lines, s.muted.Render(word))
	}
	return svc.Service, strings.Join(lines, "\n")
}

func (m *model) viewerView(h int) string {
	if m.mode != modeLogs {
		m.viewer.SetHeight(max(h-2, 1))
		return m.box(m.viewTitle, m.width, h, m.viewer.View(), true)
	}
	body, viewH := "", h-2
	if m.logFiltering || m.logFilter.Value() != "" {
		body, viewH = m.logFilter.View()+"\n", viewH-1
	}
	m.viewer.SetHeight(max(viewH, 1))
	if m.follow { // the height is only known now
		m.viewer.GotoBottom()
	}
	return m.box(m.logsTitle(), m.width, h, body+m.viewer.View(), true)
}
