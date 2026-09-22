import test from 'node:test'
import assert from 'node:assert/strict'
import { streamCanStop, streamElapsed, streamSummary, streamTimeDetail } from './streams.js'

test('active stream summaries use readable live metrics', () => {
  assert.equal(streamCanStop({state:'reading'}), true)
  assert.equal(streamCanStop({state:'completed'}), false)
  assert.equal(streamSummary({itemsReceived:2, itemsBuffered:1, creditAvailable:3}), '2 items received · 1 buffered · 3 credit')
  assert.equal(streamElapsed({startedAt:'2026-01-01T00:00:00Z'}, Date.parse('2026-01-01T00:00:01.25Z')), '1.3 s')
})

test('timer streams surface time observability fields when present', () => {
  assert.equal(streamTimeDetail({clockKind:'virtual', virtualTime:'2026-01-01T00:10:00Z', interval:'10 seconds', nextScheduledAt:'2026-01-01T00:10:10Z', missedTicks:2, combinedTicks:1}), 'virtual clock at 2026-01-01T00:10:00Z · every 10 seconds · next 2026-01-01T00:10:10Z · 2 missed · 1 combined')
  assert.equal(streamTimeDetail({clockKind:'host', schedule:'every weekday at 9:00', timeZone:'America/New_York', deadlineRemaining:'12 seconds'}), 'host clock · every weekday at 9:00 · America/New_York · 12 seconds of deadline left')
  assert.equal(streamTimeDetail({itemsReceived:3}), '')
  assert.equal(streamTimeDetail(null), '')
})
