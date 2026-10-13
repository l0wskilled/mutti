package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/hostenv"
)

// Error codes of nested mode.
const (
	CodeNotNested   = "not-nested"
	CodeUnmappable  = "unmappable-path"
	CodeSelfUnknown = "self-unknown"
)

// pathMap tells how this process's paths look on the Docker host. It is nil when mutti runs on
// the host itself. Inside a container it comes from the container's own mounts;
// $MUTTI_HOST_ROOT names the host path of the worktree root where inspecting is not possible
// or not enough (CI runners, Docker-in-Docker).
func (a *App) pathMap(ctx context.Context, root string) (*hostenv.PathMap, error) {
	extra := map[string]string{}
	if hp := a.Host.Getenv("MUTTI_HOST_ROOT"); hp != "" {
		extra[root] = hp
	}
	if a.Host.ContainerID == "" {
		if len(extra) == 0 {
			return nil, nil
		}
		return hostenv.NewPathMap(nil, extra), nil
	}
	self, ok, err := a.Engine.InspectContainer(ctx, a.Host.ContainerID)
	if err != nil {
		return nil, engineErr(err)
	}
	if !ok && len(extra) == 0 {
		return nil, &Error{
			Code:    CodeSelfUnknown,
			Message: fmt.Sprintf("mutti runs in container %s, which this Docker daemon does not know", a.Host.ContainerID),
			Hint:    "set MUTTI_HOST_ROOT to the worktree's path as the Docker host sees it",
		}
	}
	return hostenv.NewPathMap(self.Mounts, extra), nil
}

// hostPaths sets the worktree root's path on the Docker host for an instance.
func (a *App) hostPaths(ctx context.Context, inst *Instance) error {
	inst.HostPath = inst.Root
	pm, err := a.pathMap(ctx, inst.Root)
	if err != nil || pm == nil {
		return err
	}
	hp, ok := pm.ToHost(inst.Root)
	if !ok {
		return &Error{
			Code:    CodeUnmappable,
			Message: fmt.Sprintf("the worktree %s exists only inside this container, the Docker host cannot mount it", inst.Root),
			Hint:    "bind-mount the worktree into this container, or set MUTTI_HOST_ROOT",
		}
	}
	inst.HostPath, inst.paths = hp, pm
	return nil
}

// translatedBinds returns the bind mounts of m with sources on the Docker host, per service,
// for the override. It is nil when mutti runs on the host.
func (inst *Instance) translatedBinds(m *compose.Model) (map[string][]compose.VolumeMount, error) {
	if inst.paths == nil {
		return nil, nil
	}
	out := map[string][]compose.VolumeMount{}
	for _, name := range m.ServiceNames() {
		for _, v := range m.Services[name].Volumes {
			if v.Type != "bind" {
				continue
			}
			hp, ok := inst.paths.ToHost(v.Source)
			if !ok && underPath(v.Source, inst.HostPath) {
				// Written with the host path already, e.g. ${MUTTI_HOST_PATH}/src.
				hp, ok = v.Source, true
			}
			if !ok {
				return nil, &Error{
					Code:    CodeUnmappable,
					Message: fmt.Sprintf("service %s mounts %s, which exists only inside this container", name, v.Source),
					Hint:    "bind-mount it into this container too, or move it into the worktree",
				}
			}
			v.Source = hp
			out[name] = append(out[name], v)
		}
	}
	return out, nil
}

// Attach connects the container mutti runs in to an instance's ingress network, so it reaches
// the instance's services by name and its hostnames through the router, e.g. for tests in a CI
// step container.
func (a *App) Attach(ctx context.Context, dir string, detach bool) (*Instance, error) {
	if a.Host.ContainerID == "" {
		return nil, &Error{Code: CodeNotNested, Message: "mutti attach only works inside a container", Hint: "on the host, use the URLs from mutti status"}
	}
	inst, err := a.Resolve(ctx, dir)
	if err != nil {
		return nil, err
	}
	if err := a.ensureOwner(ctx, inst); err != nil {
		return nil, err
	}
	self, _, err := a.Engine.InspectContainer(ctx, a.Host.ContainerID)
	if err != nil {
		return nil, engineErr(err)
	}
	network := ingressNetwork(inst.Name)
	_, attached := self.Networks[network]
	switch {
	case detach && attached:
		err = a.Engine.DisconnectNetwork(ctx, network, a.Host.ContainerID)
	case !detach && !attached:
		err = a.Engine.ConnectNetwork(ctx, network, a.Host.ContainerID, nil)
	}
	return inst, engineErr(err)
}

// detachAll takes every container off an instance's ingress network, so it can go: the router
// and any container attached with mutti attach.
func (a *App) detachAll(ctx context.Context, instance string) error {
	network := ingressNetwork(instance)
	ids, err := a.Engine.NetworkContainers(ctx, network)
	if err != nil {
		return engineErr(err)
	}
	s, err := a.routerSettings()
	if err != nil {
		return err
	}
	// The router by name too: when it is stopped, it is no member Docker would list.
	for _, id := range append(ids, s.Name) {
		if err := a.Engine.DisconnectNetwork(ctx, network, id); err != nil {
			return engineErr(err)
		}
	}
	return nil
}

// underPath reports whether p is dir or lies below it.
func underPath(p, dir string) bool {
	return dir != "" && (p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/"))
}
