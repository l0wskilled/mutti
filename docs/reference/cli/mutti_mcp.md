## mutti mcp

Serve mutti to AI coding agents as a Model Context Protocol server over stdio

### Synopsis

Serve mutti to AI coding agents as a Model Context Protocol (MCP) server over stdin/stdout.

The agent gets tools to start and inspect environments (list, status, url, logs, diagnose, up,
stop, restart, down), to run commands in services (exec, compose) and against throwaway copies
(run), to work in parallel in worktrees with environments of their own (worktree_add,
worktree_remove), to reset, destroy, tidy and sweep, and to run the project's own commands
(commands, command). Everything the CLI does but the terminal UI, the web UI, shell and trust,
which need a person. Register it with your agent, e.g.:

  claude mcp add mutti -- mutti mcp

```
mutti mcp [flags]
```

### Options

```
  -h, --help   help for mcp
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

