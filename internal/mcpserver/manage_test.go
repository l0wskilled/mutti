package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/apptest"
	"github.com/l0wskilled/mutti/internal/engine"
)

func TestManageTools(t *testing.T) {
	w := apptest.New(t)
	s := connect(t, w)

	var started Started
	if e := call(t, s, "up", nil, &started); e != "" {
		t.Fatalf("up: %s", e)
	}
	if e := call(t, s, "reset", map[string]any{"build": true}, &started); e != "" || started.Instance != "shop" || !started.Bootstrapped {
		t.Fatalf("reset: %s %+v", e, started)
	}

	var env Env
	if e := call(t, s, "env", nil, &env); e != "" || env.Variables["MUTTI_INSTANCE"] != "shop" {
		t.Fatalf("env: %s %+v", e, env)
	}
	var ex Exited
	if e := call(t, s, "compose", map[string]any{"args": []string{"ps"}}, &ex); e != "" || ex.ExitCode != 0 {
		t.Fatalf("compose: %s %+v", e, ex)
	}
	var report app.Report
	if e := call(t, s, "doctor", nil, &report); e != "" || len(report.Checks) == 0 {
		t.Fatalf("doctor: %s %+v", e, report)
	}
	var u URL
	if e := call(t, s, "open", nil, &u); e != "" || u.URL != "https://shop.localhost" {
		t.Fatalf("open: %s %+v", e, u)
	}

	var router app.RouterStatus
	if e := call(t, s, "router_status", nil, &router); e != "" || len(router.Instances) != 1 {
		t.Fatalf("router_status: %s %+v", e, router)
	}
	if e := call(t, s, "router_restart", nil, &router); e != "" || len(router.Instances) != 1 {
		t.Fatalf("router_restart: %s %+v", e, router)
	}

	var plan Initialized
	if e := call(t, s, "init", map[string]any{"noAgents": true}, &plan); e != "" || plan.Written || plan.XMutti == "" {
		t.Fatalf("init: %s %+v", e, plan)
	}
	if e := call(t, s, "init", map[string]any{"noAgents": true, "apply": true}, &plan); e != "" || !plan.Written {
		t.Fatalf("init apply: %s %+v", e, plan)
	}
	if b, _ := os.ReadFile(filepath.Join(w.Root, "compose.yml")); !strings.Contains(string(b), "x-mutti") {
		t.Errorf("compose.yml after init: %s", b)
	}

	var wt Worktree
	if e := call(t, s, "worktree_add", map[string]any{"name": "feature", "overlays": []string{}, "build": true}, &wt); e != "" {
		t.Fatalf("worktree_add: %s", e)
	}
	var removed Removed
	if e := call(t, s, "worktree_remove", map[string]any{"target": "feature"}, &removed); e != "" || removed.Path != wt.Path {
		t.Fatalf("worktree_remove: %s %+v", e, removed)
	}

	var tidy app.TidyResult
	if e := call(t, s, "tidy", map[string]any{"idle": "1h", "apply": true}, &tidy); e != "" {
		t.Fatalf("tidy: %s", e)
	}
	var swept Swept
	if e := call(t, s, "sweep", map[string]any{"olderThan": "1h"}, &swept); e != "" || swept.Hooks {
		t.Fatalf("sweep: %s %+v", e, swept)
	}
	if e := call(t, s, "sweep", map[string]any{"dir": t.TempDir()}, &swept); e != "" {
		t.Fatalf("sweep outside a project: %s", e)
	}

	var done Done
	if e := call(t, s, "destroy", nil, &done); e != "" || done.Instance != "shop" {
		t.Fatalf("destroy: %s %+v", e, done)
	}
	var renamed Renamed
	if e := call(t, s, "rename", map[string]any{"name": "store"}, &renamed); e != "" || renamed.Instance != "shop-store" || renamed.Hostname != "store.shop.localhost" {
		t.Fatalf("rename: %s %+v", e, renamed)
	}
}

func TestAttachTool(t *testing.T) {
	w := apptest.New(t)
	s := connect(t, w)
	if e := call(t, s, "attach", nil, &struct{}{}); !strings.Contains(e, "only works inside a container") {
		t.Fatalf("on the host: %q", e)
	}
	w.App.Host.ContainerID = w.Engine.AddContainer(engine.Container{Name: "self", State: "running"})
	w.Engine.SetMounts(w.App.Host.ContainerID, []engine.Mount{{Source: "/", Target: "/"}})
	if e := call(t, s, "up", nil, &struct{}{}); e != "" {
		t.Fatalf("up: %s", e)
	}
	var at Attached
	if e := call(t, s, "attach", nil, &at); e != "" || at.Instance != "shop" || !at.Attached {
		t.Fatalf("attach: %s %+v", e, at)
	}
	if e := call(t, s, "attach", map[string]any{"detach": true}, &at); e != "" || at.Attached {
		t.Fatalf("detach: %s %+v", e, at)
	}
}

func TestCustomCommandTools(t *testing.T) {
	w := apptest.New(t)
	s := connect(t, w)
	cmds := filepath.Join(w.Root, ".mutti", "commands")
	writeCommand(t, filepath.Join(cmds, "host", "hello"), "#!/bin/sh\n# Say hello\necho \"hello $*\"\n")
	writeCommand(t, filepath.Join(cmds, "web", "migrate"), "# Migrate\nmigrate\n")

	var list Commands
	if e := call(t, s, "commands", nil, &list); e != "" || len(list.Commands) != 2 ||
		list.Commands[0] != (CustomCommand{Name: "hello", Short: "Say hello"}) || list.Commands[1].Service != "web" {
		t.Fatalf("commands: %s %+v", e, list)
	}
	var ex Exited
	if e := call(t, s, "command", map[string]any{"name": "hello", "args": []string{"there"}}, &ex); e != "" || ex.ExitCode != 0 || ex.Output != "hello there\n" {
		t.Fatalf("command: %s %+v", e, ex)
	}
	if e := call(t, s, "command", map[string]any{"name": "nope"}, &ex); !strings.Contains(e, `no command "nope"`) {
		t.Errorf("unknown command: %q", e)
	}

	// A project without a Compose file has commands, but no environment to run them in.
	bare := t.TempDir()
	writeCommand(t, filepath.Join(bare, ".mutti", "commands", "host", "hello"), "echo hi\n")
	if e := call(t, s, "command", map[string]any{"dir": bare, "name": "hello"}, &ex); !strings.Contains(e, "no-compose-file") {
		t.Errorf("without a Compose file: %q", e)
	}

	if os.Geteuid() == 0 {
		return // root reads the locked dir anyway
	}
	if err := os.Chmod(cmds, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cmds, 0o755) })
	if e := call(t, s, "commands", nil, &list); !strings.Contains(e, "permission denied") {
		t.Errorf("commands of a locked dir: %q", e)
	}
	if e := call(t, s, "command", map[string]any{"name": "hello"}, &ex); !strings.Contains(e, "permission denied") {
		t.Errorf("command of a locked dir: %q", e)
	}
}

func writeCommand(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil { //nolint:gosec // a command has to be executable
		t.Fatal(err)
	}
}

func TestManageToolErrors(t *testing.T) {
	w := apptest.New(t)
	s := connect(t, w)
	outside := map[string]any{"dir": t.TempDir()}
	for _, tool := range []string{"reset", "destroy", "env", "init", "open"} {
		if e := call(t, s, tool, outside, &struct{}{}); !strings.Contains(e, "no-compose-file") {
			t.Errorf("%s: %q", tool, e)
		}
	}
	if e := call(t, s, "compose", map[string]any{"dir": t.TempDir(), "args": []string{"ps"}}, &struct{}{}); !strings.Contains(e, "no-compose-file") {
		t.Errorf("compose: %q", e)
	}
	if e := call(t, s, "compose", map[string]any{"args": []string{}}, &struct{}{}); !strings.Contains(e, "args is empty") {
		t.Errorf("compose without args: %q", e)
	}
	if e := call(t, s, "worktree_remove", map[string]any{"dir": t.TempDir(), "target": "x"}, &struct{}{}); !strings.Contains(e, "not-git") {
		t.Errorf("worktree_remove: %q", e)
	}
	if e := call(t, s, "rename", map[string]any{"name": "Bad Name"}, &struct{}{}); !strings.Contains(e, "invalid-name") {
		t.Errorf("rename: %q", e)
	}
	if e := call(t, s, "tidy", map[string]any{"idle": "soon"}, &struct{}{}); !strings.Contains(e, "idle: ") {
		t.Errorf("tidy with a bad duration: %q", e)
	}
	if e := call(t, s, "sweep", map[string]any{"olderThan": "soon"}, &struct{}{}); !strings.Contains(e, "olderThan: ") {
		t.Errorf("sweep with a bad duration: %q", e)
	}

	if os.Geteuid() != 0 { // root writes into the locked dir anyway
		// The first init only shows the changes and sets the environment up; then writing fails.
		if e := call(t, s, "init", nil, &struct{}{}); e != "" {
			t.Fatal(e)
		}
		if err := os.Chmod(w.Root, 0o555); err != nil {
			t.Fatal(err)
		}
		e := call(t, s, "init", map[string]any{"apply": true}, &struct{}{})
		_ = os.Chmod(w.Root, 0o755)
		if !strings.Contains(e, "permission denied") {
			t.Errorf("init into a locked dir: %q", e)
		}
	}

	// Broken settings fail the sweep hooks, after the runs are swept.
	w.App.Host.Getenv = func(k string) string { return map[string]string{"MUTTI_HTTP_PORT": "x"}[k] }
	if e := call(t, s, "sweep", nil, &struct{}{}); e == "" {
		t.Error("sweep with broken settings: want error")
	}
	w.App.Host.Getenv = func(string) string { return "" }

	w.Engine.FailOn = map[string]error{"ListNetworks": engine.ErrUnavailable, "ListVolumes": engine.ErrUnavailable, "ListContainers": engine.ErrUnavailable, "InspectContainer": engine.ErrUnavailable}
	for _, tool := range []string{"tidy", "sweep", "router_status", "router_restart"} {
		if e := call(t, s, tool, nil, &struct{}{}); !strings.Contains(e, "docker-unavailable") {
			t.Errorf("%s with docker down: %q", tool, e)
		}
	}
}
