const assert = require('node:assert/strict')

function parseIdentity (xml) {
  const identities = []
  const stack = []
  let roots = 0
  let cursor = 0
  while (cursor < xml.length) {
    const open = xml.indexOf('<', cursor)
    const textEnd = open < 0 ? xml.length : open
    if (stack.length === 0) assert.equal(xml.slice(cursor, textEnd).trim(), '', 'text is not allowed outside the XML root')
    if (open < 0) break
    if (xml.startsWith('<!--', open)) {
      cursor = skipDelimited(xml, open + 4, '-->', 'XML comment')
      continue
    }
    if (xml.startsWith('<![CDATA[', open)) {
      assert.ok(stack.length > 0, 'CDATA is not allowed outside the XML root')
      cursor = skipDelimited(xml, open + 9, ']]>', 'CDATA section')
      continue
    }
    if (xml.startsWith('<?', open)) {
      cursor = skipDelimited(xml, open + 2, '?>', 'processing instruction')
      continue
    }
    assert.ok(!xml.startsWith('<!', open), 'unsupported XML declaration in VSIX manifest')

    let quote = null
    let close = open + 1
    for (; close < xml.length; close++) {
      const character = xml[close]
      if (quote) {
        if (character === quote) quote = null
      } else if (character === '"' || character === "'") {
        quote = character
      } else if (character === '>') {
        break
      }
    }
    assert.ok(close < xml.length && quote === null, 'unterminated XML tag in VSIX manifest')
    const tag = xml.slice(open + 1, close).trim()
    cursor = close + 1
    assert.ok(tag, 'empty XML tag in VSIX manifest')
    if (tag.startsWith('/')) {
      const closing = tag.slice(1).trim()
      assert.match(closing, /^[A-Za-z_:][\w:.-]*$/, 'malformed closing XML tag')
      assert.equal(stack.pop(), closing, `mismatched closing XML tag ${closing}`)
      continue
    }
    const name = tag.match(/^([A-Za-z_:][\w:.-]*)/)
    assert.ok(name, 'malformed XML tag in VSIX manifest')
    const selfClosing = tag.endsWith('/')
    const attributes = parseAttributes(tag.slice(name[0].length))
    if (stack.length === 0) {
      roots++
      assert.equal(name[1], 'PackageManifest', 'VSIX manifest root must be PackageManifest')
    }
    if (name[1] === 'Identity') {
      assert.deepEqual(stack, ['PackageManifest', 'Metadata'], 'VSIX Identity must be a direct child of PackageManifest/Metadata')
      assert.ok(selfClosing, 'VSIX Identity element must be self-closing')
      identities.push(attributes)
    }
    if (!selfClosing) stack.push(name[1])
  }
  assert.equal(stack.length, 0, 'VSIX manifest has unclosed XML elements')
  assert.equal(roots, 1, 'VSIX manifest must have exactly one root element')
  assert.equal(identities.length, 1, 'VSIX manifest must contain exactly one effective Identity element')
  return identities[0]
}

function skipDelimited (text, start, delimiter, label) {
  const end = text.indexOf(delimiter, start)
  assert.notEqual(end, -1, `unterminated ${label} in VSIX manifest`)
  return end + delimiter.length
}

function parseAttributes (source) {
  const attributes = {}
  let cursor = 0
  while (cursor < source.length) {
    while (/\s/.test(source[cursor] || '')) cursor++
    if (cursor === source.length || source[cursor] === '/') {
      if (source[cursor] === '/') cursor++
      while (/\s/.test(source[cursor] || '')) cursor++
      assert.equal(cursor, source.length, 'malformed trailing content in Identity element')
      break
    }
    const name = source.slice(cursor).match(/^([A-Za-z_:][\w:.-]*)/)
    assert.ok(name, 'malformed Identity attribute name')
    cursor += name[0].length
    while (/\s/.test(source[cursor] || '')) cursor++
    assert.equal(source[cursor], '=', `Identity attribute ${name[1]} is missing =`)
    cursor++
    while (/\s/.test(source[cursor] || '')) cursor++
    const quote = source[cursor]
    assert.ok(quote === '"' || quote === "'", `Identity attribute ${name[1]} must be quoted`)
    cursor++
    const end = source.indexOf(quote, cursor)
    assert.notEqual(end, -1, `unterminated Identity attribute ${name[1]}`)
    assert.ok(!(name[1] in attributes), `duplicate Identity attribute ${name[1]}`)
    attributes[name[1]] = source.slice(cursor, end)
    cursor = end + 1
  }
  return attributes
}

module.exports = { parseIdentity }
