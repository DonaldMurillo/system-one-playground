const assert = require('node:assert/strict')
const { SaxesParser } = require('saxes')

function parseIdentity (xml) {
  const manifestNamespace = 'http://schemas.microsoft.com/developer/vsx-schema/2011'
  const identities = []
  const stack = []
  let metadataElements = 0
  const parser = new SaxesParser({ xmlns: true })

  parser.on('opentag', tag => {
    if (stack.length === 0) {
      assert.equal(tag.local, 'PackageManifest', 'VSIX manifest root must be PackageManifest')
      assert.equal(tag.uri, manifestNamespace, 'VSIX manifest root must use the VSX 2011 namespace')
    }
    if (tag.local === 'Metadata' && stack.length === 1 && stack[0] === 'PackageManifest') {
      assert.equal(tag.uri, manifestNamespace, 'VSIX Metadata must use the VSX 2011 namespace')
      metadataElements++
    }
    if (tag.local === 'Identity') {
      assert.deepEqual(stack, ['PackageManifest', 'Metadata'], 'VSIX Identity must be a direct child of PackageManifest/Metadata')
      assert.equal(tag.uri, manifestNamespace, 'VSIX Identity must use the VSX 2011 namespace')
      assert.ok(tag.isSelfClosing, 'VSIX Identity element must be self-closing')
      const attributes = {}
      for (const attribute of Object.values(tag.attributes)) {
        assert.equal(attribute.uri, '', `Identity attribute ${attribute.name} must not be namespaced`)
        assert.ok(!(attribute.local in attributes), `duplicate Identity attribute ${attribute.local}`)
        attributes[attribute.local] = attribute.value
      }
      identities.push(attributes)
    }
    stack.push(tag.local)
  })
  parser.on('closetag', () => stack.pop())
  parser.write(xml).close()

  assert.equal(stack.length, 0, 'VSIX manifest has unclosed XML elements')
  assert.equal(metadataElements, 1, 'VSIX manifest must contain exactly one direct Metadata element')
  assert.equal(identities.length, 1, 'VSIX manifest must contain exactly one effective Identity element')
  return identities[0]
}

module.exports = { parseIdentity }
