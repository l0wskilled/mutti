// Package mcpserver offers mutti's use cases as tools of a Model Context Protocol server, for AI
// coding agents: start and inspect environments, run commands and tests in them.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/compose"
)

// instructions tell the agent what the server is for and how its tools fit together.
const instructions = `mutti runs one isolated Docker Compose environment per git worktree, with its own data,
URLs and ports. Start with status to see the environment of the current worktree; up starts it.
exec runs commands inside a service, logs shows what services print, run executes a command
(usually tests) against a throwaway copy with its own data. To work in parallel without touching
the current environment, worktree_add creates a worktree with an environment of its own; pass its
path as dir to the other tools. Errors carry a code and a hint on what to do.`

// New returns the MCP server for the project at dir. Every tool call works on its own copy of a,
// with output captured for the result: stdout belongs to the protocol.
func New(a *app.App, dir, version string) *mcp.Server {
	s := &server{app: a, dir: dir}
	srv := mcp.NewServer(&mcp.Implementation{Name: "mutti", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	no, yes := false, true
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(srv, &mcp.Tool{Name: "list", Description: "List every mutti environment on this machine with status, branch, URLs and ports.", Annotations: readOnly}, s.list)
	mcp.AddTool(srv, &mcp.Tool{Name: "status", Description: "Show the environment of a worktree service by service: state, URLs, ports.", Annotations: readOnly}, s.status)
	mcp.AddTool(srv, &mcp.Tool{Name: "url", Description: "Return the HTTPS URL of the environment, or of one of its services.", Annotations: readOnly}, s.url)
	mcp.AddTool(srv, &mcp.Tool{Name: "logs", Description: "Return the recent logs of the environment's services.", Annotations: readOnly}, s.logs)
	mcp.AddTool(srv, &mcp.Tool{Name: "up", Description: "Start the environment of a worktree (or some of its services) and wait until it is healthy.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}}, s.up)
	mcp.AddTool(srv, &mcp.Tool{Name: "stop", Description: "Stop the environment's containers, or some services'; data and containers stay.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}}, s.stop)
	mcp.AddTool(srv, &mcp.Tool{Name: "restart", Description: "Restart the environment's containers, or some services'.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}}, s.restart)
	mcp.AddTool(srv, &mcp.Tool{Name: "down", Description: "Remove the environment's containers and networks; its data stays.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes, IdempotentHint: true}}, s.down)
	mcp.AddTool(srv, &mcp.Tool{Name: "exec", Description: "Run a command in a running service of the environment and return its output and exit code.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes}}, s.exec)
	mcp.AddTool(srv, &mcp.Tool{Name: "run", Description: "Run a command, usually tests, against a throwaway copy of the environment with its own data; the copy is removed afterwards.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no}}, s.run)
	mcp.AddTool(srv, &mcp.Tool{Name: "worktree_add", Description: "Create a git worktree with its own environment, for parallel work, and start it.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no}}, s.worktreeAdd)
	return srv
}

type server struct {
	app *app.App
	dir string
}

// Where is the worktree a tool works on.
type Where struct {
	Dir string `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
}

func (s *server) resolve(dir string) string {
	switch {
	case dir == "":
		return s.dir
	case filepath.IsAbs(dir):
		return filepath.Clean(dir)
	default:
		return filepath.Join(s.dir, dir)
	}
}

// call returns a copy of the app whose commands write into out, which keeps the last part of
// what they print.
func (s *server) call() (*app.App, *capped) {
	out := &capped{}
	a := *s.app
	a.IO = compose.IO{In: strings.NewReader(""), Out: out, Err: out}
	a.Host.StdinTTY = false
	return &a, out
}

// toolErr turns an error into what the agent reads: the code to branch on, and the hint.
func toolErr(err error) error {
	var e *app.Error
	if errors.As(err, &e) {
		msg := e.Code + ": " + err.Error()
		if e.Hint != "" {
			msg += "\nhint: " + e.Hint
		}
		return errors.New(msg)
	}
	return err
}

// Empty is the input of tools that take nothing.
type Empty struct{}

// Instances is the output of list.
type Instances struct {
	Instances []app.Summary `json:"instances"`
}

func (s *server) list(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, Instances, error) {
	a, _ := s.call()
	list, err := a.List(ctx)
	if err != nil {
		return nil, Instances{}, toolErr(err)
	}
	return nil, Instances{Instances: list}, nil
}

func (s *server) status(ctx context.Context, _ *mcp.CallToolRequest, in Where) (*mcp.CallToolResult, *app.InstanceStatus, error) {
	a, _ := s.call()
	st, err := a.Status(ctx, s.resolve(in.Dir))
	if err != nil {
		return nil, nil, toolErr(err)
	}
	return nil, st, nil
}

// URLInput selects a service.
type URLInput struct {
	Dir     string `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	Service string `json:"service,omitempty" jsonschema:"service whose URL to return; default: the environment's main URL"`
}

// URL is the output of url.
type URL struct {
	URL string `json:"url"`
}

func (s *server) url(ctx context.Context, _ *mcp.CallToolRequest, in URLInput) (*mcp.CallToolResult, URL, error) {
	a, _ := s.call()
	u, err := a.URL(ctx, s.resolve(in.Dir), in.Service)
	if err != nil {
		return nil, URL{}, toolErr(err)
	}
	return nil, URL{URL: u}, nil
}

// LogsInput selects logs.
type LogsInput struct {
	Dir      string   `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	Services []string `json:"services,omitempty" jsonschema:"services to show; default: all"`
	Tail     int      `json:"tail,omitempty" jsonschema:"lines per service, counted from the end; default 200"`
}

// Output is what a command printed, stdout and stderr together. Truncated means only the end of
// it is there.
type Output struct {
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
}

func (s *server) logs(ctx context.Context, _ *mcp.CallToolRequest, in LogsInput) (*mcp.CallToolResult, Output, error) {
	a, out := s.call()
	tail := in.Tail
	if tail <= 0 {
		tail = 200
	}
	args := append([]string{"logs", "--no-color", "--tail", fmt.Sprint(tail)}, in.Services...)
	if _, err := a.ComposePassthrough(ctx, s.resolve(in.Dir), app.Selection{}, args); err != nil {
		return nil, Output{}, toolErr(err)
	}
	return nil, out.result(), nil
}

// UpInput configures up.
type UpInput struct {
	Dir      string   `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	Services []string `json:"services,omitempty" jsonschema:"start only these services and what they depend on; default: all"`
	Overlays []string `json:"overlays,omitempty" jsonschema:"x-mutti overlays to enable"`
	Profiles []string `json:"profiles,omitempty" jsonschema:"Compose profiles to enable"`
	Build    bool     `json:"build,omitempty" jsonschema:"rebuild images before starting"`
}

// Started describes a started environment.
type Started struct {
	Instance     string `json:"instance"`
	Hostname     string `json:"hostname"`
	Path         string `json:"path"`
	URL          string `json:"url,omitempty"`
	Bootstrapped bool   `json:"bootstrapped"` // the bootstrap hooks ran (first start)
	Output       string `json:"output"`
	Truncated    bool   `json:"truncated,omitempty"`
}

func (s *server) up(ctx context.Context, _ *mcp.CallToolRequest, in UpInput) (*mcp.CallToolResult, Started, error) {
	a, out := s.call()
	res, err := a.Up(ctx, s.resolve(in.Dir), app.UpOptions{
		Selection: app.Selection{Overlays: in.Overlays, Profiles: in.Profiles}, Build: in.Build, Services: in.Services,
	})
	if err != nil {
		return nil, Started{}, toolErr(withOutput(err, out))
	}
	return nil, started(res, out), nil
}

func started(res *app.UpResult, out *capped) Started {
	return Started{
		Instance: res.Instance.Name, Hostname: res.Instance.Hostname, Path: res.Instance.Root,
		URL: res.URL, Bootstrapped: res.Bootstrapped, Output: out.text(), Truncated: out.cut(),
	}
}

// Done names the instance a tool acted on.
type Done struct {
	Instance string `json:"instance"`
}

// ServicesInput names services of a worktree's environment.
type ServicesInput struct {
	Dir      string   `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	Services []string `json:"services,omitempty" jsonschema:"only these services; default: all"`
}

func (s *server) stop(ctx context.Context, _ *mcp.CallToolRequest, in ServicesInput) (*mcp.CallToolResult, Done, error) {
	return s.services(ctx, in, (*app.App).Stop)
}

func (s *server) restart(ctx context.Context, _ *mcp.CallToolRequest, in ServicesInput) (*mcp.CallToolResult, Done, error) {
	return s.services(ctx, in, (*app.App).Restart)
}

// services runs a command on the environment's existing containers.
func (s *server) services(ctx context.Context, in ServicesInput, run func(*app.App, context.Context, string, app.Selection, ...string) (*app.Instance, error)) (*mcp.CallToolResult, Done, error) {
	a, out := s.call()
	inst, err := run(a, ctx, s.resolve(in.Dir), app.Selection{}, in.Services...)
	if err != nil {
		return nil, Done{}, toolErr(withOutput(err, out))
	}
	return nil, Done{Instance: inst.Name}, nil
}

func (s *server) down(ctx context.Context, _ *mcp.CallToolRequest, in Where) (*mcp.CallToolResult, Done, error) {
	a, out := s.call()
	inst, err := a.Down(ctx, s.resolve(in.Dir), app.Selection{})
	if err != nil {
		return nil, Done{}, toolErr(withOutput(err, out))
	}
	return nil, Done{Instance: inst.Name}, nil
}

// ExecInput is a command for a service.
type ExecInput struct {
	Dir     string            `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	Service string            `json:"service" jsonschema:"service to run the command in"`
	Command []string          `json:"command" jsonschema:"the command and its arguments, e.g. [\"php\", \"bin/console\", \"cache:clear\"]; use [\"sh\", \"-c\", \"...\"] for shell syntax"`
	Workdir string            `json:"workdir,omitempty" jsonschema:"working directory inside the container"`
	Env     map[string]string `json:"env,omitempty" jsonschema:"extra environment variables"`
	User    string            `json:"user,omitempty" jsonschema:"\"host\" (your user, the default), \"service\" (the image's user) or user[:group]"`
}

// Exited is what a command printed and how it ended.
type Exited struct {
	ExitCode  int    `json:"exitCode"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
}

func (s *server) exec(ctx context.Context, _ *mcp.CallToolRequest, in ExecInput) (*mcp.CallToolResult, Exited, error) {
	if len(in.Command) == 0 {
		return nil, Exited{}, errors.New("command is empty")
	}
	a, out := s.call()
	opts := app.ExecOptions{User: in.User, Workdir: in.Workdir}
	for _, k := range sortedKeys(in.Env) {
		opts.Env = append(opts.Env, k+"="+in.Env[k])
	}
	code, err := a.Exec(ctx, s.resolve(in.Dir), in.Service, in.Command, opts)
	if err != nil {
		return nil, Exited{}, toolErr(withOutput(err, out))
	}
	return nil, Exited{ExitCode: code, Output: out.text(), Truncated: out.cut()}, nil
}

// RunInput is a command for a throwaway environment.
type RunInput struct {
	Dir      string   `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	Command  []string `json:"command" jsonschema:"the command, run on the host with MUTTI_URL and the other MUTTI_* variables set, or in service"`
	Service  string   `json:"service,omitempty" jsonschema:"run the command in a one-off container of this service instead of on the host"`
	Start    []string `json:"start,omitempty" jsonschema:"start only these services and what they depend on; default: all"`
	Overlays []string `json:"overlays,omitempty" jsonschema:"x-mutti overlays to enable"`
	Profiles []string `json:"profiles,omitempty" jsonschema:"Compose profiles to enable"`
}

// Ran is the result of a run.
type Ran struct {
	RunID     string `json:"runId"`
	ExitCode  int    `json:"exitCode"`
	Logs      string `json:"logs" jsonschema:"file with the logs of all services of the run"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
}

func (s *server) run(ctx context.Context, _ *mcp.CallToolRequest, in RunInput) (*mcp.CallToolResult, Ran, error) {
	if len(in.Command) == 0 {
		return nil, Ran{}, errors.New("command is empty")
	}
	a, out := s.call()
	res, err := a.Run(ctx, s.resolve(in.Dir), app.RunOptions{
		Selection: app.Selection{Overlays: in.Overlays, Profiles: in.Profiles},
		Services:  in.Start, Service: in.Service, Command: in.Command,
	})
	if err != nil {
		return nil, Ran{}, toolErr(withOutput(err, out))
	}
	return nil, Ran{RunID: res.RunID, ExitCode: res.ExitCode, Logs: res.Logs, Output: out.text(), Truncated: out.cut()}, nil
}

// WorktreeInput describes a new worktree.
type WorktreeInput struct {
	Dir      string `json:"dir,omitempty" jsonschema:"worktree directory, absolute or relative to where mutti mcp runs; default: that directory"`
	Name     string `json:"name" jsonschema:"name of the worktree, also of its new branch: lower case letters, digits and -"`
	Branch   string `json:"branch,omitempty" jsonschema:"check out this existing branch instead of creating one"`
	From     string `json:"from,omitempty" jsonschema:"base of the new branch; default: the current HEAD"`
	DataFrom string `json:"dataFrom,omitempty" jsonschema:"copy the data of another worktree's environment (\"main\", a name or a path) instead of starting empty"`
	NoUp     bool   `json:"noUp,omitempty" jsonschema:"only create the worktree, do not start its environment"`
}

// Worktree describes a created worktree.
type Worktree struct {
	Path    string   `json:"path" jsonschema:"pass it as dir to the other tools"`
	Branch  string   `json:"branch"`
	Started *Started `json:"started,omitempty"`
}

func (s *server) worktreeAdd(ctx context.Context, _ *mcp.CallToolRequest, in WorktreeInput) (*mcp.CallToolResult, Worktree, error) {
	a, out := s.call()
	res, err := a.WorktreeAdd(ctx, s.resolve(in.Dir), app.WorktreeAddOptions{
		Name: in.Name, Branch: in.Branch, From: in.From, DataFrom: in.DataFrom, NoUp: in.NoUp,
	})
	if err != nil {
		return nil, Worktree{}, toolErr(withOutput(err, out))
	}
	w := Worktree{Path: res.Path, Branch: res.Branch}
	if res.Up != nil {
		st := started(res.Up, out)
		w.Started = &st
	}
	return nil, w, nil
}
