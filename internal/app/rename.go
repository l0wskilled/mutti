package app

import (
	"context"
	"fmt"

	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/identity"
)

// Rename gives the worktree's instance a new name and a new identity id. Data does not move
// between names, so the instance must have no containers or volumes left.
func (a *App) Rename(ctx context.Context, dir, name string) (*Instance, error) {
	if !identity.Valid(name) {
		return nil, &Error{Code: CodeInvalidName, Message: fmt.Sprintf("%q is not a valid name", name), Hint: "use lower case letters, digits and '-'"}
	}
	inst, err := a.Resolve(ctx, dir)
	if err != nil {
		return nil, err
	}
	if err := refuseRun(inst, "rename"); err != nil {
		return nil, err
	}
	old := inst.Name
	ownsOld := false
	if inst.ID != "" {
		if v, ok, err := a.Engine.InspectVolume(ctx, claimVolume(old)); err != nil {
			return nil, engineErr(err)
		} else if ok && v.Labels[engine.LabelID] == inst.ID {
			ownsOld = true
		}
	}
	// Only our own instance's data would be lost; under a name someone else owns, nothing is ours.
	if ownsOld {
		inUse, err := a.hasResources(ctx, old)
		if err != nil {
			return nil, err
		}
		if inUse {
			return nil, &Error{Code: CodeInstanceInUse, Message: fmt.Sprintf("%s still has containers or volumes", old), Hint: "remove them first: mutti destroy"}
		}
	}

	inst.Worktree, inst.ID = name, ""
	inst.Name = identity.InstanceName(inst.Project, inst.Worktree)
	inst.Hostname = a.hostname(inst)
	if err := a.claim(ctx, inst); err != nil {
		return nil, err
	}
	if ownsOld {
		if err := a.Engine.RemoveVolume(ctx, claimVolume(old)); err != nil {
			return nil, engineErr(err)
		}
	}
	return inst, nil
}

func (a *App) hasResources(ctx context.Context, instance string) (bool, error) {
	filter := engine.LabelComposeProject + "=" + instance
	containers, err := a.Engine.ListContainers(ctx, filter)
	if err != nil {
		return false, engineErr(err)
	}
	volumes, err := a.Engine.ListVolumes(ctx, filter)
	if err != nil {
		return false, engineErr(err)
	}
	return len(containers) > 0 || len(volumes) > 0, nil
}
