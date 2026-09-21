# External command module

From this directory, inspect and run the adapter:

```sh
sos module check modules/echo/module.sos.toml
sos module generate modules/echo/module.sos.toml
sos module describe local/echo
sos run main.sos
```

The definition is parsed offline. `printf` starts only when `echo.say` is
called, and it is invoked directly without a shell.
