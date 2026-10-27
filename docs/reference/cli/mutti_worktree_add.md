## mutti worktree add

Create a worktree next to the main one (<repo>-<name>, new branch <name>) and start it

```
mutti worktree add <name> [flags]
```

### Options

```
      --branch string      check out this existing branch (local or remote) instead of creating one
      --build              rebuild images before starting
      --data-from string   copy the data of another worktree's instance ("main", a worktree or instance name, or a path) instead of bootstrapping
      --from string        base of the new branch (default: current HEAD)
  -h, --help               help for add
      --no-up              only create the worktree
      --overlay strings    enable an x-mutti overlay (repeatable)
      --path string        where to create the worktree
      --profile strings    enable a Compose profile (repeatable)
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti worktree](mutti_worktree.md)	 - Create and remove git worktrees together with their instances

