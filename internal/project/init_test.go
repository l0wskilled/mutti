package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/l0wskilled/mutti/internal/compose"
)

func TestNewDraft(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"compose.yaml", "compose.override.yaml", "compose.e2e.yml", "docker-compose.ci.yaml", "compose.Bad.yml", "notes.yml"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := &compose.Model{Services: map[string]compose.Service{
		"web":    {Ports: []compose.Port{{Target: 80}}},
		"api":    {Ports: []compose.Port{{Target: 8000}}},
		"db":     {Ports: []compose.Port{{Target: 5432}, {Target: 5432}, {Target: 53, Protocol: "udp"}}},
		"worker": {},
	}}
	files := Files{Dir: dir, Paths: []string{filepath.Join(dir, "compose.yaml"), filepath.Join(dir, "compose.override.yaml")}}

	d, err := NewDraft(m, files)
	if err != nil {
		t.Fatal(err)
	}
	want := Draft{
		Main:     "web",
		Routes:   map[string]uint16{"web": 80, "api": 8000},
		Overlays: map[string][]string{"e2e": {"compose.e2e.yml"}, "ci": {"docker-compose.ci.yaml"}},
		Ports:    map[string][]uint16{"db": {5432}},
	}
	if diff := cmp.Diff(want, d); diff != "" {
		t.Fatalf("draft (-want +got):\n%s", diff)
	}

	if _, err := NewDraft(m, Files{Dir: filepath.Join(dir, "gone")}); err == nil {
		t.Error("unreadable dir: want error")
	}
}

func TestDraftRender(t *testing.T) {
	d := Draft{
		Main:     "web",
		Routes:   map[string]uint16{"web": 80, "yes": 8000},
		Overlays: map[string][]string{"e2e": {"compose.e2e.yml"}},
		Ports:    map[string][]uint16{"db": {5432, 5433}},
	}
	want := `# mutti: every key is optional.
# Reference: https://github.com/l0wskilled/mutti/blob/main/docs/reference/x-mutti.md
x-mutti:
  main: web
  # HTTP routes mutti sets up now; listing routes replaces these defaults:
  # routes:
  #   web: { port: 80 }
  #   "yes": { port: 8000 }
  # A fixed host port per worktree, for database tools (default: a random one):
  # ports:
  #   db: [5432, 5433]
  overlays:
    e2e: [compose.e2e.yml]
`
	if diff := cmp.Diff(want, string(d.Render())); diff != "" {
		t.Fatalf("render (-want +got):\n%s", diff)
	}
	if got := string(Draft{}.Render()); got != "# mutti: every key is optional.\n# Reference: https://github.com/l0wskilled/mutti/blob/main/docs/reference/x-mutti.md\nx-mutti:\n" {
		t.Errorf("empty draft: %q", got)
	}
}

func TestInsert(t *testing.T) {
	block := []byte("x-mutti:\n  main: web\n")
	for name, tc := range map[string]struct{ in, want string }{
		"after leading comments": {
			in:   "# my project\n\n---\nname: shop\nservices:\n\n  web:   # front\n    image: nginx\n",
			want: "# my project\n\n---\nx-mutti:\n  main: web\n\nname: shop\nservices:\n\n  web:   # front\n    image: nginx\n",
		},
		"quoted key":  {in: "\"services\":\n  web: {}\n", want: "x-mutti:\n  main: web\n\n\"services\":\n  web: {}\n"},
		"no newline":  {in: "services: {}", want: "x-mutti:\n  main: web\n\nservices: {}"},
		"indented ok": {in: "  # x: 1\nservices: {}\n", want: "  # x: 1\nx-mutti:\n  main: web\n\nservices: {}\n"},
	} {
		got, err := Insert([]byte(tc.in), block)
		if err != nil || string(got) != tc.want {
			t.Errorf("%s: %v\n%s", name, err, cmp.Diff(tc.want, string(got)))
		}
	}
	for _, in := range []string{"{services: {}}\n", "", "# only a comment\n"} {
		if _, err := Insert([]byte(in), block); !errors.Is(err, ErrNoPlace) {
			t.Errorf("%q: %v", in, err)
		}
	}
}
