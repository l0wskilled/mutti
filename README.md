# mutti

Parallel, isolated Docker Compose environments for every git worktree.

mutti runs one copy of your Compose project per worktree, gives each its own HTTPS hostnames,
keeps their data apart, and cleans up after them. Your Compose files stay as they are.

```console
$ mutti worktree add feature-x
Created /home/me/shop-feature-x (branch feature-x).
shop-feature-x is up: https://feature-x.shop.localhost

$ mutti list
NAME            STATUS       BRANCH     URL                               PATH                     PORTS
shop            running 6/6  main       https://shop.localhost            /home/me/shop            db:5432=127.0.0.1:41873
shop-feature-x  running 6/6  feature-x  https://feature-x.shop.localhost  /home/me/shop-feature-x  db:5432=127.0.0.1:52309
```

## Requirements

- Docker Engine with the Compose plugin (2.24.4 or newer)
- git
- Linux. macOS and Windows follow.

## Install

Download the archive for your platform from the
[releases](https://github.com/l0wskilled/mutti/releases), unpack it and put `mutti` on your
`PATH`. It is a single static binary. Every archive carries a build provenance attestation:

```sh
gh attestation verify mutti_*.tar.gz --repo l0wskilled/mutti
```

Or with Go: `go install github.com/l0wskilled/mutti/cmd/mutti@latest`

Once per machine, let your system and browsers trust mutti's local certificate authority:

```console
$ mutti trust
```

## Versioning

mutti follows [Semantic Versioning](https://semver.org). Before 1.0, a minor release (0.2 to
0.3) may break things and a patch release only fixes them. From 1.0 on, only a major release
changes the command line, the `--json` output or the `x-mutti` configuration in incompatible
ways, and instances made by an older mutti keep working with a newer one.

## Quick start

In any directory with a `compose.yaml`:

```console
$ mutti up        # start this worktree's instance, wait until healthy
$ mutti status    # services, URLs, ports
$ mutti open      # open it in the browser
$ mutti shell api # a shell in the api service, as your user
$ mutti down      # stop it, keep the data
```

No configuration needed: the service on a typical web port gets `https://<project>.localhost`,
every other HTTP service `https://<service>.<project>.localhost`; a linked worktree gets
`https://<worktree>.<project>.localhost`. When a worktree is named like a service, the service
keeps its hostname and the worktree has to be renamed. Published ports are moved to
random ports on 127.0.0.1, so copies of the same project never fight over a port.

`mutti init` writes these defaults into the Compose file as an `x-mutti` block to edit from, and
a section for AI coding agents into `AGENTS.md`.

[examples/](examples) has small projects to try: no configuration at all, a single-page app with
its API on one origin, a full stack with database, hooks and tests, and project commands.

## Everyday commands

| | |
|---|---|
| `mutti up` / `stop` / `down` / `destroy` | start, stop, remove containers (keep data), remove everything |
| `mutti up` / `stop` / `restart <service...>` | the same for single services |
| `mutti list` | every instance on this machine; `--watch` keeps it current |
| `mutti events` | what happens to the instances' containers, as it happens |
| `mutti status` | this instance, service by service, with a diagnosis of what is not well |
| `mutti diagnose [service...]` | why services are unhealthy, crashing or gone: health check output, exit codes, last logs |
| `mutti exec <service> -- <cmd>` | run a command in a service, as your host user |
| `mutti shell [service]` | interactive shell |
| `mutti logs [service]` | logs, with the flags of `docker compose logs` |
| `mutti url` / `mutti open [service]` | the URL, or open it |
| `mutti compose <args>` | any `docker compose` command against this instance |
| `mutti worktree add <name>` | new worktree next to the main one, started (`--data-from main` copies the data) |
| `mutti worktree remove <name>` | remove instance and worktree; refuses while you have uncommitted changes |
| `mutti reset` | delete the data and start fresh |
| `mutti run -- <cmd>` | run a command against a throwaway instance, removed afterwards |
| `mutti tidy` | remove instances of deleted worktrees, `--idle 24h` also stops unused ones; shows them first, acts with `--yes` |
| `mutti doctor` | check Docker, Compose, router, HTTPS and this project |
| `mutti tui` | all of the above in a terminal UI |

Every command takes `--json` for scripts and tools; the format is versioned. The full reference
is in [docs/reference/cli](docs/reference/cli/mutti.md).

## Configuration

Optional, in an `x-mutti` block of your Compose file:

```yaml
x-mutti:
  main: web
  routes:
    api: { path: /api }          # also https://<instance>.localhost/api
    mail: { host: mailpit }      # https://mailpit.<instance>.localhost
  ports:
    db: [5432]                   # a fixed host port per instance, for database tools
  hooks:
    bootstrap:                   # once per instance
      - service: api
        run: composer install && bin/console doctrine:migrations:migrate -n
  overlays:
    e2e: [compose.e2e.yml]       # mutti up --overlay e2e
  worktree:
    copy: [.env.local]           # copied into new worktrees
```

All keys: [docs/reference/x-mutti.md](docs/reference/x-mutti.md). For completion and checks in
your editor, map your Compose files to
[docs/reference/compose-with-mutti.schema.json](docs/reference/compose-with-mutti.schema.json).

`MUTTI_INSTANCE`, `MUTTI_HOSTNAME`, `MUTTI_HOST_PATH`, `MUTTI_UID`, `MUTTI_GID`,
`MUTTI_DOCKER_SOCKET`, `MUTTI_DOCKER_GID` and `MUTTI_GIT_COMMON_DIR` are available for
interpolation, hooks and `exec`. Two of them are for tool containers:

```yaml
volumes:
  - ${MUTTI_DOCKER_SOCKET}:/var/run/docker.sock             # the daemon mutti uses, rootless or not
  - ${MUTTI_GIT_COMMON_DIR}:${MUTTI_GIT_COMMON_DIR}         # git in a linked worktree
group_add: ["${MUTTI_DOCKER_GID:-0}"]
```

### Project commands

Scripts in `.mutti/commands/host/` run on your machine, scripts in
`.mutti/commands/<service>/` inside that service; both become `mutti <name>`. The first `# `
comment is the help text.

## Tests and CI

```console
$ mutti run --overlay e2e -- npx playwright test
$ mutti run --overlay e2e --service test-runner -- npm test
```

`mutti run` starts a fresh instance with its own data and URL, runs the command, saves the logs
of all services to `.mutti/local/runs/<id>/` and removes everything, also when mutti is killed.
It exits with the command's exit code, so it fits any CI with Docker. Runs in parallel never
meet.

mutti also works inside a container (CI steps, tool containers): bind mounts are translated to
the paths the Docker host sees, and `mutti attach` puts the container on the instance's network.
Where mutti cannot tell the host path of the worktree, set `MUTTI_HOST_ROOT`.

The router is one per Docker daemon and serves the certificates of one mutti's local CA. A mutti
in a container that should use the same router as mutti on the host needs the host's state:
mount `~/.local/state/mutti` and `~/.local/share/mutti` to the same places in the container.
Otherwise it refuses to touch the host's router; `MUTTI_ROUTER_NAME`, `MUTTI_HTTP_PORT` and
`MUTTI_HTTPS_PORT` give it a router of its own.

## Terminal UI

`mutti tui` shows every instance on this machine, the services of the selected one and what is
wrong with them, kept current as containers change. Every service shows its CPU and memory with a
sparkline; the selected one also its image, uptime, restarts and last health check. The header
tells whether Docker answers and the router runs.

Keys act on the selected instance or service, or on the services marked with space: start, stop,
restart, logs, diagnose, shell, exec, open, copy URL, new worktree, down, reset and destroy, which
removes an instance whatever state it is in, also when its worktree is gone. `T` tidies what
deleted worktrees left behind, `/` filters the instances by name or branch. `:` searches every
mutti command and subcommand, the project's own commands included, and runs it; `?` lists the
keys. The mouse selects with a click and scrolls with the wheel; most terminals select text with
shift held.

## Web UI

```console
$ mutti serve --open
mutti serves http://127.0.0.1:7477 (Ctrl-C to stop)
```

Every instance on this machine, kept current as containers change: status, branch, URLs and
ports, starting, stopping and removing single services or all of them, a diagnosis of what is not
well, events, and logs that stream in live and stay whole and scrollable however long they get. It runs in the foreground like any
other command, answers only on 127.0.0.1, and serves the same data as JSON under `/api` and the
tools of `mutti mcp` under `/mcp`.

## AI coding agents

`mutti mcp` serves mutti as a Model Context Protocol server: agents get tools to start and
inspect environments, run commands in services, run tests against throwaway copies and open
worktrees with environments of their own, so several agents can work in parallel.

```console
$ claude mcp add mutti -- mutti mcp
```

`mutti init` also writes a section on the environment into `AGENTS.md`, which most agents read.

## How it works

- **Identity.** Each worktree gets a name once and keeps it, also when you rename the
  directory, switch branches or `git worktree move` it. It is stored in the worktree's git
  directory, not in your files.
- **State lives in Docker.** Instances are found through labels; there is no database to get
  out of sync.
- **One router per machine.** A Traefik container on 127.0.0.1 routes hostnames to instances.
  Each instance has its own network; the router joins them all, the instances never meet.
  Inside an instance's containers its hostnames resolve to the router as well, so a test browser
  in a container uses the same URLs. Clients that map `*.localhost` to loopback by themselves
  (Chromium, curl) need the address handed over, e.g. `--host-resolver-rules` or `--resolve`.
- **Throwaway runs** are guarded by a reaper container that removes them when mutti is gone.

## Development

```console
$ make hooks       # run the checks on every commit and push
$ make check lint  # format, generated docs, vet, lint, unit tests
$ make cover       # unit + integration tests against Docker, 100% coverage required
$ make test-e2e    # end-to-end tests with the real binary
$ make docs        # regenerate docs/reference
```

A release is a tag `vX.Y.Z` on `main`: the release workflow runs all checks, builds the archives
and publishes them with their changelog.
