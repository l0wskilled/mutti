//go:build unix

package hostenv

import (
	"net/url"
	"os"
	"syscall"
)

// SocketGID returns the group owning the Docker socket of a daemon address like
// unix:///var/run/docker.sock; containers that talk to Docker need it (group_add).
func SocketGID(daemonHost string) (int, bool) {
	u, err := url.Parse(daemonHost)
	if err != nil || u.Scheme != "unix" {
		return 0, false
	}
	fi, err := os.Stat(u.Path)
	if err != nil {
		return 0, false
	}
	return int(fi.Sys().(*syscall.Stat_t).Gid), true
}
