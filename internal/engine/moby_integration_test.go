//go:build integration

package engine_test

import (
	"context"
	"net"
	"net/netip"
	"os/exec"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/engine/enginetest"
)

type mobySeeder struct{ c *client.Client }

func (s mobySeeder) SeedContainer(t *testing.T, labels map[string]string) string {
	t.Helper()
	ctx := context.Background()
	res, err := s.c.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: "busybox:1.37", Labels: labels},
	})
	if err != nil {
		t.Fatalf("seed container: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.c.ContainerRemove(ctx, res.ID, client.ContainerRemoveOptions{Force: true})
	})
	return res.ID
}

func (s mobySeeder) SeedPublished(t *testing.T, labels map[string]string) string {
	t.Helper()
	ctx := context.Background()
	res, err := s.c.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        "busybox:1.37",
			Cmd:          []string{"sleep", "300"},
			Labels:       labels,
			ExposedPorts: network.PortSet{network.MustParsePort("80/tcp"): {}, network.MustParsePort("81/tcp"): {}},
		},
		HostConfig: &container.HostConfig{
			PortBindings: network.PortMap{network.MustParsePort("80/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1")}}},
		},
	})
	if err != nil {
		t.Fatalf("seed container: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.c.ContainerRemove(ctx, res.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err := s.c.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start container: %v", err)
	}
	return res.ID
}

func (s mobySeeder) CheckFile(t *testing.T, id, path, content string) {
	t.Helper()
	out, err := exec.Command("docker", "exec", id, "cat", path).Output()
	if err != nil || string(out) != content {
		t.Fatalf("%s: %q %v, want %q", path, out, err, content)
	}
}

func (s mobySeeder) SeedNetwork(t *testing.T, labels map[string]string) string {
	t.Helper()
	ctx := context.Background()
	res, err := s.c.NetworkCreate(ctx, "mutti-contract-"+labels["mutti.test.run"], client.NetworkCreateOptions{Labels: labels})
	if err != nil {
		t.Fatalf("seed network: %v", err)
	}
	t.Cleanup(func() { _, _ = s.c.NetworkRemove(ctx, res.ID, client.NetworkRemoveOptions{}) })
	return res.ID
}

func TestMobyContract(t *testing.T) {
	ctx := context.Background()
	e, err := engine.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	c, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	enginetest.RunContract(t, e, mobySeeder{c})
}

func TestMobyPassesDaemonErrorsThrough(t *testing.T) {
	e, err := engine.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	_, err = e.CreateVolume(context.Background(), "invalid/name", nil)
	if err == nil || engine.IsUnavailable(err) {
		t.Fatalf("want a plain daemon error, got %v", err)
	}
}

func TestMobyPullOfMissingImageFails(t *testing.T) {
	e, err := engine.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err := e.PullImage(context.Background(), "mutti-test/does-not-exist:never"); err == nil {
		t.Fatal("want error")
	}
}

func TestMobyRunContainerFailures(t *testing.T) {
	e, err := engine.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })

	if _, err := e.RunContainer(context.Background(), engine.ContainerSpec{Image: "busybox:1.37", Cmd: []string{"/no/such/binary"}}); err == nil {
		t.Error("start failure: want error")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := e.RunContainer(ctx, engine.ContainerSpec{Image: "busybox:1.37", Cmd: []string{"sleep", "30"}}); err == nil {
		t.Error("cancelled wait: want error")
	}
}

func TestMobyFixedHostPort(t *testing.T) {
	ctx := context.Background()
	e, err := engine.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	id, err := e.CreateContainer(ctx, engine.ContainerSpec{
		Image: "busybox:1.37", Cmd: []string{"sleep", "30"}, Labels: map[string]string{"mutti.test.fixed": "1"},
		Ports: []engine.Port{{HostIP: "127.0.0.1", HostPort: port, ContainerPort: 80, Proto: "tcp"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.RemoveContainer(ctx, id) })
	if err := e.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	cs, _ := e.ListContainers(ctx, "mutti.test.fixed=1")
	if len(cs) != 1 || len(cs[0].Ports) != 1 || cs[0].Ports[0].HostPort != port {
		t.Fatalf("want host port %d: %+v", port, cs)
	}
}
