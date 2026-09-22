# SysOneScript HTTP

SysOneScript provides bounded HTTP clients and owned HTTP server streams. The
readable forms are canonical and deterministic; they do not invoke Jev.

```sos
get JSON from endpoint called users

listen for HTTP requests on loopback port 8080 called requests:
  allow headers up to 32 KiB
  allow bodies up to 1 MiB
  request deadline 30 seconds
  shutdown deadline 5 seconds

for each request from requests:
  read JSON body from request as Incoming called incoming
  respond to request with status 202 and JSON incoming
```

Listeners are owned streams with bounded admission and graceful shutdown. Each
request has an unforgeable exactly-once response obligation. Unanswered handlers
receive a safe 500 and produce a trace entry. Unhandled request failures are
isolated: invalid input receives a safe 4xx response, unexpected handler errors
receive a safe 500, and the listener continues with the next request. Explicit
failure handlers override that default response policy.
The checker flags a sequential request loop whose executable path never
responds to its current request binding.
Technical `std/http.listen` and response calls obey the same obligation rules
under any import alias. Canceling a superseded handler does not replace a
response that was already sent.

HTTP requires `[external] network = true`. Listeners currently target native
execution, default to loopback, and require explicit wording to bind all
interfaces. See `examples/sos/http` for the runnable local JSON service.

Outgoing cross-origin redirects discard all caller-supplied headers, including
credentials. HTTPS downgrades are rejected.
