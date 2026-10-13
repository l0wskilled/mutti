package project

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/l0wskilled/mutti/internal/compose"
)

// Draft is the x-mutti block mutti init writes: the main service and the overlays, plus the
// default routes and suggested fixed ports as comments to edit from.
type Draft struct {
	Main     string              `json:"main,omitempty"`
	Routes   map[string]uint16   `json:"routes,omitempty"`   // service -> container port
	Overlays map[string][]string `json:"overlays,omitempty"` // name -> files relative to the project dir
	Ports    map[string][]uint16 `json:"ports,omitempty"`    // suggested fixed ports, written commented out
}

// overlayFile matches compose.<name>.yaml and docker-compose.<name>.yml and their variants.
var overlayFile = regexp.MustCompile(`^(?:docker-)?compose\.([a-z0-9][a-z0-9_-]*)\.ya?ml$`)

// NewDraft derives the draft from the project's model and the Compose files next to it.
func NewDraft(m *compose.Model, files Files) (Draft, error) {
	var cfg Config
	d := Draft{Main: cfg.MainService(m), Routes: map[string]uint16{}, Overlays: map[string][]string{}, Ports: map[string][]uint16{}}
	for _, r := range cfg.HTTPRoutes(m, "") {
		d.Routes[r.Service] = r.Port
	}
	for _, name := range m.ServiceNames() {
		if _, routed := d.Routes[name]; routed {
			continue
		}
		for _, p := range m.Services[name].Ports {
			if p.Protocol != "udp" && nonHTTPPorts[p.Target] && !slices.Contains(d.Ports[name], p.Target) {
				d.Ports[name] = append(d.Ports[name], p.Target)
			}
		}
	}
	entries, err := os.ReadDir(files.Dir)
	if err != nil {
		return Draft{}, err
	}
	for _, e := range entries {
		match := overlayFile.FindStringSubmatch(e.Name())
		if match == nil || match[1] == "override" || slices.Contains(files.Paths, filepath.Join(files.Dir, e.Name())) {
			continue
		}
		d.Overlays[match[1]] = append(d.Overlays[match[1]], e.Name())
	}
	return d, nil
}

// Render returns the draft as a top-level YAML block, ending in a newline.
func (d Draft) Render() []byte {
	var b strings.Builder
	b.WriteString("# mutti: every key is optional.\n")
	b.WriteString("# Reference: https://github.com/l0wskilled/mutti/blob/main/docs/reference/x-mutti.md\n")
	b.WriteString("x-mutti:\n")
	if d.Main != "" {
		fmt.Fprintf(&b, "  main: %s\n", scalar(d.Main))
	}
	if len(d.Routes) > 0 {
		// Commented out: written down, routes replace the defaults, and a service added later
		// would get none.
		b.WriteString("  # HTTP routes mutti sets up now; listing routes replaces these defaults:\n")
		b.WriteString("  # routes:\n")
		for _, s := range slices.Sorted(maps.Keys(d.Routes)) {
			fmt.Fprintf(&b, "  #   %s: { port: %d }\n", scalar(s), d.Routes[s])
		}
	}
	if len(d.Ports) > 0 {
		b.WriteString("  # A fixed host port per worktree, for database tools (default: a random one):\n")
		b.WriteString("  # ports:\n")
		for _, s := range slices.Sorted(maps.Keys(d.Ports)) {
			fmt.Fprintf(&b, "  #   %s: %s\n", scalar(s), portList(d.Ports[s]))
		}
	}
	if len(d.Overlays) > 0 {
		b.WriteString("  overlays:\n")
		for _, name := range slices.Sorted(maps.Keys(d.Overlays)) {
			files := make([]string, len(d.Overlays[name]))
			for i, f := range d.Overlays[name] {
				files[i] = scalar(f)
			}
			fmt.Fprintf(&b, "    %s: [%s]\n", scalar(name), strings.Join(files, ", "))
		}
	}
	return []byte(b.String())
}

func portList(ps []uint16) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = fmt.Sprint(p)
	}
	return "[" + strings.Join(s, ", ") + "]"
}

// scalar quotes s where YAML would read it as something other than that string.
func scalar(s string) string {
	out, _ := yaml.Marshal(s) // a string always marshals
	return strings.TrimSuffix(string(out), "\n")
}

// ErrNoPlace means a Compose file has no top-level block mapping to put x-mutti into.
var ErrNoPlace = errors.New("no top-level key to put x-mutti before")

// topLevelKey is a block mapping key at column 0.
var topLevelKey = regexp.MustCompile(`^[A-Za-z0-9_"'-][^:#]*:(\s|$)`)

// Insert puts block before the first top-level key of a Compose file, leaving every other byte
// as it was: comments, blank lines and formatting are the user's.
func Insert(src, block []byte) ([]byte, error) {
	lines := bytes.SplitAfter(src, []byte("\n"))
	for i, line := range lines {
		if topLevelKey.Match(line) {
			var out bytes.Buffer
			for _, l := range lines[:i] {
				out.Write(l)
			}
			out.Write(block)
			out.WriteString("\n")
			for _, l := range lines[i:] {
				out.Write(l)
			}
			return out.Bytes(), nil
		}
	}
	return nil, ErrNoPlace
}
