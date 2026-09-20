# Working in this docs project

This project is a white-label documentation site built on GoFastr and fastr-docs.

## Source of truth

- `docs/router.go` is the central route object.
- Markdown lives in `content/`.
- `content/blog/` is the Markdown publication collection. Its sibling blog layout, archive, search, tags, authors, post pages, and `/blog/feed.xml` RSS feed come from the same Router.
- Typed interactive pages are GoFastr screens registered with the same router.
- `openapi.json` is consumed by the in-project OpenAPI plugin.
- `API_SERVER_URL` overrides the spec's server URL without changing the route tree; use it for deployment-specific API hosts.
- `PUBLIC_SITE_URL` is the canonical origin used by `sitemap.xml` and `robots.txt`.
- `DOCS_INCLUDE_DRAFTS=1` enables preview content. `DOCS_LOCALE` sets the
  document language; `DOCS_CONTENT_LOCALE` and `DOCS_CONTENT_VERSION` select a
  build-time content slice. Leave the content filters empty for runtime
  locale/version switching.
- `DOCS_SEARCH_BACKEND=pagefind` selects the Pagefind-compatible browser runtime; `fastr-docs export --pagefind` creates its static bundle.
- `public/` is the safe asset source mounted at `/assets/` and copied during export.

## Skills

Task-specific guidance lives in skill files, one per workflow:

| Skill | Use it for |
| --- | --- |
| `docs-authoring` | Pages, navigation, route types, front matter |
| `docs-blog` | Posts, archive and taxonomy views, RSS |
| `docs-theming` | Templates, brand identity, UI labels, tokens |
| `docs-openapi` | The API reference plugin and its request console |
| `docs-publishing` | Check, build, export, deploy, search backends |

They are authored in `.agents/skills/` and mirrored to `.claude/skills/`,
which is where Claude Code loads them. Edit the `.agents/skills/` copy, then
run `fastr-docs sync-skills .`. `fastr-docs check .` fails when the two have
drifted, so the two agents cannot quietly end up reading different rules.

## Framework UI

- `router.Layout()` uses GoFastr `SiteHeader`, `Sidebar`, `DocLayout`, and `AnchoredRail` primitives.
- The generated `main.go` mounts the mobile sidebar drawer and native command palette with `router.MountNavigation(server.Router())` and `router.MountCommandPalette(server.Router())`.
- The primary header reflects top-level route groups; `Meta/Control + K` opens the native route command palette.
- Extend these components through GoFastr configuration and CSS variables; do not add a second sidebar, drawer, or scrollspy implementation in page JavaScript.

## Agent connection

The generated host serves GoFastr's `/mcp` endpoint with read-only introspection tools, and the agent card advertises that endpoint.

## Authoring rules

1. Give every route a clear title and description.
2. Use explicit `Order` values for sibling routes.
3. Use `Badge` only for short route metadata such as `New`, `Popular`, or `Beta`.
4. Prefer front matter for content metadata; do not put metadata in prose.
5. Use `draft: true` for incomplete pages and verify the public build does not expose them.
6. Use internal `redirects` for moved pages and absolute `canonical` URLs only when intentional.
7. Keep navigation and content changes in the same change.
8. Use `MarkdownComponentsPlugin` for shared Markdown shortcodes and keep page-local components in `PageConfig.Components`.
9. Use `MarkdownCollection` for disk-backed content and `MarkdownCollectionFS`
   for `embed.FS` or another virtual source; both feed the same Router.
10. Use `MarkdownBlog` for publication posts. Put `date`, `authors`, `tags`, and an optional `excerpt` in front matter; use the generated archive/search/taxonomy routes and use `RSSXML`/`MountRSS` instead of maintaining a second feed list.
11. Run `fastr-docs doctor .`, the browser E2E suite, and the static export checks before handing off work. `fastr-docs dev` also reloads JSON/YAML spec changes through GoFastr's native rebuild loop.
12. Keep strict validation enabled. Opt out only with a documented reason.
