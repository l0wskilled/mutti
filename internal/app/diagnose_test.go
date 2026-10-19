package app

import (
	"strings"
	"testing"
	"time"

	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/project"
)

func TestDiagnoseExplainsWhatIsWrong(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	h.compose.Stdout = func(args []string) string {
		if args[0] == "logs" {
			return args[len(args)-1] + " says goodbye\n"
		}
		return ""
	}
	if got, err := h.app.Diagnose(ctx, root); err != nil || len(got) != 0 {
		t.Fatalf("all well: %+v %v", got, err)
	}

	add := func(service, state, status string, r engine.Runtime) {
		id := h.eng.AddContainer(engine.Container{State: state, Status: status, Labels: map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: service}})
		h.eng.SetRuntime(id, r)
	}
	probe := engine.HealthProbe{Start: time.Unix(10, 0), ExitCode: 1, Output: "connection refused\n"}
	add("sick", "running", "Up 1 minute (unhealthy)", engine.Runtime{Health: []engine.HealthProbe{probe}})
	add("silent", "running", "Up 1 minute (unhealthy)", engine.Runtime{})
	add("loop", "restarting", "Restarting (1) 2 seconds ago", engine.Runtime{RestartCount: 7, ExitCode: 1})
	add("oom", "exited", "Exited (137) 1 minute ago", engine.Runtime{OOMKilled: true, ExitCode: 137})
	add("broken", "created", "Created", engine.Runtime{Error: "exec: \"nope\": executable file not found"})
	add("crashed", "exited", "Exited (127) 1 minute ago", engine.Runtime{ExitCode: 127})
	add("zombie", "dead", "Dead", engine.Runtime{})
	add("stopped", "exited", "Exited (0) 1 minute ago", engine.Runtime{})
	add("termed", "exited", "Exited (143) 1 minute ago", engine.Runtime{ExitCode: 143})
	// A one-shot that did its job is fine, though it exited.
	h.eng.AddContainer(engine.Container{State: "running", Labels: map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: "app", labelDependsOn: "deps:service_completed_successfully:false"}})
	add("deps", "exited", "Exited (0) 1 minute ago", engine.Runtime{})

	got, err := h.app.Diagnose(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	problems := map[string]Diagnosis{}
	for _, d := range got {
		problems[d.Service] = d
	}
	for service, want := range map[string]string{
		"sick":    "unhealthy: its health check exits with 1",
		"silent":  "unhealthy: its health check fails",
		"loop":    "crashing: restarted 7 times, last exit code 1",
		"oom":     "killed: out of memory",
		"crashed": "exited with 127: the command was not found",
		"zombie":  "dead: Docker could not stop or remove it",
	} {
		if d := problems[service]; d.Problem != want || d.Hint == "" || d.Logs != service+" says goodbye\n" {
			t.Errorf("%s: %+v", service, d)
		}
	}
	if len(got) != 6 || got[0].Service != "crashed" {
		t.Errorf("only the troubled ones, sorted: %+v", got)
	}
	if h := problems["sick"].Health; len(h) != 1 || h[0].Output != "connection refused" || !h[0].Time.Equal(time.Unix(10, 0)) {
		t.Errorf("health %+v", h)
	}

	// Named services are explained whatever their state.
	named, err := h.app.Diagnose(ctx, root, "broken", "stopped", "termed", "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range named {
		problems[d.Service] = d
	}
	for service, want := range map[string]string{
		"broken":  "could not start: exec",
		"stopped": "stopped",
		"termed":  "exited with 143: stopped (SIGTERM)",
		"web":     "no problem found: Up",
	} {
		if !strings.HasPrefix(problems[service].Problem, want) {
			t.Errorf("%s: %q", service, problems[service].Problem)
		}
	}
}

func TestExitProblems(t *testing.T) {
	for code, want := range map[int]string{126: "not executable", 137: "SIGKILL", 1: "exited with 1"} {
		if got := exitProblem(code); !strings.Contains(got, want) {
			t.Errorf("%d: %q", code, got)
		}
	}
}

func TestDiagnoseFailures(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, webModel())
	if _, err := h.app.Diagnose(ctx, t.TempDir()); appErrCode(err) != CodeNoComposeFile {
		t.Fatalf("outside a project: %v", err)
	}
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	h.eng.FailOn = map[string]error{"ListContainers": engine.ErrUnavailable}
	if _, err := h.app.Diagnose(ctx, root); appErrCode(err) != CodeDockerUnavailable {
		t.Fatalf("list: %v", err)
	}
	h.eng.FailOn = map[string]error{"InspectContainer": engine.ErrUnavailable}
	if _, err := h.app.Diagnose(ctx, root, "web"); appErrCode(err) != CodeDockerUnavailable {
		t.Fatalf("inspect: %v", err)
	}
	h.eng.FailOn = nil
	writeFile(t, selectionFile(&Instance{Files: project.Files{Dir: root}}), `{"overlays":["gone"]}`)
	if _, err := h.app.Diagnose(ctx, root, "web"); err == nil {
		t.Fatal("an overlay the project no longer has: want error")
	}
}
