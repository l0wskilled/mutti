package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/l0wskilled/mutti/internal/app"
)

func TestMouse(t *testing.T) {
	w := setup(t)
	w.m.instances = append(w.m.instances, app.Summary{Name: "blog", Path: "/blog"})
	w.m.status.Services = append(w.m.status.Services, app.ServiceStatus{Service: "db", State: "running"})
	if v := w.m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatal("mouse mode")
	}
	click := func(x, y int) tea.Cmd {
		w.m.View() // where things are comes from the last render
		_, cmd := w.m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		return cmd
	}
	// The services pane: the line of db.
	db := -1
	for line, i := range w.m.serviceAt {
		if i == 1 {
			db = line
		}
	}
	click(w.m.split+5, db+paneTop)
	if w.m.focus != paneServices || w.m.service() != "db" {
		t.Fatalf("db: %v %q", w.m.focus, w.m.service())
	}
	click(w.m.split+5, 0) // the header
	click(1, 0)
	if w.m.service() != "db" {
		t.Error("nothing there")
	}
	// The instances pane: the selected one only takes the focus, another one is selected.
	if click(1, 2+paneTop); w.m.focus != paneInstances || w.m.selected != w.Root {
		t.Fatalf("shop: %q", w.m.selected)
	}
	if cmd := click(1, 5+paneTop); w.m.selected != "/blog" || cmd == nil {
		t.Fatalf("blog: %q", w.m.selected)
	}
	w.m.Update(tea.MouseClickMsg{X: 1, Y: 2 + paneTop, Button: tea.MouseRight})
	if w.m.selected != "/blog" {
		t.Error("right button")
	}
	// The wheel moves through the pane under it.
	w.m.Update(tea.MouseWheelMsg{X: 1, Y: 3, Button: tea.MouseWheelUp})
	if w.m.selected != w.Root || w.m.focus != paneInstances {
		t.Errorf("wheel up %q", w.m.selected)
	}
	feed[statusMsg](t, w.m, w.m.loadStatus())
	w.m.status.Services = append(w.m.status.Services, app.ServiceStatus{Service: "db", State: "running"})
	w.m.Update(tea.MouseWheelMsg{X: w.m.split + 1, Y: 3, Button: tea.MouseWheelDown})
	if w.m.focus != paneServices || w.m.service() != "db" {
		t.Errorf("wheel down %v %q", w.m.focus, w.m.service())
	}
	// Elsewhere the mouse leaves the main view alone.
	w.m.mode = modeHelp
	w.m.Update(tea.MouseClickMsg{X: 1, Y: 5 + paneTop, Button: tea.MouseLeft})
	w.m.Update(tea.MouseWheelMsg{X: 1, Y: 3})
	if w.m.service() != "db" {
		t.Error("help")
	}
}
