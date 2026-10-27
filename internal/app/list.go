package app

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/identity"
	"github.com/l0wskilled/mutti/internal/lock"
)

// Status of an instance's containers.
const (
	StatusRunning = "running" // all containers run
	StatusPartial = "partial" // some containers run
	StatusStopped = "stopped" // containers exist, none runs
	StatusDown    = "down"    // no containers
)

// Summary describes one instance on the machine.
type Summary struct {
	Name     string        `json:"name"`
	Project  string        `json:"project"`
	Path     string        `json:"path"` // where its Compose project lives, a subdirectory of its worktree or the worktree
	Branch   string        `json:"branch,omitempty"`
	Status   string        `json:"status"`
	Running  int           `json:"running"`
	Total    int           `json:"total"`
	Orphaned bool          `json:"orphaned"`       // the worktree is gone or holds another identity
	URLs     []string      `json:"urls,omitempty"` // routed URLs, the main hostname first
	Ports    []PortSummary `json:"ports,omitempty"`
}

// PortSummary is a published port of a service.
type PortSummary struct {
	Service       string `json:"service"`
	Address       string `json:"address"` // host:port, with "/udp" for UDP
	ContainerPort uint16 `json:"containerPort"`
	Routed        bool   `json:"routed"` // the service is also reachable through the router
}

// List returns every instance on the machine, sorted by project and name. It reads all state
// with two daemon calls.
func (a *App) List(ctx context.Context) ([]Summary, error) {
	claims, err := a.Engine.ListVolumes(ctx, engine.LabelRole+"="+RoleClaim, engine.LabelSchema)
	if err != nil {
		return nil, engineErr(err)
	}
	containers, err := a.Engine.ListContainers(ctx, engine.LabelComposeProject)
	if err != nil {
		return nil, engineErr(err)
	}
	byProject := map[string][]engine.Container{}
	for _, c := range containers {
		p := c.Labels[engine.LabelComposeProject]
		byProject[p] = append(byProject[p], c)
	}

	s, err := a.routerSettings()
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(claims))
	for _, v := range claims {
		out = append(out, a.summarize(ctx, s.HTTPSPort, v, byProject[v.Labels[engine.LabelInstance]]))
	}
	slices.SortFunc(out, func(x, y Summary) int {
		return cmp.Or(cmp.Compare(x.Project, y.Project), cmp.Compare(x.Name, y.Name))
	})
	return out, nil
}

func (a *App) summarize(ctx context.Context, httpsPort uint16, claim engine.Volume, containers []engine.Container) Summary {
	root, owner := a.ownerRoot(ctx, claim)
	s := Summary{
		Name:     claim.Labels[engine.LabelInstance],
		Project:  claim.Labels[engine.LabelProject],
		Path:     root,
		Orphaned: owner == ownerGone,
	}
	if dir := claim.Labels[LabelDir]; dir != "" && owner != ownerGone {
		s.Path = filepath.Join(root, filepath.FromSlash(dir))
	}
	if owner != ownerGone {
		if b, err := a.Git.Branch(ctx, root); err == nil {
			s.Branch = b
		}
	}

	urls := map[string]bool{}
	main := ""
	done := settled(containers)
	for _, c := range containers {
		if !done[c.ID] {
			s.Total++
		}
		if c.State == "running" {
			s.Running++
		}
		routed := c.Labels[LabelRoutePort] != ""
		for _, h := range splitList(c.Labels[LabelRouteHosts]) {
			u := hostURL(h, httpsPort)
			urls[u] = true
			if h == c.Labels[LabelHostname] {
				main = u
			}
		}
		for _, p := range c.Ports {
			addr := fmt.Sprintf("%s:%d", p.HostIP, p.HostPort)
			if p.Proto == "udp" {
				addr += "/udp"
			}
			s.Ports = append(s.Ports, PortSummary{
				Service:       c.Labels[engine.LabelComposeService],
				Address:       addr,
				ContainerPort: p.ContainerPort,
				Routed:        routed && p.Proto != "udp",
			})
		}
	}
	if main != "" {
		s.URLs = append(s.URLs, main)
	}
	for _, u := range slices.Sorted(maps.Keys(urls)) {
		if u != main {
			s.URLs = append(s.URLs, u)
		}
	}
	slices.SortFunc(s.Ports, func(x, y PortSummary) int {
		return cmp.Or(cmp.Compare(x.Service, y.Service), cmp.Compare(x.ContainerPort, y.ContainerPort))
	})
	s.Status = statusOf(s.Running, s.Total)
	return s
}

func statusOf(running, total int) string {
	switch {
	case total == 0:
		return StatusDown
	case running == total:
		return StatusRunning
	case running == 0:
		return StatusStopped
	default:
		return StatusPartial
	}
}

// hostURL is the HTTPS URL of a hostname through the router.
func hostURL(host string, httpsPort uint16) string {
	if httpsPort == 443 {
		return "https://" + host
	}
	return fmt.Sprintf("https://%s:%d", host, httpsPort)
}

// TidyOptions configure Tidy.
type TidyOptions struct {
	Project string        // only this project's instances; empty: all
	Idle    time.Duration // also stop running instances unused for this long; 0: don't
	Apply   bool          // act; otherwise only report
}

// TidyResult lists what Tidy removed or stopped (or would).
type TidyResult struct {
	Removed []Summary `json:"removed"` // worktree gone or holding another identity
	Stopped []Summary `json:"stopped"` // running but idle
}

// Tidy removes instances whose worktree is gone or no longer holds their identity, and with
// Idle stops running instances nobody used for that long. An instance counts as used by every
// mutti command on it and by git activity in its worktree; browsing it does not count.
func (a *App) Tidy(ctx context.Context, opts TidyOptions) (*TidyResult, error) {
	all, err := a.List(ctx)
	if err != nil {
		return nil, err
	}
	res := &TidyResult{Removed: []Summary{}, Stopped: []Summary{}}
	for _, s := range all {
		if opts.Project != "" && s.Project != opts.Project {
			continue
		}
		switch {
		case s.Orphaned:
			if opts.Apply {
				if err := a.purge(ctx, s.Name); err != nil {
					return res, err
				}
			}
			res.Removed = append(res.Removed, s)
		case opts.Idle > 0 && s.Running > 0 && a.Now().Sub(a.lastUsed(ctx, s)) > opts.Idle:
			if opts.Apply {
				if _, err := a.Stop(ctx, s.Path, Selection{}); err != nil {
					return res, err
				}
			}
			res.Stopped = append(res.Stopped, s)
		}
	}
	if opts.Apply && len(res.Removed) > 0 {
		a.refreshRouter(ctx)
	}
	return res, nil
}

// Remove removes everything of the named instance from Docker and from mutti's state: its
// containers, networks, volumes and claim. It works by labels alone, so it needs neither the
// worktree nor Compose: it removes what a deleted worktree or a broken project left behind.
func (a *App) Remove(ctx context.Context, name string) error {
	if err := a.purge(ctx, name); err != nil {
		return err
	}
	a.refreshRouter(ctx)
	return nil
}

// lastUsed is the latest of the last mutti command on an instance and the last git activity
// in its worktree.
func (a *App) lastUsed(ctx context.Context, s Summary) time.Time {
	var last time.Time
	paths := []string{a.lastUsedFile(s.Name)}
	if info, ok, err := a.Git.Find(ctx, s.Path); err == nil && ok {
		paths = append(paths, filepath.Join(info.GitDir, "index"), filepath.Join(info.GitDir, "HEAD"), filepath.Join(info.GitDir, "logs", "HEAD"))
	}
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(last) {
			last = fi.ModTime()
		}
	}
	return last
}

// purge removes everything of an instance by label, in dependency order: containers, networks,
// volumes, then mutti's markers. The claim goes last, so an interrupted purge can be retried.
func (a *App) purge(ctx context.Context, instance string) error {
	if !identity.Valid(instance) {
		return fmt.Errorf("refusing to purge %q: not an instance name", instance)
	}
	project := engine.LabelComposeProject + "=" + instance
	containers, err := a.Engine.ListContainers(ctx, project)
	if err != nil {
		return engineErr(err)
	}
	for _, c := range containers {
		if err := a.Engine.RemoveContainer(ctx, c.ID); err != nil {
			return engineErr(err)
		}
	}
	if err := a.removeNetworks(ctx, instance); err != nil {
		return err
	}
	volumes, err := a.Engine.ListVolumes(ctx, project)
	if err != nil {
		return engineErr(err)
	}
	for _, name := range append(volumeNames(volumes), bootstrapVolume(instance), claimVolume(instance)) {
		if err := a.Engine.RemoveVolume(ctx, name); err != nil {
			return engineErr(err)
		}
	}
	return os.RemoveAll(a.Dirs.InstanceDir(instance))
}

// removeNetworks removes the networks of an instance. It holds the router lock, so a Reconcile
// running meanwhile cannot attach the router again between detaching and removing.
func (a *App) removeNetworks(ctx context.Context, instance string) error {
	release, err := lock.Acquire(ctx, a.Dirs.LockFile("router"))
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	networks, err := a.Engine.ListNetworks(ctx, engine.LabelComposeProject+"="+instance)
	if err != nil {
		return engineErr(err)
	}
	// The ingress network is mutti's: the router and attached containers have to let go first.
	if err := a.detachAll(ctx, instance); err != nil {
		return err
	}
	ingress, err := a.Engine.ListNetworks(ctx, engine.LabelInstance+"="+instance, engine.LabelRole+"="+RoleIngress)
	if err != nil {
		return engineErr(err)
	}
	for _, n := range append(networks, ingress...) {
		if err := a.Engine.RemoveNetwork(ctx, n.ID); err != nil {
			return engineErr(err)
		}
	}
	return nil
}

func volumeNames(vs []engine.Volume) []string {
	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = v.Name
	}
	return names
}

// labelDependsOn is where Compose notes a container's depends_on: service:condition:restart, ...
const labelDependsOn = "com.docker.compose.depends_on"

// LabelOptional marks a service behind a Compose profile, which only runs when asked for.
const LabelOptional = "mutti.optional"

// settled returns the ids of containers that count neither as running nor as missing: one-shot
// services that did their job, and optional services that do not run.
func settled(containers []engine.Container) map[string]bool {
	done := completed(containers)
	for _, c := range containers {
		if c.Labels[LabelOptional] == "true" && c.State != "running" {
			done[c.ID] = true
		}
	}
	return done
}

// completed returns the ids of containers of one-shot services that did their job: another
// service waits for them with service_completed_successfully, and they exited with 0. A stopped
// service that others merely start after stays stopped.
func completed(containers []engine.Container) map[string]bool {
	oneShot := map[string]bool{}
	for _, c := range containers {
		for dep := range strings.SplitSeq(c.Labels[labelDependsOn], ",") {
			if service, cond, _ := strings.Cut(dep, ":"); strings.HasPrefix(cond, "service_completed_successfully") {
				oneShot[service] = true
			}
		}
	}
	done := map[string]bool{}
	for _, c := range containers {
		if oneShot[c.Labels[engine.LabelComposeService]] && c.State == "exited" && strings.HasPrefix(c.Status, "Exited (0)") {
			done[c.ID] = true
		}
	}
	return done
}
