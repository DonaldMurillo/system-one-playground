# GitHub Pages release setup

Target repository: `DonaldMurillo/system-one-playground`.
Target documentation URL: `https://donaldmurillo.github.io/system-one-playground/`.

The root `.github/workflows/pages.yml` builds the existing fastr-docs site from
`docs-site/`, verifies its exported links/search/assets, and deploys the artifact
with GitHub Pages. Pull requests build and validate only; pushes to `main` deploy.
Select **GitHub Actions** as the Pages source in repository Settings → Pages.
No API key is needed for either workflow.

The root CI workflow runs frontend checks, Go tests/vet, instrumented parallel
runtime tests, and command builds. Release CI deliberately excludes paid live
provider tests.

Local deployment-equivalent validation:

```sh
cd docs-site
python3 scripts/sync_content.py --check
PUBLIC_SITE_URL=https://donaldmurillo.github.io/system-one-playground go run . --export tmp/release --export-base /system-one-playground
python3 scripts/verify_export.py tmp/release /system-one-playground
```

The fastr-docs dependency is pinned to a revision including its MIT license. The project
is a 0.5 SysOneScript preview. Release CI builds and verifies cross-platform CLI
archives and VSIX packages; native desktop installers still require separate
target-platform validation.
