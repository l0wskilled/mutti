package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/l0wskilled/mutti/internal/app"
)

func TestNarrowTerminalShowsOnePane(t *testing.T) {
	w := setup(t)
	w.m.Update(tea.WindowSizeMsg{Width: 60, Height: 22})
	if s := w.screen(); !strings.Contains(s, "1/1 running") || !strings.Contains(s, "╭─ INSTANCES ─") || strings.Contains(s, "╭─ shop") || w.m.split != 60 {
		t.Fatalf("instances only:\n%s", s)
	}
	press(w.m, "tab")
	if s := w.screen(); strings.Contains(s, "INSTANCES") || !strings.Contains(s, "╭─ shop · running ─") || w.m.split != 0 {
		t.Fatalf("services only:\n%s", s)
	}
	// Too low for the selected service below the services.
	w.m.Update(tea.WindowSizeMsg{Width: 60, Height: 15})
	if s := w.screen(); strings.Contains(s, "╭─ web ─") {
		t.Errorf("no room for details:\n%s", s)
	}
}

func TestServicesScrollWithTheCursor(t *testing.T) {
	w := setup(t)
	for i := range 30 {
		w.m.status.Services = append(w.m.status.Services, app.ServiceStatus{Service: fmt.Sprintf("svc%02d", i), State: "running"})
	}
	press(w.m, "tab", "up") // the last one
	if s := w.screen(); !strings.Contains(s, "svc29") || strings.Contains(s, " web ") {
		t.Errorf("scrolled to the end:\n%s", s)
	}
}

func TestDetailBox(t *testing.T) {
	w := setup(t)
	press(w.m, "tab")
	// A service without facts or addresses says what state it is in.
	w.m.status.Services = []app.ServiceStatus{{Service: "web", State: "exited", Status: "Exited (0) 1 minute ago"}}
	if s := w.screen(); !strings.Contains(s, "╭─ web ─") || !strings.Contains(s, "│ stopped") {
		t.Errorf("state:\n%s", s)
	}
	press(w.m, "space")
	if s := w.screen(); !strings.Contains(s, "╭─ 1 marked ─") {
		t.Errorf("marked:\n%s", s)
	}
	// Without services there is no box below.
	w.m.status.Services = nil
	if s := w.screen(); strings.Contains(s, "marked") {
		t.Errorf("no services:\n%s", s)
	}
}

func TestSince(t *testing.T) {
	w := setup(t)
	w.m.details["web"] = app.ServiceDetails{StartedAt: time.Now().Add(-90 * time.Second)}
	for status, want := range map[string]string{
		"Up 3 minutes (healthy)":         "3 minutes",
		"Up 1 second (health: starting)": "1 second",
		"Exited (0) 5 minutes ago":       "5 minutes ago",
		"Exited (143) 2 hours ago":       "2 hours ago",
		"Exited (2) 1 hour ago":          "exit 2, 1 hour ago",
		"Exited (2)":                     "Exited (2)",
		"Created":                        "Created",
	} {
		if got := w.m.since(app.ServiceStatus{Service: "db", Status: status}); got != want {
			t.Errorf("%q: %q", status, got)
		}
	}
	if got := w.m.since(app.ServiceStatus{Service: "web", State: "running", Status: "Up 2 minutes"}); got != "1m" {
		t.Errorf("from its start: %q", got)
	}
}

func TestOptionalBadges(t *testing.T) {
	tools := []string{"tools"}
	for svc, want := range map[*app.ServiceStatus]string{
		{State: "missing", Profiles: tools}:                                 "off",
		{State: "exited", Status: "Exited (0) 1 hour ago", Profiles: tools}: "off",
		{State: "exited", Status: "Exited (2) 1 hour ago", Profiles: tools}: "crashed",
		{State: "running", Status: "Up 1 minute", Profiles: tools}:          "running",
	} {
		if kind, _ := badge(*svc); kind != want {
			t.Errorf("%+v: %s", *svc, kind)
		}
	}
}
