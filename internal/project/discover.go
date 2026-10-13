// Package project finds a project's Compose files and reads its x-mutti configuration.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// composeNames are the file names Compose looks for, in its order of preference.
var composeNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// overrideNames are the override files Compose merges automatically when no -f is given. mutti
// always passes -f, so it adds them itself.
var overrideNames = []string{"compose.override.yaml", "compose.override.yml", "docker-compose.override.yaml", "docker-compose.override.yml"}

// ErrNoComposeFile means no Compose file was found.
var ErrNoComposeFile = errors.New("no Compose file found")

// Files are the Compose files of a project and the directory Compose treats as project dir.
type Files struct {
	Dir   string
	Paths []string // absolute, in merge order
}

// Discover finds the Compose files for start the way `docker compose` would: $COMPOSE_FILE if
// set, else the closest compose.yaml (or variant) in start or a parent, up to and including stop.
// An empty stop means up to the filesystem root.
func Discover(start, stop string, getenv func(string) string) (Files, error) {
	if list := getenv("COMPOSE_FILE"); list != "" {
		return fromEnv(start, list, getenv("COMPOSE_PATH_SEPARATOR"))
	}
	dir := start
	for {
		if f, ok := inDir(dir); ok {
			return f, nil
		}
		if dir == stop {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if stop != "" {
		return inSubdir(start, stop)
	}
	return Files{}, notFound(start)
}

func notFound(start string) error {
	return fmt.Errorf("%w in %s or its parents (looked for %s)", ErrNoComposeFile, start, strings.Join(composeNames, ", "))
}

// inSubdir finds the project in a direct subdirectory of root, for repositories that keep their
// Compose files in e.g. packages/ or docker/. It only picks one when exactly one qualifies.
func inSubdir(start, root string) (Files, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return Files{}, err
	}
	var found []Files
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if f, ok := inDir(filepath.Join(root, e.Name())); ok {
			found = append(found, f)
		}
	}
	switch len(found) {
	case 0:
		return Files{}, notFound(start)
	case 1:
		return found[0], nil
	}
	dirs := make([]string, len(found))
	for i, f := range found {
		dirs[i] = f.Dir
	}
	return Files{}, fmt.Errorf("%w: several subdirectories have one (%s); run mutti in the one you mean", ErrNoComposeFile, strings.Join(dirs, ", "))
}

func fromEnv(start, list, sep string) (Files, error) {
	if sep == "" {
		sep = string(os.PathListSeparator)
	}
	var paths []string
	for _, p := range strings.Split(list, sep) {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(start, p)
		}
		if _, err := os.Stat(p); err != nil {
			return Files{}, fmt.Errorf("COMPOSE_FILE: %w", err)
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return Files{}, fmt.Errorf("%w: COMPOSE_FILE is empty", ErrNoComposeFile)
	}
	return Files{Dir: filepath.Dir(paths[0]), Paths: paths}, nil
}

func inDir(dir string) (Files, bool) {
	main := first(dir, composeNames)
	if main == "" {
		return Files{}, false
	}
	f := Files{Dir: dir, Paths: []string{main}}
	if o := first(dir, overrideNames); o != "" {
		f.Paths = append(f.Paths, o)
	}
	return f, true
}

func first(dir string, names []string) string {
	for _, n := range names {
		p := filepath.Join(dir, n)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}
