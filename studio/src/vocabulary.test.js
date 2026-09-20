import {test} from 'node:test'
import assert from 'node:assert/strict'
import {
  normalizeCatalog, tokenize, searchEntries, searchLibraries, enabledView,
  libraryView, libraryEnabled, importPreview, entrySignature, needsJev, diagnosticsFor
} from './vocabulary.js'

// Fixture follows tooling-contract.md v2 / core-contract.md §1 exactly.
const catalog = () => normalizeCatalog({
  schema: 'sos/vocabulary@1',
  root: '/w',
  catalog: {
    entries: [
      {id: 'std/json.decode', library: 'std/json', alias: 'json', name: 'decode', kind: 'native', patterns: ['decode VALUE', 'json.decode VALUE'], description: 'Decodes JSON text into a value.', params: [{name: 'text', type: 'text'}], result: 'any', effects: ['pure'], origin: 'standard library', enabled: false, import: 'import "std/json"'},
      {id: 'std/text.trim', library: 'std/text', alias: 'text', name: 'trim', kind: 'native', patterns: ['trim VALUE', 'trim title called clean', 'text.trim VALUE'], synonyms: ['strip'], description: 'Removes surrounding whitespace.', params: [{name: 'text', type: 'text'}], result: 'text', effects: ['pure'], origin: 'import', enabled: true},
      {id: 'std/text.upper', library: 'std/text', alias: 'text', name: 'upper', kind: 'native', patterns: ['upper VALUE', 'text.upper VALUE'], description: 'Uppercases text.', params: [{name: 'text', type: 'text'}], result: 'text', effects: ['pure'], origin: 'import', enabled: true},
      {id: 'example.com/demo/media.clip', library: 'example.com/demo/media', alias: 'media', name: 'clip', kind: 'action', patterns: ['media.clip VALUE'], description: 'Trims a clip for preview.', params: [{name: 'video', type: 'any'}, {name: 'start', type: 'duration'}], result: 'any', effects: ['read', 'jev'], origin: 'local /w/media', enabled: false, import: 'import "example.com/demo/media" as media'},
      'not-an-entry'
    ],
    libraries: [
      {path: 'std/text', alias: 'text', bare: true, origin: 'import'},
      {path: 'example.com/demo/media', alias: 'media', bare: false, origin: 'local /w/media', key: 'example.com/demo/media'},
      {path: 'std/json', alias: 'json', bare: false, origin: 'standard library'},
      'not-a-library'
    ]
  },
  diagnostics: [
    {line: 3, column: 1, message: 'vocabulary word trim collides across std/text (import), example.com/demo/media (config /w/sos.toml)'},
    'not-a-diagnostic'
  ]
})

test('normalizeCatalog keeps well-formed rows and drops malformed ones', () => {
  const c = catalog()
  assert.equal(c.entries.length, 4)
  assert.equal(c.libraries.length, 3)
  assert.equal(c.diagnostics.length, 1)
  assert.equal(c.root, '/w')
  assert.equal(normalizeCatalog(null), null)
  assert.equal(normalizeCatalog({schema: 'sos/vocabulary@1'}), null)
  // Interim adapter without schema/root still normalizes.
  const bare = normalizeCatalog({catalog: {entries: [], libraries: []}})
  assert.equal(bare.schema, 'sos/vocabulary@1')
  assert.equal(bare.entries.length, 0)
})

test('normalizeCatalog synthesizes library rows for entries without one', () => {
  const c = normalizeCatalog({
    catalog: {
      entries: [
        {id: 'std/text.trim', library: 'std/text', alias: 'text', name: 'trim', patterns: ['trim VALUE', 'text.trim VALUE'], origin: 'import', enabled: true},
        {id: 'a/b.op', library: 'a/b', name: 'op', patterns: ['b.op VALUE'], origin: 'local /w/b', enabled: false}
      ],
      libraries: []
    }
  })
  const byPath = new Map(c.libraries.map(l => [l.path, l]))
  assert.deepEqual(byPath.get('std/text'), {path: 'std/text', alias: 'text', bare: true, origin: 'import'})
  assert.deepEqual(byPath.get('a/b'), {path: 'a/b', alias: 'b', bare: false, origin: 'local /w/b'})
  // and the enabled view works off synthesized rows
  assert.deepEqual(enabledView(c).libraries.map(l => l.path), ['std/text'])
})

test('tokenize lowercases and drops empties', () => {
  assert.deepEqual(tokenize('  Trim   TEXT '), ['trim', 'text'])
  assert.deepEqual(tokenize(''), [])
})

test('searchEntries matches word, description, library, patterns, and synonyms', () => {
  const c = catalog()
  assert.deepEqual(searchEntries(c.entries, 'trim').map(a => a.name), ['trim', 'clip'])
  assert.deepEqual(searchEntries(c.entries, 'whitespace').map(a => a.name), ['trim'])
  assert.deepEqual(searchEntries(c.entries, 'std/json').map(a => a.name), ['decode'])
  assert.deepEqual(searchEntries(c.entries, 'json.decode VALUE').map(a => a.name), ['decode'])
  assert.deepEqual(searchEntries(c.entries, 'STRIP').map(a => a.name), ['trim'])
  assert.equal(searchEntries(c.entries, '').length, 4)
})

test('searchEntries ANDs tokens so queries narrow', () => {
  const c = catalog()
  assert.deepEqual(searchEntries(c.entries, 'upper text').map(a => a.name), ['upper'])
  assert.deepEqual(searchEntries(c.entries, 'upper media').map(a => a.name), [])
})

test('searchEntries ranks name prefixes above description substrings, stably', () => {
  const entries = [
    {name: 'readme', library: 'a', description: 'mentions clip'},
    {name: 'clip', library: 'b', description: ' unrelated'}
  ]
  assert.deepEqual(searchEntries(entries, 'clip').map(a => a.name), ['clip', 'readme'])
  assert.deepEqual(searchEntries(entries, 'clip').map(a => a.library), ['b', 'a'])
})

test('searchLibraries matches path, alias, and provenance', () => {
  const c = catalog()
  assert.deepEqual(searchLibraries(c.libraries, 'demo').map(l => l.path), ['example.com/demo/media'])
  assert.deepEqual(searchLibraries(c.libraries, 'standard').map(l => l.path), ['std/json'])
  assert.deepEqual(searchLibraries(c.libraries, 'text').map(l => l.path), ['std/text'])
})

test('libraryEnabled follows core origins: import and config are live', () => {
  assert.equal(libraryEnabled({origin: 'import'}), true)
  assert.equal(libraryEnabled({origin: 'config /w/sos.toml'}), true)
  assert.equal(libraryEnabled({origin: 'standard library'}), false)
  assert.equal(libraryEnabled({origin: 'local /w/media'}), false)
})

test('enabledView keeps enabled entries, live libraries, and diagnostics', () => {
  const v = enabledView(catalog())
  assert.deepEqual(v.entries.map(a => a.name), ['trim', 'upper'])
  assert.deepEqual(v.libraries.map(l => l.path), ['std/text'])
  assert.equal(v.diagnostics.length, 1)
})

test('libraryView groups entries per library and carries sample words', () => {
  const groups = libraryView(catalog(), '')
  assert.deepEqual(groups.map(g => g.library.path), ['std/text', 'example.com/demo/media', 'std/json'])
  assert.deepEqual(groups[0].entries.map(a => a.name), ['trim', 'upper'])
  assert.deepEqual(groups[0].sampleWords, ['trim', 'upper'])
  assert.deepEqual(groups[1].sampleWords, ['clip'])
})

test('libraryView search keeps library or entry matches and drops the rest', () => {
  assert.deepEqual(libraryView(catalog(), 'clip').map(g => g.library.path), ['example.com/demo/media'])
  assert.deepEqual(libraryView(catalog(), 'demo').map(g => g.library.path), ['example.com/demo/media'])
})

test('importPreview states the agreed bare and prefixed forms', () => {
  const c = catalog()
  const p = importPreview(c.libraries[1], ['clip'])
  assert.equal(p.open, 'import "example.com/demo/media" exposes the bare vocabulary (clip) and media. qualified calls')
  assert.equal(p.aliased, 'import "example.com/demo/media" as media requires the media. prefix')
})

test('entrySignature renders params and result', () => {
  const c = catalog()
  assert.equal(entrySignature(c.entries[2]), 'upper(text as text) → text')
  assert.equal(entrySignature(c.entries[3]), 'clip(video as any, start as duration) → any')
  assert.equal(entrySignature({name: 'noop'}), 'noop()')
})

test('needsJev detects provider budget entries', () => {
  const c = catalog()
  assert.equal(needsJev(c.entries[3]), true)
  assert.equal(needsJev(c.entries[2]), false)
  assert.equal(needsJev({effects: []}), false)
})

test('diagnosticsFor scopes to a library when given', () => {
  const c = catalog()
  assert.equal(diagnosticsFor(c.diagnostics, '').length, 1)
  assert.equal(diagnosticsFor(c.diagnostics, 'std/text').length, 1)
  assert.equal(diagnosticsFor(c.diagnostics, 'std/nope').length, 0)
})
