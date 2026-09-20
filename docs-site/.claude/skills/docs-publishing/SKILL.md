---
name: docs-publishing
description: Validate, build, export, and deploy this fastr-docs site, including offline and search assets.
---

# Publishing

## The commands

```sh
fastr-docs check .              # route and content validation
fastr-docs doctor .             # broader project diagnosis
fastr-docs build .              # GoFastr build gates and compiler
fastr-docs dev .                # watch, rebuild, refresh open tabs
fastr-docs export . --out dist  # static PWA export
fastr-docs upgrade .            # review GoFastr migrations
fastr-docs sync-skills .        # copy .agents/skills over .claude/skills
```

`check` fails if `.claude/skills` has drifted from `.agents/skills`. They are
one authored set copied to two places, so a difference means an edit landed in
only one and one of your agents is reading stale guidance. Edit the
`.agents/skills` copy, then run `sync-skills`. Add `--prune` to delete files
that exist only under `.claude/skills`; that is opt-in, because a file there
may be a deliberate Claude-only skill rather than a leftover.

`go run .` runs the server once with no watcher. Use it when the dev loop's
rebuilds get in the way.

## Search backends

JSON is the zero-dependency default and needs nothing at build time. Pagefind
is the export-grade backend:

```sh
fastr-docs export . --out dist --pagefind
```

Two path options matter when assets are not at the default prefix.
`WithPagefindPath` moves the Pagefind runtime; `WithSearchIndexPath` moves the
JSON index, which defaults to `/__fastr-docs/search.json`. Getting the index
path wrong does not raise an error. The command palette just stops filtering
and lists every route, so verify search after changing an asset prefix.

## Offline and PWA

Generated sites are installable through GoFastr's `WithPWA`. Routes marked
`Offline: true` are precached. GoFastr fingerprints the exported worker cache
and rotates it on the next visit after a redeploy; open tabs finish on the
version they started with.

## Verifying an export

Do not trust an exit code alone. Inspect `dist` for `sitemap.xml`,
`robots.txt`, `manifest.webmanifest`, `service-worker.js`, `404.html`,
`llms.txt`, `.well-known/agent-card.json`, and a representative page under a
nested route. Confirm the search index and any Pagefind bundle are present.

If the export is served under a path prefix, pass `--base` and check that the
emitted asset URLs carry it and that every page has `data-fastr-docs-base` set
to it; the runtime reads that to navigate and match routes below the prefix.
A GitHub project page is this case, and `.github/workflows/pages.yml` derives
the base from the repository name.

## Content security policy

The live host and the export both ship a strict CSP. Any browser-side call to
another origin needs `router.AllowConnectOrigin(...)`, and static exports need
`docs.RewriteStaticCSP` after `ExportStatic`. The policy is HTML-escaped in
the emitted meta tag, so match on the decoded attribute when asserting on it.
