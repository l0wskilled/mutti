package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
)

// diagnoseLogLines is how many log lines a diagnosis carries per service.
const diagnoseLogLines = 40

// Diagnosis explains why a service is not well: what went wrong, what its health checks said
// and what it printed last.
type Diagnosis struct {
	Service      string        `json:"service"`
	State        string        `json:"state"`
	Status       string        `json:"status"`
	Problem      string        `json:"problem"`        // what is wrong, in a few words
	Hint         string        `json:"hint,omitempty"` // what to do next
	ExitCode     int           `json:"exitCode,omitempty"`
	OOMKilled    bool          `json:"oomKilled,omitempty"`
	RestartCount int           `json:"restartCount,omitempty"`
	Health       []HealthCheck `json:"health,omitempty"` // the last health checks, oldest first
	Logs         string        `json:"logs"`             // the last lines the service printed
}

// HealthCheck is one health check result.
type HealthCheck struct {
	Time     time.Time `json:"time"`
	ExitCode int       `json:"exitCode"`
	Output   string    `json:"output"`
}

// Diagnose explains the services of dir's instance that are not well, or the named services
// whatever their state.
func (a *App) Diagnose(ctx context.Context, dir string, services ...string) ([]Diagnosis, error) {
	inst, err := a.Resolve(ctx, dir)
	if err != nil {
		return nil, err
	}
	containers, err := a.Engine.ListContainers(ctx, engine.LabelComposeProject+"="+inst.Name)
	if err != nil {
		return nil, engineErr(err)
	}
	done := completed(containers)
	var picked []engine.Container
	for _, c := range containers {
		name := c.Labels[engine.LabelComposeService]
		if len(services) > 0 && slices.Contains(services, name) || len(services) == 0 && troubled(c, done[c.ID]) {
			picked = append(picked, c)
		}
	}
	if len(picked) == 0 {
		return []Diagnosis{}, nil
	}
	spec, err := a.prepare(ctx, inst, a.lastSelection(inst))
	if err != nil {
		return nil, err
	}
	slices.SortFunc(picked, func(x, y engine.Container) int {
		return strings.Compare(x.Labels[engine.LabelComposeService], y.Labels[engine.LabelComposeService])
	})
	out := make([]Diagnosis, 0, len(picked))
	for _, c := range picked {
		d, _, err := a.Engine.InspectContainer(ctx, c.ID)
		if err != nil {
			return nil, engineErr(err)
		}
		diag := diagnosis(c, d.Runtime, done[c.ID])
		var logs tail
		_, _ = a.Compose.Run(ctx, spec, compose.IO{Out: &logs, Err: &logs}, "logs", "--no-color", "--tail", fmt.Sprint(diagnoseLogLines), diag.Service)
		diag.Logs = logs.String()
		out = append(out, diag)
	}
	return out, nil
}

// troubled reports whether a container needs a look: it is unhealthy, restarting, dead, or it
// ended badly. A one-shot service that did its job and a service stopped on purpose are fine.
func troubled(c engine.Container, completed bool) bool {
	switch {
	case completed:
		return false
	case strings.Contains(c.Status, "(unhealthy)"), c.State == "restarting", c.State == "dead":
		return true
	case c.State == "exited":
		return !strings.HasPrefix(c.Status, "Exited (0)") && !strings.HasPrefix(c.Status, "Exited (143)")
	}
	return false
}

// diagnosis names the problem of a container and the next step.
func diagnosis(c engine.Container, r engine.Runtime, completed bool) Diagnosis {
	service := c.Labels[engine.LabelComposeService]
	d := Diagnosis{Service: service, State: c.State, Status: c.Status, ExitCode: r.ExitCode, OOMKilled: r.OOMKilled, RestartCount: r.RestartCount}
	for _, h := range r.Health {
		d.Health = append(d.Health, HealthCheck{Time: h.Start, ExitCode: h.ExitCode, Output: strings.TrimRight(h.Output, "\n")})
	}
	logs := "see its logs below, or follow them: mutti logs -f " + service
	switch {
	case r.Error != "":
		d.Problem, d.Hint = "could not start: "+r.Error, "check the image, command and mounts of "+service+" in the Compose file"
	case r.OOMKilled:
		d.Problem, d.Hint = "killed: out of memory", "give it more memory (deploy.resources.limits.memory), or look for a leak"
	case c.State == "restarting" || c.State == "running" && r.RestartCount > 0 && !strings.Contains(c.Status, "(healthy)"):
		d.Problem, d.Hint = fmt.Sprintf("crashing: restarted %d times, last exit code %d", r.RestartCount, r.ExitCode), logs
	case strings.Contains(c.Status, "(unhealthy)"):
		d.Problem, d.Hint = "unhealthy: its health check fails", "the check runs inside the container; its output is below. Look inside: mutti shell "+service
		if n := len(d.Health); n > 0 {
			d.Problem = fmt.Sprintf("unhealthy: its health check exits with %d", d.Health[n-1].ExitCode)
		}
	case c.State == "dead":
		d.Problem, d.Hint = "dead: Docker could not stop or remove it", "remove it and start again: mutti down, then mutti up"
	case c.State == "exited" && !completed:
		d.Problem, d.Hint = exitProblem(r.ExitCode), logs
	default:
		d.Problem = "no problem found: " + c.Status
	}
	return d
}

// exitProblem names what an exit code usually means.
func exitProblem(code int) string {
	switch code {
	case 0:
		return "stopped"
	case 126:
		return "exited with 126: the command is not executable"
	case 127:
		return "exited with 127: the command was not found"
	case 137:
		return "exited with 137: killed (SIGKILL)"
	case 143:
		return "exited with 143: stopped (SIGTERM)"
	}
	return fmt.Sprintf("exited with %d", code)
}
