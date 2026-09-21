const assert = require('node:assert/strict')
const test = require('node:test')
const { decisionLensTitle } = require('../semantic')

test('attributes confidence and shared batched Jev cost in code lenses', () => {
  assert.equal(decisionLensTitle({method:'jev', confidence:.97, usage_known:true, input_tokens:721, usage_shared:true, batch_size:8}), 'Jev · 97% confidence · 721 tokens · ~$0.00003028 shared across 8 lines')
})

test('memoized decisions report no new request cost', () => {
  assert.equal(decisionLensTitle({method:'memoized', confidence:.98}), 'Jev memoized · 98% confidence · 0 new requests · $0.00000000')
})
