package home

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnv(t *testing.T) {
	for _, tt := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"HOME": "/home/u"}, "/home/u/.mutti"},
		{map[string]string{"HOME": "/home/u", "XDG_STATE_HOME": "/s"}, "/home/u/.mutti"},
		{map[string]string{"HOME": "/home/u", "MUTTI_HOME": "/m"}, "/m"},
		{map[string]string{"HOME": "/home/u", "MUTTI_HOME": "rel"}, "/home/u/.mutti"},
	} {
		got, err := FromEnv(env(tt.env))
		if err != nil || got != (Dirs{State: tt.want, Data: tt.want, Config: tt.want}) {
			t.Errorf("%v: %+v %v", tt.env, got, err)
		}
	}
	if _, err := FromEnv(env(nil)); err == nil {
		t.Error("want error without HOME")
	}
}

func TestPaths(t *testing.T) {
	d := Dirs{State: "/m"}
	if got := d.LockFile("router"); got != "/m/locks/router.lock" {
		t.Errorf("LockFile = %s", got)
	}
	if got := d.InstanceDir("shop-main"); got != "/m/instances/shop-main" {
		t.Errorf("InstanceDir = %s", got)
	}
}

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMigrate(t *testing.T) {
	home := t.TempDir()
	data := t.TempDir()
	write(t, filepath.Join(home, ".local", "state", "mutti", "router", "x.yml"))
	write(t, filepath.Join(data, "mutti", "ca", "ca.pem"))
	getenv := env(map[string]string{"HOME": home, "XDG_DATA_HOME": data, "XDG_CONFIG_HOME": "rel"})
	dir := filepath.Join(home, ".mutti")
	moved, err := Migrate(getenv, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(data, "mutti"), filepath.Join(home, ".local", "state", "mutti")}
	slices.Sort(moved)
	slices.Sort(want)
	if !slices.Equal(moved, want) {
		t.Errorf("moved %v", moved)
	}
	for _, p := range []string{"router/x.yml", "ca/ca.pem"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "mutti")); !os.IsNotExist(err) {
		t.Error("the old directory is left")
	}
	// Once ~/.mutti exists, nothing moves any more.
	write(t, filepath.Join(home, ".config", "mutti", "commands", "x"))
	if moved, err := Migrate(getenv, dir); err != nil || moved != nil {
		t.Errorf("again: %v %v", moved, err)
	}
}

func TestMigrateFailures(t *testing.T) {
	// Two old directories hold the same entry: the second cannot move.
	home := t.TempDir()
	write(t, filepath.Join(home, ".local", "state", "mutti", "same", "a"))
	write(t, filepath.Join(home, ".local", "share", "mutti", "same", "b"))
	if _, err := Migrate(env(map[string]string{"HOME": home}), filepath.Join(home, ".mutti")); err == nil {
		t.Error("collision: want error")
	}
	// ~/.mutti cannot be made.
	if os.Getuid() == 0 {
		t.Skip("root makes directories anywhere")
	}
	home = t.TempDir()
	write(t, filepath.Join(home, ".config", "mutti", "x"))
	locked := filepath.Join(home, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(env(map[string]string{"HOME": home}), filepath.Join(locked, ".mutti")); err == nil {
		t.Error("mkdir: want error")
	}
}
