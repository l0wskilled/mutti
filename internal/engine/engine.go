// Package engine is mutti's narrow view of the Docker Engine API. Everything mutti reads or writes
// directly on the daemon goes through Engine, so tests can swap in the in-memory fake from
// enginetest.
package engine

import (
	"context"
	"errors"
	"time"
)

// Label keys mutti sets on resources it creates itself.
const (
	LabelSchema   = "mutti.schema"   // label schema version, for later migrations
	LabelInstance = "mutti.instance" // instance name (= Compose project name)
	LabelProject  = "mutti.project"  // project name
	LabelID       = "mutti.id"       // identity id of the owning worktree
	LabelPath     = "mutti.path"     // worktree path when the label was written (informational)
	LabelGitDir   = "mutti.gitdir"   // per-worktree git admin dir, empty outside git
	LabelRole     = "mutti.role"     // what a mutti-made resource is for, e.g. "claim"

	// LabelComposeProject is set by Compose on everything it creates.
	LabelComposeProject = "com.docker.compose.project"
	// LabelComposeService is set by Compose on containers.
	LabelComposeService = "com.docker.compose.service"

	SchemaVersion = "1"
)

// ErrUnavailable means the Docker daemon cannot be reached.
var ErrUnavailable = errors.New("docker daemon is not reachable")

// ErrConflict means a resource with that name already exists.
var ErrConflict = errors.New("already exists")

// ErrAddressPoolsExhausted means Docker has no subnet left for a new network.
var ErrAddressPoolsExhausted = errors.New("docker has no address pool left for a new network")

// PoolsExhaustedMessage is how Docker reports ErrAddressPoolsExhausted.
const PoolsExhaustedMessage = "all predefined address pools have been fully subnetted"

// Engine is the subset of the Docker Engine API mutti uses. Filters are label filters in Docker
// syntax: "key" or "key=value", all of which must match.
type Engine interface {
	Info(ctx context.Context) (Info, error)

	// CreateVolume creates a volume or returns the existing one with that name unchanged.
	CreateVolume(ctx context.Context, name string, labels map[string]string) (Volume, error)
	InspectVolume(ctx context.Context, name string) (vol Volume, ok bool, err error)
	ListVolumes(ctx context.Context, labelFilters ...string) ([]Volume, error)
	// RemoveVolume removes a volume. A missing volume is not an error; one a container uses is
	// ErrConflict.
	RemoveVolume(ctx context.Context, name string) error

	// ListContainers lists containers in any state.
	ListContainers(ctx context.Context, labelFilters ...string) ([]Container, error)
	// RemoveContainer force-removes a container with its anonymous volumes. A missing container
	// is not an error.
	RemoveContainer(ctx context.Context, id string) error

	ListNetworks(ctx context.Context, labelFilters ...string) ([]Network, error)
	// RemoveNetwork removes a network. A missing network is not an error.
	RemoveNetwork(ctx context.Context, id string) error

	// CreateNetwork creates a bridge network or returns the existing one with that name.
	CreateNetwork(ctx context.Context, name string, labels map[string]string) (Network, error)
	// ConnectNetwork attaches a container to a network under extra DNS aliases.
	ConnectNetwork(ctx context.Context, network, container string, aliases []string) error
	// DisconnectNetwork detaches a container. Not being attached is not an error.
	DisconnectNetwork(ctx context.Context, network, container string) error

	// ImageExists reports whether an image is present locally.
	ImageExists(ctx context.Context, ref string) (bool, error)
	// PullImage pulls an image and waits until it is complete.
	PullImage(ctx context.Context, ref string) error
	// CreateContainer creates a container; a taken name returns ErrConflict.
	CreateContainer(ctx context.Context, spec ContainerSpec) (id string, err error)
	StartContainer(ctx context.Context, id string) error
	// InspectContainer returns details of a container by id or name; ok is false if missing.
	InspectContainer(ctx context.Context, id string) (c ContainerDetails, ok bool, err error)
	// RunContainer runs a one-off container to completion, removes it and returns its exit code.
	RunContainer(ctx context.Context, spec ContainerSpec) (exitCode int, err error)
	// Events streams what happens to containers matching labelFilters until ctx ends. A failing
	// stream sends one error and ends; the event channel closes either way, and has to be read
	// until it does.
	Events(ctx context.Context, labelFilters ...string) (<-chan Event, <-chan error)
	PauseContainer(ctx context.Context, id string) error
	UnpauseContainer(ctx context.Context, id string) error
	// CopyToContainer writes files (absolute path -> content) into a container, creating parent
	// directories; it works on created and running containers.
	CopyToContainer(ctx context.Context, id string, files map[string][]byte) error
	// NetworkContainers returns the ids of the containers attached to a network.
	NetworkContainers(ctx context.Context, network string) ([]string, error)
}

// ContainerSpec describes a container mutti creates itself (the router).
type ContainerSpec struct {
	Name    string
	Image   string
	Cmd     []string
	Labels  map[string]string
	Ports   []Port  // HostIP + HostPort published to ContainerPort
	Mounts  []Mount // bind mounts
	Restart string  // restart policy, e.g. "unless-stopped"
	// Networks the container joins at creation, instead of the default bridge.
	Networks []string `json:",omitempty"`
	// Env are environment variables, KEY=value.
	Env []string `json:",omitempty"`
	// AutoRemove removes the container once it stops.
	AutoRemove bool `json:",omitempty"`
}

// Mount is a bind mount of a host path, or a named volume.
type Mount struct {
	Source   string // host path, or volume name with Volume
	Target   string // container path
	ReadOnly bool
	Volume   bool `json:",omitempty"` // omitted when false, so it only changes a spec's hash where it is used
}

// ContainerDetails are the inspected parts of a container.
type ContainerDetails struct {
	ID       string
	Name     string
	State    string
	Labels   map[string]string
	Networks map[string][]string // network name -> extra DNS aliases
	IPs      map[string]string   // network name -> IP address
	Mounts   []Mount             // Source is the path on the daemon's host
	Runtime  Runtime
}

// Runtime is how a container last ran: what ended it and what its health checks said.
type Runtime struct {
	ExitCode     int
	OOMKilled    bool
	Error        string    // set when Docker could not start the container
	RestartCount int       // restarts by the restart policy
	FinishedAt   time.Time // zero while it never ended
	Health       []HealthProbe
}

// HealthProbe is one health check result; Docker keeps the last few, oldest first.
type HealthProbe struct {
	Start    time.Time
	ExitCode int
	Output   string
}

// Info describes the daemon.
type Info struct {
	ServerVersion string
	APIVersion    string
	OS            string // e.g. "Arch Linux", "Docker Desktop"
	Rootless      bool
	Host          string // daemon address, e.g. unix:///var/run/docker.sock
}

// Volume is a named volume.
type Volume struct {
	Name   string
	Labels map[string]string
}

// Container is a container summary.
// Event is something that happened to a container.
type Event struct {
	Time   time.Time
	Action string // as Docker names it: create, start, die, destroy, pause, unpause, "health_status: healthy", ...
	ID     string
	Name   string
	// Attributes are the container's labels plus what Docker adds, such as exitCode on die.
	Attributes map[string]string
}

type Container struct {
	ID     string
	Name   string
	State  string // created, running, paused, restarting, removing, exited, dead
	Status string // human readable, e.g. "Up 3 minutes (healthy)"
	Labels map[string]string
	Ports  []Port
	// Networks are the names of the networks the container is attached to.
	Networks []string
}

// Port is a published port.
type Port struct {
	HostIP        string
	HostPort      uint16
	ContainerPort uint16
	Proto         string
}

// Network is a network summary.
type Network struct {
	ID     string
	Name   string
	Labels map[string]string
}
