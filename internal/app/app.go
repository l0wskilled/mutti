// Package app holds mutti's use cases (up, down, list, ...). It orchestrates the engine, Compose,
// git and the identity files, and returns typed results; it never prints anything but the output
// of the commands it runs for the user.
package app

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/gitdir"
	"github.com/l0wskilled/mutti/internal/home"
)

// Git is the part of the git binary mutti uses.
type Git interface {
	Find(ctx context.Context, dir string) (gitdir.Info, bool, error)
	Branch(ctx context.Context, dir string) (string, error)
	AddWorktree(ctx context.Context, dir, path, branch, newBranch, base string) error
	RemoveWorktree(ctx context.Context, dir, path string, force bool) error
	DeleteBranch(ctx context.Context, dir, branch string) error
	Dirty(ctx context.Context, dir string) (bool, error)
	Worktrees(ctx context.Context, commonDir string) ([]gitdir.Worktree, error)
}

// Host describes the machine mutti runs on.
type Host struct {
	UID, GID int
	Getenv   func(string) string
	LookPath func(string) (string, error)
	StdinTTY bool // stdin is a terminal, so exec may allocate a TTY
	IPv6     bool // the loopback interface has ::1
	// ContainerID is the container mutti runs in, empty on a plain host (hostenv.SelfContainer).
	ContainerID string
	// DockerGID is the group of the Docker socket, -1 when unknown (hostenv.SocketGID).
	DockerGID int
	// DockerSocket is the Docker socket's path on the Docker host (hostenv.DockerSocket).
	DockerSocket string
	// OpenURL opens a URL in the user's browser.
	OpenURL func(url string) error
	// PortFree reports whether a TCP port on 127.0.0.1 is free.
	PortFree func(port uint16) bool
	// SystemRoots returns the certificate authorities the system trusts.
	SystemRoots func() (*x509.CertPool, error)
}

// App wires mutti's dependencies.
type App struct {
	Engine  engine.Engine
	Compose compose.Runner
	Git     Git
	Dirs    home.Dirs
	Host    Host
	IO      compose.IO // streams of commands run on the user's behalf
	Domain  string     // base domain for hostnames, e.g. "localhost"
	Now     func() time.Time
	// Probe sends a GET to url with the Host header set to host and returns the status code.
	Probe func(ctx context.Context, url, host string) (int, error)
	// ReadyTimeout bounds how long up waits for the router; zero means 20 seconds.
	ReadyTimeout time.Duration
	Trusts       TrustStore
	// DialReaper connects to a run's reaper and registers filters; nil means reaper.Dial.
	DialReaper func(ctx context.Context, addr string, filters ...string) (io.Closer, error)
}

// Error is a failure the user can act on. Code is stable and shows up in JSON output.
type Error struct {
	Code    string
	Message string
	Hint    string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// Error codes.
const (
	CodeDockerUnavailable = "docker-unavailable"
	CodeComposeMissing    = "compose-missing"
	CodeComposeTooOld     = "compose-too-old"
	CodeComposeFailed     = "compose-failed"
	CodeNoComposeFile     = "no-compose-file"
	CodeNoSuchDir         = "no-such-dir"
	CodeInvalidConfig     = "invalid-config"
	CodeInvalidName       = "invalid-name"
	CodeNameTaken         = "name-taken"
	CodeCopiedWorktree    = "copied-worktree"
	CodeStaleClaim        = "stale-claim"
	CodeHookFailed        = "hook-failed"
	CodeUnknownOverlay    = "unknown-overlay"
	CodeInstanceInUse     = "instance-in-use"
	CodeRemoteDaemon      = "remote-daemon"
	CodePoolsExhausted    = "address-pools-exhausted"
	CodeOpenFailed        = "open-failed"
	CodeReaperFailed      = "reaper-failed"
	CodeTeardownFailed    = "teardown-failed"
	CodeTrustFailed       = "trust-failed"
	CodeCancelled         = "cancelled" // mutti got a signal
)

// poolsHint tells how to get more networks out of Docker.
const poolsHint = `remove unused networks (docker network prune), or let Docker hand out smaller subnets: ` +
	`put {"default-address-pools": [{"base": "172.17.0.0/12", "size": 24}]} into /etc/docker/daemon.json and restart Docker`

// engineErr turns engine failures into user-facing errors.
func engineErr(err error) error {
	if errors.Is(err, engine.ErrAddressPoolsExhausted) {
		return &Error{Code: CodePoolsExhausted, Message: "Docker has no subnet left for another network", Hint: poolsHint, Err: err}
	}
	if errors.Is(err, engine.ErrUnavailable) {
		return &Error{Code: CodeDockerUnavailable, Message: "cannot reach the Docker daemon", Hint: "start Docker, or check DOCKER_HOST / docker context", Err: err}
	}
	return err
}

// checkCompose makes sure the installed Compose is new enough.
func (a *App) checkCompose(ctx context.Context) error {
	v, err := a.Compose.Version(ctx)
	if errors.Is(err, compose.ErrNotInstalled) {
		return &Error{Code: CodeComposeMissing, Message: "docker compose is not installed", Hint: "install Docker with the Compose plugin", Err: err}
	}
	if err != nil {
		return err
	}
	if v.Less(compose.MinVersion()) {
		return &Error{
			Code:    CodeComposeTooOld,
			Message: fmt.Sprintf("docker compose %s is too old", v),
			Hint:    fmt.Sprintf("mutti needs %s or newer; update Docker", compose.MinVersion()),
		}
	}
	return nil
}
