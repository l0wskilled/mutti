package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/gitdir"
	"github.com/l0wskilled/mutti/internal/identity"
	"github.com/l0wskilled/mutti/internal/lock"
)

// Volume names of mutti's own per-instance markers.
func claimVolume(instance string) string     { return "mutti-claim-" + instance }
func bootstrapVolume(instance string) string { return "mutti-bootstrapped-" + instance }

// Roles of mutti's own volumes (engine.LabelRole).
const (
	RoleClaim     = "claim"
	RoleBootstrap = "bootstrap"
)

// LabelOrigin says where the paths in a resource's labels are valid (see origin).
const LabelOrigin = "mutti.origin"

func (a *App) labels(inst *Instance, role string) map[string]string {
	return map[string]string{
		LabelOrigin:          a.origin(),
		engine.LabelSchema:   engine.SchemaVersion,
		engine.LabelRole:     role,
		engine.LabelInstance: inst.Name,
		engine.LabelProject:  inst.Project,
		engine.LabelID:       inst.ID,
		engine.LabelPath:     inst.Root,
		engine.LabelGitDir:   inst.GitDir,
	}
}

// claim makes the instance name this worktree's. The claim is a labelled volume, so ownership
// lives in Docker and is visible before any container exists. It runs under a host-wide lock;
// volume creation is idempotent, so a second creator reads back the first one's labels.
func (a *App) claim(ctx context.Context, inst *Instance) error {
	release, err := lock.Acquire(ctx, a.Dirs.LockFile("claim"))
	if err != nil {
		return err
	}
	defer func() { _ = release() }()

	// Another mutti may have claimed for this worktree while this one waited for the lock.
	if inst.ID == "" {
		if id, ok, err := identity.Load(inst.IdentityPath); err == nil && ok && id.Name == inst.Worktree {
			inst.ID = id.ID
		}
	}
	// A new identity is only written once the claim is ours, so a refused name leaves no trace.
	fresh := inst.ID == ""
	if fresh {
		inst.ID = identity.NewID()
	}
	vol, err := a.Engine.CreateVolume(ctx, claimVolume(inst.Name), a.labels(inst, RoleClaim))
	if err != nil {
		return engineErr(err)
	}
	// Right after it started, Docker may answer for an existing volume without its labels.
	if vol.Labels[engine.LabelRole] == "" {
		if vol, _, err = a.Engine.InspectVolume(ctx, vol.Name); err != nil {
			return engineErr(err)
		}
	}
	if vol.Labels[engine.LabelID] == inst.ID {
		// Moved, or claimed before claims said where their paths are valid: write it anew.
		if vol.Labels[engine.LabelPath] != inst.Root || vol.Labels[LabelOrigin] == "" {
			if err := a.refreshClaim(ctx, inst, vol); err != nil {
				return err
			}
		}
		if fresh {
			if err := identity.Save(inst.IdentityPath, identity.Identity{Name: inst.Worktree, ID: inst.ID}); err != nil {
				// Without the identity, the claim would hold the name against this very worktree.
				_ = a.Engine.RemoveVolume(ctx, vol.Name)
				return fmt.Errorf("save identity: %w", err)
			}
		}
		return nil
	}

	switch root, owner := a.ownerRoot(ctx, vol); owner {
	case ownerAlive:
		return &Error{
			Code:    CodeNameTaken,
			Message: fmt.Sprintf("the name %q already belongs to %s", inst.Name, root),
			Hint:    "give this worktree its own name: mutti rename <name>",
		}
	case ownerUnknown:
		return &Error{
			Code:    CodeNameTaken,
			Message: fmt.Sprintf("the name %q is claimed by %s, which mutti cannot check from here", inst.Name, root),
			Hint:    "give this worktree its own name: mutti rename <name>",
		}
	}
	return &Error{
		Code:    CodeStaleClaim,
		Message: fmt.Sprintf("the name %q is still claimed by %s, which no longer exists", inst.Name, vol.Labels[engine.LabelPath]),
		Hint:    "mutti tidy --yes removes leftovers of deleted worktrees",
	}
}

// refreshClaim handles a claim with our id but another path: either this worktree moved (update
// the labels) or it was copied, and the original still uses the id (refuse).
func (a *App) refreshClaim(ctx context.Context, inst *Instance, vol engine.Volume) error {
	if root, owner := a.ownerRoot(ctx, vol); owner == ownerAlive && root != inst.Root {
		return &Error{
			Code:    CodeCopiedWorktree,
			Message: fmt.Sprintf("this worktree is a copy of %s and shares its identity", root),
			Hint:    "give the copy its own name: mutti rename <name>",
		}
	}
	if err := a.Engine.RemoveVolume(ctx, vol.Name); err != nil {
		return engineErr(err)
	}
	if _, err := a.Engine.CreateVolume(ctx, vol.Name, a.labels(inst, RoleClaim)); err != nil {
		return engineErr(err)
	}
	return nil
}

// ownership is what mutti can tell about the worktree behind a claim.
type ownership int

const (
	ownerUnknown ownership = iota // mutti cannot check from here; never treated as gone
	ownerAlive                    // the worktree exists and holds the claim's identity
	ownerGone                     // the worktree is gone, or holds another identity
)

// origin names where paths in labels are valid: on the Docker host, or inside one container.
func (a *App) origin() string {
	if a.Host.ContainerID != "" {
		return "container:" + a.Host.ContainerID
	}
	return "host"
}

// ownerRoot finds the worktree a claim belongs to now and tells whether it still holds the
// claim's identity. Git worktrees are followed through their admin dir, so a moved worktree is
// found at its new place. Only a path that does not exist counts as gone: a claim made in
// another place (a container, or the host seen from one), a path this user cannot read, or any
// other doubt leaves the claim alone, because a gone claim gets purged with its data.
func (a *App) ownerRoot(ctx context.Context, vol engine.Volume) (string, ownership) {
	root := vol.Labels[engine.LabelPath]
	if vol.Labels[LabelOrigin] != a.origin() {
		return root, ownerUnknown
	}
	if gitDir := vol.Labels[engine.LabelGitDir]; gitDir != "" {
		if _, err := os.Stat(gitDir); err != nil {
			return root, goneIfMissing(err)
		}
		// Without a gitdir file (a repository made with --separate-git-dir), the path label is
		// all there is.
		r, ok, err := gitdir.WorktreeRoot(gitDir)
		if err != nil {
			return root, ownerUnknown
		}
		if ok {
			root = r
		}
	}
	if _, err := os.Stat(root); err != nil {
		return root, goneIfMissing(err)
	}
	info, inGit, err := a.Git.Find(ctx, root)
	gitDir := ""
	if err == nil && inGit {
		gitDir = info.GitDir
	}
	id, ok, err := identity.Load(identity.FilePath(gitDir, root))
	switch {
	case err != nil:
		return root, ownerUnknown
	case ok && id.ID == vol.Labels[engine.LabelID]:
		return root, ownerAlive
	default:
		return root, ownerGone
	}
}

func goneIfMissing(err error) ownership {
	if errors.Is(err, fs.ErrNotExist) {
		return ownerGone
	}
	return ownerUnknown
}

// ensureOwner refuses to act on an instance whose name another worktree claimed: a second clone
// that was refused at up must not stop or destroy the first one's containers. An unclaimed name
// is fine; nothing of mutti runs under it.
func (a *App) ensureOwner(ctx context.Context, inst *Instance) error {
	vol, ok, err := a.Engine.InspectVolume(ctx, claimVolume(inst.Name))
	if err != nil {
		return engineErr(err)
	}
	if !ok || inst.ID != "" && vol.Labels[engine.LabelID] == inst.ID {
		return nil
	}
	root, _ := a.ownerRoot(ctx, vol)
	return &Error{
		Code:    CodeNameTaken,
		Message: fmt.Sprintf("the name %q belongs to %s, not to this worktree", inst.Name, root),
		Hint:    "give this worktree its own name: mutti rename <name>",
	}
}
