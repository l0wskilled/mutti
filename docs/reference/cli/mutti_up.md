## mutti up

Start this worktree's instance and wait until it is healthy

### Synopsis

Start this worktree's instance and wait until it is healthy. Given services, start only
those and what they depend on; hooks of services that do not run are skipped.

```
mutti up [service...] [flags]
```

### Options

```
      --build             rebuild images before starting
  -h, --help              help for up
      --overlay strings   enable an x-mutti overlay (repeatable)
      --profile strings   enable a Compose profile (repeatable)
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

