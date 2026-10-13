// Package gitdir asks the installed git binary where a worktree and its admin directories are.
package gitdir

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Info describes the worktree containing a path.
type Info struct {
	Root      string // worktree root
	GitDir    string // per-worktree admin dir: <root>/.git or <common>/worktrees/<id>
	CommonDir string // shared admin dir of the repository
}

// Linked reports whether the worktree was added with `git worktree add`.
func (i Info) Linked() bool { return i.GitDir != i.CommonDir }

// MainName is the directory name of the main worktree, used as the default project name.
// For a bare repository it is the repository directory without a ".git" suffix.
func (i Info) MainName() string {
	if filepath.Base(i.CommonDir) == ".git" {
		return filepath.Base(filepath.Dir(i.CommonDir))
	}
	return strings.TrimSuffix(filepath.Base(i.CommonDir), ".git")
}

// Git runs the git binary.
type Git struct {
	Path string // git binary; empty means "git" from PATH
}

// repoLocalEnv are the variables `git rev-parse --local-env-vars` lists. git sets them in hooks;
// inherited, they would point mutti's git calls at the wrong repository or index.
var repoLocalEnv = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_CONFIG": true, "GIT_CONFIG_PARAMETERS": true,
	"GIT_CONFIG_COUNT": true, "GIT_OBJECT_DIRECTORY": true, "GIT_DIR": true, "GIT_WORK_TREE": true,
	"GIT_IMPLICIT_WORK_TREE": true, "GIT_GRAFT_FILE": true, "GIT_INDEX_FILE": true,
	"GIT_NO_REPLACE_OBJECTS": true, "GIT_REPLACE_REF_BASE": true, "GIT_PREFIX": true,
	"GIT_SHALLOW_FILE": true, "GIT_COMMON_DIR": true,
}

// CleanEnv drops repository-local git variables from env and pins output to the C locale, so
// git's answer depends only on the directory it runs in.
func CleanEnv(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if repoLocalEnv[k] || k == "LC_ALL" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
}

// ErrNoGit means the git binary is not installed.
var ErrNoGit = errors.New("git is not installed")

// Find returns the worktree containing dir. ok is false when dir is not inside a worktree.
func (g Git) Find(ctx context.Context, dir string) (info Info, ok bool, err error) {
	out, err := g.run(ctx, dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && bytes.Contains(exitErr.Stderr, []byte("not a git repository")) {
			return Info{}, false, nil
		}
		return Info{}, false, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		return Info{}, false, fmt.Errorf("git rev-parse in %s: unexpected output %q", dir, out)
	}
	return Info{Root: lines[0], GitDir: lines[1], CommonDir: lines[2]}, true, nil
}

// Worktree is one entry of `git worktree list`.
type Worktree struct {
	Path     string
	Prunable bool // git knows the worktree directory is gone
}

// Worktrees lists the worktrees registered in a repository, identified by its common dir.
func (g Git) Worktrees(ctx context.Context, commonDir string) ([]Worktree, error) {
	out, err := g.run(ctx, "", "--git-dir="+commonDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []Worktree
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			list = append(list, Worktree{Path: p})
			continue
		}
		if strings.HasPrefix(line, "prunable") && len(list) > 0 {
			list[len(list)-1].Prunable = true
		}
	}
	return list, sc.Err()
}

// MainRoot is the directory of the main worktree, or of a bare repository.
func (i Info) MainRoot() string { return filepath.Dir(i.CommonDir) }

// AddWorktree adds a worktree at path. With newBranch it creates that branch from base (empty:
// the current HEAD); otherwise it checks out branch, which git also finds on a remote.
func (g Git) AddWorktree(ctx context.Context, dir, path, branch, newBranch, base string) error {
	args := []string{"worktree", "add", "--quiet"}
	if newBranch != "" {
		args = append(args, "-b", newBranch, path)
		if base != "" {
			args = append(args, base)
		}
	} else {
		args = append(args, path, branch)
	}
	_, err := g.run(ctx, dir, args...)
	return err
}

// RemoveWorktree removes the worktree at path; force also drops uncommitted changes.
func (g Git) RemoveWorktree(ctx context.Context, dir, path string, force bool) error {
	args := []string{"worktree", "remove", path}
	if force {
		args = append(args, "--force")
	}
	_, err := g.run(ctx, dir, args...)
	return err
}

// DeleteBranch deletes a local branch, merged or not.
func (g Git) DeleteBranch(ctx context.Context, dir, branch string) error {
	_, err := g.run(ctx, dir, "branch", "-D", branch)
	return err
}

// Dirty reports whether the worktree at dir has uncommitted changes or untracked files.
func (g Git) Dirty(ctx context.Context, dir string) (bool, error) {
	out, err := g.run(ctx, dir, "status", "--porcelain")
	return strings.TrimSpace(out) != "", err
}

// Branch returns the checked out branch of the worktree at dir, or "" for a detached HEAD.
func (g Git) Branch(ctx context.Context, dir string) (string, error) {
	out, err := g.run(ctx, dir, "branch", "--show-current")
	return strings.TrimSpace(out), err
}

func (g Git) run(ctx context.Context, dir string, args ...string) (string, error) {
	bin := g.Path
	if bin == "" {
		bin = "git"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", ErrNoGit
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = CleanEnv(cmd.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitErr.Stderr = stderr.Bytes()
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// WorktreeRoot returns where the worktree of a per-worktree admin dir lives now. git keeps this
// current on `git worktree move`, so it is the reliable way back from an admin dir to its
// worktree. ok is false when the admin dir is gone (worktree removed and pruned).
func WorktreeRoot(gitDir string) (root string, ok bool, err error) {
	if filepath.Base(gitDir) == ".git" {
		if _, err := os.Stat(gitDir); err != nil {
			return "", false, ignoreNotExist(err)
		}
		return filepath.Dir(gitDir), true, nil
	}
	b, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return "", false, ignoreNotExist(err)
	}
	dotGit := strings.TrimSpace(string(b))
	if !filepath.IsAbs(dotGit) {
		dotGit = filepath.Join(gitDir, dotGit)
	}
	return filepath.Dir(dotGit), true, nil
}

func ignoreNotExist(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
