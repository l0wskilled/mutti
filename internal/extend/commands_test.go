package extend

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestCommands(t *testing.T) {
	user := filepath.Join(t.TempDir(), "user")
	project := filepath.Join(t.TempDir(), ".mutti")
	write(t, filepath.Join(user, "commands", "host", "hello"), "#!/bin/sh\n# Say hello\necho hi\n")
	write(t, filepath.Join(user, "commands", "host", "mine"), "echo mine\n")
	write(t, filepath.Join(project, "commands", "host", "hello"), "#!/bin/sh\n\n# Project hello\n")
	write(t, filepath.Join(project, "commands", "api", "db-migrate"), "#!/bin/bash\n# Migrate the api DB\n")
	write(t, filepath.Join(project, "commands", "document", "db-migrate"), "bin/console migrate\n")
	write(t, filepath.Join(project, "commands", "api", "up"), "# shadows a built-in\n")
	write(t, filepath.Join(project, "commands", "api", ".hidden"), "")
	write(t, filepath.Join(project, "commands", "README.md"), "not a command dir")
	if err := os.MkdirAll(filepath.Join(project, "commands", "api", "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmds, shadowed, err := Commands([]string{"up"}, user, project, filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Command{
		{Name: "api:db-migrate", Service: "api", Path: filepath.Join(project, "commands", "api", "db-migrate"), Short: "Migrate the api DB"},
		{Name: "document:db-migrate", Service: "document", Path: filepath.Join(project, "commands", "document", "db-migrate"), Short: "custom command"},
		{Name: "hello", Path: filepath.Join(project, "commands", "host", "hello"), Short: "Project hello"},
		{Name: "mine", Path: filepath.Join(user, "commands", "host", "mine"), Short: "custom command"},
	}
	if diff := cmp.Diff(want, cmds); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if !cmp.Equal(shadowed, []string{"up"}) {
		t.Errorf("shadowed %v", shadowed)
	}
}

func TestCommandsErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	for name, setup := range map[string]func(dir string){
		"commands unreadable": func(dir string) {
			write(t, filepath.Join(dir, "commands", "host", "x"), "")
			_ = os.Chmod(filepath.Join(dir, "commands"), 0)
		},
		"service dir unreadable": func(dir string) {
			write(t, filepath.Join(dir, "commands", "api", "x"), "")
			_ = os.Chmod(filepath.Join(dir, "commands", "api"), 0o100)
		},
		"script unreadable": func(dir string) {
			write(t, filepath.Join(dir, "commands", "api", "x"), "")
			_ = os.Chmod(filepath.Join(dir, "commands", "api", "x"), 0)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			setup(dir)
			t.Cleanup(func() {
				_ = filepath.WalkDir(dir, func(p string, _ os.DirEntry, _ error) error { _ = os.Chmod(p, 0o755); return nil })
				_ = os.Chmod(filepath.Join(dir, "commands"), 0o755)
				_ = os.Chmod(filepath.Join(dir, "commands", "api"), 0o755)
			})
			if _, _, err := Commands(nil, dir); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestFindProjectDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".mutti"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "repo", "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := FindProjectDir(deep); ok {
		t.Error("must not look above the repository root")
	}
	if err := os.MkdirAll(filepath.Join(root, "repo", ".mutti"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, ok := FindProjectDir(deep); !ok || got != filepath.Join(root, "repo", ".mutti") {
		t.Errorf("got %q %v", got, ok)
	}
	if _, ok := FindProjectDir(t.TempDir()); ok {
		t.Error("found one in an empty dir")
	}
	if got, ok := FindProjectDir(root); !ok || got != filepath.Join(root, ".mutti") {
		t.Errorf("outside git: %q %v", got, ok)
	}
}

func TestInterpreter(t *testing.T) {
	for script, want := range map[string]string{
		"#!/bin/bash\necho": "bash", "#!/usr/bin/env bash\n": "bash", "#!/bin/sh\n": "sh", "echo hi": "sh", "": "sh",
	} {
		if got := Interpreter([]byte(script)); got != want {
			t.Errorf("%q: %s", script, got)
		}
	}
}
