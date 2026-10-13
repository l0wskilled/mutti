package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/apptest"
	"github.com/l0wskilled/mutti/internal/compose/composetest"
)

// setup returns a client session to a server over a git repo "shop" with a web service, and
// the fake Compose behind it.
func setup(t *testing.T) (*mcp.ClientSession, *composetest.Fake, string) {
	t.Helper()
	w := apptest.New(t)
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := New(w.App, w.Root, "test").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, w.Compose, w.Root
}

// call calls a tool and decodes its structured result into out; it returns the text of a tool error.
func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any, out any) string {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		return res.Content[0].(*mcp.TextContent).Text
	}
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v in %s", name, err, b)
	}
	return ""
}

func TestTools(t *testing.T) {
	s, fc, root := setup(t)
	fc.Stdout = func(args []string) string { return strings.Join(args, " ") + " printed\n" }

	var started Started
	if e := call(t, s, "up", nil, &started); e != "" || started.Instance != "shop" || started.URL != "https://shop.localhost" || !started.Bootstrapped {
		t.Fatalf("up: %s %+v", e, started)
	}
	var st app.InstanceStatus
	if e := call(t, s, "status", map[string]any{"dir": root}, &st); e != "" || st.Name != "shop" || len(st.Services) != 1 {
		t.Fatalf("status: %s %+v", e, st)
	}
	var list Instances
	if e := call(t, s, "list", nil, &list); e != "" || len(list.Instances) != 1 {
		t.Fatalf("list: %s %+v", e, list)
	}
	var u URL
	if e := call(t, s, "url", map[string]any{"service": "web"}, &u); e != "" || u.URL != "https://shop.localhost" {
		t.Fatalf("url: %s %+v", e, u)
	}

	fc.Exit = func(args []string) int {
		if args[0] == "exec" {
			return 3
		}
		return 0
	}
	var ex Exited
	e := call(t, s, "exec", map[string]any{"service": "web", "command": []string{"php", "-v"}, "workdir": "/app", "env": map[string]string{"B": "2", "A": "1"}}, &ex)
	if e != "" || ex.ExitCode != 3 || !strings.Contains(ex.Output, "printed") {
		t.Fatalf("exec: %s %+v", e, ex)
	}
	last := fc.Runs[len(fc.Runs)-1]
	if !slices.Contains(last, "/app") || slices.Index(last, "A=1") > slices.Index(last, "B=2") || last[len(last)-2] != "php" {
		t.Errorf("exec args %v", last)
	}
	fc.Exit = nil

	var logs Output
	if e := call(t, s, "logs", map[string]any{"services": []string{"web"}}, &logs); e != "" || !strings.Contains(logs.Output, "logs --no-color --tail 200 web printed") {
		t.Fatalf("logs: %s %+v", e, logs)
	}
	if e := call(t, s, "logs", map[string]any{"tail": 5}, &logs); e != "" || !strings.Contains(logs.Output, "--tail 5 printed") {
		t.Fatalf("logs tail: %s %+v", e, logs)
	}

	var ran Ran
	if e := call(t, s, "run", map[string]any{"command": []string{"sh", "-c", "echo tests passed; exit 4"}}, &ran); e != "" || ran.ExitCode != 4 || ran.RunID == "" || !strings.Contains(ran.Output, "tests passed") {
		t.Fatalf("run: %s %+v", e, ran)
	}

	var wt Worktree
	if e := call(t, s, "worktree_add", map[string]any{"name": "feature"}, &wt); e != "" || wt.Branch != "feature" || wt.Started == nil || wt.Started.Hostname != "feature.shop.localhost" {
		t.Fatalf("worktree_add: %s %+v", e, wt)
	}
	var quiet Worktree
	if e := call(t, s, "worktree_add", map[string]any{"name": "quiet", "noUp": true}, &quiet); e != "" || quiet.Started != nil {
		t.Fatalf("worktree_add without up: %s %+v", e, quiet)
	}
	// A relative dir is taken from where the server runs.
	if e := call(t, s, "status", map[string]any{"dir": "../shop-feature"}, &st); e != "" || st.Name != "shop-feature" {
		t.Fatalf("relative dir: %s %+v", e, st)
	}

	var done Done
	if e := call(t, s, "stop", nil, &done); e != "" || done.Instance != "shop" {
		t.Fatalf("stop: %s %+v", e, done)
	}
	if e := call(t, s, "down", nil, &done); e != "" || done.Instance != "shop" {
		t.Fatalf("down: %s %+v", e, done)
	}
}

func TestToolErrorsCarryCodeAndHint(t *testing.T) {
	s, fc, root := setup(t)
	for _, tool := range []string{"status", "url", "logs", "up", "stop", "down", "worktree_add"} {
		args := map[string]any{"dir": t.TempDir()}
		if tool == "worktree_add" {
			args = map[string]any{"dir": filepath.Dir(root), "name": "x"}
		}
		if e := call(t, s, tool, args, &struct{}{}); !strings.Contains(e, "no-compose-file") && !strings.Contains(e, "not-git") {
			t.Errorf("%s: %q", tool, e)
		}
	}
	for _, tool := range []string{"exec", "run"} {
		empty := map[string]any{"command": []string{}}
		if tool == "exec" {
			empty["service"] = "web"
		}
		if e := call(t, s, tool, empty, &struct{}{}); !strings.Contains(e, "command is empty") {
			t.Errorf("%s without command: %q", tool, e)
		}
		args := map[string]any{"dir": t.TempDir(), "command": []string{"true"}}
		if tool == "exec" {
			args["service"] = "web"
		}
		if e := call(t, s, tool, args, &struct{}{}); !strings.Contains(e, "no-compose-file") || !strings.Contains(e, "hint: ") {
			t.Errorf("%s: %q", tool, e)
		}
	}

	// A failing up says why: the output of compose comes along.
	fc.Exit = func([]string) int { return 1 }
	fc.Stderr = func([]string) string { return "port is already allocated\n" }
	if e := call(t, s, "up", nil, &struct{}{}); !strings.Contains(e, "compose-failed") || !strings.Contains(e, "port is already allocated") {
		t.Errorf("up: %q", e)
	}
	fc.Exit, fc.Stderr = nil, nil

	// An error that is no app.Error comes through as it is.
	if err := toolErr(io.EOF); !errors.Is(err, io.EOF) {
		t.Errorf("plain error: %v", err)
	}
}

func TestListFailure(t *testing.T) {
	s, fc, _ := setup(t)
	fc.Eng.FailOn = map[string]error{"ListVolumes": io.ErrUnexpectedEOF}
	if e := call(t, s, "list", nil, &struct{}{}); e == "" {
		t.Error("want error")
	}
}

func TestOutputKeepsTheEnd(t *testing.T) {
	var c capped
	_, _ = c.Write([]byte(strings.Repeat("a", outputLimit)))
	_, _ = c.Write([]byte("end"))
	o := c.result()
	if !o.Truncated || len(o.Output) != outputLimit || !strings.HasSuffix(o.Output, "end") {
		t.Fatalf("truncated=%v len=%d", o.Truncated, len(o.Output))
	}
	if err := withOutput(io.EOF, &capped{}); !errors.Is(err, io.EOF) {
		t.Errorf("no output: %v", err)
	}
}
