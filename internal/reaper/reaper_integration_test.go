//go:build integration

package reaper

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/l0wskilled/mutti/internal/engine"
)

// TestRyukReapsAfterDisconnect runs the real Ryuk: a volume it was told about disappears once
// the connection closes, and Ryuk removes itself.
func TestRyukReapsAfterDisconnect(t *testing.T) {
	ctx := context.Background()
	e, err := engine.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	run := strings.ToLower(rand.Text()[:10])
	label := map[string]string{"mutti.test.reaper": run}

	if err := e.PullImage(ctx, Image); err != nil {
		t.Fatal(err)
	}
	spec := Spec("mutti-test-reaper-"+run, "/var/run/docker.sock", label)
	spec.Env = []string{"RYUK_CONNECTION_TIMEOUT=30s", "RYUK_RECONNECTION_TIMEOUT=1s"}
	id, err := e.CreateContainer(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.RemoveContainer(ctx, id) })
	if err := e.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	cs, _ := e.ListContainers(ctx, "mutti.test.reaper="+run)
	addr := fmt.Sprintf("127.0.0.1:%d", cs[0].Ports[0].HostPort)

	vol := "mutti-test-reaped-" + run
	if _, err := e.CreateVolume(ctx, vol, map[string]string{"mutti.test.reaped": run}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.RemoveVolume(ctx, vol) })

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	filter, _ := FilterLine(map[string]string{"mutti.test.reaped": run})
	c, err := Dial(dialCtx, addr, filter)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok, _ := e.InspectVolume(ctx, vol); !ok {
			if _, alive, _ := e.InspectContainer(ctx, id); !alive {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("volume not reaped, or Ryuk still running")
}
