package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/l0wskilled/mutti/internal/extend"
)

func TestRunHostCommand(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	var out strings.Builder
	h.app.IO.Out = &out

	script := filepath.Join(t.TempDir(), "hello")
	writeFile(t, script, "#!/bin/sh\necho \"$MUTTI_INSTANCE $(pwd) $*\"\nexit 3\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	code, err := h.app.RunCommand(ctx, root, extend.Command{Name: "hello", Path: script}, []string{"a", "b"})
	if err != nil || code != 3 || out.String() != "shop "+root+" a b\n" {
		t.Fatalf("code=%d err=%v out=%q", code, err, out.String())
	}

	// Not executable: still runs as a shell script.
	plain := filepath.Join(t.TempDir(), "plain")
	writeFile(t, plain, "echo plain\n")
	out.Reset()
	if code, err := h.app.RunCommand(ctx, root, extend.Command{Name: "plain", Path: plain}, nil); err != nil || code != 0 || out.String() != "plain\n" {
		t.Fatalf("plain: %d %v %q", code, err, out.String())
	}

	if _, err := h.app.RunCommand(ctx, root, extend.Command{Name: "gone", Path: filepath.Join(t.TempDir(), "gone")}, nil); err == nil {
		t.Error("missing script: want error")
	}
	if _, err := h.app.RunCommand(ctx, t.TempDir(), extend.Command{Name: "hello", Path: script}, nil); err == nil {
		t.Error("no project: want error")
	}
	// Exists but cannot start (a directory).
	if _, err := h.app.RunCommand(ctx, root, extend.Command{Name: "dir", Path: t.TempDir()}, nil); err == nil {
		t.Error("directory: want error")
	}
}

func TestRunServiceCommand(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	script := filepath.Join(t.TempDir(), "migrate")
	writeFile(t, script, "#!/bin/bash\nbin/console migrate \"$@\"\n")
	if _, err := h.app.RunCommand(ctx, root, extend.Command{Name: "migrate", Service: "web", Path: script}, []string{"-n"}); err != nil {
		t.Fatal(err)
	}
	last := h.compose.Runs[len(h.compose.Runs)-1]
	want := []string{"web", "bash", "-c", "#!/bin/bash\nbin/console migrate \"$@\"\n", "migrate", "-n"}
	if !slices.Equal(last[len(last)-len(want):], want) {
		t.Fatalf("args %q", last)
	}
	if _, err := h.app.RunCommand(ctx, root, extend.Command{Name: "x", Service: "web", Path: filepath.Join(t.TempDir(), "gone")}, nil); err == nil {
		t.Error("missing script: want error")
	}
}
