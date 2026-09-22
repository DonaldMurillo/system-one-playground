'use strict'
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')
const assert = require('node:assert/strict')
const { timeSummary } = require('../streams')

test('fallback grammar recognizes time sentence keywords', () => {
  const grammar = fs.readFileSync(path.join(__dirname, '..', 'syntaxes', 'sos.tmLanguage.json'), 'utf8')
  for (const word of ['wait', 'allow', 'advance', 'until', 'every', 'tick', 'scheduled', 'zone', 'combining', 'skipping', 'catching']) {
    assert.match(grammar, new RegExp(`\\b${word}\\b`), word)
  }
})

test('time summary surfaces clock, schedule, and missed-tick observability', () => {
  assert.equal(
    timeSummary({clockKind:'virtual', virtualTime:'2026-01-01T00:10:00Z', interval:'10 seconds', nextScheduledAt:'2026-01-01T00:10:10Z', missedTicks:2, combinedTicks:1}),
    'virtual clock at 2026-01-01T00:10:00Z · every 10 seconds · next 2026-01-01T00:10:10Z · 2 missed · 1 combined'
  )
  assert.equal(
    timeSummary({clockKind:'host', schedule:'every Monday at 8:00', timeZone:'Europe/London', skippedTicks:3, deadlineRemaining:'5 seconds'}),
    'host clock · every Monday at 8:00 · Europe/London · 3 skipped · 5 seconds of deadline left'
  )
  assert.equal(timeSummary({itemsReceived:4}), '')
  assert.equal(timeSummary(null), '')
})
