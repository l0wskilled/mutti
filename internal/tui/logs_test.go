package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// follow feeds the logs stream that cmd starts to the UI until it ends.
func (w *world) follow(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	for cmd != nil {
		msg := feed[logLinesMsg](t, w.m, cmd)
		if msg.ended {
			return
		}
		cmd = w.m.nextLogs()
	}
}

func TestLogsStreamAndFilter(t *testing.T) {
	w := setup(t)
	lines := "web-1  | 2026-01-01T00:00:00Z hello\nweb-1  | 2026-01-01T00:00:01Z ERROR boom\nweb-1  | 2026-01-01T00:00:02Z warn: slow\nplain line\n"
	for i := range 60 {
		lines += fmt.Sprintf("web-1  | 2026-01-01T00:01:00Z line %d\n", i)
	}
	w.Compose.Stdout = func(args []string) string {
		if args[0] == "logs" {
			return lines
		}
		return ""
	}
	press(w.m, "tab")
	cmd := press(w.m, "l")
	if w.m.mode != modeLogs || !strings.Contains(w.screen(), "waiting for logs") {
		t.Fatalf("opened:\n%s", w.screen())
	}
	w.follow(t, cmd)
	run := w.Compose.Runs[len(w.Compose.Runs)-1]
	if !slices.Contains(run, "--follow") || run[len(run)-1] != "web" {
		t.Errorf("compose %v", run)
	}
	s := w.screen()
	for _, want := range []string{"Logs of web · ended", "line 59", "/ filter • f follow (on) • esc back"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in\n%s", want, s)
		}
	}

	// / filters as it is typed; enter keeps the filter, esc drops it.
	press(w.m, "/", "b", "o", "o", "m")
	if s := w.screen(); !strings.Contains(s, `1 of 64 lines with "boom"`) || !strings.Contains(s, "ERROR boom") || strings.Contains(s, "hello") || !strings.Contains(s, "enter keep • esc clear") {
		t.Errorf("filtered:\n%s", s)
	}
	w.m.Update(tea.PasteMsg{Content: "x"}) // what is not a key goes to the filter too
	if !strings.Contains(w.screen(), `no line has "boomx"`) {
		t.Errorf("no match:\n%s", w.screen())
	}
	press(w.m, "backspace", "enter")
	if w.m.logFiltering || w.m.logFilter.Value() != "boom" || w.m.mode != modeLogs {
		t.Errorf("kept %q", w.m.logFilter.Value())
	}
	press(w.m, "/", "esc")
	if w.m.logFilter.Value() != "" || w.m.mode != modeLogs || !strings.Contains(w.screen(), "line 59") {
		t.Errorf("cleared:\n%s", w.screen())
	}

	// Scrolling up stops following, f takes it up again.
	press(w.m, "up")
	if w.m.follow || !strings.Contains(w.screen(), "follow (off)") {
		t.Error("scrolled up")
	}
	press(w.m, "f")
	if !w.m.follow || !w.m.viewer.AtBottom() {
		t.Error("f follows")
	}
	press(w.m, "f")
	if w.m.follow {
		t.Error("f stops")
	}
	w.m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})

	// A stream the view no longer shows changes nothing; q leaves and stops the stream.
	if w.update(logLinesMsg{gen: w.m.logsGen - 1, lines: []string{"stale"}}) != nil || strings.Contains(w.screen(), "stale") {
		t.Error("stale stream")
	}
	press(w.m, "q")
	if w.m.mode != modeMain || w.m.logsCancel != nil {
		t.Error("q leaves")
	}
	// f in the diagnosis view does nothing; the wheel scrolls it.
	w.m.openViewer(modeDiagnose, "x", "y")
	if press(w.m, "f"); w.m.mode != modeDiagnose {
		t.Error("f in diagnose")
	}
	w.m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
}

func TestLogsFailAndKeepTheNewest(t *testing.T) {
	w := setup(t)
	w.Compose.Stdout = func(args []string) string { return "no end of line" }
	w.Compose.Exit = func(args []string) int { return 1 }
	w.follow(t, press(w.m, "l"))
	if s := w.screen(); !strings.Contains(s, "no end of line") || !strings.Contains(s, "✗ docker compose logs ended with exit code 1") {
		t.Errorf("failed:\n%s", s)
	}
	many := make([]string, maxLogLines+5)
	w.m.logsEnded = false
	w.m.Update(logLinesMsg{gen: w.m.logsGen, lines: many, ended: true})
	if len(w.m.logLines) != maxLogLines {
		t.Errorf("kept %d", len(w.m.logLines))
	}
}

func TestLineWriter(t *testing.T) {
	ch := make(chan logChunk, 4)
	w := &lineWriter{ctx: context.Background(), ch: ch}
	_, _ = w.Write([]byte("a\nb"))
	_, _ = w.Write([]byte("c\n"))
	w.end(errors.New("gone"))
	var got []string
	var err error
	for c := range ch {
		got, err = append(got, c.lines...), c.err
	}
	if !slices.Equal(got, []string{"a", "bc"}) || err == nil {
		t.Errorf("%q %v", got, err)
	}
	// Nobody reads after the view went away: writing does not block.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	blocked := &lineWriter{ctx: ctx, ch: make(chan logChunk)}
	if _, err := blocked.Write([]byte("x\n")); err != nil {
		t.Error(err)
	}
}

func TestMark(t *testing.T) {
	w := setup(t)
	st := w.m.styles.muted
	if got := ansi.Strip(mark("Boom and boom", "boom", st)); got != "Boom and boom" {
		t.Errorf("text kept: %q", got)
	}
	if mark("Boom and boom", "boom", st) == st.Render("Boom and boom") {
		t.Error("marked")
	}
	if mark("İstanbul", "i", st) != st.Render("İstanbul") {
		t.Error("a case change that alters the length")
	}
}

func TestNextLogsTakesWhatWaits(t *testing.T) {
	w := setup(t)
	w.m.mode = modeLogs
	ch := make(chan logChunk, 4)
	w.m.logsCh = ch
	// Two chunks waiting come as one message; the stream goes on.
	ch <- logChunk{lines: []string{"a"}}
	ch <- logChunk{lines: []string{"b"}}
	msg := feed[logLinesMsg](t, w.m, w.m.nextLogs())
	if !slices.Equal(msg.lines, []string{"a", "b"}) || msg.ended || w.update(msg) == nil {
		t.Errorf("batch %+v", msg)
	}
	// An error is kept to the end of the stream.
	ch <- logChunk{err: errors.New("boom")}
	close(ch)
	if msg := feed[logLinesMsg](t, w.m, w.m.nextLogs()); !msg.ended || msg.err == nil {
		t.Errorf("end %+v", msg)
	}
	if msg := feed[logLinesMsg](t, w.m, w.m.nextLogs()); !msg.ended || len(msg.lines) != 0 {
		t.Errorf("closed %+v", msg)
	}
	// The wheel scrolls the logs.
	w.m.follow = true
	w.m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if w.m.follow != w.m.viewer.AtBottom() {
		t.Error("wheel")
	}
}
