package project

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/l0wskilled/mutti/internal/compose"
)

func ports(targets ...uint16) []compose.Port {
	var out []compose.Port
	for _, t := range targets {
		out = append(out, compose.Port{Target: t, Protocol: "tcp"})
	}
	return out
}

func TestDefaultRoutes(t *testing.T) {
	m := &compose.Model{Services: map[string]compose.Service{
		"frontend": {Ports: ports(4200)},
		"api":      {Ports: ports(8000, 9003)},
		"db":       {Ports: ports(5432)},
		"mail":     {Ports: ports(8025, 1025)},
		"dns":      {Ports: []compose.Port{{Target: 53, Protocol: "udp"}}},
		"worker":   {},
		"my_admin": {Ports: ports(9000)},
	}}
	got := Config{}.HTTPRoutes(m, "x.shop.localhost")
	// 4200 ranks before 8000 in webPorts, so frontend is the main service.
	want := []HTTPRoute{
		{Service: "api", Hosts: []string{"api.x.shop.localhost"}, Port: 8000},
		{Service: "frontend", Hosts: []string{"x.shop.localhost"}, Port: 4200},
		{Service: "mail", Hosts: []string{"mail.x.shop.localhost"}, Port: 8025},
		{Service: "my_admin", Hosts: []string{"my-admin.x.shop.localhost"}, Port: 9000},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestConfiguredRoutes(t *testing.T) {
	m := &compose.Model{Services: map[string]compose.Service{
		"web":    {Ports: ports(80)},
		"api":    {Ports: ports(80)},
		"mail":   {Ports: ports(8025)},
		"worker": {},
	}}
	main, mailpit := "", "mailpit"
	c := Config{Main: "web", Routes: map[string]Route{
		"api":    {Path: "/api"},
		"mail":   {Host: &mailpit},
		"worker": {Host: &main}, // no port anywhere: dropped
	}}
	got := c.HTTPRoutes(m, "shop.localhost")
	want := []HTTPRoute{
		{Service: "api", Hosts: []string{"api.shop.localhost"}, Paths: []string{"/api"}, Port: 80},
		{Service: "mail", Hosts: []string{"mailpit.shop.localhost"}, Port: 8025},
		{Service: "web", Hosts: []string{"shop.localhost"}, Port: 80},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}

	// api and web both serve port 80; the alphabetically first one becomes main.
	c = Config{Routes: map[string]Route{"worker": {Port: 3000}}}
	if got := c.HTTPRoutes(m, "shop.localhost"); len(got) != 2 || got[0].Service != "api" || got[0].Hosts[0] != "shop.localhost" || got[1].Port != 3000 {
		t.Errorf("explicit port and heuristic main: %+v", got)
	}
}

func TestNoWebService(t *testing.T) {
	m := &compose.Model{Services: map[string]compose.Service{"db": {Ports: ports(5432)}, "dns": {Ports: []compose.Port{{Target: 80, Protocol: "udp"}}}}}
	if got := (Config{}).HTTPRoutes(m, "shop.localhost"); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestExposedPortsCount(t *testing.T) {
	m := &compose.Model{Services: map[string]compose.Service{
		"web": {Expose: []string{"80/tcp"}},
		"api": {Expose: []string{"53/udp", "8000-8001", "junk"}},
		"db":  {Expose: []string{"5432"}},
		"pub": {Ports: ports(9000), Expose: []string{"9001"}}, // published first
	}}
	var c Config
	if main := c.MainService(m); main != "web" {
		t.Fatalf("main %q", main)
	}
	got := map[string]uint16{}
	for _, r := range c.HTTPRoutes(m, "shop.localhost") {
		got[r.Service] = r.Port
	}
	if diff := cmp.Diff(map[string]uint16{"web": 80, "api": 8000, "pub": 9000}, got); diff != "" {
		t.Errorf("routes (-want +got):\n%s", diff)
	}
}
