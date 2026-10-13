package app

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/project"
)

// CodeNoRoute means a service is not reachable through the router.
const CodeNoRoute = "no-route"

// URL returns the HTTPS URL of a service, or of the instance's main hostname for "". It needs
// no running containers: the URL follows from the configuration.
func (a *App) URL(ctx context.Context, dir, service string) (string, error) {
	inst, err := a.Resolve(ctx, dir)
	if err != nil {
		return "", err
	}
	s, err := a.routerSettings()
	if err != nil {
		return "", err
	}
	routes := inst.Config.HTTPRoutes(inst.Model, inst.Hostname)
	if service == "" {
		if len(routes) == 0 {
			return "", &Error{Code: CodeNoRoute, Message: inst.Name + " has no HTTP service", Hint: "see its ports with: mutti status"}
		}
		return hostURL(inst.Hostname, s.HTTPSPort), nil
	}
	for _, r := range routes {
		if r.Service == service {
			url := hostURL(r.Hosts[0], s.HTTPSPort)
			if r.Hosts[0] == inst.Hostname && len(r.Paths) > 0 {
				url += r.Paths[0]
			}
			return url, nil
		}
	}
	if _, ok := inst.Model.Services[service]; !ok {
		return "", &Error{Code: CodeInvalidConfig, Message: fmt.Sprintf("unknown service %q", service), Hint: "services: " + strings.Join(inst.Model.ServiceNames(), ", ")}
	}
	return "", &Error{Code: CodeNoRoute, Message: fmt.Sprintf("%s has no HTTP route", service), Hint: "add one under x-mutti.routes, or see its ports with: mutti status"}
}

// Open opens a service's URL (see URL) in the browser.
func (a *App) Open(ctx context.Context, dir, service string) (string, error) {
	url, err := a.URL(ctx, dir, service)
	if err != nil {
		return "", err
	}
	if err := a.Host.OpenURL(url); err != nil {
		return url, &Error{Code: CodeOpenFailed, Message: "cannot open a browser", Hint: "open it yourself: " + url, Err: err}
	}
	return url, nil
}

// shellCommand starts bash where the image has it, sh otherwise.
var shellCommand = []string{"sh", "-c", `if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi`}

// Shell opens an interactive shell in a service, by default the main one.
func (a *App) Shell(ctx context.Context, dir, service string, opts ExecOptions) (int, error) {
	if service == "" {
		inst, err := a.Resolve(ctx, dir)
		if err != nil {
			return 0, err
		}
		service = inst.Config.MainService(inst.Model)
		if service == "" {
			names := inst.Model.ServiceNames()
			if len(names) == 0 {
				return 0, &Error{Code: CodeInvalidConfig, Message: inst.Name + " has no services"}
			}
			service = names[0]
		}
	}
	return a.Exec(ctx, dir, service, shellCommand, opts)
}

// ServiceStatus is one service in Status.
type ServiceStatus struct {
	Service string        `json:"service"`
	State   string        `json:"state"`  // running, exited, ... or "missing" when not created
	Status  string        `json:"status"` // Docker's text, e.g. "Up 3 minutes (healthy)"
	URLs    []string      `json:"urls,omitempty"`
	Ports   []PortSummary `json:"ports,omitempty"`
}

// InstanceStatus is the detailed state of one worktree's instance.
type InstanceStatus struct {
	Name     string          `json:"name"`
	Project  string          `json:"project"`
	Path     string          `json:"path"`
	Branch   string          `json:"branch,omitempty"`
	URL      string          `json:"url,omitempty"`
	Services []ServiceStatus `json:"services"`
}

// Status describes the instance of dir service by service.
func (a *App) Status(ctx context.Context, dir string) (*InstanceStatus, error) {
	inst, err := a.Resolve(ctx, dir)
	if err != nil {
		return nil, err
	}
	s, err := a.routerSettings()
	if err != nil {
		return nil, err
	}
	containers, err := a.Engine.ListContainers(ctx, engine.LabelComposeProject+"="+inst.Name)
	if err != nil {
		return nil, engineErr(err)
	}
	st := &InstanceStatus{Name: inst.Name, Project: inst.Project, Path: inst.Root}
	if b, err := a.Git.Branch(ctx, inst.Root); err == nil {
		st.Branch = b
	}
	routes := map[string]project.HTTPRoute{}
	for _, r := range inst.Config.HTTPRoutes(inst.Model, inst.Hostname) {
		routes[r.Service] = r
		if slices.Contains(r.Hosts, inst.Hostname) {
			st.URL = hostURL(inst.Hostname, s.HTTPSPort)
		}
	}
	byService := map[string]*ServiceStatus{}
	for _, name := range inst.Model.ServiceNames() {
		ss := &ServiceStatus{Service: name, State: "missing"}
		for _, h := range routes[name].Hosts {
			ss.URLs = append(ss.URLs, hostURL(h, s.HTTPSPort))
		}
		byService[name] = ss
	}
	for _, c := range containers {
		ss := byService[c.Labels[engine.LabelComposeService]]
		if ss == nil {
			continue // orphan of an older configuration
		}
		ss.State, ss.Status = c.State, c.Status
		for _, p := range c.Ports {
			ss.Ports = append(ss.Ports, PortSummary{Service: ss.Service, Address: fmt.Sprintf("%s:%d", p.HostIP, p.HostPort), ContainerPort: p.ContainerPort})
		}
	}
	for _, name := range inst.Model.ServiceNames() {
		ss := byService[name]
		if ss.State == "missing" && len(inst.Model.Services[name].Profiles) > 0 {
			continue // started on demand, not part of the instance until then
		}
		slices.SortFunc(ss.Ports, func(x, y PortSummary) int { return cmp.Compare(x.ContainerPort, y.ContainerPort) })
		st.Services = append(st.Services, *ss)
	}
	return st, nil
}

// EnvOf returns the MUTTI_* variables of the instance of dir.
func (a *App) EnvOf(ctx context.Context, dir string) ([]string, error) {
	inst, err := a.Resolve(ctx, dir)
	if err != nil {
		return nil, err
	}
	return a.Env(inst), nil
}

// Services returns the service names of the project of dir, for shell completion.
func (a *App) Services(ctx context.Context, dir string) []string {
	inst, err := a.Resolve(ctx, dir)
	if err != nil {
		return nil
	}
	return inst.Model.ServiceNames()
}

// WorktreeNames returns the names worktree remove accepts, for shell completion.
func (a *App) WorktreeNames(ctx context.Context, dir string) []string {
	info, err := a.repo(ctx, dir)
	if err != nil {
		return nil
	}
	list, err := a.Git.Worktrees(ctx, info.CommonDir)
	if err != nil {
		return nil
	}
	prefix := filepath.Base(info.MainRoot()) + "-"
	var names []string
	for _, w := range list {
		if w.Path != info.MainRoot() {
			names = append(names, strings.TrimPrefix(filepath.Base(w.Path), prefix))
		}
	}
	return names
}

// Prompt is a short state line for shell prompts: "<instance> <status> <running>/<total>", or
// "" outside an instance. It skips Compose entirely to stay fast, and never fails: a prompt has
// no place for errors.
func (a *App) Prompt(ctx context.Context, dir string) string {
	info, inGit, err := a.Git.Find(ctx, dir)
	if err != nil {
		return ""
	}
	claims, err := a.Engine.ListVolumes(ctx, engine.LabelRole+"="+RoleClaim, engine.LabelSchema)
	if err != nil {
		return ""
	}
	for _, c := range claims {
		mine := inGit && c.Labels[engine.LabelGitDir] == info.GitDir ||
			!inGit && c.Labels[engine.LabelGitDir] == "" && strings.HasPrefix(dir+string(filepath.Separator), c.Labels[engine.LabelPath]+string(filepath.Separator))
		if !mine {
			continue
		}
		name := c.Labels[engine.LabelInstance]
		containers, err := a.Engine.ListContainers(ctx, engine.LabelComposeProject+"="+name)
		if err != nil {
			return ""
		}
		s := Summary{}
		done := completed(containers)
		for _, ct := range containers {
			if !done[ct.ID] {
				s.Total++
			}
			if ct.State == "running" {
				s.Running++
			}
		}
		status := statusOf(s.Running, s.Total)
		if status == StatusDown {
			return name + " " + status
		}
		return fmt.Sprintf("%s %s %d/%d", name, status, s.Running, s.Total)
	}
	return ""
}
