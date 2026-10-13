## mutti exec

Run a command in a service of this instance, as your host user

```
mutti exec <service> -- <command> [args...] [flags]
```

### Options

```
  -e, --env stringArray   set an environment variable, KEY=VALUE (repeatable)
  -h, --help              help for exec
  -u, --user string       run as "host", "service" or user[:group] (default from x-mutti exec.user)
  -w, --workdir string    working directory inside the container
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

