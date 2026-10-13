# minimal

One web service, no `x-mutti` block: what mutti does without any configuration.

```console
$ mutti up
minimal is up: https://minimal.localhost
$ mutti worktree add feature
Created /path/to/minimal-feature (branch feature).
minimal-feature is up: https://feature.minimal.localhost
$ mutti list
```

Both copies run at once although the Compose file publishes port 8080: mutti moves published
ports to random ports on 127.0.0.1 and routes each copy by its own hostname.
