# HTTP clients and services

Status: approved design; implementation pending

This specification defines HTTP client requests, server listeners, routing,
request and response ownership, bounded bodies, graceful shutdown, and the
relationship between HTTP and SysOneScript streams. It uses the shared flow
control defined by the readable stream-handling specification rather than
inventing HTTP-specific throttling or concurrency.

Canonical source reads like ordinary English. Precise module actions remain
available through `std/http` for tooling, adapters, and advanced use. Both forms
resolve to one checked runtime implementation and execute deterministically.

## Goals

- Make API clients, webhook receivers, small services, and automation endpoints
  practical in SysOneScript.
- Keep every body, connection, redirect chain, listener, and handler bounded.
- Give every server request exactly one visible response obligation.
- Support sequential and bounded-concurrent request handling.
- Integrate cancellation, throttling, batching, latest-work replacement, typed
  failures, tracing, and graceful shutdown.
- Protect credentials and use conservative network defaults.
- Preserve interpreter, native-build, CLI, Studio, VS Code, and debugger parity.

## Non-goals

HTTP does not add an implicit application framework, hidden global router,
unbounded body buffering, automatic unsafe retries, detached request handlers,
transparent authentication, or silent request dropping. WebSockets and
streaming response writers build on this ownership model but remain explicit
resources rather than ordinary records.

## Client requests

Common bounded requests use canonical convenience forms:

```sos
get text from "https://example.com/status" called status
get JSON from "https://api.example.com/users" called users
post order as JSON to "https://api.example.com/orders" called created
```

These forms require a successful status, enforce response-size and time limits,
and decode the declared representation. An unexpected status fails with
`UnexpectedHttpStatus`; invalid JSON fails with `InvalidHttpResponse`.

The complete form preserves every valid HTTP status as data:

```sos
send an HTTP request to endpoint called response:
  method from "POST"
  headers from headers
  JSON body from order
  timeout from 10 seconds
  following redirects from false
  accepting at most 2 MiB
```

Fallback module actions include `http.get_text`, `http.get_json`,
`http.post_json`, and `http.request`. Convenience actions delegate to the
complete request implementation.

## Client response

```sos
define HttpResponse:
  status as integer
  headers as record
  body as text
  final_url as text
```

Header names are normalized to lowercase. Repeated values remain lists rather
than being joined ambiguously. The complete bounded form reads and closes the
body before returning.

Statuses from 100 through 599 are protocol results, not transport failures.
Only convenience forms apply a default success-status policy. Callers may set an
explicit expected range:

```sos
get JSON from endpoint expecting status 200 through 299 called result
```

## Streaming client responses

Large or indefinite response bodies remain owned resources:

```sos
open an HTTP GET request to download_url called response
stream body of response called chunks

for each chunk from chunks:
  append chunk to file "download.tmp"
```

`open` waits for response headers. The response owns its unread body until the
body is consumed or explicitly closed. Opening another request to the same URL
creates an independent response and body stream. Canceling or abandoning the
body closes the network response and prevents connection reuse when necessary.

Body chunks are bounded byte values; text and JSON decoding remain separate
operations so split UTF-8 sequences and oversized documents cannot be
misinterpreted.

## Request construction

The complete request supports:

- method;
- URL;
- query parameters;
- repeated headers;
- no body, bounded text, bounded JSON, form data, or an owned body stream;
- overall deadline and response-header deadline;
- response-size limit;
- redirect policy;
- accepted content types; and
- optional named secret references for credentials.

Methods and header values reject control characters. A body is forbidden where
the selected canonical form does not permit one. Supplying both a complete body
and a body stream is invalid.

## Redirects

Redirect following is explicit in the complete form and bounded by count.
Authorization, cookie, proxy-authorization, and other sensitive headers are
removed on cross-origin redirects unless a policy explicitly permits forwarding
to an allowed origin. HTTPS-to-HTTP downgrade redirects are rejected by default.

Redirect loops and invalid locations fail with `HttpRedirectRejected`. The final
response records `final_url`; traces record safe redirect origins without query
secrets.

## Retries

There is no automatic retry. A caller must request a bounded policy:

```sos
get JSON from endpoint called result
  retry temporary failures at most 3 times
  waiting exponentially from 200 milliseconds to 2 seconds
```

Retries are allowed automatically only for idempotent methods and replayable
bodies. Retrying a non-idempotent request requires an explicit idempotency key or
an acknowledgment that completed effects may be duplicated. `Retry-After` is
honored only within the caller's total deadline and maximum delay.

## Server listeners

A server is an owned incoming request stream:

```sos
listen for HTTP requests on loopback port 8080 called requests:
  allow headers up to 32 KiB
  allow bodies up to 1 MiB
  request deadline 30 seconds
  shutdown deadline 10 seconds
```

Binding to all interfaces is deliberately explicit:

```sos
listen for HTTP requests on all interfaces port 8080 called requests
```

Each invocation creates an independent listener and request stream. Stopping the
stream closes admission, begins graceful shutdown, waits for bounded in-flight
handlers, and cancels what remains at the shutdown deadline.

## Server request

```sos
define HttpRequest:
  id as text
  method as text
  scheme as text
  host as text
  path as text
  query as record
  headers as record
  remote_address as text
  received_at as timestamp
```

An emitted request also owns one internal response obligation. That capability
is not serializable, copyable, or forgeable. Every reachable handler path must
respond, reject, transfer ownership, or upgrade the connection exactly once.

## Handling requests

Sequential handling:

```sos
handle each request from requests one at a time:
  call application.route with request
```

Bounded concurrency:

```sos
handle each request from requests
  with at most 32 at once:

  call application.route with request
```

The semantics come from the readable stream-handling specification. Admission
stops when all handler slots and the bounded waiting queue are full. A listener
may apply an explicit overload response such as status 503; it never creates an
unbounded queue.

Throttling and latest-work policies must complete displaced obligations:

```sos
limit requests to 100 each second
  rejecting excess requests with status 429
  called admitted_requests
```

```sos
handle only the newest request for each account_id from requests
  canceling an older request with status 409:
```

Ignoring or dropping an HTTP request without a response policy is invalid.

## Routing

Routing is deterministic source syntax:

```sos
when request matches GET "/users/{id}" using id:
  call users.find with id called user
  respond to request with status 200 and JSON user

when request matches POST "/users":
  read JSON body from request as User called user
  call users.create with user called created
  respond to request with status 201 and JSON created

otherwise:
  respond to request with status 404 and JSON {"error": "route not found"}
```

Routes are checked in source order. Static segments take no hidden priority over
earlier parameter segments. Duplicate, shadowed, malformed, and unreachable
routes are diagnostics. Path parameters are percent-decoded exactly once and
validated as UTF-8; encoded separators are rejected unless explicitly allowed.

Routing does not use Jev. Alternate author wording may be canonicalized, but the
saved route matcher is deterministic.

## Reading request bodies

Common bounded forms:

```sos
read text body from request called text
read JSON body from request as User called user
read form body from request called fields
```

A body may be consumed once. Reading it again is a checker error and runtime
failure. JSON decoding validates the requested named type before returning.
Unsupported content type fails rather than guessing.

Large uploads use an owned stream:

```sos
stream body of request called chunks
```

Responding before consuming or closing the body closes it automatically and may
prevent connection reuse. Body limits count decoded transfer bytes and enforce a
separate compressed-byte limit when content decoding is enabled.

## Sending responses

Canonical forms:

```sos
respond to request with status 204
respond to request with status 200 and text "healthy"
respond to request with status 200 and JSON report
```

Complete form:

```sos
respond to request with:
  status from 200
  headers from {"cache-control": "no-store"}
  JSON body from report
```

Responding consumes the request's response obligation. A second response fails
with `HttpResponseAlreadySent`. Invalid status, header, body, or content-length
combinations fail before headers are committed where possible.

If a handler exits while still owning an unanswered request, the checker reports
the incomplete path. Runtime protection sends a safe 500 response when possible,
records `UnansweredHttpRequest`, and releases the request. It never leaves the
client hanging indefinitely.

## Streaming responses

Large downloads and server-sent events use an owned writer:

```sos
start a streaming response to request called output:
  status from 200
  content_type from "text/event-stream"

send event to output with data
finish response output
```

The writer is single-owner and must be finished or closed. Once response headers
are committed, failures cannot be replaced with an ordinary error response;
they terminate the body and are reported in traces. Writes apply network
backpressure and inherit client disconnect and server shutdown cancellation.

The general language may later define writable streams, but HTTP response
writers remain explicit until their ownership and failure semantics are shared
by another proven use case.

## Cancellation and deadlines

Distinct limits are available for:

- connection and response-header wait;
- complete client request;
- request-header reading;
- request-body reading;
- server handler execution;
- response writing and idle time; and
- graceful listener shutdown.

A server handler context is canceled when the client disconnects, its deadline
expires, the listener shuts down, or the run is canceled. Child HTTP requests,
processes, timers, streams, and Jev calls inherit that context. Detached handler
work is not permitted by this specification.

## Typed failures

Reserved client failures:

| Failure | Important fields |
|---|---|
| `InvalidHttpRequest` | `reason as text` |
| `HttpConnectionFailed` | `origin as text`, `reason as text` |
| `HttpTimeout` | `phase as text`, `duration as duration` |
| `HttpTlsFailure` | `origin as text`, `reason as text` |
| `HttpBodyTooLarge` | `limit as integer`, `observed as optional integer` |
| `InvalidHttpResponse` | `reason as text` |
| `UnexpectedHttpStatus` | `status as integer`, `body_preview as optional text` |
| `HttpRedirectRejected` | `reason as text` |

Reserved server failures:

| Failure | Important fields |
|---|---|
| `HttpAddressInUse` | `address as text` |
| `HttpListenerFailed` | `address as text`, `reason as text` |
| `InvalidHttpBody` | `reason as text` |
| `HttpRequestTooLarge` | `limit as integer`, `observed as optional integer` |
| `HttpResponseAlreadySent` | `request_id as text` |
| `HttpClientDisconnected` | `request_id as text` |
| `HttpServerShuttingDown` | `address as text` |
| `UnansweredHttpRequest` | `request_id as text`, `path as text` |

Raw transport errors and sensitive response bodies are not exposed unredacted.
Global cancellation and budget exhaustion remain fatal run outcomes.

## Security defaults

- TLS certificate and hostname verification are enabled.
- Listener binding defaults to loopback.
- Header count, header bytes, body bytes, decompressed bytes, redirects,
  concurrent connections, handlers, and idle connections are bounded.
- Sensitive headers and URL credentials are redacted from logs and traces.
- Cross-origin redirects strip credentials by default.
- Environment proxy use is explicit and visible in configuration.
- Client and server operations require network capabilities and optional host or
  bind allowlists.
- Request smuggling ambiguities, invalid transfer encodings, conflicting content
  lengths, control characters, and malformed hosts are rejected.
- Server identity headers do not expose runtime versions by default.
- Secrets are referenced through the project secret system rather than embedded
  in source or returned from diagnostics.

## WebSockets

Upgrade is explicit because a WebSocket is a bidirectional owned connection:

```sos
upgrade request to a WebSocket called connection
stream messages from connection called messages

for each message from messages:
  send message to connection
```

The connection owns one incoming stream and one outgoing writer. It has bounded
message sizes, bounded outgoing queue, close handshake, idle timeout, ping/pong
policy, and targeted cancellation. Upgrade consumes the HTTP response obligation.

WebSocket implementation follows ordinary HTTP delivery but may be staged after
bounded request/response service behavior is complete. Its public contract is
not emulated with ordinary body streams.

## Server-sent events

Server-sent events are a canonical specialization of streaming responses:

```sos
start an event stream to request called events
send event to events with data
finish event stream events
```

Encoding, heartbeat comments, retry hints, event IDs, client disconnect, and
write deadlines are handled by the HTTP runtime rather than hand-built string
concatenation.

## Targets and capabilities

Client requests target native, capable WASI hosts, and browser hosts with an
explicit network adapter. Server listeners target native hosts initially.
Browser execution never receives arbitrary raw socket or secret access.

Capabilities distinguish outbound origins from inbound bind addresses. A
module's network permission does not automatically allow listening, and
listening permission does not allow arbitrary outbound requests.

## Tooling and observability

CLI, Studio, VS Code, traces, and the debugger expose:

- safe method, origin, route, and status;
- request/response byte counts without secret body contents;
- redirects, retries, deadlines, and current phase;
- listener address, admission state, active and waiting handlers;
- throttle rejections and overload responses;
- request ownership and response-completion state;
- streaming body/writer counters;
- graceful shutdown progress; and
- typed terminal failure.

Inspecting an HTTP body or request stream never consumes it. Stop Stream closes
only the selected response body, listener, WebSocket input, or event stream and
propagates the appropriate owned-resource cleanup.

## Compatibility and English denomination

Technical operations remain searchable:

| Technical operation | Canonical wording |
|---|---|
| `http.get_text` | `get text from ...` |
| `http.get_json` | `get JSON from ...` |
| `http.post_json` | `post ... as JSON to ...` |
| `http.request` | `send an HTTP request to ...` |
| `http.listen` | `listen for HTTP requests on ...` |
| `http.read_json` | `read JSON body from request ...` |
| `http.respond_json` | `respond to request with ... and JSON ...` |

Editors may accept unambiguous technical wording and offer a canonical rewrite.
Generated programs and examples use the English form.

## Required verification

Implementation is complete only when tests cover:

- every method, query encoding, repeated headers, text, JSON, forms, empty
  bodies, and streaming bodies;
- exact response, decompression, header, redirect, connection, and concurrency
  limits;
- TLS validation, cross-origin redirects, secret redaction, and malformed HTTP;
- status handling differences between convenience and complete forms;
- cancellation during DNS, connect, TLS, headers, body, handler, and response;
- listener startup failure and partial cleanup;
- exactly-once response obligations across all control-flow paths;
- sequential, concurrent, throttled, rejected, and latest-only handling;
- client disconnect and graceful shutdown with active handlers;
- streaming download, upload, response, SSE, and WebSocket backpressure;
- interpreter and native-build parity;
- deterministic local fixtures with no public-network dependency;
- CLI, Studio, debugger, and VS Code observability; and
- Linux, macOS, and Windows race and conformance tests.

## Delivery sequence

1. Define HTTP records, owned resources, typed failures, and capability policy.
2. Implement bounded complete client requests and convenience forms.
3. Implement redirects, explicit retries, secrets, and streaming client bodies.
4. Implement loopback listeners, request streams, body readers, routing, and
   exactly-once responses.
5. Integrate bounded concurrent handling, throttling, overload rejection, and
   latest-work policies from the stream-handling specification.
6. Implement graceful shutdown and service observability.
7. Implement streaming responses and server-sent events.
8. Implement WebSocket connection ownership and backpressure.
9. Add canonical grammar, technical aliases, canonicalization, and editor help.
10. Add complete API-client, webhook-service, and live-service examples.
11. Run native-build, cross-platform, race, security, and independent OMP
    reviews before declaring the specification implemented.
