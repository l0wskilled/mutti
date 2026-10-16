package tui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/apptest"
	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
)

// messages runs cmd and what it batches, and returns their messages. Commands that wait (ticks,
// the next event) are dropped after a moment.
func messages(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-got:
	case <-time.After(100 * time.Millisecond):
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		results := make(chan []tea.Msg, len(batch))
		for _, c := range batch {
			go func() { results <- messages(c) }()
		}
		for range batch {
			out = append(out, <-results...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// find returns the first message of type T.
func find[T any](msgs []tea.Msg) (T, bool) {
	for _, m := range msgs {
		if t, ok := m.(T); ok {
			return t, true
		}
	}
	var zero T
	return zero, false
}

// feed updates m with the messages of cmd that the test cares about, one level deep.
func feed[T any](t *testing.T, m *model, cmd tea.Cmd) T {
	t.Helper()
	msg, ok := find[T](messages(cmd))
	if !ok {
		t.Fatalf("no %T from %v", msg, cmd)
	}
	m.Update(msg)
	return msg
}

func press(m *model, keys ...string) tea.Cmd {
	var cmds []tea.Cmd
	for _, k := range keys {
		_, cmd := m.Update(keyMsg(k))
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// typeIn presses keys in the palette and hands its filter results back to it, as the program
// does.
func typeIn(m *model, keys ...string) {
	for _, k := range keys {
		for _, msg := range messages(press(m, k)) {
			if _, ok := msg.(list.FilterMatchesMsg); ok {
				m.Update(msg)
			}
		}
	}
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+c", "ctrl+l", "ctrl+k":
		return tea.KeyPressMsg{Code: rune(k[5]), Mod: tea.ModCtrl}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

type world struct {
	*apptest.World
	m *model
}

// setup starts the UI on an app with the instance "shop" up and its web service running.
func setup(t *testing.T) *world {
	t.Helper()
	w := &world{World: apptest.New(t)}
	if _, err := w.App.Up(context.Background(), w.Root, app.UpOptions{}); err != nil {
		t.Fatal(err)
	}
	commands := []Command{{Path: []string{"list"}, Use: "list", Short: "List every instance"}, {Path: []string{"exec"}, Use: "exec <service> -- <command...>", Short: "Run a command"}}
	w.m = newModel(context.Background(), Options{App: w.App, Dir: w.Root, Self: "true", Version: "test", Commands: commands})
	w.m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	msgs := messages(w.m.Init())
	list, _ := find[instancesMsg](msgs)
	feed[statusMsg](t, w.m, w.update(list))
	return w
}

func (w *world) update(msg tea.Msg) tea.Cmd {
	_, cmd := w.m.Update(msg)
	return cmd
}

// screen is what the terminal shows, without colors.
func (w *world) screen() string { return ansi.Strip(w.m.View().Content) }

func TestMainViewShowsInstancesAndServices(t *testing.T) {
	w := setup(t)
	if c := w.m.current(); c == nil || c.Name != "shop" || w.m.status == nil {
		t.Fatalf("selection %+v %+v", c, w.m.status)
	}
	screen := w.screen()
	for _, want := range []string{"◆ mutti", "1 instances · 1 running", "INSTANCES", "shop", "1/1", "web", "running", "tab services", "https://shop.localhost"} {
		if !strings.Contains(screen, want) {
			t.Errorf("no %q in\n%s", want, screen)
		}
	}
	if v := w.m.View(); !v.AltScreen || v.WindowTitle != "mutti" {
		t.Errorf("view %+v", v)
	}
	w.m.Update(tea.BackgroundColorMsg{Color: nil})
	w.m.width = 0
	if w.screen() != "mutti is waking up…" {
		t.Errorf("before the size is known: %q", w.screen())
	}
}

func TestNavigationAndServiceDetails(t *testing.T) {
	w := setup(t)
	if press(w.m, "tab"); w.m.focus != paneServices || w.m.service() != "web" {
		t.Fatalf("tab: %v %q", w.m.focus, w.m.service())
	}
	press(w.m, "down", "up", "k", "j")
	if w.m.svcCursor != 0 || !strings.Contains(w.screen(), "tab instances • space mark • u start") {
		t.Errorf("services: %d\n%s", w.m.svcCursor, w.screen())
	}
	if cmd := press(w.m, "enter"); cmd == nil {
		t.Error("enter on a service opens a shell")
	}
	press(w.m, "tab")
	if w.m.focus != paneInstances {
		t.Fatal("tab back")
	}
	// Another instance: its status comes anew.
	w.m.instances = append(w.m.instances, app.Summary{Name: "other", Path: "/elsewhere"})
	if cmd := press(w.m, "down"); w.m.selected != "/elsewhere" || w.m.status != nil || cmd == nil {
		t.Fatalf("down: %q", w.m.selected)
	}
	if !strings.Contains(w.screen(), "loading") {
		t.Errorf("loading: %s", w.screen())
	}
	msg, _ := find[statusMsg](messages(w.m.loadStatus()))
	if w.m.Update(msg); w.m.statusErr == nil || !strings.Contains(w.screen(), app.CodeNoComposeFile[:2]) && !strings.Contains(w.screen(), "compose") {
		t.Errorf("status error: %v\n%s", w.m.statusErr, w.screen())
	}
	// A status for an instance no longer selected is dropped.
	w.m.Update(statusMsg{path: "/stale"})
	feed[statusMsg](t, w.m, press(w.m, "up"))
	if w.m.selected != w.Root {
		t.Fatal("up")
	}
	// enter on an instance shows its services.
	if cmd := press(w.m, "enter"); w.m.focus != paneServices || cmd == nil {
		t.Errorf("enter: %v", w.m.focus)
	}
}

func TestEmptyMachineAndNoInstance(t *testing.T) {
	w := setup(t)
	w.m.Update(instancesMsg{})
	if !strings.Contains(w.screen(), "has no instances yet") {
		t.Errorf("welcome:\n%s", w.screen())
	}
	w.m.selected = ""
	for _, k := range []string{"u", "s", "r", "d", "R", "X", "o", "y", "l", "i", "e", "n"} {
		press(w.m, k)
		if !strings.Contains(w.m.toast, "no instance yet") {
			t.Errorf("%s: %q", k, w.m.toast)
		}
	}
	if w.m.service() != "" || w.m.instanceName() != "the instance" || w.m.loadStatus() != nil {
		t.Error("nothing selected")
	}
	if what, svcs := w.m.target(); what != "" || svcs != nil {
		t.Error("no target")
	}
	w.m.move(1)
	w.m.instances = []app.Summary{{Name: "x", Path: "/x"}}
	w.m.selected = "/gone"
	if !strings.Contains(w.screen(), "Select an instance.") {
		t.Errorf("no current:\n%s", w.screen())
	}
	// A failing list shows up in the header and keeps what was there.
	w.m.toast = ""
	w.m.Update(instancesMsg{err: errors.New("docker is away\nfor good")})
	if !strings.Contains(w.screen(), "✗ docker is away") || len(w.m.instances) != 1 {
		t.Errorf("list error:\n%s", w.screen())
	}
}

func TestSelectsTheInstanceOfTheDirectory(t *testing.T) {
	w := setup(t)
	// A new list keeps the selection.
	if w.m.Update(instancesMsg{list: w.m.instances}); w.m.selected != w.Root || w.m.status == nil {
		t.Fatal("kept")
	}
	w.m.opts.Dir = "/b/sub"
	w.m.selected = ""
	w.m.Update(instancesMsg{list: []app.Summary{{Name: "a", Path: "/a"}, {Name: "b", Path: "/b"}}})
	if w.m.selected != "/b" {
		t.Errorf("selected %q", w.m.selected)
	}
	w.m.opts.Dir = "/nowhere"
	w.m.selected = ""
	w.m.Update(instancesMsg{list: []app.Summary{{Name: "a", Path: "/a"}}})
	if w.m.selected != "/a" {
		t.Errorf("first: %q", w.m.selected)
	}
}

func TestLifecycleActions(t *testing.T) {
	w := setup(t)
	done := feed[doneMsg](t, w.m, press(w.m, "s"))
	if done.err != nil || w.m.busy != "" || !strings.Contains(w.m.toast, "shop stopped") {
		t.Fatalf("stop: %+v %q", done, w.m.toast)
	}
	feed[doneMsg](t, w.m, press(w.m, "u"))
	feed[doneMsg](t, w.m, press(w.m, "r"))
	press(w.m, "tab")
	feed[doneMsg](t, w.m, press(w.m, "r"))
	if !strings.Contains(w.m.toast, "web restarted") {
		t.Errorf("restart web: %q", w.m.toast)
	}
	feed[doneMsg](t, w.m, press(w.m, "s"))
	feed[doneMsg](t, w.m, press(w.m, "u"))

	// One thing at a time.
	cmd := press(w.m, "s")
	if w.m.busy == "" || !strings.Contains(w.screen(), "stopping web") {
		t.Fatalf("busy:\n%s", w.screen())
	}
	tick := w.m.spinner.Tick()
	if c := w.update(tick); c == nil {
		t.Error("the spinner turns while busy")
	}
	press(w.m, "u")
	if !strings.Contains(w.m.toast, "wait a moment") {
		t.Errorf("second action: %q", w.m.toast)
	}
	feed[doneMsg](t, w.m, cmd)
	if c := w.update(tick); c != nil {
		t.Error("the spinner rests")
	}

	// Confirmed ones: d asks, anything but y cancels.
	if press(w.m, "d"); w.m.mode != modeConfirm || !strings.Contains(w.screen(), "Remove the containers of shop?") || w.m.busy != "" {
		t.Fatalf("down asks:\n%s", w.screen())
	}
	press(w.m, "n")
	if w.m.mode != modeMain || w.m.toast != "cancelled" {
		t.Fatal("cancel")
	}
	press(w.m, "d")
	feed[doneMsg](t, w.m, press(w.m, "y"))
	if !strings.Contains(w.m.toast, "shop is down") {
		t.Errorf("down: %q", w.m.toast)
	}
	press(w.m, "R")
	if cmd := press(w.m, "Y"); cmd == nil {
		t.Error("reset runs in the terminal")
	}
	press(w.m, "X")
	feed[doneMsg](t, w.m, press(w.m, "y"))
	if !strings.Contains(w.m.toast, "shop is gone") {
		t.Errorf("destroy: %q", w.m.toast)
	}
}

func TestFailedActionShowsWhatWentWrong(t *testing.T) {
	w := setup(t)
	w.Compose.Stderr = func([]string) string { return "port is already allocated\n" }
	w.Compose.Exit = func(args []string) int { return map[string]int{"up": 1}[args[0]] }
	done := feed[doneMsg](t, w.m, press(w.m, "u"))
	if done.err == nil || !strings.Contains(w.m.toast, "docker compose up failed") || w.m.toastOK {
		t.Fatalf("failure: %+v %q", done, w.m.toast)
	}
	diag := feed[diagnosisMsg](t, w.m, w.m.done(done))
	if w.m.mode != modeDiagnose || diag.diags[0].Service != "mutti" || !strings.Contains(w.screen(), "port is already allocated") {
		t.Errorf("what went wrong:\n%s", w.screen())
	}
	if !strings.Contains(w.screen(), "↑/↓ scroll · esc back") {
		t.Error("viewer hint")
	}
	press(w.m, "esc")
	if w.m.mode != modeMain {
		t.Error("esc")
	}
}

func TestToasts(t *testing.T) {
	w := setup(t)
	cmd := w.m.setToast("hello", true)
	if !strings.Contains(w.screen(), "✓ hello") {
		t.Errorf("ok toast:\n%s", w.screen())
	}
	old := w.m.toastID
	w.m.setToast("oops", false)
	if !strings.Contains(w.screen(), "✗ oops") {
		t.Error("error toast")
	}
	w.m.Update(toastDoneMsg{id: old}) // an older toast's timer
	if w.m.toast != "oops" {
		t.Error("a newer toast stays")
	}
	w.m.Update(toastDoneMsg{id: w.m.toastID})
	if w.m.toast != "" {
		t.Error("gone")
	}
	_ = cmd
}

func TestOpenCopyAndURL(t *testing.T) {
	w := setup(t)
	opened := ""
	w.App.Host.OpenURL = func(u string) error { opened = u; return nil }
	feed[doneMsg](t, w.m, press(w.m, "o"))
	if opened != "https://shop.localhost" {
		t.Errorf("opened %q", opened)
	}
	msg := feed[copyMsg](t, w.m, press(w.m, "y"))
	if msg.url != "https://shop.localhost" || !strings.Contains(w.m.toast, "copied") {
		t.Errorf("copy: %+v %q", msg, w.m.toast)
	}
	w.m.Update(copyMsg{err: errors.New("no url")})
	if w.m.toast != "no url" {
		t.Errorf("copy error: %q", w.m.toast)
	}
}

func TestLogsView(t *testing.T) {
	w := setup(t)
	w.Compose.Stdout = func(args []string) string {
		if args[0] == "logs" {
			return "web-1  | 2026-01-01T00:00:00Z hello\nweb-1  | 2026-01-01T00:00:01Z ERROR boom\nweb-1  | 2026-01-01T00:00:02Z warn: slow\nplain line\n"
		}
		return ""
	}
	press(w.m, "tab")
	cmd := press(w.m, "l")
	if w.m.mode != modeLogs || w.m.logsOf != "web" {
		t.Fatalf("logs: %v %q", w.m.mode, w.m.logsOf)
	}
	logs := feed[logsMsg](t, w.m, cmd)
	if !strings.Contains(w.screen(), "Logs of web") || !strings.Contains(w.screen(), "ERROR boom") || !strings.Contains(w.screen(), "follow (on)") {
		t.Fatalf("logs:\n%s", w.screen())
	}
	// Logs of another service than shown are dropped; a stale tick does nothing.
	w.m.Update(logsMsg{service: "db", text: "other"})
	if w.update(logsTickMsg{gen: w.m.logsGen - 1}) != nil {
		t.Error("stale tick")
	}
	if c := w.update(logsTickMsg{gen: w.m.logsGen}); c == nil {
		t.Error("following")
	}
	press(w.m, "f")
	if w.m.follow || !strings.Contains(w.screen(), "follow (off)") {
		t.Error("follow off")
	}
	w.m.Update(logs)
	if w.update(logsTickMsg{gen: w.m.logsGen}) != nil {
		t.Error("not following")
	}
	if cmd := press(w.m, "f"); !w.m.follow || cmd == nil {
		t.Error("follow on")
	}
	press(w.m, "down")
	w.m.Update(tea.MouseWheelMsg{})
	press(w.m, "q")
	if w.m.mode != modeMain {
		t.Error("q leaves")
	}
	// f in the diagnosis view does nothing.
	w.m.openViewer(modeDiagnose, "x", "y")
	if press(w.m, "f"); w.m.mode != modeDiagnose {
		t.Error("f in diagnose")
	}
}

func TestDiagnosis(t *testing.T) {
	w := setup(t)
	id := w.Engine.AddContainer(engine.Container{State: "running", Status: "Up 1 minute (unhealthy)", Labels: map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: "db"}})
	w.Engine.SetRuntime(id, engine.Runtime{Health: []engine.HealthProbe{{ExitCode: 1, Output: "refused\n"}}})
	w.Compose.Stdout = func(args []string) string {
		if args[0] == "logs" {
			return "db-1  | panic: no disk\n"
		}
		return ""
	}
	w.Compose.ConfigFn = func(compose.Spec) (*compose.Model, error) {
		return &compose.Model{Services: map[string]compose.Service{"web": {Ports: []compose.Port{{Target: 80}}}, "db": {}, "tool": {Profiles: []string{"tools"}}}}, nil
	}
	w.App.Reconcile(context.Background()) //nolint:errcheck // only the model matters
	feed[statusMsg](t, w.m, w.m.refresh())
	screen := w.screen()
	if !strings.Contains(screen, "1 service needs care") || !strings.Contains(screen, "profile tools · optional") || !strings.Contains(screen, "unhealthy") {
		t.Fatalf("status with trouble:\n%s", screen)
	}
	press(w.m, "tab")
	if !strings.Contains(w.screen(), "unhealthy: its health check exits with 1") || !strings.Contains(w.screen(), "panic: no disk") {
		t.Errorf("diagnosis below:\n%s", w.screen())
	}
	feed[diagnosisMsg](t, w.m, press(w.m, "i"))
	if w.m.mode != modeDiagnose || !strings.Contains(w.screen(), "Diagnosis of db") || !strings.Contains(w.screen(), "refused") {
		t.Errorf("diagnose:\n%s", w.screen())
	}
	press(w.m, "esc")
	press(w.m, "down")
	if !strings.Contains(w.screen(), "port   80 →") {
		t.Errorf("ports of web:\n%s", w.screen())
	}
	if got := w.m.renderDiagnoses(nil, errors.New("docker is away")); !strings.Contains(got, "docker is away") {
		t.Error(got)
	}
	if got := w.m.renderDiagnoses([]app.Diagnosis{{Service: "a", Problem: "x"}, {Service: "b", Problem: "y"}}, nil); !strings.Contains(ansi.Strip(got), "x\n\n! b") {
		t.Errorf("two: %q", ansi.Strip(got))
	}
	if got := w.m.renderDiagnoses(nil, nil); !strings.Contains(got, "All services are well.") {
		t.Error(got)
	}
}

func TestBadges(t *testing.T) {
	for want, svc := range map[string]app.ServiceStatus{
		"off":         {State: "missing", Profiles: []string{"tools"}},
		"not created": {State: "missing"},
		"unhealthy":   {State: "running", Status: "Up (unhealthy)"},
		"healthy":     {State: "running", Status: "Up (healthy)"},
		"starting":    {State: "running", Status: "Up (health: starting)"},
		"done":        {State: "exited", Status: "Exited (0)", Completed: true},
		"stopped":     {State: "exited", Status: "Exited (143)"},
		"crashed":     {State: "exited", Status: "Exited (1)"},
		"restarting":  {State: "restarting"},
	} {
		if _, got := badge(svc); got != want {
			t.Errorf("%+v: %s", svc, got)
		}
	}
	s := newStyles(false)
	if s.badge("whatever").Render("x") == "" || s.dot("whatever") == "" || statusWord(app.StatusPartial) != "partly running" {
		t.Error("styles")
	}
}

func TestPrompts(t *testing.T) {
	w := setup(t)
	if press(w.m, "e"); !strings.Contains(w.m.toast, "select a service first") {
		t.Errorf("exec without service: %q", w.m.toast)
	}
	press(w.m, "tab", "e")
	if w.m.mode != modePrompt || !strings.Contains(w.screen(), "Command to run in web") {
		t.Fatalf("prompt:\n%s", w.screen())
	}
	press(w.m, "l", "s")
	if w.m.input.Value() != "ls" {
		t.Errorf("typed %q", w.m.input.Value())
	}
	w.m.Update(tea.PasteMsg{Content: " -la"})
	if cmd := press(w.m, "enter"); cmd == nil || w.m.mode != modeMain {
		t.Error("enter runs")
	}
	press(w.m, "n")
	press(w.m, "esc")
	if w.m.mode != modeMain {
		t.Error("esc cancels")
	}
	press(w.m, "n", "enter")
	if !strings.Contains(w.m.toast, "nothing entered") {
		t.Errorf("empty: %q", w.m.toast)
	}
	press(w.m, "n", "x")
	if cmd := press(w.m, "enter"); cmd == nil {
		t.Error("worktree add")
	}
}

func TestPalette(t *testing.T) {
	w := setup(t)
	press(w.m, ":")
	if w.m.mode != modePalette || !strings.Contains(w.screen(), "Start") || !strings.Contains(w.screen(), "What do you want to do? ›") {
		t.Fatalf("palette:\n%s", w.screen())
	}
	typeIn(w.m, "l", "i", "s", "t")
	if item, _ := w.m.palette.SelectedItem().(paletteItem); item.name != "mutti list" {
		t.Fatalf("list: %+v\n%s", item, w.screen())
	}
	if cmd := press(w.m, "enter"); cmd == nil || w.m.mode != modeMain {
		t.Error("list runs in the terminal")
	}
	// Commands with arguments ask for them, with the selected service filled in.
	press(w.m, "tab", ":")
	typeIn(w.m, "e", "x", "e", "c", " ", "<")
	press(w.m, "enter")
	if w.m.mode != modePrompt || w.m.input.Value() != "web" {
		t.Fatalf("exec asks: %v %q", w.m.mode, w.m.input.Value())
	}
	if cmd := press(w.m, "enter"); cmd == nil {
		t.Error("exec runs")
	}
	// An action from the palette.
	press(w.m, ":")
	typeIn(w.m, "s", "t", "o", "p")
	feed[doneMsg](t, w.m, press(w.m, "enter"))
	// Enter right away takes the first entry; nothing matching runs nothing.
	press(w.m, "tab", ":")
	press(w.m, "enter")
	if w.m.busy == "" {
		t.Error("the first entry is Start")
	}
	w.m.busy = ""
	press(w.m, ":")
	typeIn(w.m, "z", "z", "z")
	if press(w.m, "enter"); w.m.busy != "" || !strings.Contains(w.m.toast, "nothing matches zzz") {
		t.Errorf("nothing to run: %q %q", w.m.busy, w.m.toast)
	}
	press(w.m, ":")
	w.m.Update(tea.MouseWheelMsg{})
	press(w.m, "esc")
	if w.m.mode != modeMain {
		t.Error("esc")
	}
	// The palette knows mutti looks after you.
	press(w.m, ":")
	typeIn(w.m, "u", "w", "u")
	if w.m.mode != modeUwu || !strings.Contains(w.screen(), "Mutti says:") {
		t.Fatalf("uwu:\n%s", w.screen())
	}
	press(w.m, "x")
	if w.m.mode != modeMain {
		t.Error("back to work")
	}
	// Without an instance commands run where mutti runs.
	w.m.selected = ""
	if w.m.command(Command{Path: []string{"doctor"}, Use: "doctor"}) == nil {
		t.Error("doctor")
	}
}

func TestTimers(t *testing.T) {
	defer func(r, s, f, d, p time.Duration) {
		refreshEvery, settleEvents, followLogs, toastDuration, sampleEvery = r, s, f, d, p
	}(refreshEvery, settleEvents, followLogs, toastDuration, sampleEvery)
	refreshEvery, settleEvents, followLogs, toastDuration, sampleEvery = time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond
	w := setup(t)
	if _, ok := find[tickMsg](messages(tick())); !ok {
		t.Error("tick")
	}
	if _, ok := find[sampleMsg](messages(sampleTick())); !ok {
		t.Error("sample tick")
	}
	if _, ok := find[toastDoneMsg](messages(w.m.setToast("x", true))); !ok {
		t.Error("toast timer")
	}
	if _, ok := find[logsTickMsg](messages(w.m.logsTick())); !ok {
		t.Error("logs timer")
	}
	if _, ok := find[refreshMsg](messages(w.update(eventMsg{open: true}))); !ok {
		t.Error("settle timer")
	}
}

func TestCornerCases(t *testing.T) {
	w := setup(t)
	// The services vanished while the pane had the focus.
	w.m.focus, w.m.status = paneServices, nil
	if press(w.m, "enter") != nil || w.m.move(1) != nil {
		t.Error("no services")
	}
	// Other messages go to the prompt.
	press(w.m, "n")
	w.m.Update(tea.PasteMsg{Content: "feature"})
	if w.m.input.Value() != "feature" {
		t.Errorf("paste: %q", w.m.input.Value())
	}
}

func TestPaletteFilter(t *testing.T) {
	targets := []string{"mutti router\x00the router", "New worktree\x00a worktree", "Down\x00remove networks"}
	got := filterPalette("work", targets)
	if len(got) != 2 || got[0].Index != 1 || got[1].Index != 2 {
		t.Errorf("ranks %+v", got)
	}
	if got := filterPalette("", targets); len(got) != 3 {
		t.Errorf("empty term %+v", got)
	}
	if !(Command{Use: "rename <name>"}).takesArgs() || (Command{Use: "list"}).takesArgs() {
		t.Error("takesArgs")
	}
}

func TestHelpAndKeys(t *testing.T) {
	w := setup(t)
	press(w.m, "?")
	if w.m.mode != modeHelp || !strings.Contains(w.screen(), "copy URL") || !strings.Contains(w.screen(), "Every mutti command, searchable") {
		t.Fatalf("help:\n%s", w.screen())
	}
	press(w.m, "x")
	if w.m.mode != modeMain {
		t.Error("help closes")
	}
	if cmd := press(w.m, "ctrl+l"); cmd == nil {
		t.Error("redraw")
	}
	if !reflect.DeepEqual(messages(press(w.m, "q")), []tea.Msg{tea.QuitMsg{}}) {
		t.Error("q quits")
	}
	if !reflect.DeepEqual(messages(press(w.m, "ctrl+c")), []tea.Msg{tea.QuitMsg{}}) {
		t.Error("ctrl+c quits")
	}
	if press(w.m, "Z") != nil && len(messages(press(w.m, "Z"))) > 0 {
		t.Error("unknown key")
	}
	if len(w.m.keys.footer(paneServices, true)) <= len(w.m.keys.footer(paneServices, false)) {
		t.Error("esc unmarks shows with marks only")
	}
}

func TestEventsRefresh(t *testing.T) {
	w := setup(t)
	if cmd := w.update(eventMsg{open: true}); !w.m.pending || cmd == nil {
		t.Fatal("first event schedules a refresh")
	}
	if cmd := w.update(eventMsg{open: true}); cmd == nil {
		t.Error("keeps listening")
	}
	if cmd := w.update(eventMsg{}); cmd != nil {
		t.Error("closed stream")
	}
	if cmd := w.update(refreshMsg{}); w.m.pending || cmd == nil {
		t.Error("refresh")
	}
	if cmd := w.update(tickMsg{}); cmd == nil {
		t.Error("tick")
	}
	if cmd := w.update(execDoneMsg{what: "mutti list", err: errors.New("exit status 1")}); cmd == nil || !strings.Contains(w.m.toast, "exit status 1") {
		t.Errorf("exec failed: %q", w.m.toast)
	}
	w.update(execDone("mutti list")(nil))
	if !strings.Contains(w.m.toast, "mutti list: done") {
		t.Errorf("exec done: %q", w.m.toast)
	}
	// An event from Docker reaches the UI.
	evs := make(chan app.InstanceEvent, 1)
	w.m.events = evs
	evs <- app.InstanceEvent{Instance: "shop"}
	if msg, ok := find[eventMsg](messages(w.m.waitEvent())); !ok || !msg.open {
		t.Error("event")
	}
}

func TestOrphanedAndCareInHeader(t *testing.T) {
	w := setup(t)
	w.m.instances = append(w.m.instances, app.Summary{Name: "gone", Path: "/gone", Status: app.StatusPartial, Orphaned: true})
	if !strings.Contains(w.screen(), "1 need care") || !strings.Contains(w.screen(), "worktree gone · mutti tidy") {
		t.Errorf("care:\n%s", w.screen())
	}
	if padBetween("a", "b", 1) != "a b" || clip("a\nb", 1) != "a" || lastLines("a\nb\nc", 2) != "b\nc" || trunc("abc", -1) != "" {
		t.Error("helpers")
	}
}

func TestPausedCommand(t *testing.T) {
	var out bytes.Buffer
	p := &pausedCommand{cmd: exec.Command("true")}
	p.SetStdin(strings.NewReader("\n"))
	p.SetStdout(&out)
	p.SetStderr(&out)
	if err := p.Run(); err != nil || !strings.Contains(out.String(), "done · press enter") {
		t.Errorf("true: %v %q", err, out.String())
	}
	out.Reset()
	p = &pausedCommand{cmd: exec.Command("false")}
	p.SetStdin(strings.NewReader(""))
	p.SetStdout(&out)
	if err := p.Run(); err == nil || !strings.Contains(out.String(), "exit status 1") {
		t.Errorf("false: %v %q", err, out.String())
	}
}

func TestRun(t *testing.T) {
	w := apptest.New(t)
	var out bytes.Buffer
	if err := Run(context.Background(), Options{App: w.App, Dir: w.Root}, strings.NewReader("q"), &out); err != nil {
		t.Fatalf("q: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, Options{App: w.App, Dir: w.Root}, strings.NewReader(""), &out); err != nil {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestGoneWorktree(t *testing.T) {
	w := setup(t)
	if err := os.RemoveAll(w.Root); err != nil {
		t.Fatal(err)
	}
	feed[instancesMsg](t, w.m, w.m.loadInstances())
	if c := w.m.current(); c == nil || !c.Orphaned {
		t.Fatalf("orphaned: %+v", c)
	}
	if s := w.screen(); !strings.Contains(s, "Its worktree is gone.") || !strings.Contains(s, "X removes them") {
		t.Errorf("gone:\n%s", s)
	}
	if press(w.m, "u"); !strings.Contains(w.m.toast, "the worktree of shop is gone: X removes what is left") {
		t.Errorf("start: %q", w.m.toast)
	}
	// Destroy removes what is left without the worktree.
	press(w.m, "X")
	feed[doneMsg](t, w.m, press(w.m, "y"))
	if !w.m.toastOK || !strings.Contains(w.m.toast, "shop is gone") {
		t.Fatalf("destroy: %q", w.m.toast)
	}
	if list, _ := w.App.List(context.Background()); len(list) != 0 {
		t.Errorf("left: %+v", list)
	}
}

func TestDestroyWhateverState(t *testing.T) {
	w := setup(t)
	w.Compose.Exit = func(args []string) int { return map[string]int{"down": 1}[args[0]] }
	press(w.m, "X")
	feed[doneMsg](t, w.m, press(w.m, "y"))
	if !w.m.toastOK {
		t.Fatalf("a broken Compose does not stop destroy: %q", w.m.toast)
	}
	if list, _ := w.App.List(context.Background()); len(list) != 0 {
		t.Errorf("left: %+v", list)
	}
}

func TestTidy(t *testing.T) {
	w := setup(t)
	if press(w.m, "T"); !strings.Contains(w.m.toast, "nothing to tidy") {
		t.Errorf("nothing: %q", w.m.toast)
	}
	if err := os.RemoveAll(w.Root); err != nil {
		t.Fatal(err)
	}
	feed[instancesMsg](t, w.m, w.m.loadInstances())
	press(w.m, "T")
	if w.m.mode != modeConfirm || !strings.Contains(w.screen(), "Remove what 1 deleted worktree(s) left behind?") {
		t.Fatalf("asks:\n%s", w.screen())
	}
	feed[doneMsg](t, w.m, press(w.m, "y"))
	if w.m.toast != "removed 1 instance(s)" {
		t.Errorf("tidied: %q", w.m.toast)
	}
	w.Engine.FailOn = map[string]error{"ListVolumes": engine.ErrUnavailable}
	w.m.instances = []app.Summary{{Name: "x", Path: "/x", Orphaned: true}}
	press(w.m, "T")
	if done := feed[doneMsg](t, w.m, press(w.m, "y")); done.err == nil {
		t.Error("a failing tidy")
	}
}
