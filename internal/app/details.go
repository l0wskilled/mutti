package app

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/l0wskilled/mutti/internal/engine"
)

// ServiceDetails is how the container of a service runs: what it was made from, since when, and
// what it uses at the moment.
type ServiceDetails struct {
	Service      string       `json:"service"`
	Image        string       `json:"image"`
	StartedAt    time.Time    `json:"startedAt,omitzero"`
	RestartCount int          `json:"restartCount"`
	Health       *HealthCheck `json:"health,omitempty"` // the last health check
	Usage        *Usage       `json:"usage,omitempty"`  // while it runs
}

// Usage is one sample of what a container uses.
type Usage struct {
	Sampled     time.Time     `json:"sampled"`
	CPU         time.Duration `json:"cpu"`         // CPU time used since it started, over all cores
	Memory      uint64        `json:"memory"`      // bytes
	MemoryLimit uint64        `json:"memoryLimit"` // bytes
}

// Details describes the containers of the named instance service by service. It takes the
// instance's name rather than its directory, so it is cheap enough to ask every few seconds.
func (a *App) Details(ctx context.Context, instance string) ([]ServiceDetails, error) {
	containers, err := a.Engine.ListContainers(ctx, engine.LabelComposeProject+"="+instance)
	if err != nil {
		return nil, engineErr(err)
	}
	out := []ServiceDetails{}
	for _, c := range containers {
		d, _, err := a.Engine.InspectContainer(ctx, c.ID) // one removed meanwhile tells nothing
		if err != nil {
			return nil, engineErr(err)
		}
		sd := ServiceDetails{Service: c.Labels[engine.LabelComposeService], Image: d.Image, StartedAt: d.Runtime.StartedAt, RestartCount: d.Runtime.RestartCount}
		if n := len(d.Runtime.Health); n > 0 {
			h := d.Runtime.Health[n-1]
			sd.Health = &HealthCheck{Time: h.Start, ExitCode: h.ExitCode, Output: strings.TrimRight(h.Output, "\n")}
		}
		if c.State == "running" {
			// One that stops meanwhile has no usage, which is no error.
			if st, err := a.Engine.ContainerStats(ctx, c.ID); err == nil {
				sd.Usage = &Usage{Sampled: st.Read, CPU: st.CPU, Memory: st.Memory, MemoryLimit: st.MemoryLimit}
			}
		}
		out = append(out, sd)
	}
	slices.SortFunc(out, func(x, y ServiceDetails) int { return cmp.Compare(x.Service, y.Service) })
	return out, nil
}
