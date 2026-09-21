const assert = require('node:assert/strict')
const { SaxesParser } = require('saxes')

function parseIdentity (xml) {
  const identities = []
  const stack = []
  const parser = new SaxesParser({ xmlns: false })

  parser.on('opentag', tag => {
    if (tag.name === 'Identity') {
      assert.deepEqual(stack, ['PackageManifest', 'Metadata'], 'VSIX Identity must be a direct child of PackageManifest/Metadata')
      assert.ok(tag.isSelfClosing, 'VSIX Identity element must be self-closing')
      identities.push(Object.fromEntries(Object.entries(tag.attributes)))
    }
    stack.push(tag.name)
  })
  parser.on('closetag', () => stack.pop())
  parser.write(xml).close()

  assert.equal(stack.length, 0, 'VSIX manifest has unclosed XML elements')
  assert.equal(identities.length, 1, 'VSIX manifest must contain exactly one effective Identity element')
  return identities[0]
}

module.exports = { parseIdentity }
