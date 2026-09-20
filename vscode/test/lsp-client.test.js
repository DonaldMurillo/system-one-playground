const assert = require('node:assert/strict')
const test = require('node:test')

const { LspMessageParser, frameMessage } = require('../lsp-client')

test('parses a complete framed JSON-RPC message', () => {
  const parser = new LspMessageParser()
  const message = { jsonrpc: '2.0', id: 7, result: { ok: true } }

  assert.deepEqual(parser.push(frameMessage(message)), [message])
})

test('parses split frames and multiple messages', () => {
  const parser = new LspMessageParser()
  const first = frameMessage({ jsonrpc: '2.0', method: 'initialized', params: {} })
  const second = frameMessage({ jsonrpc: '2.0', id: 2, result: null })
  const all = Buffer.concat([first, second])
  const split = Math.floor(all.length / 2)

  assert.deepEqual(parser.push(all.subarray(0, split)), [])
  assert.deepEqual(parser.push(all.subarray(split)), [
    { jsonrpc: '2.0', method: 'initialized', params: {} },
    { jsonrpc: '2.0', id: 2, result: null },
  ])
})

test('uses UTF-8 byte length for non-ASCII JSON', () => {
  const parser = new LspMessageParser()
  const message = { jsonrpc: '2.0', method: 'textDocument/didOpen', params: { text: 'make greeting "こんにちは"' } }
  const frame = frameMessage(message)
  const headerEnd = frame.indexOf(Buffer.from('\r\n\r\n'))
  const header = frame.subarray(0, headerEnd).toString('ascii')
  const declared = Number(/Content-Length: (\d+)/.exec(header)[1])
  const body = frame.subarray(headerEnd + 4)

  assert.equal(declared, body.length)
  assert.deepEqual(parser.push(frame), [message])
})

