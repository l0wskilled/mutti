## mutti

Parallel, isolated Docker Compose environments per worktree

### Synopsis

mutti runs one isolated copy of your Compose project per git worktree and cleans up after them.

### Options

```
  -C, --dir string   run as if mutti was started in this directory
  -h, --help         help for mutti
      --json         print machine-readable JSON
```

### SEE ALSO

* [mutti attach](mutti_attach.md)	 - Connect the container mutti runs in to this instance's network (e.g. a CI step)
* [mutti compose](mutti_compose.md)	 - Run any docker compose command against this instance
* [mutti destroy](mutti_destroy.md)	 - Remove the instance with all its data
* [mutti diagnose](mutti_diagnose.md)	 - Explain what is wrong with the instance's services: health checks, exit codes, last logs
* [mutti doctor](mutti_doctor.md)	 - Check Docker, Compose and this project
* [mutti down](mutti_down.md)	 - Remove the instance's containers and networks, keep its data
* [mutti env](mutti_env.md)	 - Print the MUTTI_* variables, for `mutti env | source` (fish) or `eval "$(mutti env)"`
* [mutti events](mutti_events.md)	 - Print what happens to the containers of all instances, as it happens
* [mutti exec](mutti_exec.md)	 - Run a command in a service of this instance, as your host user
* [mutti init](mutti_init.md)	 - Write mutti's settings into the Compose file and a section for AI agents into AGENTS.md
* [mutti list](mutti_list.md)	 - List all instances on this machine
* [mutti logs](mutti_logs.md)	 - Show the instance's logs (flags as for docker compose logs)
* [mutti mcp](mutti_mcp.md)	 - Serve mutti to AI coding agents as a Model Context Protocol server over stdio
* [mutti open](mutti_open.md)	 - Open the instance or a service in the browser
* [mutti prompt](mutti_prompt.md)	 - Print a short state line for your shell prompt (empty outside an instance)
* [mutti rename](mutti_rename.md)	 - Give this worktree's instance a new name
* [mutti reset](mutti_reset.md)	 - Delete the instance's data and start it fresh (bootstrap runs again)
* [mutti restart](mutti_restart.md)	 - Restart the instance's containers, or some services'
* [mutti router](mutti_router.md)	 - Manage the machine-wide router
* [mutti run](mutti_run.md)	 - Run a command against a fresh throwaway instance, removed afterwards whatever happens
* [mutti run-id](mutti_run-id.md)	 - Print a new run id (sortable, carries its time), for naming per-run resources
* [mutti serve](mutti_serve.md)	 - Serve the web UI and HTTP API on 127.0.0.1 until interrupted
* [mutti shell](mutti_shell.md)	 - Open a shell in a service (default: the main one), as your host user
* [mutti status](mutti_status.md)	 - Show this worktree's instance service by service
* [mutti stop](mutti_stop.md)	 - Stop the instance's containers, or some services', and keep everything
* [mutti sweep](mutti_sweep.md)	 - Remove leftovers of old runs, and run this project's sweep hooks
* [mutti tidy](mutti_tidy.md)	 - Remove instances whose worktree is gone, stop idle ones (shows them only, unless --yes)
* [mutti trust](mutti_trust.md)	 - Make the system and browsers trust mutti's local CA (asks for sudo)
* [mutti tui](mutti_tui.md)	 - Every instance, its services and every mutti command in a terminal UI
* [mutti up](mutti_up.md)	 - Start this worktree's instance and wait until it is healthy
* [mutti url](mutti_url.md)	 - Print the URL of the instance or of a service
* [mutti worktree](mutti_worktree.md)	 - Create and remove git worktrees together with their instances

