import test from 'node:test'
import assert from 'node:assert/strict'
import { streamCanStop, streamElapsed, streamSummary } from './streams.js'

test('active stream summaries use readable live metrics', () => {
  assert.equal(streamCanStop({state:'reading'}), true)
  assert.equal(streamCanStop({state:'completed'}), false)
  assert.equal(streamSummary({itemsReceived:2, itemsBuffered:1, creditAvailable:3}), '2 items received · 1 buffered · 3 credit')
  assert.equal(streamElapsed({startedAt:'2026-01-01T00:00:00Z'}, Date.parse('2026-01-01T00:00:01.25Z')), '1.3 s')
})
