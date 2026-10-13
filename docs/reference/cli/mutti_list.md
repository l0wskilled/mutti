## mutti list

List all instances on this machine

### Synopsis

List all instances on this machine.

With --watch, the list is shown again whenever a container of an instance changes, until
interrupted; with --json too, every list is one JSON object on a line of its own.

```
mutti list [flags]
```

### Options

```
  -h, --help    help for list
  -w, --watch   show the list again whenever an instance changes
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

