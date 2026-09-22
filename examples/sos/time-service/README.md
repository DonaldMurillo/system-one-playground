# time-service

A scheduled CLI service from the approved time specification: an hourly
calendar schedule in a named zone and a scoped deadline around each check.
The service waits for each next hourly occurrence and keeps running until
stopped, so it is a long-running demo; for a fast CLI walkthrough of the same
time constructs see `examples/sos/time-basics` (`sos run main.sos`).

Run the service:

```text
sos run main.sos -- --zone America/New_York
```

Build a standalone artifact; the binary embeds the IANA time-zone database and
records its provenance (including the IANA release) beside the artifact:

```text
sos build main.sos --output healthwatch
cat healthwatch.timezone.json
```

Durable progress checkpoints for restart catch-up are a future state-library
feature; today `watch` starts at the next scheduled time after it opens.
