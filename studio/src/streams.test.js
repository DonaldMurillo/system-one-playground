import test from 'node:test'
import assert from 'node:assert/strict'
import { streamCanStop, streamElapsed, streamProducer, streamSubtitle, streamSummary } from './streams.js'

test('active stream summaries use readable live metrics', () => {
  assert.equal(streamCanStop({state:'reading'}), true)
  assert.equal(streamCanStop({state:'completed'}), false)
  assert.equal(streamSummary({itemsReceived:2, itemsBuffered:1, creditAvailable:3}), '2 items received · 1 buffered · 3 credit')
  assert.equal(
    streamSummary({policy:{keys:2, max_keys:4, pending:1, timers:2}}),
    '0 items received · 0 buffered · 0 credit · 2 keys (4 max) · 1 pending · 2 timers')
  assert.equal(streamElapsed({startedAt:'2026-01-01T00:00:00Z'}, Date.parse('2026-01-01T00:00:01.25Z')), '1.3 s')
})

test('terminal summaries surface the stream reason', () => {
  assert.equal(
    streamSummary({itemsReceived:1, state:'cancelled', reason:'run cancelled'}),
    '1 item received · 0 buffered · 0 credit · reason: run cancelled')
  assert.equal(streamSummary({state:'reading', reason:'stop requested'}), '0 items received · 0 buffered · 0 credit')
  assert.equal(streamSummary({state:'failed'}), '0 items received · 0 buffered · 0 credit')
})

test('derived producers split into source and operation', () => {
  assert.equal(streamProducer({producer:'events/map'}), 'events → map')
  assert.equal(streamProducer({producer:'events'}), 'events')
  assert.equal(streamProducer({}), '')
})

test('subtitles carry producer, item type, binding, and source line', () => {
  assert.equal(
    streamSubtitle({producer:'events/map', itemType:'text', binding:'mapped', line:4}),
    'events → map · stream of text · binding: mapped · line 4')
  assert.equal(streamSubtitle({itemType:'text', line:2}), 'stream of text · line 2')
  assert.equal(streamSubtitle({binding:'raw'}), 'binding: raw')
})
