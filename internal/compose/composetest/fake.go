// Package composetest provides a compose.Runner that simulates `docker compose` on top of the
// in-memory engine from enginetest.
package composetest

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/engine/enginetest"
)

// Fake simulates Compose: up creates one running container per service, a network and a named
// volume; stop exits the containers; down removes them (and the volumes with --volumes).
type Fake struct {
	Eng        *enginetest.Fake
	Ver        compose.Version
	VersionErr error
	BuildxErr  error
	// ConfigFn returns the model for a spec.
	ConfigFn func(compose.Spec) (*compose.Model, error)
	// Exit returns the exit code of a Run; nil means 0.
	Exit func(args []string) int
	// Stderr returns what a failing Run prints to stderr.
	Stderr func(args []string) string
	// Stdout returns what a Run prints to stdout, such as a hook's output.
	Stdout func(args []string) string
	RunErr error
	Runs   [][]string
	Specs  []compose.Spec
	port   uint16
}

// New returns a Fake that serves model for every spec.
func New(eng *enginetest.Fake, model *compose.Model) *Fake {
	return &Fake{
		Eng:      eng,
		Ver:      compose.MustParseVersion("2.30.0"),
		ConfigFn: func(compose.Spec) (*compose.Model, error) { return model, nil },
		port:     40000,
	}
}

func (f *Fake) Version(context.Context) (compose.Version, error) {
	return f.Ver, f.VersionErr
}

func (f *Fake) BuildxVersion(context.Context) (string, error) {
	return "buildx 0.30.0", f.BuildxErr
}

func (f *Fake) Config(_ context.Context, s compose.Spec) (*compose.Model, error) {
	f.Specs = append(f.Specs, s)
	return f.ConfigFn(s)
}

func (f *Fake) Run(ctx context.Context, s compose.Spec, io compose.IO, args ...string) (int, error) {
	f.Runs = append(f.Runs, args)
	f.Specs = append(f.Specs, s)
	if f.RunErr != nil {
		return 0, f.RunErr
	}
	if f.Stdout != nil {
		fmt.Fprint(io.Out, f.Stdout(args))
	}
	if f.Exit != nil {
		if code := f.Exit(args); code != 0 {
			if f.Stderr != nil {
				fmt.Fprint(io.Err, f.Stderr(args))
			}
			return code, nil
		}
	}
	project := engine.LabelComposeProject + "=" + s.Project
	switch args[0] {
	case "up":
		m, _ := f.ConfigFn(s)
		o, err := readOverride(s.Files)
		if err != nil {
			return 0, err
		}
		for _, n := range o.external {
			if nets, _ := f.Eng.ListNetworks(ctx); !slices.ContainsFunc(nets, func(x engine.Network) bool { return x.Name == n }) {
				fmt.Fprintf(io.Err, "network %s declared as external, but could not be found\n", n)
				return 1, nil
			}
		}
		// Recreate everything, as Compose does when the configuration changed.
		existing, _ := f.Eng.ListContainers(ctx, project)
		for _, c := range existing {
			_ = f.Eng.RemoveContainer(ctx, c.ID)
		}
		_, _ = f.Eng.CreateNetwork(ctx, s.Project+"_default", map[string]string{engine.LabelComposeProject: s.Project})
		// Named services start alone: the fake knows no dependencies.
		// Like Compose: a service behind a profile starts only with that profile, or when named.
		names := slices.DeleteFunc(m.ServiceNames(), func(name string) bool { return disabled(m, s, name) })
		if named := slices.DeleteFunc(slices.Clone(args[1:]), func(a string) bool { return strings.HasPrefix(a, "-") }); len(named) > 0 {
			names = named
		}
		for _, name := range names {
			var ports []engine.Port
			seen := map[uint16]bool{}
			for _, p := range m.Services[name].Ports {
				proto := p.Protocol
				if proto == "" {
					proto = "tcp"
				}
				host, ok := o.fixed[name][p.Target]
				if !ok || proto != "tcp" {
					f.port++
					host = f.port
				}
				seen[p.Target] = true
				ports = append(ports, engine.Port{HostIP: compose.LoopbackIP, HostPort: host, ContainerPort: p.Target, Proto: proto})
			}
			for _, target := range slices.Sorted(maps.Keys(o.fixed[name])) {
				if !seen[target] {
					ports = append(ports, engine.Port{HostIP: compose.LoopbackIP, HostPort: o.fixed[name][target], ContainerPort: target, Proto: "tcp"})
				}
			}
			labels := map[string]string{engine.LabelComposeProject: s.Project, engine.LabelComposeService: name}
			maps.Copy(labels, o.labels[name])
			id := f.Eng.AddContainer(engine.Container{
				Name:   s.Project + "-" + name + "-1",
				State:  "running",
				Status: "Up 1 second",
				Labels: labels,
				Ports:  ports,
			})
			for _, n := range append([]string{s.Project + "_default"}, o.external...) {
				_ = f.Eng.ConnectNetwork(ctx, n, id, nil)
			}
		}
		_, _ = f.Eng.CreateVolume(ctx, s.Project+"_data", map[string]string{engine.LabelComposeProject: s.Project, "com.docker.compose.volume": "data"})
	case "stop", "restart":
		state := map[string]string{"stop": "exited", "restart": "running"}[args[0]]
		for _, c := range f.enabledContainers(ctx, s) {
			if named := args[1:]; len(named) == 0 || slices.Contains(named, c.Labels[engine.LabelComposeService]) {
				f.Eng.SetContainerState(c.ID, state)
			}
		}
	case "down":
		for _, c := range f.enabledContainers(ctx, s) {
			_ = f.Eng.RemoveContainer(ctx, c.ID)
		}
		ns, _ := f.Eng.ListNetworks(ctx, project)
		for _, n := range ns {
			_ = f.Eng.RemoveNetwork(ctx, n.ID)
		}
		if slices.Contains(args, "--volumes") {
			vs, _ := f.Eng.ListVolumes(ctx, project)
			for _, v := range vs {
				_ = f.Eng.RemoveVolume(ctx, v.Name)
			}
		}
	}
	return 0, nil
}

// disabled reports whether a profile keeps the service out of the spec, as in Compose.
func disabled(m *compose.Model, s compose.Spec, name string) bool {
	p := m.Services[name].Profiles
	return len(p) > 0 && !slices.Contains(s.Profiles, "*") && !slices.ContainsFunc(p, func(x string) bool { return slices.Contains(s.Profiles, x) })
}

// enabledContainers are the project's containers that Compose's stop and down reach: a service
// behind a profile the spec does not enable is left alone, even when it runs.
func (f *Fake) enabledContainers(ctx context.Context, s compose.Spec) []engine.Container {
	m, _ := f.ConfigFn(s)
	cs, _ := f.Eng.ListContainers(ctx, engine.LabelComposeProject+"="+s.Project)
	return slices.DeleteFunc(cs, func(c engine.Container) bool { return disabled(m, s, c.Labels[engine.LabelComposeService]) })
}

// override is what the fake reads from mutti's generated override file.
type override struct {
	labels   map[string]map[string]string // per service
	external []string                     // names of external networks
	fixed    map[string]map[uint16]uint16 // per service: container port -> published host port
}

// readOverride reads the generated override among files (the one under a "mutti" state dir).
func readOverride(files []string) (override, error) {
	o := override{labels: map[string]map[string]string{}, fixed: map[string]map[uint16]uint16{}}
	for _, path := range files {
		if !strings.Contains(path, "/instances/") {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return o, err
		}
		var doc struct {
			Services map[string]struct {
				Labels map[string]string `yaml:"labels"`
				Ports  []struct {
					Target    uint16 `yaml:"target"`
					Published string `yaml:"published"`
				} `yaml:"ports"`
			} `yaml:"services"`
			Networks map[string]struct {
				Name     string `yaml:"name"`
				External bool   `yaml:"external"`
			} `yaml:"networks"`
		}
		var node yaml.Node
		if err := yaml.Unmarshal(b, &node); err != nil {
			return o, err
		}
		stripTags(&node)
		if err := node.Decode(&doc); err != nil {
			return o, err
		}
		for name, svc := range doc.Services {
			o.labels[name] = svc.Labels
			for _, p := range svc.Ports {
				if p.Published == "" {
					continue
				}
				port, _ := strconv.ParseUint(p.Published, 10, 16)
				if o.fixed[name] == nil {
					o.fixed[name] = map[uint16]uint16{}
				}
				o.fixed[name][p.Target] = uint16(port)
			}
		}
		for _, n := range doc.Networks {
			if n.External {
				o.external = append(o.external, n.Name)
			}
		}
	}
	return o, nil
}

// stripTags drops Compose's custom tags (!override, !reset) so the YAML decodes plainly.
func stripTags(n *yaml.Node) {
	if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
		n.Tag = ""
	}
	for _, c := range n.Content {
		stripTags(c)
	}
}

// RunsWith returns the recorded runs whose first argument is cmd.
func (f *Fake) RunsWith(cmd string) [][]string {
	var out [][]string
	for _, r := range f.Runs {
		if r[0] == cmd {
			out = append(out, r)
		}
	}
	return out
}

var _ compose.Runner = (*Fake)(nil)
