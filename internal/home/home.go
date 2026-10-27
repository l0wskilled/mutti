// Package home resolves ~/.mutti, the directory mutti keeps its own files in: the local CA, the
// router's configuration, locks and the user's own commands.
package home

import (
	"errors"
	"os"
	"path/filepath"
)

// Dirs are mutti's own directories. They are one, ~/.mutti; the fields say what goes where.
type Dirs struct {
	State  string // runtime state: locks, the router's configuration
	Data   string // durable data: the local CA
	Config string // the user's own commands
}

// FromEnv is $MUTTI_HOME, or ~/.mutti.
func FromEnv(getenv func(string) string) (Dirs, error) {
	dir := getenv("MUTTI_HOME")
	if dir == "" || !filepath.IsAbs(dir) {
		home := getenv("HOME")
		if home == "" {
			return Dirs{}, errors.New("neither $MUTTI_HOME nor $HOME is set")
		}
		dir = filepath.Join(home, ".mutti")
	}
	return Dirs{State: dir, Data: dir, Config: dir}, nil
}

// LockFile returns the path of a named host-wide lock.
func (d Dirs) LockFile(name string) string {
	return filepath.Join(d.State, "locks", name+".lock")
}

// InstanceDir returns the directory holding generated files of one instance.
func (d Dirs) InstanceDir(instance string) string {
	return filepath.Join(d.State, "instances", instance)
}

// Migrate moves what an older mutti kept in the XDG directories (~/.local/state/mutti,
// ~/.local/share/mutti, ~/.config/mutti, or where $XDG_*_HOME point) into dir, so the local CA
// and with it the browsers' trust carry over. It runs only while dir does not exist, and
// returns the old directories it emptied.
func Migrate(getenv func(string) string, dir string) ([]string, error) {
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var moved []string
	for env, fallback := range map[string]string{"XDG_STATE_HOME": ".local/state", "XDG_DATA_HOME": ".local/share", "XDG_CONFIG_HOME": ".config"} {
		base := getenv(env)
		if base == "" || !filepath.IsAbs(base) {
			base = filepath.Join(getenv("HOME"), fallback)
		}
		old := filepath.Join(base, "mutti")
		entries, err := os.ReadDir(old)
		if err != nil {
			continue // nothing there
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return moved, err
		}
		for _, e := range entries {
			if err := os.Rename(filepath.Join(old, e.Name()), filepath.Join(dir, e.Name())); err != nil {
				return moved, err
			}
		}
		_ = os.Remove(old)
		moved = append(moved, old)
	}
	return moved, nil
}
