---
name: docs-blog
description: Publish Markdown posts and the matching RSS feed in this fastr-docs site.
---

# Blog and RSS

Posts live in the Router, not in a parallel publishing system. That is what
keeps navigation, search, locale and version filters, and the static export in
agreement with the feed.

## Registering the collection

```go
router.MarkdownBlog("/blog", "content/blog", docs.BlogConfig{
    Title:       "Updates",
    Description: "Release notes and announcements.",
    Order:       5,
    Offline:     true,
})
```

Use `MarkdownBlogFS` when content ships in an `embed.FS`. One call gives you
the post routes plus search, tags, authors, and archive views. Those aggregate
views are on by default; turn them off with `DisableSearch`, `DisableTags`,
`DisableAuthors`, or `DisableArchive`. `PostsPerPage` defaults to 10 and
`RelatedPosts` to 3.

## Post front matter

`date` drives ordering and the archive year. `authors` and `tags` drive the
author and tag views. `excerpt` overrides the generated summary. `draft: true`
keeps a post out of navigation, search, export, and the feed.

## The feed

`router.MountRSS(server.Router(), "/blog/feed.xml", cfg)` serves the feed from
the live host. `router.RSSXML(cfg)` returns the bytes, and
`docs.WriteStaticRSS` writes them into a static export. A site that does not
publish updates should not call any of these; the feed is opt-in.

## Checks

`router.BlogPosts("/blog")` returns the published posts in order. Use it to
confirm drafts are excluded and dates sort the way you expect. Then run
`fastr-docs check .` and `go test ./...`.
