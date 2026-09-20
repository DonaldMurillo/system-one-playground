# TypeSafe Go client

`typesafe/` is this repository's Go client for the TypeSafe System One API. It is not a Typesense search client. The current module path is `github.com/DonaldMurillo/system-one-playground`; the import below works inside this checkout. For use in another Go module, run `go get github.com/DonaldMurillo/system-one-playground/typesafe`.

## Make a request

Save this as `client-demo/main.go` inside the repository. Set `TYPESAFE_API_KEY` in the launching environment, then run `go run ./client-demo`. This makes a live provider request.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    "github.com/DonaldMurillo/system-one-playground/typesafe"
)

func main() {
    client, err := typesafe.New()
    if err != nil { log.Fatal(err) }
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    result, err := client.SystemOne(ctx, typesafe.Request{
        State: "Checkout fails for every customer after the latest deploy.",
        Questions: typesafe.Questions{
            "blocked": typesafe.Noul("Does this describe an active service outage?"),
            "team": typesafe.Choice("Which team should investigate?", typesafe.Options("payments", "platform", "other")),
        },
    })
    if err != nil { log.Fatal(err) }
    fmt.Println(result.Answers.Noul("blocked"))
    fmt.Println(result.Answers.Choice("team").Choice)
    fmt.Println("input tokens:", result.Usage.InputTokens)
}
```

The client marshals state and questions into one `POST /v1/systemone` request. `ListModels(ctx)` calls `GET /v1/models`. State can be text, a JSON-marshalable object or slice, or nil. Supply at least one question.

## Questions and answers

| Constructor | Meaning | Answer |
| --- | --- | --- |
| `Noul(instructions)` | Yes/no judgment | `Noul` is the probability of yes |
| `NoulWith(instructions, yes, no)` | Yes/no with outcome descriptions | Same probability field |
| `Choice(instructions, options)` | Select a label | `Choice`, `Confidence`, `Probabilities` |
| `Options(labels...)` | Build undescribed choice labels | Pass to `Choice` |
| `Score(instructions, levels...)` | Ordered rubric; supply at least two levels | Expected `Score`, confidence, probabilities and legend |

`Answers.Noul`, `Answers.Choice` and `Answers.Score` panic for missing IDs or mismatched answer types. Use `Answers.Get` and inspect `Answer.Type` when handling an uncertain response shape. A score can fall between rubric levels; `Normalized()` maps it using the legend length. Probability is evidence for your policy, not an automatic permission to act.

## Configuration

Explicit options override environment defaults:

| Environment | Default | Option |
| --- | --- | --- |
| `TYPESAFE_API_KEY` | Required | `WithAPIKey` |
| `TYPESAFE_BASE_URL` | `https://api.typesafe.ai` | `WithBaseURL` |
| `TYPESAFE_DEFAULT_MODEL` | `jev-latest` | `WithModel` |

A request's nonempty `Model` overrides the client default. `WithHTTPClient`, `WithTimeout` and `WithMaxRetries` control transport behavior. The default client timeout is ten seconds and the default is two retries after the initial attempt. Construct clients before concurrent use; configuration options are not runtime setters.

The client package reads environment variables, not `.env` files. Individual command-line tools load `.env` themselves. Do not commit keys or print authorization headers.

## Errors, retries and usage

Use `errors.As` for `*typesafe.APIError` and `*typesafe.ConnectionError`. API errors include status, request ID, error type, message, raw body and retry delay. Transport and body-read errors use `ConnectionError`; decoding failures are ordinary wrapped errors.

Retries apply to transport failures, HTTP 408, 429 and 5xx responses. Backoff starts at 500 ms, caps at five seconds, and applies jitter. A positive numeric server retry delay up to one minute is respected. The context bounds the overall operation; the HTTP timeout applies to individual attempts. Retried requests can add latency and provider activity.

`Result.Usage` carries reported input/output token counts; it is not an invoice or account-wide spending ledger. `Client.Stats()` reports cumulative attempts, retries, transport errors, HTTP statuses and the largest observed retry delay. Never infer a monetary spending cap from these counters.
