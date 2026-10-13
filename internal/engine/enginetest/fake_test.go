package enginetest

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/l0wskilled/mutti/internal/engine"
)

type fakeSeeder struct{ f *Fake }

func (s fakeSeeder) SeedContainer(_ *testing.T, labels map[string]string) string {
	return s.f.AddContainer(engine.Container{Name: "seeded", State: "created", Labels: labels})
}

func (s fakeSeeder) SeedPublished(_ *testing.T, labels map[string]string) string {
	return s.f.AddContainer(engine.Container{
		Name: "published", State: "running", Labels: labels,
		Ports: []engine.Port{{HostIP: "127.0.0.1", HostPort: 49153, ContainerPort: 80, Proto: "tcp"}},
	})
}

func (s fakeSeeder) SeedNetwork(_ *testing.T, labels map[string]string) string {
	return s.f.AddNetwork(engine.Network{Name: "seeded", Labels: labels})
}

func (s fakeSeeder) CheckFile(t *testing.T, id, path, content string) {
	t.Helper()
	if got := string(s.f.Files(id)[path]); got != content {
		t.Fatalf("%s: %q, want %q", path, got, content)
	}
}

func TestFakeContract(t *testing.T) {
	f := New()
	f.RunExit = 3
	RunContract(t, f, fakeSeeder{f})
}

func TestFakeFailOn(t *testing.T) {
	f := New()
	boom := errors.New("boom")
	f.FailOn = map[string]error{"ListVolumes": boom}
	if _, err := f.ListVolumes(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestFakePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docker.json")
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.CreateVolume(context.Background(), "v", map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	f.AddContainer(engine.Container{Name: "c", State: "running"})
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}

	g, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := g.InspectVolume(context.Background(), "v"); !ok || v.Labels["a"] != "b" {
		t.Fatalf("volume lost: %+v", v)
	}
	id := g.AddContainer(engine.Container{Name: "d"})
	if cs, _ := g.ListContainers(context.Background()); len(cs) != 2 || cs[0].ID == id && cs[1].ID == id {
		t.Fatalf("ids clash after reload: %+v", cs)
	}
}

func TestFakeReturnsCopies(t *testing.T) {
	f := New()
	v, _ := f.CreateVolume(context.Background(), "v", map[string]string{"a": "b"})
	v.Labels["a"] = "mutated"
	got, _, _ := f.InspectVolume(context.Background(), "v")
	if got.Labels["a"] != "b" {
		t.Fatal("caller mutated fake state")
	}
}
