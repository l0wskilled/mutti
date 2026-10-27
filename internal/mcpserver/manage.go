package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/extend"
	"github.com/l0wskilled/mutti/internal/project"
)

// addManageTools registers the tools beyond everyday work: removing data and worktrees,
// tidying up, the router, setup and the project's own commands.
func (s *server) addManageTools(srv *mcp.Server) {
	no, yes := false, true
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	destructive := &mcp.ToolAnnotations{DestructiveHint: &yes}
	mcp.AddTool(srv, &mcp.Tool{Name: "reset", Description: "Delete all data of the environment and start it fresh; the bootstrap hooks run again.", Annotations: destructive}, s.reset)
	mcp.AddTool(srv, &mcp.Tool{Name: "destroy", Description: "Remove the environment with all its containers, volumes and data.", Annotations: destructive}, s.destroy)
	mcp.AddTool(srv, &mcp.Tool{Name: "worktree_remove", Description: "Destroy a worktree's environment with its data and remove the worktree; the branch stays.", Annotations: destructive}, s.worktreeRemove)
	mcp.AddTool(srv, &mcp.Tool{Name: "tidy", Description: "Find environments whose worktree is gone and, with idle, running ones nobody used for that long. Only reports them unless apply is set, then removes or stops them.", Annotations: destructive}, s.tidy)
	mcp.AddTool(srv, &mcp.Tool{Name: "sweep", Description: "Remove what old runs left behind, then run the project's sweep hooks in the environment of dir if it runs.", Annotations: destructive}, s.sweep)
	mcp.AddTool(srv, &mcp.Tool{Name: "rename", Description: "Give the environment of a worktree a new name, which also changes its hostname.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no}}, s.rename)
	mcp.AddTool(srv, &mcp.Tool{Name: "compose", Description: "Run any docker compose command against the environment, e.g. [\"ps\", \"--all\"] or [\"config\"].", Annotations: destructive}, s.compose)
	mcp.AddTool(srv, &mcp.Tool{Name: "doctor", Description: "Check Docker, Compose and the project, with hints on what to fix.", Annotations: readOnly}, s.doctor)
	mcp.AddTool(srv, &mcp.Tool{Name: "env", Description: "Return the MUTTI_* variables of the environment: URLs, ports, names.", Annotations: readOnly}, s.env)
	mcp.AddTool(srv, &mcp.Tool{Name: "init", Description: "Show what mutti init would write: an x-mutti block into the Compose file and a section for AI agents into AGENTS.md. With apply it writes them.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}}, s.initProject)
	mcp.AddTool(srv, &mcp.Tool{Name: "router_status", Description: "Start the machine-wide router if needed, sync its routes and show which environments it serves.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}}, s.routerStatus)
	mcp.AddTool(srv, &mcp.Tool{Name: "router_restart", Description: "Recreate the machine-wide router container; every environment's URLs are briefly gone.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no}}, s.routerRestart)
	mcp.AddTool(srv, &mcp.Tool{Name: "attach", Description: "Connect the container mutti runs in to the environment's network, so it reaches services by name; detach disconnects again.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}}, s.attach)
	mcp.AddTool(srv, &mcp.Tool{Name: "open", Description: "Open the environment, or a service, in the user's browser.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no}}, s.open)
	mcp.AddTool(srv, &mcp.Tool{Name: "commands", Description: "List the project's own commands (.mutti/commands) and the user's (~/.mutti/commands), with where they run.", Annotations: readOnly}, s.commands)
	mcp.AddTool(srv, &mcp.Tool{Name: "command", Description: "Run one of the project's own commands (see commands) and return its output and exit code.", Annotations: destructive}, s.command)
}

// ResetInput configures reset.
type ResetInput struct {
	Dir      string   `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	Overlays []string `json:"overlays,omitempty" jsonschema:"x-mutti overlays to enable"`
	Profiles []string `json:"profiles,omitempty" jsonschema:"Compose profiles to enable"`
	Build    bool     `json:"build,omitempty" jsonschema:"rebuild images before starting"`
}

func (s *server) reset(ctx context.Context, _ *mcp.CallToolRequest, in ResetInput) (*mcp.CallToolResult, Started, error) {
	a, out := s.call()
	res, err := a.Reset(ctx, s.resolve(in.Dir), app.UpOptions{
		Selection: app.Selection{Overlays: in.Overlays, Profiles: in.Profiles}, Build: in.Build,
	})
	if err != nil {
		return nil, Started{}, toolErr(withOutput(err, out))
	}
	return nil, started(res, out), nil
}

func (s *server) destroy(ctx context.Context, _ *mcp.CallToolRequest, in Where) (*mcp.CallToolResult, Done, error) {
	a, out := s.call()
	inst, err := a.Destroy(ctx, s.resolve(in.Dir))
	if err != nil {
		return nil, Done{}, toolErr(withOutput(err, out))
	}
	return nil, Done{Instance: inst.Name}, nil
}

// WorktreeRemoveInput names the worktree to remove.
type WorktreeRemoveInput struct {
	Dir    string `json:"dir,omitempty" jsonschema:"a worktree of the repository, absolute or relative to where mutti mcp runs; default: that directory"`
	Target string `json:"target" jsonschema:"name of the worktree (as given to worktree_add) or its path"`
	Force  bool   `json:"force,omitempty" jsonschema:"also drop uncommitted changes"`
}

// Removed is the path of a removed worktree.
type Removed struct {
	Path string `json:"path"`
}

func (s *server) worktreeRemove(ctx context.Context, _ *mcp.CallToolRequest, in WorktreeRemoveInput) (*mcp.CallToolResult, Removed, error) {
	a, out := s.call()
	path, err := a.WorktreeRemove(ctx, s.resolve(in.Dir), app.WorktreeRemoveOptions{Target: in.Target, Force: in.Force})
	if err != nil {
		return nil, Removed{}, toolErr(withOutput(err, out))
	}
	return nil, Removed{Path: path}, nil
}

// TidyInput configures tidy.
type TidyInput struct {
	Project string `json:"project,omitempty" jsonschema:"only this project's environments; default: all"`
	Idle    string `json:"idle,omitempty" jsonschema:"also stop running environments unused for this long, e.g. \"24h\""`
	Apply   bool   `json:"apply,omitempty" jsonschema:"remove and stop them; otherwise only report"`
}

func (s *server) tidy(ctx context.Context, _ *mcp.CallToolRequest, in TidyInput) (*mcp.CallToolResult, *app.TidyResult, error) {
	idle, err := duration("idle", in.Idle, 0)
	if err != nil {
		return nil, nil, err
	}
	a, out := s.call()
	res, err := a.Tidy(ctx, app.TidyOptions{Project: in.Project, Idle: idle, Apply: in.Apply})
	if err != nil {
		return nil, nil, toolErr(withOutput(err, out))
	}
	return nil, res, nil
}

// SweepInput configures sweep.
type SweepInput struct {
	Dir       string `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	OlderThan string `json:"olderThan,omitempty" jsonschema:"only runs older than this, e.g. \"1h\"; default \"24h\""`
}

// Swept tells whether the project's sweep hooks ran.
type Swept struct {
	Hooks bool `json:"hooks"`
}

func (s *server) sweep(ctx context.Context, _ *mcp.CallToolRequest, in SweepInput) (*mcp.CallToolResult, Swept, error) {
	olderThan, err := duration("olderThan", in.OlderThan, 24*time.Hour)
	if err != nil {
		return nil, Swept{}, err
	}
	a, out := s.call()
	if err := a.Sweep(ctx, olderThan); err != nil {
		return nil, Swept{}, toolErr(withOutput(err, out))
	}
	hooks, err := a.SweepHooks(ctx, s.resolve(in.Dir), olderThan)
	if err != nil && !errors.Is(err, project.ErrNoComposeFile) {
		return nil, Swept{}, toolErr(withOutput(err, out))
	}
	return nil, Swept{Hooks: hooks}, nil
}

// duration parses a duration the agent gave by name, or returns def for none.
func duration(name, value string, def time.Duration) (time.Duration, error) {
	if value == "" {
		return def, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return d, nil
}

// RenameInput names the new name.
type RenameInput struct {
	Dir  string `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	Name string `json:"name" jsonschema:"the new name: lower case letters, digits and -"`
}

// Renamed describes a renamed environment.
type Renamed struct {
	Instance string `json:"instance"`
	Hostname string `json:"hostname"`
}

func (s *server) rename(ctx context.Context, _ *mcp.CallToolRequest, in RenameInput) (*mcp.CallToolResult, Renamed, error) {
	a, out := s.call()
	inst, err := a.Rename(ctx, s.resolve(in.Dir), in.Name)
	if err != nil {
		return nil, Renamed{}, toolErr(withOutput(err, out))
	}
	return nil, Renamed{Instance: inst.Name, Hostname: inst.Hostname}, nil
}

// ComposeInput is a docker compose command.
type ComposeInput struct {
	Dir      string   `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	Args     []string `json:"args" jsonschema:"the docker compose arguments, e.g. [\"ps\", \"--all\"]"`
	Overlays []string `json:"overlays,omitempty" jsonschema:"x-mutti overlays to enable"`
}

func (s *server) compose(ctx context.Context, _ *mcp.CallToolRequest, in ComposeInput) (*mcp.CallToolResult, Exited, error) {
	if len(in.Args) == 0 {
		return nil, Exited{}, errors.New("args is empty")
	}
	a, out := s.call()
	code, err := a.ComposePassthrough(ctx, s.resolve(in.Dir), app.Selection{Overlays: in.Overlays}, in.Args)
	if err != nil {
		return nil, Exited{}, toolErr(withOutput(err, out))
	}
	return nil, Exited{ExitCode: code, Output: out.text(), Truncated: out.cut()}, nil
}

func (s *server) doctor(ctx context.Context, _ *mcp.CallToolRequest, in Where) (*mcp.CallToolResult, *app.Report, error) {
	a, _ := s.call()
	return nil, a.Doctor(ctx, s.resolve(in.Dir)), nil
}

// Env holds the MUTTI_* variables by name.
type Env struct {
	Variables map[string]string `json:"variables"`
}

func (s *server) env(ctx context.Context, _ *mcp.CallToolRequest, in Where) (*mcp.CallToolResult, Env, error) {
	a, _ := s.call()
	vars, err := a.EnvOf(ctx, s.resolve(in.Dir))
	if err != nil {
		return nil, Env{}, toolErr(err)
	}
	env := Env{Variables: map[string]string{}}
	for _, kv := range vars {
		k, v, _ := strings.Cut(kv, "=")
		env.Variables[k] = v
	}
	return nil, env, nil
}

// InitInput configures init.
type InitInput struct {
	Dir      string `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	NoAgents bool   `json:"noAgents,omitempty" jsonschema:"leave AGENTS.md alone"`
	Apply    bool   `json:"apply,omitempty" jsonschema:"write the changes; otherwise only show them"`
}

// Initialized is what init would write, or wrote.
type Initialized struct {
	*app.InitPlan
	Written bool `json:"written"`
}

func (s *server) initProject(ctx context.Context, _ *mcp.CallToolRequest, in InitInput) (*mcp.CallToolResult, Initialized, error) {
	a, _ := s.call()
	plan, err := a.Init(ctx, s.resolve(in.Dir), app.InitOptions{NoAgents: in.NoAgents})
	if err != nil {
		return nil, Initialized{}, toolErr(err)
	}
	apply := in.Apply && !plan.Empty()
	if apply {
		if err := a.InitApply(plan); err != nil {
			return nil, Initialized{}, toolErr(err)
		}
	}
	return nil, Initialized{InitPlan: plan, Written: apply}, nil
}

func (s *server) routerStatus(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, *app.RouterStatus, error) {
	a, _ := s.call()
	st, err := a.Reconcile(ctx)
	if err != nil {
		return nil, nil, toolErr(err)
	}
	return nil, st, nil
}

func (s *server) routerRestart(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, *app.RouterStatus, error) {
	a, _ := s.call()
	st, err := a.RouterRestart(ctx)
	if err != nil {
		return nil, nil, toolErr(err)
	}
	return nil, st, nil
}

// AttachInput configures attach.
type AttachInput struct {
	Dir    string `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	Detach bool   `json:"detach,omitempty" jsonschema:"disconnect again"`
}

// Attached tells which environment's network the container is on now, or left.
type Attached struct {
	Instance string `json:"instance"`
	Attached bool   `json:"attached"`
}

func (s *server) attach(ctx context.Context, _ *mcp.CallToolRequest, in AttachInput) (*mcp.CallToolResult, Attached, error) {
	a, _ := s.call()
	inst, err := a.Attach(ctx, s.resolve(in.Dir), in.Detach)
	if err != nil {
		return nil, Attached{}, toolErr(err)
	}
	return nil, Attached{Instance: inst.Name, Attached: !in.Detach}, nil
}

func (s *server) open(ctx context.Context, _ *mcp.CallToolRequest, in URLInput) (*mcp.CallToolResult, URL, error) {
	a, _ := s.call()
	u, err := a.Open(ctx, s.resolve(in.Dir), in.Service)
	if err != nil {
		return nil, URL{}, toolErr(err)
	}
	return nil, URL{URL: u}, nil
}

// CustomCommand is one of the project's own commands.
type CustomCommand struct {
	Name    string `json:"name"`
	Short   string `json:"short,omitempty"`
	Service string `json:"service,omitempty" jsonschema:"the service it runs in; empty: on the host"`
}

// Commands lists the project's own commands.
type Commands struct {
	Commands []CustomCommand `json:"commands"`
}

// customCommands finds the commands of the user's ~/.mutti and of the project of dir.
func (s *server) customCommands(dir string) ([]extend.Command, error) {
	dirs := []string{s.app.Dirs.Config}
	if p, ok := extend.FindProjectDir(dir); ok {
		dirs = append(dirs, p)
	}
	cmds, _, err := extend.Commands(nil, dirs...)
	return cmds, err
}

func (s *server) commands(_ context.Context, _ *mcp.CallToolRequest, in Where) (*mcp.CallToolResult, Commands, error) {
	cmds, err := s.customCommands(s.resolve(in.Dir))
	if err != nil {
		return nil, Commands{}, err
	}
	out := Commands{Commands: []CustomCommand{}}
	for _, c := range cmds {
		out.Commands = append(out.Commands, CustomCommand{Name: c.Name, Short: c.Short, Service: c.Service})
	}
	return nil, out, nil
}

// CommandInput runs one of the project's own commands.
type CommandInput struct {
	Dir  string   `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	Name string   `json:"name" jsonschema:"name of the command, as commands lists it"`
	Args []string `json:"args,omitempty" jsonschema:"arguments for the command"`
}

func (s *server) command(ctx context.Context, _ *mcp.CallToolRequest, in CommandInput) (*mcp.CallToolResult, Exited, error) {
	dir := s.resolve(in.Dir)
	cmds, err := s.customCommands(dir)
	if err != nil {
		return nil, Exited{}, err
	}
	i := slices.IndexFunc(cmds, func(c extend.Command) bool { return c.Name == in.Name })
	if i < 0 {
		return nil, Exited{}, fmt.Errorf("no command %q; commands lists them", in.Name)
	}
	a, out := s.call()
	code, err := a.RunCommand(ctx, dir, cmds[i], in.Args)
	if err != nil {
		return nil, Exited{}, toolErr(withOutput(err, out))
	}
	return nil, Exited{ExitCode: code, Output: out.text(), Truncated: out.cut()}, nil
}
