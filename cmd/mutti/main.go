// Command mutti runs parallel, isolated Docker Compose environments per git worktree.
package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/smallstep/truststore"
	"golang.org/x/term"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/cli"
	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/gitdir"
	"github.com/l0wskilled/mutti/internal/home"
	"github.com/l0wskilled/mutti/internal/hostenv"
)

// Set by the release build (.goreleaser.yaml).
var (
	version = "dev"
	commit  = "none"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal winds mutti down, a second one ends it at once; a run's reaper removes
	// what is left.
	go func() {
		<-ctx.Done()
		stop()
	}()

	dirs, err := home.FromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mutti:", err)
		return cli.ExitError
	}
	if os.Getenv("MUTTI_HOME") == "" {
		moved, err := home.Migrate(os.Getenv, dirs.State)
		for _, old := range moved {
			fmt.Fprintf(os.Stderr, "mutti: moved %s to %s\n", old, dirs.State)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "mutti: could not move older files to %s: %v\n", dirs.State, err)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mutti:", err)
		return cli.ExitError
	}
	domain, err := app.DomainFromEnv(os.Getenv)
	if err != nil {
		var e *app.Error
		if errors.As(err, &e) {
			fmt.Fprintf(os.Stderr, "mutti: %v\nhint: %s\n", e, e.Hint)
		}
		return cli.ExitError
	}
	eng, err := engine.Connect(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mutti:", err)
		return cli.ExitError
	}
	defer eng.Close()
	dockerGID := -1
	if gid, ok := hostenv.SocketGID(eng.DaemonHost()); ok {
		dockerGID = gid
	}

	a := &app.App{
		Engine:  eng,
		Compose: compose.CLI{},
		Git:     gitdir.Git{},
		Dirs:    dirs,
		Host: app.Host{
			UID:          os.Getuid(),
			GID:          os.Getgid(),
			Getenv:       os.Getenv,
			LookPath:     exec.LookPath,
			StdinTTY:     term.IsTerminal(int(os.Stdin.Fd())),
			IPv6:         hasIPv6Loopback(),
			SystemRoots:  x509.SystemCertPool,
			PortFree:     portFree,
			OpenURL:      openURL,
			ContainerID:  containerID(),
			DockerGID:    dockerGID,
			DockerSocket: hostenv.DockerSocket(os.Getenv, eng.DaemonHost()),
		},
		IO:     compose.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr},
		Domain: domain,
		Now:    time.Now,
		Probe:  probe,
		Trusts: trustStore{},
	}
	return cli.Run(ctx, cli.Deps{App: a, Dir: cwd, In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Version: version + " (" + commit + ")", OutTTY: term.IsTerminal(int(os.Stdout.Fd()))}, os.Args[1:])
}

// hasIPv6Loopback reports whether ::1 exists, so the router can listen there too.
func hasIPv6Loopback() bool {
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

var probeClient = &http.Client{Timeout: 2 * time.Second}

// probe sends a GET with an explicit Host header, so it works without name resolution.
func probe(ctx context.Context, url, host string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, err
	}
	req.Host = host
	res, err := probeClient.Do(req)
	if err != nil {
		return 0, err
	}
	_ = res.Body.Close()
	return res.StatusCode, nil
}

// trustStore changes the system and NSS (Firefox, Chromium) trust stores.
type trustStore struct{}

func (trustStore) Install(certFile string) error {
	return truststore.InstallFile(certFile, truststore.WithFirefox())
}

func (trustStore) Uninstall(certFile string) error {
	return truststore.UninstallFile(certFile, truststore.WithFirefox())
}

func portFree(port uint16) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// openURL hands a URL to the desktop's default browser.
func openURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func containerID() string {
	id, _ := hostenv.SelfContainer(os.ReadFile)
	return id
}
