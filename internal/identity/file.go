package identity

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Identity is the name tag of one worktree's instance. It is written once and never recomputed,
// so renaming a directory or switching branches keeps the instance.
type Identity struct {
	Name string `json:"name"` // worktree part of the instance name
	ID   string `json:"id"`   // random, tells a moved worktree apart from a copied one
}

// NewID returns a random identity id.
func NewID() string { return rand.Text() }

// FilePath returns where the identity of a worktree lives: inside its git admin dir when there
// is one (moves with `git worktree move`, needs no gitignore), else under <root>/.mutti/local.
func FilePath(gitDir, root string) string {
	if gitDir != "" {
		return filepath.Join(gitDir, "mutti", "identity.json")
	}
	return filepath.Join(root, ".mutti", "local", "identity.json")
}

// Load reads an identity file. ok is false if it does not exist.
func Load(path string) (id Identity, ok bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Identity{}, false, nil
	}
	if err != nil {
		return Identity{}, false, err
	}
	if err := json.Unmarshal(b, &id); err != nil {
		return Identity{}, false, fmt.Errorf("%s: %w", path, err)
	}
	if !Valid(id.Name) || id.ID == "" {
		return Identity{}, false, fmt.Errorf("%s: invalid identity %+v", path, id)
	}
	return id, true, nil
}

// Save writes an identity file atomically. For a file under .mutti/local it also makes sure
// .mutti/.gitignore ignores local/.
func Save(path string, id Identity) error {
	if !Valid(id.Name) || id.ID == "" {
		return fmt.Errorf("refusing to save invalid identity %+v", id)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // .mutti/ is part of the user's repo
		return err
	}
	if filepath.Base(dir) == "local" && filepath.Base(filepath.Dir(dir)) == ".mutti" {
		if err := EnsureLocalIgnored(filepath.Dir(dir)); err != nil {
			return err
		}
	}
	b, _ := json.MarshalIndent(id, "", "  ") // two strings, cannot fail
	return writeAtomic(path, append(b, '\n'), 0o600)
}

// EnsureLocalIgnored makes sure <muttiDir>/.gitignore exists, ignoring local/: machine-local
// files such as run logs never show up in git.
func EnsureLocalIgnored(muttiDir string) error {
	if err := os.MkdirAll(muttiDir, 0o755); err != nil { //nolint:gosec // .mutti/ is part of the user's repo
		return err
	}
	path := filepath.Join(muttiDir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return writeAtomic(path, []byte("local/\n"), 0o644)
}

// writeAtomic writes via a temp file in the same dir and a rename, so readers never see a
// half-written file.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+NewID())
	if err := os.WriteFile(tmp, data, perm); err != nil { //nolint:gosec // paths come from mutti, not from input
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// EnsureExcluded adds pattern to the repository's info/exclude (in its common git dir, so it
// applies to every worktree) unless it is there: git ignores it without any change to the repo.
func EnsureExcluded(commonDir, pattern string) error {
	path := filepath.Join(commonDir, "info", "exclude")
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		b = append(b, '\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // git's own dir
		return err
	}
	return writeAtomic(path, append(b, pattern+"\n"...), 0o644)
}
