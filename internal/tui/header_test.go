package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/l0wskilled/mutti/internal/app"
)

func TestHeaderTellsDockerAndRouter(t *testing.T) {
	w := setup(t)
	feed[routerMsg](t, w.m, w.m.loadRouter())
	if w.m.router == nil || !strings.Contains(w.screen(), "● docker  ● router") {
		t.Fatalf("all well %+v\n%s", w.m.router, w.screen())
	}
	for check, want := range map[app.Check]string{
		{Status: app.CheckWarn, Detail: "mutti-router on 127.0.0.1:80 (http) and :443 (https), exited"}: "● router exited",
		{Status: app.CheckWarn, Detail: "not created yet"}:                                              "● router not created yet",
		{Status: app.CheckFail, Detail: "boom"}:                                                         "● router failing",
	} {
		w.m.Update(routerMsg{check: check})
		if !strings.Contains(w.screen(), want) {
			t.Errorf("no %q in\n%s", want, w.screen())
		}
	}
	w.m.listErr = &app.Error{Code: app.CodeDockerUnavailable, Message: "cannot reach the Docker daemon"}
	if !strings.Contains(w.screen(), "● docker unreachable") {
		t.Errorf("docker down\n%s", w.screen())
	}
}

func TestListFailed(t *testing.T) {
	w := setup(t)
	w.m.instances = nil
	w.m.Update(instancesMsg{err: &app.Error{Code: app.CodeDockerUnavailable, Message: "cannot reach the Docker daemon"}})
	if s := w.screen(); !strings.Contains(s, "Docker does not answer") || !strings.Contains(s, "Start Docker") {
		t.Errorf("docker down\n%s", s)
	}
	w.m.Update(instancesMsg{err: errors.New("state dir unreadable")})
	if s := w.screen(); !strings.Contains(s, "mutti cannot list the instances") || !strings.Contains(s, "state dir unreadable") || strings.Contains(s, "docker unreachable") {
		t.Errorf("other error\n%s", s)
	}
}

func TestPaneTitlesFollowTheFocus(t *testing.T) {
	w := setup(t)
	if w.m.paneTitle("X", paneInstances) == w.m.paneTitle("X", paneServices) {
		t.Error("the focused pane stands out")
	}
	press(w.m, "tab")
	if !strings.Contains(w.screen(), "› ● shop") || !strings.Contains(w.screen(), "SERVICES") {
		t.Errorf("the selected instance stays marked\n%s", w.screen())
	}
}
