---
name: docs-openapi
description: Wire an OpenAPI spec into this fastr-docs site as a searchable API reference.
---

# OpenAPI reference

The first-party plugin turns a JSON or YAML spec into routes on the same
tree as the rest of the docs, so the reference is navigable and searchable
without a parallel system.

```go
router.Use(openapi.Plugin{
    SpecPath:  "openapi.json",
    ServerURL: os.Getenv("API_SERVER_URL"),
    Order:     6,
})
```

Supply either `SpecPath` (read from disk) or `Spec` (bytes you already hold,
including from an `embed.FS`). One of the two is required. `Path`, `Title`,
`Description`, `Order`, and `Badge` control where the reference lands in
navigation.

## Server URL

The plugin reads the spec's `servers` entry by default. `ServerURL`
overrides it per deployment, which is what you want when staging and
production render the same spec. The generated starter maps this to
`API_SERVER_URL`.

## The request console and CSP

The console calls the API from the browser, so the server's origin must be
allowed explicitly:

```go
router.AllowConnectOrigin(serverURL)
```

Without it the strict CSP blocks the request and the console fails with no
useful message. For static exports, apply the same policy with
`docs.RewriteStaticCSP` after `ExportStatic`.

## Checks

After changing the spec, confirm the reference routes appear in
navigation and in the search index, and that a request from the console
reaches the configured server. `fastr-docs check .` validates the routes the
plugin contributed alongside everything else.
