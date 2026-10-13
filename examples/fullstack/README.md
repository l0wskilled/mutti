# fullstack

A typical multi-service app in miniature, without app code: nginx (web), an API (busybox httpd),
a worker, Postgres, Redis, Mailpit and an optional `tools` service.

- **Routes without configuration:** web gets `https://fullstack.localhost`, the API
  `https://api.fullstack.localhost`, Mailpit `https://mail.fullstack.localhost`.
- **A fixed database port** per worktree for database tools (`x-mutti.ports`): see `mutti status`.
- **Hooks:** the bootstrap hook creates a table once per instance, the post-up hook counts every
  `mutti up` in it.
- **Tests against a throwaway copy:** the `e2e` overlay adds a test runner.

```console
$ mutti up
$ mutti exec db -u service -- psql -U app -c "select count(*) from visits"
$ mutti run --overlay e2e --service e2e-runner -- sh -c 'wget -qO- http://web/ && wget -qO- http://api:8000/ready.json'
$ mutti worktree add review --data-from main   # a second copy with the same data
```

Every published port is hard-coded on purpose: mutti moves them to random loopback ports, so
any number of copies run side by side.
