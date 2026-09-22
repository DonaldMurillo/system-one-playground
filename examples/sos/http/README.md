# HTTP service

This runnable example starts a bounded loopback HTTP service, validates one
JSON request as `Incoming`, returns a typed JSON response, and shuts down
gracefully after that request.

From this folder, run:

```sh
sos run main.sos
```

In another terminal:

```sh
curl -i -X POST http://127.0.0.1:8080 \
  -H 'content-type: application/json' \
  --data '{"message":"hello from curl"}'
```

The response is HTTP `202`, includes `cache-control: no-store`, and contains
`{"received":"hello from curl"}`. The program then prints its shutdown line.
