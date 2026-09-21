// Pure helpers for the sos/vocabulary@1 catalog (tooling-contract.md v2):
// normalization, search, and the two dictionary views (enabled here / library
// explorer). No DOM, no fetch — main.js owns transport and rendering, these
// own matching so search behavior stays testable without a server.

// normalizeCatalog validates the wire shape and drops malformed rows
// instead of throwing: one bad row must not blank the whole dictionary.
// Tolerates the interim adapter omitting schema/root (contract §3).
export function normalizeCatalog(result) {
  const catalog = result && typeof result === 'object' && result.catalog && typeof result.catalog === 'object' ? result.catalog : null
  if (!catalog) return null
  const out = {
    schema: typeof result.schema === 'string' ? result.schema : 'sos/vocabulary@1',
    root: typeof result.root === 'string' ? result.root : '',
    entries: (Array.isArray(catalog.entries) ? catalog.entries : []).filter(isEntry),
    libraries: (Array.isArray(catalog.libraries) ? catalog.libraries : []).filter(isLibrary),
    failures: (Array.isArray(catalog.failures) ? catalog.failures : []).filter(f => f && typeof f.name === 'string'),
    diagnostics: (Array.isArray(result.diagnostics) ? result.diagnostics : []).filter(isDiagnostic)
  }
  completeLibraries(out)
  return out
}

// completeLibraries synthesizes a library row for entries whose library has
// none: alias and origin come from the entries, bare means some pattern is
// spelled without the qualifier. The panel renders headers either way.
function completeLibraries(catalog) {
  const rows = new Map(catalog.libraries.map((l) => [l.path, l]))
  for (const entry of catalog.entries) {
    if (rows.has(entry.library)) continue
    const alias = typeof entry.alias === 'string' && entry.alias ? entry.alias : entry.library.slice(entry.library.lastIndexOf('/') + 1)
    const bare = stringsOf(entry.patterns).some((p) => !p.startsWith(alias + '.'))
    rows.set(entry.library, { path: entry.library, alias, bare, origin: entry.origin || '' })
  }
  catalog.libraries = Array.from(rows.values())
}

function isEntry(e) {
  return !!e && typeof e === 'object' && typeof e.name === 'string' && typeof e.library === 'string'
}
function isLibrary(l) {
  return !!l && typeof l === 'object' && typeof l.path === 'string'
}
function isDiagnostic(d) {
  return !!d && typeof d === 'object' && typeof d.message === 'string'
}

// tokenize splits a query into lowercase AND-tokens: every token must match
// some field, so "trim text" narrows rather than broadens.
export function tokenize(query) {
  return String(query || '').toLowerCase().split(/\s+/).filter(Boolean)
}

function textOf(value) {
  return typeof value === 'string' ? value.toLowerCase() : ''
}

function stringsOf(list) {
  return Array.isArray(list) ? list : []
}

// entryFields is the searchable text of one entry: word, description,
// library, patterns (the usable sentence forms), and synonyms.
function entryFields(entry) {
  const fields = [textOf(entry.name), textOf(entry.id), textOf(entry.library), textOf(entry.description), textOf(entry.import)]
  for (const pattern of stringsOf(entry.patterns)) fields.push(textOf(pattern))
  for (const synonym of stringsOf(entry.synonyms)) fields.push(textOf(synonym))
  return fields
}

function libraryFields(library) {
  return [textOf(library.path), textOf(library.alias), textOf(library.origin), textOf(library.key)]
}

// tokenScore rates one token against one entry's fields: 0 no match,
// 1 substring, 2 word-start, 3 name-prefix.
function tokenScore(fields, token, primary) {
  let best = 0
  for (let i = 0; i < fields.length; i++) {
    const field = fields[i]
    if (!field) continue
    if (field.startsWith(token)) best = Math.max(best, i === 0 ? 3 : 2)
    else if (field.includes(token) || (' ' + field).includes(' ' + token)) best = Math.max(best, i === 0 && primary ? 2 : 1)
  }
  return best
}

function entryMatches(fields, tokens, primary) {
  let score = 0
  for (const token of tokens) {
    const s = tokenScore(fields, token, primary)
    if (!s) return 0 // AND across tokens
    score += s
  }
  return score
}

// searchEntries filters and rank-orders entries for a query. Ranking is a
// stable sort (catalog order breaks ties), so results are deterministic.
export function searchEntries(entries, query) {
  return ranked(entries, entryFields, query, true)
}

export function searchLibraries(libraries, query) {
  return ranked(libraries, libraryFields, query, false)
}

function ranked(entries, fieldsOf, query, primary) {
  const tokens = tokenize(query)
  if (!tokens.length) return entries.slice()
  return entries
    .map((entry, index) => ({ entry, index, score: entryMatches(fieldsOf(entry), tokens, primary) }))
    .filter((e) => e.score > 0)
    .sort((a, b) => b.score - a.score || a.index - b.index)
    .map((e) => e.entry)
}

// libraryEnabled reports whether a library's vocabulary is actually usable
// in this context: imports and configured libraries are; standard and local
// rows are preview-only (core-contract.md §5 origins).
export function libraryEnabled(library) {
  const origin = String(library.origin || '')
  return origin === 'import' || origin.startsWith('config')
}

// enabledView is the "Enabled here" view: enabled entries grouped by their
// libraries, plus every diagnostic (the deterministic conflict surface).
export function enabledView(catalog) {
  return {
    libraries: catalog.libraries.filter(libraryEnabled),
    entries: catalog.entries.filter((e) => e.enabled === true),
    diagnostics: catalog.diagnostics
  }
}

// libraryView is the "Library explorer": every catalog library with its own
// entries (enabled state preserved for dimming) and import preview inputs.
// Groups with neither a library match nor entry matches drop out of a search
// instead of rendering empty shells.
export function libraryView(catalog, query) {
  const tokens = tokenize(query)
  const groups = catalog.libraries.map((library) => {
    const own = catalog.entries.filter((e) => e.library === library.path)
    return { library, entries: own, sampleWords: own.slice(0, 3).map((e) => e.name) }
  })
  if (!tokens.length) return groups
  return groups.filter((g) => entryMatches(libraryFields(g.library), tokens, false) ||
    g.entries.some((e) => entryMatches(entryFields(e), tokens, true)))
}

// importPreview states what the two agreed import forms enable for one
// library (core-contract.md §2): the open import exposes bare words and
// keeps qualified calls; an aliased import requires the qualifier.
export function importPreview(library, sampleWords) {
  const path = library.path
  const alias = library.alias || path.slice(path.lastIndexOf('/') + 1)
  const words = sampleWords.length ? ` (${sampleWords.join(', ')})` : ''
  return {
    open: `import "${path}" exposes the bare vocabulary${words} and ${alias}. qualified calls`,
    aliased: `import "${path}" as ${alias} requires the ${alias}. prefix`
  }
}

// entrySignature renders one entry's args/result line from the catalog's
// param names+types and result type, e.g. "trim(text as text) → text".
export function entrySignature(entry) {
  const params = stringsOf(entry.params).map((p) => p && `${p.name} as ${p.type}`).filter(Boolean)
  const head = `${entry.name}(${params.join(', ')})`
  const result = entry.result ? `${head} → ${entry.result}` : head
  const failures = stringsOf(entry.possibleFailures).filter(Boolean)
  return failures.length ? `${result} · may fail with ${failures.join(', ')}` : result
}

// needsJev reports whether invoking an entry spends provider budget.
export function needsJev(entry) {
  return stringsOf(entry.effects).some((e) => String(e).toLowerCase() === 'jev')
}

// diagnosticsFor filters diagnostics to those mentioning one library path
// (used when the dictionary is scoped from the editor).
export function diagnosticsFor(diagnostics, importPath) {
  if (!importPath) return diagnostics
  return diagnostics.filter((d) => String(d.message || '').includes(importPath))
}
