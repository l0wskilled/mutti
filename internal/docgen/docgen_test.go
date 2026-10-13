package docgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/spf13/cobra"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/project"
)

const repoRoot = "../.."

// compiled returns a validator for the generated x-mutti schema.
func compiled(t *testing.T) *jsonschema.Schema {
	t.Helper()
	s, err := Schema(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(encode(s))))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("x-mutti.json", doc); err != nil {
		t.Fatal(err)
	}
	v, err := c.Compile("x-mutti.json")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestSchemaAgreesWithTheParser checks that the schema editors use accepts and rejects exactly
// what mutti itself does.
func TestSchemaAgreesWithTheParser(t *testing.T) {
	v := compiled(t)
	model := &compose.Model{Services: map[string]compose.Service{"web": {}, "api": {}, "db": {}}}
	for _, tc := range []struct {
		config string
		valid  bool
	}{
		{`{}`, true},
		{`{"name":"shop","main":"web","routes":{"api":{"host":"api","path":"/api","port":8000}},
		  "ports":{"db":[5432]},"worktree":{"copy":[".env.local"]},"exec":{"user":"host","home":"/tmp"},
		  "hooks":{"bootstrap":[{"service":"api","run":"migrate","user":"service"}],"post-up":[],"pre-down":[],
		  "after-clone":[],"sweep":[]},"overlays":{"e2e":["compose.e2e.yml"]}}`, true},
		{`{"mian":"web"}`, false},
		{`{"routes":{"api":{"hots":"x"}}}`, false},
		{`{"ports":{"db":["5432"]}}`, false},
		{`{"routes":{"api":{"path":"/a,b"}}}`, false},
		{`{"routes":{"api":{"path":"/v1/api-docs_x.~"}}}`, true},
		{`{"hooks":{"bootstrap":[{"service":"api","run":"x","when":"always"}]}}`, false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(tc.config), &doc); err != nil {
			t.Fatal(err)
		}
		schemaOK := v.Validate(doc) == nil
		model.XMutti = []byte(tc.config)
		_, parseErr := project.ParseConfig(model)
		if schemaOK != tc.valid || (parseErr == nil) != tc.valid {
			t.Errorf("%s: schema valid=%v, parser err=%v, want valid=%v", tc.config, schemaOK, parseErr, tc.valid)
		}
	}
}

func TestGenerate(t *testing.T) {
	dir := t.TempDir()
	root := &cobra.Command{Use: "mutti", Short: "root"}
	root.AddCommand(&cobra.Command{Use: "up", Short: "Start it", Run: func(*cobra.Command, []string) {}})
	if err := Generate(repoRoot, dir, root); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{CLIDir + "/mutti.md", CLIDir + "/mutti_up.md", XMuttiSchema, ComposeSchema, XMuttiMarkdown} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	md, _ := os.ReadFile(filepath.Join(dir, XMuttiMarkdown))
	for _, want := range []string{"| `routes.<name>.host` | string |", "| `hooks.bootstrap[].run` | string |", "| `ports` | map of list of integer |"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
	cs, _ := os.ReadFile(filepath.Join(dir, ComposeSchema))
	if !strings.Contains(string(cs), ComposeSpecSchema) || !strings.Contains(string(cs), `"x-mutti"`) {
		t.Errorf("compose schema %s", cs)
	}
	if cli, _ := os.ReadFile(filepath.Join(dir, CLIDir, "mutti_up.md")); strings.Contains(string(cli), "Auto generated") {
		t.Error("generated docs must not carry a date")
	}
}

func TestGenerateFailures(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	root := &cobra.Command{Use: "mutti"}
	if err := Generate(repoRoot, file, root); err == nil {
		t.Error("docs dir below a file: want error")
	}
	if err := Generate("/no/such/repo", t.TempDir(), root); err == nil {
		t.Error("no sources: want error")
	}

	// A command whose name makes an impossible file name.
	bad := &cobra.Command{Use: "mutti"}
	bad.AddCommand(&cobra.Command{Use: "a/b", Run: func(*cobra.Command, []string) {}})
	if err := Generate(repoRoot, t.TempDir(), bad); err == nil {
		t.Error("bad command name: want error")
	}

	// A generated file's place is taken by a directory.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, XMuttiSchema), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Generate(repoRoot, dir, root); err == nil {
		t.Error("blocked file: want error")
	}

	if os.Geteuid() != 0 {
		// The old CLI pages cannot be removed.
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, CLIDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, CLIDir, "old.md"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		cli := filepath.Join(dir, CLIDir)
		if err := os.Chmod(cli, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cli, 0o755) })
		if err := Generate(repoRoot, dir, root); err == nil {
			t.Error("old pages not removable: want error")
		}

		// reference/ exists but cli/ cannot be created in it.
		dir = t.TempDir()
		ref := filepath.Join(dir, filepath.Dir(CLIDir))
		if err := os.MkdirAll(ref, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(ref, 0o755) })
		if err := Generate(repoRoot, dir, root); err == nil {
			t.Error("cli dir not creatable: want error")
		}
	}
}
