# SysOneScript HTTP

SysOneScript provides bounded HTTP clients and owned HTTP server streams. The
readable forms are canonical and deterministic; they do not invoke Jev.

## Client requests

```sos
get text from "https://example.com/status" called status
get JSON from endpoint called users
post order as JSON to endpoint called created
```

Use the complete form when status is data or controls must be explicit:

```sos
send an HTTP request to endpoint called response:
  method from "POST"
  headers from {"authorization": token}
  query from {"page": "2"}
  JSON body from order
  timeout from 10 seconds
  following redirects from false
  accepting at most 2 MiB
```

Responses expose `status`, lowercase repeated `headers`, `body`, and
`final_url`. Convenience requests require a 2xx response. Redirects are bounded,
cross-origin redirects discard all caller-supplied headers (including
credentials), and HTTPS downgrades are rejected.

## HTTP services

```sos
listen for HTTP requests on loopback port 8080 called requests:
  allow headers up to 32 KiB
  allow bodies up to 1 MiB
  request deadline 30 seconds
  shutdown deadline 5 seconds

for each request from requests:
  read JSON body from request as Incoming called incoming
  respond to request with status 202 and JSON incoming
```

A listener is an owned stream. Every request carries an unforgeable response
obligation and must receive exactly one response. An unanswered iteration gets
a safe 500 and a trace entry. `stop reading` closes admission, lets in-flight
requests finish within the shutdown deadline, and then closes the listener.
The checker flags a sequential request loop whose executable path never
responds to its current request binding; merely mentioning that binding in a
response body does not count.

Connections, concurrent handlers, headers, bodies, idle time, and request time
are bounded.

Unhandled request-scoped failures do not terminate the listener. Invalid input
receives a bounded 4xx JSON response, unexpected handler failures receive a
safe 500, the failure is recorded in the runtime trace, and the listener moves
on to the next request. An explicit `on failure` handler takes precedence when
the application needs a custom response. Explicit process termination and
global integrity failures still stop the run. Structured runtime traces are
available to tooling and saved analysis; ordinary CLI and VS Code Run output
does not print raw trace telemetry.

If an application response completes as its request deadline or server
shutdown arrives, the application response and fallback race atomically; the
first to complete wins. A request deadline, client disconnect, or listener
shutdown also cancels that request's handler without stopping the listener.
This applies to both `for each request` and concurrent handling policies.
Sending a response does not itself cancel the handler: follow-up work in the
same handler may finish, subject to its request deadline and server shutdown.
The network write deadline includes a short, bounded grace period so a
deadline fallback can still reach the client as an HTTP response.

Complete responses support repeated headers and either text or JSON:

```sos
respond to request with:
  status from 200
  headers from {"cache-control": "no-store"}
  JSON body from report
```

HTTP requires `[external] network = true` in `sos.toml`. Listeners currently
target native execution and default to loopback. Binding all interfaces is
explicit. The technical `std/http` actions remain available for adapters and
advanced use.
The checker tracks the same response obligation through `std/http.listen`
under any import alias; `respond_status`, `respond_text`, `respond_json`, and
`respond` complete it when called with the request binding. A latest-request
cancellation leaves an already-sent response intact.

See [`examples/sos/http`](../examples/sos/http) for a runnable service and
[`sysonescript-http-spec.md`](sysonescript-http-spec.md) for the full contract.
