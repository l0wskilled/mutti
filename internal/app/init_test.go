package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/l0wskilled/mutti/internal/compose"
)

const initCompose = "# shop\n\nservices:\n  web:\n    image: nginx\n"

func initRepo(t *testing.T) string {
	t.Helper()
	root := newRepo(t, "shop")
	writeFile(t, filepath.Join(root, "compose.yml"), initCompose)
	return root
}

func TestInitWritesXMuttiAndAgents(t *testing.T) {
	root := initRepo(t)
	writeFile(t, filepath.Join(root, ".mutti", "commands", "host", "seed"), "#!/bin/sh\n# Load demo data\n")
	writeFile(t, filepath.Join(root, ".mutti", "commands", "web", "quiet"), "echo\n")
	h := newHarness(t, webModel())
	var checked []string
	h.compose.ConfigFn = func(s compose.Spec) (*compose.Model, error) {
		if strings.Contains(s.Files[0], ".mutti-init-") {
			content, _ := os.ReadFile(s.Files[0])
			checked = append(checked, string(content))
		}
		return webModel(), nil
	}

	plan, err := h.app.Init(ctx, root, InitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ComposeFile != filepath.Join(root, "compose.yml") || plan.Draft.Main != "web" || !strings.Contains(plan.XMutti, "x-mutti:\n  main: web\n") {
		t.Fatalf("plan %+v", plan)
	}
	if len(checked) != 1 || !strings.HasPrefix(checked[0], "# shop\n\n# mutti:") || !strings.HasSuffix(checked[0], "\nservices:\n  web:\n    image: nginx\n") {
		t.Fatalf("compose did not check the new file: %q", checked)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, ".mutti-init-*")); len(matches) != 0 {
		t.Errorf("check file left: %v", matches)
	}
	for _, want := range []string{agentsBegin, "Services: web, worker.", "- `mutti seed`: Load demo data\n", "- `mutti quiet`: custom command\n", agentsEnd + "\n"} {
		if !strings.Contains(plan.AgentsSection, want) {
			t.Errorf("agents section lacks %q:\n%s", want, plan.AgentsSection)
		}
	}
	if b, _ := os.ReadFile(plan.ComposeFile); string(b) != initCompose {
		t.Fatal("planning wrote the Compose file")
	}

	if err := h.app.InitApply(plan); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(plan.ComposeFile); string(b) != checked[0] {
		t.Errorf("written compose %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "AGENTS.md")); string(b) != plan.AgentsSection {
		t.Errorf("written agents %q", b)
	}

	// Once the project has x-mutti and AGENTS.md is current, there is nothing to do.
	m := webModel()
	m.XMutti = []byte(`{"main":"web"}`)
	h.compose.ConfigFn = func(compose.Spec) (*compose.Model, error) { return m, nil }
	plan, err = h.app.Init(ctx, root, InitOptions{})
	if err != nil || !plan.Empty() {
		t.Fatalf("second init: %+v %v", plan, err)
	}
}

func TestInitAgentsFile(t *testing.T) {
	root := initRepo(t)
	configured := webModel()
	configured.XMutti = []byte(`{"main":"web"}`)
	h := newHarness(t, configured) // only AGENTS.md changes
	section := func() string {
		plan, err := h.app.Init(ctx, root, InitOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return plan.AgentsSection
	}
	agents := filepath.Join(root, "AGENTS.md")
	cases := []struct{ old, want func(string) string }{
		{func(string) string { return "# Rules\n" }, func(s string) string { return "# Rules\n\n" + s }},
		{func(string) string { return "# Rules" }, func(s string) string { return "# Rules\n\n" + s }},
		{
			func(string) string { return "# Rules\n" + agentsBegin + "\nold\n" + agentsEnd + "\n\nmore\n" },
			func(s string) string { return "# Rules\n" + s + "\nmore\n" },
		},
		// An unterminated section is left alone; a new one is appended.
		{func(string) string { return agentsBegin + "\n" }, func(s string) string { return agentsBegin + "\n\n" + s }},
	}
	for i, tc := range cases {
		writeFile(t, agents, tc.old(""))
		s := section()
		plan, _ := h.app.Init(ctx, root, InitOptions{})
		if err := h.app.InitApply(plan); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(agents); string(b) != tc.want(s) {
			t.Errorf("case %d: %q", i, b)
		}
	}

	_ = os.Remove(agents)
	plan, err := h.app.Init(ctx, root, InitOptions{NoAgents: true})
	if err != nil || plan.AgentsFile != "" || !plan.Empty() {
		t.Fatalf("no agents: %+v %v", plan, err)
	}
	if err := h.app.InitApply(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agents); !os.IsNotExist(err) {
		t.Error("--no-agents wrote AGENTS.md")
	}
}

func TestInitFailures(t *testing.T) {
	boom := errors.New("boom")
	t.Run("compose too old", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.compose.Ver = compose.MustParseVersion("2.20.0")
		if _, err := h.app.Init(ctx, initRepo(t), InitOptions{}); appErrCode(err) != CodeComposeTooOld {
			t.Fatal(err)
		}
	})
	t.Run("no project", func(t *testing.T) {
		h := newHarness(t, webModel())
		_, err := h.app.Init(ctx, t.TempDir(), InitOptions{})
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeNoComposeFile || !strings.Contains(e.Hint, "docker init") {
			t.Fatal(err)
		}
	})
	t.Run("unreadable project", func(t *testing.T) {
		h := newHarness(t, webModel())
		h.compose.ConfigFn = func(compose.Spec) (*compose.Model, error) { return nil, boom }
		if _, err := h.app.Init(ctx, initRepo(t), InitOptions{}); appErrCode(err) != CodeInvalidConfig {
			t.Fatal(err)
		}
	})
	t.Run("no place for x-mutti", func(t *testing.T) {
		root := initRepo(t)
		writeFile(t, filepath.Join(root, "compose.yml"), "{services: {web: {image: nginx}}}\n")
		h := newHarness(t, webModel())
		if _, err := h.app.Init(ctx, root, InitOptions{}); appErrCode(err) != CodeInvalidConfig || !strings.Contains(err.Error(), "cannot find where") {
			t.Fatal(err)
		}
	})
	for name, fn := range map[string]func(compose.Spec) (*compose.Model, error){
		"compose rejects it": func(s compose.Spec) (*compose.Model, error) {
			if strings.Contains(s.Files[0], ".mutti-init-") {
				return nil, boom
			}
			return webModel(), nil
		},
		"x-mutti does not parse": func(s compose.Spec) (*compose.Model, error) {
			if strings.Contains(s.Files[0], ".mutti-init-") {
				return &compose.Model{XMutti: []byte(`{"nope":1}`)}, nil
			}
			return webModel(), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, webModel())
			h.compose.ConfigFn = fn
			if _, err := h.app.Init(ctx, initRepo(t), InitOptions{}); appErrCode(err) != CodeInvalidConfig || !strings.Contains(err.Error(), "does not work") {
				t.Fatal(err)
			}
		})
	}
	t.Run("unreadable files", func(t *testing.T) {
		root := initRepo(t)
		h := newHarness(t, webModel())
		file := filepath.Join(root, "compose.yml")
		if err := os.Chmod(file, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := h.app.Init(ctx, root, InitOptions{}); err == nil {
			t.Error("unreadable compose file: want error")
		}
		_ = os.Chmod(file, 0o644)

		// The project dir holds the compose file, so it can be listed; read-only, the check file
		// cannot be written.
		if err := os.Chmod(root, 0o555); err != nil {
			t.Fatal(err)
		}
		_, err := h.app.Init(ctx, root, InitOptions{})
		_ = os.Chmod(root, 0o755)
		if err == nil {
			t.Error("read-only project dir: want error")
		}
	})
	t.Run("agents file", func(t *testing.T) {
		root := initRepo(t)
		h := newHarness(t, webModel())
		if err := os.Mkdir(filepath.Join(root, "AGENTS.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := h.app.Init(ctx, root, InitOptions{}); err == nil {
			t.Error("AGENTS.md is a directory: want error")
		}
	})
	t.Run("commands", func(t *testing.T) {
		root := initRepo(t)
		h := newHarness(t, webModel())
		writeFile(t, filepath.Join(root, ".mutti", "commands"), "not a dir")
		if _, err := h.app.Init(ctx, root, InitOptions{}); err == nil {
			t.Error("broken commands dir: want error")
		}
	})
	t.Run("draft", func(t *testing.T) {
		// The project dir can be entered but not listed: the overlays cannot be found.
		root := initRepo(t)
		h := newHarness(t, webModel())
		if err := os.Chmod(root, 0o311); err != nil {
			t.Fatal(err)
		}
		_, err := h.app.Init(ctx, root, InitOptions{})
		_ = os.Chmod(root, 0o755)
		if err == nil {
			t.Error("unlistable project dir: want error")
		}
	})
	t.Run("apply", func(t *testing.T) {
		root := initRepo(t)
		h := newHarness(t, webModel())
		plan, err := h.app.Init(ctx, root, InitOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// AGENTS.md became a non-empty directory: the rename fails, nothing is left behind.
		writeFile(t, filepath.Join(root, "AGENTS.md", "x"), "")
		if err := h.app.InitApply(plan); err == nil {
			t.Error("rename over a directory: want error")
		}
		if matches, _ := filepath.Glob(filepath.Join(root, ".mutti-*")); len(matches) != 0 {
			t.Errorf("temp file left: %v", matches)
		}
		if err := os.Chmod(root, 0o555); err != nil {
			t.Fatal(err)
		}
		err = h.app.InitApply(plan)
		_ = os.Chmod(root, 0o755)
		if err == nil {
			t.Error("read-only dir: want error")
		}
	})
}
