package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/project"
)

// Selection picks optional parts of a project for a command. Commands given none use the one
// the instance was last started with, so they see the services that actually run.
type Selection struct {
	Overlays []string `json:"overlays,omitempty"` // x-mutti overlay names
	Profiles []string `json:"profiles,omitempty"` // Compose profiles
}

func (s Selection) empty() bool { return len(s.Overlays) == 0 && len(s.Profiles) == 0 }

func (a *App) selectionFile(instance string) string {
	return filepath.Join(a.Dirs.InstanceDir(instance), "selection.json")
}

// saveSelection records the selection an instance was started with.
func (a *App) saveSelection(inst *Instance, sel Selection) error {
	data, _ := json.Marshal(sel) // a struct of strings always marshals
	return os.WriteFile(a.selectionFile(inst.Name), data, 0o600)
}

// lastSelection is the selection the instance was last started with, or none.
func (a *App) lastSelection(instance string) Selection {
	var sel Selection
	if data, err := os.ReadFile(a.selectionFile(instance)); err == nil {
		_ = json.Unmarshal(data, &sel) // a damaged file means no selection
	}
	return sel
}

// prepare writes the override matching the selected files and returns the Compose spec for them.
// The override only names services of exactly this file set, so it is rendered per command.
func (a *App) prepare(ctx context.Context, inst *Instance, sel Selection) (compose.Spec, error) {
	if err := a.checkOverlays(inst, sel.Overlays); err != nil {
		return compose.Spec{}, err
	}
	spec := a.userSpec(inst, sel.Overlays, sel.Profiles)
	m := inst.Model
	if len(sel.Overlays) > 0 {
		var err error
		if m, err = a.Compose.Config(ctx, spec); err != nil {
			return compose.Spec{}, &Error{Code: CodeInvalidConfig, Message: "docker compose cannot read the project with overlays " + strings.Join(sel.Overlays, ", "), Err: err}
		}
	}

	// "+" is not allowed in overlay names, so every set of overlays gets a file of its own.
	name := "override.yml"
	if len(sel.Overlays) > 0 {
		name = "override-" + strings.Join(sel.Overlays, "+") + ".yml"
	}
	path := filepath.Join(a.Dirs.InstanceDir(inst.Name), name)
	rs, err := a.routerSettings()
	if err != nil {
		return compose.Spec{}, err
	}
	common, perService := routeLabels(inst, m, rs.Name)
	fixed, err := a.fixedPorts(ctx, inst)
	if err != nil {
		return compose.Spec{}, err
	}
	binds, err := inst.translatedBinds(m)
	if err != nil {
		return compose.Spec{}, err
	}
	content := compose.RenderOverride(m, compose.Override{
		Network: ingressNetwork(inst.Name), CommonLabels: common, Labels: perService, FixedPorts: fixed, Binds: binds,
	})
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return compose.Spec{}, err
	}
	// Replaced in one step: another mutti may be running Compose on it right now.
	if err := writeAtomic(path, content, 0o600); err != nil {
		return compose.Spec{}, err
	}
	spec.Files = append(spec.Files, path)
	return spec, nil
}

// compose runs a Compose command and turns a non-zero exit into an Error. Compose's output
// goes to the user as it comes; mutti keeps an eye on it to recognize failures it can explain.
func (a *App) compose(ctx context.Context, spec compose.Spec, args ...string) error {
	var seen tail
	streams := a.IO
	streams.Err = io.MultiWriter(a.IO.Err, &seen)
	code, err := a.Compose.Run(ctx, spec, streams, args...)
	if err != nil {
		return err
	}
	if code == 0 {
		return nil
	}
	if strings.Contains(seen.String(), engine.PoolsExhaustedMessage) {
		return &Error{Code: CodePoolsExhausted, Message: "Docker has no subnet left for another network", Hint: poolsHint}
	}
	return &Error{Code: CodeComposeFailed, Message: fmt.Sprintf("docker compose %s failed with exit code %d", args[0], code)}
}

// tail keeps the last few KiB written to it.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	const keep = 8 << 10
	t.b = append(t.b, p...)
	if len(t.b) > keep {
		t.b = t.b[len(t.b)-keep:]
	}
	return len(p), nil
}

func (t *tail) String() string { return string(t.b) }

// UpOptions configure Up.
type UpOptions struct {
	Selection
	Build    bool     // rebuild images before starting
	Services []string // start only these and what they depend on; empty: all

	afterClone bool // run after-clone hooks: the data was just copied from another instance
}

// UpResult describes a started instance.
type UpResult struct {
	Instance     *Instance
	Bootstrapped bool   // the bootstrap hooks ran during this up
	URL          string // main URL through the router, empty if nothing is routed
}

// Up claims the instance, starts it and waits until it is healthy, then runs the hooks.
func (a *App) Up(ctx context.Context, dir string, opts UpOptions) (*UpResult, error) {
	if err := a.checkCompose(ctx); err != nil {
		return nil, err
	}
	inst, err := a.Resolve(ctx, dir)
	if err != nil {
		return nil, err
	}
	if err := refuseRun(inst, "start"); err != nil {
		return nil, err
	}
	a.touch(inst.Name)
	if err := a.claim(ctx, inst); err != nil {
		return nil, err
	}
	// Every network counts against Docker's address pools: finish what dead runs left first.
	if err := a.Sweep(ctx, staleRuns); err != nil {
		fmt.Fprintf(a.IO.Err, "mutti: could not sweep old runs: %v\n", err)
	}
	if err := a.ensureIngress(ctx, inst); err != nil {
		return nil, err
	}
	spec, err := a.prepare(ctx, inst, opts.Selection)
	if err != nil {
		return nil, err
	}
	if err := a.compose(ctx, spec, upArgs(opts.Build, opts.Services)...); err != nil {
		return nil, err
	}
	if err := a.saveSelection(inst, opts.Selection); err != nil {
		return nil, err
	}

	res := &UpResult{Instance: inst}
	if _, done, err := a.Engine.InspectVolume(ctx, bootstrapVolume(inst.Name)); err != nil {
		return nil, engineErr(err)
	} else if !done {
		skipped, err := a.runHooks(ctx, inst, spec, "bootstrap", inst.Config.Hooks.Bootstrap)
		if err != nil {
			return nil, err
		}
		// A bootstrap is done when every hook ran; skipped ones run on a later up.
		if skipped == 0 {
			if _, err := a.Engine.CreateVolume(ctx, bootstrapVolume(inst.Name), a.labels(inst, RoleBootstrap)); err != nil {
				return nil, engineErr(err)
			}
			res.Bootstrapped = true
		}
	}
	if opts.afterClone {
		if _, err := a.runHooks(ctx, inst, spec, "after-clone", inst.Config.Hooks.AfterClone); err != nil {
			return nil, err
		}
	}
	if _, err := a.runHooks(ctx, inst, spec, "post-up", inst.Config.Hooks.PostUp); err != nil {
		return nil, err
	}
	status, err := a.Reconcile(ctx)
	if err != nil {
		return res, err
	}
	if err := a.checkHosts(status, inst); err != nil {
		return res, err
	}
	if slices.Contains(status.Instances, inst.Name) {
		if err := a.waitReady(ctx, status, inst); err != nil {
			return res, err
		}
		res.URL = hostURL(inst.Hostname, status.HTTPSPort)
	}
	return res, nil
}

// Stop stops the instance's containers and keeps everything else.
func (a *App) Stop(ctx context.Context, dir string, sel Selection) (*Instance, error) {
	inst, spec, err := a.resolvePrepared(ctx, dir, sel)
	if err != nil {
		return nil, err
	}
	if err := a.compose(ctx, everyProfile(spec), "stop"); err != nil {
		return nil, err
	}
	a.refreshRouter(ctx)
	return inst, nil
}

// Down removes the instance's containers and networks and keeps its volumes.
func (a *App) Down(ctx context.Context, dir string, sel Selection) (*Instance, error) {
	inst, spec, err := a.resolvePrepared(ctx, dir, sel)
	if err != nil {
		return nil, err
	}
	a.runPreDown(ctx, inst, spec)
	if err := a.compose(ctx, everyProfile(spec), "down", "--remove-orphans"); err != nil {
		return nil, err
	}
	a.refreshRouter(ctx)
	return inst, nil
}

// Destroy removes everything of the instance: containers, networks, volumes, its claim and
// generated files. The identity file stays, so the next up uses the same name.
func (a *App) Destroy(ctx context.Context, dir string) (*Instance, error) {
	inst, spec, err := a.resolvePrepared(ctx, dir, Selection{})
	if err != nil {
		return nil, err
	}
	if err := refuseRun(inst, "destroy"); err != nil {
		return nil, err
	}
	a.runPreDown(ctx, inst, spec)
	// Compose knows the project's volumes and images; whatever it misses, purge finds by label.
	if err := a.compose(ctx, everyProfile(spec), "down", "--volumes", "--remove-orphans"); err != nil {
		return nil, err
	}
	if err := a.purge(ctx, inst.Name); err != nil {
		return nil, err
	}
	a.refreshRouter(ctx)
	return inst, nil
}

// Reset deletes the instance's data and starts it fresh: containers and volumes go, the
// bootstrap hooks run again. Name, claim and routes stay.
func (a *App) Reset(ctx context.Context, dir string, opts UpOptions) (*UpResult, error) {
	inst, spec, err := a.resolvePrepared(ctx, dir, opts.Selection)
	if err != nil {
		return nil, err
	}
	if err := refuseRun(inst, "reset"); err != nil {
		return nil, err
	}
	if opts.empty() {
		opts.Selection = a.lastSelection(inst.Name)
	}
	a.runPreDown(ctx, inst, spec)
	if err := a.compose(ctx, everyProfile(spec), "down", "--volumes", "--remove-orphans"); err != nil {
		return nil, err
	}
	if err := a.Engine.RemoveVolume(ctx, bootstrapVolume(inst.Name)); err != nil {
		return nil, engineErr(err)
	}
	return a.Up(ctx, dir, opts)
}

// everyProfile widens a spec to all profiles. Stopping and removing must reach services started
// by name, such as one behind a profile, which Compose leaves alone otherwise.
func everyProfile(s compose.Spec) compose.Spec {
	s.Profiles = []string{"*"}
	return s
}

// refreshRouter updates a running router after instances went away. It never starts one, and
// a failure only warns: tearing down must not depend on the router.
func (a *App) refreshRouter(ctx context.Context) {
	s, err := a.routerSettings()
	if err == nil {
		var ok bool
		if _, ok, err = a.Engine.InspectContainer(ctx, s.Name); err == nil && ok {
			_, err = a.Reconcile(ctx)
		}
	}
	if err != nil {
		fmt.Fprintf(a.IO.Err, "mutti: could not update the router: %v\n", err)
	}
}

func (a *App) resolvePrepared(ctx context.Context, dir string, sel Selection) (*Instance, compose.Spec, error) {
	if err := a.checkCompose(ctx); err != nil {
		return nil, compose.Spec{}, err
	}
	inst, err := a.Resolve(ctx, dir)
	if err != nil {
		return nil, compose.Spec{}, err
	}
	if err := a.ensureOwner(ctx, inst); err != nil {
		return nil, compose.Spec{}, err
	}
	// Using the instance counts for tidy --idle; looking at it (status, url, list) does not.
	a.touch(inst.Name)
	if sel.empty() {
		sel = a.lastSelection(inst.Name)
	}
	spec, err := a.prepare(ctx, inst, sel)
	return inst, spec, err
}

// runPreDown runs pre-down hooks. Their failure is reported but does not block tearing down,
// because a broken service must still be removable.
func (a *App) runPreDown(ctx context.Context, inst *Instance, spec compose.Spec) {
	if _, err := a.runHooks(ctx, inst, spec, "pre-down", inst.Config.Hooks.PreDown); err != nil {
		fmt.Fprintf(a.IO.Err, "mutti: %v (continuing)\n", err)
	}
}

// upArgs are the arguments of the `docker compose up` that starts an instance.
func upArgs(build bool, services []string) []string {
	args := []string{"up", "--detach", "--wait", "--remove-orphans"}
	if build {
		args = append(args, "--build")
	}
	return append(args, services...)
}

// runHooks runs hooks in order and returns how many it skipped: a hook needs its service
// running, which it is not when only some services were started.
func (a *App) runHooks(ctx context.Context, inst *Instance, spec compose.Spec, kind string, hooks []project.Hook) (int, error) {
	if len(hooks) == 0 {
		return 0, nil
	}
	containers, err := a.Engine.ListContainers(ctx, engine.LabelComposeProject+"="+inst.Name)
	if err != nil {
		return 0, engineErr(err)
	}
	running := map[string]bool{}
	for _, c := range containers {
		running[c.Labels[engine.LabelComposeService]] = running[c.Labels[engine.LabelComposeService]] || c.State == "running"
	}
	skipped := 0
	for i, h := range hooks {
		if !running[h.Service] {
			fmt.Fprintf(a.IO.Err, "mutti: skipping %s hook %d: %s is not running\n", kind, i+1, h.Service)
			skipped++
			continue
		}
		user := h.User
		if user == "" {
			user = inst.Config.Exec.User
		}
		uargs, err := a.userArgs(ctx, inst, user)
		if err != nil {
			return skipped, err
		}
		args := append([]string{"exec", "-T"}, uargs...)
		args = append(args, h.Service, "sh", "-c", h.Run)
		code, err := a.Compose.Run(ctx, spec, a.IO, args...)
		if err != nil {
			return skipped, err
		}
		if code != 0 {
			return skipped, &Error{
				Code:    CodeHookFailed,
				Message: fmt.Sprintf("%s hook %d (%s: %s) failed with exit code %d", kind, i+1, h.Service, h.Run, code),
			}
		}
	}
	return skipped, nil
}

// userArgs returns the `docker compose exec` flags that run a command as the configured user,
// plus the MUTTI_* environment. The host user is only mapped where the daemon writes bind mounts
// with container uids: rootless Docker and Docker Desktop already map container root to the user.
func (a *App) userArgs(ctx context.Context, inst *Instance, user string) ([]string, error) {
	var args []string
	switch user {
	case project.UserService:
	case project.UserHost:
		info, err := a.Engine.Info(ctx)
		if err != nil {
			return nil, engineErr(err)
		}
		if !info.Rootless && info.OS != "Docker Desktop" {
			args = append(args, "--user", strconv.Itoa(a.Host.UID)+":"+strconv.Itoa(a.Host.GID), "--env", "HOME="+inst.Config.Exec.Home)
		}
	default:
		args = append(args, "--user", user)
	}
	for _, kv := range a.Env(inst) {
		args = append(args, "--env", kv)
	}
	return args, nil
}

// ExecOptions configure Exec.
type ExecOptions struct {
	User    string   // overrides x-mutti exec.user
	Workdir string   // working directory in the container; empty: the image's
	Env     []string // KEY=VALUE pairs on top of the MUTTI_* variables
}

// Exec runs a command in a service of the instance and returns its exit code.
func (a *App) Exec(ctx context.Context, dir, service string, cmd []string, opts ExecOptions) (int, error) {
	inst, spec, err := a.resolvePrepared(ctx, dir, Selection{})
	if err != nil {
		return 0, err
	}
	user := opts.User
	if user == "" {
		user = inst.Config.Exec.User
	}
	args := []string{"exec"}
	if !a.Host.StdinTTY {
		args = append(args, "-T")
	}
	uargs, err := a.userArgs(ctx, inst, user)
	if err != nil {
		return 0, err
	}
	args = append(args, uargs...)
	if opts.Workdir != "" {
		args = append(args, "--workdir", opts.Workdir)
	}
	for _, kv := range opts.Env {
		args = append(args, "--env", kv)
	}
	args = append(append(args, service), cmd...)
	return a.Compose.Run(ctx, spec, a.IO, args...)
}

// ComposePassthrough runs any `docker compose` command against the instance. It is the escape hatch for
// everything mutti has no command of its own for.
func (a *App) ComposePassthrough(ctx context.Context, dir string, sel Selection, args []string) (int, error) {
	_, spec, err := a.resolvePrepared(ctx, dir, sel)
	if err != nil {
		return 0, err
	}
	return a.Compose.Run(ctx, spec, a.IO, args...)
}
