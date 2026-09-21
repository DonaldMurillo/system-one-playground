# Bundled external module

This project uses `printf` while developing, then substitutes a checksummed
artifact when building a standalone application. The emitted directory contains
the SOS executable, locked manifest, module definition, and plugin artifact.

From this directory:

```sh
sos module check modules/echo/module.sos.toml
sos run main.sos
sos build main.sos --output dist/echo-app
./dist/echo-app/echo-app
cat dist/echo-app/manifest.json
```

The portable fixture covers macOS and Linux. A real release should produce a
native artifact per target (including Windows), update each checksum, then let
`sos build` verify and package it. SOS deliberately does not run ecosystem build
hooks or copy an arbitrary executable from `PATH`.
