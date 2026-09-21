const assert = require('node:assert/strict')
const test = require('node:test')

const { parseIdentity } = require('./vsix-identity')

test('parses exactly one effective self-closing Identity element', () => {
  const identity = parseIdentity(`<?xml version="1.0"?>
    <PackageManifest xmlns="http://schemas.microsoft.com/developer/vsx-schema/2011">
      <Metadata>
      <!-- <Identity Id="comment-decoy" /> -->
      <![CDATA[<Identity Id="cdata-decoy" />]]>
      <Identity Language="en-US" Id="real" Version='1.2.3' Publisher="owner" />
      </Metadata>
    </PackageManifest>`)
  assert.deepEqual(identity, { Language: 'en-US', Id: 'real', Version: '1.2.3', Publisher: 'owner' })
})

for (const [name, xml] of [
  ['duplicate elements', '<PackageManifest><Metadata><Identity Id="one" /><Identity Id="two" /></Metadata></PackageManifest>'],
  ['duplicate attributes', '<PackageManifest><Metadata><Identity Id="one" Id="two" /></Metadata></PackageManifest>'],
  ['non-self-closing identity', '<PackageManifest><Metadata><Identity Id="one"></Identity></Metadata></PackageManifest>'],
  ['malformed trailing content', '<PackageManifest><Metadata><Identity Id="one" surprise /></Metadata></PackageManifest>'],
  ['unterminated comment', '<PackageManifest><!-- <Identity Id="one" />'],
  ['mismatched closing element', '<PackageManifest><Metadata><Identity Id="one" /></Wrong></PackageManifest>'],
  ['unclosed root', '<PackageManifest><Metadata><Identity Id="one" /></Metadata>'],
  ['identity outside metadata', '<PackageManifest><Identity Id="one" /></PackageManifest>'],
  ['multiple roots', '<PackageManifest><Metadata><Identity Id="one" /></Metadata></PackageManifest><PackageManifest />'],
  ['CDATA outside root', '<![CDATA[bad]]><PackageManifest><Metadata><Identity Id="one" /></Metadata></PackageManifest>'],
  ['malformed parent attribute', '<PackageManifest bogus><Metadata><Identity Id="one" /></Metadata></PackageManifest>'],
  ['invalid comment', '<PackageManifest><Metadata><Identity Id="one" /></Metadata></PackageManifest><!-- bad--comment -->'],
  ['undefined entity', '<PackageManifest><Metadata>&bogus;<Identity Id="one" /></Metadata></PackageManifest>'],
  ['unescaped attribute content', '<PackageManifest><Metadata><Identity Id="one&two" /></Metadata></PackageManifest>']
]) {
  test(`rejects ${name}`, () => assert.throws(() => parseIdentity(xml.replace('<PackageManifest', '<PackageManifest xmlns="http://schemas.microsoft.com/developer/vsx-schema/2011"'))))
}

test('rejects the wrong manifest namespace', () => {
  assert.throws(() => parseIdentity('<PackageManifest xmlns="urn:wrong"><Metadata><Identity Id="one" /></Metadata></PackageManifest>'))
})

test('rejects duplicate Metadata elements', () => {
  assert.throws(() => parseIdentity('<PackageManifest xmlns="http://schemas.microsoft.com/developer/vsx-schema/2011"><Metadata><Identity Id="one" /></Metadata><Metadata /></PackageManifest>'))
})
