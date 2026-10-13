## mutti events

Print what happens to the containers of all instances, as it happens

### Synopsis

Print what happens to the containers of all instances, as it happens, until interrupted:
created, started, stopped, removed, paused, unpaused, healthy, unhealthy.

With --json, every event is one JSON object on a line of its own, for tools that follow along.

```
mutti events [flags]
```

### Options

```
  -h, --help   help for events
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

