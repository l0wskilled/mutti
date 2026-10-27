package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/gitdir"
)

// HelperImage runs mutti's one-off jobs, such as copying volumes. Pinned by digest.
const HelperImage = "busybox:1.37@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

// Compose's label naming the volume within its project.
const labelComposeVolume = "com.docker.compose.volume"

// CodeNoData means the instance to copy data from has none.
const CodeNoData = "no-data"

// sourceRoot finds the worktree to copy data from: "main", a worktree name, an instance name as
// mutti list shows it, or a path.
func (a *App) sourceRoot(ctx context.Context, dir string, info gitdir.Info, from string) string {
	if from == "main" {
		return info.MainRoot()
	}
	if filepath.IsAbs(from) {
		return from
	}
	if _, err := os.Stat(filepath.Join(dir, from)); err == nil {
		return filepath.Join(dir, from)
	}
	path := a.worktreePath(info, from)
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if list, err := a.List(ctx); err == nil {
		for _, s := range list {
			if s.Name == from && s.Path != "" {
				return s.Path
			}
		}
	}
	return path
}

// cloneVolumes copies every named volume of src into the matching volume of dst, as Compose
// will name it. src's containers are paused meanwhile, so the copy is consistent the way a
// sudden power loss is: databases recover from it. It returns the copied volumes' names
// within the project.
func (a *App) cloneVolumes(ctx context.Context, src, dst *Instance) ([]string, error) {
	volumes, err := a.Engine.ListVolumes(ctx, engine.LabelComposeProject+"="+src.Name)
	if err != nil {
		return nil, engineErr(err)
	}
	if len(volumes) == 0 {
		return nil, &Error{Code: CodeNoData, Message: src.Name + " has no data to copy", Hint: "start it once with mutti up"}
	}
	if err := a.ensureImage(ctx, HelperImage); err != nil {
		return nil, err
	}

	containers, err := a.Engine.ListContainers(ctx, engine.LabelComposeProject+"="+src.Name)
	if err != nil {
		return nil, engineErr(err)
	}
	var paused []string
	defer func() {
		for _, id := range paused {
			_ = a.Engine.UnpauseContainer(context.WithoutCancel(ctx), id)
		}
	}()
	for _, c := range containers {
		// mutti itself may run in one of them (a dev container); paused, it would wait forever.
		if c.State != "running" || a.isSelf(c.ID) {
			continue
		}
		if err := a.Engine.PauseContainer(ctx, c.ID); err != nil {
			return nil, engineErr(err)
		}
		paused = append(paused, c.ID)
	}

	var copied []string
	for _, v := range volumes {
		logical := v.Labels[labelComposeVolume]
		if v.Name != src.Name+"_"+logical {
			continue // named explicitly in the Compose file: shared by every instance anyway
		}
		target := dst.Name + "_" + logical
		if _, err := a.Engine.CreateVolume(ctx, target, map[string]string{
			engine.LabelComposeProject: dst.Name,
			labelComposeVolume:         logical,
		}); err != nil {
			return nil, engineErr(err)
		}
		code, err := a.Engine.RunContainer(ctx, engine.ContainerSpec{
			Image:  HelperImage,
			Cmd:    []string{"cp", "-a", "/from/.", "/to/"},
			Labels: map[string]string{engine.LabelSchema: engine.SchemaVersion, engine.LabelInstance: dst.Name, engine.LabelRole: "clone"},
			Mounts: []engine.Mount{
				{Source: v.Name, Target: "/from", ReadOnly: true, Volume: true},
				{Source: target, Target: "/to", Volume: true},
			},
		})
		if err != nil {
			return nil, engineErr(err)
		}
		if code != 0 {
			return nil, &Error{Code: CodeNoData, Message: fmt.Sprintf("copying volume %s failed with exit code %d", logical, code)}
		}
		copied = append(copied, logical)
	}
	return copied, nil
}

// ensureImage pulls an image unless it is there.
func (a *App) ensureImage(ctx context.Context, ref string) error {
	ok, err := a.Engine.ImageExists(ctx, ref)
	if err != nil {
		return engineErr(err)
	}
	if !ok {
		return engineErr(a.Engine.PullImage(ctx, ref))
	}
	return nil
}

// isSelf reports whether id is the container mutti runs in. Docker ids come full or shortened.
func (a *App) isSelf(id string) bool {
	self := a.Host.ContainerID
	return self != "" && id != "" && (strings.HasPrefix(id, self) || strings.HasPrefix(self, id))
}
