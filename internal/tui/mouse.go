package tui

import tea "charm.land/bubbletea/v2"

// paneTop is the screen line the content of the panes starts on: below the header and the
// border.
const paneTop = 2

// click selects the instance or the service under the mouse in the main view.
func (m *model) click(at tea.Mouse) tea.Cmd {
	if m.mode != modeMain || at.Button != tea.MouseLeft {
		return nil
	}
	line := at.Y - paneTop
	if at.X < m.split {
		path, ok := m.instanceAt[line]
		if !ok {
			return nil
		}
		m.focus = paneInstances
		if path == m.selected {
			return nil
		}
		return m.selectInstance(path)
	}
	if i, ok := m.serviceAt[line]; ok {
		m.focus, m.svcCursor = paneServices, i
	}
	return nil
}

// wheel moves through the pane under the mouse.
func (m *model) wheel(at tea.Mouse) tea.Cmd {
	by := 1
	if at.Button == tea.MouseWheelUp {
		by = -1
	}
	m.focus = paneInstances
	if at.X >= m.split && len(m.services()) > 0 {
		m.focus = paneServices
	}
	return m.move(by)
}
