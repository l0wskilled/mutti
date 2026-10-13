package compose

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// fakeDocker writes a docker stand-in that prints its argv (one per line), then the env vars
// named in $SHOW_ENV, then $FAKE_STDOUT, and exits with $FAKE_EXIT.
func fakeDocker(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/sh
if [ -n "$FAKE_STDOUT" ]; then printf '%s' "$FAKE_STDOUT"; exit "${FAKE_EXIT:-0}"; fi
for a in "$@"; do echo "arg:$a"; done
for k in $SHOW_ENV; do eval "echo env:$k=\${$k-unset}"; done
echo "pwd:$(pwd)"
echo "to-stderr" >&2
exit "${FAKE_EXIT:-0}"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunBuildsArgvEnvAndDir(t *testing.T) {
	dir := t.TempDir()
	cli := CLI{Docker: fakeDocker(t), Environ: []string{
		"PATH=" + os.Getenv("PATH"),
		"SHOW_ENV=COMPOSE_FILE COMPOSE_PROJECT_NAME COMPOSE_PROFILES KEEP MUTTI_INSTANCE",
		"COMPOSE_FILE=evil.yml", "COMPOSE_PROJECT_NAME=evil", "COMPOSE_PROFILES=evil", "KEEP=yes",
	}}
	spec := Spec{
		Project: "shop-x", Dir: dir, Files: []string{"compose.yml", "/state/override.yml"},
		Profiles: []string{"tools"}, Env: []string{"MUTTI_INSTANCE=shop-x"},
	}
	var out, errOut bytes.Buffer
	code, err := cli.Run(context.Background(), spec, IO{Out: &out, Err: &errOut}, "up", "-d")
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	want := strings.Join([]string{
		"arg:compose", "arg:--project-name", "arg:shop-x", "arg:--project-directory", "arg:" + dir,
		"arg:--file", "arg:compose.yml", "arg:--file", "arg:/state/override.yml",
		"arg:--profile", "arg:tools", "arg:up", "arg:-d",
		"env:COMPOSE_FILE=unset", "env:COMPOSE_PROJECT_NAME=unset", "env:COMPOSE_PROFILES=unset",
		"env:KEEP=yes", "env:MUTTI_INSTANCE=shop-x", "pwd:" + dir, "",
	}, "\n")
	if diff := cmp.Diff(want, out.String()); diff != "" {
		t.Errorf("output (-want +got):\n%s", diff)
	}
	if errOut.String() != "to-stderr\n" {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestRunPassesExitCodeThrough(t *testing.T) {
	cli := CLI{Docker: fakeDocker(t), Environ: []string{"PATH=" + os.Getenv("PATH"), "FAKE_EXIT=23"}}
	code, err := cli.Run(context.Background(), Spec{Dir: t.TempDir()}, IO{})
	if err != nil || code != 23 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestRunStartFailure(t *testing.T) {
	cli := CLI{Docker: fakeDocker(t)}
	_, err := cli.Run(context.Background(), Spec{Dir: filepath.Join(t.TempDir(), "missing")}, IO{})
	if err == nil {
		t.Fatal("want error when the project dir does not exist")
	}
}

func TestMissingDocker(t *testing.T) {
	cli := CLI{Docker: filepath.Join(t.TempDir(), "nope")}
	ctx := context.Background()
	if _, err := cli.Run(ctx, Spec{}, IO{}); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Run: %v", err)
	}
	if _, err := cli.Version(ctx); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Version: %v", err)
	}
	if _, err := cli.Config(ctx, Spec{}); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Config: %v", err)
	}
}

func TestDefaultDockerBinaryAndEnviron(t *testing.T) {
	dir := t.TempDir()
	bin := fakeDocker(t)
	if err := os.Rename(bin, filepath.Join(dir, "docker")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_STDOUT", "v2.30.0")
	v, err := CLI{}.Version(context.Background())
	if err != nil || v != (Version{2, 30, 0}) {
		t.Fatalf("v=%v err=%v", v, err)
	}
}

func fakeWithStdout(t *testing.T, stdout string, exit string) CLI {
	t.Helper()
	return CLI{Docker: fakeDocker(t), Environ: []string{"PATH=" + os.Getenv("PATH"), "FAKE_STDOUT=" + stdout, "FAKE_EXIT=" + exit}}
}

func TestVersion(t *testing.T) {
	v, err := fakeWithStdout(t, "2.24.4-desktop.1\n", "0").Version(context.Background())
	if err != nil || v != (Version{2, 24, 4}) {
		t.Fatalf("v=%v err=%v", v, err)
	}
}

func TestVersionWithoutComposePlugin(t *testing.T) {
	_, err := fakeWithStdout(t, "docker: 'compose' is not a docker command", "1").Version(context.Background())
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled, got %v", err)
	}
}

func TestConfigParsesModelAndEnablesAllProfiles(t *testing.T) {
	cli := fakeWithStdout(t, `{"name":"p","services":{"web":{"image":"nginx","ports":[{"target":80,"published":"8080","protocol":"tcp"}]}},"x-mutti":{"main":"web"}}`, "0")
	m, err := cli.Config(context.Background(), Spec{Project: "p", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if m.Services["web"].Ports[0].Published != "8080" || string(m.XMutti) != `{"main":"web"}` {
		t.Fatalf("unexpected model %+v", m)
	}
}

func TestConfigErrors(t *testing.T) {
	if _, err := fakeWithStdout(t, "not json", "0").Config(context.Background(), Spec{Dir: t.TempDir()}); err == nil {
		t.Error("want parse error")
	}
	if _, err := fakeWithStdout(t, "", "1").Config(context.Background(), Spec{Dir: t.TempDir()}); err == nil {
		t.Error("want error on non-zero exit")
	}
}

func TestBuildxVersion(t *testing.T) {
	v, err := fakeWithStdout(t, "github.com/docker/buildx 0.37.1 abc\n", "0").BuildxVersion(context.Background())
	if err != nil || v != "github.com/docker/buildx 0.37.1 abc" {
		t.Fatalf("v=%q err=%v", v, err)
	}
	if _, err := fakeWithStdout(t, "", "1").BuildxVersion(context.Background()); err == nil {
		t.Error("want error on non-zero exit")
	}
	if _, err := (CLI{Docker: filepath.Join(t.TempDir(), "nope")}).BuildxVersion(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("missing docker: %v", err)
	}
}

func TestRunCancelSendsSigterm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\ntrap 'echo got-term; exit 143' TERM\necho started\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var out syncBuffer
	done := make(chan int)
	go func() {
		code, _ := CLI{Docker: path}.Run(ctx, Spec{Dir: t.TempDir()}, IO{Out: &out, Err: &out})
		done <- code
	}()
	for !strings.Contains(out.String(), "started") {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 143 || !strings.Contains(out.String(), "got-term") {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
