package app

import (
	"testing"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
)

func dbModel() *compose.Model {
	m := &compose.Model{Services: map[string]compose.Service{"db": {Ports: []compose.Port{{Target: 5432}}}}}
	m.XMutti = []byte(`{"ports":{"db":[5432,9187]}}`)
	return m
}

func TestStablePort(t *testing.T) {
	a, b := stablePort("shop", "db", 5432), stablePort("shop-feature", "db", 5432)
	if a == b || a != stablePort("shop", "db", 5432) {
		t.Fatalf("a=%d b=%d", a, b)
	}
	for _, p := range []uint16{a, b} {
		if p < fixedPortBase || p >= fixedPortBase+fixedPortRange {
			t.Errorf("%d out of range", p)
		}
	}
}

func dbPort(t *testing.T, h *harness, target uint16) uint16 {
	t.Helper()
	cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeService+"=db")
	for _, c := range cs {
		for _, p := range c.Ports {
			if p.ContainerPort == target {
				return p.HostPort
			}
		}
	}
	t.Fatalf("db:%d not published: %+v", target, cs)
	return 0
}

func TestFixedPortsSurviveRestarts(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, dbModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	first := dbPort(t, h, 5432)
	if first != stablePort("shop", "db", 5432) || dbPort(t, h, 9187) != stablePort("shop", "db", 9187) {
		t.Fatalf("not the stable ports: %d", first)
	}
	// While running, the port is taken, but by this instance: it stays.
	h.app.Host.PortFree = func(p uint16) bool { return p != first }
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := dbPort(t, h, 5432); got != first {
		t.Fatalf("moved from %d to %d", first, got)
	}
}

func TestFixedPortsAvoidForeignPorts(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, dbModel())
	preferred := stablePort("shop", "db", 5432)
	h.app.Host.PortFree = func(p uint16) bool { return p != preferred }
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := dbPort(t, h, 5432); got == preferred {
		t.Fatal("took a busy port")
	}
}

func TestFixedPortsFailures(t *testing.T) {
	root := newRepo(t, "shop")

	h := newHarness(t, dbModel())
	h.app.Host.PortFree = func(uint16) bool { return false }
	if _, err := h.app.Up(ctx, root, UpOptions{}); appErrCode(err) != CodeInvalidConfig {
		t.Fatalf("all busy: %v", err)
	}

	h = newHarness(t, dbModel())
	inst, err := h.app.Resolve(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	h.eng.FailOn = map[string]error{"ListContainers": engine.ErrUnavailable}
	if _, err := h.app.fixedPorts(ctx, inst); appErrCode(err) != CodeDockerUnavailable {
		t.Fatalf("engine: %v", err)
	}
}

func TestFixedPortsNeverCollideWithinOneInstance(t *testing.T) {
	h := newHarness(t, webModel())
	inst := &Instance{Name: "shop"}
	// The same port listed twice wants the same host port both times; the second must move on.
	inst.Config.Ports = map[string][]uint16{"a": {1, 1}}
	got, err := h.app.fixedPorts(ctx, inst)
	if err != nil {
		t.Fatal(err)
	}
	if got["a"][1] != stablePort("shop", "a", 1)+1 {
		t.Fatalf("second claim of the same port not moved: %v", got)
	}
}
