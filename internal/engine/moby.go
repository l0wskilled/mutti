package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	networktypes "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Moby implements Engine with the official Docker client.
type Moby struct {
	c *client.Client
}

// Connect opens a client for the daemon the docker CLI would talk to: $DOCKER_HOST if set,
// else the endpoint of the current docker context.
func Connect(ctx context.Context) (*Moby, error) {
	opts := []client.Opt{client.FromEnv}
	if os.Getenv("DOCKER_HOST") == "" {
		if host, err := contextHost(ctx); err == nil && host != "" {
			opts = append(opts, client.WithHost(host))
		}
	}
	c, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Moby{c: c}, nil
}

// contextHost asks the docker CLI for the endpoint of the current context.
func contextHost(ctx context.Context) (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	return strings.TrimSpace(string(out)), err
}

// DaemonHost is the address the client talks to, e.g. unix:///var/run/docker.sock.
func (m *Moby) DaemonHost() string { return m.c.DaemonHost() }

// Close releases the client's connections.
func (m *Moby) Close() error { return m.c.Close() }

func filters(labels []string) client.Filters {
	f := client.Filters{}
	if len(labels) > 0 {
		f = f.Add("label", labels...)
	}
	return f
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	if client.IsErrConnectionFailed(err) {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return err
}

func ignoreNotFound(err error) error {
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return wrap(err)
}

func (m *Moby) Info(ctx context.Context) (Info, error) {
	res, err := m.c.Info(ctx, client.InfoOptions{})
	if err != nil {
		return Info{}, wrap(err)
	}
	i := res.Info
	rootless := slices.ContainsFunc(i.SecurityOptions, func(s string) bool { return strings.Contains(s, "name=rootless") })
	return Info{
		ServerVersion: i.ServerVersion,
		APIVersion:    m.c.ClientVersion(),
		OS:            i.OperatingSystem,
		Rootless:      rootless,
		Host:          m.c.DaemonHost(),
	}, nil
}

func (m *Moby) CreateVolume(ctx context.Context, name string, labels map[string]string) (Volume, error) {
	res, err := m.c.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: labels})
	if err != nil {
		return Volume{}, wrap(err)
	}
	return Volume{Name: res.Volume.Name, Labels: res.Volume.Labels}, nil
}

func (m *Moby) InspectVolume(ctx context.Context, name string) (Volume, bool, error) {
	res, err := m.c.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return Volume{}, false, nil
	}
	if err != nil {
		return Volume{}, false, wrap(err)
	}
	return Volume{Name: res.Volume.Name, Labels: res.Volume.Labels}, true, nil
}

func (m *Moby) ListVolumes(ctx context.Context, labelFilters ...string) ([]Volume, error) {
	res, err := m.c.VolumeList(ctx, client.VolumeListOptions{Filters: filters(labelFilters)})
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]Volume, 0, len(res.Items))
	for _, v := range res.Items {
		out = append(out, Volume{Name: v.Name, Labels: v.Labels})
	}
	return out, nil
}

func (m *Moby) RemoveVolume(ctx context.Context, name string) error {
	_, err := m.c.VolumeRemove(ctx, name, client.VolumeRemoveOptions{})
	if cerrdefs.IsConflict(err) {
		return fmt.Errorf("volume %s is in use: %w", name, ErrConflict)
	}
	return ignoreNotFound(err)
}

func (m *Moby) ListContainers(ctx context.Context, labelFilters ...string) ([]Container, error) {
	res, err := m.c.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters(labelFilters)})
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]Container, 0, len(res.Items))
	for _, c := range res.Items {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		var ports []Port
		for _, p := range c.Ports {
			if p.PublicPort == 0 {
				continue
			}
			ip := ""
			if p.IP.IsValid() {
				ip = p.IP.String()
			}
			ports = append(ports, Port{HostIP: ip, HostPort: p.PublicPort, ContainerPort: p.PrivatePort, Proto: p.Type})
		}
		var networks []string
		if c.NetworkSettings != nil {
			networks = slices.Sorted(maps.Keys(c.NetworkSettings.Networks))
		}
		out = append(out, Container{ID: c.ID, Name: name, State: string(c.State), Status: c.Status, Labels: c.Labels, Ports: ports, Networks: networks})
	}
	return out, nil
}

func (m *Moby) RemoveContainer(ctx context.Context, id string) error {
	_, err := m.c.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	return ignoreNotFound(err)
}

func (m *Moby) ListNetworks(ctx context.Context, labelFilters ...string) ([]Network, error) {
	res, err := m.c.NetworkList(ctx, client.NetworkListOptions{Filters: filters(labelFilters)})
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]Network, 0, len(res.Items))
	for _, n := range res.Items {
		out = append(out, Network{ID: n.ID, Name: n.Name, Labels: n.Labels})
	}
	return out, nil
}

func (m *Moby) RemoveNetwork(ctx context.Context, id string) error {
	_, err := m.c.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
	return ignoreNotFound(err)
}

func (m *Moby) CreateNetwork(ctx context.Context, name string, labels map[string]string) (Network, error) {
	res, err := m.c.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge", Labels: labels})
	if cerrdefs.IsConflict(err) {
		return m.networkByName(ctx, name)
	}
	if err != nil && strings.Contains(err.Error(), PoolsExhaustedMessage) {
		return Network{}, fmt.Errorf("%w: %w", ErrAddressPoolsExhausted, err)
	}
	if err != nil {
		return Network{}, wrap(err)
	}
	return Network{ID: res.ID, Name: name, Labels: labels}, nil
}

func (m *Moby) networkByName(ctx context.Context, name string) (Network, error) {
	res, err := m.c.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err != nil {
		return Network{}, wrap(err)
	}
	return Network{ID: res.Network.ID, Name: res.Network.Name, Labels: res.Network.Labels}, nil
}

func (m *Moby) ConnectNetwork(ctx context.Context, network, container string, aliases []string) error {
	_, err := m.c.NetworkConnect(ctx, network, client.NetworkConnectOptions{
		Container:      container,
		EndpointConfig: &networktypes.EndpointSettings{Aliases: aliases},
	})
	return wrap(err)
}

func (m *Moby) DisconnectNetwork(ctx context.Context, network, container string) error {
	_, err := m.c.NetworkDisconnect(ctx, network, client.NetworkDisconnectOptions{Container: container, Force: true})
	if err != nil && (cerrdefs.IsNotFound(err) || strings.Contains(err.Error(), "is not connected")) {
		return nil
	}
	return wrap(err)
}

func (m *Moby) ImageExists(ctx context.Context, ref string) (bool, error) {
	_, err := m.c.ImageInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return err == nil, wrap(err)
}

func (m *Moby) PullImage(ctx context.Context, ref string) error {
	res, err := m.c.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return wrap(err)
	}
	return wrap(res.Wait(ctx))
}

func (m *Moby) CreateContainer(ctx context.Context, spec ContainerSpec) (string, error) {
	exposed := networktypes.PortSet{}
	bindings := networktypes.PortMap{}
	for _, p := range spec.Ports {
		port, ok := networktypes.PortFrom(p.ContainerPort, networktypes.IPProtocol(p.Proto))
		if !ok {
			return "", fmt.Errorf("port %d: protocol missing", p.ContainerPort)
		}
		ip, err := netip.ParseAddr(p.HostIP)
		if err != nil {
			return "", err
		}
		exposed[port] = struct{}{}
		hostPort := "" // random
		if p.HostPort != 0 {
			hostPort = strconv.Itoa(int(p.HostPort))
		}
		bindings[port] = append(bindings[port], networktypes.PortBinding{HostIP: ip, HostPort: hostPort})
	}
	var mounts []mount.Mount
	for _, mt := range spec.Mounts {
		typ := mount.TypeBind
		if mt.Volume {
			typ = mount.TypeVolume
		}
		mounts = append(mounts, mount.Mount{Type: typ, Source: mt.Source, Target: mt.Target, ReadOnly: mt.ReadOnly})
	}
	var networking *networktypes.NetworkingConfig
	if len(spec.Networks) > 0 {
		networking = &networktypes.NetworkingConfig{EndpointsConfig: map[string]*networktypes.EndpointSettings{}}
		for _, n := range spec.Networks {
			networking.EndpointsConfig[n] = &networktypes.EndpointSettings{}
		}
	}
	res, err := m.c.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:             spec.Name,
		NetworkingConfig: networking,
		Config: &container.Config{
			Image: spec.Image, Cmd: spec.Cmd, Labels: spec.Labels, ExposedPorts: exposed, Env: spec.Env,
		},
		HostConfig: &container.HostConfig{
			PortBindings:  bindings,
			Mounts:        mounts,
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(spec.Restart)},
			AutoRemove:    spec.AutoRemove,
		},
	})
	if cerrdefs.IsConflict(err) {
		return "", fmt.Errorf("container %s: %w", spec.Name, ErrConflict)
	}
	if err != nil {
		return "", wrap(err)
	}
	return res.ID, nil
}

func (m *Moby) StartContainer(ctx context.Context, id string) error {
	_, err := m.c.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return wrap(err)
}

func (m *Moby) InspectContainer(ctx context.Context, id string) (ContainerDetails, bool, error) {
	res, err := m.c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return ContainerDetails{}, false, nil
	}
	if err != nil {
		return ContainerDetails{}, false, wrap(err)
	}
	c := res.Container
	d := ContainerDetails{ID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), Networks: map[string][]string{}, IPs: map[string]string{}}
	if c.State != nil {
		d.State = string(c.State.Status)
	}
	if c.Config != nil {
		d.Labels = c.Config.Labels
	}
	if c.NetworkSettings != nil {
		for name, ep := range c.NetworkSettings.Networks {
			d.Networks[name] = ep.Aliases
			if ep.IPAddress.IsValid() {
				d.IPs[name] = ep.IPAddress.String()
			}
		}
	}
	for _, mp := range c.Mounts {
		d.Mounts = append(d.Mounts, Mount{Source: mp.Source, Target: mp.Destination, ReadOnly: !mp.RW, Volume: mp.Type == mount.TypeVolume})
	}
	return d, true, nil
}

func (m *Moby) RunContainer(ctx context.Context, spec ContainerSpec) (int, error) {
	id, err := m.CreateContainer(ctx, spec)
	if err != nil {
		return 0, err
	}
	defer func() { _ = m.RemoveContainer(context.WithoutCancel(ctx), id) }()
	// Wait first, so the exit cannot happen before anyone listens.
	wait := m.c.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if err := m.StartContainer(ctx, id); err != nil {
		return 0, err
	}
	select {
	case res := <-wait.Result:
		return int(res.StatusCode), nil
	case err := <-wait.Error:
		return 0, wrap(err)
	}
}

func (m *Moby) PauseContainer(ctx context.Context, id string) error {
	_, err := m.c.ContainerPause(ctx, id, client.ContainerPauseOptions{})
	return wrap(err)
}

func (m *Moby) UnpauseContainer(ctx context.Context, id string) error {
	_, err := m.c.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
	return wrap(err)
}

func (m *Moby) CopyToContainer(ctx context.Context, id string, files map[string][]byte) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{}
	for _, p := range slices.Sorted(maps.Keys(files)) {
		rel := strings.TrimPrefix(path.Clean(p), "/")
		for d := path.Dir(rel); d != "." && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	for _, d := range slices.Sorted(maps.Keys(dirs)) {
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d + "/", Mode: 0o755})
	}
	for _, p := range slices.Sorted(maps.Keys(files)) {
		content := files[p]
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: strings.TrimPrefix(path.Clean(p), "/"), Mode: 0o600, Size: int64(len(content))})
		_, _ = tw.Write(content)
	}
	_ = tw.Close() // writes into memory, cannot fail
	_, err := m.c.CopyToContainer(ctx, id, client.CopyToContainerOptions{DestinationPath: "/", Content: &buf})
	return wrap(err)
}

func (m *Moby) NetworkContainers(ctx context.Context, network string) ([]string, error) {
	res, err := m.c.NetworkInspect(ctx, network, client.NetworkInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap(err)
	}
	return slices.Sorted(maps.Keys(res.Network.Containers)), nil
}

// IsUnavailable reports whether err means the daemon could not be reached.
func IsUnavailable(err error) bool { return errors.Is(err, ErrUnavailable) }

func (m *Moby) Events(ctx context.Context, labelFilters ...string) (<-chan Event, <-chan error) {
	res := m.c.Events(ctx, client.EventsListOptions{Filters: filters(labelFilters).Add("type", "container")})
	out, errs := make(chan Event), make(chan error, 1)
	go func() {
		defer close(out)
		// The client ends the stream with an error, ctx's own when it ends.
		for {
			select {
			case msg := <-res.Messages:
				attrs := maps.Clone(msg.Actor.Attributes)
				out <- Event{Time: time.Unix(0, msg.TimeNano), Action: string(msg.Action), ID: msg.Actor.ID, Name: attrs["name"], Attributes: attrs}
			case err := <-res.Err:
				if ctx.Err() == nil {
					errs <- wrap(err)
				}
				return
			}
		}
	}()
	return out, errs
}
