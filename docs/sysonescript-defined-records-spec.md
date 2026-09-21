# Named record definitions

Status: implemented in SysOneScript 0.3; retained as the detailed design and
compatibility contract.

This proposal adds named, structurally validated records without introducing
classes or object-oriented dispatch. `field of value` is the idiomatic data
access form. Existing `value.field` access remains valid but is non-idiomatic;
dotted names are preferred for actions exposed by imported modules and standard
libraries.

## Goals

- Let scripts name reusable record shapes.
- Permit named record types in action parameters.
- Express optional fields as `as optional TYPE`.
- Keep JSON and records from external sources useful without constructors or
  hidden runtime wrappers.
- Offer concise, explicit field destructuring for actions.
- Make natural data access the documented and generated default while retaining
  concise dotted access for compatibility and advanced use.
- Give the checker, runtime, debugger, and editors one shared type model.

## Non-goals

This proposal does not add classes, methods, inheritance, interfaces, mutable
fields, overloaded actions, generic types, inferred action return types, or
user-defined constructors. It does not add `field from record` or possessive
access such as `record's field` as alternate spellings.

## Core syntax

```sos
define User:
  name as text
  age as optional integer
  active as boolean

to greet with user as User:
  return "Hello {name of user}"

make donald as User with:
  name from "Donald"
  active from true

call greet with donald called message
show message
```

`define` declares data only. Behavior remains in `to` actions.

### Definition grammar

```text
definition      := "define" TypeName ":"
field           := FieldName "as" ["optional"] FieldType
FieldType       := ScalarType | TypeName | "list of" FieldType
ScalarType      := "text" | "timestamp" | "number" | "integer" |
                   "boolean" | "duration"
```

Type names begin with an uppercase letter. Field and binding names retain the
existing lowercase/underscore convention. Duplicate fields are errors.

Definitions are allowed at file top level and participate in package exports.
They are visible throughout their declaration scope regardless of source order,
like existing actions and validation schemas.

The initial implementation may ship scalar and named field types first, but
the grammar reserves `list of TYPE`; it must not be silently accepted until the
runtime and all tooling implement it.

### Optional fields

The modifier precedes the underlying type:

```sos
age as optional integer
manager as optional User
```

An optional field may be absent or contain `null`. If present and non-null, its
value must match the declared type. Reading an absent optional field evaluates
to `null`. A required field may not be absent or `null`.

Optional does not imply a default value. Defaults need a separate proposal
because they raise evaluation-order, serialization, and versioning questions.

## Constructing a named record

```sos
make donald as User with:
  name from "Donald"
  age from 30
  active from true
```

Construction evaluates field expressions from top to bottom, exactly once.
The result is an ordinary immutable SOS record; named records do not receive
methods or hidden user-visible fields.

Construction fails when:

- a required field is missing;
- an unknown field is supplied;
- a field appears more than once; or
- a supplied value does not match its field type.

Optional fields may be omitted. Declaration order controls stable display and
editor presentation; JSON object ordering remains semantically irrelevant.

Named records are structurally validated. A record read from JSON can be passed
to an action expecting `User` when its complete shape satisfies `User`; callers
do not have to rebuild external data merely to attach a nominal runtime tag.
Unknown fields are rejected at typed boundaries. This preserves typo detection
and makes definitions closed shapes rather than partial schemas.

## Typed action parameters

```sos
to greet with user as User, greeting as text:
  return "{greeting}, {name of user}"

call greet with donald, "Hello" called message
```

Parameter grammar becomes:

```text
parameter       := Name ["as" ["optional"] ParameterType]
ParameterType   := FieldType | "any"
```

Existing untyped parameters remain valid and behave as `any`. Arguments remain
positional and comma-separated. Each typed argument is validated before the
action body begins. An optional parameter accepts `null`; it does not make the
argument itself omittable. Default and omitted action arguments are out of
scope.

This proposal does not add return-type declarations. A later proposal can do
that without changing parameter or record syntax.

## Field access

The idiomatic field-access form is:

```sos
name of user
manager of user
name of manager of user
```

`of` associates from right to left, so the last expression means:

```text
name of (manager of user)
```

Accessing an absent optional field returns `null`. Accessing a field through
`null`, accessing an unknown field, or applying field access to a non-record is
an error. When the receiver has a known named type, unknown fields are checker
errors. Otherwise they are runtime errors.

Dynamic keys remain explicit library operations:

```sos
call record.get with user, key called value
```

### Dotted names

Dotted data access remains valid, but is not the idiomatic spelling:

```sos
# Valid, non-idiomatic
show user.name

# Valid and idiomatic
show name of user
```

Dotted module and standard-library action targets remain idiomatic:

```sos
import "std/text" as text
call text.upper with name of user called loud_name
```

The parser distinguishes the forms by context: a dotted call target resolves as
a module action, while a dotted expression resolves as data access. The checker
does not emit an error for dotted data access. Formatters must preserve the
author's spelling and must not silently rewrite it.

An optional style diagnostic may recommend `name of user` when idiomatic hints
are enabled. It must be informational, disabled by default, and accompanied by
a safe quick fix. Generated examples, completion, documentation, and code
actions use the `field of value` form.

## Explicit destructuring with `using`

An action with exactly one named-record parameter may expose selected fields as
local bindings:

```sos
to greet with user as User using name:
  return "Hello {name}"

to describe with user as User using name, age:
  when age is null:
    return "{name} has no recorded age"
  return "{name} is {age}"
```

This is shorthand for reading those fields at action entry. It does not pass a
smaller record and does not change the action's call syntax.

Rules:

- `using` is allowed only when there is exactly one parameter whose type is a
  named record.
- Every selected name must be a field of that definition.
- A field may be selected once.
- A selected name must not collide with a parameter or another local binding.
- Optional selected fields bind `null` when absent.
- The checker resolves `using` statically; no dynamic field names are allowed.

These restrictions avoid ambiguity when an action later accepts two records
with identically named fields. Callers can always use `field of parameter`
without destructuring.

## Type identity and compatibility

Named record definitions are closed structural types at runtime:

1. The value must be a record.
2. Every required declared field must be present and non-null.
3. Every present declared field must match its type recursively.
4. No undeclared field may be present.

Two differently named definitions with identical fields accept the same raw
record. The names improve declarations, diagnostics, documentation, and editor
support; they are not opaque nominal brands. This keeps JSON interoperability
predictable.

Recursive definitions are rejected in the first implementation, including
recursion through lists or optional fields. Supporting recursive data safely
requires explicit depth and cycle rules and can be added separately.

## Relationship to existing `expect` schemas

Existing validation schemas remain focused on collection validation:

```sos
expect UserInput with:
  name as text
  age as integer

require each user in users matches UserInput
```

`define User` is a language type usable in construction, parameters, field
checking, hover information, and completion. `expect UserInput` remains an
explicit runtime validation declaration used by `require each`.

They should share one internal field/type validator, but they are not aliases in
0.3. Merging or replacing `expect` would be a separate compatibility decision.

## Packages and exports

A package may export a definition:

```sos
package people
export User

define User:
  name as text
```

Imported types are referenced by their exported type name in type positions;
they do not introduce general-purpose dotted expressions. If two imports export
the same type name, the checker requires import aliases to disambiguate them in
a future qualified-type proposal. For 0.3, such collisions are errors rather
than inventing partially supported `alias.Type` syntax.

## Diagnostics and tooling

The language service must provide:

- completion for definition names after `as`;
- completion for valid fields after `using` and after `FIELD of` contexts;
- hover information showing requiredness and declared type;
- definition navigation from a typed parameter or construction to `define`;
- references and rename for definitions and their declared fields;
- semantic tokens for definitions, fields, types, and optional modifiers;
- inlay hints for inferred receiver types where useful;
- debugger rendering that identifies a known shape as `User` without changing
  the underlying record value; and
- an optional style quick fix converting unambiguous `receiver.field` source
  into `field of receiver`.

Formatting preserves one field per line and declaration order. It does not
expand or collapse `using` clauses.

## Errors

Diagnostics should name the definition, field, expected type, actual type, and
source location when available. Representative messages:

```text
User requires field name as text
User has no field nickname
User.age must be integer or null; received text
cannot read name of null
using field name conflicts with parameter name
recursive definition Node is not supported
```

Runtime errors preserve action call frames. Validation must stop before the
action body produces effects.

## Compatibility and migration

Existing dotted data access remains source-compatible. Before release:

1. Prefer `field of record` in new repository examples and documentation.
2. Add an optional editor style hint and quick fix for simple dotted chains.
3. Preserve dotted expressions as supported field access.
4. Keep `alias.action` resolution in call-target grammar and field access in
   expression grammar so the two meanings remain unambiguous.
5. Increment the language/analysis format version so stale saved resolutions
   fail with a regeneration message.
6. Regenerate bundled compiler and editor assets.

## Verification matrix

The implementation is complete only when the same cases pass in the interpreter,
native builds, browser WASM, WASI, CLI checking, Studio, and the VS Code extension.

Required coverage includes:

- construction with all required fields;
- omitted and explicit-null optional fields;
- missing, unknown, duplicate, and mistyped fields;
- typed calls using constructed and JSON-origin records;
- nested named records and field access;
- `using` success, optional fields, unknown fields, and collisions;
- rejection of recursive definitions;
- dotted data access compatibility and optional idiomatic-style quick fixes;
- continued module and standard-library dotted calls;
- package export visibility and duplicate type names;
- source formatting, diagnostics, hover, completion, rename, semantic tokens,
  debugger values, and source maps; and
- stable JSON output with no hidden type marker.

## Implementation sequence

1. Add shared type/definition structures and recursive validation limits.
2. Parse and check `define`, optional types, and typed action parameters.
3. Implement typed construction and action-boundary validation.
4. Make `of` access the generated/documented default while preserving dotted
   expression compatibility.
5. Implement constrained `using` destructuring.
6. Add package export behavior and collision diagnostics.
7. Complete LSP, Studio, debugger, syntax grammar, and VS Code support.
8. Run backend parity, end-to-end editor, build, lint, and packaging checks.

## Accepted design decisions

- The declaration keyword is `define`.
- Optional fields use `as optional TYPE`.
- Idiomatic data access uses `field of value`.
- `value.field` remains valid but non-idiomatic; tooling preserves it and may
  offer an opt-in style hint.
- Dots remain idiomatic for module/library action targets.
- Records contain data; actions contain behavior.
- Named records use closed structural validation and remain ordinary records.
- `using` is explicit, restricted destructuring rather than implicit capture.
- Defaults, methods, recursive types, return annotations, and omitted action
  arguments are deferred.
