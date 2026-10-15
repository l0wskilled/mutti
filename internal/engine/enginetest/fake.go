// Package enginetest provides an in-memory engine.Engine and a contract suite that keeps it
// honest: the same suite runs against the fake and against a real daemon.
package enginetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/l0wskilled/mutti/internal/engine"
)

// Fake is an in-memory Engine. The zero value is not usable, call New.
type Fake struct {
	mu sync.Mutex
	s  State
	n  int

	// FailOn makes the named method ("CreateVolume", "ListContainers", ...) return the error.
	FailOn map[string]error
	// OnCall, if set, runs before every method; a non-nil result is returned as its error.
	OnCall func(method string) error
	// RunExit is the exit code RunContainer reports.
	RunExit int
	// CreateVolumeHidesLabels makes CreateVolume answer for an existing volume without its
	// labels, as Docker may right after it started.
	CreateVolumeHidesLabels bool

	runs    []engine.ContainerSpec
	runtime map[string]engine.Runtime    // container id -> what InspectContainer reports
	files   map[string]map[string][]byte // container id -> path -> content
	mounts  map[string][]engine.Mount
	subs    map[*subscriber]bool
}

// subscriber is one Events stream.
type subscriber struct {
	filters []string
	ch      chan engine.Event
}

// State is the fake daemon's content. It is exported so it can be persisted between processes.
type State struct {
	Info       engine.Info
	Volumes    map[string]engine.Volume
	Containers map[string]engine.Container
	Networks   map[string]engine.Network
	Images     map[string]bool
	Specs      map[string]engine.ContainerSpec // containers created with CreateContainer
	// Aliases are the DNS aliases per container id and network name.
	Aliases map[string]map[string][]string
}

// New returns an empty fake daemon.
func New() *Fake {
	return &Fake{s: State{
		Info:       engine.Info{ServerVersion: "29.0.0", APIVersion: "1.52", OS: "fake", Host: "unix:///fake.sock"},
		Volumes:    map[string]engine.Volume{},
		Containers: map[string]engine.Container{},
		Networks:   map[string]engine.Network{},
		Images:     map[string]bool{},
		Specs:      map[string]engine.ContainerSpec{},
		Aliases:    map[string]map[string][]string{},
	}}
}

// Load reads a fake persisted with Save, or returns an empty one if path does not exist.
func Load(path string) (*Fake, error) {
	f := New()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &f.s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	f.n = len(f.s.Containers) + len(f.s.Networks)
	for _, m := range []*map[string]bool{&f.s.Images} {
		if *m == nil {
			*m = map[string]bool{}
		}
	}
	if f.s.Specs == nil {
		f.s.Specs = map[string]engine.ContainerSpec{}
	}
	if f.s.Aliases == nil {
		f.s.Aliases = map[string]map[string][]string{}
	}
	return f, nil
}

// Save persists the fake's state.
func (f *Fake) Save(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := json.MarshalIndent(f.s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func (f *Fake) fail(method string) error {
	if err, ok := f.FailOn[method]; ok {
		return err
	}
	if f.OnCall != nil {
		return f.OnCall(method)
	}
	return nil
}

func (f *Fake) nextID(prefix string) string {
	f.n++
	return fmt.Sprintf("%s%04d", prefix, f.n)
}

// AddContainer adds a container, as Compose would, and returns its id.
func (f *Fake) AddContainer(c engine.Container) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.ID == "" {
		c.ID = f.nextID("c")
	}
	c.Labels = maps.Clone(c.Labels)
	f.s.Containers[c.ID] = c
	f.emit("create", c, nil)
	if c.State == "running" {
		f.emit("start", c, nil)
	}
	return c.ID
}

// SetContainerState changes a container's state, e.g. to "exited".
func (f *Fake) SetContainerState(id, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.s.Containers[id]
	c.State = state
	f.s.Containers[id] = c
	switch state {
	case "running":
		f.emit("start", c, nil)
	case "exited":
		f.emit("die", c, map[string]string{"exitCode": "0"})
	}
}

// AddNetwork adds a network and returns its id.
func (f *Fake) AddNetwork(n engine.Network) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n.ID == "" {
		n.ID = f.nextID("n")
	}
	n.Labels = maps.Clone(n.Labels)
	f.s.Networks[n.ID] = n
	return n.ID
}

// SetInfo replaces what Info returns.
func (f *Fake) SetInfo(i engine.Info) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.s.Info = i
}

func matches(labels map[string]string, filters []string) bool {
	for _, flt := range filters {
		k, v, hasValue := strings.Cut(flt, "=")
		got, ok := labels[k]
		if !ok || hasValue && got != v {
			return false
		}
	}
	return true
}

func (f *Fake) Info(context.Context) (engine.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Info"); err != nil {
		return engine.Info{}, err
	}
	return f.s.Info, nil
}

func (f *Fake) CreateVolume(_ context.Context, name string, labels map[string]string) (engine.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("CreateVolume"); err != nil {
		return engine.Volume{}, err
	}
	if v, ok := f.s.Volumes[name]; ok {
		if f.CreateVolumeHidesLabels {
			return engine.Volume{Name: name}, nil
		}
		return cloneVolume(v), nil
	}
	v := engine.Volume{Name: name, Labels: maps.Clone(labels)}
	f.s.Volumes[name] = v
	return cloneVolume(v), nil
}

func (f *Fake) InspectVolume(_ context.Context, name string) (engine.Volume, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("InspectVolume"); err != nil {
		return engine.Volume{}, false, err
	}
	v, ok := f.s.Volumes[name]
	return cloneVolume(v), ok, nil
}

func (f *Fake) ListVolumes(_ context.Context, labelFilters ...string) ([]engine.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ListVolumes"); err != nil {
		return nil, err
	}
	var out []engine.Volume
	for _, name := range slices.Sorted(maps.Keys(f.s.Volumes)) {
		if v := f.s.Volumes[name]; matches(v.Labels, labelFilters) {
			out = append(out, cloneVolume(v))
		}
	}
	return out, nil
}

func (f *Fake) RemoveVolume(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("RemoveVolume"); err != nil {
		return err
	}
	for id := range f.s.Containers {
		if slices.ContainsFunc(f.s.Specs[id].Mounts, func(m engine.Mount) bool { return m.Volume && m.Source == name }) {
			return fmt.Errorf("volume %s is in use: %w", name, engine.ErrConflict)
		}
	}
	delete(f.s.Volumes, name)
	return nil
}

func (f *Fake) ListContainers(_ context.Context, labelFilters ...string) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ListContainers"); err != nil {
		return nil, err
	}
	var out []engine.Container
	for _, id := range slices.Sorted(maps.Keys(f.s.Containers)) {
		if c := f.s.Containers[id]; matches(c.Labels, labelFilters) {
			c.Labels = maps.Clone(c.Labels)
			c.Ports = slices.Clone(c.Ports)
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *Fake) RemoveContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("RemoveContainer"); err != nil {
		return err
	}
	if c, ok := f.s.Containers[id]; ok {
		f.emit("destroy", c, nil)
	}
	delete(f.s.Containers, id)
	delete(f.s.Specs, id)
	delete(f.s.Aliases, id)
	delete(f.files, id)
	return nil
}

func (f *Fake) ListNetworks(_ context.Context, labelFilters ...string) ([]engine.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ListNetworks"); err != nil {
		return nil, err
	}
	var out []engine.Network
	for _, id := range slices.Sorted(maps.Keys(f.s.Networks)) {
		if n := f.s.Networks[id]; matches(n.Labels, labelFilters) {
			n.Labels = maps.Clone(n.Labels)
			out = append(out, n)
		}
	}
	return out, nil
}

func (f *Fake) RemoveNetwork(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("RemoveNetwork"); err != nil {
		return err
	}
	n, ok := f.findNetwork(id)
	if !ok {
		return nil
	}
	for _, c := range f.s.Containers {
		if slices.Contains(c.Networks, n.Name) {
			return fmt.Errorf("network %s has active endpoints (%s)", n.Name, c.Name)
		}
	}
	delete(f.s.Networks, n.ID)
	return nil
}

func cloneVolume(v engine.Volume) engine.Volume {
	v.Labels = maps.Clone(v.Labels)
	return v
}

// AddImage marks an image as present.
func (f *Fake) AddImage(ref string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.s.Images[ref] = true
}

// Spec returns the spec a container was created with.
func (f *Fake) Spec(id string) engine.ContainerSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.s.Specs[id]
}

func (f *Fake) findContainer(idOrName string) (engine.Container, bool) {
	if c, ok := f.s.Containers[idOrName]; ok {
		return c, true
	}
	for _, c := range f.s.Containers {
		if c.Name == idOrName {
			return c, true
		}
	}
	return engine.Container{}, false
}

func (f *Fake) findNetwork(idOrName string) (engine.Network, bool) {
	if n, ok := f.s.Networks[idOrName]; ok {
		return n, true
	}
	for _, n := range f.s.Networks {
		if n.Name == idOrName {
			return n, true
		}
	}
	return engine.Network{}, false
}

func (f *Fake) CreateNetwork(_ context.Context, name string, labels map[string]string) (engine.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("CreateNetwork"); err != nil {
		return engine.Network{}, err
	}
	if n, ok := f.findNetwork(name); ok {
		n.Labels = maps.Clone(n.Labels)
		return n, nil
	}
	n := engine.Network{ID: f.nextID("n"), Name: name, Labels: maps.Clone(labels)}
	f.s.Networks[n.ID] = n
	return n, nil
}

func (f *Fake) ConnectNetwork(_ context.Context, network, container string, aliases []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ConnectNetwork"); err != nil {
		return err
	}
	n, ok := f.findNetwork(network)
	if !ok {
		return fmt.Errorf("network %s not found", network)
	}
	c, ok := f.findContainer(container)
	if !ok {
		return fmt.Errorf("container %s not found", container)
	}
	if slices.Contains(c.Networks, n.Name) {
		return fmt.Errorf("container %s is already attached to %s", container, n.Name)
	}
	c.Networks = append(c.Networks, n.Name)
	slices.Sort(c.Networks)
	f.s.Containers[c.ID] = c
	if f.s.Aliases[c.ID] == nil {
		f.s.Aliases[c.ID] = map[string][]string{}
	}
	f.s.Aliases[c.ID][n.Name] = slices.Clone(aliases)
	return nil
}

func (f *Fake) DisconnectNetwork(_ context.Context, network, container string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("DisconnectNetwork"); err != nil {
		return err
	}
	n, okN := f.findNetwork(network)
	c, okC := f.findContainer(container)
	if !okN || !okC {
		return nil
	}
	c.Networks = slices.DeleteFunc(c.Networks, func(s string) bool { return s == n.Name })
	f.s.Containers[c.ID] = c
	delete(f.s.Aliases[c.ID], n.Name)
	return nil
}

func (f *Fake) ImageExists(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ImageExists"); err != nil {
		return false, err
	}
	return f.s.Images[ref], nil
}

func (f *Fake) PullImage(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("PullImage"); err != nil {
		return err
	}
	f.s.Images[ref] = true
	return nil
}

func (f *Fake) CreateContainer(_ context.Context, spec engine.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("CreateContainer"); err != nil {
		return "", err
	}
	if _, ok := f.findContainer(spec.Name); ok {
		return "", fmt.Errorf("container %s: %w", spec.Name, engine.ErrConflict)
	}
	if !f.s.Images[spec.Image] {
		return "", fmt.Errorf("no such image: %s", spec.Image)
	}
	id := f.nextID("c")
	ports := slices.Clone(spec.Ports)
	for i := range ports {
		if ports[i].HostPort == 0 { // like Docker: a random free port
			ports[i].HostPort = uint16(49000 + f.n%10000) //nolint:gosec // stays within 49000-58999
		}
	}
	f.s.Containers[id] = engine.Container{ID: id, Name: spec.Name, State: "created", Labels: maps.Clone(spec.Labels), Ports: ports, Networks: slices.Clone(spec.Networks)}
	f.s.Specs[id] = spec
	f.emit("create", f.s.Containers[id], nil)
	return id, nil
}

func (f *Fake) StartContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("StartContainer"); err != nil {
		return err
	}
	c, ok := f.findContainer(id)
	if !ok {
		return fmt.Errorf("container %s not found", id)
	}
	c.State = "running"
	f.s.Containers[c.ID] = c
	f.emit("start", c, nil)
	return nil
}

func (f *Fake) InspectContainer(_ context.Context, id string) (engine.ContainerDetails, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("InspectContainer"); err != nil {
		return engine.ContainerDetails{}, false, err
	}
	c, ok := f.findContainer(id)
	if !ok {
		return engine.ContainerDetails{}, false, nil
	}
	d := engine.ContainerDetails{ID: c.ID, Name: c.Name, State: c.State, Labels: maps.Clone(c.Labels), Networks: map[string][]string{}, IPs: map[string]string{}, Mounts: slices.Clone(f.mounts[c.ID]), Runtime: f.runtime[c.ID]}
	for i, n := range c.Networks {
		d.Networks[n] = slices.Clone(f.s.Aliases[c.ID][n])
		if c.State == "running" {
			d.IPs[n] = fmt.Sprintf("10.%d.0.%d", i, len(c.ID))
		}
	}
	return d, true, nil
}

// SetRuntime sets how a container last ran, as InspectContainer reports it.
func (f *Fake) SetRuntime(id string, r engine.Runtime) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runtime == nil {
		f.runtime = map[string]engine.Runtime{}
	}
	f.runtime[id] = r
}

// Files returns what was copied into a container.
func (f *Fake) Files(id string) map[string][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, _ := f.findContainer(id)
	return maps.Clone(f.files[c.ID])
}

func (f *Fake) CopyToContainer(_ context.Context, id string, files map[string][]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("CopyToContainer"); err != nil {
		return err
	}
	c, ok := f.findContainer(id)
	if !ok {
		return fmt.Errorf("container %s not found", id)
	}
	if f.files == nil {
		f.files = map[string]map[string][]byte{}
	}
	if f.files[c.ID] == nil {
		f.files[c.ID] = map[string][]byte{}
	}
	for p, b := range files {
		f.files[c.ID][p] = slices.Clone(b)
	}
	return nil
}

func (f *Fake) NetworkContainers(_ context.Context, network string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("NetworkContainers"); err != nil {
		return nil, err
	}
	n, ok := f.findNetwork(network)
	if !ok {
		return nil, nil
	}
	var ids []string
	for _, id := range slices.Sorted(maps.Keys(f.s.Containers)) {
		c := f.s.Containers[id]
		// Like Docker: only containers with a live endpoint, i.e. running or paused ones.
		if slices.Contains(c.Networks, n.Name) && (c.State == "running" || c.State == "paused") {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// SetMounts sets the mounts InspectContainer reports for a container.
func (f *Fake) SetMounts(id string, mounts []engine.Mount) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mounts == nil {
		f.mounts = map[string][]engine.Mount{}
	}
	f.mounts[id] = mounts
}

// Runs are the specs of all one-off containers run so far.
func (f *Fake) Runs() []engine.ContainerSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

func (f *Fake) RunContainer(_ context.Context, spec engine.ContainerSpec) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("RunContainer"); err != nil {
		return 0, err
	}
	if !f.s.Images[spec.Image] {
		return 0, fmt.Errorf("no such image: %s", spec.Image)
	}
	f.runs = append(f.runs, spec)
	return f.RunExit, nil
}

func (f *Fake) PauseContainer(_ context.Context, id string) error {
	return f.setState("PauseContainer", id, "paused")
}

func (f *Fake) UnpauseContainer(_ context.Context, id string) error {
	return f.setState("UnpauseContainer", id, "running")
}

func (f *Fake) setState(method, id, state string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail(method); err != nil {
		return err
	}
	c, ok := f.findContainer(id)
	if !ok {
		return fmt.Errorf("container %s not found", id)
	}
	c.State = state
	f.s.Containers[c.ID] = c
	f.emit(map[string]string{"PauseContainer": "pause", "UnpauseContainer": "unpause"}[method], c, nil)
	return nil
}

func (f *Fake) Events(ctx context.Context, labelFilters ...string) (<-chan engine.Event, <-chan error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	errs := make(chan error, 1)
	if err := f.fail("Events"); err != nil {
		ch := make(chan engine.Event)
		close(ch)
		errs <- err
		return ch, errs
	}
	sub := &subscriber{filters: labelFilters, ch: make(chan engine.Event, 256)}
	if f.subs == nil {
		f.subs = map[*subscriber]bool{}
	}
	f.subs[sub] = true
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.subs, sub)
		close(sub.ch)
	}()
	return sub.ch, errs
}

// Subscribers returns how many Events streams are open; tests wait for a command to listen.
func (f *Fake) Subscribers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs)
}

// Emit sends an event about a container to the Events streams, as Docker would; for events
// the fake does not make itself, such as "health_status: healthy".
func (f *Fake) Emit(id, action string, attrs map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.s.Containers[id]; ok {
		f.emit(action, c, attrs)
	}
}

// emit sends an event to the matching streams; the caller holds f.mu. A stream whose reader
// fell 256 events behind misses events, which is what tests deserve for not reading.
func (f *Fake) emit(action string, c engine.Container, extra map[string]string) {
	attrs := maps.Clone(c.Labels)
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs["name"] = c.Name
	maps.Copy(attrs, extra)
	e := engine.Event{Time: time.Now(), Action: action, ID: c.ID, Name: c.Name, Attributes: attrs}
	for sub := range f.subs {
		if matches(c.Labels, sub.filters) {
			select {
			case sub.ch <- e:
			default:
			}
		}
	}
}

var _ engine.Engine = (*Fake)(nil)
