# Filesystem operations

This project demonstrates the canonical filesystem surface end to end:
immediate listing, bounded materialized traversal, backpressured streaming
traversal, atomic writing with an explicit policy, copy and move with explicit
overwrite wording, explicit removal, and a native background watcher.

From this directory:

```sh
sos run main.sos -- scan src --output output/inventory.txt
sos run main.sos -- tidy
sos run stream-scan.sos
sos run watch-inbox.sos
```

`main.sos` is the CLI example. `scan` checks existence before touching the
tree (`check whether ... exists` distinguishes a missing folder from a
permission failure), lists immediate folder children, walks the tree under an
explicit entry bound with exclusions, a depth limit, and no link following,
then writes its summary atomically with a replacing policy. `tidy` archives
the inventory with `copy ... only if the destination does not exist`, handles
the reserved `FileAlreadyExists` failure instead of overwriting, moves with
`replacing an existing file`, and removes files and an empty folder with
wording that states exactly what is deleted.

`stream-scan.sos` contrasts `walk through` with `stream ... under`: the stream
emits entries incrementally in depth-first lexical order, `stop reading`
cancels the walk from inside the loop and execution continues below it, and a
second stream opens independently afterwards. Watch the Studio or VS Code
Streams panel while it runs: the traversal appears with its bound and can be
stopped without stopping the run.

`watch-inbox.sos` is the background-service example. It watches `incoming/`
recursively, files each created document under `processed/`, appends to an
activity log, and treats an `overflowed` change as a required rescan rather
than a silent gap. The watcher is an independently owned stream: leave it
running, create and delete files under `incoming/`, then stop the run (or use
Stop stream) to finish. Watching is native-only; it needs the native CLI
rather than a browser host.

The scripts create their working folders as they go, so the example is safe to
run inside this directory. `FileNotFound`, `FileAlreadyExists`, and the other
reserved failures are typed runtime failures: their fields (like `path`) are
bound directly by `on failure` handlers.
