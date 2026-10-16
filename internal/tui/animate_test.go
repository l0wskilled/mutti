package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/l0wskilled/mutti/internal/app"
)

func TestAnimations(t *testing.T) {
	defer func(d time.Duration) { animateEvery = d }(animateEvery)
	animateEvery = time.Millisecond
	w := setup(t)
	if w.m.animate() != nil || w.m.animating {
		t.Fatal("nothing moves")
	}
	st := *w.m.status
	st.Services = []app.ServiceStatus{{Service: "web", State: "running", Status: "Up 1 second (health: starting)"}}
	cmd := w.update(statusMsg{path: w.Root, status: &st})
	if !w.m.animating || cmd == nil || w.m.animate() != nil {
		t.Fatal("starting moves, one frame at a time")
	}
	before := w.screen()
	feed[animateMsg](t, w.m, cmd)
	if w.m.frame != 1 || !w.m.animating || w.screen() == before || !strings.Contains(w.screen(), " starting") {
		t.Errorf("next frame %d\n%s", w.m.frame, w.screen())
	}
	// Unhealthy pulses; healthy rests.
	st.Services[0].Status = "Up 1 minute (unhealthy)"
	bold := w.m.badgeStyle("unhealthy").GetBold()
	w.m.frame += 5
	if !w.m.animated() || w.m.badgeStyle("unhealthy").GetBold() == bold {
		t.Error("pulse")
	}
	st.Services[0].Status = "Up 1 minute (healthy)"
	if w.m.nextFrame() != nil || w.m.animating {
		t.Error("rests")
	}
}
