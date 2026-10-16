// Package server serves mutti over HTTP on the loopback interface: a web UI, a JSON API with the
// envelopes of --json, server-sent events, and the MCP server for agents that speak HTTP.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/mcpserver"
)

// SchemaVersion of the API's JSON, the same as the command line's.
const SchemaVersion = 1

//go:embed ui
var ui embed.FS

// Server is mutti's HTTP server for one machine.
type Server struct {
	app     *app.App
	dir     string // where mutti serve runs; the default worktree of the MCP tools
	version string
}

// New returns the server. dir is the directory mutti serve was started in.
func New(a *app.App, dir, version string) *Server { return &Server{app: a, dir: dir, version: version} }

// Handler routes the UI, the API and MCP. Every request must name a loopback host, so a web page
// that rebinds its own domain to 127.0.0.1 gets nothing; requests that change something must
// come from this origin.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(ui, "ui") // the embedded directory exists
	mux.Handle("/", http.FileServerFS(static))
	mux.HandleFunc("GET /api/instances", s.instances)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/diagnose", s.diagnose)
	mux.HandleFunc("GET /api/logs", s.logs)
	mux.HandleFunc("GET /api/logs/stream", s.logStream)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/doctor", s.doctor)
	mux.HandleFunc("POST /api/up", s.lifecycle("up"))
	mux.HandleFunc("POST /api/stop", s.lifecycle("stop"))
	mux.HandleFunc("POST /api/restart", s.lifecycle("restart"))
	mux.HandleFunc("POST /api/down", s.lifecycle("down"))
	mcpServer := mcpserver.New(s.app, s.dir, s.version)
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil))
	return loopbackOnly(http.NewCrossOriginProtection().Handler(mux))
}

// loopbackOnly refuses requests whose Host is not a loopback name or address.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "mutti serve answers only to localhost", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type envelope struct {
	SchemaVersion int        `json:"schemaVersion"`
	Data          any        `json:"data,omitempty"`
	Error         *jsonError `json:"error,omitempty"`
}

type jsonError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func reply(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(envelope{SchemaVersion: SchemaVersion, Data: data})
}

// fail answers with the error envelope: 400 for what the caller can fix, 500 otherwise.
func fail(w http.ResponseWriter, err error) {
	e := jsonError{Code: "error", Message: err.Error()}
	status := http.StatusInternalServerError
	var appErr *app.Error
	if errors.As(err, &appErr) {
		e.Code, e.Hint = appErr.Code, appErr.Hint
		status = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{SchemaVersion: SchemaVersion, Error: &e})
}

// worktree is the worktree a request names with ?dir=, an absolute path as list reports it.
func worktree(r *http.Request) (string, error) {
	dir := r.URL.Query().Get("dir")
	if !strings.HasPrefix(dir, "/") {
		return "", &app.Error{Code: "usage", Message: "dir must be the absolute path of a worktree", Hint: "use the path of an instance from /api/instances"}
	}
	return dir, nil
}

// call returns a copy of the app whose commands write into out.
func (s *Server) call() (*app.App, *output) {
	out := &output{}
	a := *s.app
	a.IO = compose.IO{In: strings.NewReader(""), Out: out, Err: out}
	a.Host.StdinTTY = false
	return &a, out
}

// output collects what commands print; the limit keeps a chatty log from filling memory.
type output struct {
	mu sync.Mutex
	b  []byte
}

const outputLimit = 1 << 20

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.b = append(o.b, p...)
	if len(o.b) > outputLimit {
		o.b = o.b[len(o.b)-outputLimit:]
	}
	return len(p), nil
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return string(o.b)
}

func (s *Server) instances(w http.ResponseWriter, r *http.Request) {
	a, _ := s.call()
	list, err := a.List(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, list)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	dir, err := worktree(r)
	if err != nil {
		fail(w, err)
		return
	}
	a, _ := s.call()
	st, err := a.Status(r.Context(), dir)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, st)
}

// diagnose explains the services that are not well, or those named with ?service=.
func (s *Server) diagnose(w http.ResponseWriter, r *http.Request) {
	dir, err := worktree(r)
	if err != nil {
		fail(w, err)
		return
	}
	a, _ := s.call()
	diags, err := a.Diagnose(r.Context(), dir, r.URL.Query()["service"]...)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, diags)
}

// doctor checks Docker, Compose, the router, HTTPS and the project mutti serve runs in.
func (s *Server) doctor(w http.ResponseWriter, r *http.Request) {
	a, _ := s.call()
	reply(w, a.Doctor(r.Context(), s.dir))
}

// Logs is the answer of /api/logs.
type Logs struct {
	Output string `json:"output"`
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	dir, err := worktree(r)
	if err != nil {
		fail(w, err)
		return
	}
	tail, err := strconv.Atoi(r.URL.Query().Get("tail"))
	if err != nil || tail <= 0 {
		tail = 200
	}
	args := []string{"logs", "--no-color", "--timestamps", "--tail", strconv.Itoa(tail)}
	if service := r.URL.Query().Get("service"); service != "" {
		args = append(args, service)
	}
	a, out := s.call()
	if _, err := a.ComposePassthrough(r.Context(), dir, app.Selection{}, args); err != nil {
		fail(w, err)
		return
	}
	reply(w, Logs{Output: out.String()})
}

// Done is the answer of the lifecycle actions.
type Done struct {
	Instance string `json:"instance"`
	URL      string `json:"url,omitempty"`
	Output   string `json:"output"`
}

func (s *Server) lifecycle(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dir, err := worktree(r)
		if err != nil {
			fail(w, err)
			return
		}
		a, out := s.call()
		// ?service= (repeatable) limits up, stop and restart to those services.
		services := r.URL.Query()["service"]
		var done Done
		switch action {
		case "up":
			var res *app.UpResult
			if res, err = a.Up(r.Context(), dir, app.UpOptions{Services: services}); err == nil {
				done.Instance, done.URL = res.Instance.Name, res.URL
			}
		case "stop", "restart":
			run := map[string]func(*app.App, context.Context, string, app.Selection, ...string) (*app.Instance, error){"stop": (*app.App).Stop, "restart": (*app.App).Restart}[action]
			var inst *app.Instance
			if inst, err = run(a, r.Context(), dir, app.Selection{}, services...); err == nil {
				done.Instance = inst.Name
			}
		default:
			var inst *app.Instance
			if inst, err = a.Down(r.Context(), dir, app.Selection{}); err == nil {
				done.Instance = inst.Name
			}
		}
		if err != nil {
			fail(w, fmt.Errorf("%w\n\n%s", err, out))
			return
		}
		done.Output = out.String()
		reply(w, done)
	}
}

// events streams instance events as server-sent events, one envelope per event, until the
// client goes away.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher := w.(http.Flusher) // net/http's writers flush
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events, errs := s.app.Events(ctx)
	for ev := range events {
		b, _ := json.Marshal(envelope{SchemaVersion: SchemaVersion, Data: ev}) // plain data
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			cancel()
			continue // drain until the stream ends
		}
		flusher.Flush()
	}
	select {
	case err := <-errs:
		b, _ := json.Marshal(envelope{SchemaVersion: SchemaVersion, Error: &jsonError{Code: "events-failed", Message: err.Error()}})
		_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", b)
	default:
	}
}
