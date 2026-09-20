package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	fastrdocs "github.com/DonaldMurillo/fastr-docs"
	uiapp "github.com/DonaldMurillo/gofastr/core-ui/app"
	"github.com/DonaldMurillo/gofastr/core/middleware"
	"github.com/DonaldMurillo/gofastr/framework"
	"github.com/DonaldMurillo/gofastr/framework/uihost"
	docsite "sysonescript.local/docs-site/docs"
)

func main() {
	built, err := buildSite()
	if err != nil {
		panic(err)
	}
	server := built.server
	if dir := exportDir(os.Args[1:]); dir != "" {
		if err := validateExportArgs(os.Args[1:]); err != nil {
			panic(err)
		}
		warnPublicSiteURL()
		base := normalizeBase(exportBase(os.Args[1:]))
		if err := server.ExportStatic(context.Background(), dir, base); err != nil {
			panic(err)
		}

		if err := fastrdocs.WriteStaticNotFound(dir, base, built.notFound, built.notFoundCSS); err != nil {
			panic(err)
		}
		if err := built.router.WriteRuntimeAssets(dir, built.router.AssetPrefix(), normalizeBase(exportBase(os.Args[1:]))); err != nil {
			panic(err)
		}
		if err := copyPublicAssets(dir); err != nil {
			panic(err)
		}
		if err := writeAgentAssets(dir, base, server.Router()); err != nil {
			panic(err)
		}
		if err := writeRedirectStubs(dir, base, built.manifestBody); err != nil {
			panic(err)
		}
		if err := rewriteRuntimeURLs(dir, base, built.router.AssetPrefix()); err != nil {
			panic(err)
		}
		if err := fastrdocs.RewriteStaticCSP(dir, fastrdocs.ContentSecurityPolicy(built.serverConnectOrigins...)); err != nil {
			panic(err)
		}
		fmt.Println("static site exported to " + dir)
		return
	}
	addr := ":3070"
	if port := os.Getenv("PORT"); port != "" {
		addr = listenAddress(port, addr)
	}
	fmt.Println("System One Playground" + " listening on " + listenURL(addr))
	if err := server.Start(addr); err != nil {
		panic(err)
	}
}

func listenAddress(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	if strings.Contains(value, ":") {
		return value
	}
	return ":" + value
}

func listenURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}

type generatedSite struct {
	server               *framework.App
	router               *fastrdocs.Router
	serverConnectOrigins []string
	notFound             fastrdocs.NotFoundScreen
	notFoundCSS          string
	manifestBody         []byte
}

func buildSite() (*generatedSite, error) {
	router := docsite.NewRouter()
	mcpEndpoint := "/mcp"
	if exportDir(os.Args[1:]) != "" {
		mcpEndpoint = "" // Static hosts cannot serve JSON-RPC.
	}
	// Built from the Router so the 404 answers in the language of the URL
	// that was missed. It is the one surface with no route to read a
	// language from.
	notFound := router.NotFoundScreen()
	// Each page carries its own <html lang>, which Pagefind reads to choose a
	// language index and a screen reader reads to choose pronunciation rules.
	site := uiapp.NewApp("System One Playground").WithTheme(router.Theme()).WithLang(router.Language()).WithLangFunc(router.LanguageFor)
	if err := router.Mount(site, docsite.RepositoryLayout(router.Layout())); err != nil {
		return nil, err
	}
	assetNames, err := router.RuntimeAssetNames(normalizeBase(exportBase(os.Args[1:])))
	if err != nil {
		return nil, err
	}
	// Page scripts are asked for by name rather than filtered by suffix: a
	// plugin may ship JavaScript that belongs somewhere other than the page,
	// such as the Mermaid bundle the sandboxed frame loads for itself.
	scriptNames, err := router.RuntimeScriptNames(normalizeBase(exportBase(os.Args[1:])))
	if err != nil {
		return nil, err
	}
	precache := make([]string, 0, len(assetNames)+1)
	for _, name := range assetNames {
		precache = append(precache, router.AssetPrefix()+"/"+name)
	}
	if router.SearchBackend() == fastrdocs.SearchBackendPagefind {
		precache = append(precache, "/pagefind/pagefind.js")
	}
	host := uihost.New(site,
		uihost.WithDescription("System One Playground"+" — reusable documentation built with GoFastr."),
		uihost.WithCustomCSS(router.CSS()+"\n"+router.BrandCSS()+"\n"+docsite.RepositoryCSS),
		uihost.WithNotFoundScreen(notFound),
		uihost.WithPublicLLMMD(),
		uihost.WithAgentReady(uihost.AgentReadyConfig{
			Title:     "System One Playground",
			Summary:   "Read [For agents](/docs/for-agents) and [Add Jev](/docs/enable-jev). The public static site has no executable tools.",
			WhenToUse: "Use this site for the TypeSafe Go client, semlint, SysOneScript, Studio, gate and experiments.",
			AgentCard: &uihost.AgentCardConfig{
				Name:        "System One Playground",
				Description: "Searchable documentation with a router-owned content tree.",
				MCPEndpoint: mcpEndpoint,
			},
			CLI: &uihost.CLIToolConfig{
				Name:    "sysone",
				Install: "git clone https://github.com/DonaldMurillo/system-one-playground.git\ncd system-one-playground\ngo build -o bin/sos ./cmd/sos\ngo build -o bin/sysone ./cmd/sysone\ngo build -o bin/sos-studio ./cmd/sos-studio\nexport PATH=\"$PWD/bin:$PATH\"",
				Docs:    "/docs/for-agents",
			},
		}),
		uihost.WithHeadHTML(`<link rel="icon" href="/assets/favicon.svg">`),
		uihost.WithThemeColor(router.ThemeColor()),
		uihost.WithSitemap(uihost.SitemapConfig{BaseURL: publicSiteURL(), ExcludePaths: append([]string{"/__fastr-docs/"}, router.SitemapExcludePaths()...)}),
		uihost.WithRobots(uihost.RobotsConfig{Disallow: []string{"/__fastr-docs/"}}),
		uihost.WithExtraScripts(assetURLs(router, scriptNames)...),
		uihost.WithAppIcon(docsite.DefaultIconPNG()),
		uihost.WithPWA(uihost.PWAConfig{Name: "System One Playground", ShortName: "System One Playground", Precache: precache}),
	)
	server := framework.NewUIHostApp(host,
		framework.WithConfig(framework.AppConfig{
			Name: "System One Playground",
			SecurityHeaders: middleware.SecurityHeadersConfig{
				ContentSecurityPolicy: fastrdocs.ContentSecurityPolicy(router.ConnectOrigins()...),
			},
		}),
		framework.WithMCP(),
		framework.WithMCPIntrospection(),
	)
	if err := router.MountNavigation(server.Router()); err != nil {
		return nil, err
	}
	if err := router.MountCommandPalette(server.Router()); err != nil {
		return nil, err
	}
	if err := router.MountPluginAssets(server.Router()); err != nil {
		return nil, err
	}
	// One call serves the docs runtime, the search index, the export manifest,
	// and every plugin's assets. Adding a plugin needs no change here.
	if err := router.MountRuntimeAssets(server.Router(), router.AssetPrefix(), normalizeBase(exportBase(os.Args[1:]))); err != nil {
		return nil, err
	}
	if _, err := os.Stat("public"); err == nil {
		if err := router.MountAssets(server.Router(), fastrdocs.AssetConfig{FS: os.DirFS("public"), Prefix: "/assets", MaxAge: 365 * 24 * 60 * 60 * 1e9}); err != nil {
			return nil, err
		}
	}
	manifestBody, err := router.ExportManifestJSON("")
	if err != nil {
		return nil, err
	}
	return &generatedSite{server: server, router: router, serverConnectOrigins: router.ConnectOrigins(), notFound: notFound, notFoundCSS: router.CSS() + "\n" + router.BrandCSS(), manifestBody: manifestBody}, nil
}

func copyPublicAssets(dir string) error {
	if _, err := os.Stat("public"); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return filepath.WalkDir("public", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("public", path)
		if err != nil {
			return err
		}
		destination := filepath.Join(dir, "assets", rel)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.WriteFile(destination, body, 0o644)
	})
}

func publicSiteURL() string {
	if value := strings.TrimSpace(os.Getenv("PUBLIC_SITE_URL")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return "http://localhost:3070"
}

func rewriteRuntimeURLs(dir, base string, assetPrefix string) error {
	base = normalizeBase(base)
	if base == "" {
		return nil
	}
	// Attribute-shaped rewrites: whatever search index or pagefind path the
	// project configured rides on its own attribute, so the rewrite covers
	// custom values instead of chasing the defaults one literal at a time.
	attrRe := regexp.MustCompile(`(data-fastr-docs-(?:index|pagefind)-path=")/`)
	assetPrefix = "/" + strings.Trim(assetPrefix, "/") + "/"
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".html" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		updated := string(body)
		updated = strings.ReplaceAll(updated, `"`+assetPrefix, `"`+base+assetPrefix)
		updated = attrRe.ReplaceAllString(updated, `$1`+base+`/`)
		updated = strings.ReplaceAll(updated, `data-fastr-docs-base=""`, `data-fastr-docs-base="`+base+`"`)
		if updated == string(body) {
			return nil
		}
		return os.WriteFile(path, []byte(updated), 0o644)
	})
}

func validateExportArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		if args[i] == "--export" || args[i] == "--export-base" {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return fmt.Errorf("%s requires a value", args[i])
			}
		}
	}
	return nil
}

func writeRedirectStubs(dir, base string, manifest []byte) error {
	var m struct {
		Redirects []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"redirects"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return err
	}
	for _, redirect := range m.Redirects {
		if err := fastrdocs.WriteStaticRedirect(dir, base, strings.TrimPrefix(redirect.From, base), redirect.To); err != nil {
			return err
		}
	}
	return nil
}

func warnPublicSiteURL() {
	if strings.TrimSpace(os.Getenv("PUBLIC_SITE_URL")) == "" {
		fmt.Println("warning: PUBLIC_SITE_URL is unset; feeds and the sitemap carry http://localhost:3070 URLs")
	}
}

func rewriteStaticCSP(dir, policy string) error {
	return fastrdocs.RewriteStaticCSP(dir, policy)
}

func normalizeBase(base string) string {
	base = strings.TrimSpace(base)
	if base == "" || base == "/" {
		return ""
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	return strings.TrimRight(base, "/")
}

func exportDir(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--export" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(args[i], "--export=") {
			return strings.TrimPrefix(args[i], "--export=")
		}
	}
	return ""
}

func exportBase(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--export-base" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(args[i], "--export-base=") {
			return strings.TrimPrefix(args[i], "--export-base=")
		}
	}
	return ""
}

// assetURLs turns runtime asset names into the URLs the host serves them
// from.
func assetURLs(router *fastrdocs.Router, names []string) []string {
	urls := make([]string, 0, len(names))
	for _, name := range names {
		urls = append(urls, router.AssetPrefix()+"/"+name)
	}
	return urls
}

// fastr-docs prefixes both URLs and physical agent paths. Static hosts mount
// this entire export under base, so normalize only the three physical paths.
func writeAgentAssets(dir, base string, handler http.Handler) error {
	if err := fastrdocs.WriteAgentAssets(dir, base, handler); err != nil {
		return err
	}
	for _, name := range []string{"llms.txt", ".well-known/agent-card.json", ".well-known/agent.json"} {
		if base == "" {
			continue
		}
		source := filepath.Join(dir, strings.Trim(base, "/"), name)
		if _, err := os.Stat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		target := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.Rename(source, target); err != nil {
			return err
		}
	}
	// Upstream defaults every agent card to JSON-RPC, even without an endpoint.
	// Keep descriptive discovery metadata but advertise no callable interfaces.
	for _, name := range []string{".well-known/agent-card.json", ".well-known/agent.json"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var card map[string]any
		if err := json.Unmarshal(data, &card); err != nil {
			return err
		}
		card["supportedInterfaces"] = []any{}
		card["skills"] = []any{}
		card["description"] = "Static documentation only. No MCP or A2A execution endpoint. See the documentation URL for local sysone MCP setup."
		card["documentationUrl"] = strings.TrimRight(publicSiteURL(), "/") + "/docs/for-agents"
		data, err = json.MarshalIndent(card, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
			return err
		}
	}
	return nil
}
