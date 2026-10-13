// Package xdg resolves the directories mutti keeps its own files in, following the XDG Base
// Directory spec with the usual fallbacks under $HOME.
package xdg

import (
	"errors"
	"path/filepath"
)

// Dirs are mutti's own directories, already suffixed with "mutti".
type Dirs struct {
	State  string // runtime state: locks, generated Compose overrides
	Data   string // durable data: the local CA
	Config string // user-level extensions and settings
}

// FromEnv resolves the directories from the environment.
func FromEnv(getenv func(string) string) (Dirs, error) {
	home := getenv("HOME")
	pick := func(env, fallback string) (string, error) {
		if v := getenv(env); v != "" && filepath.IsAbs(v) {
			return filepath.Join(v, "mutti"), nil
		}
		if home == "" {
			return "", errors.New("neither $" + env + " nor $HOME is set")
		}
		return filepath.Join(home, fallback, "mutti"), nil
	}

	var d Dirs
	var err error
	if d.State, err = pick("XDG_STATE_HOME", ".local/state"); err != nil {
		return Dirs{}, err
	}
	if d.Data, err = pick("XDG_DATA_HOME", ".local/share"); err != nil {
		return Dirs{}, err
	}
	if d.Config, err = pick("XDG_CONFIG_HOME", ".config"); err != nil {
		return Dirs{}, err
	}
	return d, nil
}

// LockFile returns the path of a named host-wide lock.
func (d Dirs) LockFile(name string) string {
	return filepath.Join(d.State, "locks", name+".lock")
}

// InstanceDir returns the directory holding generated files of one instance.
func (d Dirs) InstanceDir(instance string) string {
	return filepath.Join(d.State, "instances", instance)
}
