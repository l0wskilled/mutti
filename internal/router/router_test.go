package router

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "rewrite golden files")

// golden compares got with testdata/<name>.golden, or rewrites the file with -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("%s (-want +got):\n%s", path, diff)
	}
}

func TestRender(t *testing.T) {
	feature := Instance{
		Name:     "shop-feature",
		MainHost: "feature.shop.localhost",
		CertPEM:  []byte("CERT feature"),
		KeyPEM:   []byte("KEY feature"),
		Routes: []Route{
			{Service: "web", Hosts: []string{"feature.shop.localhost"}, Port: 80, Upstreams: []string{"shop-feature-web-2", "shop-feature-web-1"}},
			{Service: "api", Hosts: []string{"api.feature.shop.localhost"}, Paths: []string{"/api"}, Port: 8000, Upstreams: []string{"shop-feature-api-1"}},
			{Service: "mail", Hosts: []string{"mail.feature.shop.localhost"}, Port: 8025}, // stopped: no upstreams
		},
	}
	stopped := Instance{Name: "shop", MainHost: "shop.localhost", CertPEM: []byte("CERT shop"), Routes: []Route{{Service: "web", Hosts: []string{"shop.localhost"}, Port: 80}}}

	config, token := Render([]Instance{feature, stopped})
	golden(t, "render", config)
	if token == "" || !strings.Contains(string(config), ReadyPath(token)) || strings.Contains(string(config), "CERT shop") {
		t.Errorf("token %q, or a stopped instance got routes or a certificate", token)
	}
	if hosts := feature.Hosts(); !cmp.Equal(hosts, []string{"api.feature.shop.localhost", "feature.shop.localhost", "mail.feature.shop.localhost"}) {
		t.Errorf("hosts %v", hosts)
	}
	fewer := feature
	fewer.Routes = feature.Routes[:1]
	if _, other := Render([]Instance{fewer}); other == token {
		t.Error("different routes must get a different token")
	}
	if empty, _ := Render(nil); !strings.Contains(string(empty), `"services": {}`) || !strings.Contains(string(empty), ReadyHost) {
		t.Errorf("empty config: %s", empty)
	}
}

func TestSpec(t *testing.T) {
	s := Settings{Name: "mutti-router", HTTPPort: 80, HTTPSPort: 443}
	spec := Spec(s)
	b, _ := json.MarshalIndent(spec, "", "  ")
	golden(t, "spec", append(b, '\n'))

	v6 := Spec(Settings{Name: "mutti-router", HTTPPort: 80, HTTPSPort: 443, IPv6: true})
	if len(v6.Ports) != 4 || v6.Ports[2].HostIP != "::1" {
		t.Errorf("ipv6 ports %+v", v6.Ports)
	}
	if v6.Labels[LabelVersion] == spec.Labels[LabelVersion] {
		t.Error("a different spec must have a different version")
	}
	if Spec(s).Labels[LabelVersion] != spec.Labels[LabelVersion] {
		t.Error("the same spec must keep its version")
	}
}
