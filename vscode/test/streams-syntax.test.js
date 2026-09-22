const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')

test('fallback grammar recognizes stream lifecycle syntax', () => {
  const grammar = fs.readFileSync(path.join(__dirname, '..', 'syntaxes', 'sos.tmLanguage.json'), 'utf8')
  for (const word of ['streaming', 'stream', 'close', 'collect', 'reading']) {
    assert.match(grammar, new RegExp(`\\b${word}\\b`), word)
  }
})
