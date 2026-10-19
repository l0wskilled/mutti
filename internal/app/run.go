package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/engine"
	"github.com/l0wskilled/mutti/internal/identity"
	"github.com/l0wskilled/mutti/internal/reaper"
)

// Labels of ephemeral runs.
const (
	LabelRun    = "mutti.run"    // run id, on everything a run creates
	LabelReaper = "mutti.reaper" // run id, on the run's Ryuk container (which must not reap itself)
	RoleReaper  = "reaper"
)

// staleRuns is how old a run's leftovers must be before any run sweeps them.
const staleRuns = 24 * time.Hour

// NewRunID returns a unique id for a run: base36 seconds since 1970, then random hex. It sorts
// by time, says when it was made, and is a valid part of a DNS label.
func NewRunID(now time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b) // crypto/rand never fails
	return strconv.FormatInt(now.Unix(), 36) + "-" + hex.EncodeToString(b)
}

// runTime returns when a run id was made.
func runTime(id string) (time.Time, bool) {
	secs, _, ok := strings.Cut(id, "-")
	n, err := strconv.ParseInt(secs, 36, 64)
	if !ok || err != nil {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

// RunOptions configure Run.
type RunOptions struct {
	Selection
	Build    bool
	Services []string // start only these and what they depend on; empty: all
	Service  string   // run Command in a one-off container of this service; empty: on the host
	User     string   // with Service: like ExecOptions.User
	Command  []string // what to run
	LogsDir  string   // where the services' logs go; empty: <worktree>/.mutti/local/runs/<run id>
}

// RunResult describes a finished run.
type RunResult struct {
	RunID    string `json:"runId"`
	Instance string `json:"instance"`
	URL      string `json:"url,omitempty"`
	ExitCode int    `json:"exitCode"`
	Logs     string `json:"logs"` // file with the logs of all services
}

// Run starts a fresh, throwaway instance of the worktree's project, runs a command against it
// and removes the instance with all its data, whatever happens: a reaper container removes it
// even if mutti is killed. The result carries the command's exit code; a teardown that fails
// after a successful command is an error.
func (a *App) Run(ctx context.Context, dir string, opts RunOptions) (res *RunResult, err error) {
	if err := a.checkCompose(ctx); err != nil {
		return nil, err
	}
	base, err := a.resolveWorktree(ctx, dir)
	if err != nil {
		return nil, err
	}
	if err := a.Sweep(ctx, staleRuns); err != nil {
		fmt.Fprintf(a.IO.Err, "mutti: could not sweep old runs: %v\n", err)
	}

	runID := NewRunID(a.Now())
	inst := runInstance(base, runID)
	res = &RunResult{RunID: runID, Instance: inst.Name, Logs: opts.LogsDir}
	if res.Logs == "" {
		local, err := localDir(inst)
		res.Logs = filepath.Join(local, "runs", runID)
		if err != nil {
			return res, err
		}
	}

	conn, err := a.startReaper(ctx, inst)
	if err != nil {
		return res, err
	}
	// Teardown runs even when ctx was cancelled by a signal.
	defer func() {
		if terr := a.teardown(context.WithoutCancel(ctx), inst, opts.Selection, res.Logs); terr != nil {
			err = errors.Join(err, terr)
		}
		_ = conn.Close() // the reaper finds nothing left; it removes itself
	}()

	code, err := a.runIn(ctx, inst, opts, res)
	res.ExitCode = code
	return res, err
}

// runInstance returns the throwaway instance of run id of the worktree's instance base.
func runInstance(base *Instance, id string) *Instance {
	inst := *base
	inst.runID = id
	inst.Name = identity.Shorten(base.Name + "-run-" + id)
	inst.Hostname = "run-" + id + "." + base.Hostname
	inst.ID = ""
	return &inst
}

// CodeRunInstance means a command would change the instance a mutti run manages.
const CodeRunInstance = "run-instance"

// refuseRun fails for the instance of a run: its run starts and removes it.
func refuseRun(inst *Instance, what string) error {
	if inst.runID == "" {
		return nil
	}
	return &Error{
		Code:    CodeRunInstance,
		Message: fmt.Sprintf("cannot %s %s: mutti run %s manages it", what, inst.Name, inst.runID),
		Hint:    "to work on this worktree's own instance, unset MUTTI_RUN_ID",
	}
}

// runIn brings the run's instance up and runs the command.
func (a *App) runIn(ctx context.Context, inst *Instance, opts RunOptions, res *RunResult) (int, error) {
	if err := a.ensureIngress(ctx, inst); err != nil {
		return 0, err
	}
	spec, err := a.prepare(ctx, inst, opts.Selection)
	if err != nil {
		return 0, err
	}
	if err := a.compose(ctx, spec, upArgs(opts.Build, opts.Services)...); err != nil {
		return 0, err
	}
	if err := a.saveSelection(inst, opts.Selection); err != nil {
		return 0, err
	}
	if _, err := a.runHooks(ctx, inst, spec, "bootstrap", inst.Config.Hooks.Bootstrap); err != nil {
		return 0, err
	}
	if _, err := a.runHooks(ctx, inst, spec, "post-up", inst.Config.Hooks.PostUp); err != nil {
		return 0, err
	}
	status, err := a.Reconcile(ctx)
	if err != nil {
		return 0, err
	}
	if err := a.checkHosts(status, inst); err != nil {
		return 0, err
	}
	if slices.Contains(status.Instances, inst.Name) {
		if err := a.waitReady(ctx, status, inst); err != nil {
			return 0, err
		}
		res.URL = hostURL(inst.Hostname, status.HTTPSPort)
	}

	if opts.Service != "" {
		user := opts.User
		if user == "" {
			user = inst.Config.Exec.User
		}
		uargs, err := a.userArgs(ctx, inst, user)
		if err != nil {
			return 0, err
		}
		args := []string{"run", "--rm", "--no-deps"}
		if !a.Host.StdinTTY {
			args = append(args, "-T")
		}
		args = append(append(append(args, uargs...), opts.Service), opts.Command...)
		return a.Compose.Run(ctx, spec, a.IO, args...)
	}
	cmd := exec.CommandContext(ctx, opts.Command[0], opts.Command[1:]...)
	cmd.Dir = inst.Root
	cmd.Env = append(os.Environ(), a.Env(inst)...)
	if res.URL != "" {
		cmd.Env = append(cmd.Env, "MUTTI_URL="+res.URL)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.IO.In, a.IO.Out, a.IO.Err
	code, err := runCommand(cmd)
	if err != nil {
		return 0, fmt.Errorf("run %s: %w", opts.Command[0], err)
	}
	return code, nil
}

// teardown saves the logs of all services, then removes the run's instance: through Compose,
// then by label for whatever Compose missed.
func (a *App) teardown(ctx context.Context, inst *Instance, sel Selection, logsDir string) error {
	spec, err := a.prepare(ctx, inst, sel)
	if err != nil {
		return err
	}
	spec = everyProfile(spec)
	var errs []error
	if err := os.MkdirAll(logsDir, 0o755); err != nil { //nolint:gosec // logs belong to the user
		errs = append(errs, err)
	} else if f, err := os.Create(filepath.Join(logsDir, "services.log")); err != nil {
		errs = append(errs, err)
	} else {
		_, _ = a.Compose.Run(ctx, spec, compose.IO{Out: f, Err: f}, "logs", "--no-color", "--timestamps")
		errs = append(errs, f.Close())
	}
	if code, err := a.Compose.Run(ctx, spec, compose.IO{Out: a.IO.Err, Err: a.IO.Err}, "down", "--volumes", "--remove-orphans"); err != nil {
		errs = append(errs, err)
	} else if code != 0 {
		errs = append(errs, fmt.Errorf("docker compose down failed with exit code %d", code))
	}
	errs = append(errs, a.purge(ctx, inst.Name))
	a.refreshRouter(ctx)
	if err := errors.Join(errs...); err != nil {
		return &Error{Code: CodeTeardownFailed, Message: "removing run " + inst.Name + " failed", Hint: "mutti sweep removes leftovers of runs", Err: err}
	}
	return nil
}

// startReaper starts the run's Ryuk and hands it the run's labels, before anything else of the
// run exists.
func (a *App) startReaper(ctx context.Context, inst *Instance) (conn io.Closer, err error) {
	if err := a.ensureImage(ctx, reaper.Image); err != nil {
		return nil, err
	}
	spec := reaper.Spec("mutti-reaper-"+inst.runID, a.Host.DockerSocket, map[string]string{
		engine.LabelSchema: engine.SchemaVersion, engine.LabelRole: RoleReaper, LabelReaper: inst.runID,
	})
	// Inside a container 127.0.0.1 is not the host's: reach Ryuk on a network both share.
	var network string
	if a.Host.ContainerID != "" {
		self, _, err := a.Engine.InspectContainer(ctx, a.Host.ContainerID)
		if err != nil {
			return nil, engineErr(err)
		}
		for _, n := range slices.Sorted(maps.Keys(self.IPs)) {
			network = n
			break
		}
		spec.Networks, spec.Ports = []string{network}, nil
	}
	id, err := a.Engine.CreateContainer(ctx, spec)
	if err != nil {
		return nil, engineErr(err)
	}
	// A reaper nobody connected to guards nothing; never started, it would not even remove
	// itself, and sweep would take its run as alive.
	defer func() {
		if err != nil {
			_ = a.Engine.RemoveContainer(context.WithoutCancel(ctx), id)
		}
	}()
	if err := a.Engine.StartContainer(ctx, id); err != nil {
		return nil, engineErr(err)
	}
	var addr string
	if network != "" {
		d, _, err := a.Engine.InspectContainer(ctx, id)
		if err != nil {
			return nil, engineErr(err)
		}
		addr = fmt.Sprintf("%s:%d", d.IPs[network], reaper.Port)
	} else {
		cs, err := a.Engine.ListContainers(ctx, LabelReaper+"="+inst.runID)
		if err != nil {
			return nil, engineErr(err)
		}
		if len(cs) != 1 || len(cs[0].Ports) == 0 {
			return nil, &Error{Code: CodeReaperFailed, Message: "the reaper container did not start"}
		}
		addr = fmt.Sprintf("127.0.0.1:%d", cs[0].Ports[0].HostPort)
	}
	byProject, _ := reaper.FilterLine(map[string]string{engine.LabelComposeProject: inst.Name}) // fixed, valid keys
	byRun, _ := reaper.FilterLine(map[string]string{LabelRun: inst.runID})
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dial := a.DialReaper
	if dial == nil {
		dial = func(ctx context.Context, addr string, filters ...string) (io.Closer, error) {
			return reaper.Dial(ctx, addr, filters...)
		}
	}
	conn, err = dial(dialCtx, addr, byProject, byRun)
	if err != nil {
		return nil, &Error{Code: CodeReaperFailed, Message: "cannot reach the reaper", Err: err}
	}
	return conn, nil
}

// Sweep removes the leftovers of finished runs: of every run whose reaper is gone (it cannot
// remove a network the router still uses), and of every run older than olderThan, whatever
// happened to it.
//
// Runs are found by their ingress networks only. mutti creates those itself, before any container
// of the run, and removes them after the containers. Container labels are no evidence: an image
// can carry any label, and a container of another project with mutti.run must not get that
// project purged.
func (a *App) Sweep(ctx context.Context, olderThan time.Duration) error {
	runs := map[string]string{} // instance -> run id
	networks, err := a.Engine.ListNetworks(ctx, LabelRun, engine.LabelRole+"="+RoleIngress)
	if err != nil {
		return engineErr(err)
	}
	for _, n := range networks {
		instance, id := n.Labels[engine.LabelInstance], n.Labels[LabelRun]
		if _, ok := runTime(id); ok && identity.Valid(instance) && n.Name == ingressNetwork(instance) {
			runs[instance] = id
		}
	}
	reapers, err := a.Engine.ListContainers(ctx, LabelReaper)
	if err != nil {
		return engineErr(err)
	}
	alive := map[string]bool{}
	for _, r := range reapers {
		alive[r.Labels[LabelReaper]] = true
	}
	for _, instance := range slices.Sorted(maps.Keys(runs)) {
		id := runs[instance]
		made, _ := runTime(id)
		if alive[id] && a.Now().Sub(made) < olderThan {
			continue
		}
		if err := a.purge(ctx, instance); err != nil {
			return err
		}
	}
	return nil
}

// SweepHooks runs x-mutti.hooks.sweep in the running instance of dir, with
// MUTTI_SWEEP_OLDER_THAN (seconds) set: the project removes per-run resources it made itself,
// such as test databases named after run ids. It reports whether hooks ran.
func (a *App) SweepHooks(ctx context.Context, dir string, olderThan time.Duration) (bool, error) {
	inst, spec, err := a.resolvePrepared(ctx, dir, Selection{})
	if err != nil {
		return false, err
	}
	hooks := inst.Config.Hooks.Sweep
	if len(hooks) == 0 {
		return false, nil
	}
	inst.extraEnv = []string{"MUTTI_SWEEP_OLDER_THAN=" + strconv.Itoa(int(olderThan.Seconds()))}
	_, err = a.runHooks(ctx, inst, spec, "sweep", hooks)
	return true, err
}
