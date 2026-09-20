# System One Playground documentation site

Built with [fastr-docs](https://github.com/DonaldMurillo/fastr-docs), pinned in `go.mod`. This module needs Go 1.27; the language itself requires Go 1.25. No sibling checkout or local `replace` is required.

From this directory:

```sh
python3 scripts/sync_content.py
go test ./...
go vet ./...
go run .
```

Open `http://localhost:3070`. Set `PORT=127.0.0.1:3070` to bind the preview to loopback. Search uses the bundled JSON index and requires no external search service.

## Content ownership

Edit reference material in the parent `docs/` files or the repository example README. `content-manifest.json` is the explicit allowlist; `scripts/sync_content.py` copies those pages and translates internal links to site routes. Hand-authored home, getting-started and limits pages live in `content/`. After editing references, regenerate and run `python3 scripts/sync_content.py --check`.

Do not crawl the entire parent docs folder: design proposals and agent notes are not public reference material. The root site router controls navigation, search and exported routes. Generated starter compatibility files for blog/OpenAPI are retained but do not mount a blog or API console.

## Static export

```sh
PUBLIC_SITE_URL=https://your-docs-domain.example go run . --export dist
python3 scripts/verify_export.py dist
```

Replace the example origin with the real hosting URL before deployment. For hosting below a path, add `--export-base /your-repository` and include that path in `PUBLIC_SITE_URL`. Verify the resulting export under that prefix. `dist/` is generated and ignored.

The export includes local search, offline/PWA assets, `llms.txt` and an agent card. The docs host's MCP is a documentation surface; it does not replace `sysone mcp`, which operates on language projects. Static hosting cannot serve the live MCP endpoint.

No deployment has been configured or run. The scaffold's nested workflow is manual-only and assumes this site is its own repository; adapt its working directory and artifact path before moving it to the repository root.

The pinned upstream `fastr-docs check` requires its starter blog and OpenAPI plugin, even for a documentation-only site. We run the same `Router.Validate()` through `go test` and verify the real static export instead. This is a known upstream scaffold-check limitation, not a skipped route check.

The site covers the entire repository. Product landings are `/client`, `/semlint`, `/docs` (SysOneScript), `/studio`, and `/tools`. Existing language URLs remain stable. The original four-tab book icon is maintained in `public/favicon.svg` and `docs/icon.go` for header/favicon and generated PWA sizes.
