package app

import (
	"context"
	"strconv"
	"time"

	"github.com/l0wskilled/mutti/internal/engine"
)

// InstanceEvent is something that happened to a container of an instance.
type InstanceEvent struct {
	Time      time.Time `json:"time"`
	Instance  string    `json:"instance"`
	Service   string    `json:"service,omitempty"`
	Container string    `json:"container"`
	// Event is created, started, stopped, removed, paused, unpaused, healthy or unhealthy.
	Event    string `json:"event"`
	ExitCode *int   `json:"exitCode,omitempty"` // with stopped
}

// eventNames maps the Docker events that change what list and status show to mutti's names.
var eventNames = map[string]string{
	"create": "created", "start": "started", "die": "stopped", "destroy": "removed",
	"pause": "paused", "unpause": "unpaused",
	"health_status: healthy": "healthy", "health_status: unhealthy": "unhealthy",
}

// Events streams what happens to the containers of every instance until ctx ends. mutti's own
// helper containers (probes, copies, reapers) are left out. The error channel gets one error when
// the stream from Docker fails; the event channel closes either way, and has to be read until it
// does.
func (a *App) Events(ctx context.Context) (<-chan InstanceEvent, <-chan error) {
	in, errs := a.Engine.Events(ctx, engine.LabelInstance)
	out := make(chan InstanceEvent)
	go func() {
		defer close(out)
		for ev := range in {
			name, known := eventNames[ev.Action]
			if !known || ev.Attributes[engine.LabelRole] != "" {
				continue
			}
			ie := InstanceEvent{
				Time: ev.Time, Instance: ev.Attributes[engine.LabelInstance], Service: ev.Attributes[engine.LabelComposeService],
				Container: ev.Name, Event: name,
			}
			if code, err := strconv.Atoi(ev.Attributes["exitCode"]); err == nil && name == "stopped" {
				ie.ExitCode = &code
			}
			out <- ie
		}
	}()
	return out, errs
}
