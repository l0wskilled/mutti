## mutti tidy

Remove instances whose worktree is gone, stop idle ones (shows them only, unless --yes)

```
mutti tidy [flags]
```

### Options

```
  -h, --help             help for tidy
      --idle duration    also stop running instances unused for this long, e.g. 24h
      --project string   only this project's instances
  -y, --yes              do it
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

