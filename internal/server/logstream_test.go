package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/l0wskilled/mutti/internal/app"
)

// streamEvents reads a log stream to its end and returns its lines and the names of its events.
func streamEvents(t *testing.T, u string) (lines, events []string) {
	t.Helper()
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	sc := bufio.NewScanner(res.Body)
	name := ""
	for sc.Scan() {
		switch line := sc.Text(); {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if name != "" {
				events = append(events, name)
				name = ""
				continue
			}
			var batch []string
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &batch); err != nil {
				t.Fatalf("batch %q: %v", line, err)
			}
			lines = append(lines, batch...)
		}
	}
	return lines, events
}

func TestLogStream(t *testing.T) {
	w, ts := start(t)
	dir := "?dir=" + url.QueryEscape(w.Root)
	var args []string
	w.Compose.Stdout = func(a []string) string {
		args = a
		return "web-1  | one\nweb-1  | two\nno end"
	}
	if _, err := w.App.Up(t.Context(), w.Root, app.UpOptions{}); err != nil {
		t.Fatal(err)
	}
	lines, events := streamEvents(t, ts.URL+"/api/logs/stream"+dir+"&service=web")
	if !slices.Equal(lines, []string{"web-1  | one", "web-1  | two", "no end"}) || !slices.Equal(events, []string{"end"}) {
		t.Errorf("lines %q events %v", lines, events)
	}
	if !slices.Equal(args, []string{"logs", "--no-color", "--timestamps", "--follow", "--tail", "5000", "web"}) {
		t.Errorf("args %v", args)
	}
	streamEvents(t, ts.URL+"/api/logs/stream"+dir+"&tail=999999")
	if args[5] != "100000" {
		t.Errorf("tail capped: %v", args)
	}

	w.Compose.Exit = func(a []string) int { return 1 }
	if _, events := streamEvents(t, ts.URL+"/api/logs/stream"+dir); !slices.Equal(events, []string{"error", "end"}) {
		t.Errorf("failing compose: %v", events)
	}
	if code, _, e := call[any](t, http.MethodGet, ts.URL+"/api/logs/stream?dir=relative"); code != http.StatusBadRequest || e.Code != "usage" {
		t.Errorf("relative dir: %d %+v", code, e)
	}
}

func TestLineSenderFlushesWhileComposeRuns(t *testing.T) {
	rec := httptest.NewRecorder()
	l := newLineSender(rec, rec, time.Millisecond)
	_, _ = l.Write([]byte("a\nb"))
	done := make(chan struct{})
	go func() { time.Sleep(20 * time.Millisecond); close(done) }()
	l.flushUntil(done)
	l.flush() // nothing left
	if body := rec.Body.String(); body != "data: []\n\ndata: [\"a\"]\n\n" {
		t.Errorf("body %q", body)
	}
}
