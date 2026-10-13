package app

import (
	"errors"
	"slices"
	"testing"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
)

func cloneModel() *compose.Model {
	m := webModel()
	m.XMutti = []byte(`{"hooks":{"bootstrap":[{"service":"web","run":"seed"}],"after-clone":[{"service":"web","run":"install"}]}}`)
	return m
}

func TestWorktreeAddWithDataFromMain(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, cloneModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	// A volume with an explicit name is shared by all instances and is not copied.
	_, _ = h.eng.CreateVolume(ctx, "shared-cache", map[string]string{engine.LabelComposeProject: "shop", labelComposeVolume: "cache"})
	// A stopped container must not be paused.
	h.eng.AddContainer(engine.Container{Name: "shop-old-1", State: "exited", Labels: map[string]string{engine.LabelComposeProject: "shop"}})
	var paused []string
	h.eng.OnCall = func(m string) error {
		if m == "PauseContainer" {
			paused = append(paused, m)
		}
		return nil
	}
	h.compose.Runs = nil

	res, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "copy", DataFrom: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Cloned, []string{"data"}) || res.Up.Bootstrapped {
		t.Fatalf("cloned=%v bootstrapped=%v", res.Cloned, res.Up.Bootstrapped)
	}
	runs := h.eng.Runs()
	if len(runs) != 1 || runs[0].Image != HelperImage || runs[0].Mounts[0].Source != "shop_data" || runs[0].Mounts[1].Source != "shop-copy_data" || !runs[0].Mounts[0].ReadOnly {
		t.Fatalf("copy runs %+v", runs)
	}
	v, ok, _ := h.eng.InspectVolume(ctx, "shop-copy_data")
	if !ok || v.Labels[engine.LabelComposeProject] != "shop-copy" || v.Labels[labelComposeVolume] != "data" {
		t.Fatalf("target volume %+v", v)
	}
	if len(paused) != 2 { // web and worker of the source; the exited one stays as it is
		t.Fatalf("paused %d containers", len(paused))
	}
	cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeProject+"=shop")
	for _, c := range cs {
		if c.State == "paused" {
			t.Fatalf("source left paused: %+v", c)
		}
	}
	var hooks []string
	for _, r := range h.compose.RunsWith("exec") {
		hooks = append(hooks, r[len(r)-1])
	}
	if !slices.Equal(hooks, []string{"install"}) {
		t.Fatalf("hooks %v: after-clone must run instead of bootstrap", hooks)
	}
}

func TestDataFromSources(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, cloneModel())
	first, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	for name, from := range map[string]string{"by-name": "first", "by-path": first.Path, "relative": "../shop-first"} {
		if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: name, DataFrom: from, NoUp: true}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "nothing", DataFrom: "main"}); appErrCode(err) != CodeNoData {
		t.Errorf("source without data: %v", err)
	}
	if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "nowhere", DataFrom: "/no/such/dir"}); appErrCode(err) != CodeNoSuchDir {
		t.Errorf("unknown source: %v", err)
	}
}

func TestCloneFailures(t *testing.T) {
	boom := errors.New("boom")
	for _, tt := range []struct {
		name  string
		setup func(h *harness)
	}{
		{"list volumes", func(h *harness) { h.eng.FailOn = map[string]error{"ListVolumes": engine.ErrUnavailable} }},
		{"image check", func(h *harness) { h.eng.FailOn = map[string]error{"ImageExists": engine.ErrUnavailable} }},
		{"pull", func(h *harness) { h.eng.FailOn = map[string]error{"PullImage": engine.ErrUnavailable} }},
		{"list containers", func(h *harness) {
			lists := 0
			h.eng.OnCall = func(m string) error {
				if m == "ListContainers" {
					if lists++; lists == 1 {
						return engine.ErrUnavailable
					}
				}
				return nil
			}
		}},
		{"pause", func(h *harness) { h.eng.FailOn = map[string]error{"PauseContainer": engine.ErrUnavailable} }},
		{"run", func(h *harness) { h.eng.FailOn = map[string]error{"RunContainer": engine.ErrUnavailable} }},
		{"copy exit code", func(h *harness) { h.eng.RunExit = 1 }},
		{"target volume", func(h *harness) {
			creates := 0
			h.eng.OnCall = func(m string) error {
				if m == "CreateVolume" {
					if creates++; creates == 2 { // 1: claim of the new instance
						return boom
					}
				}
				return nil
			}
		}},
		{"bootstrap marker", func(h *harness) {
			creates := 0
			h.eng.OnCall = func(m string) error {
				if m == "CreateVolume" {
					if creates++; creates == 3 {
						return boom
					}
				}
				return nil
			}
		}},
		{"claim", func(h *harness) {
			_, _ = h.eng.CreateVolume(ctx, claimVolume("shop-copy"), map[string]string{LabelOrigin: "host", engine.LabelID: "other", engine.LabelPath: "/gone"})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := newRepo(t, "shop")
			h := newHarness(t, cloneModel())
			if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
				t.Fatal(err)
			}
			if tt.name != "pull" && tt.name != "image check" {
				h.eng.AddImage(HelperImage)
			}
			tt.setup(h)
			if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "copy", DataFrom: "main"}); err == nil {
				t.Fatal("want error")
			}
			h.eng.OnCall, h.eng.FailOn = nil, nil
			cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeProject+"=shop")
			for _, c := range cs {
				if c.State == "paused" {
					t.Fatalf("source left paused after a failure: %+v", c)
				}
			}
		})
	}
}

func TestCloneHookAndTargetFailures(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, cloneModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	h.compose.Exit = func(args []string) int {
		if slices.Contains(args, "install") {
			return 2
		}
		return 0
	}
	if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "hook", DataFrom: "main"}); appErrCode(err) != CodeHookFailed {
		t.Errorf("after-clone hook: %v", err)
	}

	// A branch without a Compose file: the new worktree has no project to copy into.
	runGit(t, root, "checkout", "-q", "-b", "bare")
	runGit(t, root, "rm", "-q", "compose.yml")
	runGit(t, root, "commit", "-q", "-m", "no compose")
	runGit(t, root, "checkout", "-q", "main")
	if _, err := h.app.WorktreeAdd(ctx, root, WorktreeAddOptions{Name: "bare", Branch: "bare", DataFrom: "main"}); appErrCode(err) != CodeNoComposeFile {
		t.Errorf("target without project: %v", err)
	}
}

func TestCloneDoesNotPauseMuttisOwnContainer(t *testing.T) {
	root := newRepo(t, "shop")
	h := newHarness(t, cloneModel())
	if _, err := h.app.Up(ctx, root, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	src, err := h.app.Resolve(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	dst := *src
	dst.Name = "shop-copy"
	cs, _ := h.eng.ListContainers(ctx, engine.LabelComposeProject+"=shop")
	h.app.Host.ContainerID = cs[0].ID // mutti runs in the source's first container
	var paused []string
	h.eng.OnCall = func(m string) error {
		if m == "PauseContainer" {
			paused = append(paused, m)
		}
		return nil
	}
	if _, err := h.app.cloneVolumes(ctx, src, &dst); err != nil {
		t.Fatal(err)
	}
	if len(paused) != len(cs)-1 {
		t.Fatalf("paused %d of %d", len(paused), len(cs))
	}
	if h.app.isSelf("") || !h.app.isSelf(cs[0].ID[:3]) {
		t.Error("isSelf")
	}
}
