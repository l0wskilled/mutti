## mutti serve

Serve the web UI and HTTP API on 127.0.0.1 until interrupted

### Synopsis

Serve mutti's web UI, its HTTP API and its MCP server on 127.0.0.1 until interrupted.

The UI lists the instances of this machine and keeps them current, starts, stops and removes
them, and shows their logs and events. The API (/api/...) answers with the JSON of --json,
/api/events streams events, and /mcp serves the tools of mutti mcp over HTTP.

It answers only to localhost, and only pages of its own origin may change anything. It runs in
the foreground, like any other command; nothing is installed.

```
mutti serve [flags]
```

### Options

```
  -h, --help       help for serve
      --open       open the UI in the browser
      --port int   port on 127.0.0.1 to listen on (default 7477)
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

