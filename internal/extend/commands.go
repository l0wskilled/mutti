// Package extend finds the extensions a project or user adds to mutti without changing it:
// custom commands in .mutti/commands.
package extend

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// HostDir is the directory of commands that run on the host; every other directory under
// commands/ is the name of the service its commands run in.
const HostDir = "host"

// Command is one custom command.
type Command struct {
	Name    string // command name as typed: <file> or, when ambiguous, <dir>:<file>
	Service string // service to run in; empty: the host
	Path    string // script file
	Short   string // one-line help: the first "# " comment
}

// FindProjectDir returns the .mutti directory of the project containing dir: the closest one
// on the way up, not beyond the directory holding .git. ok is false if there is none.
func FindProjectDir(dir string) (string, bool) {
	for {
		candidate := filepath.Join(dir, ".mutti")
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return candidate, true
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Commands lists the custom commands of the given .mutti directories, later ones overriding
// earlier ones (user dir first, project dir last). reserved names (built-in commands) are
// skipped and reported in shadowed.
func Commands(reserved []string, dirs ...string) (cmds []Command, shadowed []string, err error) {
	byKey := map[string]Command{} // "<dir>/<file>"
	for _, d := range dirs {
		root := filepath.Join(d, "commands")
		entries, err := os.ReadDir(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		for _, sub := range entries {
			if !sub.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(root, sub.Name()))
			if err != nil {
				return nil, nil, err
			}
			for _, f := range files {
				if f.IsDir() || strings.HasPrefix(f.Name(), ".") {
					continue
				}
				c := Command{Name: f.Name(), Path: filepath.Join(root, sub.Name(), f.Name())}
				if sub.Name() != HostDir {
					c.Service = sub.Name()
				}
				byKey[sub.Name()+"/"+f.Name()] = c
			}
		}
	}

	count := map[string]int{}
	for _, c := range byKey {
		count[c.Name]++
	}
	for _, key := range slices.Sorted(maps.Keys(byKey)) {
		c := byKey[key]
		if count[c.Name] > 1 {
			c.Name = strings.Replace(key, "/", ":", 1)
		}
		if slices.Contains(reserved, c.Name) {
			shadowed = append(shadowed, c.Name)
			continue
		}
		if c.Short, err = short(c.Path); err != nil {
			return nil, nil, err
		}
		cmds = append(cmds, c)
	}
	return cmds, shadowed, nil
}

// short returns the first "# " comment line of a script, after an optional shebang.
func short(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#!") || line == "" {
			continue
		}
		if text, ok := strings.CutPrefix(line, "# "); ok {
			return text, nil
		}
		break
	}
	return "custom command", nil
}

// Interpreter is the shell that runs a service command's script: bash if its shebang names
// bash, sh otherwise.
func Interpreter(script []byte) string {
	first, _, _ := bytes.Cut(script, []byte("\n"))
	if bytes.HasPrefix(first, []byte("#!")) && bytes.Contains(first, []byte("bash")) {
		return "bash"
	}
	return "sh"
}
