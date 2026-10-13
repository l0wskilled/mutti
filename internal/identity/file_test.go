package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilePath(t *testing.T) {
	if got := FilePath("/r/.git/worktrees/x", "/w"); got != "/r/.git/worktrees/x/mutti/identity.json" {
		t.Errorf("git: %s", got)
	}
	if got := FilePath("", "/w"); got != "/w/.mutti/local/identity.json" {
		t.Errorf("no git: %s", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin", "mutti", "identity.json")
	want := Identity{Name: "feature-x", ID: NewID()}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(path)
	if err != nil || !ok || got != want {
		t.Fatalf("Load = %+v, %v, %v", got, ok, err)
	}
}

func TestLoadMissing(t *testing.T) {
	_, ok, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestLoadRejectsCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"garbage":  "{not json",
		"badname":  `{"name":"Bad_Name","id":"x"}`,
		"noid":     `{"name":"ok"}`,
		"emptyobj": `{}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(path); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestSaveRefusesInvalid(t *testing.T) {
	if err := Save(filepath.Join(t.TempDir(), "i.json"), Identity{Name: "-x", ID: "1"}); err == nil {
		t.Fatal("want error")
	}
}

func TestSaveUnderMuttiLocalWritesGitignore(t *testing.T) {
	root := t.TempDir()
	if err := Save(FilePath("", root), Identity{Name: "x", ID: "1"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".mutti", ".gitignore"))
	if err != nil || string(b) != "local/\n" {
		t.Fatalf("gitignore = %q, %v", b, err)
	}
}

func TestSaveKeepsExistingGitignore(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mutti"), 0o755); err != nil {
		t.Fatal(err)
	}
	custom := []byte("local/\nother/\n")
	if err := os.WriteFile(filepath.Join(root, ".mutti", ".gitignore"), custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(FilePath("", root), Identity{Name: "x", ID: "1"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, ".mutti", ".gitignore"))
	if string(b) != string(custom) {
		t.Fatalf("gitignore overwritten: %q", b)
	}
}

func TestNewIDIsRandom(t *testing.T) {
	if a, b := NewID(), NewID(); a == b {
		t.Fatal("two ids are equal")
	}
}

func TestLoadReadError(t *testing.T) {
	if _, _, err := Load(t.TempDir()); err == nil {
		t.Fatal("want error when the path is a directory")
	}
}

func TestSaveErrors(t *testing.T) {
	skipIfRoot(t)
	id := Identity{Name: "x", ID: "1"}

	t.Run("dir cannot be created", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := Save(filepath.Join(file, "identity.json"), id); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("gitignore cannot be written", func(t *testing.T) {
		root := t.TempDir()
		local := filepath.Join(root, ".mutti", "local")
		if err := os.MkdirAll(local, 0o755); err != nil {
			t.Fatal(err)
		}
		readOnly(t, filepath.Join(root, ".mutti"))
		if err := Save(filepath.Join(local, "identity.json"), id); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("temp file cannot be written", func(t *testing.T) {
		dir := t.TempDir()
		readOnly(t, dir)
		if err := Save(filepath.Join(dir, "identity.json"), id); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("rename fails and leaves no temp file", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "identity.json")
		if err := os.MkdirAll(filepath.Join(target, "blocker"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := Save(target, id); err == nil {
			t.Fatal("want error")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Fatalf("temp file left behind: %v", entries)
		}
	})
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
}

func readOnly(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func TestEnsureLocalIgnoredFailures(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLocalIgnored(filepath.Join(file, ".mutti")); err == nil {
		t.Fatal("want error")
	}
}

func TestEnsureExcluded(t *testing.T) {
	common := filepath.Join(t.TempDir(), ".git")
	for range 2 {
		if err := EnsureExcluded(common, "/.mutti/local/"); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(common, "info", "exclude"))
	if string(b) != "/.mutti/local/\n" {
		t.Fatalf("exclude %q", b)
	}
	if err := os.WriteFile(filepath.Join(common, "info", "exclude"), []byte("# git's comments\n*.tmp"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureExcluded(common, "/.mutti/local/"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(common, "info", "exclude"))
	if string(b) != "# git's comments\n*.tmp\n/.mutti/local/\n" {
		t.Fatalf("exclude %q", b)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureExcluded(filepath.Join(file, ".git"), "x"); err == nil {
		t.Error("common dir below a file: want error")
	}
	dirAsFile := filepath.Join(t.TempDir(), ".git")
	if err := os.MkdirAll(filepath.Join(dirAsFile, "info", "exclude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureExcluded(dirAsFile, "x"); err == nil {
		t.Error("exclude is a directory: want error")
	}
}

func TestEnsureExcludedUnwritableGitDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	common := filepath.Join(t.TempDir(), ".git")
	if err := os.Mkdir(common, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(common, 0o700) })
	if err := EnsureExcluded(common, "x"); err == nil {
		t.Fatal("want error")
	}
}
