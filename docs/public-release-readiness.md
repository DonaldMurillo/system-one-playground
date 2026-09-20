# Public preview release

System One Playground is prepared as a **0.6 source preview** under MIT.
Repository: `https://github.com/DonaldMurillo/system-one-playground`.
Documentation: `https://donaldmurillo.github.io/system-one-playground/`.

## Release checks

- Root CI covers Go tests/vet, frontend tests/lint, command builds and selected runtime race tests.
- GitHub Pages builds the pinned fastr-docs site, verifies the exported project path, then deploys on main. Pull requests validate without deploying.
- fastr-docs now includes an MIT license; the documentation module pins that licensed revision. GoFastr is MIT licensed.
- Monaco's license and upstream notices ship with Studio. See `THIRD_PARTY_NOTICES.md`.
- Local environment files, generated executables, browser captures and internal agent notes are excluded from Git.
- The README provides source installation, an offline first script, Studio setup and component entry points.

## Preview boundaries

No cross-platform binary installers are published. Desktop packaging has been
checked locally on macOS; other target platforms still need native validation.
Do not present this preview as a stable 1.0 release. Paid live API tests are
opt-in and do not run in CI. Request/time limits are not monetary spending caps.

See [GitHub Pages setup](github-pages.md) for the exact export command and workflow.
