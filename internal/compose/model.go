package compose

import (
	"encoding/json"
	"maps"
	"slices"
)

// Model is the part of `docker compose config --format json` mutti reads.
type Model struct {
	Name     string             `json:"name"`
	Services map[string]Service `json:"services"`
	XMutti   json.RawMessage    `json:"x-mutti,omitempty"`
}

// Service is one service of the model.
type Service struct {
	Image       string                     `json:"image,omitempty"`
	Ports       []Port                     `json:"ports,omitempty"`
	Expose      []string                   `json:"expose,omitempty"`
	Profiles    []string                   `json:"profiles,omitempty"`
	Labels      map[string]string          `json:"labels,omitempty"`
	Networks    map[string]json.RawMessage `json:"networks,omitempty"`
	NetworkMode string                     `json:"network_mode,omitempty"`
	Volumes     []VolumeMount              `json:"volumes,omitempty"`
}

// VolumeMount is a mount of a service in Compose's long syntax.
type VolumeMount struct {
	Type     string    `json:"type"` // bind, volume, tmpfs, ...
	Source   string    `json:"source,omitempty"`
	Target   string    `json:"target"`
	ReadOnly bool      `json:"read_only,omitempty"`
	Bind     *BindOpts `json:"bind,omitempty"`
}

// BindOpts are the options of a bind mount.
type BindOpts struct {
	CreateHostPath bool   `json:"create_host_path,omitempty"`
	Propagation    string `json:"propagation,omitempty"`
	SELinux        string `json:"selinux,omitempty"`
}

// Port is a port entry in Compose's long syntax.
type Port struct {
	Target    uint16 `json:"target"`
	Published string `json:"published,omitempty"`
	HostIP    string `json:"host_ip,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
}

// ServiceNames returns the service names, sorted.
func (m *Model) ServiceNames() []string {
	return slices.Sorted(maps.Keys(m.Services))
}
