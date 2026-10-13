package reaper

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeRyuk answers each line with reply and records what it got.
func fakeRyuk(t *testing.T, reply string) (addr string, got chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	got = make(chan string, 10)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				close(got)
				return
			}
			got <- line
			if reply == "" {
				return
			}
			_, _ = c.Write([]byte(reply))
		}
	}()
	return l.Addr().String(), got
}

func TestWatch(t *testing.T) {
	addr, got := fakeRyuk(t, "ACK\n")
	c, err := Dial(context.Background(), addr, "label=a=b", "label=c=d&label=e=f")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for l := range got {
		lines = append(lines, l)
	}
	if strings.Join(lines, "") != "label=a=b\nlabel=c=d&label=e=f\n" {
		t.Fatalf("got %q", lines)
	}
}

func TestWatchFailures(t *testing.T) {
	handshakeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { handshakeTimeout = 2 * time.Second })
	for name, reply := range map[string]string{"bad reply": "NOPE\n", "hang-up": ""} {
		addr, _ := fakeRyuk(t, reply)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		if _, err := Dial(ctx, addr, "label=a=b"); err == nil {
			t.Errorf("%s: want error", name)
		}
		cancel()
	}

	// Writing to a connection the other side closed fails.
	addr, _ := fakeRyuk(t, "ACK\n")
	c, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if err := c.Watch("label=a=b"); err == nil {
		t.Error("write after close: want error")
	}
}

func TestDialRetriesUntilCancelled(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close() // nobody listens there now
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if _, err := Dial(ctx, addr); err == nil {
		t.Fatal("want error")
	}
}

func TestFilterLine(t *testing.T) {
	got, err := FilterLine(map[string]string{"mutti.run": "x1", "com.docker.compose.project": "shop-run-x1"})
	if err != nil || got != "label=com.docker.compose.project=shop-run-x1&label=mutti.run=x1" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range []map[string]string{nil, {"": "x"}, {"a": "b&c"}, {"a": "b=c"}, {"a\n": "b"}} {
		if _, err := FilterLine(bad); err == nil {
			t.Errorf("%v: want error", bad)
		}
	}
}

func TestSpec(t *testing.T) {
	s := Spec("mutti-reaper-x", "/var/run/docker.sock", map[string]string{"mutti.run": "x"})
	if s.Image != Image || !s.AutoRemove || s.Mounts[0].Source != "/var/run/docker.sock" || s.Ports[0].ContainerPort != Port || s.Labels["mutti.run"] != "x" {
		t.Fatalf("%+v", s)
	}
}
