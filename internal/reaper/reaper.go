// Package reaper talks to Ryuk, the Testcontainers resource reaper: a container that removes
// everything matching the filters it was given once its client's connection is gone. mutti
// hands it the labels of an ephemeral instance before creating anything, so even a mutti that
// is killed with SIGKILL leaves nothing behind.
package reaper

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/l0wskilled/mutti/internal/engine"
)

// Image is the Ryuk release mutti runs, pinned by digest.
const Image = "testcontainers/ryuk:0.14.0@sha256:7c1a8a9a47c780ed0f983770a662f80deb115d95cce3e2daa3d12115b8cd28f0"

// handshakeTimeout bounds one attempt to register filters.
var handshakeTimeout = 2 * time.Second

// Port is Ryuk's TCP port inside its container.
const Port = 8080

// Spec returns the Ryuk container for one run. It removes itself when done, and gives a client
// time to connect and to reconnect before it prunes.
func Spec(name, socket string, labels map[string]string) engine.ContainerSpec {
	return engine.ContainerSpec{
		Name:   name,
		Image:  Image,
		Labels: labels,
		Env: []string{
			"RYUK_CONNECTION_TIMEOUT=60s",
			"RYUK_RECONNECTION_TIMEOUT=10s",
		},
		Ports:      []engine.Port{{HostIP: "127.0.0.1", ContainerPort: Port, Proto: "tcp"}},
		Mounts:     []engine.Mount{{Source: socket, Target: "/var/run/docker.sock"}},
		AutoRemove: true,
	}
}

// FilterLine renders labels as one Ryuk filter: resources need all of them to match.
func FilterLine(labels map[string]string) (string, error) {
	if len(labels) == 0 {
		return "", errors.New("a reaper filter needs at least one label")
	}
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		v := labels[k]
		if k == "" || strings.ContainsAny(k+v, "&=\n") {
			// Ryuk acknowledges filters it cannot parse, so mutti must never send one.
			return "", fmt.Errorf("label %q=%q cannot be a reaper filter", k, v)
		}
		parts = append(parts, "label="+k+"="+v)
	}
	return strings.Join(parts, "&"), nil
}

// Conn is a connection to Ryuk. Resources are reaped once it is closed (or mutti dies).
type Conn struct {
	c net.Conn
	r *bufio.Reader
}

// Dial connects to Ryuk at addr and registers filters, retrying until Ryuk acknowledges or ctx
// ends. Retrying the handshake too matters: Docker's port forwarding accepts connections before
// Ryuk listens, and then drops them.
func Dial(ctx context.Context, addr string, filters ...string) (*Conn, error) {
	var d net.Dialer
	for {
		raw, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			// A Ryuk that never answers must not block forever.
			_ = raw.SetDeadline(time.Now().Add(handshakeTimeout))
			c := &Conn{c: raw, r: bufio.NewReader(raw)}
			if err = c.Watch(filters...); err == nil {
				_ = raw.SetDeadline(time.Time{})
				return c, nil
			}
			_ = c.Close()
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connect to the reaper at %s: %w", addr, err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Watch registers filter sets; resources matching any of them are reaped. It returns once
// Ryuk acknowledged each.
func (c *Conn) Watch(filters ...string) error {
	for _, f := range filters {
		if _, err := fmt.Fprintf(c.c, "%s\n", f); err != nil {
			return fmt.Errorf("send reaper filter: %w", err)
		}
		reply, err := c.r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read reaper reply: %w", err)
		}
		if reply != "ACK\n" {
			return fmt.Errorf("reaper answered %q", reply)
		}
	}
	return nil
}

// Close ends the connection; Ryuk reaps after its reconnection timeout.
func (c *Conn) Close() error { return c.c.Close() }
