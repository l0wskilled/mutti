package project

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/identity"
)

// webPorts are typical HTTP ports in order of how likely they are the app's front door.
var webPorts = []uint16{80, 3000, 4200, 5173, 8000, 8080}

// nonHTTPPorts are well-known ports of protocols other than HTTP (databases, caches, mail,
// brokers). A service whose first port is one of them gets no default route.
var nonHTTPPorts = map[uint16]bool{
	21: true, 22: true, 25: true, 53: true, 110: true, 143: true, 389: true, 465: true, 587: true,
	993: true, 995: true, 1025: true, 1433: true, 1521: true, 1883: true, 2181: true, 3306: true,
	4222: true, 5432: true, 5672: true, 6379: true, 6380: true, 9042: true, 9092: true,
	11211: true, 27017: true,
}

// HTTPRoute is the resolved route of one service.
type HTTPRoute struct {
	Service string
	Hosts   []string // full hostnames
	Paths   []string // path prefixes on the main hostname
	Port    uint16
}

// HTTPRoutes resolves the configured or default routes of a project under mainHost.
func (c Config) HTTPRoutes(m *compose.Model, mainHost string) []HTTPRoute {
	main := c.MainService(m)
	routes := c.Routes
	if len(routes) == 0 {
		routes = map[string]Route{}
		for name, svc := range m.Services {
			if port := firstTCP(svc); port != 0 && !nonHTTPPorts[port] {
				routes[name] = Route{}
			}
		}
	}
	if main != "" {
		if _, ok := routes[main]; !ok {
			routes[main] = Route{}
		}
	}

	var out []HTTPRoute
	for _, name := range slices.Sorted(maps.Keys(routes)) {
		r := routes[name]
		port := r.Port
		if port == 0 {
			port = firstTCP(m.Services[name])
		}
		if port == 0 {
			continue
		}
		host := identity.Sanitize(name)
		if name == main {
			host = ""
		}
		if r.Host != nil {
			host = *r.Host
		}
		hr := HTTPRoute{Service: name, Port: port, Hosts: []string{mainHost}}
		if host != "" {
			hr.Hosts = []string{host + "." + mainHost}
		}
		if r.Path != "" {
			hr.Paths = []string{r.Path}
		}
		out = append(out, hr)
	}
	return out
}

// MainService is x-mutti.main, or the service publishing or exposing the most typical web port.
func (c Config) MainService(m *compose.Model) string {
	if c.Main != "" {
		return c.Main
	}
	for _, port := range webPorts {
		for _, name := range m.ServiceNames() {
			if slices.Contains(tcpPorts(m.Services[name]), port) {
				return name
			}
		}
	}
	return ""
}

func firstTCP(svc compose.Service) uint16 {
	if ports := tcpPorts(svc); len(ports) > 0 {
		return ports[0]
	}
	return 0
}

// tcpPorts are the TCP container ports a service declares: the published ones first, then the
// exposed ones, which declare a port without publishing it on the host. Of a range like
// 8000-8001, the first port counts.
func tcpPorts(svc compose.Service) []uint16 {
	var out []uint16
	for _, p := range svc.Ports {
		if p.Protocol != "udp" {
			out = append(out, p.Target)
		}
	}
	for _, e := range svc.Expose {
		port, proto, _ := strings.Cut(e, "/")
		port, _, _ = strings.Cut(port, "-")
		if n, err := strconv.ParseUint(port, 10, 16); err == nil && n > 0 && proto != "udp" {
			out = append(out, uint16(n))
		}
	}
	return out
}
