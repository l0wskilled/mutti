//go:build e2e

package main

import (
	"crypto/rand"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"mutti": func() { os.Exit(run()) },
	})
}

// TestScripts runs the end-to-end scenarios in testdata/script against the real Docker daemon.
// Every scenario gets its own project name (MUTTI_TEST_RUN), so runs never collide and leftovers
// of a failed run are removed by label.
func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
		Setup: func(env *testscript.Env) error {
			run := "t" + strings.ToLower(rand.Text()[:8])
			env.Setenv("MUTTI_TEST_RUN", run)
			env.Setenv("XDG_STATE_HOME", env.WorkDir+"/.state")
			env.Setenv("XDG_DATA_HOME", env.WorkDir+"/.data")
			// A router of its own on free ports, so scenarios never touch a real one.
			env.Setenv("MUTTI_ROUTER_NAME", "mutti-router-"+run)
			env.Setenv("MUTTI_HTTP_PORT", freePort())
			env.Setenv("MUTTI_HTTPS_PORT", freePort())
			env.Setenv("HOME", env.WorkDir+"/.home")
			for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
				env.Setenv(k, "test")
			}
			for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
				env.Setenv(k, "test@example.com")
			}
			env.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
			env.Setenv("DOCKER_HOST", os.Getenv("DOCKER_HOST"))
			env.Defer(func() {
				_ = exec.Command("docker", "rm", "--force", "mutti-router-"+run).Run()
				purge(run)
			})
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"exitcode":   cmdExitcode,
			"eventually": cmdEventually,
			"example":    cmdExample,
		},
	})
}

func freePort() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// cmdEventually retries a command until it succeeds: eventually <seconds> <command> [args...]
func cmdEventually(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) < 2 {
		ts.Fatalf("usage: eventually <seconds> <command> [args...]")
	}
	secs, err := strconv.Atoi(args[0])
	if err != nil {
		ts.Fatalf("eventually: %v", err)
	}
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	for {
		err := ts.Exec(args[1], args[2:]...)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			ts.Fatalf("still failing after %ds: %v", secs, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// cmdExitcode runs a command and checks its exact exit code: exitcode <code> <cmd> [args...]
// cmdExample copies examples/<name> into $WORK/<run>-<name> as a git repository of its own, so
// the instance carries the test run's name and is cleaned up with it.
func cmdExample(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 1 {
		ts.Fatalf("usage: example <name>")
	}
	dst := ts.MkAbs(ts.Getenv("MUTTI_TEST_RUN") + "-" + args[0])
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("..", "..", "examples", args[0]))); err != nil {
		ts.Fatalf("copy example: %v", err)
	}
	for _, git := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		ts.Check(ts.Exec("git", append([]string{"-C", dst}, git...)...))
	}
}

func cmdExitcode(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) < 2 {
		ts.Fatalf("usage: exitcode <code> <command> [args...]")
	}
	err := ts.Exec(args[1], args[2:]...)
	got := "0"
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			ts.Fatalf("%v", err)
		}
		got = strings.TrimPrefix(exitErr.String(), "exit status ")
	}
	if got != args[0] {
		ts.Fatalf("exit code %s, want %s", got, args[0])
	}
}

// purge removes every container, network and volume of a scenario's projects, which all start
// with its run id.
func purge(run string) {
	for _, kind := range []string{"container", "network", "volume"} {
		id := "{{.ID}}"
		if kind == "volume" {
			id = "{{.Name}}"
		}
		ls := []string{kind, "ls", "--format", id + ` {{.Label "com.docker.compose.project"}}|{{.Label "mutti.project"}}|{{.Label "mutti.instance"}}`}
		if kind == "container" {
			ls = append(ls, "--all")
		}
		out, _ := exec.Command("docker", ls...).Output()
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			ref, labels, _ := strings.Cut(line, " ")
			if !slices.ContainsFunc(strings.Split(labels, "|"), func(l string) bool { return strings.HasPrefix(l, run) }) {
				continue
			}
			f := []string{ref}
			rm := []string{kind, "rm", f[0]}
			if kind == "container" {
				rm = []string{kind, "rm", "--force", "--volumes", f[0]}
			}
			_ = exec.Command("docker", rm...).Run()
		}
	}
}
