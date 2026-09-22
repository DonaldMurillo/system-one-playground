package docs

import docs "github.com/DonaldMurillo/fastr-docs"

// NewRouter is the single public content inventory for navigation, search and export.
func NewRouter() *docs.Router {
	r := docs.NewRouter(docs.WithSiteName("System One Playground"), docs.WithBrand(docs.BrandConfig{Name: "System One Playground", LogoURL: "/assets/favicon.svg", LogoAlt: "Four-tab documentation book", FaviconURL: "/assets/favicon.svg"}))
	r.MustPage("/", docs.PageConfig{Title: "System One Playground", Description: "Go client, semantic tooling, language and Studio documentation.", SourcePath: "content/home.md", Order: 1, Offline: true})
	r.MustPage("/docs/getting-started", docs.PageConfig{Title: "Get started", Description: "Build the tools and run your first offline script.", SourcePath: "content/getting-started.md", Order: 2, Offline: true})
	r.MustPage("/docs/language", docs.PageConfig{Title: "Language reference", Description: "SysOneScript 0.6: language reference.", SourcePath: "content/language.md", Order: 3, Offline: true})
	r.MustPage("/docs/commands", docs.PageConfig{Title: "Commands and actions", Description: "SysOneScript 0.6: commands and actions.", SourcePath: "content/commands.md", Order: 4, Offline: true})
	r.MustPage("/docs/packages", docs.PageConfig{Title: "Packages and standard library", Description: "SysOneScript 0.6: packages and standard library.", SourcePath: "content/packages.md", Order: 5, Offline: true})
	r.MustPage("/docs/vocabulary", docs.PageConfig{Title: "Imports and vocabulary", Description: "SysOneScript 0.6: imports and vocabulary.", SourcePath: "content/vocabulary.md", Order: 6, Offline: true})
	r.MustPage("/docs/configuration", docs.PageConfig{Title: "Configuration and budgets", Description: "SysOneScript 0.6: configuration and budgets.", SourcePath: "content/configuration.md", Order: 7, Offline: true})
	r.MustPage("/docs/interpretation", docs.PageConfig{Title: "Jev interpretation", Description: "SysOneScript 0.6: jev interpretation.", SourcePath: "content/interpretation.md", Order: 8, Offline: true})
	r.MustPage("/docs/projects", docs.PageConfig{Title: "Studio projects", Description: "SysOneScript 0.6: studio projects.", SourcePath: "content/projects.md", Order: 9, Offline: true})
	r.MustPage("/docs/project-environment", docs.PageConfig{Title: "Environment settings", Description: "SysOneScript 0.6: environment settings.", SourcePath: "content/project-environment.md", Order: 10, Offline: true})
	r.MustPage("/docs/agent-interface", docs.PageConfig{Title: "CLI and MCP", Description: "SysOneScript 0.6: cli and mcp.", SourcePath: "content/agent-interface.md", Order: 11, Offline: true})
	r.MustPage("/docs/external-modules", docs.PageConfig{Title: "External modules", Description: "Typed command and persistent stdio plugin modules.", SourcePath: "content/external-modules.md", Order: 30, Offline: true})
	r.MustPage("/docs/editor", docs.PageConfig{Title: "Colors, hints and diagnostics", Description: "SysOneScript 0.6: colors, hints and diagnostics.", SourcePath: "content/editor.md", Order: 12, Offline: true})
	r.MustPage("/docs/tickets", docs.PageConfig{Title: "Tickets walkthrough", Description: "SysOneScript 0.6: tickets walkthrough.", SourcePath: "content/tickets.md", Order: 13, Offline: true})
	r.MustPage("/docs/repository-cli", docs.PageConfig{Title: "Build a repository CLI", Description: "SysOneScript 0.6: build a repository cli.", SourcePath: "content/repository-cli.md", Order: 14, Offline: true})
	r.MustPage("/docs/limits", docs.PageConfig{Title: "Status and limitations", Description: "What ships in 0.6 and what remains planned.", SourcePath: "content/limits.md", Order: 15, Offline: true})
	r.MustPage("/docs", docs.PageConfig{Title: "SysOneScript", Description: "Language guides and reference.", SourcePath: "content/index.md", Order: 16, Offline: true})
	r.MustPage("/docs/tickets-source", docs.PageConfig{Title: "Tickets source", Description: "Complete runnable tickets CLI source.", SourcePath: "content/tickets-source.md", Order: 17, Offline: true})
	r.MustPage("/client", docs.PageConfig{Title: "Go client", Description: "Go client documentation.", SourcePath: "content/go-client.md", Order: 18, Offline: true})
	r.MustPage("/semlint", docs.PageConfig{Title: "semlint", Description: "semlint documentation.", SourcePath: "content/semlint-guide.md", Order: 19, Offline: true})
	r.MustPage("/studio", docs.PageConfig{Title: "Studio", Description: "Studio documentation.", SourcePath: "content/studio-overview.md", Order: 20, Offline: true})
	r.MustPage("/tools", docs.PageConfig{Title: "Tools and experiments", Description: "Tools and experiments documentation.", SourcePath: "content/repository-tools.md", Order: 21, Offline: true})
	r.MustPage("/docs/question-batches", docs.PageConfig{Title: "Questions and batches", Description: "First-class Jev questions and one-request batch evaluation.", SourcePath: "content/question-batches.md", Order: 22, Offline: true})
	r.MustPage("/docs/io", docs.PageConfig{Title: "Files, streams and values", Description: "Filesystem, subprocess, text and collection operations.", SourcePath: "content/io.md", Order: 23, Offline: true})
	r.MustPage("/docs/streams", docs.PageConfig{Title: "Streams and long-running operations", Description: "Bounded single-owner streams, cancellation, materialization, and external producers.", SourcePath: "content/streams.md", Order: 24, Offline: true})
	r.MustPage("/docs/http", docs.PageConfig{Title: "HTTP clients and services", Description: "Bounded HTTP requests and owned native service listeners.", SourcePath: "content/http.md", Order: 31, Offline: true})
	r.MustPage("/docs/parallel", docs.PageConfig{Title: "Parallel maps and failures", Description: "Bounded workers, isolated state, ordered results and shared budgets.", SourcePath: "content/parallel.md", Order: 25, Offline: true})
	r.MustPage("/docs/semlint-sos", docs.PageConfig{Title: "Build semlint in SOS", Description: "Repository scanning, parallel rule evaluation and calibration with the Go reference retained.", SourcePath: "content/semlint-sos.md", Order: 26, Offline: true})
	r.MustPage("/docs/semlint-source", docs.PageConfig{Title: "SOS semlint source", Description: "Complete runnable repository scanner and calibration CLI.", SourcePath: "content/semlint-source.md", Order: 27, Offline: true})
	r.MustPage("/docs/enable-jev", docs.PageConfig{Title: "Add Jev to your application", Description: "Enable credentials, modes, budgets and application integration.", SourcePath: "content/enable-jev.md", Order: 28, Offline: true})
	r.MustPage("/docs/for-agents", docs.PageConfig{Title: "For agents", Description: "Read machine-friendly docs and connect the local project CLI or MCP.", SourcePath: "content/for-agents.md", Order: 29, Offline: true})
	return r
}
