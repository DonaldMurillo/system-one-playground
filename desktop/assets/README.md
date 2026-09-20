# Studio icon

`appicon.svg` is the editable source for Studio's System One terminal mark.
Its charcoal, teal and amber palette matches the playground documentation.
The PNG has transparent outer margins and is rendered at 1024 × 1024.

On macOS, regenerate from the repository root without installing dependencies:

```sh
swift desktop/scripts/render-icon.swift desktop/assets/appicon.svg desktop/build/appicon.png
```

Wails converts `build/appicon.png` to the packaged platform icon during its
build. The About dialog embeds the same PNG. Commit the SVG and PNG together;
other platforms can build from the checked-in PNG without Swift.
