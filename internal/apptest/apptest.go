// Package apptest builds an app.App on fakes over a real git repository, for the tests of the
// packages that serve the app: command line, MCP, HTTP.
package apptest

import (
	"context"
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/compose/composetest"
	"github.com/l0wskilled/mutti/internal/engine/enginetest"
	"github.com/l0wskilled/mutti/internal/gitdir"
	"github.com/l0wskilled/mutti/internal/xdg"
)

// World is an app on fakes and the repository it works on.
type World struct {
	App     *app.App
	Engine  *enginetest.Fake
	Compose *composetest.Fake
	Root    string // the git repository "shop", with a web service publishing port 80
}

type trust struct{}

func (trust) Install(string) error   { return nil }
func (trust) Uninstall(string) error { return nil }

type closer struct{}

func (closer) Close() error { return nil }

// New returns a world with a repository "shop" holding one commit and a compose.yml.
func New(t *testing.T) *World {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "shop")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(gitdir.CleanEnv(os.Environ()), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	eng := enginetest.New()
	model := &compose.Model{Services: map[string]compose.Service{"web": {Ports: []compose.Port{{Target: 80}}}}}
	fc := composetest.New(eng, model)
	a := &app.App{
		Engine: eng, Compose: fc, Git: gitdir.Git{},
		Dirs: xdg.Dirs{State: filepath.Join(base, "state"), Data: filepath.Join(base, "data")},
		Host: app.Host{
			UID: 1000, GID: 1000, Getenv: func(string) string { return "" }, LookPath: exec.LookPath,
			SystemRoots: func() (*x509.CertPool, error) { return x509.NewCertPool(), nil }, DockerGID: -1, DockerSocket: "/var/run/docker.sock",
			OpenURL: func(string) error { return nil },
		},
		IO:         compose.IO{Out: io.Discard, Err: io.Discard},
		Domain:     "localhost",
		Now:        time.Now,
		Probe:      func(context.Context, string, string) (int, error) { return http.StatusOK, nil },
		Trusts:     trust{},
		DialReaper: func(context.Context, string, ...string) (io.Closer, error) { return closer{}, nil },
	}
	return &World{App: a, Engine: eng, Compose: fc, Root: root}
}
