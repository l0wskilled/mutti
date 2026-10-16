package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/l0wskilled/mutti/internal/app"
)

func TestMarkServices(t *testing.T) {
	w := setup(t)
	press(w.m, "space")
	if len(w.m.marks()) != 0 {
		t.Fatal("the instances pane marks nothing")
	}
	w.m.status.Services = append(w.m.status.Services, app.ServiceStatus{Service: "db", State: "running"}, app.ServiceStatus{Service: "cache", State: "running"})
	// Space marks and moves on; marks keep the order of the pane.
	press(w.m, "tab", "down", "down", "space", "space")
	if got := w.m.marks(); !reflect.DeepEqual(got, []string{"web", "cache"}) || w.m.svcCursor != 1 {
		t.Fatalf("marks %v cursor %d", got, w.m.svcCursor)
	}
	if what, services := w.m.target(); what != "web, cache" || len(services) != 2 {
		t.Errorf("target %q %v", what, services)
	}
	screen := w.screen()
	for _, want := range []string{" ✓ web", "›  db", " ✓ cache", "2 marked", "esc unmark"} {
		if !strings.Contains(screen, want) {
			t.Errorf("no %q in\n%s", want, screen)
		}
	}
	// Logs follow every marked service.
	feed[logsMsg](t, w.m, press(w.m, "l"))
	if run := w.Compose.Runs[len(w.Compose.Runs)-1]; !reflect.DeepEqual(run[len(run)-2:], []string{"web", "cache"}) {
		t.Errorf("logs %v", run)
	}
	press(w.m, "esc")
	// The instances pane acts on the instance, whatever is marked.
	press(w.m, "tab")
	if what, services := w.m.target(); what != "shop" || services != nil {
		t.Errorf("instance %q %v", what, services)
	}
	// Space again unmarks; esc unmarks all; another instance starts unmarked.
	press(w.m, "tab", "up", "space")
	if got := w.m.marks(); !reflect.DeepEqual(got, []string{"cache"}) {
		t.Errorf("unmarked %v", got)
	}
	press(w.m, "esc")
	if len(w.m.marks()) != 0 {
		t.Error("esc")
	}
	w.m.marked["web"] = true
	w.m.selectInstance("/elsewhere")
	if len(w.m.marked) != 0 {
		t.Error("other instance")
	}
}
