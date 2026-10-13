## mutti sweep

Remove leftovers of old runs, and run this project's sweep hooks

### Synopsis

Remove what runs older than --older-than left behind (normally the reaper does), then run
x-mutti.hooks.sweep in this worktree's instance, if it runs, with MUTTI_SWEEP_OLDER_THAN in
seconds: the project removes per-run resources it made itself, such as test databases.

```
mutti sweep [flags]
```

### Options

```
  -h, --help                  help for sweep
      --older-than duration   only runs older than this (default 24h0m0s)
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

