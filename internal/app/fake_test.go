package app

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/compose/composetest"
	"github.com/l0wskilled/mutti/internal/engine/enginetest"
	"github.com/l0wskilled/mutti/internal/gitdir"
	"github.com/l0wskilled/mutti/internal/home"
	"github.com/l0wskilled/mutti/internal/project"
)

// env is a fixed environment for tests.
type env map[string]string

func (e env) get(k string) string { return e[k] }

// harness is an App wired to fakes, with a temp state dir.
type harness struct {
	app     *App
	eng     *enginetest.Fake
	compose *composetest.Fake
	stderr  *strings.Builder
}

func newHarness(t *testing.T, model *compose.Model) *harness {
	t.Helper()
	eng := enginetest.New()
	fc := composetest.New(eng, model)
	var stderr strings.Builder
	h := &harness{eng: eng, compose: fc, stderr: &stderr}
	h.app = &App{
		Engine:  eng,
		Compose: fc,
		Git:     gitdir.Git{},
		Dirs:    home.Dirs{State: filepath.Join(t.TempDir(), "state"), Data: filepath.Join(t.TempDir(), "data")},
		Host:    Host{UID: 1000, GID: 1001, Getenv: env{}.get, LookPath: exec.LookPath, SystemRoots: func() (*x509.CertPool, error) { return x509.NewCertPool(), nil }, PortFree: func(uint16) bool { return true }, DockerGID: -1, DockerSocket: "/var/run/docker.sock"},
		IO:      compose.IO{Err: &stderr},
		Domain:  "localhost",
		Now:     func() time.Time { return testNow },
		Probe:   func(context.Context, string, string) (int, error) { return http.StatusOK, nil },
		Trusts:  &fakeTrust{},
	}
	return h
}

var testNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// webModel is a project with a web service publishing port 80 and a worker without ports.
func webModel() *compose.Model {
	return &compose.Model{Services: map[string]compose.Service{
		"web":    {Ports: []compose.Port{{Target: 80, Published: "8080", Protocol: "tcp"}}},
		"worker": {},
	}}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitdir.CleanEnv(os.Environ()),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newRepo creates <tmp>/<name> as a git repo with a compose.yml and one commit.
func newRepo(t *testing.T, name string) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newRepoIn(t, base, name)
}

func newRepoIn(t *testing.T, base, name string) string {
	t.Helper()
	root := filepath.Join(base, name)
	writeFile(t, filepath.Join(root, "compose.yml"), "services: {}\n")
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "init")
	return root
}

// addWorktree adds a linked worktree next to root.
func addWorktree(t *testing.T, root, name string) string {
	t.Helper()
	wt := filepath.Join(filepath.Dir(root), name)
	runGit(t, root, "worktree", "add", "-q", "-b", name, wt)
	return wt
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appErrCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return fmt.Sprintf("not an app error: %v", err)
}

func claimLabels(t *testing.T, eng *enginetest.Fake, instance string) map[string]string {
	t.Helper()
	v, ok, err := eng.InspectVolume(context.Background(), claimVolume(instance))
	if err != nil || !ok {
		t.Fatalf("claim of %s: ok=%v err=%v", instance, ok, err)
	}
	return maps.Clone(v.Labels)
}

func projectFiles(dir string, paths ...string) project.Files {
	return project.Files{Dir: dir, Paths: paths}
}
