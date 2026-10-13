package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/identity"
)

// Config is the x-mutti block of a Compose file. Every field is optional.
type Config struct {
	// The project's name, part of every hostname. Default: the main worktree's directory name.
	Name string `json:"name,omitempty"`
	// The service behind the instance's main hostname. Default: the service publishing or
	// exposing the most typical web port (80, 3000, 4200, 5173, 8000, 8080).
	Main string `json:"main,omitempty"`
	// HTTP routes per service. Default: the main service on the main hostname, every other
	// service publishing or exposing an HTTP port on <service>.<main hostname>.
	Routes map[string]Route `json:"routes,omitempty"`
	// Container ports to publish on a fixed host port, per service, for tools that keep a
	// connection setting, such as database GUIs. The port follows from instance, service and
	// port, so it stays the same across restarts.
	Ports map[string][]uint16 `json:"ports,omitempty"`
	// Settings for mutti worktree add.
	Worktree WorktreeConfig `json:"worktree,omitzero"`
	// How mutti exec, mutti shell and hooks run commands.
	Exec Exec `json:"exec,omitzero"`
	// Commands run inside services at points of an instance's life.
	Hooks Hooks `json:"hooks,omitzero"`
	// Named sets of extra Compose files, enabled with --overlay <name>. Without --overlay, commands
	// other than up and run use the overlays the instance was last started with.
	Overlays map[string][]string `json:"overlays,omitempty"`
}

// Route is how the router reaches one service.
type Route struct {
	// Subdomain under the main hostname; "" is the main hostname itself. Default: the service
	// name, or "" for the main service.
	Host *string `json:"host,omitempty"`
	// Path prefix on the main hostname that also leads to the service, e.g. /api.
	Path string `json:"path,omitempty" jsonschema:"pattern=^/[A-Za-z0-9._~/-]*$"`
	// Container port to send traffic to. Default: the service's first published TCP port, else
	// its first exposed one.
	Port uint16 `json:"port,omitempty"`
}

// WorktreeConfig configures new worktrees.
type WorktreeConfig struct {
	// Files and directories (globs allowed) that git does not track but a worktree needs, such
	// as .env.local; copied from the main worktree into new ones, never overwriting.
	Copy []string `json:"copy,omitempty"`
}

// Exec configures command execution inside services.
type Exec struct {
	// "host" (default): the host user's uid:gid, so files in bind mounts belong to you;
	// "service": the image's user; or an explicit user[:group].
	User string `json:"user,omitempty"`
	// $HOME for commands run as the host user, who has no home in the image. Default /tmp.
	Home string `json:"home,omitempty"`
}

// Hooks are commands run inside services.
type Hooks struct {
	// Once per instance, after its first successful up.
	Bootstrap []Hook `json:"bootstrap,omitempty"`
	// After every up.
	PostUp []Hook `json:"post-up,omitempty"`
	// Before down, destroy and reset; a failure is reported but does not stop the teardown.
	PreDown []Hook `json:"pre-down,omitempty"`
	// Instead of bootstrap when the data was copied from another instance
	// (worktree add --data-from): the data needs no setup, the worktree may.
	AfterClone []Hook `json:"after-clone,omitempty"`
	// On mutti sweep, with MUTTI_SWEEP_OLDER_THAN in seconds: remove per-run resources the
	// project made inside services, such as test databases named after run ids.
	Sweep []Hook `json:"sweep,omitempty"`
}

// Hook is one command.
type Hook struct {
	// Service to run the command in; it must be running.
	Service string `json:"service"`
	// Shell command, run with sh -c inside the service.
	Run string `json:"run"`
	// Like exec.user; empty means exec.user.
	User string `json:"user,omitempty"`
}

// User values with a special meaning.
const (
	UserHost    = "host"
	UserService = "service"
)

// ParseConfig reads and validates the x-mutti block of a model. Unknown keys are errors, so a
// typo never silently does nothing.
func ParseConfig(m *compose.Model) (Config, error) {
	var c Config
	if len(m.XMutti) > 0 && !bytes.Equal(m.XMutti, []byte("null")) {
		// `docker compose config` re-escapes literal "$" as "$$" so its output stays loadable;
		// x-mutti values are used as they are, so undo that.
		raw := bytes.ReplaceAll(m.XMutti, []byte("$$"), []byte("$"))
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return Config{}, fmt.Errorf("x-mutti: %w", err)
		}
	}
	if c.Exec.User == "" {
		c.Exec.User = UserHost
	}
	if c.Exec.Home == "" {
		c.Exec.Home = "/tmp"
	}
	return c, c.validate(m)
}

// routePath is what a route's path prefix may hold: it ends up in a router rule, so nothing
// that could end the quoted string there or split the list it travels in as a label.
var routePath = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)

// ValidRoutePath reports whether p can be a route's path prefix.
func ValidRoutePath(p string) bool { return routePath.MatchString(p) }

// overlayName is what an overlay name may hold; it becomes part of a file name.
var overlayName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (c Config) validate(m *compose.Model) error {
	var errs []string
	if c.Name != "" && !identity.Valid(c.Name) {
		errs = append(errs, fmt.Sprintf("name %q must be a DNS label (lower case letters, digits, '-')", c.Name))
	}
	known := m.ServiceNames()
	service := func(field, name string) {
		if !slices.Contains(known, name) {
			errs = append(errs, fmt.Sprintf("%s: unknown service %q (services: %s)", field, name, strings.Join(known, ", ")))
		}
	}
	if c.Main != "" {
		service("main", c.Main)
	}
	for _, name := range slices.Sorted(maps.Keys(c.Routes)) {
		r := c.Routes[name]
		service("routes."+name, name)
		if r.Host != nil && *r.Host != "" && !identity.Valid(*r.Host) {
			errs = append(errs, fmt.Sprintf("routes.%s.host %q must be a DNS label", name, *r.Host))
		}
		if r.Path != "" && !ValidRoutePath(r.Path) {
			errs = append(errs, fmt.Sprintf("routes.%s.path %q must start with / and hold only letters, digits and ./_~-", name, r.Path))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Ports)) {
		service("ports."+name, name)
		if slices.Contains(c.Ports[name], 0) {
			errs = append(errs, fmt.Sprintf("ports.%s: 0 is not a port", name))
		}
	}
	for kind, hooks := range map[string][]Hook{"bootstrap": c.Hooks.Bootstrap, "post-up": c.Hooks.PostUp, "pre-down": c.Hooks.PreDown, "after-clone": c.Hooks.AfterClone, "sweep": c.Hooks.Sweep} {
		for i, h := range hooks {
			field := fmt.Sprintf("hooks.%s[%d]", kind, i)
			service(field+".service", h.Service)
			if strings.TrimSpace(h.Run) == "" {
				errs = append(errs, field+".run is empty")
			}
		}
	}
	for _, pattern := range c.Worktree.Copy {
		if _, err := filepath.Match(pattern, ""); err != nil || filepath.IsAbs(pattern) || strings.HasPrefix(filepath.Clean(pattern), "..") {
			errs = append(errs, fmt.Sprintf("worktree.copy: %q must be a relative path or glob inside the worktree", pattern))
		}
	}
	for name, files := range c.Overlays {
		if !overlayName.MatchString(name) {
			errs = append(errs, fmt.Sprintf("overlays: name %q must be lower case letters, digits, '-' and '_'", name))
		}
		if len(files) == 0 {
			errs = append(errs, fmt.Sprintf("overlays.%s lists no files", name))
		}
	}
	if len(errs) > 0 {
		slices.Sort(errs)
		return fmt.Errorf("x-mutti: %s", strings.Join(errs, "; "))
	}
	return nil
}
