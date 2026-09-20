package docs

import (
	"context"

	uiapp "github.com/DonaldMurillo/gofastr/core-ui/app"
	"github.com/DonaldMurillo/gofastr/core-ui/component"
	"github.com/DonaldMurillo/gofastr/core/render"
)

// RepositoryLayout preserves the docs header and its request-aware navigation,
// adding persistent source and release links on every page and viewport.
func RepositoryLayout(layout *uiapp.Layout) *uiapp.Layout {
	layout.Header = repositoryHeader{original: layout.Header}
	return layout
}

type repositoryHeader struct{ original component.Component }

func (h repositoryHeader) Render() render.HTML { return h.RenderCtx(context.Background()) }
func (h repositoryHeader) RenderCtx(ctx context.Context) render.HTML {
	var header render.HTML
	if c, ok := h.original.(interface {
		RenderCtx(context.Context) render.HTML
	}); ok {
		header = c.RenderCtx(ctx)
	} else if h.original != nil {
		header = h.original.Render()
	}
	return render.Join(header, render.Tag("nav", map[string]string{"class": "repository-links", "aria-label": "Project on GitHub"},
		render.Tag("a", map[string]string{"href": "https://github.com/DonaldMurillo/system-one-playground"}, render.Text("GitHub ↗")),
		render.Tag("a", map[string]string{"href": "https://github.com/DonaldMurillo/system-one-playground/releases"}, render.Text("Releases")),
		render.Tag("a", map[string]string{"href": "https://github.com/DonaldMurillo/system-one-playground/issues"}, render.Text("Report an issue")),
	))
}

const RepositoryCSS = `.repository-links{display:flex;flex-wrap:wrap;justify-content:flex-end;align-items:center;gap:.25rem 1.25rem;padding:.25rem 1rem;border-bottom:1px solid var(--color-border);font-size:.875rem}.repository-links a{display:inline-flex;align-items:center;min-height:2rem;text-decoration:underline;text-underline-offset:.2em}.repository-links a:focus-visible{outline:2px solid currentColor;outline-offset:3px}`
