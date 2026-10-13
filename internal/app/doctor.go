package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/router"
)

// Check results.
const (
	CheckOK   = "ok"
	CheckWarn = "warn"
	CheckFail = "fail"
)

// Check is one finding of Doctor.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// FeatureStatus tells whether the installed Compose has a feature mutti knows about.
type FeatureStatus struct {
	Name      string `json:"name"`
	Since     string `json:"since"`
	Required  bool   `json:"required"`
	Available bool   `json:"available"`
	Use       string `json:"use"`
}

// Report is the result of Doctor.
type Report struct {
	Checks          []Check         `json:"checks"`
	ComposeFeatures []FeatureStatus `json:"composeFeatures,omitempty"`
}

// Failed reports whether any check failed.
func (r *Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == CheckFail {
			return true
		}
	}
	return false
}

func (r *Report) add(name, status, detail, hint string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Detail: detail, Hint: hint})
}

// Doctor checks the environment mutti depends on and, if dir is inside a project, the project.
func (a *App) Doctor(ctx context.Context, dir string) *Report {
	r := &Report{}

	info, err := a.Engine.Info(ctx)
	if err != nil {
		r.add("docker", CheckFail, err.Error(), "start Docker, or check DOCKER_HOST / docker context")
	} else {
		detail := fmt.Sprintf("Docker %s on %s (%s)", info.ServerVersion, info.OS, info.Host)
		if info.Rootless {
			detail += ", rootless"
		}
		r.add("docker", CheckOK, detail, "")
		if remote := remoteHost(info.Host); remote != "" {
			r.add("docker host", CheckWarn, "the daemon runs on "+remote,
				"bind mounts only work where that host sees the worktree under some path (e.g. a shared CI volume); set MUTTI_HOST_ROOT to it")
		}
	}

	v, err := a.Compose.Version(ctx)
	switch {
	case err != nil:
		r.add("compose", CheckFail, err.Error(), "install Docker with the Compose plugin")
	case v.Less(compose.MinVersion()):
		r.add("compose", CheckFail, "docker compose "+v.String(), "mutti needs "+compose.MinVersion().String()+" or newer")
	default:
		r.add("compose", CheckOK, "docker compose "+v.String(), "")
	}
	if err == nil {
		for _, f := range compose.Features {
			r.ComposeFeatures = append(r.ComposeFeatures, FeatureStatus{
				Name: f.Name, Since: f.Since.String(), Required: f.Required, Available: v.Has(f), Use: f.Use,
			})
		}
	}

	if bx, err := a.Compose.BuildxVersion(ctx); err != nil {
		r.add("buildx", CheckWarn, "not installed", "without buildx, Compose builds with the legacy builder (no RUN --mount, secrets, ssh)")
	} else {
		r.add("buildx", CheckOK, bx, "")
	}

	if _, err := a.Host.LookPath("git"); err != nil {
		r.add("git", CheckWarn, "not installed", "without git, instances are identified by directory only")
	} else {
		r.add("git", CheckOK, "installed", "")
	}

	a.checkRouter(ctx, r)
	a.checkTrust(r)
	a.checkContainer(ctx, r)

	// Docker's default address pools fit about 31 bridge networks; mutti needs two per instance.
	if nets, err := a.Engine.ListNetworks(ctx); err == nil && len(nets) >= 24 {
		r.add("networks", CheckWarn, fmt.Sprintf("%d networks; with Docker's default address pools about 31 fit", len(nets)), poolsHint)
	}

	inst, err := a.Resolve(ctx, dir)
	if err != nil {
		status := CheckFail
		if e := (*Error)(nil); errors.As(err, &e) && e.Code == CodeNoComposeFile {
			status = CheckWarn
		}
		r.add("project", status, err.Error(), "")
		return r
	}
	detail := fmt.Sprintf("%s (%s), %d services, %s", inst.Name, inst.Hostname, len(inst.Model.Services), strings.Join(inst.Files.Paths, ", "))
	r.add("project", CheckOK, detail, "")
	return r
}

func (a *App) checkRouter(ctx context.Context, r *Report) {
	s, err := a.routerSettings()
	if err != nil {
		r.add("router", CheckFail, err.Error(), "")
		return
	}
	c, ok, err := a.Engine.InspectContainer(ctx, s.Name)
	addr := fmt.Sprintf("%s on 127.0.0.1:%d (http) and :%d (https)", s.Name, s.HTTPPort, s.HTTPSPort)
	switch {
	case err != nil:
		r.add("router", CheckFail, err.Error(), "")
	case !ok:
		r.add("router", CheckWarn, "not created yet", "it starts with the first mutti up")
	case c.Labels[router.LabelVersion] != router.Spec(s).Labels[router.LabelVersion]:
		r.add("router", CheckWarn, addr+", outdated", "mutti up or mutti router restart replaces it")
	case c.State != "running":
		r.add("router", CheckWarn, addr+", "+c.State, "mutti up or mutti router restart starts it")
	default:
		r.add("router", CheckOK, addr, "")
	}
}

func (a *App) checkTrust(r *Report) {
	trusted, err := a.trusted()
	switch {
	case err != nil:
		r.add("https", CheckWarn, err.Error(), "")
	case !trusted:
		r.add("https", CheckWarn, "the system does not trust mutti's local CA yet", "run mutti trust once, then restart your browser")
	default:
		r.add("https", CheckOK, "mutti's local CA is trusted", "")
	}
}

// checkContainer reports nested mode: mutti inside a container, mapping paths to the host.
func (a *App) checkContainer(ctx context.Context, r *Report) {
	if a.Host.ContainerID == "" {
		return
	}
	id := a.Host.ContainerID[:min(12, len(a.Host.ContainerID))]
	if _, ok, err := a.Engine.InspectContainer(ctx, a.Host.ContainerID); err != nil || !ok {
		r.add("container", CheckWarn, "mutti runs in container "+id+", which this Docker daemon does not know",
			"set MUTTI_HOST_ROOT to the worktree's path as the Docker host sees it")
		return
	}
	r.add("container", CheckOK, "mutti runs in container "+id+"; bind mounts are translated to host paths", "")
}

// remoteHost returns the host of a daemon address that is not on this machine, or "".
func remoteHost(addr string) string {
	u, err := url.Parse(addr)
	if err != nil || u.Scheme == "unix" || u.Scheme == "npipe" {
		return ""
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return ""
	}
	return u.Host
}
