## mutti init

Write mutti's settings into the Compose file and a section for AI agents into AGENTS.md

### Synopsis

Write mutti's settings into the Compose file and a section for AI agents into AGENTS.md.

mutti needs no configuration: init writes down what it would do anyway, as an x-mutti block to
edit from (main service, overlays, default routes and fixed-port suggestions as comments). It
leaves a Compose file that has x-mutti alone and never changes anything else in it. AGENTS.md
gets a section on how to use the environment, which init replaces on every run.

Shows the changes and asks first; --yes skips the question, --dry-run only shows them.

```
mutti init [flags]
```

### Options

```
      --dry-run     only show the changes
  -h, --help        help for init
      --no-agents   leave AGENTS.md alone
  -y, --yes         do not ask for confirmation
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

