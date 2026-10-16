package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/engine"
)

// webID is the container of the web service of "shop".
func (w *world) webID(t *testing.T) string {
	t.Helper()
	cs, err := w.Engine.ListContainers(context.Background(), engine.LabelComposeService+"=web")
	if err != nil || len(cs) != 1 {
		t.Fatalf("%+v %v", cs, err)
	}
	return cs[0].ID
}

// sample has the UI sample what the services use.
func (w *world) sample(t *testing.T) {
	t.Helper()
	feed[detailsMsg](t, w.m, w.update(sampleMsg{}))
}

func TestServiceFactsAndUsage(t *testing.T) {
	w := setup(t)
	id := w.webID(t)
	t0 := time.Now()
	w.Engine.SetRuntime(id, engine.Runtime{StartedAt: t0.Add(-90 * time.Second), RestartCount: 1, Health: []engine.HealthProbe{{Start: t0}}})
	w.Engine.SetStats(id, engine.Stats{Read: t0, Memory: 100 << 20, MemoryLimit: 8 << 30})
	w.sample(t)
	press(w.m, "tab")
	for _, want := range []string{"image", "up     1m · restarted 1 times", "health passing", "cpu    …", "memory 100M of 8.0G", "100M"} {
		if !strings.Contains(w.screen(), want) {
			t.Errorf("no %q in\n%s", want, w.screen())
		}
	}

	// Two samples make the CPU use: one second of CPU time in two seconds.
	w.Engine.SetStats(id, engine.Stats{Read: t0.Add(2 * time.Second), CPU: time.Second, Memory: 200 << 20, MemoryLimit: 8 << 30})
	w.sample(t)
	w.sample(t) // the same sample again changes nothing
	u := w.m.usage["web"]
	if len(u.cpu) != 1 || u.cpu[0] != 50 || len(u.mem) != 2 || !strings.Contains(w.screen(), " 50.0% ") {
		t.Errorf("usage %+v\n%s", u, w.screen())
	}
	// Less CPU time than before: it restarted, there is no CPU use to tell.
	w.Engine.SetStats(id, engine.Stats{Read: t0.Add(4 * time.Second), Memory: 200 << 20, MemoryLimit: 8 << 30})
	w.sample(t)
	if len(u.cpu) != 1 || len(u.mem) != 3 {
		t.Errorf("after a restart %+v", u)
	}

	w.Engine.SetRuntime(id, engine.Runtime{Health: []engine.HealthProbe{{Start: t0, ExitCode: 1}}})
	w.sample(t)
	if !strings.Contains(w.screen(), "health failing, exit 1") || strings.Contains(w.screen(), "up     ") {
		t.Errorf("failing health, no start time\n%s", w.screen())
	}

	// Samples of another instance or that failed change nothing; a stopped service has no usage.
	w.m.Update(detailsMsg{instance: "other"})
	w.m.Update(detailsMsg{instance: "shop", err: errors.New("docker is gone")})
	if w.m.usage["web"] == nil {
		t.Error("kept")
	}
	w.Engine.SetContainerState(id, "exited")
	w.sample(t)
	if w.m.usage["web"] != nil || strings.Contains(w.screen(), "memory") {
		t.Errorf("stopped: %+v", w.m.usage)
	}

	// Without an instance, or with a gone worktree, nothing is sampled.
	w.m.instances[0].Orphaned = true
	if w.m.loadDetails() != nil {
		t.Error("gone worktree")
	}
	w.m.selected = ""
	if w.m.loadDetails() != nil {
		t.Error("no instance")
	}
}

func TestUsageColumns(t *testing.T) {
	w := setup(t)
	if w.m.showUsage(100) {
		t.Error("nothing sampled yet")
	}
	w.m.usage["web"] = &usage{mem: []float64{1 << 20}}
	if !w.m.showUsage(100) || w.m.showUsage(60) {
		t.Error("room for usage")
	}
	row := func(svc string) string {
		return ansi.Strip(w.m.renderService(app.ServiceStatus{Service: svc, State: "running", Status: "Up 2 seconds"}, false, 100))
	}
	if got := row("web"); !strings.Contains(got, "1M") {
		t.Errorf("memory only %q", got)
	}
	if web, db := row("web"), row("db"); strings.Index(web, "2 seconds") != strings.Index(db, "2 seconds") {
		t.Errorf("columns line up:\n%s\n%s", web, db)
	}
}

func TestSparklinesAndUnits(t *testing.T) {
	if got := sparkline([]float64{0, 5, 10}, 5, 0, 10); got != "  ▁▅█" {
		t.Errorf("sparkline %q", got)
	}
	if got := sparkline([]float64{1, 1}, 2, 1, 1); got != "▁▁" {
		t.Errorf("no range %q", got)
	}
	if got := memLine([]float64{100, 101, 200}, 3); got != "▁▁█" {
		t.Errorf("memory %q", got)
	}
	if got := memLine([]float64{8, 8}, 2); got != "▅▅" {
		t.Errorf("flat %q", got)
	}
	if got := cpuLine([]float64{0, 1}, 2); got != "▁▂" {
		t.Errorf("idle stays low %q", got)
	}
	for b, want := range map[float64]string{512 << 10: "512K", 300 << 20: "300M", 3 << 30: "3.0G"} {
		if got := humanBytes(b); got != want {
			t.Errorf("%v: %q", b, got)
		}
	}
	for d, want := range map[time.Duration]string{5 * time.Second: "5s", 3 * time.Minute: "3m", 125 * time.Minute: "2h 5m", 50 * time.Hour: "2d 2h"} {
		if got := humanDuration(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
}
