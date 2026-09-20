# Imports in command projects

A command entry file can declare imports at the top level, alongside its root
command, schemas, and reusable actions. Imports resolve through the normal
module loader before any command body runs. Missing libraries and invalid
exports remain errors; imports do not authorize top-level executable statements.

For example:

```sos
import "std/text" as text

command title:
  argument value as text
  call text.trim with value called clean
  show clean
```

Local modules follow the same rule. Native builds embed their resolved package
graph. The [repository assistant](../examples/sos/repo-assistant/README.md) exercises
two local package files, standard imports, nested commands, data validation,
optional Jev judgment, and source-independent native execution.
