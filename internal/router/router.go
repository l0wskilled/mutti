// Package router describes mutti's machine-wide reverse proxy: one Traefik container that
// owns the HTTP(S) ports on loopback and routes hostnames to instances. Traefik reads its routes
// from files mutti renders; it gets no access to the Docker socket.
package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/l0wskilled/mutti/internal/engine"
)

// Image is the Traefik release the router runs, pinned by digest.
const Image = "traefik:v3.6.25@sha256:31267173a15b4944e797a76ffd9c419707c8d8b32fe5b610f80cd0cfa05f372d"

// Labels and roles of the router container.
const (
	Role         = "router"
	LabelVersion = "mutti.router.version"
)

// ConfigFile is where mutti puts the routes inside the router container. mutti copies it in
// through the Docker API, so it needs no path on the host: that works the same from inside a
// container, with Docker Desktop and on any OS.
const ConfigFile = "/etc/mutti/dynamic/mutti.yml"

// Settings configure the router container.
type Settings struct {
	Name      string // container name
	HTTPPort  uint16 // host port for HTTP on loopback
	HTTPSPort uint16 // host port for HTTPS on loopback
	IPv6      bool   // also listen on [::1]
}

// Spec returns the container spec of the router. Its version label changes whenever anything
// in the spec changes, which is the only reason mutti ever replaces a running router.
func Spec(s Settings) engine.ContainerSpec {
	spec := engine.ContainerSpec{
		Name:  s.Name,
		Image: Image,
		Cmd: []string{
			"--entryPoints.web.address=:80",
			"--entryPoints.websecure.address=:443",
			"--entryPoints.websecure.http.tls=true",
			"--providers.file.directory=" + path.Dir(ConfigFile),
			"--providers.file.watch=true",
			"--ping=true",
			"--ping.manualRouting=true",
			"--log.level=WARN",
			"--global.checkNewVersion=false",
			"--global.sendAnonymousUsage=false",
		},
		Restart: "unless-stopped",
	}
	ips := []string{"127.0.0.1"}
	if s.IPv6 {
		ips = append(ips, "::1")
	}
	for _, ip := range ips {
		spec.Ports = append(spec.Ports,
			engine.Port{HostIP: ip, HostPort: s.HTTPPort, ContainerPort: 80, Proto: "tcp"},
			engine.Port{HostIP: ip, HostPort: s.HTTPSPort, ContainerPort: 443, Proto: "tcp"})
	}
	b, _ := json.Marshal(spec) // plain struct of strings and numbers, cannot fail
	spec.Labels = map[string]string{
		engine.LabelSchema: engine.SchemaVersion,
		engine.LabelRole:   Role,
		LabelVersion:       hash(b),
	}
	return spec
}

// Instance is everything the router needs to serve one instance.
type Instance struct {
	Name     string
	MainHost string
	Routes   []Route
	CertPEM  []byte // certificate for MainHost and *.MainHost
	KeyPEM   []byte
}

// Route sends hosts (and path prefixes on the main host) to a service's containers.
type Route struct {
	Service   string
	Hosts     []string
	Paths     []string
	Port      uint16
	Upstreams []string // container names, reachable on the instance's ingress network
}

type dynamic struct {
	HTTP struct {
		Routers  map[string]httpRouter  `json:"routers"`
		Services map[string]httpService `json:"services"`
	} `json:"http"`
	TLS struct {
		Certificates []certificate `json:"certificates"`
	} `json:"tls"`
}

type httpRouter struct {
	Rule        string    `json:"rule"`
	Service     string    `json:"service"`
	EntryPoints []string  `json:"entryPoints"`
	TLS         *struct{} `json:"tls,omitempty"`
}

type httpService struct {
	LoadBalancer struct {
		Servers []server `json:"servers"`
	} `json:"loadBalancer"`
}

type server struct {
	URL string `json:"url"`
}

type certificate struct {
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}

// ReadyHost is the host the router answers ReadyPath on. No instance route matches it: on an
// instance's own host, a catch-all route (a single-page app answering every path) would answer
// the probe while the router still runs the previous configuration.
const ReadyHost = "mutti-ready.internal"

// ReadyPath is the path under which the router answers on ReadyHost once it loaded the
// configuration with the given token.
func ReadyPath(token string) string { return "/.well-known/mutti/" + token }

// Render returns the Traefik dynamic configuration of all routed instances, certificates
// inline, and its token: a hash of the content, which the configuration also serves under
// ReadyPath on ReadyHost. Seeing that path answer proves Traefik loaded exactly this version.
// JSON is valid YAML, which Traefik's file provider reads; encoding/json keeps it deterministic
// and correctly quoted.
func Render(instances []Instance) (config []byte, token string) {
	var d dynamic
	d.HTTP.Routers = map[string]httpRouter{}
	d.HTTP.Services = map[string]httpService{}
	d.TLS.Certificates = []certificate{}
	for _, in := range instances {
		routed := false
		for _, r := range in.Routes {
			if len(r.Upstreams) == 0 {
				continue
			}
			routed = true
			id := in.Name + "__" + r.Service
			var rules []string
			for _, h := range r.Hosts {
				rules = append(rules, fmt.Sprintf("Host(`%s`)", h))
			}
			for _, p := range r.Paths {
				rules = append(rules, fmt.Sprintf("(Host(`%s`) && PathPrefix(`%s`))", in.MainHost, p))
			}
			rule := strings.Join(rules, " || ")
			d.HTTP.Routers[id+"__http"] = httpRouter{Rule: rule, Service: id, EntryPoints: []string{"web"}}
			d.HTTP.Routers[id+"__https"] = httpRouter{Rule: rule, Service: id, EntryPoints: []string{"websecure"}, TLS: &struct{}{}}

			var svc httpService
			for _, u := range slices.Sorted(slices.Values(r.Upstreams)) {
				svc.LoadBalancer.Servers = append(svc.LoadBalancer.Servers, server{URL: fmt.Sprintf("http://%s:%d", u, r.Port)})
			}
			d.HTTP.Services[id] = svc
		}
		if routed {
			d.TLS.Certificates = append(d.TLS.Certificates, certificate{CertFile: string(in.CertPEM), KeyFile: string(in.KeyPEM)})
		}
	}
	token = hash(encode(d))
	d.HTTP.Routers["mutti__ready"] = httpRouter{
		Rule:        fmt.Sprintf("Host(`%s`) && Path(`%s`)", ReadyHost, ReadyPath(token)),
		Service:     "ping@internal",
		EntryPoints: []string{"web"},
	}
	return encode(d), token
}

func encode(d dynamic) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // keep "&&" in rules readable
	enc.SetIndent("", "  ")
	_ = enc.Encode(d) // plain structs, cannot fail
	return b.Bytes()
}

func hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// Hosts returns every hostname an instance serves, sorted: the names the router answers to on
// the instance's ingress network and the names its certificate needs.
func (in Instance) Hosts() []string {
	set := map[string]bool{in.MainHost: true}
	for _, r := range in.Routes {
		for _, h := range r.Hosts {
			set[h] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}
