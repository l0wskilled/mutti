package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// unreachable returns a client for a socket nobody listens on.
func unreachable(t *testing.T) *Moby {
	t.Helper()
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(t.TempDir(), "none.sock"))
	m, err := Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestUnreachableDaemon(t *testing.T) {
	m := unreachable(t)
	ctx := context.Background()
	calls := map[string]func() error{
		"Info":            func() error { _, err := m.Info(ctx); return err },
		"CreateVolume":    func() error { _, err := m.CreateVolume(ctx, "v", nil); return err },
		"InspectVolume":   func() error { _, _, err := m.InspectVolume(ctx, "v"); return err },
		"ListVolumes":     func() error { _, err := m.ListVolumes(ctx); return err },
		"RemoveVolume":    func() error { return m.RemoveVolume(ctx, "v") },
		"ListContainers":  func() error { _, err := m.ListContainers(ctx, "a=b"); return err },
		"RemoveContainer": func() error { return m.RemoveContainer(ctx, "c") },
		"ListNetworks":    func() error { _, err := m.ListNetworks(ctx); return err },
		"RemoveNetwork":   func() error { return m.RemoveNetwork(ctx, "n") },
		"CreateNetwork":   func() error { _, err := m.CreateNetwork(ctx, "n", nil); return err },
		"networkByName":   func() error { _, err := m.networkByName(ctx, "n"); return err },
		"ConnectNetwork":  func() error { return m.ConnectNetwork(ctx, "n", "c", nil) },
		"Disconnect":      func() error { return m.DisconnectNetwork(ctx, "n", "c") },
		"ImageExists":     func() error { _, err := m.ImageExists(ctx, "busybox"); return err },
		"PullImage":       func() error { return m.PullImage(ctx, "busybox") },
		"CreateContainer": func() error { _, err := m.CreateContainer(ctx, ContainerSpec{Image: "busybox"}); return err },
		"StartContainer":  func() error { return m.StartContainer(ctx, "c") },
		"Inspect":         func() error { _, _, err := m.InspectContainer(ctx, "c"); return err },
		"RunContainer":    func() error { _, err := m.RunContainer(ctx, ContainerSpec{Image: "busybox"}); return err },
		"Pause":           func() error { return m.PauseContainer(ctx, "c") },
		"Unpause":         func() error { return m.UnpauseContainer(ctx, "c") },
		"Copy":            func() error { return m.CopyToContainer(ctx, "c", map[string][]byte{"/a/b": nil}) },
		"Members":         func() error { _, err := m.NetworkContainers(ctx, "n"); return err },
		"Events": func() error {
			events, errs := m.Events(ctx, "a=b")
			for range events { //nolint:revive // drain until the stream ends
			}
			return <-errs
		},
	}
	for name, call := range calls {
		if err := call(); !IsUnavailable(err) {
			t.Errorf("%s: want ErrUnavailable, got %v", name, err)
		}
	}
}

func TestDaemonHost(t *testing.T) {
	if m := unreachable(t); !strings.HasPrefix(m.DaemonHost(), "unix://") {
		t.Fatalf("host %q", m.DaemonHost())
	}
}

func TestWrapKeepsOtherErrors(t *testing.T) {
	other := errors.New("other")
	if err := wrap(other); !errors.Is(err, other) || IsUnavailable(err) {
		t.Fatalf("got %v", err)
	}
	if wrap(nil) != nil {
		t.Fatal("wrap(nil) != nil")
	}
}

func TestConnectRejectsBadHost(t *testing.T) {
	t.Setenv("DOCKER_HOST", "::bad::")
	if _, err := Connect(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestConnectWithoutDockerCLI(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("PATH", t.TempDir())
	m, err := Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
}

func TestCreateContainerRejectsBadPorts(t *testing.T) {
	m := unreachable(t)
	for _, p := range []Port{{HostIP: "not-an-ip", ContainerPort: 80, Proto: "tcp"}, {HostIP: "127.0.0.1", ContainerPort: 80}} {
		_, err := m.CreateContainer(context.Background(), ContainerSpec{Image: "x", Ports: []Port{p}})
		if err == nil || IsUnavailable(err) {
			t.Errorf("%+v: want a validation error, got %v", p, err)
		}
	}
}

// fakeDaemon answers like a Docker daemon whose address pools are used up.
func fakeDaemon(t *testing.T) *Moby {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.52")
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			_, _ = w.Write([]byte("OK"))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"` + PoolsExhaustedMessage + `"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+srv.Listener.Addr().String())
	m, err := Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestCreateNetworkReportsExhaustedPools(t *testing.T) {
	_, err := fakeDaemon(t).CreateNetwork(context.Background(), "n", nil)
	if !errors.Is(err, ErrAddressPoolsExhausted) {
		t.Fatalf("got %v", err)
	}
}
