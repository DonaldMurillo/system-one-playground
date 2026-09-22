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
sos run node.sos
sos run local.sos
sos run market-watch.sos
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

`node.sos` exercises the same credit-based protocol through a Node.js
producer. Keeping both fixtures executable prevents the protocol from quietly
depending on one language's buffering or process behavior.

`market-watch.sos` is the complete live-feed example: its Node adapter remains
alive for ten seconds and emits one typed quote each second. The SysOneScript
program reacts as values arrive and computes the observed high without first
building a list. The adapter uses deterministic offline prices so anyone can
run it; its `emit` call is the seam where a production broker WebSocket or
weather polling client would supply real updates.

`local.sos` needs no plugin at all. It defines a streaming action in
SysOneScript, sends typed progress records as work happens, and deliberately
stops after the successful deployment. This demonstrates that a stream is a
live, cancellable producer rather than another spelling of a list loop.

The fixture honors item credit and acknowledges cancellation. Its stdout is
protocol-only; program output still comes from `show`. Try replacing a bound
with zero, consuming a handle twice, copying it with `make`, or leaving it active
to see the ownership diagnostics before the plugin starts.
