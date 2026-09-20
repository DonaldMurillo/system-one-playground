---
name: docs-theming
description: Change the visual template, brand identity, and UI labels of this fastr-docs site.
---

# Theming and branding

Three separate knobs. Reach for the narrowest one that does the job.

## Template

A build-time choice of visual system. Five are supported:

`editorial` (default, warm and compact), `terminal` (monospace, high
contrast), `blueprint` (cool and technical), `studio` (softer, expressive),
`notebook` (reading-first serif).

```go
router := docs.NewRouter(docs.WithTemplate(docs.TemplateBlueprint))
```

`docs.ParseTemplate` normalizes a name from an environment variable and falls
back to `editorial` rather than failing, so a typo cannot break a deployment.

## Brand

`WithBrand` carries identity without coupling the site to a product:

```go
router := docs.NewRouter(docs.WithBrand(docs.BrandConfig{
    Name:        "Acme Docs",
    LogoURL:     "/assets/logo.svg",
    FaviconURL:  "/assets/favicon.svg",
    ThemeColor:  "#0b1020",
    AccentColor: "#4f8cff",
}))
```

Empty fields fall back to the neutral mark and the Router's site name. Leave
them empty rather than restating the default.

## Labels and translation

`WithUIStrings` translates every framework-owned label without touching route
titles or content metadata. The struct is grouped: top-level fields for the
shell, `Blog` for the publication surface, `NotFound` for the 404 page. Empty
fields keep their English defaults, so translate one label at a time.

Labels with `%s` or `%d` are format strings. Reorder the surrounding words
freely; dropping the placeholder is tolerated rather than corrupting the page.

`DateFormat` is a Go time layout; `Months` and `ShortMonths` name the months for a layout that spells them out. Go has no CLDR data, so the date format is
the project's choice, not something derived from the locale.

Translated page content is the project's job; the framework does not invent
translations. `WithLocaleFallback("en")` keeps a partially translated site
usable by serving the default-locale page where a translation is missing, and
`fastr-docs check` logs which pages still need one.

## Token overrides

`WithTheme(docs.ThemeConfig{...})` takes `Overrides` for individual design
tokens when a template is close but not exact. Prefer overriding tokens to
writing page-specific CSS, which the layout will not know about.

## Checks

Themes change rendered HTML and CSS, so run the Playwright visual suite after
a change, not just `go test ./...`. Check both light and dark.
