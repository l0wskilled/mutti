package app

import (
	"errors"
	"testing"
	"time"

	"github.com/l0wskilled/mutti/internal/engine"
)

func TestDetails(t *testing.T) {
	h := newHarness(t, webModel())
	add := func(service, state string, r engine.Runtime) string {
		id := h.eng.AddContainer(engine.Container{State: state, Labels: map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: service}})
		h.eng.SetRuntime(id, r)
		return id
	}
	started := time.Unix(100, 0)
	web := add("web", "running", engine.Runtime{StartedAt: started, RestartCount: 2, Health: []engine.HealthProbe{{ExitCode: 1}, {Start: started, Output: "ok\n"}}})
	h.eng.SetStats(web, engine.Stats{Read: started, CPU: time.Second, Memory: 10, MemoryLimit: 100})
	add("db", "exited", engine.Runtime{})
	add("other", "running", engine.Runtime{})
	h.eng.AddContainer(engine.Container{State: "running", Labels: map[string]string{engine.LabelComposeProject: "elsewhere"}})

	got, err := h.app.Details(ctx, "shop")
	if err != nil || len(got) != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	db, other, w := got[0], got[1], got[2]
	if db.Service != "db" || db.Usage != nil || db.Health != nil {
		t.Errorf("stopped: %+v", db)
	}
	if other.Usage == nil || other.Usage.MemoryLimit == 0 {
		t.Errorf("running: %+v", other)
	}
	if w.StartedAt != started || w.RestartCount != 2 || w.Health == nil || w.Health.Output != "ok" || w.Health.ExitCode != 0 ||
		*w.Usage != (Usage{Sampled: started, CPU: time.Second, Memory: 10, MemoryLimit: 100}) {
		t.Errorf("web: %+v %+v %+v", w, w.Health, w.Usage)
	}

	// A container that stops between the list and the sample has no usage.
	h.eng.FailOn = map[string]error{"ContainerStats": errors.New("gone")}
	if got, err := h.app.Details(ctx, "shop"); err != nil || got[2].Usage != nil {
		t.Errorf("stats failed: %+v %v", got, err)
	}
	for _, method := range []string{"ListContainers", "InspectContainer"} {
		h.eng.FailOn = map[string]error{method: engine.ErrUnavailable}
		if _, err := h.app.Details(ctx, "shop"); appErrCode(err) != CodeDockerUnavailable {
			t.Errorf("%s: %v", method, err)
		}
	}
}
