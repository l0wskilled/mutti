package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/l0wskilled/mutti/internal/extend"
	"github.com/l0wskilled/mutti/internal/project"
)

// InitPlan is what mutti init would change; nothing is written until InitApply.
type InitPlan struct {
	// ComposeFile gets the x-mutti block; empty when the project has one already.
	ComposeFile string        `json:"composeFile,omitempty"`
	Draft       project.Draft `json:"draft"`
	XMutti      string        `json:"xMutti,omitempty"` // the block written into ComposeFile
	// AgentsFile gets the mutti section for AI coding agents; empty with InitOptions.NoAgents.
	AgentsFile    string `json:"agentsFile,omitempty"`
	AgentsSection string `json:"agentsSection,omitempty"`

	compose, agents []byte // the files' new content
	composeMode     os.FileMode
}

// Empty reports whether there is nothing to write.
func (p *InitPlan) Empty() bool { return p.ComposeFile == "" && p.AgentsFile == "" }

// InitOptions configure Init.
type InitOptions struct {
	NoAgents bool // leave AGENTS.md alone
}

// Agents section markers: init replaces what is between them and leaves the rest of the file.
const (
	agentsBegin = "<!-- mutti:begin (written by mutti init, which replaces this section) -->"
	agentsEnd   = "<!-- mutti:end -->"
)

// Init plans writing mutti's defaults into the project: an x-mutti block in the Compose file,
// which zero config does not need but makes them visible and editable, and a section in
// AGENTS.md telling AI coding agents how to use the environment. The changed Compose file is
// checked with docker compose before it is planned.
func (a *App) Init(ctx context.Context, dir string, opts InitOptions) (*InitPlan, error) {
	if err := a.checkCompose(ctx); err != nil {
		return nil, err
	}
	inst, err := a.Resolve(ctx, dir)
	var appErr *Error
	if errors.As(err, &appErr) && appErr.Code == CodeNoComposeFile {
		return nil, &Error{
			Code:    CodeNoComposeFile,
			Message: "mutti init sets up a Compose project for mutti, and there is none here",
			Hint:    "docker init writes a Compose file and Dockerfile for most languages; run mutti init after it",
			Err:     err,
		}
	}
	if err != nil {
		return nil, err
	}
	draft, err := project.NewDraft(inst.Model, inst.Files)
	if err != nil {
		return nil, err
	}
	plan := &InitPlan{Draft: draft}
	if len(inst.Model.XMutti) == 0 {
		if err := a.planCompose(ctx, inst, plan); err != nil {
			return nil, err
		}
	}
	if !opts.NoAgents {
		if err := planAgents(inst, plan); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

func (a *App) planCompose(ctx context.Context, inst *Instance, plan *InitPlan) error {
	file := inst.Files.Paths[0]
	src, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	block := plan.Draft.Render()
	content, err := project.Insert(src, block) // fails only with ErrNoPlace
	if err != nil {
		return &Error{Code: CodeInvalidConfig, Message: "cannot find where to put x-mutti in " + file, Hint: "add it by hand:\n" + string(block), Err: err}
	}

	// Compose reads the changed file in place of the original, from the same directory, so
	// relative paths and the project name stay as they are.
	check := filepath.Join(filepath.Dir(file), ".mutti-init-"+randomSuffix()+".yaml")
	if err := os.WriteFile(check, content, 0o600); err != nil { //nolint:gosec // next to the project's own Compose file
		return err
	}
	defer func() { _ = os.Remove(check) }()
	spec := a.userSpec(inst, nil, nil)
	spec.Files = slices.Clone(spec.Files)
	spec.Files[0] = check
	m, err := a.Compose.Config(ctx, spec)
	if err == nil {
		_, err = project.ParseConfig(m)
	}
	if err != nil {
		return &Error{Code: CodeInvalidConfig, Message: "the generated x-mutti block does not work in " + file, Hint: "add it by hand:\n" + string(block), Err: err}
	}
	plan.ComposeFile, plan.XMutti, plan.compose, plan.composeMode = file, string(block), content, fileMode(file)
	return nil
}

func planAgents(inst *Instance, plan *InitPlan) error {
	file := filepath.Join(inst.Root, "AGENTS.md")
	old, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var cmds []extend.Command
	if dir, ok := extend.FindProjectDir(inst.Root); ok {
		if cmds, _, err = extend.Commands(nil, dir); err != nil {
			return err
		}
	}
	section := agentsSection(inst, cmds)
	content := replaceSection(old, section)
	if bytes.Equal(content, old) {
		return nil
	}
	plan.AgentsFile, plan.AgentsSection, plan.agents = file, section, content
	return nil
}

func agentsSection(inst *Instance, cmds []extend.Command) string {
	var b strings.Builder
	fmt.Fprintln(&b, agentsBegin)
	fmt.Fprintln(&b, "## Dev environment (mutti)")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "This repository's services run with [mutti](https://github.com/l0wskilled/mutti): one isolated")
	fmt.Fprintln(&b, "Docker Compose environment per git worktree, with its own data, URLs and ports.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "- Start it: `mutti up` (returns once everything is healthy).")
	fmt.Fprintln(&b, "- URLs, ports and state: `mutti status --json`.")
	fmt.Fprintf(&b, "- Run a command in a service: `mutti exec <service> -- <command>`. Services: %s.\n", strings.Join(inst.Model.ServiceNames(), ", "))
	fmt.Fprintln(&b, "- Logs: `mutti logs <service>`.")
	fmt.Fprintln(&b, "- Tests against a throwaway copy with its own data, removed afterwards: `mutti run -- <command>`.")
	fmt.Fprintln(&b, "- Work in parallel without touching this environment: `mutti worktree add <name>` creates a")
	fmt.Fprintln(&b, "  worktree with its own environment.")
	if len(cmds) > 0 {
		fmt.Fprintln(&b, "- Project commands:")
		for _, c := range cmds {
			fmt.Fprintf(&b, "  - `mutti %s`: %s\n", c.Name, c.Short)
		}
	}
	fmt.Fprintln(&b, "- Every command takes `--json`; failures carry an error code and a hint.")
	fmt.Fprintln(&b, agentsEnd)
	return b.String()
}

// replaceSection puts section in place of the marked one in old, or appends it.
func replaceSection(old []byte, section string) []byte {
	s := string(old)
	if begin := strings.Index(s, agentsBegin); begin >= 0 {
		if end := strings.Index(s[begin:], agentsEnd); end >= 0 {
			end += begin + len(agentsEnd)
			return []byte(s[:begin] + strings.TrimSuffix(section, "\n") + s[end:])
		}
	}
	switch {
	case s == "":
		return []byte(section)
	case strings.HasSuffix(s, "\n"):
		return []byte(s + "\n" + section)
	default:
		return []byte(s + "\n\n" + section)
	}
}

// InitApply writes a plan.
func (a *App) InitApply(plan *InitPlan) error {
	if plan.ComposeFile != "" {
		if err := writeAtomic(plan.ComposeFile, plan.compose, plan.composeMode); err != nil {
			return err
		}
	}
	if plan.AgentsFile != "" {
		return writeAtomic(plan.AgentsFile, plan.agents, fileMode(plan.AgentsFile))
	}
	return nil
}

// fileMode is the permission bits of an existing file, or those of a new one.
func fileMode(path string) os.FileMode {
	if fi, err := os.Stat(path); err == nil {
		return fi.Mode().Perm()
	}
	return 0o644
}

// writeAtomic replaces path in one step, so a reader never sees half of it.
func writeAtomic(path string, content []byte, mode os.FileMode) error {
	tmp := filepath.Join(filepath.Dir(path), ".mutti-"+randomSuffix())
	if err := os.WriteFile(tmp, content, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func randomSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b) // crypto/rand never fails
	return hex.EncodeToString(b)
}
