// Package compose runs the user's installed `docker compose` CLI. mutti never reimplements
// Compose; it decides which files, project name and environment Compose gets.
package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Spec selects the Compose project a command runs against.
type Spec struct {
	Project  string   // Compose project name (= instance name)
	Dir      string   // project directory
	Files    []string // Compose files in merge order
	Profiles []string // profiles to enable; "*" enables all
	Env      []string // extra environment, e.g. MUTTI_* interpolation variables
}

// IO are the streams a command is attached to.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Runner runs Compose commands. CLI is the real implementation.
type Runner interface {
	Version(ctx context.Context) (Version, error)
	Config(ctx context.Context, s Spec) (*Model, error)
	// Run runs `docker compose <global flags> <args>` and returns its exit code. err is only set
	// when Compose could not be started at all.
	Run(ctx context.Context, s Spec, streams IO, args ...string) (exitCode int, err error)
	// BuildxVersion returns the version line of the buildx plugin Compose builds with.
	BuildxVersion(ctx context.Context) (string, error)
}

// CancelGrace is how long Compose may take to stop after a cancel before it is killed.
var CancelGrace = 30 * time.Second

// ErrNotInstalled means docker or its Compose plugin is missing.
var ErrNotInstalled = errors.New("docker compose is not installed")

// CLI runs the docker binary's compose plugin.
type CLI struct {
	Docker  string   // docker binary; empty means "docker" from PATH
	Environ []string // base environment; nil means the current process environment
}

// ownedEnv are variables mutti sets through flags. Inherited from the shell they would silently
// change which files or project Compose uses.
var ownedEnv = map[string]bool{"COMPOSE_FILE": true, "COMPOSE_PROJECT_NAME": true, "COMPOSE_PROFILES": true}

func (c CLI) command(ctx context.Context, s *Spec, args []string) (*exec.Cmd, error) {
	bin := c.Docker
	if bin == "" {
		bin = "docker"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil, ErrNotInstalled
	}
	argv := []string{"compose"}
	if s != nil {
		argv = append(argv, "--project-name", s.Project, "--project-directory", s.Dir)
		for _, f := range s.Files {
			argv = append(argv, "--file", f)
		}
		for _, p := range s.Profiles {
			argv = append(argv, "--profile", p)
		}
	}
	cmd := exec.CommandContext(ctx, bin, append(argv, args...)...)
	// On cancel, let Compose shut down cleanly (it stops what it started) instead of killing it.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = CancelGrace
	base := c.Environ
	if base == nil {
		base = cmd.Environ()
	}
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); !ownedEnv[k] {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if s != nil {
		cmd.Env = append(cmd.Env, s.Env...)
		cmd.Dir = s.Dir
	}
	return cmd, nil
}

// output runs a command and returns stdout, with stderr folded into the error.
func (c CLI) output(ctx context.Context, s *Spec, args ...string) ([]byte, error) {
	cmd, err := c.command(ctx, s, args)
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker compose %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Version returns the installed Compose version.
func (c CLI) Version(ctx context.Context) (Version, error) {
	out, err := c.output(ctx, nil, "version", "--short")
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			return Version{}, err
		}
		return Version{}, fmt.Errorf("%w: %w", ErrNotInstalled, err)
	}
	return ParseVersion(string(out))
}

// BuildxVersion returns `docker buildx version`.
func (c CLI) BuildxVersion(ctx context.Context) (string, error) {
	cmd, err := c.command(ctx, nil, nil)
	if err != nil {
		return "", err
	}
	cmd.Args = []string{cmd.Args[0], "buildx", "version"}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker buildx version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Config returns the fully resolved model of a project with all profiles enabled, so every
// service is known no matter which profiles a later command uses.
func (c CLI) Config(ctx context.Context, s Spec) (*Model, error) {
	s.Profiles = []string{"*"}
	out, err := c.output(ctx, &s, "config", "--format", "json")
	if err != nil {
		return nil, err
	}
	var m Model
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("parse docker compose config: %w", err)
	}
	return &m, nil
}

// Run runs a Compose command attached to streams.
func (c CLI) Run(ctx context.Context, s Spec, streams IO, args ...string) (int, error) {
	cmd, err := c.command(ctx, &s, args)
	if err != nil {
		return 0, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = streams.In, streams.Out, streams.Err
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 0, fmt.Errorf("run docker compose: %w", err)
	}
	return 0, nil
}

var _ Runner = CLI{}
