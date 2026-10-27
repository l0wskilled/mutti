package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/l0wskilled/mutti/internal/gitdir"
	"github.com/l0wskilled/mutti/internal/identity"
	"github.com/l0wskilled/mutti/internal/project"
)

// Error codes of the worktree commands.
const (
	CodeNotGit       = "not-git"
	CodeGitFailed    = "git-failed"
	CodeDirtyTree    = "dirty-worktree"
	CodeMainWorktree = "main-worktree"
)

// WorktreeAddOptions configure WorktreeAdd.
type WorktreeAddOptions struct {
	Name   string // name of the worktree; also the new branch, unless Branch is set
	Branch string // check out this existing branch instead of creating one
	From   string // base of the new branch; empty: the current HEAD
	Path   string // where to put it; empty: next to the main worktree as <repo>-<name>
	// DataFrom copies the volumes of another worktree's instance ("main", a worktree or instance
	// name, or a path) instead of bootstrapping; after-clone hooks run instead of bootstrap hooks.
	DataFrom string
	NoUp     bool // only create the worktree
	Up       UpOptions
}

// WorktreeResult describes a created worktree.
type WorktreeResult struct {
	Path   string
	Branch string
	Copied []string  // paths copied from the main worktree (x-mutti.worktree.copy)
	Cloned []string  // volumes copied from DataFrom
	Up     *UpResult // nil with NoUp
}

// WorktreeAdd creates a git worktree and starts its instance.
func (a *App) WorktreeAdd(ctx context.Context, dir string, opts WorktreeAddOptions) (*WorktreeResult, error) {
	info, err := a.repo(ctx, dir)
	if err != nil {
		return nil, err
	}
	if !identity.Valid(identity.Sanitize(opts.Name)) || identity.Sanitize(opts.Name) != opts.Name {
		return nil, &Error{Code: CodeInvalidName, Message: fmt.Sprintf("%q is not a valid worktree name", opts.Name), Hint: "use lower case letters, digits and '-'"}
	}
	path := opts.Path
	if path == "" {
		path = a.worktreePath(info, opts.Name)
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	res := &WorktreeResult{Path: path, Branch: opts.Branch}
	newBranch := ""
	if opts.Branch == "" {
		newBranch, res.Branch = opts.Name, opts.Name
	}
	if err := a.Git.AddWorktree(ctx, info.Root, path, opts.Branch, newBranch, opts.From); err != nil {
		return nil, &Error{Code: CodeGitFailed, Message: "cannot create the worktree", Err: err}
	}
	// Until the worktree is set up, a failure takes it back, so the same command can run again.
	undo := func(cause error) error {
		ctx := context.WithoutCancel(ctx)
		err := a.Git.RemoveWorktree(ctx, info.Root, path, true)
		if err == nil && newBranch != "" {
			err = a.Git.DeleteBranch(ctx, info.Root, newBranch)
		}
		if err != nil {
			return errors.Join(cause, fmt.Errorf("remove the half set up worktree %s: %w", path, err))
		}
		return cause
	}
	if inst, err := a.Resolve(ctx, info.MainRoot()); err == nil {
		res.Copied, err = copyInto(info.MainRoot(), path, inst.Config.Worktree.Copy)
		if err != nil {
			return nil, undo(fmt.Errorf("copy files into the new worktree: %w", err))
		}
	}
	if opts.DataFrom != "" {
		if res.Cloned, err = a.cloneInto(ctx, path, a.sourceRoot(ctx, dir, info, opts.DataFrom)); err != nil {
			return nil, undo(err)
		}
		opts.Up.afterClone = true
	}
	if opts.NoUp {
		return res, nil
	}
	up, err := a.Up(ctx, path, opts.Up)
	res.Up = up
	return res, err
}

// cloneInto claims the instance of the new worktree at path and fills its volumes from the
// instance of source. Its bootstrap counts as done: the data comes from a bootstrapped instance.
func (a *App) cloneInto(ctx context.Context, path, source string) (cloned []string, err error) {
	src, err := a.Resolve(ctx, source)
	if err != nil {
		return nil, err
	}
	dst, err := a.Resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := a.claim(ctx, dst); err != nil {
		return nil, err
	}
	// Half copied data is no instance anyone wants.
	defer func() {
		if err != nil {
			err = errors.Join(err, a.purge(context.WithoutCancel(ctx), dst.Name))
		}
	}()
	cloned, err = a.cloneVolumes(ctx, src, dst)
	if err != nil {
		return nil, err
	}
	if _, err := a.Engine.CreateVolume(ctx, bootstrapVolume(dst.Name), a.labels(dst, RoleBootstrap)); err != nil {
		return nil, engineErr(err)
	}
	return cloned, nil
}

// worktreePath is where a new worktree goes: next to the main worktree, named <repo>-<name>,
// the usual convention, which mutti shortens back to <name> for the instance.
func (a *App) worktreePath(info gitdir.Info, name string) string {
	main := info.MainRoot()
	return filepath.Join(filepath.Dir(main), filepath.Base(main)+"-"+name)
}

// WorktreeRemoveOptions configure WorktreeRemove.
type WorktreeRemoveOptions struct {
	Target string // name (as given to add) or path of the worktree
	Force  bool   // also drop uncommitted changes
}

// WorktreeRemove destroys a worktree's instance and removes the worktree. The branch stays.
// It refuses to touch anything while the worktree has uncommitted changes, unless forced, so no
// data is destroyed for a removal git would refuse anyway.
func (a *App) WorktreeRemove(ctx context.Context, dir string, opts WorktreeRemoveOptions) (string, error) {
	info, err := a.repo(ctx, dir)
	if err != nil {
		return "", err
	}
	path := opts.Target
	if !filepath.IsAbs(path) {
		if _, err := os.Stat(filepath.Join(dir, path)); err == nil {
			path = filepath.Join(dir, path)
		} else {
			path = a.worktreePath(info, opts.Target)
		}
	}
	target, inGit, err := a.Git.Find(ctx, path)
	if err != nil || !inGit || target.CommonDir != info.CommonDir || target.Root != filepath.Clean(path) {
		return "", &Error{Code: CodeNotGit, Message: fmt.Sprintf("%s is no worktree of this repository", path), Err: err}
	}
	if !target.Linked() {
		return "", &Error{Code: CodeMainWorktree, Message: "the main worktree cannot be removed"}
	}
	if !opts.Force {
		dirty, err := a.Git.Dirty(ctx, path)
		if err != nil {
			return "", &Error{Code: CodeGitFailed, Message: "cannot check the worktree for changes", Err: err}
		}
		if dirty {
			return "", &Error{Code: CodeDirtyTree, Message: fmt.Sprintf("%s has uncommitted changes", path), Hint: "commit or stash them, or pass --force to drop them"}
		}
	}
	if _, err := a.Destroy(ctx, path); err != nil && !errors.Is(err, project.ErrNoComposeFile) {
		return "", err
	}
	if err := a.Git.RemoveWorktree(ctx, info.Root, path, opts.Force); err != nil {
		return "", &Error{Code: CodeGitFailed, Message: "cannot remove the worktree", Err: err}
	}
	return path, nil
}

// repo finds the repository dir belongs to.
func (a *App) repo(ctx context.Context, dir string) (gitdir.Info, error) {
	info, ok, err := a.Git.Find(ctx, dir)
	if err != nil || !ok {
		return gitdir.Info{}, &Error{Code: CodeNotGit, Message: "not inside a git repository", Hint: "worktrees need git", Err: err}
	}
	return info, nil
}

// copyInto copies what patterns match in src to the same place in dst. It never overwrites:
// whatever already exists in dst, e.g. because git tracks it, stays.
func copyInto(src, dst string, patterns []string) ([]string, error) {
	var copied []string
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(src, pattern)) // patterns are validated
		for _, m := range matches {
			rel, _ := filepath.Rel(src, m) // m is below src
			target := filepath.Join(dst, rel)
			if _, err := os.Lstat(target); err == nil {
				continue
			}
			fi, err := os.Stat(m)
			if err != nil {
				return copied, err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // a directory of the user's worktree
				return copied, err
			}
			if fi.IsDir() {
				err = copyTree(m, target)
			} else {
				err = copyFile(m, target, fi.Mode().Perm())
			}
			if err != nil {
				return copied, err
			}
			copied = append(copied, rel)
		}
	}
	return copied, nil
}

// copyTree copies the directory src to dst with its files, directories and symlinks, which
// node_modules and vendor directories are full of. Both sides are opened as roots, so nothing
// read or written can lead outside them, whatever a symlink inside says.
func copyTree(src, dst string) error {
	from, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = from.Close() }()
	err = os.Mkdir(dst, 0o755) //nolint:gosec // a directory of the user's worktree
	var to *os.Root
	if err == nil {
		to, err = os.OpenRoot(dst)
	}
	if err != nil {
		return err
	}
	defer func() { _ = to.Close() }()
	return fs.WalkDir(from.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.Type()&fs.ModeSymlink != 0:
			link, err := from.Readlink(path)
			if err == nil {
				err = to.Symlink(link, path)
			}
			return err
		case d.IsDir():
			return to.MkdirAll(path, 0o755)
		default:
			info, err := d.Info()
			var b []byte
			if err == nil {
				b, err = from.ReadFile(path)
			}
			if err == nil {
				err = to.WriteFile(path, b, info.Mode().Perm())
			}
			return err
		}
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, perm) //nolint:gosec // dst lies in the new worktree, patterns are validated to stay inside
}
