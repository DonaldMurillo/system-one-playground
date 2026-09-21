# Persistent stdio plugin

This project demonstrates one long-lived Python JSON-RPC plugin, typed records,
declared failures, explicit secret authorization, and host-enforced timeouts.
The plugin never receives the rest of the parent process environment.

From this directory:

```sh
export SOS_EXAMPLE_PLUGIN_TOKEN=demo-only
sos module check modules/profile/module.sos.toml
sos module generate modules/profile/module.sos.toml
sos module describe local/profile
sos module doctor local/profile
sos run main.sos
```

Run `failure.sos` with the token set to exercise the declared `Rejected`
failure and recovery without restarting the process. Run `timeout.sos` to see
SOS cancel and clean up a plugin that exceeds its action deadline; that command
is expected to exit with an error.
