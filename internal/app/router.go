package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/identity"
	"github.com/l0wskilled/mutti/internal/lock"
	"github.com/l0wskilled/mutti/internal/project"
	"github.com/l0wskilled/mutti/internal/router"
	"github.com/l0wskilled/mutti/internal/tlsca"
)

// Labels mutti puts on instance containers through the override.
const (
	LabelHostname    = "mutti.hostname"    // main hostname of the instance
	LabelRouteHosts  = "mutti.route.hosts" // comma-separated hostnames routed to this service
	LabelRoutePaths  = "mutti.route.paths" // comma-separated path prefixes on the main hostname
	LabelRoutePort   = "mutti.route.port"  // container port the router sends traffic to
	LabelRouter      = "mutti.router"      // name of the router that serves the instance
	RoleIngress      = "ingress"           // engine.LabelRole of an instance's ingress network
	defaultRouter    = "mutti-router"      // router container name
	CodeRouterFailed = "router-failed"     // error code
)

// ingressNetwork is the name of the network an instance's services and the router share.
func ingressNetwork(instance string) string { return "mutti-" + instance }

// routerSettings reads the router configuration from the environment.
func (a *App) routerSettings() (router.Settings, error) {
	s := router.Settings{Name: defaultRouter, HTTPPort: 80, HTTPSPort: 443, IPv6: a.Host.IPv6}
	if v := a.Host.Getenv("MUTTI_ROUTER_NAME"); v != "" {
		s.Name = v
	}
	for env, port := range map[string]*uint16{"MUTTI_HTTP_PORT": &s.HTTPPort, "MUTTI_HTTPS_PORT": &s.HTTPSPort} {
		if v := a.Host.Getenv(env); v != "" {
			n, err := strconv.ParseUint(v, 10, 16)
			if err != nil || n == 0 {
				return router.Settings{}, &Error{Code: CodeInvalidConfig, Message: fmt.Sprintf("%s=%q is not a port", env, v)}
			}
			*port = uint16(n)
		}
	}
	return s, nil
}

// routeLabels returns the container labels that describe an instance's routes in model m.
func routeLabels(inst *Instance, m *compose.Model, routerName string) (common map[string]string, perService map[string]map[string]string) {
	common = map[string]string{engine.LabelInstance: inst.Name, LabelHostname: inst.Hostname, LabelRouter: routerName}
	if inst.runID != "" {
		common[LabelRun] = inst.runID
	}
	perService = map[string]map[string]string{}
	for _, r := range inst.Config.HTTPRoutes(m, inst.Hostname) {
		l := map[string]string{LabelRouteHosts: strings.Join(r.Hosts, ","), LabelRoutePort: strconv.Itoa(int(r.Port))}
		if len(r.Paths) > 0 {
			l[LabelRoutePaths] = strings.Join(r.Paths, ",")
		}
		perService[r.Service] = l
	}
	return common, perService
}

// ensureIngress creates the instance's ingress network. mutti owns it, not Compose, so a plain
// `docker compose down` never trips over the router still being attached.
func (a *App) ensureIngress(ctx context.Context, inst *Instance) error {
	s, err := a.routerSettings()
	if err != nil {
		return err
	}
	labels := map[string]string{
		engine.LabelSchema:   engine.SchemaVersion,
		engine.LabelRole:     RoleIngress,
		engine.LabelInstance: inst.Name,
		LabelRouter:          s.Name,
	}
	if inst.runID != "" {
		labels[LabelRun] = inst.runID
	}
	_, err = a.Engine.CreateNetwork(ctx, ingressNetwork(inst.Name), labels)
	return engineErr(err)
}

// RouterStatus describes the router after a reconcile.
type RouterStatus struct {
	Name      string   `json:"name"`
	HTTPPort  uint16   `json:"httpPort"`
	HTTPSPort uint16   `json:"httpsPort"`
	Instances []string `json:"instances"` // instances with at least one routed, running service
	// Conflicts are instances left out because another one holds a hostname they need.
	Conflicts []HostConflict `json:"conflicts,omitempty"`

	token string // of the configuration the router serves (router.Render)
}

// Router ownership.
const (
	LabelCA           = "mutti.ca"       // fingerprint of the CA whose certificates the router serves
	CodeRouterForeign = "router-foreign" // the router belongs to another mutti state
)

// CodeHostnameTaken means an instance's hostname is held by another instance.
const CodeHostnameTaken = "hostname-taken"

// HostConflict is an instance the router leaves out because another one holds its hostname.
type HostConflict struct {
	Instance string `json:"instance"`
	Host     string `json:"host"`
	Owner    string `json:"owner"`             // the instance holding the hostname
	Service  string `json:"service,omitempty"` // the owner's service, when one of its routes holds it
}

func (c HostConflict) appError() *Error {
	owner := c.Owner
	if c.Service != "" {
		owner += " (service " + c.Service + ")"
	}
	return &Error{
		Code:    CodeHostnameTaken,
		Message: fmt.Sprintf("%s cannot be routed: %s belongs to %s", c.Instance, c.Host, owner),
		Hint:    "give this worktree another name: mutti rename <name>",
	}
}

// checkHosts fails when inst lost a hostname, and reports instances it took one from.
func (a *App) checkHosts(status *RouterStatus, inst *Instance) error {
	var own error
	for _, c := range status.Conflicts {
		switch {
		case c.Instance == inst.Name:
			own = c.appError()
		case c.Owner == inst.Name:
			fmt.Fprintf(a.IO.Err, "mutti: %s is no longer routed: %s belongs to %s now\n", c.Instance, c.Host, inst.Name)
		}
	}
	return own
}

// hostClaim is one hostname an instance's routes need.
type hostClaim struct {
	host, instance, service string // service is empty for the instance's main hostname
}

// dropHostConflicts gives every hostname to one instance and leaves out the others. A service's
// own subdomain wins over another instance's main hostname: worktree admin of project shop
// gets admin.shop.localhost, which shop's service admin uses already, so the worktree is the
// one that has to move. Between equals, the instance name decides.
func dropHostConflicts(instances []router.Instance) ([]router.Instance, []HostConflict) {
	// An instance that loses one hostname gives up all of them, which may free a hostname
	// another instance lost to it. So each round only drops instances that lost to one that
	// stays; when every loser lost to another loser (they hold each other's hostnames), all go.
	var all []HostConflict
	for {
		conflicts := settleHosts(instances)
		if len(conflicts) == 0 {
			return instances, all
		}
		losers := map[string]bool{}
		for _, c := range conflicts {
			losers[c.Instance] = true
		}
		final := slices.DeleteFunc(slices.Clone(conflicts), func(c HostConflict) bool { return losers[c.Owner] })
		if len(final) == 0 {
			final = conflicts
		}
		drop := map[string]bool{}
		for _, c := range final {
			drop[c.Instance] = true
		}
		all = append(all, final...)
		instances = slices.DeleteFunc(slices.Clone(instances), func(in router.Instance) bool { return drop[in.Name] })
	}
}

// settleHosts reports, per instance, the first hostname it loses to another one.
func settleHosts(instances []router.Instance) []HostConflict {
	var claims []hostClaim
	for _, in := range instances {
		claims = append(claims, hostClaim{host: in.MainHost, instance: in.Name})
		for _, r := range in.Routes {
			for _, h := range r.Hosts {
				if h != in.MainHost {
					claims = append(claims, hostClaim{host: h, instance: in.Name, service: r.Service})
				}
			}
		}
	}
	slices.SortStableFunc(claims, func(x, y hostClaim) int {
		if (x.service != "") != (y.service != "") {
			if x.service != "" {
				return -1
			}
			return 1
		}
		return strings.Compare(x.instance, y.instance)
	})
	owners := map[string]hostClaim{}
	var conflicts []HostConflict
	lost := map[string]bool{}
	for _, c := range claims {
		owner, held := owners[c.host]
		switch {
		case !held:
			owners[c.host] = c
		case owner.instance != c.instance && !lost[c.instance]:
			lost[c.instance] = true
			conflicts = append(conflicts, HostConflict{Instance: c.instance, Host: c.host, Owner: owner.instance, Service: owner.service})
		}
	}
	return conflicts
}

// Reconcile brings the router in line with what runs: it starts the router if needed, attaches
// it to every ingress network under the instances' hostnames, and writes certificates and
// routes. Everything it does is derived from Docker state, so running it again is harmless.
func (a *App) Reconcile(ctx context.Context) (*RouterStatus, error) {
	s, err := a.routerSettings()
	if err != nil {
		return nil, err
	}
	release, err := lock.Acquire(ctx, a.Dirs.LockFile("router"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = release() }()

	// Only this router's instances: several routers (MUTTI_ROUTER_NAME) never share networks.
	containers, err := a.Engine.ListContainers(ctx, engine.LabelInstance, LabelRouter+"="+s.Name)
	if err != nil {
		return nil, engineErr(err)
	}
	networks, err := a.Engine.ListNetworks(ctx, engine.LabelRole+"="+RoleIngress, LabelRouter+"="+s.Name)
	if err != nil {
		return nil, engineErr(err)
	}
	instances, conflicts := dropHostConflicts(desiredRoutes(containers, networks))
	r, err := a.renderRouter(s, instances)
	if err != nil {
		return nil, err
	}

	routerID, err := a.ensureRouter(ctx, s, r)
	if err != nil {
		return nil, err
	}
	if err := a.attachRouter(ctx, routerID, networks, instances); err != nil {
		return nil, err
	}
	status := &RouterStatus{Name: s.Name, HTTPPort: s.HTTPPort, HTTPSPort: s.HTTPSPort, Instances: []string{}, Conflicts: conflicts, token: r.token}
	for _, in := range instances {
		if routed(in) {
			status.Instances = append(status.Instances, in.Name)
		}
	}
	return status, nil
}

// desiredRoutes groups instance containers into router instances, sorted by name. Stopped
// containers still contribute hostnames (for the network aliases) but no upstreams.
//
// Labels come from images too, so they are checked before they reach file paths and router
// rules. An instance counts only with the ingress network mutti created for it, a container only
// as part of the Compose project of that name, and a route only with hosts under the instance's
// hostname and well-formed paths.
func desiredRoutes(containers []engine.Container, ingress []engine.Network) []router.Instance {
	known := map[string]bool{}
	for _, n := range ingress {
		if name := n.Labels[engine.LabelInstance]; identity.Valid(name) && n.Name == ingressNetwork(name) {
			known[name] = true
		}
	}
	byName := map[string]*router.Instance{}
	routes := map[string]map[string]*router.Route{}
	for _, c := range containers {
		name := c.Labels[engine.LabelInstance]
		host := c.Labels[LabelHostname]
		if !known[name] || c.Labels[engine.LabelComposeProject] != name || !validHostname(host) {
			continue
		}
		in := byName[name]
		if in == nil {
			in = &router.Instance{Name: name, MainHost: host}
			byName[name] = in
			routes[name] = map[string]*router.Route{}
		}
		if host != in.MainHost {
			continue
		}
		port, err := strconv.ParseUint(c.Labels[LabelRoutePort], 10, 16)
		if err != nil || port == 0 {
			continue // not a routed service
		}
		service := c.Labels[engine.LabelComposeService]
		r := routes[name][service]
		if r == nil {
			hosts, paths := splitList(c.Labels[LabelRouteHosts]), splitList(c.Labels[LabelRoutePaths])
			if !slices.ContainsFunc(hosts, func(h string) bool { return !underHost(h, in.MainHost) }) &&
				!slices.ContainsFunc(paths, func(p string) bool { return !project.ValidRoutePath(p) }) {
				r = &router.Route{Service: service, Port: uint16(port), Hosts: hosts, Paths: paths}
				routes[name][service] = r
			}
		}
		if r != nil && c.State == "running" {
			r.Upstreams = append(r.Upstreams, c.Name)
		}
	}
	var out []router.Instance
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		in := byName[name]
		for _, service := range slices.Sorted(maps.Keys(routes[name])) {
			in.Routes = append(in.Routes, *routes[name][service])
		}
		out = append(out, *in)
	}
	return out
}

// validHostname reports whether h is a hostname of DNS labels.
func validHostname(h string) bool {
	return h != "" && !slices.ContainsFunc(strings.Split(h, "."), func(l string) bool { return !identity.Valid(l) })
}

// underHost reports whether h is main or a name under it.
func underHost(h, main string) bool {
	return validHostname(h) && (h == main || strings.HasSuffix(h, "."+main))
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func routed(in router.Instance) bool {
	return slices.ContainsFunc(in.Routes, func(r router.Route) bool { return len(r.Upstreams) > 0 })
}

// ensureRouter makes sure the router container exists in the current version, holds config and
// runs. It never recreates a running router of the current version, so instances keep their
// traffic; a new config is copied in and Traefik picks it up by itself.
func (a *App) ensureRouter(ctx context.Context, s router.Settings, r rendered) (string, error) {
	spec := router.Spec(s)
	spec.Labels[LabelCA] = r.ca
	current, exists, err := a.Engine.InspectContainer(ctx, s.Name)
	if err != nil {
		return "", engineErr(err)
	}
	// The router is one per Docker daemon, its CA and certificates come from one mutti's state.
	// Another state (another user, or mutti in a container) must not take it over: both would
	// keep replacing each other's configuration and certificates.
	if owner := current.Labels[LabelCA]; exists && owner != "" && owner != spec.Labels[LabelCA] {
		return "", &Error{
			Code:    CodeRouterForeign,
			Message: fmt.Sprintf("the router %s belongs to another mutti (another user, or mutti on the host while this one runs in a container)", s.Name),
			Hint:    "share mutti's state with it (" + a.Dirs.State + " and " + a.Dirs.Data + "), or run a router of your own: set MUTTI_ROUTER_NAME, MUTTI_HTTP_PORT and MUTTI_HTTPS_PORT",
		}
	}
	if exists && current.Labels[router.LabelVersion] != spec.Labels[router.LabelVersion] {
		if err := a.Engine.RemoveContainer(ctx, current.ID); err != nil {
			return "", engineErr(err)
		}
		exists = false
	}
	if !exists {
		if ok, err := a.Engine.ImageExists(ctx, router.Image); err != nil {
			return "", engineErr(err)
		} else if !ok {
			if err := a.Engine.PullImage(ctx, router.Image); err != nil {
				return "", routerErr("cannot pull the router image "+router.Image, err)
			}
		}
		id, err := a.Engine.CreateContainer(ctx, spec)
		switch {
		case errors.Is(err, engine.ErrConflict):
			// Another mutti created it a moment ago; use theirs.
			current, _, err = a.Engine.InspectContainer(ctx, s.Name)
			if err != nil {
				return "", engineErr(err)
			}
		case err != nil:
			return "", routerErr("cannot create the router", err)
		default:
			current = engine.ContainerDetails{ID: id, State: "created"}
		}
	}
	// Before the first start too, so Traefik never runs without its file. Only when it changed:
	// every copy makes Traefik reload.
	pushed := filepath.Join(a.Dirs.State, "router", s.Name, "pushed")
	stamp := []byte(current.ID + " " + hashOf(r.config))
	if old, err := os.ReadFile(pushed); err != nil || !bytes.Equal(old, stamp) {
		if err := a.Engine.CopyToContainer(ctx, current.ID, map[string][]byte{router.ConfigFile: r.config}); err != nil {
			return "", engineErr(err)
		}
		if err := writeIfChanged(pushed, stamp); err != nil {
			return "", err
		}
	}
	if current.State != "running" {
		if err := a.Engine.StartContainer(ctx, current.ID); err != nil {
			return "", &Error{
				Code:    CodeRouterFailed,
				Message: fmt.Sprintf("cannot start the router on 127.0.0.1:%d and :%d", s.HTTPPort, s.HTTPSPort),
				Hint:    "another program probably uses these ports; stop it, or pick others with MUTTI_HTTP_PORT and MUTTI_HTTPS_PORT",
				Err:     err,
			}
		}
	}
	return current.ID, nil
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func routerErr(msg string, err error) error {
	if engine.IsUnavailable(err) {
		return engineErr(err)
	}
	return &Error{Code: CodeRouterFailed, Message: msg, Err: err}
}

// attachRouter connects the router to every ingress network, answering to the instance's
// hostnames there. Containers of the instance then reach their own hostnames through the router;
// other instances' networks never see them.
func (a *App) attachRouter(ctx context.Context, routerID string, networks []engine.Network, instances []router.Instance) error {
	current, _, err := a.Engine.InspectContainer(ctx, routerID)
	if err != nil {
		return engineErr(err)
	}
	hosts := map[string][]string{}
	for _, in := range instances {
		hosts[ingressNetwork(in.Name)] = in.Hosts()
	}
	for _, n := range networks {
		want := hosts[n.Name]
		have, attached := current.Networks[n.Name]
		if attached && slices.Equal(sortedCopy(have), want) {
			continue
		}
		if attached {
			if err := a.Engine.DisconnectNetwork(ctx, n.Name, routerID); err != nil {
				return engineErr(err)
			}
		}
		if err := a.Engine.ConnectNetwork(ctx, n.Name, routerID, want); err != nil {
			return engineErr(err)
		}
	}
	return nil
}

func sortedCopy(s []string) []string {
	return slices.Sorted(slices.Values(s))
}

// rendered is a router configuration with its token and the fingerprint of the CA it holds
// certificates of.
type rendered struct {
	config    []byte
	token, ca string
}

// renderRouter renders the router configuration for instances, with a certificate for each
// routed one. Certificates are cached in the state dir and only reissued when they no longer fit
// or run out, so the configuration only changes when routes do.
func (a *App) renderRouter(s router.Settings, instances []router.Instance) (rendered, error) {
	ca, err := tlsca.LoadOrCreate(a.caDir(), []string{a.Domain}, a.Now())
	if err != nil {
		return rendered{}, err
	}
	certDir := filepath.Join(a.Dirs.State, "router", s.Name, "certs")
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		return rendered{}, err
	}
	keep := map[string]bool{}
	for i, in := range instances {
		if !routed(in) {
			continue
		}
		certPath := filepath.Join(certDir, in.Name+".pem")
		keyPath := filepath.Join(certDir, in.Name+"-key.pem")
		keep[certPath], keep[keyPath] = true, true

		names := []string{in.MainHost, "*." + in.MainHost}
		cert, certErr := os.ReadFile(certPath)
		key, keyErr := os.ReadFile(keyPath)
		if certErr != nil || keyErr != nil || !ca.Valid(cert, names, a.Now()) {
			cert, key, _ = ca.Issue(names, a.Now()) // signing fails only for a mismatched pair, which Load refuses
			if err := writeIfChanged(keyPath, key); err != nil {
				return rendered{}, err
			}
			if err := writeIfChanged(certPath, cert); err != nil {
				return rendered{}, err
			}
		}
		instances[i].CertPEM, instances[i].KeyPEM = cert, key
	}
	entries, err := os.ReadDir(certDir)
	if err != nil {
		return rendered{}, err
	}
	for _, e := range entries {
		if p := filepath.Join(certDir, e.Name()); !keep[p] {
			if err := os.Remove(p); err != nil {
				return rendered{}, err
			}
		}
	}
	config, token := router.Render(instances)
	return rendered{config: config, token: token, ca: hashOf(ca.CertPEM)[:16]}, nil
}

// writeIfChanged writes data atomically unless the file already holds exactly that. Traefik
// watches these files, so they must never be seen half-written.
func writeIfChanged(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// waitReady waits until the router serves the configuration with the status's token for the
// instance, so it answers as soon as up returns. On the host it asks the router's published
// port; inside a container, where 127.0.0.1 is not the host's, a helper container on the
// instance's ingress network asks the router there.
func (a *App) waitReady(ctx context.Context, status *RouterStatus, inst *Instance) error {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", status.HTTPPort, router.ReadyPath(status.token))
	probe := a.Probe
	if a.Host.ContainerID != "" {
		if err := a.ensureImage(ctx, HelperImage); err != nil {
			return err
		}
		// The instance's hostname leads to the router on its network.
		probe = func(ctx context.Context, _, host string) (int, error) {
			code, err := a.Engine.RunContainer(ctx, engine.ContainerSpec{
				Image:    HelperImage,
				Cmd:      []string{"wget", "-q", "-T", "2", "-O", "/dev/null", "--header", "Host: " + host, "http://" + inst.Hostname + router.ReadyPath(status.token)},
				Labels:   map[string]string{engine.LabelSchema: engine.SchemaVersion, engine.LabelInstance: inst.Name, engine.LabelRole: "probe"},
				Networks: []string{ingressNetwork(inst.Name)},
			})
			if err != nil || code != 0 {
				return http.StatusServiceUnavailable, err
			}
			return http.StatusOK, nil
		}
	}
	timeout := a.ReadyTimeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	deadline := time.After(timeout)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if code, err := probe(ctx, url, router.ReadyHost); err == nil && code == http.StatusOK {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return &Error{
				Code:    CodeRouterFailed,
				Message: fmt.Sprintf("the router did not pick up the routes of %s within %s", inst.Hostname, timeout),
				Hint:    "see what it says: docker logs " + status.Name,
			}
		case <-tick.C:
		}
	}
}

// RouterRestart replaces the router container, e.g. after it was changed by hand.
func (a *App) RouterRestart(ctx context.Context) (*RouterStatus, error) {
	s, err := a.routerSettings()
	if err != nil {
		return nil, err
	}
	if c, ok, err := a.Engine.InspectContainer(ctx, s.Name); err != nil {
		return nil, engineErr(err)
	} else if ok {
		if err := a.Engine.RemoveContainer(ctx, c.ID); err != nil {
			return nil, engineErr(err)
		}
	}
	return a.Reconcile(ctx)
}
