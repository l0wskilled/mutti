# commands

Project commands: scripts in `.mutti/commands` become mutti commands, committed with the project.

- `.mutti/commands/host/greet` runs on your machine, with the `MUTTI_*` variables set.
- `.mutti/commands/web/pages` runs inside the `web` service, as your user.

The first `# ` comment is the help text `mutti --help` shows.

```console
$ mutti up
$ mutti greet Ada
Hello Ada, this worktree runs at https://commands.localhost
$ mutti pages
index.html
```
