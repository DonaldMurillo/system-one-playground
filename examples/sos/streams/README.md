# Bounded streams

This project uses a local Python stdio plugin to demonstrate the complete
single-owner stream lifecycle without network access. Python 3.10 or newer must
be available on `PATH`.
On Windows, the bundled `python3.cmd` adapter uses the standard `py -3`
launcher, so a normal python.org installation works without a `python3.exe`
alias.

From this directory:

```sh
sos module check modules/events/module.sos.toml
sos run main.sos
sos run early-stop.sos
sos run close.sos
sos run collect.sos
sos run sample.sos
sos run failure.sos
```

`main.sos` watches a simulated deployment as delayed updates arrive from the
Python process. When its health check fails, the script reacts immediately and
cancels the feed, so the plugin's later rollback updates are never emitted.
This is the practical distinction from looping over a list: the values do not
exist when the loop begins, work starts on the first update, and stopping the
consumer stops the producer. `early-stop.sos` cancels an
infinite producer from inside its loop, while `close.sos` closes before
consumption. `collect.sos` materializes only under an overflow bound and
`sample.sos` intentionally cancels after three items. `failure.sos` retains and
prints the two items observed before handling the producer's declared terminal
`ConnectionLost` failure.

The fixture honors item credit and acknowledges cancellation. Its stdout is
protocol-only; program output still comes from `show`. Try replacing a bound
with zero, consuming a handle twice, copying it with `make`, or leaving it active
to see the ownership diagnostics before the plugin starts.
