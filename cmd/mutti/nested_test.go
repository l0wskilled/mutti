//go:build e2e

package main

import (
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// nestedImage is a docker CLI image with git, like a CI step or a tool container.
const nestedImage = "mutti-test-nested:latest"

const nestedDockerfile = `FROM docker:29-cli
RUN apk add --no-cache git
`

func mustRun(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// TestNested runs mutti inside a container that sees the worktree under another path than the
// host does: bind mounts must be translated, the router must get its config without host paths,
// and attach must give the container the instance's network.
func TestNested(t *testing.T) {
	work := t.TempDir()
	bin := filepath.Join(work, "mutti")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	ctxDir := filepath.Join(work, "image")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte(nestedDockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "docker", "build", "-q", "-t", nestedImage, ctxDir)

	runID := "n" + strings.ToLower(rand.Text()[:8])
	httpPort, httpsPort := freePort(), freePort()
	router := "mutti-router-" + runID
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", router).Run()
		purge(runID)
	})

	repo := filepath.Join(work, "ws", "shop")
	if err := os.MkdirAll(filepath.Join(repo, "www"), 0o755); err != nil {
		t.Fatal(err)
	}
	compose := `x-mutti:
  name: ` + runID + `
services:
  web:
    image: busybox:1.37
    command: ["httpd", "-f", "-p", "80", "-h", "/www"]
    init: true
    ports: ["8080:80"]
    volumes: ["./www:/www"]
`
	for path, content := range map[string]string{"compose.yml": compose, "www/index.html": "served from the host path\n"} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	script := `set -e
trap 'chown -R "$HOST_OWNER" /workspace' EXIT
git config --global --add safe.directory '*'
cd /workspace/shop
git init -q -b main
mutti up
mutti attach
wget -qO- http://web/index.html
mutti doctor | grep -q "mutti runs in container"
mutti destroy --yes
`
	out := mustRun(t, "docker", "run", "--rm",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", filepath.Join(work, "ws")+":/workspace",
		"-v", bin+":/usr/local/bin/mutti:ro",
		"-e", "HOST_OWNER="+strconv.Itoa(os.Getuid())+":"+strconv.Itoa(os.Getgid()),
		"-e", "MUTTI_ROUTER_NAME="+router, "-e", "MUTTI_HTTP_PORT="+httpPort, "-e", "MUTTI_HTTPS_PORT="+httpsPort,
		nestedImage, "sh", "-c", script+"\n")
	if !strings.Contains(out, "is up: https://") || !strings.Contains(out, "served from the host path") || !strings.Contains(out, "is gone") {
		t.Fatalf("inside the container:\n%s", out)
	}
}
