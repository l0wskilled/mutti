package tui

import (
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// animateEvery is how often the states that change move; tests shorten it.
var animateEvery = 120 * time.Millisecond

type animateMsg struct{}

// animated reports whether a service is in a state that moves: starting, restarting, unhealthy.
func (m *model) animated() bool {
	for _, svc := range m.services() {
		if kind, _ := badge(svc); kind == "starting" || kind == "restarting" || kind == "unhealthy" {
			return true
		}
	}
	return false
}

// animate keeps the animation going while something moves, and only then.
func (m *model) animate() tea.Cmd {
	if m.animating || !m.animated() {
		return nil
	}
	m.animating = true
	return tea.Tick(animateEvery, func(time.Time) tea.Msg { return animateMsg{} })
}

func (m *model) nextFrame() tea.Cmd {
	m.frame++
	m.animating = false
	return m.animate()
}

// badgeWord is the word of a state, with a spinner while it changes.
func (m *model) badgeWord(kind, word string) string {
	if kind == "starting" || kind == "restarting" {
		frames := spinner.MiniDot.Frames
		return frames[m.frame%len(frames)] + " " + word
	}
	return word
}

// badgeStyle is the style of a state; an unhealthy one pulses.
func (m *model) badgeStyle(kind string) lipgloss.Style {
	st := m.styles.badge(kind)
	if kind == "unhealthy" {
		return st.Bold(m.frame/5%2 == 0)
	}
	return st
}
