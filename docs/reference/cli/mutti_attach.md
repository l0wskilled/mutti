## mutti attach

Connect the container mutti runs in to this instance's network (e.g. a CI step)

### Synopsis

Connect the container mutti runs in to this instance's network: it then reaches the
services by name (db, api, ...) and the instance's hostnames through the router, for example
for end-to-end tests in a CI step container. Detach again before tearing the instance down,
or let mutti destroy do it.

```
mutti attach [flags]
```

### Options

```
      --detach   disconnect again
  -h, --help     help for attach
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

