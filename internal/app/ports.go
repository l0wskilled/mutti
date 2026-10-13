package app

import (
	"context"
	"fmt"
	"hash/fnv"
	"maps"
	"slices"

	"github.com/l0wskilled/mutti/internal/engine"
)

// Fixed host ports come from this range.
const (
	fixedPortBase  = 20000
	fixedPortRange = 40000
	fixedPortTries = 100
)

// stablePort is the preferred host port of a service's container port: a hash, so it stays the
// same across restarts and machines without being stored anywhere.
func stablePort(instance, service string, port uint16) uint16 {
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%s/%s/%d", instance, service, port) // hash writes never fail
	return uint16(fixedPortBase + h.Sum32()%fixedPortRange)
}

// fixedPorts picks the host ports for x-mutti.ports. A port the instance already holds is kept;
// a port something else holds moves on to the next free one.
func (a *App) fixedPorts(ctx context.Context, inst *Instance) (map[string]map[uint16]uint16, error) {
	if len(inst.Config.Ports) == 0 {
		return nil, nil
	}
	containers, err := a.Engine.ListContainers(ctx, engine.LabelComposeProject+"="+inst.Name)
	if err != nil {
		return nil, engineErr(err)
	}
	own := map[uint16]bool{}
	for _, c := range containers {
		for _, p := range c.Ports {
			own[p.HostPort] = true
		}
	}
	taken := map[uint16]bool{}
	out := map[string]map[uint16]uint16{}
	for _, service := range slices.Sorted(maps.Keys(inst.Config.Ports)) {
		out[service] = map[uint16]uint16{}
		for _, target := range inst.Config.Ports[service] {
			preferred := stablePort(inst.Name, service, target)
			found := false
			for i := range uint16(fixedPortTries) {
				port := fixedPortBase + (preferred-fixedPortBase+i)%fixedPortRange
				// Inside a container, its own loopback says nothing about the host's ports.
				if taken[port] || !own[port] && a.Host.ContainerID == "" && !a.Host.PortFree(port) {
					continue
				}
				taken[port], out[service][target], found = true, port, true
				break
			}
			if !found {
				return nil, &Error{
					Code:    CodeInvalidConfig,
					Message: fmt.Sprintf("no free host port for %s:%d near %d", service, target, preferred),
					Hint:    "free some ports in 20000-59999 on 127.0.0.1",
				}
			}
		}
	}
	return out, nil
}
