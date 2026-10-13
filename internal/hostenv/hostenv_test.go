package hostenv

import (
	"io/fs"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/l0wskilled/mutti/internal/engine"
)

type files map[string]string

func (f files) read(p string) ([]byte, error) {
	if s, ok := f[p]; ok {
		return []byte(s), nil
	}
	return nil, fs.ErrNotExist
}

const id = "d7849ae8aaa9e208ba21502dd4e6ea5613f8e9ea5d9e69aa4d29f83b6d69c631"

func TestSelfContainer(t *testing.T) {
	mountinfo := "528 519 259:2 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw - ext4 /dev/x rw\n"
	tests := []struct {
		name   string
		fs     files
		want   string
		wantOK bool
	}{
		{"host", files{"/proc/self/mountinfo": mountinfo}, "", false},
		{"docker", files{"/.dockerenv": "", "/proc/self/mountinfo": mountinfo, "/etc/hostname": "other\n"}, id, true},
		{"podman", files{"/run/.containerenv": "", "/proc/self/mountinfo": mountinfo}, id, true},
		{"hostname fallback", files{"/.dockerenv": "", "/proc/self/mountinfo": "nothing", "/etc/hostname": "d7849ae8aaa9\nignored\n"}, "d7849ae8aaa9", true},
		{"nothing to go by", files{"/.dockerenv": "", "/etc/hostname": "\n"}, "", false},
	}
	for _, tt := range tests {
		got, ok := SelfContainer(tt.fs.read)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("%s: %q %v", tt.name, got, ok)
		}
	}
}

func TestPathMap(t *testing.T) {
	m := NewPathMap([]engine.Mount{
		{Source: "/home/u/shop", Target: "/app"},
		{Source: "/home/u/shop/.git", Target: "/app/.git"},
		{Source: "/var/lib/docker/volumes/cache/_data", Target: "/cache", Volume: true},
		{Source: "/ci/override-me", Target: "/builds"},
	}, map[string]string{"/builds/": "/srv/ci/build-42"})
	for in, want := range map[string]string{
		"/app":                "/home/u/shop",
		"/app/www":            "/home/u/shop/www",
		"/app/.git/HEAD":      "/home/u/shop/.git/HEAD",
		"/cache/x":            "/var/lib/docker/volumes/cache/_data/x",
		"/builds/repo/a":      "/srv/ci/build-42/repo/a",
		"/application/x":      "",
		"/tmp/only-in-here/a": "",
	} {
		got, ok := m.ToHost(in)
		if got != want || ok != (want != "") {
			t.Errorf("%s: %q %v", in, got, ok)
		}
	}
	root := NewPathMap([]engine.Mount{{Source: "/host-root", Target: "/"}}, nil)
	if got, _ := root.ToHost("/etc/x"); got != "/host-root/etc/x" {
		t.Errorf("root mount: %q", got)
	}
	if got := NewPathMap([]engine.Mount{{Source: "/s", Target: "/t"}}, nil).Lines(); !cmp.Equal(got, []string{"/t -> /s"}) {
		t.Errorf("lines %v", got)
	}
}

func TestDockerSocket(t *testing.T) {
	none := func(string) string { return "" }
	for _, tc := range []struct {
		getenv func(string) string
		host   string
		want   string
	}{
		{none, "unix:///run/user/1000/docker.sock", "/run/user/1000/docker.sock"},
		{none, "tcp://10.0.0.1:2376", "/var/run/docker.sock"},
		{none, "::bad", "/var/run/docker.sock"},
		{func(string) string { return "/host/docker.sock" }, "unix:///var/run/docker.sock", "/host/docker.sock"},
	} {
		if got := DockerSocket(tc.getenv, tc.host); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.host, got, tc.want)
		}
	}
}
