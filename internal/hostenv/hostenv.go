// Package hostenv tells whether mutti runs inside a container and maps paths of that container
// to paths on the Docker host, which is what the daemon needs for bind mounts.
package hostenv

import (
	"bytes"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/l0wskilled/mutti/internal/engine"
)

// FS is the part of the filesystem detection reads; the real one is os.ReadFile.
type FS func(path string) ([]byte, error)

var containerID = regexp.MustCompile(`/containers/([0-9a-f]{64})/`)

// SelfContainer returns the id of the container mutti runs in, or ok=false on a plain host.
// The id comes from /proc/self/mountinfo, where Docker's per-container files show up; the
// hostname, Docker's default for it, is the fallback.
func SelfContainer(read FS) (id string, ok bool) {
	_, docker := read("/.dockerenv")
	_, podman := read("/run/.containerenv")
	if docker != nil && podman != nil {
		return "", false
	}
	if b, err := read("/proc/self/mountinfo"); err == nil {
		if m := containerID.FindSubmatch(b); m != nil {
			return string(m[1]), true
		}
	}
	if b, err := read("/etc/hostname"); err == nil {
		if name := strings.TrimSpace(string(bytes.SplitN(b, []byte("\n"), 2)[0])); name != "" {
			return name, true
		}
	}
	return "", false
}

// PathMap translates paths inside this container to paths on the Docker host.
type PathMap struct {
	mounts []engine.Mount // longest Target first
}

// NewPathMap builds a map from the container's mounts. Extra pairs (container path -> host
// path) win over the mounts, for runners that know better, like CI systems.
func NewPathMap(mounts []engine.Mount, extra map[string]string) *PathMap {
	var all []engine.Mount
	for target, source := range extra {
		all = append(all, engine.Mount{Source: source, Target: filepath.Clean(target)})
	}
	for _, m := range mounts {
		if _, ok := extra[m.Target]; !ok {
			all = append(all, m)
		}
	}
	slices.SortStableFunc(all, func(x, y engine.Mount) int { return len(y.Target) - len(x.Target) })
	return &PathMap{mounts: all}
}

// ToHost returns the host path of p, or ok=false when p is not on any mount (it only exists
// inside this container).
func (m *PathMap) ToHost(p string) (string, bool) {
	p = filepath.Clean(p)
	for _, mt := range m.mounts {
		if p == mt.Target {
			return mt.Source, true
		}
		if rel, ok := strings.CutPrefix(p, mt.Target+string(filepath.Separator)); ok {
			return filepath.Join(mt.Source, rel), true
		}
		if mt.Target == "/" {
			return filepath.Join(mt.Source, p), true
		}
	}
	return "", false
}

// Lines returns the "a -> b" view of the map, for doctor.
func (m *PathMap) Lines() []string {
	var out []string
	for _, mt := range m.mounts {
		out = append(out, mt.Target+" -> "+mt.Source)
	}
	return out
}

// DockerSocket returns the Docker socket's path on the Docker host, for containers that talk to
// Docker: $MUTTI_DOCKER_SOCKET if set (needed where mutti itself runs in a container), else the
// path of a unix daemon address like unix:///run/user/1000/docker.sock, else the default.
func DockerSocket(getenv func(string) string, daemonHost string) string {
	if s := getenv("MUTTI_DOCKER_SOCKET"); s != "" {
		return s
	}
	if u, err := url.Parse(daemonHost); err == nil && u.Scheme == "unix" && u.Path != "" {
		return u.Path
	}
	return "/var/run/docker.sock"
}
