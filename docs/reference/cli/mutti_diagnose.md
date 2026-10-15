## mutti diagnose

Explain what is wrong with the instance's services: health checks, exit codes, last logs

### Synopsis

Explain what is wrong with the instance's services: why they are unhealthy, crashing or
gone, what their health checks said and what they printed last. Without services it looks at
every service that is not well; given services, it explains those whatever their state.

```
mutti diagnose [service...] [flags]
```

### Options

```
  -h, --help   help for diagnose
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

