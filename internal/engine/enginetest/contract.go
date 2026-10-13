package enginetest

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/l0wskilled/mutti/internal/engine"
)

// Seeder creates resources the Engine interface itself cannot create.
type Seeder interface {
	// SeedContainer creates a stopped container with the given labels and returns its id.
	SeedContainer(t *testing.T, labels map[string]string) string
	// SeedPublished creates a running container that publishes container port 80 on 127.0.0.1
	// (random host port) and exposes port 81 without publishing it.
	SeedPublished(t *testing.T, labels map[string]string) string
	// SeedNetwork creates a network with the given labels and returns its id.
	SeedNetwork(t *testing.T, labels map[string]string) string
	// CheckFile fails the test unless the file in the running container has that content.
	CheckFile(t *testing.T, id, path, content string)
}

// RunContract checks the behavior every Engine implementation must share. Resources are
// labelled with a per-run marker, so the suite is safe to run against a shared daemon.
func RunContract(t *testing.T, e engine.Engine, s Seeder) {
	ctx := context.Background()
	run := "contract-" + strings.ToLower(rand.Text()[:10])
	marker := "mutti.test.run=" + run
	labels := map[string]string{"mutti.test.run": run}

	t.Run("info", func(t *testing.T) {
		i, err := e.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if i.ServerVersion == "" || i.Host == "" {
			t.Errorf("incomplete info %+v", i)
		}
	})

	t.Run("volume create is idempotent and keeps first labels", func(t *testing.T) {
		name := run + "-claim"
		t.Cleanup(func() { _ = e.RemoveVolume(ctx, name) })
		first, err := e.CreateVolume(ctx, name, map[string]string{"mutti.test.run": run, "owner": "a"})
		if err != nil {
			t.Fatal(err)
		}
		second, err := e.CreateVolume(ctx, name, map[string]string{"mutti.test.run": run, "owner": "b"})
		if err != nil {
			t.Fatal(err)
		}
		if first.Labels["owner"] != "a" || second.Labels["owner"] != "a" {
			t.Fatalf("labels changed on second create: %v / %v", first.Labels, second.Labels)
		}
		got, ok, err := e.InspectVolume(ctx, name)
		if err != nil || !ok || got.Labels["owner"] != "a" {
			t.Fatalf("inspect = %+v, %v, %v", got, ok, err)
		}
	})

	t.Run("volume list filters by label", func(t *testing.T) {
		a, b := run+"-a", run+"-b"
		t.Cleanup(func() { _ = e.RemoveVolume(ctx, a); _ = e.RemoveVolume(ctx, b) })
		if _, err := e.CreateVolume(ctx, a, map[string]string{"mutti.test.run": run, "kind": "x"}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.CreateVolume(ctx, b, map[string]string{"mutti.test.run": run, "kind": "y"}); err != nil {
			t.Fatal(err)
		}
		got, err := e.ListVolumes(ctx, marker, "kind=x")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != a {
			t.Fatalf("want only %s, got %+v", a, got)
		}
		got, err = e.ListVolumes(ctx, marker, "kind")
		if err != nil || len(got) != 2 {
			t.Fatalf("key-only filter: %+v, %v", got, err)
		}
	})

	t.Run("missing resources", func(t *testing.T) {
		if _, ok, err := e.InspectVolume(ctx, run+"-nope"); ok || err != nil {
			t.Errorf("inspect missing volume: ok=%v err=%v", ok, err)
		}
		if err := e.RemoveVolume(ctx, run+"-nope"); err != nil {
			t.Errorf("remove missing volume: %v", err)
		}
		if err := e.RemoveContainer(ctx, run+"-nope"); err != nil {
			t.Errorf("remove missing container: %v", err)
		}
		if err := e.RemoveNetwork(ctx, run+"-nope"); err != nil {
			t.Errorf("remove missing network: %v", err)
		}
	})

	t.Run("containers are listed in any state and removed", func(t *testing.T) {
		id := s.SeedContainer(t, labels)
		got, err := e.ListContainers(ctx, marker)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != id || got[0].Labels["mutti.test.run"] != run || got[0].Name == "" {
			t.Fatalf("unexpected %+v", got)
		}
		if err := e.RemoveContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.ListContainers(ctx, marker); len(got) != 0 {
			t.Fatalf("still listed: %+v", got)
		}
	})

	t.Run("only published ports are listed", func(t *testing.T) {
		id := s.SeedPublished(t, labels)
		t.Cleanup(func() { _ = e.RemoveContainer(ctx, id) })
		got, err := e.ListContainers(ctx, marker)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].State != "running" {
			t.Fatalf("unexpected %+v", got)
		}
		ports := got[0].Ports
		if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort == 0 || ports[0].ContainerPort != 80 || ports[0].Proto != "tcp" {
			t.Fatalf("unexpected ports %+v", ports)
		}
	})

	t.Run("network create is idempotent", func(t *testing.T) {
		name := run + "-net"
		first, err := e.CreateNetwork(ctx, name, map[string]string{"mutti.test.run": run, "owner": "a"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.RemoveNetwork(ctx, first.ID) })
		second, err := e.CreateNetwork(ctx, name, map[string]string{"owner": "b"})
		if err != nil || second.ID != first.ID || second.Labels["owner"] != "a" || second.Name != name {
			t.Fatalf("second create: %+v %v", second, err)
		}
	})

	t.Run("containers attach to networks with aliases", func(t *testing.T) {
		netName := run + "-attach"
		n, err := e.CreateNetwork(ctx, netName, labels)
		if err != nil {
			t.Fatal(err)
		}
		id := s.SeedPublished(t, labels)
		t.Cleanup(func() {
			_ = e.RemoveContainer(ctx, id)
			_ = e.RemoveNetwork(ctx, n.ID)
		})
		if err := e.ConnectNetwork(ctx, netName, id, []string{"a.example.localhost", "b.example.localhost"}); err != nil {
			t.Fatal(err)
		}
		d, ok, err := e.InspectContainer(ctx, id)
		if err != nil || !ok || d.ID != id || d.State != "running" || d.Labels["mutti.test.run"] != run {
			t.Fatalf("inspect: %+v %v %v", d, ok, err)
		}
		if got := d.Networks[netName]; len(got) != 2 || got[0] != "a.example.localhost" || got[1] != "b.example.localhost" {
			t.Fatalf("aliases %v", d.Networks)
		}
		cs, _ := e.ListContainers(ctx, marker)
		if len(cs) != 1 || !slices.Contains(cs[0].Networks, netName) {
			t.Fatalf("list networks %+v", cs)
		}
		if err := e.ConnectNetwork(ctx, netName, id, nil); err == nil {
			t.Fatal("connecting twice must fail")
		}
		if err := e.RemoveNetwork(ctx, n.ID); err == nil {
			t.Fatal("removing a network with attached containers must fail")
		}
		if err := e.DisconnectNetwork(ctx, netName, id); err != nil {
			t.Fatal(err)
		}
		if err := e.DisconnectNetwork(ctx, netName, id); err != nil {
			t.Fatalf("disconnecting twice: %v", err)
		}
		d, _, _ = e.InspectContainer(ctx, id)
		if _, attached := d.Networks[netName]; attached {
			t.Fatalf("still attached: %v", d.Networks)
		}
	})

	t.Run("container create, conflict, start, inspect", func(t *testing.T) {
		const image = "busybox:1.37"
		if err := e.PullImage(ctx, image); err != nil {
			t.Fatal(err)
		}
		if ok, err := e.ImageExists(ctx, image); err != nil || !ok {
			t.Fatalf("image exists: %v %v", ok, err)
		}
		if ok, err := e.ImageExists(ctx, "mutti-test/nope:never"); err != nil || ok {
			t.Fatalf("missing image: %v %v", ok, err)
		}
		dir := t.TempDir()
		spec := engine.ContainerSpec{
			Name: run + "-created", Image: image, Cmd: []string{"sleep", "300"}, Labels: labels,
			Ports:   []engine.Port{{HostIP: "127.0.0.1", HostPort: 0, ContainerPort: 80, Proto: "tcp"}},
			Mounts:  []engine.Mount{{Source: dir, Target: "/data", ReadOnly: true}},
			Restart: "unless-stopped",
		}
		id, err := e.CreateContainer(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.RemoveContainer(ctx, id) })
		if _, err := e.CreateContainer(ctx, spec); !errors.Is(err, engine.ErrConflict) {
			t.Fatalf("second create: %v", err)
		}
		if err := e.StartContainer(ctx, spec.Name); err != nil {
			t.Fatal(err)
		}
		d, ok, err := e.InspectContainer(ctx, spec.Name)
		if err != nil || !ok || d.ID != id || d.Name != spec.Name || d.State != "running" {
			t.Fatalf("inspect: %+v %v %v", d, ok, err)
		}
		if _, ok, err := e.InspectContainer(ctx, run+"-missing"); ok || err != nil {
			t.Fatalf("inspect missing: %v %v", ok, err)
		}
	})

	t.Run("one-off containers run to completion and go away", func(t *testing.T) {
		vol := run + "-data"
		if _, err := e.CreateVolume(ctx, vol, labels); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.RemoveVolume(ctx, vol) })
		code, err := e.RunContainer(ctx, engine.ContainerSpec{
			Image: "busybox:1.37", Cmd: []string{"sh", "-c", "echo x > /v/f; exit 3"}, Labels: labels,
			Mounts: []engine.Mount{{Source: vol, Target: "/v", Volume: true}},
		})
		if err != nil || code != 3 {
			t.Fatalf("code=%d err=%v", code, err)
		}
		if cs, _ := e.ListContainers(ctx, marker); len(cs) != 0 {
			t.Fatalf("one-off container left: %+v", cs)
		}
	})

	t.Run("events report what happens to containers", func(t *testing.T) {
		ectx, cancel := context.WithCancel(ctx)
		defer cancel()
		events, errs := e.Events(ectx, marker)
		id, err := e.CreateContainer(ctx, engine.ContainerSpec{
			Name: run + "-evented", Image: "busybox:1.37", Cmd: []string{"sleep", "60"}, Labels: labels,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.StartContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := e.RemoveContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
		want := []string{"create", "start", "destroy"}
		timeout := time.After(20 * time.Second)
		for len(want) > 0 {
			select {
			case ev := <-events:
				if ev.ID == id && ev.Action == want[0] {
					if ev.Name != run+"-evented" || ev.Attributes["mutti.test.run"] != run || ev.Time.IsZero() {
						t.Fatalf("event %+v", ev)
					}
					want = want[1:]
				}
			case err := <-errs:
				t.Fatalf("stream: %v", err)
			case <-timeout:
				t.Fatalf("still waiting for %v", want)
			}
		}
		cancel()
		for range events { //nolint:revive // drain until the stream ends
		}
	})

	t.Run("a volume in use cannot be removed", func(t *testing.T) {
		vol := run + "-used"
		if _, err := e.CreateVolume(ctx, vol, labels); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.RemoveVolume(ctx, vol) })
		id, err := e.CreateContainer(ctx, engine.ContainerSpec{
			Name: run + "-user", Image: "busybox:1.37", Cmd: []string{"true"}, Labels: labels,
			Mounts: []engine.Mount{{Source: vol, Target: "/v", Volume: true}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.RemoveVolume(ctx, vol); !errors.Is(err, engine.ErrConflict) {
			t.Fatalf("in use: %v", err)
		}
		if err := e.RemoveContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := e.RemoveVolume(ctx, vol); err != nil {
			t.Fatalf("no longer in use: %v", err)
		}
	})

	t.Run("containers pause and resume", func(t *testing.T) {
		id := s.SeedPublished(t, labels)
		t.Cleanup(func() { _ = e.RemoveContainer(ctx, id) })
		if err := e.PauseContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
		if d, _, _ := e.InspectContainer(ctx, id); d.State != "paused" {
			t.Fatalf("state %s", d.State)
		}
		if err := e.UnpauseContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
		if d, _, _ := e.InspectContainer(ctx, id); d.State != "running" {
			t.Fatalf("state %s", d.State)
		}
	})

	t.Run("files are copied into containers", func(t *testing.T) {
		id, err := e.CreateContainer(ctx, engine.ContainerSpec{Name: run + "-copy", Image: "busybox:1.37", Cmd: []string{"sleep", "300"}, Labels: labels})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.RemoveContainer(ctx, id) })
		if err := e.CopyToContainer(ctx, id, map[string][]byte{"/etc/mutti-test/a/f.yml": []byte("one"), "/etc/mutti-test/g": []byte("two")}); err != nil {
			t.Fatal(err)
		}
		if err := e.StartContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := e.CopyToContainer(ctx, id, map[string][]byte{"/etc/mutti-test/a/f.yml": []byte("three")}); err != nil {
			t.Fatalf("overwrite in a running container: %v", err)
		}
		if err := e.CopyToContainer(ctx, run+"-missing", map[string][]byte{"/x": nil}); err == nil {
			t.Fatal("copy into a missing container: want error")
		}
		s.CheckFile(t, id, "/etc/mutti-test/a/f.yml", "three")
	})

	t.Run("network members", func(t *testing.T) {
		n, err := e.CreateNetwork(ctx, run+"-members", labels)
		if err != nil {
			t.Fatal(err)
		}
		id, err := e.CreateContainer(ctx, engine.ContainerSpec{Name: run + "-member", Image: "busybox:1.37", Cmd: []string{"sleep", "300"}, Labels: labels, Networks: []string{n.Name}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = e.RemoveContainer(ctx, id)
			_ = e.RemoveNetwork(ctx, n.ID)
		})
		if got, err := e.NetworkContainers(ctx, n.Name); err != nil || len(got) != 0 {
			t.Fatalf("a container that never ran has no endpoint yet: %v %v", got, err)
		}
		if err := e.StartContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
		got, err := e.NetworkContainers(ctx, n.Name)
		if err != nil || len(got) != 1 || got[0] != id {
			t.Fatalf("members %v %v", got, err)
		}
		if d, _, _ := e.InspectContainer(ctx, id); d.IPs[n.Name] == "" {
			t.Fatalf("no IP on %s: %+v", n.Name, d)
		}
		if got, err := e.NetworkContainers(ctx, run+"-no-such-net"); err != nil || len(got) != 0 {
			t.Fatalf("missing network: %v %v", got, err)
		}
	})

	t.Run("networks are listed and removed", func(t *testing.T) {
		id := s.SeedNetwork(t, labels)
		got, err := e.ListNetworks(ctx, marker)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != id {
			t.Fatalf("unexpected %+v", got)
		}
		if err := e.RemoveNetwork(ctx, id); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.ListNetworks(ctx, marker); len(got) != 0 {
			t.Fatalf("still listed: %+v", got)
		}
	})
}
