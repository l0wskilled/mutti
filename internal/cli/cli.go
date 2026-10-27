// Package cli maps mutti's commands and flags onto the use cases in app and renders their
// results.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/compose"
	"github.com/l0wskilled/mutti/internal/extend"
	"github.com/l0wskilled/mutti/internal/mcpserver"
	"github.com/l0wskilled/mutti/internal/project"
	"github.com/l0wskilled/mutti/internal/server"
	"github.com/l0wskilled/mutti/internal/tui"
)

// Deps are what the commands run against.
type Deps struct {
	App     *app.App
	Dir     string // working directory
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	Version string
	OutTTY  bool // Out is a terminal
}

// exitError carries an exit code chosen by a command (a child's code, or a failed check).
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

// Run executes mutti with args and returns the process exit code.
func Run(ctx context.Context, d Deps, args []string) int {
	p := &printer{out: d.Out, err: d.Err}
	defer func(io compose.IO) { d.App.IO = io }(d.App.IO) // --json redirects it for one command
	d.Dir = dirFromArgs(d.Dir, args)
	root := newRoot(d, p)
	root.SetArgs(args)
	root.SetIn(d.In)
	root.SetOut(d.Out)
	root.SetErr(d.Err)
	err := root.ExecuteContext(ctx)
	var exit exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.code
	default:
		return p.failure(err)
	}
}

// Commands returns mutti's built-in command tree without any project, for generated docs.
func Commands() *cobra.Command {
	d := Deps{App: &app.App{}, Out: io.Discard, Err: io.Discard}
	return newRoot(d, &printer{out: io.Discard, err: io.Discard})
}

// dirFromArgs applies a -C/--dir flag early: custom commands are found relative to it before
// cobra parses any flag.
func dirFromArgs(cwd string, args []string) string {
	for i, a := range args {
		v, ok := "", false
		switch {
		case (a == "-C" || a == "--dir") && i+1 < len(args):
			v, ok = args[i+1], true
		case strings.HasPrefix(a, "--dir="), strings.HasPrefix(a, "-C="):
			_, v, _ = strings.Cut(a, "=")
			ok = true
		case a == "--":
			return cwd
		}
		if ok {
			if !filepath.IsAbs(v) {
				v = filepath.Join(cwd, v)
			}
			return filepath.Clean(v)
		}
	}
	return cwd
}

// rawOutputAnnotation marks commands whose output is that of the command they run.
const rawOutputAnnotation = "mutti/raw-output"

var rawOutput = map[string]string{rawOutputAnnotation: "true"}

func newRoot(d Deps, p *printer) *cobra.Command {
	root := &cobra.Command{
		Use:           "mutti",
		Short:         "Parallel, isolated Docker Compose environments per worktree",
		Long:          "mutti runs one isolated copy of your Compose project per git worktree and cleans up after them.",
		Version:       d.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVar(&p.json, "json", false, "print machine-readable JSON")
	// Applied by dirFromArgs before parsing; declared here for --help and validation.
	root.PersistentFlags().StringP("dir", "C", "", "run as if mutti was started in this directory")
	// With --json, stdout carries the JSON result alone: what hooks and commands print goes to
	// stderr. Passthrough commands have no JSON result; their output is the result.
	root.PersistentPreRun = func(c *cobra.Command, _ []string) {
		if p.json && c.Annotations[rawOutputAnnotation] == "" {
			d.App.IO.Out = d.App.IO.Err
		}
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &app.Error{Code: CodeUsage, Message: err.Error(), Hint: "see mutti --help"}
	})

	builtins := []*cobra.Command{
		initCmd(&d, p), upCmd(&d, p), stopCmd(&d, p), restartCmd(&d, p), downCmd(&d, p), destroyCmd(&d, p),
		listCmd(&d, p), eventsCmd(&d, p), logsCmd(&d), execCmd(&d), composeCmd(&d),
		tidyCmd(&d, p), renameCmd(&d, p), doctorCmd(&d, p), routerCmd(&d, p), trustCmd(&d, p), worktreeCmd(&d, p),
		resetCmd(&d, p), urlCmd(&d, p), openCmd(&d, p), shellCmd(&d), statusCmd(&d, p), diagnoseCmd(&d, p), envCmd(&d, p), promptCmd(&d), attachCmd(&d, p), runCmd(&d, p), runIDCmd(&d), sweepCmd(&d, p), mcpCmd(&d), serveCmd(&d),
		tuiCmd(&d, func() []*cobra.Command { return root.Commands() }),
	}
	root.AddGroup(&cobra.Group{ID: "builtin", Title: "Commands:"}, &cobra.Group{ID: "custom", Title: "Project commands (.mutti/commands):"})
	reserved := []string{"help", "completion"}
	for _, c := range builtins {
		c.GroupID = "builtin"
		root.AddCommand(c)
		reserved = append(reserved, c.Name())
	}
	root.SetHelpCommandGroupID("builtin")
	root.SetCompletionCommandGroupID("builtin")
	addCustomCommands(root, &d, reserved)
	return root
}

// addCustomCommands registers the commands of the project's .mutti dir and the user's ~/.mutti
// dir. A broken commands dir only warns, so mutti itself keeps working.
func addCustomCommands(root *cobra.Command, d *Deps, reserved []string) {
	dirs := []string{d.App.Dirs.Config}
	if project, ok := extend.FindProjectDir(d.Dir); ok {
		dirs = append(dirs, project)
	}
	cmds, shadowed, err := extend.Commands(reserved, dirs...)
	if err != nil {
		fmt.Fprintf(d.Err, "mutti: ignoring custom commands: %v\n", err)
		return
	}
	for _, name := range shadowed {
		fmt.Fprintf(d.Err, "mutti: custom command %q is ignored, a built-in command has that name\n", name)
	}
	for _, cmd := range cmds {
		where := "on the host"
		if cmd.Service != "" {
			where = "in " + cmd.Service
		}
		root.AddCommand(&cobra.Command{
			Annotations:        rawOutput,
			Use:                cmd.Name + " [args...]",
			Short:              cmd.Short + " (" + where + ")",
			GroupID:            "custom",
			DisableFlagParsing: true,
			RunE: func(c *cobra.Command, args []string) error {
				return passthrough(d.App.RunCommand(c.Context(), d.Dir, cmd, args))
			},
		})
	}
}

func selectionFlags(c *cobra.Command, sel *app.Selection) {
	c.Flags().StringSliceVar(&sel.Overlays, "overlay", nil, "enable an x-mutti overlay (repeatable)")
	c.Flags().StringSliceVar(&sel.Profiles, "profile", nil, "enable a Compose profile (repeatable)")
}

type instanceJSON struct {
	Name     string `json:"name"`
	Project  string `json:"project"`
	Worktree string `json:"worktree"`
	Hostname string `json:"hostname"`
	Path     string `json:"path"`
}

func toJSON(i *app.Instance) instanceJSON {
	return instanceJSON{Name: i.Name, Project: i.Project, Worktree: i.Worktree, Hostname: i.Hostname, Path: i.Root}
}

func initCmd(d *Deps, p *printer) *cobra.Command {
	var opts app.InitOptions
	var dryRun, yes bool
	c := &cobra.Command{
		Use:   "init",
		Short: "Write mutti's settings into the Compose file and a section for AI agents into AGENTS.md",
		Long: `Write mutti's settings into the Compose file and a section for AI agents into AGENTS.md.

mutti needs no configuration: init writes down what it would do anyway, as an x-mutti block to
edit from (main service, overlays, default routes and fixed-port suggestions as comments). It
leaves a Compose file that has x-mutti alone and never changes anything else in it. AGENTS.md
gets a section on how to use the environment, which init replaces on every run.

Shows the changes and asks first; --yes skips the question, --dry-run only shows them.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			plan, err := d.App.Init(c.Context(), d.Dir, opts)
			if err != nil {
				return err
			}
			apply := !dryRun && !plan.Empty()
			if apply && !yes {
				if p.json || !d.App.Host.StdinTTY {
					return &app.Error{Code: CodeConfirmationRequired, Message: "init changes files", Hint: "pass --yes, or --dry-run to see the changes"}
				}
				showInit(d.Err, plan)
				if !confirm(d, "Write these changes?") {
					return exitError{code: 1}
				}
			}
			if apply {
				if err := d.App.InitApply(plan); err != nil {
					return err
				}
			}
			type initJSON struct {
				*app.InitPlan
				Written bool `json:"written"`
			}
			p.result(initJSON{plan, apply}, func(w io.Writer) {
				switch {
				case plan.Empty():
					fmt.Fprintln(w, "Nothing to change: the Compose file has x-mutti and AGENTS.md is current.")
				case !apply:
					showInit(w, plan)
				default:
					if plan.ComposeFile != "" {
						fmt.Fprintf(w, "Added x-mutti to %s.\n", plan.ComposeFile)
					}
					if plan.AgentsFile != "" {
						fmt.Fprintf(w, "Wrote the mutti section of %s.\n", plan.AgentsFile)
					}
					fmt.Fprintln(w, "Next: mutti up")
				}
			})
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "only show the changes")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	c.Flags().BoolVar(&opts.NoAgents, "no-agents", false, "leave AGENTS.md alone")
	return c
}

// showInit prints the planned changes as added lines.
func showInit(w io.Writer, plan *app.InitPlan) {
	for _, change := range []struct{ file, text string }{{plan.ComposeFile, plan.XMutti}, {plan.AgentsFile, plan.AgentsSection}} {
		if change.file == "" {
			continue
		}
		fmt.Fprintf(w, "%s:\n", change.file)
		for line := range strings.Lines(change.text) {
			fmt.Fprint(w, "+ "+line)
		}
		fmt.Fprintln(w)
	}
}

func upCmd(d *Deps, p *printer) *cobra.Command {
	var opts app.UpOptions
	c := &cobra.Command{
		Use:   "up [service...]",
		Short: "Start this worktree's instance and wait until it is healthy",
		Long: `Start this worktree's instance and wait until it is healthy. Given services, start only
those and what they depend on; hooks of services that do not run are skipped.`,
		ValidArgsFunction: completeServices(d),
		RunE: func(c *cobra.Command, args []string) error {
			opts.Services = args
			res, err := d.App.Up(c.Context(), d.Dir, opts)
			if err != nil {
				explain(c.Context(), d)
				return err
			}
			type upJSON struct {
				Instance     instanceJSON `json:"instance"`
				Bootstrapped bool         `json:"bootstrapped"`
				URL          string       `json:"url,omitempty"`
			}
			p.result(upJSON{toJSON(res.Instance), res.Bootstrapped, res.URL}, func(w io.Writer) {
				if res.URL == "" {
					fmt.Fprintf(w, "%s is up. See its ports with: mutti list\n", res.Instance.Name)
					return
				}
				fmt.Fprintf(w, "%s is up: %s\n", res.Instance.Name, res.URL)
			})
			return nil
		},
	}
	selectionFlags(c, &opts.Selection)
	c.Flags().BoolVar(&opts.Build, "build", false, "rebuild images before starting")
	return c
}

func simpleCmd(d *Deps, p *printer, use, short, done string, run func(context.Context, string, app.Selection) (*app.Instance, error)) *cobra.Command {
	var sel app.Selection
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			inst, err := run(c.Context(), d.Dir, sel)
			if err != nil {
				return err
			}
			p.result(map[string]any{"instance": toJSON(inst)}, func(w io.Writer) {
				fmt.Fprintf(w, "%s %s.\n", inst.Name, done)
			})
			return nil
		},
	}
	selectionFlags(c, &sel)
	return c
}

// servicesCmd builds a command that acts on the instance or on some of its services.
func servicesCmd(d *Deps, p *printer, use, short, done string, run func(context.Context, string, app.Selection, ...string) (*app.Instance, error)) *cobra.Command {
	var sel app.Selection
	c := &cobra.Command{
		Use:               use + " [service...]",
		Short:             short,
		ValidArgsFunction: completeServices(d),
		RunE: func(c *cobra.Command, args []string) error {
			inst, err := run(c.Context(), d.Dir, sel, args...)
			if err != nil {
				explain(c.Context(), d)
				return err
			}
			p.result(map[string]any{"instance": toJSON(inst), "services": args}, func(w io.Writer) {
				if len(args) > 0 {
					fmt.Fprintf(w, "%s: %s %s.\n", inst.Name, strings.Join(args, ", "), done)
					return
				}
				fmt.Fprintf(w, "%s is %s.\n", inst.Name, done)
			})
			return nil
		},
	}
	selectionFlags(c, &sel)
	return c
}

func stopCmd(d *Deps, p *printer) *cobra.Command {
	return servicesCmd(d, p, "stop", "Stop the instance's containers, or some services', and keep everything", "stopped", d.App.Stop)
}

func restartCmd(d *Deps, p *printer) *cobra.Command {
	return servicesCmd(d, p, "restart", "Restart the instance's containers, or some services'", "restarted", d.App.Restart)
}

func downCmd(d *Deps, p *printer) *cobra.Command {
	return simpleCmd(d, p, "down", "Remove the instance's containers and networks, keep its data", "is down", d.App.Down)
}

// confirmDestruction asks before data is deleted; without a terminal it insists on --yes.
func confirmDestruction(d *Deps, yes bool, what, question string) error {
	if yes {
		return nil
	}
	if !d.App.Host.StdinTTY {
		return &app.Error{Code: CodeConfirmationRequired, Message: what + " deletes all data of the instance", Hint: "pass --yes"}
	}
	if !confirm(d, question) {
		return exitError{code: 1}
	}
	return nil
}

func resetCmd(d *Deps, p *printer) *cobra.Command {
	var opts app.UpOptions
	var yes bool
	c := &cobra.Command{
		Use:   "reset",
		Short: "Delete the instance's data and start it fresh (bootstrap runs again)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := confirmDestruction(d, yes, "reset", "Delete all data of this instance and start it fresh?"); err != nil {
				return err
			}
			res, err := d.App.Reset(c.Context(), d.Dir, opts)
			if err != nil {
				return err
			}
			p.result(map[string]any{"instance": toJSON(res.Instance), "url": res.URL}, func(w io.Writer) {
				fmt.Fprintf(w, "%s is fresh and up.\n", res.Instance.Name)
			})
			return nil
		},
	}
	selectionFlags(c, &opts.Selection)
	c.Flags().BoolVar(&opts.Build, "build", false, "rebuild images before starting")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return c
}

func destroyCmd(d *Deps, p *printer) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "destroy",
		Short: "Remove the instance with all its data",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := confirmDestruction(d, yes, "destroy", "Delete all containers, volumes and data of this instance?"); err != nil {
				return err
			}
			inst, err := d.App.Destroy(c.Context(), d.Dir)
			if err != nil {
				return err
			}
			p.result(map[string]any{"instance": toJSON(inst)}, func(w io.Writer) {
				fmt.Fprintf(w, "%s is gone.\n", inst.Name)
			})
			return nil
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return c
}

func confirm(d *Deps, question string) bool {
	fmt.Fprintf(d.Err, "%s [y/N] ", question)
	line, _ := bufio.NewReader(d.In).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func listCmd(d *Deps, p *printer) *cobra.Command {
	var watch bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List all instances on this machine",
		Long: `List all instances on this machine.

With --watch, the list is shown again whenever a container of an instance changes, until
interrupted; with --json too, every list is one JSON object on a line of its own.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if watch {
				return watchList(c.Context(), d, p)
			}
			list, err := d.App.List(c.Context())
			if err != nil {
				return err
			}
			p.result(list, func(w io.Writer) { renderList(w, list) })
			return nil
		},
	}
	c.Flags().BoolVarP(&watch, "watch", "w", false, "show the list again whenever an instance changes")
	return c
}

// watchSettle is how long watch waits for more events before it shows the list again: an up
// sends a burst of them.
var watchSettle = 250 * time.Millisecond

func watchList(ctx context.Context, d *Deps, p *printer) error {
	events, errs := d.App.Events(ctx) // before the first list, so no change goes unseen
	show := func() error {
		list, err := d.App.List(ctx)
		if err != nil {
			return err
		}
		if d.OutTTY && !p.json {
			fmt.Fprint(d.Out, "\x1b[H\x1b[2J") // clear the screen
		}
		p.stream(list, func(w io.Writer) { renderList(w, list) })
		return nil
	}
	if err := show(); err != nil {
		return err
	}
	var settled <-chan time.Time
	for {
		select {
		case _, open := <-events:
			if !open {
				return streamEnd(errs)
			}
			if settled == nil {
				settled = time.After(watchSettle)
			}
		case <-settled:
			settled = nil
			if err := show(); err != nil {
				return err
			}
		}
	}
}

// streamEnd is the error that ended an event stream, or nil when it ended with its context.
func streamEnd(errs <-chan error) error {
	select {
	case err := <-errs:
		return err
	default:
		return nil
	}
}

func eventsCmd(d *Deps, p *printer) *cobra.Command {
	return &cobra.Command{
		Use:   "events",
		Short: "Print what happens to the containers of all instances, as it happens",
		Long: `Print what happens to the containers of all instances, as it happens, until interrupted:
created, started, stopped, removed, paused, unpaused, healthy, unhealthy.

With --json, every event is one JSON object on a line of its own, for tools that follow along.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			events, errs := d.App.Events(c.Context())
			for ev := range events {
				p.stream(ev, func(w io.Writer) {
					what := ev.Event
					if ev.ExitCode != nil {
						what += fmt.Sprintf(" (exit %d)", *ev.ExitCode)
					}
					fmt.Fprintf(w, "%s  %s  %s  %s\n", ev.Time.Local().Format("15:04:05"), ev.Instance, ev.Service, what)
				})
			}
			return streamEnd(errs)
		},
	}
}

func renderList(w io.Writer, list []app.Summary) {
	if len(list) == 0 {
		fmt.Fprintln(w, "No instances. Start one with: mutti up")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tBRANCH\tURL\tPATH\tPORTS")
	for _, s := range list {
		status := fmt.Sprintf("%s %d/%d", s.Status, s.Running, s.Total)
		if s.Orphaned {
			status = "orphaned"
		}
		url := ""
		if len(s.URLs) > 0 {
			url = s.URLs[0]
		}
		var ports []string
		for _, port := range s.Ports {
			if !port.Routed {
				ports = append(ports, fmt.Sprintf("%s:%d=%s", port.Service, port.ContainerPort, port.Address))
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, status, s.Branch, url, s.Path, strings.Join(ports, " "))
	}
	_ = tw.Flush()
}

func passthrough(code int, err error) error {
	if err != nil {
		return err
	}
	if code != 0 {
		return exitError{code: code}
	}
	return nil
}

// splitOverlays takes leading --overlay flags off args, which otherwise go to Compose untouched.
func splitOverlays(args []string) (sel app.Selection, rest []string) {
	for len(args) > 0 {
		if v, ok := strings.CutPrefix(args[0], "--overlay="); ok {
			sel.Overlays = append(sel.Overlays, v)
			args = args[1:]
			continue
		}
		if args[0] == "--overlay" && len(args) > 1 {
			sel.Overlays = append(sel.Overlays, args[1])
			args = args[2:]
			continue
		}
		break
	}
	return sel, args
}

func logsCmd(d *Deps) *cobra.Command {
	return &cobra.Command{
		Annotations:        rawOutput,
		Use:                "logs [--overlay <name>] [docker compose logs flags] [service...]",
		Short:              "Show the instance's logs (flags as for docker compose logs)",
		DisableFlagParsing: true,
		RunE: func(c *cobra.Command, args []string) error {
			sel, rest := splitOverlays(args)
			return passthrough(d.App.ComposePassthrough(c.Context(), d.Dir, sel, append([]string{"logs"}, rest...)))
		},
	}
}

func composeCmd(d *Deps) *cobra.Command {
	return &cobra.Command{
		Annotations:        rawOutput,
		Use:                "compose [--overlay <name>] [docker compose args]",
		Short:              "Run any docker compose command against this instance",
		DisableFlagParsing: true,
		RunE: func(c *cobra.Command, args []string) error {
			sel, rest := splitOverlays(args)
			return passthrough(d.App.ComposePassthrough(c.Context(), d.Dir, sel, rest))
		},
	}
}

func execCmd(d *Deps) *cobra.Command {
	var opts app.ExecOptions
	c := &cobra.Command{
		Annotations: rawOutput,
		Use:         "exec <service> -- <command> [args...]",
		Short:       "Run a command in a service of this instance, as your host user",
		Args:        cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			if c.ArgsLenAtDash() != 1 {
				return &app.Error{Code: CodeUsage, Message: "separate the command with --", Hint: "mutti exec api -- php -v"}
			}
			return passthrough(d.App.Exec(c.Context(), d.Dir, args[0], args[1:], opts))
		},
	}
	c.Flags().StringVarP(&opts.User, "user", "u", "", `run as "host", "service" or user[:group] (default from x-mutti exec.user)`)
	c.Flags().StringVarP(&opts.Workdir, "workdir", "w", "", "working directory inside the container")
	c.Flags().StringArrayVarP(&opts.Env, "env", "e", nil, "set an environment variable, KEY=VALUE (repeatable)")
	c.ValidArgsFunction = completeServices(d)
	return c
}

func tidyCmd(d *Deps, p *printer) *cobra.Command {
	var opts app.TidyOptions
	c := &cobra.Command{
		Use:   "tidy",
		Short: "Remove instances whose worktree is gone, stop idle ones (shows them only, unless --yes)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			res, err := d.App.Tidy(c.Context(), opts)
			if err != nil {
				return err
			}
			p.result(res, func(w io.Writer) {
				if len(res.Removed)+len(res.Stopped) == 0 {
					fmt.Fprintln(w, "Nothing to tidy.")
					return
				}
				remove, stop := "would remove", "would stop"
				if opts.Apply {
					remove, stop = "removed", "stopped"
				}
				for _, s := range res.Removed {
					fmt.Fprintf(w, "%s %s (%s)\n", remove, s.Name, s.Path)
				}
				for _, s := range res.Stopped {
					fmt.Fprintf(w, "%s %s (idle, %s)\n", stop, s.Name, s.Path)
				}
				if !opts.Apply {
					fmt.Fprintln(w, "Run it with --yes to do it.")
				}
			})
			return nil
		},
	}
	c.Flags().BoolVarP(&opts.Apply, "yes", "y", false, "do it")
	c.Flags().StringVar(&opts.Project, "project", "", "only this project's instances")
	c.Flags().DurationVar(&opts.Idle, "idle", 0, "also stop running instances unused for this long, e.g. 24h")
	return c
}

func renameCmd(d *Deps, p *printer) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <name>",
		Short: "Give this worktree's instance a new name",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			inst, err := d.App.Rename(c.Context(), d.Dir, args[0])
			if err != nil {
				return err
			}
			p.result(map[string]any{"instance": toJSON(inst)}, func(w io.Writer) {
				fmt.Fprintf(w, "This worktree is now %s.\n", inst.Name)
			})
			return nil
		},
	}
}

func doctorCmd(d *Deps, p *printer) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check Docker, Compose and this project",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			r := d.App.Doctor(c.Context(), d.Dir)
			p.result(r, func(w io.Writer) {
				for _, check := range r.Checks {
					fmt.Fprintf(w, "%-4s  %-12s %s\n", check.Status, check.Name, check.Detail)
					if check.Hint != "" {
						fmt.Fprintf(w, "      %-12s hint: %s\n", "", check.Hint)
					}
				}
				if len(r.ComposeFeatures) > 0 {
					fmt.Fprintln(w, "\nCompose features:")
					for _, f := range r.ComposeFeatures {
						mark := "yes"
						if !f.Available {
							mark = "no "
						}
						fmt.Fprintf(w, "  %s  %-20s since %-8s %s\n", mark, f.Name, f.Since, f.Use)
					}
				}
			})
			if r.Failed() {
				return exitError{code: ExitCheck}
			}
			return nil
		},
	}
}

func routerCmd(d *Deps, p *printer) *cobra.Command {
	c := &cobra.Command{Use: "router", Short: "Manage the machine-wide router"}
	show := func(status *app.RouterStatus) {
		p.result(status, func(w io.Writer) {
			fmt.Fprintf(w, "%s listens on 127.0.0.1:%d (http) and :%d (https), routing %d instance(s).\n",
				status.Name, status.HTTPPort, status.HTTPSPort, len(status.Instances))
			for _, name := range status.Instances {
				fmt.Fprintf(w, "  %s\n", name)
			}
		})
	}
	c.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Start the router if needed, sync its routes and show them",
			Args:  cobra.NoArgs,
			RunE: func(c *cobra.Command, _ []string) error {
				status, err := d.App.Reconcile(c.Context())
				if err != nil {
					return err
				}
				show(status)
				return nil
			},
		},
		&cobra.Command{
			Use:   "restart",
			Short: "Recreate the router container",
			Args:  cobra.NoArgs,
			RunE: func(c *cobra.Command, _ []string) error {
				status, err := d.App.RouterRestart(c.Context())
				if err != nil {
					return err
				}
				show(status)
				return nil
			},
		},
	)
	return c
}

func trustCmd(d *Deps, p *printer) *cobra.Command {
	var uninstall bool
	c := &cobra.Command{
		Use:   "trust",
		Short: "Make the system and browsers trust mutti's local CA (asks for sudo)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			res, err := d.App.Trust(c.Context(), uninstall)
			if err != nil {
				return err
			}
			p.result(res, func(w io.Writer) {
				if !res.Installed {
					fmt.Fprintf(w, "mutti's local CA (%s) is no longer trusted.\n", res.CertFile)
					return
				}
				fmt.Fprintf(w, "mutti's local CA (%s) is trusted now. Restart your browser to pick it up.\n", res.CertFile)
				if !res.Browsers {
					fmt.Fprintln(w, "Firefox and Chromium keep their own list and were not updated: install certutil (nss tools) and run mutti trust again.")
				}
			})
			return nil
		},
	}
	c.Flags().BoolVar(&uninstall, "uninstall", false, "stop trusting the CA")
	return c
}

func worktreeCmd(d *Deps, p *printer) *cobra.Command {
	c := &cobra.Command{Use: "worktree", Short: "Create and remove git worktrees together with their instances"}

	var add app.WorktreeAddOptions
	addCmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a worktree next to the main one (<repo>-<name>, new branch <name>) and start it",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			add.Name = args[0]
			res, err := d.App.WorktreeAdd(c.Context(), d.Dir, add)
			if res == nil {
				return err
			}
			type addJSON struct {
				Path     string        `json:"path"`
				Branch   string        `json:"branch"`
				Copied   []string      `json:"copied,omitempty"`
				Cloned   []string      `json:"cloned,omitempty"`
				Instance *instanceJSON `json:"instance,omitempty"`
				URL      string        `json:"url,omitempty"`
			}
			out := addJSON{Path: res.Path, Branch: res.Branch}
			if res.Up != nil && err == nil {
				i := toJSON(res.Up.Instance)
				out.Instance, out.URL = &i, res.Up.URL
			}
			out.Copied, out.Cloned = res.Copied, res.Cloned
			p.result(out, func(w io.Writer) {
				fmt.Fprintf(w, "Created %s (branch %s).\n", res.Path, res.Branch)
				if len(res.Copied) > 0 {
					fmt.Fprintf(w, "Copied from the main worktree: %s\n", strings.Join(res.Copied, ", "))
				}
				if len(res.Cloned) > 0 {
					fmt.Fprintf(w, "Data copied from %s: %s\n", add.DataFrom, strings.Join(res.Cloned, ", "))
				}
				if out.Instance != nil {
					if out.URL != "" {
						fmt.Fprintf(w, "%s is up: %s\n", out.Instance.Name, out.URL)
					} else {
						fmt.Fprintf(w, "%s is up.\n", out.Instance.Name)
					}
				}
			})
			return err
		},
	}
	addCmd.Flags().StringVar(&add.Branch, "branch", "", "check out this existing branch (local or remote) instead of creating one")
	addCmd.Flags().StringVar(&add.From, "from", "", "base of the new branch (default: current HEAD)")
	addCmd.Flags().StringVar(&add.Path, "path", "", "where to create the worktree")
	addCmd.Flags().StringVar(&add.DataFrom, "data-from", "", `copy the data of another worktree's instance ("main", a worktree or instance name, or a path) instead of bootstrapping`)
	addCmd.Flags().BoolVar(&add.NoUp, "no-up", false, "only create the worktree")
	selectionFlags(addCmd, &add.Up.Selection)
	addCmd.Flags().BoolVar(&add.Up.Build, "build", false, "rebuild images before starting")

	var rm app.WorktreeRemoveOptions
	var yes bool
	removeCmd := &cobra.Command{
		Use:   "remove <name|path>",
		Short: "Destroy a worktree's instance with its data and remove the worktree (the branch stays)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			rm.Target = args[0]
			if err := confirmDestruction(d, yes, "worktree remove", "Delete the worktree and all data of its instance?"); err != nil {
				return err
			}
			path, err := d.App.WorktreeRemove(c.Context(), d.Dir, rm)
			if err != nil {
				return err
			}
			p.result(map[string]string{"path": path}, func(w io.Writer) {
				fmt.Fprintf(w, "Removed %s.\n", path)
			})
			return nil
		},
	}
	removeCmd.ValidArgsFunction = func(c *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return d.App.WorktreeNames(c.Context(), d.Dir), cobra.ShellCompDirectiveNoFileComp
	}
	removeCmd.Flags().BoolVar(&rm.Force, "force", false, "also drop uncommitted changes")
	removeCmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")

	c.AddCommand(addCmd, removeCmd)
	return c
}

// completeServices completes the first argument with the project's service names.
func completeServices(d *Deps) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(c *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveDefault
		}
		return d.App.Services(c.Context(), d.Dir), cobra.ShellCompDirectiveNoFileComp
	}
}

func optionalService(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func urlCmd(d *Deps, p *printer) *cobra.Command {
	return &cobra.Command{
		Use:               "url [service]",
		Short:             "Print the URL of the instance or of a service",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServices(d),
		RunE: func(c *cobra.Command, args []string) error {
			url, err := d.App.URL(c.Context(), d.Dir, optionalService(args))
			if err != nil {
				return err
			}
			p.result(map[string]string{"url": url}, func(w io.Writer) { fmt.Fprintln(w, url) })
			return nil
		},
	}
}

func openCmd(d *Deps, p *printer) *cobra.Command {
	return &cobra.Command{
		Use:               "open [service]",
		Short:             "Open the instance or a service in the browser",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServices(d),
		RunE: func(c *cobra.Command, args []string) error {
			url, err := d.App.Open(c.Context(), d.Dir, optionalService(args))
			if err != nil {
				return err
			}
			p.result(map[string]string{"url": url}, func(w io.Writer) { fmt.Fprintf(w, "Opening %s\n", url) })
			return nil
		},
	}
}

func shellCmd(d *Deps) *cobra.Command {
	var opts app.ExecOptions
	c := &cobra.Command{
		Annotations:       rawOutput,
		Use:               "shell [service]",
		Short:             "Open a shell in a service (default: the main one), as your host user",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeServices(d),
		RunE: func(c *cobra.Command, args []string) error {
			return passthrough(d.App.Shell(c.Context(), d.Dir, optionalService(args), opts))
		},
	}
	c.Flags().StringVarP(&opts.User, "user", "u", "", `run as "host", "service" or user[:group]`)
	return c
}

func statusCmd(d *Deps, p *printer) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show this worktree's instance service by service",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := d.App.Status(c.Context(), d.Dir)
			if err != nil {
				return err
			}
			p.result(st, func(w io.Writer) {
				fmt.Fprintf(w, "%s", st.Name)
				if st.Branch != "" {
					fmt.Fprintf(w, " (%s)", st.Branch)
				}
				fmt.Fprintf(w, " in %s\n", st.Path)
				if st.URL != "" {
					fmt.Fprintf(w, "%s\n", st.URL)
				}
				fmt.Fprintln(w)
				// A profile column only for projects that have optional services.
				optional := slices.ContainsFunc(st.Services, func(s app.ServiceStatus) bool { return len(s.Profiles) > 0 })
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				if optional {
					fmt.Fprint(tw, "PROFILE\t")
				}
				fmt.Fprintln(tw, "SERVICE\tSTATE\tURL\tPORTS")
				for _, s := range st.Services {
					state := s.State
					switch {
					case s.Status != "":
						state = s.Status
					case len(s.Profiles) > 0:
						state = "off"
					}
					var ports []string
					for _, port := range s.Ports {
						ports = append(ports, fmt.Sprintf("%d=%s", port.ContainerPort, port.Address))
					}
					if optional {
						fmt.Fprintf(tw, "%s\t", strings.Join(s.Profiles, ","))
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Service, state, strings.Join(s.URLs, " "), strings.Join(ports, " "))
				}
				_ = tw.Flush()
				// What is not well comes with its diagnosis, short; mutti diagnose has it all.
				if diags, err := d.App.Diagnose(c.Context(), d.Dir); err == nil {
					for _, diag := range diags {
						fmt.Fprintln(w)
						printDiagnosis(w, diag, 8)
					}
					if len(diags) > 0 {
						fmt.Fprintln(w, "\nMore: mutti diagnose")
					}
				}
			})
			return nil
		},
	}
}

func diagnoseCmd(d *Deps, p *printer) *cobra.Command {
	return &cobra.Command{
		Use:   "diagnose [service...]",
		Short: "Explain what is wrong with the instance's services: health checks, exit codes, last logs",
		Long: `Explain what is wrong with the instance's services: why they are unhealthy, crashing or
gone, what their health checks said and what they printed last. Without services it looks at
every service that is not well; given services, it explains those whatever their state.`,
		ValidArgsFunction: completeServices(d),
		RunE: func(c *cobra.Command, args []string) error {
			diags, err := d.App.Diagnose(c.Context(), d.Dir, args...)
			if err != nil {
				return err
			}
			p.result(diags, func(w io.Writer) {
				if len(diags) == 0 {
					fmt.Fprintln(w, "All services are well.")
					return
				}
				for i, diag := range diags {
					if i > 0 {
						fmt.Fprintln(w)
					}
					printDiagnosis(w, diag, 0)
				}
			})
			return nil
		},
	}
}

// explain prints, after a failed command, the diagnosis of what is not well, to stderr.
func explain(ctx context.Context, d *Deps) {
	diags, err := d.App.Diagnose(ctx, d.Dir)
	if err != nil {
		return
	}
	for _, diag := range diags {
		fmt.Fprintln(d.Err)
		printDiagnosis(d.Err, diag, 8)
	}
}

// printDiagnosis prints a diagnosis with at most logLines lines of logs; 0 prints them all.
func printDiagnosis(w io.Writer, d app.Diagnosis, logLines int) {
	fmt.Fprintf(w, "! %s: %s\n", d.Service, d.Problem)
	if d.Hint != "" {
		fmt.Fprintf(w, "  -> %s\n", d.Hint)
	}
	if n := len(d.Health); n > 0 {
		last := d.Health[n-1]
		fmt.Fprintf(w, "  last health check (%s, exit %d):\n", last.Time.Local().Format(time.TimeOnly), last.ExitCode)
		printIndented(w, last.Output, 0)
	}
	if strings.TrimSpace(d.Logs) != "" {
		fmt.Fprintln(w, "  last logs:")
		printIndented(w, d.Logs, logLines)
	}
}

// printIndented prints the last max lines of text (all for 0), indented.
func printIndented(w io.Writer, text string, maxLines int) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	for _, l := range lines {
		fmt.Fprintf(w, "    %s\n", l)
	}
}

func envCmd(d *Deps, p *printer) *cobra.Command {
	var format string
	c := &cobra.Command{
		Use:   "env",
		Short: "Print the MUTTI_* variables, for `mutti env | source` (fish) or `eval \"$(mutti env)\"`",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			vars, err := d.App.EnvOf(c.Context(), d.Dir)
			if err != nil {
				return err
			}
			if format == "" {
				format = "sh"
				if strings.HasSuffix(d.App.Host.Getenv("SHELL"), "fish") {
					format = "fish"
				}
			}
			values := map[string]string{}
			for _, kv := range vars {
				k, v, _ := strings.Cut(kv, "=")
				values[k] = v
			}
			p.result(values, func(w io.Writer) {
				for _, kv := range vars {
					k, v, _ := strings.Cut(kv, "=")
					quoted := "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
					if format == "fish" {
						fmt.Fprintf(w, "set -gx %s %s\n", k, quoted)
					} else {
						fmt.Fprintf(w, "export %s=%s\n", k, quoted)
					}
				}
			})
			return nil
		},
	}
	c.Flags().StringVar(&format, "format", "", "sh or fish (default: from $SHELL)")
	return c
}

func promptCmd(d *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "prompt",
		Short: "Print a short state line for your shell prompt (empty outside an instance)",
		Long: `Print a short state line for your shell prompt, e.g. "shop-feature running 6/6".
It is fast (no Compose involved), prints nothing outside an instance and never fails.

Starship (~/.config/starship.toml):

  [custom.mutti]
  command = "mutti prompt"
  when = true
  format = "[$output]($style) "

fish (in fish_right_prompt):

  mutti prompt`,
		Args: cobra.NoArgs,
		Run: func(c *cobra.Command, _ []string) {
			if line := d.App.Prompt(c.Context(), d.Dir); line != "" {
				fmt.Fprintln(d.Out, line)
			}
		},
	}
}

func attachCmd(d *Deps, p *printer) *cobra.Command {
	var detach bool
	c := &cobra.Command{
		Use:   "attach",
		Short: "Connect the container mutti runs in to this instance's network (e.g. a CI step)",
		Long: `Connect the container mutti runs in to this instance's network: it then reaches the
services by name (db, api, ...) and the instance's hostnames through the router, for example
for end-to-end tests in a CI step container. Detach again before tearing the instance down,
or let mutti destroy do it.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			inst, err := d.App.Attach(c.Context(), d.Dir, detach)
			if err != nil {
				return err
			}
			p.result(map[string]any{"instance": toJSON(inst), "attached": !detach}, func(w io.Writer) {
				if detach {
					fmt.Fprintf(w, "Detached from %s.\n", inst.Name)
				} else {
					fmt.Fprintf(w, "Attached to %s.\n", inst.Name)
				}
			})
			return nil
		},
	}
	c.Flags().BoolVar(&detach, "detach", false, "disconnect again")
	return c
}

func runCmd(d *Deps, p *printer) *cobra.Command {
	var opts app.RunOptions
	c := &cobra.Command{
		Use:   "run [flags] -- <command> [args...]",
		Short: "Run a command against a fresh throwaway instance, removed afterwards whatever happens",
		Long: `Start a fresh instance of this worktree's project (own containers, volumes and URL),
wait until it is healthy, run the command, then save the services' logs and remove the
instance with all its data. A reaper container removes it even if mutti is killed.

The command runs on the host with MUTTI_RUN_ID, MUTTI_URL and the other MUTTI_* variables set,
or with --service in a one-off container of that service. mutti exits with its exit code.
mutti commands inside it, such as mutti exec or mutti compose, work on the run's instance.

  mutti run --overlay e2e -- npx playwright test
  mutti run --overlay e2e --service test-runner -- npm test`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if c.ArgsLenAtDash() != 0 {
				return &app.Error{Code: CodeUsage, Message: "separate the command with --", Hint: "mutti run -- npm test"}
			}
			opts.Command = args
			res, err := d.App.Run(c.Context(), d.Dir, opts)
			if res != nil {
				p.result(res, func(w io.Writer) {
					if err != nil {
						fmt.Fprintf(d.Err, "mutti: run %s failed, logs in %s\n", res.RunID, res.Logs)
						return
					}
					fmt.Fprintf(d.Err, "mutti: run %s exited with %d, logs in %s\n", res.RunID, res.ExitCode, res.Logs)
				})
			}
			if err != nil {
				return err
			}
			return passthrough(res.ExitCode, nil)
		},
	}
	selectionFlags(c, &opts.Selection)
	c.Flags().BoolVar(&opts.Build, "build", false, "rebuild images before starting")
	c.Flags().StringSliceVar(&opts.Services, "start", nil, "start only this service and what it depends on (repeatable)")
	c.Flags().StringVar(&opts.Service, "service", "", "run the command in a one-off container of this service")
	c.Flags().StringVarP(&opts.User, "user", "u", "", `with --service: run as "host", "service" or user[:group]`)
	c.Flags().StringVar(&opts.LogsDir, "logs", "", "where to save the services' logs (default .mutti/local/runs/<run id>)")
	return c
}

func mcpCmd(d *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve mutti to AI coding agents as a Model Context Protocol server over stdio",
		Long: `Serve mutti to AI coding agents as a Model Context Protocol (MCP) server over stdin/stdout.

The agent gets tools to start and inspect environments (list, status, url, logs, diagnose, up,
stop, restart, down), to run commands in services (exec, compose) and against throwaway copies
(run), to work in parallel in worktrees with environments of their own (worktree_add,
worktree_remove), to reset, destroy, tidy and sweep, and to run the project's own commands
(commands, command). Everything the CLI does but the terminal UI, the web UI, shell and trust,
which need a person. Register it with your agent, e.g.:

  claude mcp add mutti -- mutti mcp`,
		Annotations: rawOutput,
		Args:        cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			srv := mcpserver.New(d.App, d.Dir, d.Version)
			return srv.Run(c.Context(), &mcp.IOTransport{Reader: io.NopCloser(d.In), Writer: nopWriteCloser{d.Out}})
		},
	}
}

// CodePortInUse means mutti serve cannot listen on its port.
const CodePortInUse = "port-in-use"

// runTUI starts the terminal UI and executable finds mutti's binary; tests replace them.
var (
	runTUI     = tui.Run
	executable = os.Executable
)

// tuiHidden are the commands the terminal UI does not offer: they only make sense outside it.
var tuiHidden = []string{"tui", "mcp", "help", "completion", "prompt"}

// tuiCommands lists every command the terminal UI offers, the subcommands of a group under
// the group's path. A command with flags of its own says so in its use, before the arguments, so the UI asks
// for them.
func tuiCommands(path []string, cmds []*cobra.Command) []tui.Command {
	var out []tui.Command
	for _, cmd := range cmds {
		if cmd.Hidden || slices.Contains(tuiHidden, cmd.Name()) {
			continue
		}
		p := append(slices.Clone(path), cmd.Name())
		if cmd.Runnable() {
			use := strings.Join(p, " ")
			if args := strings.TrimPrefix(cmd.Use, cmd.Name()); !cmd.DisableFlagParsing && ownFlags(cmd) && !strings.Contains(args, "[flags]") {
				use += " [flags]"
			}
			use += strings.TrimPrefix(cmd.Use, cmd.Name())
			out = append(out, tui.Command{Path: p, Use: use, Short: cmd.Short})
		}
		out = append(out, tuiCommands(p, cmd.Commands())...)
	}
	return out
}

// ownFlags reports whether cmd has flags besides the global ones and --help.
func ownFlags(cmd *cobra.Command) bool {
	found := false
	cmd.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		found = found || (f.Name != "help" && !f.Hidden)
	})
	return found
}

func tuiCmd(d *Deps, commands func() []*cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Every instance, its services and every mutti command in a terminal UI",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if !d.OutTTY {
				return &app.Error{Code: CodeUsage, Message: "mutti tui needs a terminal", Hint: "for scripts, use mutti list --json and mutti status --json"}
			}
			self, err := executable()
			if err != nil {
				return err
			}
			opts := tui.Options{App: d.App, Dir: d.Dir, Self: self, Version: d.Version, Commands: tuiCommands(nil, commands())}
			return runTUI(c.Context(), opts, d.In, d.Out)
		},
	}
}

func serveCmd(d *Deps) *cobra.Command {
	var port int
	var open bool
	c := &cobra.Command{
		Use:   "serve",
		Short: "Serve the web UI and HTTP API on 127.0.0.1 until interrupted",
		Long: `Serve mutti's web UI, its HTTP API and its MCP server on 127.0.0.1 until interrupted.

The UI lists the instances of this machine and keeps them current, starts, stops and removes
them, and shows their logs and events. The API (/api/...) answers with the JSON of --json,
/api/events streams events, and /mcp serves the tools of mutti mcp over HTTP.

It answers only to localhost, and only pages of its own origin may change anything. It runs in
the foreground, like any other command; nothing is installed.`,
		Annotations: rawOutput,
		Args:        cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				return &app.Error{Code: CodePortInUse, Message: fmt.Sprintf("cannot listen on 127.0.0.1:%d", port), Hint: "is mutti serve running already? Pick another port with --port", Err: err}
			}
			ctx := c.Context()
			srv := &http.Server{
				Handler:           server.New(d.App, d.Dir, d.Version).Handler(),
				ReadHeaderTimeout: 10 * time.Second,
				// Requests end with mutti, event streams included.
				BaseContext: func(net.Listener) context.Context { return ctx },
			}
			url := "http://" + ln.Addr().String()
			fmt.Fprintf(d.Out, "mutti serves %s (Ctrl-C to stop)\n", url)
			if open {
				if err := d.App.Host.OpenURL(url); err != nil {
					fmt.Fprintf(d.Err, "mutti: cannot open a browser: %v\n", err)
				}
			}
			go func() {
				<-ctx.Done()
				shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdown)
			}()
			err = srv.Serve(ln)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil // shut down by Ctrl-C
			}
			return err
		},
	}
	c.Flags().IntVar(&port, "port", 7477, "port on 127.0.0.1 to listen on")
	c.Flags().BoolVar(&open, "open", false, "open the UI in the browser")
	return c
}

// nopWriteCloser lets the MCP transport close stdout without closing it.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func runIDCmd(d *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "run-id",
		Short: "Print a new run id (sortable, carries its time), for naming per-run resources",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Fprintln(d.Out, app.NewRunID(d.App.Now()))
		},
	}
}

func sweepCmd(d *Deps, p *printer) *cobra.Command {
	var olderThan time.Duration
	c := &cobra.Command{
		Use:   "sweep",
		Short: "Remove leftovers of old runs, and run this project's sweep hooks",
		Long: `Remove what runs older than --older-than left behind (normally the reaper does), then run
x-mutti.hooks.sweep in this worktree's instance, if it runs, with MUTTI_SWEEP_OLDER_THAN in
seconds: the project removes per-run resources it made itself, such as test databases.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := d.App.Sweep(c.Context(), olderThan); err != nil {
				return err
			}
			hooks, err := d.App.SweepHooks(c.Context(), d.Dir, olderThan)
			if err != nil && !errors.Is(err, project.ErrNoComposeFile) {
				return err
			}
			p.result(map[string]bool{"hooks": hooks}, func(w io.Writer) {
				fmt.Fprintln(w, "Swept.")
			})
			return nil
		},
	}
	c.Flags().DurationVar(&olderThan, "older-than", 24*time.Hour, "only runs older than this")
	return c
}
