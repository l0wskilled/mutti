# Examples

Small projects that show what mutti does, each with a README. They use tiny public images and
start in seconds.

| Example | Shows |
|---|---|
| [minimal](minimal) | no configuration at all; worktrees side by side |
| [spa-api](spa-api) | a single-page app and its API on one origin with path routes |
| [fullstack](fullstack) | database, cache and mail; hooks; a fixed database port; tests with `mutti run`; copying data into a new worktree |
| [commands](commands) | project commands in `.mutti/commands`, on the host and in a service |

mutti works on git repositories, so copy an example out of this one first:

```console
$ cp -r examples/minimal ~/minimal && cd ~/minimal
$ git init -q && git add -A && git commit -qm init
$ mutti up
```
