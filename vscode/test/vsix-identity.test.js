const assert = require('node:assert/strict')
const test = require('node:test')

const { parseIdentity } = require('./vsix-identity')

test('parses exactly one effective self-closing Identity element', () => {
  const identity = parseIdentity(`<?xml version="1.0"?>
    <Package>
      <!-- <Identity Id="comment-decoy" /> -->
      <![CDATA[<Identity Id="cdata-decoy" />]]>
      <Identity Language="en-US" Id="real" Version='1.2.3' Publisher="owner" />
    </Package>`)
  assert.deepEqual(identity, { Language: 'en-US', Id: 'real', Version: '1.2.3', Publisher: 'owner' })
})

for (const [name, xml] of [
  ['duplicate elements', '<Identity Id="one" /><Identity Id="two" />'],
  ['duplicate attributes', '<Identity Id="one" Id="two" />'],
  ['non-self-closing identity', '<Identity Id="one"></Identity>'],
  ['malformed trailing content', '<Identity Id="one" surprise />'],
  ['unterminated comment', '<!-- <Identity Id="one" />']
]) {
  test(`rejects ${name}`, () => assert.throws(() => parseIdentity(xml)))
}
