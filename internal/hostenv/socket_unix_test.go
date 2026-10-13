//go:build unix

package hostenv

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestSocketGID(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if gid, ok := SocketGID("unix://" + sock); !ok || gid != os.Getgid() {
		t.Errorf("gid %d %v", gid, ok)
	}
	for _, host := range []string{"tcp://127.0.0.1:2375", "unix://" + filepath.Join(t.TempDir(), "none.sock"), "::bad"} {
		if _, ok := SocketGID(host); ok {
			t.Errorf("%s: want unknown", host)
		}
	}
}
