package app

import (
	"context"
	"testing"
	"time"

	"github.com/l0wskilled/mutti/internal/engine"
)

func TestEvents(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	events, _ := h.app.Events(c)

	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeProject+"=shop", engine.LabelComposeService+"=web")
	h.eng.Emit(cs[0].ID, "health_status: healthy", nil)
	h.eng.Emit(cs[0].ID, "exec_start: sh", nil) // not one that changes what mutti shows
	h.eng.SetContainerState(cs[0].ID, "exited")
	// mutti's own helpers carry a role and stay out.
	h.eng.AddContainer(engine.Container{Name: "probe", State: "running", Labels: map[string]string{engine.LabelInstance: "shop", engine.LabelRole: "probe"}})

	var got []string
	var stopped InstanceEvent
	timeout := time.After(5 * time.Second)
	for len(got) < 6 {
		select {
		case ev := <-events:
			if ev.Instance != "shop" {
				t.Fatalf("event of %q: %+v", ev.Instance, ev)
			}
			got = append(got, ev.Service+" "+ev.Event)
			if ev.Event == "stopped" {
				stopped = ev
			}
		case <-timeout:
			t.Fatalf("got only %v", got)
		}
	}
	want := map[string]bool{"web created": true, "web started": true, "worker created": true, "worker started": true, "web healthy": true, "web stopped": true}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected %q in %v", g, got)
		}
	}
	if stopped.ExitCode == nil || *stopped.ExitCode != 0 || stopped.Container == "" || stopped.Time.IsZero() {
		t.Errorf("stopped %+v", stopped)
	}

	cancel()
	for range events { //nolint:revive // drain until the stream ends
	}
}

func TestEventsEndWithTheirContext(t *testing.T) {
	h := newHarness(t, webModel())
	c, cancel := context.WithCancel(ctx)
	events, _ := h.app.Events(c)
	h.eng.AddContainer(engine.Container{Name: "a", State: "running", Labels: map[string]string{engine.LabelInstance: "shop"}})
	cancel()           // with an event waiting to be read
	for range events { //nolint:revive // drain until the stream ends
	}

	h.eng.FailOn = map[string]error{"Events": engine.ErrUnavailable}
	events, errs := h.app.Events(ctx)
	if err := <-errs; err == nil {
		t.Error("want error")
	}
	if _, open := <-events; open {
		t.Error("stream open after failure")
	}
}
