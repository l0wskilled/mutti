## mutti reset

Delete the instance's data and start it fresh (bootstrap runs again)

```
mutti reset [flags]
```

### Options

```
      --build             rebuild images before starting
  -h, --help              help for reset
      --overlay strings   enable an x-mutti overlay (repeatable)
      --profile strings   enable a Compose profile (repeatable)
  -y, --yes               do not ask for confirmation
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

