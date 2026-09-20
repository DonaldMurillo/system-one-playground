import test from 'node:test'
import assert from 'node:assert/strict'
import { usageLine } from './usage.js'

test('usage estimates only reported input tokens and exposes unresolved calls', () => {
  const text = usageLine({totalAdmitted:3,totalLimit:8,buckets:{interpretation:{requests:1,limit:8,reportedInputTokens:1000,unresolved:0},runtime:{requests:2,limit:8,reportedInputTokens:2000,unresolved:1}}})
  assert.match(text,/requests 3 of 8/)
  assert.match(text,/\$0\.00012600 USD \+ unknown usage/)
  assert.match(text,/not an invoice/)
})
test('missing usage is not presented as zero billing', () => {
  assert.equal(usageLine(null),'')
  assert.match(usageLine({totalAdmitted:1,buckets:{runtime:{requests:1,unresolved:1}}}),/unknown usage/)
})
