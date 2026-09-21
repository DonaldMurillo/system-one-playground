const assert = require('node:assert/strict')

function parseIdentity (xml) {
  const identities = []
  let cursor = 0
  while (cursor < xml.length) {
    const open = xml.indexOf('<', cursor)
    if (open < 0) break
    if (xml.startsWith('<!--', open)) {
      cursor = skipDelimited(xml, open + 4, '-->', 'XML comment')
      continue
    }
    if (xml.startsWith('<![CDATA[', open)) {
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
    if (!tag || tag.startsWith('/')) continue
    const name = tag.match(/^([A-Za-z_:][\w:.-]*)/)
    assert.ok(name, 'malformed XML tag in VSIX manifest')
    if (name[1] === 'Identity') {
      assert.ok(tag.endsWith('/'), 'VSIX Identity element must be self-closing')
      identities.push(parseAttributes(tag.slice(name[0].length)))
    }
  }
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
