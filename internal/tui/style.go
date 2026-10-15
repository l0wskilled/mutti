package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// styles are the looks of the UI, in the colors of the web UI, for a light or a dark terminal.
type styles struct {
	accent, green, amber, red, text, dim, faint, border color.Color

	title, muted, faintText, accentText, key, errText, okText lipgloss.Style
	pane, focusedPane, overlay, diag, code                    lipgloss.Style
	cursor, selectedRow, brand                                lipgloss.Style
	logo                                                      []color.Color
}

func newStyles(dark bool) styles {
	pick := lipgloss.LightDark(dark)
	s := styles{
		accent: pick(lipgloss.Color("#4f46e5"), lipgloss.Color("#8b85ff")),
		green:  pick(lipgloss.Color("#12a150"), lipgloss.Color("#3ccf7e")),
		amber:  pick(lipgloss.Color("#c27803"), lipgloss.Color("#f0b44c")),
		red:    pick(lipgloss.Color("#d93b3b"), lipgloss.Color("#ff6b6b")),
		text:   pick(lipgloss.Color("#14161a"), lipgloss.Color("#eceef2")),
		dim:    pick(lipgloss.Color("#5b6170"), lipgloss.Color("#a3a9b6")),
		faint:  pick(lipgloss.Color("#8b91a0"), lipgloss.Color("#6f7685")),
		border: pick(lipgloss.Color("#d5d9e0"), lipgloss.Color("#2a2e37")),
	}
	s.title = lipgloss.NewStyle().Bold(true).Foreground(s.text)
	s.muted = lipgloss.NewStyle().Foreground(s.dim)
	s.faintText = lipgloss.NewStyle().Foreground(s.faint)
	s.accentText = lipgloss.NewStyle().Foreground(s.accent)
	s.key = lipgloss.NewStyle().Bold(true).Foreground(s.accent)
	s.errText = lipgloss.NewStyle().Foreground(s.red)
	s.okText = lipgloss.NewStyle().Foreground(s.green)
	s.pane = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(s.border).Padding(0, 1)
	s.focusedPane = s.pane.BorderForeground(s.accent)
	s.overlay = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(s.accent).Padding(1, 2)
	s.diag = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(s.red).PaddingLeft(1)
	s.code = lipgloss.NewStyle().Foreground(s.dim)
	s.cursor = lipgloss.NewStyle().Bold(true).Foreground(s.accent)
	s.selectedRow = lipgloss.NewStyle().Bold(true).Foreground(s.text)
	s.brand = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Background(s.accent).Padding(0, 1)
	s.logo = lipgloss.Blend1D(5, lipgloss.Color("#6d5dfc"), lipgloss.Color("#c06cff"), lipgloss.Color("#ff7eb6"))
	return s
}

// badge is the colored word for a service state.
func (s styles) badge(kind string) lipgloss.Style {
	c := map[string]color.Color{
		"healthy": s.green, "running": s.green, "starting": s.amber, "restarting": s.amber,
		"unhealthy": s.red, "crashed": s.red, "missing": s.red,
	}[kind]
	if c == nil {
		c = s.faint
	}
	return lipgloss.NewStyle().Foreground(c)
}

// dot is the colored status dot of an instance.
func (s styles) dot(status string) string {
	c := map[string]color.Color{"running": s.green, "partial": s.amber, "stopped": s.faint, "down": s.faint}[status]
	if c == nil {
		c = s.faint
	}
	return lipgloss.NewStyle().Foreground(c).Render("●")
}

// renderLogo writes "mutti" in a gradient.
func (s styles) renderLogo() string {
	out := ""
	for i, r := range "mutti" {
		out += lipgloss.NewStyle().Bold(true).Foreground(s.logo[i]).Render(string(r))
	}
	return out
}
