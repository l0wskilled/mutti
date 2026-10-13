## mutti prompt

Print a short state line for your shell prompt (empty outside an instance)

### Synopsis

Print a short state line for your shell prompt, e.g. "shop-feature running 6/6".
It is fast (no Compose involved), prints nothing outside an instance and never fails.

Starship (~/.config/starship.toml):

  [custom.mutti]
  command = "mutti prompt"
  when = true
  format = "[$output]($style) "

fish (in fish_right_prompt):

  mutti prompt

```
mutti prompt [flags]
```

### Options

```
  -h, --help   help for prompt
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

