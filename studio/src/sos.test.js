import {test} from 'node:test'
import assert from 'node:assert/strict'
import {SENTENCE_STARTERS, CONNECTORS, SOS_KEYWORDS} from './sos.js'

test('offline highlighting recognizes the complete stream lifecycle syntax', () => {
  for (const word of ['stream', 'streaming', 'send', 'close', 'collect']) assert.ok(SENTENCE_STARTERS.includes(word), word)
  assert.ok(CONNECTORS.includes('reading'))
  for (const word of ['from', 'at', 'most', 'first', 'items']) assert.ok(SOS_KEYWORDS.includes(word), word)
})
