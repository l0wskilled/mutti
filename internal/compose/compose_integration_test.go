//go:build integration

package compose

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixedPortProject = `services:
  web:
    image: busybox:1.37
    command: ["httpd", "-f", "-p", "80"]
    ports: ["${MUTTI_TEST_PORT:?}:80"]
x-mutti:
  main: web
`

// TestOverrideLetsTwoProjectsShareAFixedPort is the reason the override exists: two copies of a
// project that hard-codes host port 18080 run side by side.
func TestOverrideLetsTwoProjectsShareAFixedPort(t *testing.T) {
	ctx := context.Background()
	cli := CLI{}
	if v, err := cli.Version(ctx); err != nil || v.Less(MinVersion()) {
		t.Fatalf("compose %v (%v), need %v", v, err, MinVersion())
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(fixedPortProject), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"MUTTI_TEST_PORT=18080"}

	for _, name := range []string{"mutti-it-a", "mutti-it-b"} {
		spec := Spec{Project: name, Dir: dir, Files: []string{"compose.yml"}, Env: env}
		m, err := cli.Config(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(m.XMutti), `"main"`) {
			t.Fatalf("x-mutti missing from model: %s", m.XMutti)
		}
		override := filepath.Join(t.TempDir(), "override.yml")
		if err := os.WriteFile(override, RenderOverride(m, Override{}), 0o600); err != nil {
			t.Fatal(err)
		}
		spec.Files = append(spec.Files, override)
		t.Cleanup(func() { _, _ = cli.Run(ctx, spec, IO{Out: io.Discard, Err: io.Discard}, "down", "-v") })

		var stderr strings.Builder
		code, err := cli.Run(ctx, spec, IO{Out: io.Discard, Err: &stderr}, "up", "-d", "--wait")
		if err != nil || code != 0 {
			t.Fatalf("up %s: code=%d err=%v\n%s", name, code, err, stderr.String())
		}
		code, _ = cli.Run(ctx, spec, IO{Out: io.Discard, Err: io.Discard}, "exec", "-T", "web", "sh", "-c", "exit 7")
		if code != 7 {
			t.Fatalf("exec exit code = %d, want 7", code)
		}
	}
}
