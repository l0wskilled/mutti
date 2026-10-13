## mutti run

Run a command against a fresh throwaway instance, removed afterwards whatever happens

### Synopsis

Start a fresh instance of this worktree's project (own containers, volumes and URL),
wait until it is healthy, run the command, then save the services' logs and remove the
instance with all its data. A reaper container removes it even if mutti is killed.

The command runs on the host with MUTTI_RUN_ID, MUTTI_URL and the other MUTTI_* variables set,
or with --service in a one-off container of that service. mutti exits with its exit code.
mutti commands inside it, such as mutti exec or mutti compose, work on the run's instance.

  mutti run --overlay e2e -- npx playwright test
  mutti run --overlay e2e --service test-runner -- npm test

```
mutti run [flags] -- <command> [args...]
```

### Options

```
      --build             rebuild images before starting
  -h, --help              help for run
      --logs string       where to save the services' logs (default .mutti/local/runs/<run id>)
      --overlay strings   enable an x-mutti overlay (repeatable)
      --profile strings   enable a Compose profile (repeatable)
      --service string    run the command in a one-off container of this service
      --start strings     start only this service and what it depends on (repeatable)
  -u, --user string       with --service: run as "host", "service" or user[:group]
```

### Options inherited from parent commands

```
  -C, --dir string   run as if mutti was started in this directory
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti](mutti.md)	 - Parallel, isolated Docker Compose environments per worktree

