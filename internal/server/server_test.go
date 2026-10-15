package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/apptest"
	"github.com/l0wskilled/mutti/internal/engine"
)

func start(t *testing.T) (*apptest.World, *httptest.Server) {
	t.Helper()
	w := apptest.New(t)
	ts := httptest.NewServer(New(w.App, w.Root, "test").Handler())
	t.Cleanup(ts.Close)
	return w, ts
}

// get calls the API and decodes the envelope; it returns the HTTP status too.
func call[T any](t *testing.T, method, u string) (int, T, *jsonError) {
	t.Helper()
	req, _ := http.NewRequest(method, u, http.NoBody)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var env struct {
		SchemaVersion int        `json:"schemaVersion"`
		Data          T          `json:"data"`
		Error         *jsonError `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	if env.SchemaVersion != SchemaVersion {
		t.Errorf("schema version %d", env.SchemaVersion)
	}
	return res.StatusCode, env.Data, env.Error
}

func TestAPI(t *testing.T) {
	w, ts := start(t)
	dir := "?dir=" + url.QueryEscape(w.Root)
	w.Compose.Stdout = func(args []string) string { return strings.Join(args, " ") + "\n" }

	code, up, _ := call[Done](t, http.MethodPost, ts.URL+"/api/up"+dir)
	if code != http.StatusOK || up.Instance != "shop" || up.URL != "https://shop.localhost" {
		t.Fatalf("up: %d %+v", code, up)
	}
	if code, _, _ := call[Done](t, http.MethodPost, ts.URL+"/api/up"+dir+"&service=web"); code != http.StatusOK || !slices.Contains(lastUp(w), "web") {
		t.Fatalf("up of one service: %d %v", code, lastUp(w))
	}
	if _, list, _ := call[[]app.Summary](t, http.MethodGet, ts.URL+"/api/instances"); len(list) != 1 || list[0].Status != app.StatusRunning {
		t.Fatalf("instances %+v", list)
	}
	if _, st, _ := call[app.InstanceStatus](t, http.MethodGet, ts.URL+"/api/status"+dir); st.Name != "shop" {
		t.Fatalf("status %+v", st)
	}
	if _, diags, _ := call[[]app.Diagnosis](t, http.MethodGet, ts.URL+"/api/diagnose"+dir+"&service=web"); len(diags) != 1 || diags[0].Service != "web" {
		t.Errorf("diagnose %+v", diags)
	}
	if _, logs, _ := call[Logs](t, http.MethodGet, ts.URL+"/api/logs"+dir); !strings.Contains(logs.Output, "logs --no-color --timestamps --tail 200\n") {
		t.Errorf("logs %q", logs.Output)
	}
	if _, logs, _ := call[Logs](t, http.MethodGet, ts.URL+"/api/logs"+dir+"&tail=5&service=web"); !strings.Contains(logs.Output, "--tail 5 web") {
		t.Errorf("logs of one service %q", logs.Output)
	}
	if _, done, _ := call[Done](t, http.MethodPost, ts.URL+"/api/restart"+dir+"&service=web"); done.Instance != "shop" || !slices.Equal(w.Compose.Runs[len(w.Compose.Runs)-1], []string{"restart", "web"}) {
		t.Errorf("restart web %+v", done)
	}
	if _, done, _ := call[Done](t, http.MethodPost, ts.URL+"/api/stop"+dir+"&service=web"); done.Instance != "shop" || !slices.Equal(w.Compose.Runs[len(w.Compose.Runs)-1], []string{"stop", "web"}) {
		t.Errorf("stop web %+v", done)
	}
	if _, done, _ := call[Done](t, http.MethodPost, ts.URL+"/api/stop"+dir); done.Instance != "shop" {
		t.Errorf("stop %+v", done)
	}
	if _, done, _ := call[Done](t, http.MethodPost, ts.URL+"/api/down"+dir); done.Instance != "shop" || !strings.Contains(done.Output, "down") {
		t.Errorf("down %+v", done)
	}

	if _, report, _ := call[app.Report](t, http.MethodGet, ts.URL+"/api/doctor"); len(report.Checks) == 0 {
		t.Errorf("doctor %+v", report)
	}

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if !strings.Contains(string(page), `<script src="app.js">`) {
		t.Errorf("ui %q", page)
	}
}

// lastUp returns the arguments of the last docker compose up.
func lastUp(w *apptest.World) []string {
	for i := len(w.Compose.Runs) - 1; i >= 0; i-- {
		if w.Compose.Runs[i][0] == "up" {
			return w.Compose.Runs[i]
		}
	}
	return nil
}

func TestAPIErrors(t *testing.T) {
	w, ts := start(t)
	elsewhere := "?dir=" + url.QueryEscape(t.TempDir())
	for _, u := range []string{"/api/status", "/api/diagnose", "/api/logs"} {
		if code, _, e := call[any](t, http.MethodGet, ts.URL+u+"?dir=relative"); code != http.StatusBadRequest || e.Code != "usage" {
			t.Errorf("%s with a relative dir: %d %+v", u, code, e)
		}
		if code, _, e := call[any](t, http.MethodGet, ts.URL+u+elsewhere); code != http.StatusBadRequest || e.Code != app.CodeNoComposeFile || e.Hint == "" {
			t.Errorf("%s outside a project: %d %+v", u, code, e)
		}
	}
	for _, action := range []string{"up", "stop", "restart", "down"} {
		if code, _, e := call[any](t, http.MethodPost, ts.URL+"/api/"+action+"?dir=relative"); code != http.StatusBadRequest || e.Code != "usage" {
			t.Errorf("%s with a relative dir: %d %+v", action, code, e)
		}
		if code, _, e := call[any](t, http.MethodPost, ts.URL+"/api/"+action+elsewhere); code != http.StatusBadRequest || e.Code != app.CodeNoComposeFile {
			t.Errorf("%s outside a project: %d %+v", action, code, e)
		}
	}
	// What compose printed comes with a failure; an error mutti cannot name is a 500.
	w.Compose.Stderr = func([]string) string { return "port is already allocated\n" }
	w.Compose.Exit = func([]string) int { return 1 }
	if code, _, e := call[any](t, http.MethodPost, ts.URL+"/api/up?dir="+url.QueryEscape(w.Root)); code != http.StatusBadRequest || !strings.Contains(e.Message, "port is already allocated") {
		t.Errorf("failing up: %d %+v", code, e)
	}
	w.Compose.Exit, w.Compose.RunErr = nil, errors.New("boom")
	if code, _, e := call[any](t, http.MethodPost, ts.URL+"/api/up?dir="+url.QueryEscape(w.Root)); code != http.StatusInternalServerError || e.Code != "error" {
		t.Errorf("unnamed failure: %d %+v", code, e)
	}
	w.Engine.FailOn = map[string]error{"ListVolumes": engine.ErrUnavailable}
	if code, _, e := call[any](t, http.MethodGet, ts.URL+"/api/instances"); code != http.StatusBadRequest || e.Code != app.CodeDockerUnavailable {
		t.Errorf("instances: %d %+v", code, e)
	}
}

func TestOnlyLocalhostAndOnlyThisOrigin(t *testing.T) {
	_, ts := start(t)
	for host, want := range map[string]int{
		"evil.example":    http.StatusForbidden, // a page that rebound its domain to 127.0.0.1
		"10.0.0.1:7477":   http.StatusForbidden,
		"localhost:7477":  http.StatusOK,
		"[::1]:7477":      http.StatusOK,
		"127.0.0.1":       http.StatusOK,
		"127.0.0.1:12345": http.StatusOK,
	} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", http.NoBody)
		req.Host = host
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("Host %s: %d, want %d", host, res.StatusCode, want)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/stop?dir=/x", http.NoBody)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://evil.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST: %d", res.StatusCode)
	}
}

func TestEventStream(t *testing.T) {
	w, ts := start(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", http.NoBody)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	lines := bufio.NewScanner(res.Body)
	if !lines.Scan() || lines.Text() != ": connected" {
		t.Fatalf("first line %q", lines.Text())
	}
	for deadline := time.Now().Add(5 * time.Second); w.Engine.Subscribers() == 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	w.Engine.AddContainer(engine.Container{Name: "shop-web-1", State: "exited", Labels: map[string]string{engine.LabelInstance: "shop", engine.LabelComposeService: "web"}})
	for lines.Scan() {
		if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
			var env struct{ Data app.InstanceEvent }
			if err := json.Unmarshal([]byte(data), &env); err != nil || env.Data.Event != "created" || env.Data.Service != "web" {
				t.Fatalf("event %s: %v", data, err)
			}
			return
		}
	}
	t.Fatal("no event")
}

func TestEventStreamFailure(t *testing.T) {
	w, ts := start(t)
	w.Engine.FailOn = map[string]error{"Events": engine.ErrUnavailable}
	res, err := http.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if !strings.Contains(string(body), "event: error\ndata: ") || !strings.Contains(string(body), "events-failed") {
		t.Errorf("body %q", body)
	}
}

// goneClient fails every write, as a connection to a client that went away does.
type goneClient struct{ header http.Header }

func (g *goneClient) Header() http.Header       { return g.header }
func (g *goneClient) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (g *goneClient) WriteHeader(int)           {}
func (g *goneClient) Flush()                    {}

func TestEventStreamEndsWhenTheClientIsGone(t *testing.T) {
	w := apptest.New(t)
	s := New(w.App, w.Root, "test")
	done := make(chan struct{})
	go func() {
		s.events(&goneClient{header: http.Header{}}, httptest.NewRequest(http.MethodGet, "/api/events", http.NoBody))
		close(done)
	}()
	for deadline := time.Now().Add(5 * time.Second); w.Engine.Subscribers() == 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	w.Engine.AddContainer(engine.Container{Name: "a", State: "exited", Labels: map[string]string{engine.LabelInstance: "shop"}})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived its client")
	}
}

func TestMCPOverHTTP(t *testing.T) {
	_, ts := start(t)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) == 0 {
		t.Fatalf("tools: %v %v", tools, err)
	}
}

func TestOutputKeepsTheEnd(t *testing.T) {
	var o output
	_, _ = o.Write([]byte(strings.Repeat("a", outputLimit)))
	_, _ = o.Write([]byte("end"))
	if s := o.String(); len(s) != outputLimit || !strings.HasSuffix(s, "end") {
		t.Fatalf("len %d", len(s))
	}
}
