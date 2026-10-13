package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/gitdir"
	"github.com/l0wskilled/mutti/internal/hostenv"
	"github.com/l0wskilled/mutti/internal/identity"
	"github.com/l0wskilled/mutti/internal/project"
)

// Instance is the environment of one worktree, as resolved from disk. Nothing on the daemon is
// touched while resolving.
type Instance struct {
	Name         string // instance name = Compose project name
	Project      string
	Worktree     string
	ID           string // empty until the instance was claimed once
	Root         string // worktree root
	HostPath     string // Root as the Docker host sees it; differs when mutti runs in a container
	GitDir       string // per-worktree git admin dir, empty outside git
	CommonDir    string // the repository's shared git dir, empty outside git
	IdentityPath string
	Hostname     string
	Files        project.Files
	Model        *compose.Model
	Config       project.Config

	paths    *hostenv.PathMap // nil when mutti runs on the Docker host
	runID    string           // set for the throwaway instance of a run
	extraEnv []string         // added to Env, e.g. for one kind of hook
}

// Resolve finds the instance for the worktree containing dir, which must be absolute. Inside the
// command of a mutti run (MUTTI_RUN_ID and MUTTI_INSTANCE set by it), that is the run's instance,
// so the command can work on it with mutti exec, mutti compose and the like.
func (a *App) Resolve(ctx context.Context, dir string) (*Instance, error) {
	inst, err := a.resolveWorktree(ctx, dir)
	if err != nil {
		return nil, err
	}
	if id := a.Host.Getenv("MUTTI_RUN_ID"); id != "" {
		if run := runInstance(inst, id); run.Name == a.Host.Getenv("MUTTI_INSTANCE") {
			return run, nil
		}
	}
	return inst, nil
}

// resolveWorktree is Resolve without the run's instance.
func (a *App) resolveWorktree(ctx context.Context, dir string) (*Instance, error) {
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, &Error{Code: CodeNoSuchDir, Message: dir + " is not a directory", Err: err}
	}
	info, inGit, err := a.Git.Find(ctx, dir)
	if err != nil && !errors.Is(err, gitdir.ErrNoGit) {
		return nil, err
	}

	stop := ""
	if inGit {
		stop = info.Root
	}
	files, err := project.Discover(dir, stop, a.Host.Getenv)
	if err != nil {
		return nil, &Error{Code: CodeNoComposeFile, Message: "no Compose file here", Hint: "run mutti in a directory with a compose.yaml, or set COMPOSE_FILE", Err: err}
	}

	inst := &Instance{Files: files, Root: files.Dir}
	defaultProject := filepath.Base(files.Dir)
	if inGit {
		inst.Root, inst.GitDir, inst.CommonDir = info.Root, info.GitDir, info.CommonDir
		defaultProject = info.MainName()
	}
	inst.IdentityPath = identity.FilePath(inst.GitDir, inst.Root)
	if err := a.hostPaths(ctx, inst); err != nil {
		return nil, err
	}

	inst.Project = identity.Shorten(identity.Sanitize(defaultProject))
	unnamed := inst.Project == ""
	if unnamed {
		inst.Project = "project" // only to read x-mutti.name
	}
	id, known, err := identity.Load(inst.IdentityPath)
	if err != nil {
		return nil, err
	}
	// The main checkout is the project itself; only linked worktrees get a name of their own.
	linked := inGit && info.Linked()
	switch {
	case known:
		inst.Worktree, inst.ID = id.Name, id.ID
	case linked:
		inst.Worktree = defaultWorktree(filepath.Base(inst.Root), inst.Project)
	default:
		inst.Worktree = inst.Project
	}

	if err := a.loadModel(ctx, inst); err != nil {
		return nil, err
	}
	switch {
	case inst.Config.Name != "" && inst.Config.Name != inst.Project:
		// The project name is part of the interpolation env; load again with the right one.
		if !known && !linked {
			inst.Worktree = inst.Config.Name
		}
		inst.Project = inst.Config.Name
		if err := a.loadModel(ctx, inst); err != nil {
			return nil, err
		}
	case unnamed && inst.Config.Name == "":
		return nil, &Error{Code: CodeInvalidName, Message: fmt.Sprintf("cannot derive a project name from %q", defaultProject), Hint: "set x-mutti.name in the Compose file"}
	}
	return inst, nil
}

// lastUsedFile is touched whenever a command works on an instance; tidy --idle reads it.
func (a *App) lastUsedFile(instance string) string {
	return filepath.Join(a.Dirs.InstanceDir(instance), "last-used")
}

// touch records that the instance is in use. It is a hint only, so failures are ignored.
func (a *App) touch(instance string) {
	path := a.lastUsedFile(instance)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, nil, 0o600)
	now := a.Now()
	_ = os.Chtimes(path, now, now)
}

// defaultWorktree derives a worktree name from its directory. Worktrees are often named
// "<repo>-<branch>"; the repo part is dropped because the project name already says it. A
// directory name without usable characters falls back to the project name.
func defaultWorktree(dirName, project string) string {
	name := identity.Sanitize(dirName)
	if short := strings.TrimPrefix(name, project+"-"); short != "" {
		return identity.Shorten(short)
	}
	return project
}

func (a *App) loadModel(ctx context.Context, inst *Instance) error {
	inst.Name = identity.InstanceName(inst.Project, inst.Worktree)
	inst.Hostname = a.hostname(inst)
	m, err := a.Compose.Config(ctx, a.userSpec(inst, nil, nil))
	if err != nil {
		return &Error{Code: CodeInvalidConfig, Message: "docker compose cannot read the project", Err: err}
	}
	cfg, err := project.ParseConfig(m)
	if err != nil {
		return &Error{Code: CodeInvalidConfig, Message: "invalid x-mutti configuration", Err: err}
	}
	inst.Model, inst.Config = m, cfg
	return nil
}

func (a *App) hostname(inst *Instance) string {
	if inst.Worktree == inst.Project {
		return inst.Project + "." + a.Domain
	}
	return inst.Worktree + "." + inst.Project + "." + a.Domain
}

// Env returns the MUTTI_* variables available to Compose interpolation, hooks and exec.
// MUTTI_DOCKER_GID is only set when the socket's group is known; use ${MUTTI_DOCKER_GID:-...}.
// MUTTI_DOCKER_SOCKET is the Docker socket's host path, to mount where a container needs Docker.
// MUTTI_GIT_COMMON_DIR, the repository's shared git directory, is set inside git: a linked
// worktree's .git points there, so a container running git mounts it at the same path.
func (a *App) Env(inst *Instance) []string {
	env := []string{
		"MUTTI_INSTANCE=" + inst.Name,
		"MUTTI_PROJECT=" + inst.Project,
		"MUTTI_WORKTREE=" + inst.Worktree,
		"MUTTI_HOST_PATH=" + inst.HostPath,
		"MUTTI_HOSTNAME=" + inst.Hostname,
		"MUTTI_UID=" + strconv.Itoa(a.Host.UID),
		"MUTTI_GID=" + strconv.Itoa(a.Host.GID),
	}
	if a.Host.DockerSocket != "" {
		env = append(env, "MUTTI_DOCKER_SOCKET="+a.Host.DockerSocket)
	}
	if a.Host.DockerGID >= 0 {
		env = append(env, "MUTTI_DOCKER_GID="+strconv.Itoa(a.Host.DockerGID))
	}
	if inst.CommonDir != "" {
		env = append(env, "MUTTI_GIT_COMMON_DIR="+inst.CommonDir)
	}
	if inst.runID != "" {
		env = append(env, "MUTTI_RUN_ID="+inst.runID)
	}
	return append(env, inst.extraEnv...)
}

// userSpec selects the user's files plus the named x-mutti overlays, without mutti's override.
func (a *App) userSpec(inst *Instance, overlays, profiles []string) compose.Spec {
	files := append([]string(nil), inst.Files.Paths...)
	for _, o := range overlays {
		for _, f := range inst.Config.Overlays[o] {
			if !filepath.IsAbs(f) {
				f = filepath.Join(inst.Files.Dir, f)
			}
			files = append(files, f)
		}
	}
	return compose.Spec{Project: inst.Name, Dir: inst.Files.Dir, Files: files, Profiles: profiles, Env: a.Env(inst)}
}

func (a *App) checkOverlays(inst *Instance, overlays []string) error {
	for _, o := range overlays {
		if _, ok := inst.Config.Overlays[o]; !ok {
			return &Error{Code: CodeUnknownOverlay, Message: fmt.Sprintf("unknown overlay %q", o), Hint: "define it under x-mutti.overlays"}
		}
	}
	return nil
}
