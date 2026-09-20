# Third-party notices

System One Playground source is MIT licensed. Dependencies retain their own licenses.

- Studio bundles Monaco Editor 0.52.2, copyright Microsoft Corporation, under MIT. Its license and upstream third-party notices ship in `studio/public/licenses/` and the built workbench's `/licenses/` directory.
- The optional desktop app uses Wails v2; its dependency graph is pinned in `desktop/go.mod` and `desktop/go.sum`.
- The documentation site uses GoFastr (MIT) and fastr-docs, pinned in `docs-site/go.mod`. Both projects are MIT licensed; their own license files retain their respective copyright notices.

Before distributing desktop binaries, include the notices for the linked platform dependencies. This source preview does not include desktop installers.
