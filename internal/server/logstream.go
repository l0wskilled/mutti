package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/compose"
)

// Log streams start with this many lines unless ?tail= asks for others.
const (
	streamTail    = 5000
	maxStreamTail = 100000
)

// flushEvery bundles log lines into one event, so a chatty service does not cost a browser
// render per line.
const flushEvery = 100 * time.Millisecond

// logStream sends the instance's logs as server-sent events: the last lines first, then what
// the services print, until the client goes away. Each event carries a JSON array of lines;
// "event: end" closes the stream, after "event: error" when Compose failed.
func (s *Server) logStream(w http.ResponseWriter, r *http.Request) {
	dir, err := worktree(r)
	if err != nil {
		fail(w, err)
		return
	}
	tail, err := strconv.Atoi(r.URL.Query().Get("tail"))
	if err != nil || tail <= 0 {
		tail = streamTail
	}
	args := []string{"logs", "--no-color", "--timestamps", "--follow", "--tail", strconv.Itoa(min(tail, maxStreamTail))}
	if service := r.URL.Query().Get("service"); service != "" {
		args = append(args, service)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	lines := newLineSender(w, w.(http.Flusher), flushEvery) // net/http's writers flush
	done := make(chan struct{})
	go lines.flushUntil(done)

	a, _ := s.call()
	a.IO = compose.IO{In: a.IO.In, Out: lines, Err: lines}
	code, err := a.ComposePassthrough(r.Context(), dir, app.Selection{}, args)
	if err == nil && code != 0 {
		err = fmt.Errorf("docker compose logs ended with exit code %d", code)
	}
	close(done)
	lines.flush()
	if err != nil && r.Context().Err() == nil {
		lines.event("error", envelope{SchemaVersion: SchemaVersion, Error: &jsonError{Code: "logs-failed", Message: err.Error()}})
	}
	lines.event("end", envelope{SchemaVersion: SchemaVersion})
}

// lineSender collects what Compose prints into whole lines and sends them in batches. Compose
// writes stdout and stderr from goroutines of its own, so everything is under a lock.
type lineSender struct {
	mu      sync.Mutex
	w       io.Writer
	flusher http.Flusher
	every   time.Duration
	partial []byte   // the start of a line not yet ended
	lines   []string // ended lines not yet sent
}

func newLineSender(w io.Writer, f http.Flusher, every time.Duration) *lineSender {
	l := &lineSender{w: w, flusher: f, every: every}
	l.event("", []string{}) // the stream is open
	return l
}

func (l *lineSender) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.partial = append(l.partial, p...)
	for {
		i := bytes.IndexByte(l.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		l.lines = append(l.lines, string(l.partial[:i]))
		l.partial = l.partial[i+1:]
	}
}

func (l *lineSender) flushUntil(done <-chan struct{}) {
	t := time.NewTicker(l.every)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			l.flush()
		}
	}
}

// flush sends the lines collected so far.
func (l *lineSender) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return
	}
	lines := l.lines
	l.lines = nil
	l.send("", lines)
}

// event sends a named event, after the lines collected so far, a line without its end
// included.
func (l *lineSender) event(name string, data any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.partial) > 0 {
		l.lines, l.partial = append(l.lines, string(l.partial)), nil
	}
	if len(l.lines) > 0 {
		l.send("", l.lines)
		l.lines = nil
	}
	l.send(name, data)
}

func (l *lineSender) send(name string, data any) {
	b, _ := json.Marshal(data) // strings and plain structs
	if name != "" {
		fmt.Fprintf(l.w, "event: %s\n", name)
	}
	fmt.Fprintf(l.w, "data: %s\n\n", b)
	l.flusher.Flush()
}
