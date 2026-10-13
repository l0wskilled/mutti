# spa-api

A single-page app and its API on one origin, so the browser needs no CORS: `x-mutti.routes` sends
`/api` on the app's hostname to the api service, which also keeps a subdomain of its own.

```console
$ mutti up
spa-api is up: https://spa-api.localhost
$ curl https://spa-api.localhost/api/hello.json
{"message": "hello from the api"}
$ curl https://api.spa-api.localhost/api/hello.json
```

Neither service publishes a port; routes name the container port to use.
