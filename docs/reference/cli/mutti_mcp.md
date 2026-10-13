## mutti mcp

Serve mutti to AI coding agents as a Model Context Protocol server over stdio

### Synopsis

Serve mutti to AI coding agents as a Model Context Protocol (MCP) server over stdin/stdout.

The agent gets tools to start and inspect environments (list, status, url, logs, up, stop, down),
to run commands in services (exec) and against throwaway copies (run), and to work in parallel in
worktrees with environments of their own (worktree_add). Register it with your agent, e.g.:

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

